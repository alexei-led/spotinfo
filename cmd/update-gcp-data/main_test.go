package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/providers/gcp"
	"spotinfo/internal/snapshot"
)

// A page that moves to another host is a contract change, not a fetch detail:
// the body would be hashed into the manifest against the contracted URL, so the
// refresh must fail and force a review instead. A redirect that stays on the
// contracted host is a path move and is still followed.
func TestFetchRefusesAnOffHostRedirect(t *testing.T) {
	t.Parallel()

	contracted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/off-host":
			http.Redirect(w, r, "http://pricing.example.invalid/pricing", http.StatusFound)
		case "/in-host":
			http.Redirect(w, r, "/pricing", http.StatusFound)
		default:
			_, _ = io.WriteString(w, "contracted body")
		}
	}))
	defer contracted.Close()

	client := contractedHostClient()

	_, err := fetchOnce(t.Context(), client, contracted.URL+"/off-host")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the contracted")

	body, err := fetchOnce(t.Context(), client, contracted.URL+"/in-host")
	require.NoError(t, err)
	assert.Equal(t, "contracted body", string(body))
}

func testCatalog(t *testing.T) *gcp.Catalog {
	t.Helper()

	spot, err := cloud.ParseMoney("0.058121")
	require.NoError(t, err)
	onDemand, err := cloud.ParseMoney("0.117660")
	require.NoError(t, err)

	catalog, unpaired, err := gcp.BuildCatalog("us-central1",
		[]gcp.MachineRow{{ID: "c4-standard-2", VCPU: 2, MemoryGiB: 7, Price: spot}},
		[]gcp.MachineRow{{ID: "c4-standard-2", VCPU: 2, MemoryGiB: 7, Price: onDemand}})
	require.NoError(t, err)
	require.Empty(t, unpaired)

	return catalog
}

// A hash-stable payload is what keeps a no-op refresh from firing the manifest
// gate. The gzip header is where that could break: a modification time or a
// host-dependent OS byte would give unchanged data a new hash every run.
func TestEncodePayloadIsReproducible(t *testing.T) {
	t.Parallel()

	catalog := testCatalog(t)

	first, err := encodePayload(catalog)
	require.NoError(t, err)
	second, err := encodePayload(catalog)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

const gcpDataDir = "../../internal/providers/gcp/data"

func TestCoverageFloorFallsBackToTheContractedMinimum(t *testing.T) {
	t.Parallel()

	contract, err := loadContract(gcpDataDir)
	require.NoError(t, err)

	floor, err := coverageFloor(t.TempDir(), contract)
	require.NoError(t, err)

	assert.Equal(t, snapshot.Coverage{
		Regions:  contract.Thresholds.MinRegions,
		Machines: contract.Thresholds.MinMachines,
		Prices:   contract.Thresholds.MinMachines * 2,
	}, floor)
}

// The committed floor is hand-curated review evidence. Deriving it from the
// data that just arrived would ratchet the gate into always passing, and so
// would falling back to the contract minimum when a reviewer has raised it. The
// staged manifest therefore carries a floor above every contracted minimum, so
// the assertion fails if the fallback is taken.
func TestCoverageFloorKeepsTheReviewedManifestFloor(t *testing.T) {
	t.Parallel()

	contract, err := loadContract(gcpDataDir)
	require.NoError(t, err)

	raised := snapshot.Coverage{
		Regions:  contract.Thresholds.MinRegions + 1,
		Machines: contract.Thresholds.MinMachines + 7,
		Prices:   contract.Thresholds.MinMachines*2 + 13,
	}
	dataDir := stageManifestWithFloor(t, gcpDataDir, raised)

	floor, err := coverageFloor(dataDir, contract)
	require.NoError(t, err)

	assert.Equal(t, raised, floor)
}

// An unparseable manifest must fail the refresh. Silently seeding the contract
// minimum would discard a reviewer's raised floor and open a green PR.
func TestCoverageFloorFailsOnAnUnparseableManifest(t *testing.T) {
	t.Parallel()

	contract, err := loadContract(gcpDataDir)
	require.NoError(t, err)

	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, manifestFile), []byte("{"), 0o600))

	_, err = coverageFloor(dataDir, contract)

	require.ErrorIs(t, err, snapshot.ErrInvalidManifest)
}

