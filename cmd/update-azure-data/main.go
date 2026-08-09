// Command update-azure-data refreshes the committed Azure price catalogue from
// the sources its approved contract enumerates: the anonymous Azure Retail
// Prices API for amounts, and Microsoft Learn size pages for specifications and
// processor architecture.
//
// It reads no credentials and writes nothing until every source has been
// fetched, parsed, joined, and validated against the contract and the previous
// coverage floor, so a failure before that point leaves the reviewed snapshot
// exactly as it was. The payload and the manifest are then two separate atomic
// renames: a failure between them leaves the new payload under the old
// manifest, which fails closed at load and in `make verify-data` rather than
// being served.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"time"

	"spotinfo/internal/cloud"
	"spotinfo/internal/providers/azure"
	"spotinfo/internal/snapshot"
)

const (
	// File names inside the data directory. They are also what the manifest and
	// the provider's embed directives name.
	contractFile = "source-contract.json"
	manifestFile = "manifest.json"
	payloadFile  = "catalog.json.gz"

	defaultDataDir = "internal/providers/azure/data"
	// userAgent identifies these requests. An anonymous scraper with no contact
	// string is what gets blocked first.
	userAgent = "spotinfo-data-updater/1.0 (+https://github.com/alexei-led/spotinfo)"

	defaultTimeout = 15 * time.Minute
	// maxPageBytes bounds one downloaded document. A Retail API page is under
	// 2 MB and a size page under 1 MB; the ceiling stops a redirect loop or a
	// hostile response from filling memory.
	maxPageBytes = 64 << 20
	// maxPagesPerRegion bounds pagination. A region returns eight to ten pages
	// today; the ceiling stops a NextPageLink cycle from looping forever.
	maxPagesPerRegion = 40
	// fetchAttempts is the total number of tries per request.
	fetchAttempts = 3
	// retryPause is the wait between attempts.
	retryPause = 3 * time.Second

	// gzipLevel is maximum compression: the payload is committed once a week and
	// read on every process start.
	gzipLevel = gzip.BestCompression
	// gzipOSUnknown is the only gzip OS byte that does not depend on the host
	// that produced the file. Go's writer already defaults to it and to a zero
	// modification time, so setting the header changes no byte today; it is
	// written explicitly so reproducibility does not rest on that default.
	gzipOSUnknown = 255

	// pricesPerMachine is the two classes every catalogue row publishes.
	pricesPerMachine = 2
)

func main() {
	if err := refresh(); err != nil {
		fmt.Fprintf(os.Stderr, "update-azure-data: %v\n", err)
		os.Exit(1)
	}
}

func refresh() error {
	dataDir := flag.String("data-dir", defaultDataDir,
		"directory holding the Azure source contract, catalogue and manifest")
	timeout := flag.Duration("timeout", defaultTimeout, "overall deadline for the refresh")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	return run(ctx, *dataDir)
}

func run(ctx context.Context, dataDir string) error {
	contract, err := loadContract(dataDir)
	if err != nil {
		return err
	}

	// One instant resolves every region, so a refresh cannot mix a price that
	// expired halfway through the sweep with its replacement.
	at := time.Now().UTC()

	client := &http.Client{}

	series, specSources, err := fetchSeries(ctx, client, contract)
	if err != nil {
		return err
	}

	items, priceSources, err := fetchPrices(ctx, client, contract)
	if err != nil {
		return err
	}

	catalog, err := buildCatalog(contract, series, items, at)
	if err != nil {
		return err
	}

	payload, err := encodePayload(catalog)
	if err != nil {
		return err
	}

	floor, err := coverageFloor(dataDir, contract)
	if err != nil {
		return err
	}

	sources := append(priceSources, specSources...) //nolint:gocritic // a new slice is intended.
	manifest := newManifest(contract, catalog, payload, sources, floor)

	if err := verify(contract, catalog, manifest, payload); err != nil {
		return err
	}

	if err := snapshot.WriteFile(filepath.Join(dataDir, payloadFile), payload); err != nil {
		return err
	}
	if err := snapshot.WriteManifest(filepath.Join(dataDir, manifestFile), manifest); err != nil {
		return err
	}

	fmt.Printf("azure catalogue: %d machines, %d series, %d regions, %d prices, %d compressed bytes\n",
		len(catalog.Machines), len(catalog.Series()), len(catalog.Regions),
		catalog.Coverage().Prices, len(payload))

	return nil
}

func loadContract(dataDir string) (*snapshot.SourceContract, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, contractFile)) //nolint:gosec // the path is an operator-supplied data directory.
	if err != nil {
		return nil, fmt.Errorf("read azure source contract: %w", err)
	}

	contract, err := snapshot.ParseSourceContract(data)
	if err != nil {
		return nil, err
	}
	if contract.Provider != cloud.ProviderAzure {
		return nil, fmt.Errorf("%w: %s is not an azure contract", snapshot.ErrInvalidSourceContract, contract.Provider)
	}
	if contract.ParserVersion != azure.ParserVersion {
		return nil, fmt.Errorf("%w: contract approves parser %q, this binary is %q",
			snapshot.ErrContractMismatch, contract.ParserVersion, azure.ParserVersion)
	}

	return contract, nil
}

