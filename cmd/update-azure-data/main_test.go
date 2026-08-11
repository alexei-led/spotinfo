package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/providers/azure"
	"spotinfo/internal/snapshot"
)

const dataDir = "../../internal/providers/azure/data"

func testCatalog(t *testing.T) *azure.Catalog {
	t.Helper()

	spot, err := cloud.ParseMoney("0.041200")
	require.NoError(t, err)
	onDemand, err := cloud.ParseMoney("0.096000")
	require.NoError(t, err)

	catalog, report, err := azure.BuildCatalog(
		[]azure.SeriesSpec{{
			Series:       "dsv5",
			Architecture: cloud.ArchitectureX8664,
			Sizes:        []azure.SizeSpec{{ID: "Standard_D2s_v5", VCPU: 2, MemoryGiB: 8}},
		}},
		[]azure.PriceRow{
			{Machine: "Standard_D2s_v5", Region: "eastus", OS: cloud.OSLinux,
				Class: cloud.PriceClassSpot, Amount: spot},
			{Machine: "Standard_D2s_v5", Region: "eastus", OS: cloud.OSLinux,
				Class: cloud.PriceClassOnDemand, Amount: onDemand},
		})
	require.NoError(t, err)
	require.Empty(t, report.Unpaired)
	require.Empty(t, report.Unspecified)

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

func TestCoverageFloorFallsBackToTheContractedMinimum(t *testing.T) {
	t.Parallel()

	contract, err := loadContract(dataDir)
	require.NoError(t, err)

	floor, err := coverageFloor(t.TempDir(), contract)
	require.NoError(t, err)

	assert.Equal(t, snapshot.Coverage{
		Regions:  contract.Thresholds.MinRegions,
		Machines: contract.Thresholds.MinMachines,
		Prices:   contract.Thresholds.MinRegions * contract.Thresholds.MinMachines * pricesPerMachine,
	}, floor)
}

// The committed floor is hand-curated review evidence. Deriving it from the
// data that just arrived would ratchet the gate into always passing, and so
// would falling back to the contract minimum when a reviewer has raised it. The
// staged manifest therefore carries a floor above every contracted minimum, so
// the assertion fails if the fallback is taken.
func TestCoverageFloorKeepsTheReviewedManifestFloor(t *testing.T) {
	t.Parallel()

	contract, err := loadContract(dataDir)
	require.NoError(t, err)

	raised := snapshot.Coverage{
		Regions:  contract.Thresholds.MinRegions + 1,
		Machines: contract.Thresholds.MinMachines + 7,
		Prices:   contract.Thresholds.MinRegions*contract.Thresholds.MinMachines*pricesPerMachine + 13,
	}
	staged := stageManifestWithFloor(t, dataDir, raised)

	floor, err := coverageFloor(staged, contract)
	require.NoError(t, err)

	assert.Equal(t, raised, floor)
}

// An unparseable manifest must fail the refresh. Silently seeding the contract
// minimum would discard a reviewer's raised floor and open a green PR.
func TestCoverageFloorFailsOnAnUnparseableManifest(t *testing.T) {
	t.Parallel()

	contract, err := loadContract(dataDir)
	require.NoError(t, err)

	broken := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(broken, manifestFile), []byte("{"), 0o600))

	_, err = coverageFloor(broken, contract)

	require.ErrorIs(t, err, snapshot.ErrInvalidManifest)
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

	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)

	staged := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(staged, manifestFile), encoded, 0o600))

	return staged
}

// TestEverySupportedSeriesHasASourcePage is the check that keeps the approved
// matrix and the pages honest with each other: a series listed in support but
// documented by no source would silently vanish from the catalogue, and the
// coverage floor is the only thing that would notice.
func TestEverySupportedSeriesHasASourcePage(t *testing.T) {
	t.Parallel()

	contract, err := loadContract(dataDir)
	require.NoError(t, err)

	documented := make(map[string]struct{}, len(contract.Sources))
	for i := range contract.Sources {
		series, seriesErr := SeriesFromURL(contract.Sources[i].URL)
		if seriesErr == nil {
			documented[series] = struct{}{}
		}
	}

	for _, series := range contract.Support.MachineSeries {
		assert.Contains(t, documented, series)
	}
}

// TestPaginationStaysOnTheContractedHost pins the one place a response gets to
// choose the next request.
func TestPaginationStaysOnTheContractedHost(t *testing.T) {
	t.Parallel()

	first := "https://prices.azure.com/api/retail/prices?$skip=0"

	require.NoError(t, sameHost(first, "https://prices.azure.com:443/api/retail/prices?$skip=100"))

	for _, next := range []string{
		"https://prices.example.invalid/api/retail/prices",
		"http://prices.azure.com/api/retail/prices",
	} {
		assert.Error(t, sameHost(first, next), next)
	}
}