// TestBuildCatalogRefusesAHalfPricedRefresh pins the fail-closed guard. Both
// classes are required: a spot price with no list price beside it is a savings
// figure with no denominator, and a list price alone is not a spot catalogue at
// all. Either way the contract named the wrong pages, so the refresh stops
// rather than committing half a catalogue.
func TestBuildCatalogRefusesAHalfPricedRefresh(t *testing.T) {
	t.Parallel()

	contract, err := loadContract(gcpDataDir)
	require.NoError(t, err)

	region := contract.Support.Regions[0]

	for name, pages := range map[string][]page{
		"spot page only":      {fixturePage(t, "spot-pricing.html", snapshot.DataKindSpotPrice)},
		"on-demand page only": {fixturePage(t, "on-demand-pricing.html", snapshot.DataKindOnDemandPrice)},
		"no priced page":      {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, _, err := buildCatalog(contract, region, pages)

			require.ErrorIs(t, err, snapshot.ErrInvalidSourceContract)
		})
	}
}

// fixturePage presents a committed parser fixture as one downloaded source
// page. The fixtures render the contracted region, so a failure here is the
// guard rather than the parser.
func fixturePage(t *testing.T, name string, kind snapshot.DataKind) page {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)

	return page{
		url:   "https://cloud.google.com/compute/" + name,
		body:  body,
		kinds: []snapshot.DataKind{kind},
	}
}

// stageManifestWithFloor copies the committed manifest into a temporary data
// directory with a different coverage floor.
func stageManifestWithFloor(t *testing.T, sourceDir string, floor snapshot.Coverage) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(sourceDir, manifestFile))
	require.NoError(t, err)

	manifest, err := snapshot.ParseManifest(data)
	require.NoError(t, err)
	manifest.MinRecords = floor

	staged, err := json.Marshal(manifest)
	require.NoError(t, err)

	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, manifestFile), staged, 0o600))

	return dataDir
}

// Google's CDN can hold two price generations at once and alternate between
// them: on 2026-08-10 five consecutive requests to the Spot page returned
// n2-standard-4 at $0.101336 three times and $0.111472 twice. A single read
// cannot tell which generation it got, and the four contracted pages are read
// seconds apart, so a run could pair a Spot price from one generation with an
// On-Demand price from another. Reading twice is what makes that visible.
func TestFetchRefusesAPageThatChangesBetweenTwoReads(t *testing.T) {
	t.Parallel()

	var reads atomic.Int32
	unstable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Alternates, exactly as the live page did.
		if reads.Add(1)%2 == 1 {
			_, _ = io.WriteString(w, "n2-standard-4 $0.101336")

			return
		}
		_, _ = io.WriteString(w, "n2-standard-4 $0.111472")
	}))
	defer unstable.Close()

	_, err := fetch(t.Context(), contractedHostClient(), unstable.URL)
	require.ErrorIs(t, err, ErrSourceUnstable)
	assert.Contains(t, err.Error(), "mid-rollout")
	assert.Equal(t, int32(2), reads.Load(), "the page must be read twice, not once")
}

// A stable page still costs two reads and returns the body unchanged, so the
// gate above cannot be satisfied by never reading twice.
func TestFetchAcceptsAPageThatIsStableAcrossTwoReads(t *testing.T) {
	t.Parallel()

	var reads atomic.Int32
	stable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		_, _ = io.WriteString(w, "n2-standard-4 $0.111472")
	}))
	defer stable.Close()

	body, err := fetch(t.Context(), contractedHostClient(), stable.URL)
	require.NoError(t, err)
	assert.Equal(t, "n2-standard-4 $0.111472", string(body))
	assert.Equal(t, int32(2), reads.Load())
}

// The per-page double read cannot see a rollover that lands between two pages,
// which is the one that mixes a Spot price from one generation with an On-Demand
// price from the next. The bracket re-read is what covers that gap.
func TestWindowBracketRefusesAPageThatMovedWhileTheOthersWereRead(t *testing.T) {
	t.Parallel()

	var moved atomic.Bool
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if moved.Load() {
			_, _ = io.WriteString(w, "n2-standard-4 $0.111472")

			return
		}
		_, _ = io.WriteString(w, "n2-standard-4 $0.101336")
	}))
	defer source.Close()

	client := contractedHostClient()

	body, err := fetch(t.Context(), client, source.URL)
	require.NoError(t, err, "the page is stable across its own two reads")

	pages := []page{
		// The architecture reference is provenance only; it must not be re-read.
		{url: "https://cloud.google.com/compute/docs/cpu-platforms"},
		{url: source.URL, sha256: snapshot.SHA256Hex(body), digest: stabilityDigest(body), body: body},
	}

	// The CDN flips after every page has been read once — invisible to fetch.
	moved.Store(true)

	err = confirmWindowStable(t.Context(), client, pages)
	require.ErrorIs(t, err, ErrSourceUnstable)
	assert.Contains(t, err.Error(), "rolled over mid-run")
}

