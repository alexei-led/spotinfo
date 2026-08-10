// Command update-gcp-data refreshes the committed GCP price catalogue from the
// pages its approved source contract enumerates.
//
// It reads no credentials and writes nothing until every page has been fetched,
// parsed, joined, and validated against the contract and the previous coverage
// floor, so a failure before that point leaves the reviewed snapshot exactly as
// it was. The payload and manifest commit uses a rollback pair: if the second
// file cannot be replaced, the previous payload is restored and the update
// fails rather than leaving an unpaired snapshot.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"spotinfo/internal/cloud"
	"spotinfo/internal/providers/gcp"
	"spotinfo/internal/reproducible"
	"spotinfo/internal/snapshot"
)

const (
	// File names inside the data directory. They are also what the manifest and
	// the provider's embed directives name.
	contractFile = "source-contract.json"
	manifestFile = "manifest.json"
	payloadFile  = "catalog.json.gz"

	defaultDataDir = "internal/providers/gcp/data"
	// userAgent identifies these requests. An anonymous scraper with no contact
	// string is what gets blocked first.
	userAgent = "spotinfo-data-updater/1.0 (+https://github.com/alexei-led/spotinfo)"

	defaultTimeout = 5 * time.Minute
	// maxPageBytes bounds one downloaded page. The largest contracted page is
	// around 35 MB; the ceiling stops a redirect loop or a hostile response from
	// filling memory.
	maxPageBytes = 128 << 20
	// fetchAttempts is the total number of tries per page.
	fetchAttempts = 3
	// retryPause is the wait between attempts.
	retryPause = 3 * time.Second
)

func main() {
	if err := refresh(); err != nil {
		fmt.Fprintf(os.Stderr, "update-gcp-data: %v\n", err)
		os.Exit(1)
	}
}

func refresh() error {
	dataDir := flag.String("data-dir", defaultDataDir, "directory holding the GCP source contract, catalogue and manifest")
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

	region := contract.Support.Regions[0]

	pages, err := fetchSources(ctx, contract)
	if err != nil {
		return err
	}

	catalog, unpaired, err := buildCatalog(contract, region, pages)
	if err != nil {
		return err
	}
	for _, id := range unpaired {
		fmt.Fprintf(os.Stderr, "skipped %s: spot price with no on-demand pair\n", id)
	}

	payload, err := encodePayload(catalog)
	if err != nil {
		return err
	}

	floor, err := coverageFloor(dataDir, contract)
	if err != nil {
		return err
	}

	manifest := newManifest(contract, catalog, payload, pages, floor)
	if err := verify(contract, catalog, manifest, payload); err != nil {
		return err
	}

	if err := snapshot.WriteSnapshot(
		filepath.Join(dataDir, payloadFile), payload,
		filepath.Join(dataDir, manifestFile), manifest,
	); err != nil {
		return err
	}

	fmt.Printf("gcp catalogue: %d machines, %d series, region %s, %d compressed bytes\n",
		len(catalog.Machines), len(catalog.Series()), catalog.Region, len(payload))

	return nil
}

func loadContract(dataDir string) (*snapshot.SourceContract, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, contractFile)) //nolint:gosec // the path is an operator-supplied data directory.
	if err != nil {
		return nil, fmt.Errorf("read gcp source contract: %w", err)
	}

	contract, err := snapshot.ParseSourceContract(data)
	if err != nil {
		return nil, err
	}
	if contract.Provider != cloud.ProviderGCP {
		return nil, fmt.Errorf("%w: %s is not a gcp contract", snapshot.ErrInvalidSourceContract, contract.Provider)
	}
	if len(contract.Support.Regions) != 1 {
		return nil, fmt.Errorf("%w: the gcp pages render one region, contract claims %d",
			snapshot.ErrInvalidSourceContract, len(contract.Support.Regions))
	}
	if contract.ParserVersion != gcp.ParserVersion {
		return nil, fmt.Errorf("%w: contract approves parser %q, this binary is %q",
			snapshot.ErrContractMismatch, contract.ParserVersion, gcp.ParserVersion)
	}

	return contract, nil
}

// page is one downloaded source document with the provenance the manifest needs.
type page struct {
	fetchedAt time.Time
	url       string
	sha256    string
	body      []byte
	kinds     []snapshot.DataKind
}

// contractedHostClient refuses a redirect that leaves the contracted host. Go's
// default policy would follow a response-supplied 3xx Location anywhere, and
// the body it returned would then be hashed into the manifest against the
// contracted URL — committing a moved source as if the contract still described
// it. An in-host redirect is still followed: those are path and locale moves,
// not a change of source.
func contractedHostClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			origin := via[0].URL
			if req.URL.Scheme != origin.Scheme || req.URL.Hostname() != origin.Hostname() {
				return fmt.Errorf("%s redirects to %s://%s, not the contracted %s://%s",
					origin, req.URL.Scheme, req.URL.Hostname(), origin.Scheme, origin.Hostname())
			}

			return nil
		},
	}
}

