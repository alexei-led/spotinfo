package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/providers/azure"
)

const dataDir = "../../internal/providers/azure/data"

func testCatalog(t *testing.T) *azure.Catalog {
	t.Helper()

	spot, err := cloud.ParseMoney("0.041200")
	require.NoError(t, err)
	onDemand, err := cloud.ParseMoney("0.096000")
	require.NoError(t, err)

	catalog, unpaired, err := azure.BuildCatalog(
		[]azure.SeriesSpec{{
			Series:       "dsv5",
			Architecture: cloud.ArchitectureX8664,
			Sizes:        []azure.SizeSpec{{ID: "Standard_D2s_v5", VCPU: 2, MemoryGiB: 8}},
		}},
		[]azure.PriceRow{
			{Machine: "Standard_D2s_v5", Region: "eastus", Class: cloud.PriceClassSpot, Amount: spot},
			{Machine: "Standard_D2s_v5", Region: "eastus", Class: cloud.PriceClassOnDemand, Amount: onDemand},
		})
	require.NoError(t, err)
	require.Empty(t, unpaired)

	return catalog
}

// The gzip header carries a modification time and an OS byte by default, which
// would give unchanged data a different hash on every run and make the manifest
// gate fire on a no-op refresh.
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

	floor := coverageFloor(t.TempDir(), contract)

	assert.Equal(t, contract.Thresholds.MinRegions, floor.Regions)
	assert.Equal(t, contract.Thresholds.MinMachines, floor.Machines)
	assert.Positive(t, floor.Prices)
}

// The committed floor is hand-curated review evidence. Deriving it from the
// data that just arrived would ratchet the gate into always passing.
func TestCoverageFloorKeepsTheReviewedManifestFloor(t *testing.T) {
	t.Parallel()

	contract, err := loadContract(dataDir)
	require.NoError(t, err)

	floor := coverageFloor(dataDir, contract)

	assert.GreaterOrEqual(t, floor.Regions, contract.Thresholds.MinRegions)
	assert.GreaterOrEqual(t, floor.Machines, contract.Thresholds.MinMachines)
	assert.Positive(t, floor.Prices)
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
		series, seriesErr := azure.SeriesFromURL(contract.Sources[i].URL)
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

func TestPriceAPIBaseIsTheContractedRESTSource(t *testing.T) {
	t.Parallel()

	contract, err := loadContract(dataDir)
	require.NoError(t, err)

	base, err := priceAPIBase(contract)
	require.NoError(t, err)
	assert.Equal(t, "https://prices.azure.com/api/retail/prices", base)
}