func TestWindowBracketAcceptsAStableWindow(t *testing.T) {
	t.Parallel()

	var reads atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		_, _ = io.WriteString(w, "n2-standard-4 $0.111472")
	}))
	defer source.Close()

	body := []byte("n2-standard-4 $0.111472")
	pages := []page{{url: source.URL, sha256: snapshot.SHA256Hex(body), digest: stabilityDigest(body), body: body}}

	require.NoError(t, confirmWindowStable(t.Context(), contractedHostClient(), pages))
	assert.Equal(t, int32(1), reads.Load(), "the bracket costs exactly one extra read per run")
}

// The gate compares rendered text, not raw bytes, and this is why. Every
// response from the contracted pages carries a fresh CSP nonce and a fresh
// request id, so a raw-body comparison reported ErrSourceUnstable on every run
// and `make update-gcp-data` could not write a snapshot at all — measured on
// 2026-08-12 as three different SHA-256 sums from three consecutive reads whose
// prices were identical. A page that only re-rolls its nonce must be accepted.
func TestFetchAcceptsAPageWhoseOnlyChangeIsPerResponseMarkup(t *testing.T) {
	t.Parallel()

	var reads atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A different nonce and request id on every response, same price.
		_, _ = fmt.Fprintf(w, `<html><head><script nonce="nonce-%d">`+
			`window.cfg={"FdrFJe":"%d"}</script></head>`+
			`<body><table><tr><td>n2-standard-4</td><td>$0.111472 / 1 hour</td></tr></table></body></html>`,
			reads.Add(1), reads.Load()*7919)
	}))
	defer source.Close()

	body, err := fetch(t.Context(), contractedHostClient(), source.URL)
	require.NoError(t, err, "a re-rolled nonce is not a price generation")
	assert.Contains(t, string(body), "0.111472")
	assert.Equal(t, int32(2), reads.Load(), "the page is still read twice")
}

// The other half: the digest must still see the change the gate exists for.
// Without this the fix above would read as "the gate was switched off".
func TestStabilityDigestSeesContentAndIgnoresPerResponseMarkup(t *testing.T) {
	t.Parallel()

	const page = `<html><head><script nonce="A">window.cfg={"FdrFJe":"-584"}</script></head>` +
		`<body><!-- built A --><table><tr><td>n2-standard-4</td><td>$0.101336 / 1 hour</td></tr></table></body></html>`

	tests := []struct {
		name  string
		other string
		same  bool
	}{
		{
			name: "a re-rolled nonce and request id digest the same",
			other: `<html><head><script nonce="B">window.cfg={"FdrFJe":"4354"}</script></head>` +
				`<body><!-- built B --><table><tr><td>n2-standard-4</td><td>$0.101336 / 1 hour</td></tr></table></body></html>`,
			same: true,
		},
		{
			name: "reformatted whitespace digests the same",
			other: "<html><head></head>\n\n  <body>\t<table>\n<tr><td>n2-standard-4</td>\n" +
				"<td>$0.101336 / 1 hour</td></tr>\n</table>  </body></html>",
			same: true,
		},
		{
			name: "a moved price digests differently",
			other: `<html><head><script nonce="A">window.cfg={"FdrFJe":"-584"}</script></head>` +
				`<body><!-- built A --><table><tr><td>n2-standard-4</td><td>$0.111472 / 1 hour</td></tr></table></body></html>`,
			same: false,
		},
		{
			name:  "a dropped row digests differently",
			other: `<html><body><table></table></body></html>`,
			same:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.same {
				assert.Equal(t, stabilityDigest([]byte(page)), stabilityDigest([]byte(tc.other)))

				return
			}

			assert.NotEqual(t, stabilityDigest([]byte(page)), stabilityDigest([]byte(tc.other)))
		})
	}
}