// fetchSeries downloads every contracted size page and reads its specifications
// and architecture. Every approved series must produce a page: a missing one
// would silently drop its sizes from the catalogue.
func fetchSeries(ctx context.Context, client *http.Client, contract *snapshot.SourceContract,
) ([]azure.SeriesSpec, []snapshot.Source, error) {
	var (
		specs   []azure.SeriesSpec
		sources []snapshot.Source
		covered []string
	)

	for i := range contract.Sources {
		source := &contract.Sources[i]
		if !slices.Contains(source.DataKinds, snapshot.DataKindMachineSpec) {
			continue
		}

		series, err := azure.SeriesFromURL(source.URL)
		if err != nil {
			return nil, nil, err
		}

		fetchedAt := time.Now().UTC()

		body, err := fetch(ctx, client, source.URL)
		if err != nil {
			return nil, nil, err
		}

		spec, err := azure.ParseSeriesPage(bytes.NewReader(body), series)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", source.URL, err)
		}

		specs = append(specs, *spec)
		covered = append(covered, series)
		sources = append(sources, snapshot.Source{
			URL:       source.URL,
			FetchedAt: fetchedAt,
			SHA256:    snapshot.SHA256Hex(body),
		})
	}

	for _, series := range contract.Support.MachineSeries {
		if !slices.Contains(covered, series) {
			return nil, nil, fmt.Errorf("%w: series %q is approved but no source page documents it",
				snapshot.ErrInvalidSourceContract, series)
		}
	}

	return specs, sources, nil
}

// fetchPrices sweeps the Retail Prices API once per approved region, following
// NextPageLink until the region is exhausted.
func fetchPrices(ctx context.Context, client *http.Client, contract *snapshot.SourceContract,
) ([]azure.RetailItem, []snapshot.Source, error) {
	base, err := priceAPIBase(contract)
	if err != nil {
		return nil, nil, err
	}

	var (
		items   []azure.RetailItem
		sources []snapshot.Source
	)

	for _, region := range contract.Support.Regions {
		first, err := azure.RetailRequestURL(base, region)
		if err != nil {
			return nil, nil, err
		}

		fetchedAt := time.Now().UTC()

		regionItems, bodies, err := sweepRegion(ctx, client, first)
		if err != nil {
			return nil, nil, fmt.Errorf("region %s: %w", region, err)
		}

		items = append(items, regionItems...)
		sources = append(sources, snapshot.Source{
			URL:       first,
			FetchedAt: fetchedAt,
			SHA256:    snapshot.SHA256Hex(bodies),
		})
	}

	return items, sources, nil
}

// sweepRegion follows one region's pagination and returns its rows together
// with the concatenated response bytes, which is what the manifest hashes.
func sweepRegion(ctx context.Context, client *http.Client, first string) ([]azure.RetailItem, []byte, error) {
	var (
		items  []azure.RetailItem
		bodies bytes.Buffer
		next   = first
	)

	for pages := 0; next != ""; pages++ {
		if pages == maxPagesPerRegion {
			return nil, nil, fmt.Errorf("pagination did not terminate within %d pages", maxPagesPerRegion)
		}

		if err := sameHost(first, next); err != nil {
			return nil, nil, err
		}

		body, err := fetch(ctx, client, next)
		if err != nil {
			return nil, nil, err
		}
		bodies.Write(body)

		page, err := azure.DecodeRetailPage(bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}

		items = append(items, page.Items...)
		next = page.NextPageLink
	}

	return items, bodies.Bytes(), nil
}

// sameHost keeps pagination inside the contracted service. NextPageLink is
// supplied by the response, so following it anywhere would let one compromised
// reply redirect the refresh at another host.
func sameHost(first, next string) error {
	left, err := url.Parse(first)
	if err != nil {
		return fmt.Errorf("parse request url: %w", err)
	}

	right, err := url.Parse(next)
	if err != nil {
		return fmt.Errorf("parse next page link: %w", err)
	}

	if right.Scheme != left.Scheme || right.Hostname() != left.Hostname() {
		return fmt.Errorf("next page link points at %s://%s, not the contracted %s://%s",
			right.Scheme, right.Hostname(), left.Scheme, left.Hostname())
	}

	return nil
}

// priceAPIBase is the one contracted source that supplies amounts.
func priceAPIBase(contract *snapshot.SourceContract) (string, error) {
	for i := range contract.Sources {
		source := &contract.Sources[i]
		if source.SourceType == snapshot.SourceTypeDocumentedRESTAPI &&
			slices.Contains(source.DataKinds, snapshot.DataKindSpotPrice) {
			return source.URL, nil
		}
	}

	return "", fmt.Errorf("%w: no contracted rest api supplies spot prices", snapshot.ErrInvalidSourceContract)
}