func fetchSources(ctx context.Context, contract *snapshot.SourceContract) ([]page, error) {
	client := contractedHostClient()
	pages := make([]page, 0, len(contract.Sources))

	for i := range contract.Sources {
		source := &contract.Sources[i]
		// The architecture reference is read by a human, not by this parser. It
		// is recorded as provenance without being downloaded, so a docs-site
		// redirect cannot fail a price refresh.
		if slices.Contains(source.DataKinds, snapshot.DataKindArchitecture) {
			pages = append(pages, page{url: source.URL, fetchedAt: time.Now().UTC(), kinds: source.DataKinds})

			continue
		}

		body, err := fetch(ctx, client, source.URL)
		if err != nil {
			return nil, err
		}

		pages = append(pages, page{
			url:       source.URL,
			fetchedAt: time.Now().UTC(),
			sha256:    snapshot.SHA256Hex(body),
			body:      body,
			kinds:     source.DataKinds,
		})
	}

	return pages, nil
}

func fetch(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	var lastErr error

	for attempt := 1; attempt <= fetchAttempts; attempt++ {
		body, err := fetchOnce(ctx, client, url)
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

	return nil, fmt.Errorf("fetch %s: %w", url, lastErr)
}

func fetchOnce(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
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

func buildCatalog(contract *snapshot.SourceContract, region cloud.Region, pages []page,
) (*gcp.Catalog, []cloud.MachineID, error) {
	var spotRows, onDemandRows []gcp.MachineRow

	for i := range pages {
		source := &pages[i]

		switch {
		case slices.Contains(source.kinds, snapshot.DataKindSpotPrice):
			rows, err := ParseSpotPage(bytes.NewReader(source.body), region)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", source.url, err)
			}
			spotRows = append(spotRows, rows...)
		case slices.Contains(source.kinds, snapshot.DataKindOnDemandPrice):
			rows, err := ParseOnDemandPage(bytes.NewReader(source.body), region)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", source.url, err)
			}
			onDemandRows = append(onDemandRows, rows...)
		}
	}

	if len(spotRows) == 0 || len(onDemandRows) == 0 {
		return nil, nil, fmt.Errorf("%w: the contract must name at least one spot page and one on-demand page",
			snapshot.ErrInvalidSourceContract)
	}

	catalog, unpaired, err := gcp.BuildCatalog(region, spotRows, onDemandRows)
	if err != nil {
		return nil, nil, err
	}

	if err := catalog.Verify(contract); err != nil {
		return nil, nil, err
	}

	return catalog, unpaired, nil
}

// encodePayload gzips the catalogue with a zeroed header. The default header
// stamps the current time, which would give identical data a different hash on
// every run and make the manifest gate fire on a no-op refresh.
func encodePayload(catalog *gcp.Catalog) ([]byte, error) {
	data, err := catalog.Encode()
	if err != nil {
		return nil, err
	}

	return reproducible.Compress(data)
}

func newManifest(contract *snapshot.SourceContract, catalog *gcp.Catalog, payload []byte,
	pages []page, floor snapshot.Coverage,
) *snapshot.Manifest {
	sources := make([]snapshot.Source, 0, len(pages))
	for i := range pages {
		sources = append(sources, snapshot.Source{
			URL:       pages[i].url,
			FetchedAt: pages[i].fetchedAt,
			SHA256:    pages[i].sha256,
		})
	}

	return &snapshot.Manifest{
		SchemaVersion:     snapshot.ManifestSchemaVersion,
		DataSchemaVersion: catalog.SchemaVersion,
		ParserVersion:     contract.ParserVersion,
		Provider:          cloud.ProviderGCP,
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
			return snapshot.Coverage{}, fmt.Errorf("read gcp manifest for its reviewed coverage floor: %w", err)
		}

		return snapshot.Coverage{
			Regions:  contract.Thresholds.MinRegions,
			Machines: contract.Thresholds.MinMachines,
			Prices:   contract.Thresholds.MinMachines * 2, //nolint:mnd // spot and on-demand, one pair per machine.
		}, nil
	}

	previous, err := snapshot.ParseManifest(data)
	if err != nil {
		return snapshot.Coverage{}, fmt.Errorf("parse gcp manifest for its reviewed coverage floor: %w", err)
	}

	return previous.MinRecords, nil
}

func verify(contract *snapshot.SourceContract, catalog *gcp.Catalog, manifest *snapshot.Manifest, payload []byte) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if err := contract.VerifySnapshot(manifest, len(payload)); err != nil {
		return err
	}

	return snapshot.ValidatePrices(catalog.PriceRecords(), manifest)
}