// TestSweepRegionRefusesAnOffHostNextPageLink drives the guard through the real
// pagination loop: the response chooses the next request, so the check has to
// happen before that request is made, not after.
// A 3xx Location is as response-supplied as a NextPageLink. Following one off
// the contracted host would hash the moved page into the manifest against the
// contracted URL, so it fails; an in-host redirect is a path move and is still
// followed.
func TestFetchRefusesAnOffHostRedirect(t *testing.T) {
	t.Parallel()

	contracted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/off-host":
			http.Redirect(w, r, "http://prices.example.invalid/api/retail/prices", http.StatusFound)
		case "/in-host":
			http.Redirect(w, r, "/prices", http.StatusFound)
		default:
			_, _ = io.WriteString(w, "contracted body")
		}
	}))
	defer contracted.Close()

	client := contractedHostClient()

	_, err := fetchOnce(t.Context(), client, contracted.URL+"/off-host")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prices.example.invalid")

	body, err := fetchOnce(t.Context(), client, contracted.URL+"/in-host")
	require.NoError(t, err)
	assert.Equal(t, "contracted body", string(body))
}

func TestSweepRegionRefusesAnOffHostNextPageLink(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, retailPage("Standard_D2s_v5", "http://prices.example.invalid/api/retail/prices"))
	}))
	defer server.Close()

	_, _, err := sweepRegion(t.Context(), server.Client(), server.URL)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "prices.example.invalid")
	assert.Equal(t, int64(1), requests.Load(), "the off-host page must never be fetched")
}

// TestSweepRegionHashesEveryPageItFollowed pins what the manifest records: one
// hash per region, over the bytes of every page the sweep read. A hash taken
// from the last page alone would not cover the rows the catalogue ships.
func TestSweepRegionHashesEveryPageItFollowed(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)

		if r.URL.Query().Get("page") == "2" {
			_, _ = io.WriteString(w, retailPage("Standard_D4s_v5", ""))

			return
		}
		_, _ = io.WriteString(w, retailPage("Standard_D2s_v5", "http://"+r.Host+"/?page=2"))
	}))
	defer server.Close()

	items, bodies, err := sweepRegion(t.Context(), server.Client(), server.URL+"/?page=1")
	require.NoError(t, err)

	assert.Equal(t, int64(2), requests.Load())
	require.Len(t, items, 2)
	assert.Equal(t, "Standard_D2s_v5", items[0].ArmSkuName)
	assert.Equal(t, "Standard_D4s_v5", items[1].ArmSkuName)

	host := strings.TrimPrefix(server.URL, "http://")
	assert.Equal(t,
		retailPage("Standard_D2s_v5", "http://"+host+"/?page=2")+retailPage("Standard_D4s_v5", ""),
		string(bodies))
}

func TestSweepRegionStopsAtThePageCeiling(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, retailPage("Standard_D2s_v5", "http://"+r.Host+"/"))
	}))
	defer server.Close()

	_, _, err := sweepRegion(t.Context(), server.Client(), server.URL+"/")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not terminate")
	assert.Equal(t, int64(maxPagesPerRegion), requests.Load(), "a cycle must cost exactly the ceiling")
}

// retailPage renders one API response page. sweepRegion decodes the envelope
// only, so one identifying field per row is enough.
func retailPage(machine, next string) string {
	return fmt.Sprintf(`{"Items":[{"armSkuName":%q}],"NextPageLink":%q,"Count":1}`, machine, next)
}

// TestBuildCatalogResolvesPricesAgainstTheGivenInstant proves `at` reaches the
// effective-date rule instead of the parser reading the wall clock: the same
// rows must yield February's price in February and June's in June.
func TestBuildCatalogResolvesPricesAgainstTheGivenInstant(t *testing.T) {
	t.Parallel()

	changeover := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	opened := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)

	items := []azure.RetailItem{
		retailItem("D2s v5 Spot", "0.041200", opened, &changeover),
		retailItem("D2s v5 Spot", "0.038900", changeover, nil),
		retailItem("D2s v5", "0.096000", opened, nil),
	}

	for name, tc := range map[string]struct {
		at   time.Time
		spot string
	}{
		"before the changeover": {at: time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC), spot: "0.041200000"},
		"after the changeover":  {at: time.Date(2026, time.June, 15, 0, 0, 0, 0, time.UTC), spot: "0.038900000"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			catalog, err := buildCatalog(minimalContract(), testSeries(), items, tc.at)
			require.NoError(t, err)

			require.Len(t, catalog.Regions, 1)
			require.Len(t, catalog.Regions[0].Prices, 1)
			assert.Equal(t, tc.spot, catalog.Regions[0].Prices[0].Spot.String())
			assert.Equal(t, "0.096000000", catalog.Regions[0].Prices[0].OnDemand.String())
		})
	}
}

