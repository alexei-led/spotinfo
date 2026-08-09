package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/providers/gcp"
)

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

	contract, err := loadContract("../../internal/providers/gcp/data")
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

	dataDir := "../../internal/providers/gcp/data"

	contract, err := loadContract(dataDir)
	require.NoError(t, err)

	floor := coverageFloor(dataDir, contract)

	assert.GreaterOrEqual(t, floor.Machines, contract.Thresholds.MinMachines)
	assert.Positive(t, floor.Prices)
}