func fetch(ctx context.Context, client *http.Client, target string) ([]byte, error) {
	var lastErr error

	for attempt := 1; attempt <= fetchAttempts; attempt++ {
		body, err := fetchOnce(ctx, client, target)
		if err == nil {
			return body, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			break
		}
		if attempt < fetchAttempts {
			select {
			case <-ctx.Done():
			case <-time.After(retryPause):
			}
		}
	}

	return nil, fmt.Errorf("fetch %s: %w", target, lastErr)
}

func fetchOnce(ctx context.Context, client *http.Client, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept-Language", "en")

	response, err := client.Do(request)
	if err != nil {
		return nil, err //nolint:wrapcheck // wrapped once by the caller, with the url.
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxPageBytes))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) == maxPageBytes {
		return nil, errors.New("response hit the size ceiling and may be truncated")
	}

	return body, nil
}

func buildCatalog(contract *snapshot.SourceContract, series []azure.SeriesSpec,
	items []azure.RetailItem, at time.Time,
) (*azure.Catalog, error) {
	observations, err := azure.AcceptRows(items)
	if err != nil {
		return nil, err
	}

	rows, expired, err := azure.SelectCurrent(azure.RetainSpecified(observations, series), at)
	if err != nil {
		return nil, err
	}
	for _, key := range expired {
		fmt.Fprintf(os.Stderr, "skipped %s: no price interval in effect at %s\n", key, at.Format(time.RFC3339))
	}

	catalog, unpaired, err := azure.BuildCatalog(series, rows)
	if err != nil {
		return nil, err
	}
	for _, key := range unpaired {
		fmt.Fprintf(os.Stderr, "skipped %s: priced in only one class\n", key)
	}

	if err := catalog.Verify(contract); err != nil {
		return nil, err
	}

	return catalog, nil
}

// encodePayload gzips the catalogue with a zeroed header. The default header
// stamps the current time, which would give identical data a different hash on
// every run and make the manifest gate fire on a no-op refresh.
func encodePayload(catalog *azure.Catalog) ([]byte, error) {
	data, err := catalog.Encode()
	if err != nil {
		return nil, err
	}

	var buffer bytes.Buffer

	writer, err := gzip.NewWriterLevel(&buffer, gzipLevel)
	if err != nil {
		return nil, fmt.Errorf("create gzip writer: %w", err)
	}
	writer.Header = gzip.Header{OS: gzipOSUnknown}

	if _, err := writer.Write(data); err != nil {
		return nil, fmt.Errorf("compress catalogue: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish catalogue: %w", err)
	}

	return buffer.Bytes(), nil
}

func newManifest(contract *snapshot.SourceContract, catalog *azure.Catalog, payload []byte,
	sources []snapshot.Source, floor snapshot.Coverage,
) *snapshot.Manifest {
	return &snapshot.Manifest{
		SchemaVersion:     snapshot.ManifestSchemaVersion,
		DataSchemaVersion: catalog.SchemaVersion,
		ParserVersion:     contract.ParserVersion,
		Provider:          cloud.ProviderAzure,
		Kind:              snapshot.KindSpotPrice,
		Currency:          catalog.Currency,
		BillingUnit:       catalog.BillingUnit,
		Payload: snapshot.Payload{
			File:   payloadFile,
			Form:   snapshot.PayloadFormParsedCatalog,
			SHA256: snapshot.SHA256Hex(payload),
		},
		Sources:    sources,
		MinRecords: floor,
	}
}

// coverageFloor keeps the reviewed floor of an existing manifest. Deriving it
// from the data that just arrived would ratchet the gate into always passing;
// only a genuinely absent manifest — the first run — seeds it from the
// contracted minimum. An unreadable or unparseable manifest is an error: a
// reviewer's raised floor must never be replaced by the contract minimum
// because the file it lives in broke.
func coverageFloor(dataDir string, contract *snapshot.SourceContract) (snapshot.Coverage, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, manifestFile)) //nolint:gosec // the path is an operator-supplied data directory.
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return snapshot.Coverage{}, fmt.Errorf("read azure manifest for its reviewed coverage floor: %w", err)
		}

		return snapshot.Coverage{
			Regions:  contract.Thresholds.MinRegions,
			Machines: contract.Thresholds.MinMachines,
			Prices:   contract.Thresholds.MinRegions * contract.Thresholds.MinMachines * pricesPerMachine,
		}, nil
	}

	previous, err := snapshot.ParseManifest(data)
	if err != nil {
		return snapshot.Coverage{}, fmt.Errorf("parse azure manifest for its reviewed coverage floor: %w", err)
	}

	return previous.MinRecords, nil
}

func verify(contract *snapshot.SourceContract, catalog *azure.Catalog,
	manifest *snapshot.Manifest, payload []byte,
) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if err := contract.VerifySnapshot(manifest, len(payload)); err != nil {
		return err
	}

	return snapshot.ValidatePrices(catalog.PriceRecords(), manifest)
}