// The bracket protects a refresh only if the acquisition path actually calls it,
// so this drives fetchSources rather than confirmWindowStable. Every page is
// stable across its own two reads and the source flips only after the last one —
// the case no per-page double read can see. Deleting the call site fails here.
func TestFetchSourcesBracketsTheWholeReadWindow(t *testing.T) {
	t.Parallel()

	var reads atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		generation := "A"
		// Downloaded pages cost two reads each; the flip lands after all of them
		// and before the bracket re-read, so every per-page pair still agrees.
		if reads.Add(1) > 2*gcpDownloadedPages {
			generation = "B"
		}
		_, _ = io.WriteString(w, generation+r.URL.Path)
	}))
	defer source.Close()

	contract := contractServedBy(t, source.URL)

	_, err := fetchSources(t.Context(), contract)
	require.ErrorIs(t, err, ErrSourceUnstable)
	assert.Contains(t, err.Error(), "rolled over mid-run")
	assert.Equal(t, int32(2*gcpDownloadedPages+1), reads.Load(),
		"two reads per page, then one bracket re-read of the first")
}

// gcpDownloadedPages is how many contracted sources are actually fetched: every
// source but the architecture reference, which is provenance only.
const gcpDownloadedPages = 4

// contractServedBy mirrors the committed contract's source list onto a test
// server: the same number of sources, the same data kinds, one distinct path
// each.
//
// Built as a struct rather than parsed, because ParseSourceContract requires
// absolute https URLs and httptest serves plain http. That validation is not
// what this test is about — fetchSources reads only Sources — and the page count
// is still taken from the committed contract, so a source added there fails the
// assertion below instead of silently weakening the test.
func contractServedBy(t *testing.T, base string) *snapshot.SourceContract {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "providers", "gcp", "data", contractFile))
	require.NoError(t, err)

	committed, err := snapshot.ParseSourceContract(raw)
	require.NoError(t, err)

	sources := make([]snapshot.ContractSource, 0, len(committed.Sources))
	downloaded := 0

	for i := range committed.Sources {
		source := committed.Sources[i]
		source.URL = fmt.Sprintf("%s/page-%d", base, i)
		sources = append(sources, source)

		if !slices.Contains(source.DataKinds, snapshot.DataKindArchitecture) {
			downloaded++
		}
	}

	require.Equal(t, gcpDownloadedPages, downloaded,
		"the read count asserted above is derived from this; update both together")

	return &snapshot.SourceContract{Sources: sources}
}

// A contract whose only source is the undownloaded architecture reference has no
// window to bracket, and must not be turned into a fetch of an empty URL.
func TestWindowBracketSkipsPagesThatWereNeverDownloaded(t *testing.T) {
	t.Parallel()

	pages := []page{{url: "https://cloud.google.com/compute/docs/cpu-platforms"}}

	require.NoError(t, confirmWindowStable(t.Context(), contractedHostClient(), pages))
}

// The weekly workflow branches on this code to report a wait rather than a
// break, so it is part of the contract with that workflow, not an internal
// detail. 75 is EX_TEMPFAIL from sysexits.h: a temporary failure, retry later.
func TestUnstableSourceHasItsOwnExitCode(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 75, exitSourceUnstable,
		"the workflow greps for this exact code; changing it silently makes every "+
			"mid-rollout week look like a broken parser")
}

// stageDataDir writes a data directory holding only the contract, with the
// coverage floor lowered to what the committed fixtures carry. These tests are
// about the order assemble does things in; each gate has its own test above.
func stageDataDir(t *testing.T, machines int) (string, *snapshot.SourceContract) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "providers", "gcp", "data", contractFile))
	require.NoError(t, err)

	var contract map[string]any
	require.NoError(t, json.Unmarshal(raw, &contract))

	thresholds, ok := contract["thresholds"].(map[string]any)
	require.True(t, ok)
	thresholds["min_machines"] = machines

	// The fixtures carry three series, not the full approved set. Narrowing the
	// contract keeps these tests about assemble's ordering; the series check
	// itself is covered by the catalogue tests.
	support, ok := contract["support"].(map[string]any)
	require.True(t, ok)
	support["machine_series"] = []string{"c4", "n1", "t2a"}

	dir := t.TempDir()
	encoded, err := json.Marshal(contract)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, contractFile), encoded, 0o600))

	parsed, err := snapshot.ParseSourceContract(encoded)
	require.NoError(t, err)

	return dir, parsed
}

