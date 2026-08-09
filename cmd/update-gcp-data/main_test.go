package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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

	body, err := os.ReadFile(filepath.Join("..", "..", "internal", "providers", "gcp", "testdata", name))
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