// minimalContract approves exactly the one region, series and architecture the
// rows above carry. The committed contract's floors describe a full sweep and
// would reject any fixture small enough to read.
func minimalContract() *snapshot.SourceContract {
	return &snapshot.SourceContract{
		Provider:      cloud.ProviderAzure,
		ParserVersion: azure.ParserVersion,
		Support: snapshot.Support{
			OperatingSystems: []cloud.OperatingSystem{cloud.OSLinux},
			Architectures:    []cloud.Architecture{cloud.ArchitectureX8664},
			Regions:          []cloud.Region{"eastus"},
			MachineSeries:    []string{"dsv5"},
		},
		Thresholds: snapshot.Thresholds{MinRegions: 1, MinMachines: 1, MaxFractionalDigits: 6},
	}
}

func testSeries() []azure.SeriesSpec {
	return []azure.SeriesSpec{{
		Series:       "dsv5",
		Architecture: cloud.ArchitectureX8664,
		Sizes:        []azure.SizeSpec{{ID: "Standard_D2s_v5", VCPU: 2, MemoryGiB: 8}},
	}}
}

func retailItem(sku, amount string, start time.Time, end *time.Time) azure.RetailItem {
	return azure.RetailItem{
		ServiceName:        "Virtual Machines",
		Type:               "Consumption",
		UnitOfMeasure:      "1 Hour",
		CurrencyCode:       "USD",
		ProductName:        "Virtual Machines Dsv5 Series",
		SkuName:            sku,
		ArmSkuName:         "Standard_D2s_v5",
		ArmRegionName:      "eastus",
		RetailPrice:        json.Number(amount),
		EffectiveStartDate: start,
		EffectiveEndDate:   end,
	}
}

func TestPriceAPIBaseIsTheContractedRESTSource(t *testing.T) {
	t.Parallel()

	contract, err := loadContract(dataDir)
	require.NoError(t, err)

	base, err := priceAPIBase(contract)
	require.NoError(t, err)
	assert.Equal(t, "https://prices.azure.com/api/retail/prices", base)
}

// assembleFixture is the smallest input that satisfies minimalContract: one
// series, one region, one size priced in both classes, plus the provenance the
// manifest gate demands of every source.
func assembleFixture(t *testing.T) (*snapshot.SourceContract, []azure.RetailItem, []snapshot.Source, time.Time) {
	t.Helper()

	at := time.Date(2026, time.August, 10, 7, 0, 0, 0, time.UTC)
	opened := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)

	contract := minimalContract()
	contract.Thresholds.MaxCompressedBytes = 65536
	contract.Sources = []snapshot.ContractSource{{
		URL:       "https://prices.azure.com/api/retail/prices",
		DataKinds: []snapshot.DataKind{snapshot.DataKindSpotPrice, snapshot.DataKindOnDemandPrice},
	}}

	items := []azure.RetailItem{
		retailItem("D2s v5 Spot", "0.041200", opened, nil),
		retailItem("D2s v5", "0.096000", opened, nil),
	}

	sources := []snapshot.Source{{
		URL:       contract.Sources[0].URL,
		FetchedAt: at,
		SHA256:    snapshot.SHA256Hex([]byte("retail prices response")),
	}}

	return contract, items, sources, at
}

// The write path end to end: join, encode, read the floor, build the manifest,
// verify, write. A wiring mistake no single-step test can see — a manifest built
// from a different payload than the one written, a floor read from the contract
// when a manifest exists, a write before verification — fails here.
func TestAssembleWritesASnapshotAndItsManifest(t *testing.T) {
	t.Parallel()

	contract, items, sources, at := assembleFixture(t)
	dir := t.TempDir()

	require.NoError(t, assemble(dir, contract, testSeries(), items, sources, at))

	payload, err := os.ReadFile(filepath.Join(dir, payloadFile))
	require.NoError(t, err)
	require.NotEmpty(t, payload)

	raw, err := os.ReadFile(filepath.Join(dir, manifestFile))
	require.NoError(t, err)

	manifest, err := snapshot.ParseManifest(raw)
	require.NoError(t, err)
	assert.Equal(t, snapshot.SHA256Hex(payload), manifest.Payload.SHA256,
		"the manifest must hash the payload that was actually written")
	assert.NotEmpty(t, manifest.Sources)
}

// A refresh that fails a gate leaves the previous snapshot untouched. This is
// the property the whole design rests on: a bad refresh costs a red run, never
// the committed data.
func TestAssembleWritesNothingWhenAGateFails(t *testing.T) {
	t.Parallel()

	contract, items, sources, at := assembleFixture(t)
	contract.Thresholds.MinMachines = 10_000 // more than the fixture carries
	dir := t.TempDir()

	require.Error(t, assemble(dir, contract, testSeries(), items, sources, at))

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

	contract, items, sources, at := assembleFixture(t)
	dir := t.TempDir()

	require.NoError(t, assemble(dir, contract, testSeries(), items, sources, at))

	raw, err := os.ReadFile(filepath.Join(dir, manifestFile))
	require.NoError(t, err)
	manifest, err := snapshot.ParseManifest(raw)
	require.NoError(t, err)

	manifest.MinRecords.Machines = 10_000
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestFile), encoded, 0o600))

	require.Error(t, assemble(dir, contract, testSeries(), items, sources, at),
		"a raised floor must survive the next refresh")
}