// fixturePages presents the committed parser fixtures as fetched pages, under
// the contract's own approved URLs and carrying the provenance a real fetch
// would. assemble builds the manifest from this, and refuses a source that is
// unapproved, unhashed, or undated — so all three have to be right here.
func fixturePages(t *testing.T, contract *snapshot.SourceContract) []page {
	t.Helper()

	fixtures := map[snapshot.DataKind]string{
		snapshot.DataKindSpotPrice:     "spot-pricing.html",
		snapshot.DataKindOnDemandPrice: "on-demand-pricing.html",
	}

	at := time.Date(2026, time.August, 10, 7, 0, 0, 0, time.UTC)

	pages := make([]page, 0, len(contract.Sources))
	for i := range contract.Sources {
		source := &contract.Sources[i]

		entry := page{url: source.URL, fetchedAt: at, kinds: source.DataKinds}
		for kind, fixture := range fixtures {
			if !slices.Contains(source.DataKinds, kind) {
				continue
			}

			body, err := os.ReadFile(filepath.Join("testdata", fixture))
			require.NoError(t, err)

			entry.body, entry.sha256, entry.kinds = body, snapshot.SHA256Hex(body), []snapshot.DataKind{kind}

			break
		}

		// A source with no fixture is the architecture reference, which the
		// updater records as provenance without downloading: a human reads it,
		// and a docs-site redirect must not fail a price refresh.
		pages = append(pages, entry)
	}

	return pages
}

// The write path end to end: join, encode, read the floor, build the manifest,
// verify, write. A wiring mistake no single-step test can see — a manifest built
// from a different payload than the one written, a floor read from the contract
// when a manifest exists, a write that happens before verification — fails here.
func TestAssembleWritesASnapshotAndItsManifest(t *testing.T) {
	t.Parallel()

	dir, contract := stageDataDir(t, 1)
	require.NoError(t, assemble(dir, contract, contract.Support.Regions[0], fixturePages(t, contract)))

	payload, err := os.ReadFile(filepath.Join(dir, payloadFile))
	require.NoError(t, err)
	require.NotEmpty(t, payload)

	raw, err := os.ReadFile(filepath.Join(dir, manifestFile))
	require.NoError(t, err)

	manifest, err := snapshot.ParseManifest(raw)
	require.NoError(t, err)
	assert.Equal(t, snapshot.SHA256Hex(payload), manifest.Payload.SHA256,
		"the manifest must hash the payload that was actually written, not an earlier encoding")
	assert.NotEmpty(t, manifest.Sources, "every answer must be able to say where it came from")
}

// A refresh that fails a gate leaves the previous snapshot untouched. This is
// the property the whole design rests on: a bad refresh costs a red run, never
// the committed data.
func TestAssembleWritesNothingWhenAGateFails(t *testing.T) {
	t.Parallel()

	dir, contract := stageDataDir(t, 10_000) // more machines than the fixtures carry

	require.Error(t, assemble(dir, contract, contract.Support.Regions[0], fixturePages(t, contract)))

	for _, name := range []string{payloadFile, manifestFile} {
		_, err := os.Stat(filepath.Join(dir, name))
		assert.ErrorIs(t, err, os.ErrNotExist, "%s must not exist: a failed refresh writes nothing", name)
	}
}

// The reviewed floor comes from the manifest on disk, not the contract minimum.
// Seeding from the contract whenever the manifest is merely unreadable would let
// a reviewer's raised floor be discarded by a green run.
func TestAssembleKeepsTheReviewedFloorFromTheManifest(t *testing.T) {
	t.Parallel()

	dir, contract := stageDataDir(t, 1)
	require.NoError(t, assemble(dir, contract, contract.Support.Regions[0], fixturePages(t, contract)))

	raw, err := os.ReadFile(filepath.Join(dir, manifestFile))
	require.NoError(t, err)
	first, err := snapshot.ParseManifest(raw)
	require.NoError(t, err)

	// Raise the floor above what the fixtures carry, exactly as a reviewer would.
	first.MinRecords.Machines = 10_000
	encoded, err := json.Marshal(first)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestFile), encoded, 0o600))

	require.Error(t, assemble(dir, contract, contract.Support.Regions[0], fixturePages(t, contract)),
		"a raised floor must survive the next refresh")
}
