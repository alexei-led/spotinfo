// Command update-gcp-data refreshes the committed GCP price catalogue from the
// pages its approved source contract enumerates.
//
// It reads no credentials and writes nothing until every page has been fetched,
// parsed, joined, and validated against the contract and the previous coverage
// floor, so a failure before that point leaves the reviewed snapshot exactly as
// it was. The payload and manifest commit uses a rollback pair: if the second
// file cannot be replaced, the previous payload is restored and the update
// fails rather than leaving a partial snapshot.
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

// exitSourceUnstable is returned when the source served two different documents
// moments apart. It is separated from the ordinary failure code so the weekly
// workflow can report "waited" rather than "broke".
//
// Both are refusals to write, and both are correct. But they ask for opposite
// things from whoever reads the run: an unstable source needs nothing but time,
// while a renamed header or a coverage shortfall needs a person. A scheduled job
// that goes red every week for a reason nobody must act on is a job people stop
// reading, and the one week it goes red for a real reason is the week it is
// ignored.
const exitSourceUnstable = 75

func main() {
	err := refresh()
	if err == nil {
		return
	}

	fmt.Fprintf(os.Stderr, "update-gcp-data: %v\n", err)

	if errors.Is(err, ErrSourceUnstable) {
		os.Exit(exitSourceUnstable)
	}

	os.Exit(1)
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

	return assemble(dataDir, contract, region, pages)
}

// assemble turns fetched pages into a committed snapshot: join, encode, read the
// reviewed floor, build the manifest, verify, and only then write.
//
// Split from run so the ordering can be tested without reaching Google. The
// order is the whole point — the manifest must hash the payload that is actually
// written, the floor must come from the manifest on disk rather than the
// contract minimum, and nothing may be written before verification passes. None
// of that is visible to a unit test of any single step.
func assemble(dataDir string, contract *snapshot.SourceContract, region cloud.Region, pages []page) error {
	// Reported before the error check, never silent: an excluded machine is a
	// source defect worth looking at, and a contract failure caused by dropped
	// rows is unreadable without them.
	catalog, excluded, err := buildCatalog(contract, region, pages)
	for _, machine := range excluded {
		fmt.Fprintf(os.Stderr, "skipped %s: %s\n", machine.ID, machine.Reason)
	}
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
		return nil, fmt.Errorf("%w: contract is for %s, not %s", snapshot.ErrInvalidSourceContract, contract.Provider, cloud.ProviderGCP)
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
//
// sha256 is the raw body, which is what the manifest publishes. digest is the
// page's rendered text, which is what two reads are compared on — see
// stabilityDigest for why the two cannot be the same value.
type page struct {
	fetchedAt time.Time
	url       string
	sha256    string
	digest    string
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
			digest:    stabilityDigest(body),
			body:      body,
			kinds:     source.DataKinds,
		})
	}

	if err := confirmWindowStable(ctx, client, pages); err != nil {
		return nil, err
	}

	return pages, nil
}

// confirmWindowStable re-reads the first downloaded page after the last one and
// refuses when it has moved.
//
// fetch guards each page against a rollover landing inside that page's own two
// reads. By construction it cannot see one that lands *between* two pages, and
// that is the case which actually corrupts a snapshot: a Spot price from one
// generation divided by an On-Demand price from the next publishes a savings
// figure spanning two days, and every downstream gate — manifest hash, parser
// contract, coverage floor, per-machine spec cross-check — passes it.
//
// One re-read closes the whole gap. The first page's hash is known from before
// any other page was fetched, so comparing it after the last one brackets every
// interval between them at the cost of a single extra download per run.
func confirmWindowStable(ctx context.Context, client *http.Client, pages []page) error {
	// The architecture reference is recorded as provenance without being
	// downloaded, so it carries no body to compare.
	first := slices.IndexFunc(pages, func(p page) bool { return p.body != nil })
	if first < 0 {
		return nil
	}

	body, err := fetchWithRetry(ctx, client, pages[first].url)
	if err != nil {
		return err
	}

	if after := stabilityDigest(body); after != pages[first].digest {
		return fmt.Errorf("%w: %s digested %s before the other pages were read and %s after. "+
			"The source rolled over mid-run; a snapshot taken now can pair prices from two "+
			"generations. Retry when the digests agree",
			ErrSourceUnstable, pages[first].url, pages[first].digest, after)
	}

	return nil
}

// ErrSourceUnstable reports a page that served different bytes to two requests
// made moments apart.
var ErrSourceUnstable = errors.New("gcp pricing page is not serving a stable document")

// fetch reads a contracted page, then reads it again and refuses to proceed if
// the two copies differ.
//
// Google serves these pages from a CDN that can hold more than one generation at
// once. On 2026-08-10 five consecutive requests to the Spot page — same URL, same
// User-Agent, seconds apart — alternated between two price generations:
// n2-standard-4 came back at $0.101336 three times and $0.111472 twice, and every
// response had a different hash. The general-purpose page was stable in the same
// window, so this is per-page and cannot be assumed away for any of them.
//
// A single read cannot tell which generation it got, and the four contracted
// pages are read seconds apart, so one run can pair a Spot price from one
// generation with an On-Demand price from another and publish a savings figure
// computed across two different days. Nothing downstream can detect that: both
// numbers are well-formed, in range, and from the contracted URL.
//
// So the second read is the gate. Two identical copies do not prove the source is
// stable, but two different ones prove it is not, and that is the case worth
// refusing. It costs one extra download per page on a weekly build-time job and
// nothing at all at runtime.
//
// The comparison is on stabilityDigest, not on the raw body: these pages carry a
// fresh CSP nonce and a fresh request id in every response, so a raw-body
// comparison refused every run and no snapshot could be written at all.
func fetch(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	first, err := fetchWithRetry(ctx, client, url)
	if err != nil {
		return nil, err
	}

	second, err := fetchWithRetry(ctx, client, url)
	if err != nil {
		return nil, err
	}

	if firstSum, secondSum := stabilityDigest(first), stabilityDigest(second); firstSum != secondSum {
		return nil, fmt.Errorf("%w: %s digested %s then %s. The source is mid-rollout; "+
			"a snapshot taken now can mix price generations across pages. Retry when the digests agree",
			ErrSourceUnstable, url, firstSum, secondSum)
	}

	return first, nil
}

func fetchWithRetry(ctx context.Context, client *http.Client, url string) ([]byte, error) {
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
) (*gcp.Catalog, []gcp.ExcludedMachine, error) {
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

	catalog, excluded, err := gcp.BuildCatalog(region, spotRows, onDemandRows)
	if err != nil {
		return nil, excluded, err
	}

	// Excluded machines are returned alongside a verification failure, not
	// swallowed by it. A contract check that fails *because* rows were dropped —
	// "no machine in approved series n4d" — is unreadable without the list of
	// what was dropped and why.
	if err := catalog.Verify(contract); err != nil {
		return nil, excluded, err
	}

	return catalog, excluded, nil
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
