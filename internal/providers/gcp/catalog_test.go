package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/snapshot"
)

func money(t *testing.T, text string) cloud.Money {
	t.Helper()

	amount, err := cloud.ParseMoney(text)
	require.NoError(t, err)

	return amount
}

func row(t *testing.T, id, price string, vcpu int, memoryGiB float64) MachineRow {
	t.Helper()

	return MachineRow{ID: cloud.MachineID(id), VCPU: vcpu, MemoryGiB: memoryGiB, Price: money(t, price)}
}

// testSeries is the small approved support matrix these tests build catalogues
// against: one x86_64 series and one Arm series, named by the machines below so
// the two lists cannot drift apart.
var testSeries = []string{SeriesOf("c4-standard-2"), SeriesOf("t2a-standard-4")}

// testContract mirrors the committed contract's shape with a small support
// matrix, so a test can put a catalogue outside it deliberately.
func testContract() *snapshot.SourceContract {
	return &snapshot.SourceContract{
		SchemaVersion: snapshot.SourceContractSchemaVersion,
		Provider:      cloud.ProviderGCP,
		ParserVersion: ParserVersion,
		Support: snapshot.Support{
			RiskStatus:       cloud.RiskStatusUnavailable,
			OperatingSystems: []cloud.OperatingSystem{cloud.OSLinux},
			Architectures:    []cloud.Architecture{cloud.ArchitectureX8664, cloud.ArchitectureARM64},
			PriceClasses:     []cloud.PriceClass{cloud.PriceClassSpot, cloud.PriceClassOnDemand},
			Regions:          []cloud.Region{contractedRegion},
			MachineSeries:    testSeries,
		},
		Thresholds: snapshot.Thresholds{
			MinRegions: 1, MinMachines: 1, MaxCompressedBytes: 1 << 16, MaxFractionalDigits: 6,
		},
	}
}

func buildTestCatalog(t *testing.T) *Catalog {
	t.Helper()

	catalog, unpaired, err := BuildCatalog(contractedRegion,
		[]MachineRow{row(t, "c4-standard-2", "0.058121", 2, 7), row(t, "t2a-standard-4", "0.031500", 4, 16)},
		[]MachineRow{row(t, "c4-standard-2", "0.117660", 2, 7), row(t, "t2a-standard-4", "0.154", 4, 16)})
	require.NoError(t, err)
	require.Empty(t, unpaired)

	return catalog
}

func TestBuildCatalogJoinsSpotWithItsOnDemandPair(t *testing.T) {
	t.Parallel()

	catalog := buildTestCatalog(t)

	require.Len(t, catalog.Machines, 2)
	assert.Equal(t, cloud.MachineID("c4-standard-2"), catalog.Machines[0].ID, "machines are sorted by identifier")
	assert.Equal(t, "0.058121000", catalog.Machines[0].Spot.String())
	assert.Equal(t, "0.117660000", catalog.Machines[0].OnDemand.String())
	assert.Equal(t, cloud.ArchitectureARM64, catalog.Machines[1].Architecture)
	assert.Equal(t, cloud.OSLinux, catalog.OS)
	assert.Equal(t, snapshot.Coverage{Regions: 1, Machines: 2, Prices: 4}, catalog.Coverage())
	assert.Equal(t, testSeries, catalog.Series())
}

func TestBuildCatalogLeavesOutASpotMachineWithNoOnDemandPair(t *testing.T) {
	t.Parallel()

	catalog, unpaired, err := BuildCatalog(contractedRegion,
		[]MachineRow{row(t, "c4-standard-2", "0.058121", 2, 7), row(t, "e2-orphan-2", "0.011", 2, 8)},
		[]MachineRow{row(t, "c4-standard-2", "0.117660", 2, 7), row(t, "m2-ondemandonly-16", "1.0", 16, 128)})
	require.NoError(t, err)

	assert.Equal(t, []cloud.MachineID{"e2-orphan-2"}, unpaired)
	require.Len(t, catalog.Machines, 1)
	assert.Equal(t, cloud.MachineID("c4-standard-2"), catalog.Machines[0].ID,
		"an on-demand-only machine is never published as a spot candidate")
}

func TestBuildCatalogRejectsTwoDifferentPricesForOneMachine(t *testing.T) {
	t.Parallel()

	_, _, err := BuildCatalog(contractedRegion,
		[]MachineRow{row(t, "c4-standard-2", "0.058121", 2, 7), row(t, "c4-standard-2", "0.06", 2, 7)},
		[]MachineRow{row(t, "c4-standard-2", "0.117660", 2, 7)})
	require.ErrorIs(t, err, ErrSourceContract)
}

func TestBuildCatalogRejectsTwoDifferentSpecificationsForOneMachine(t *testing.T) {
	t.Parallel()

	_, _, err := BuildCatalog(contractedRegion,
		[]MachineRow{row(t, "c4-standard-2", "0.058121", 2, 7), row(t, "c4-standard-2", "0.058121", 4, 15)},
		[]MachineRow{row(t, "c4-standard-2", "0.117660", 2, 7)})
	require.ErrorIs(t, err, ErrSourceContract)
}

func TestBuildCatalogRejectsSpotAndOnDemandSpecificationMismatch(t *testing.T) {
	t.Parallel()

	_, _, err := BuildCatalog(contractedRegion,
		[]MachineRow{row(t, "c4-standard-2", "0.058121", 2, 7)},
		[]MachineRow{row(t, "c4-standard-2", "0.117660", 4, 15)})
	require.ErrorIs(t, err, ErrSourceContract)
}

func TestBuildCatalogAcceptsAnExactRepeat(t *testing.T) {
	t.Parallel()

	catalog, _, err := BuildCatalog(contractedRegion,
		[]MachineRow{row(t, "c4-standard-2", "0.058121", 2, 7), row(t, "c4-standard-2", "0.058121", 2, 7)},
		[]MachineRow{row(t, "c4-standard-2", "0.117660", 2, 7)})
	require.NoError(t, err)
	assert.Len(t, catalog.Machines, 1)
}

func TestSavingsPercentNeedsAReadableDenominator(t *testing.T) {
	t.Parallel()

	saved := (&CatalogMachine{Spot: money(t, "0.25"), OnDemand: money(t, "1.0")}).SavingsPercent()
	require.NotNil(t, saved)
	assert.Equal(t, 75, *saved)

	assert.Nil(t, (&CatalogMachine{Spot: money(t, "1.0"), OnDemand: money(t, "1.0")}).SavingsPercent())
	assert.Nil(t, (&CatalogMachine{Spot: money(t, "0.5")}).SavingsPercent())
}

func TestCatalogRoundTripsThroughItsCommittedEncoding(t *testing.T) {
	t.Parallel()

	catalog := buildTestCatalog(t)

	encoded, err := catalog.Encode()
	require.NoError(t, err)

	decoded, err := DecodeCatalog(encoded)
	require.NoError(t, err)
	assert.Equal(t, catalog, decoded)
}

func TestDecodeCatalogRejectsAnUnknownField(t *testing.T) {
	t.Parallel()

	_, err := DecodeCatalog([]byte(`{"schema_version":"spotinfo.gcp-catalog/v1","surprise":1}`))
	require.ErrorIs(t, err, ErrCatalog)
}

func TestDecodeCatalogRejectsTrailingJSON(t *testing.T) {
	t.Parallel()

	catalog, err := buildTestCatalog(t).Encode()
	require.NoError(t, err)

	_, err = DecodeCatalog(append(catalog, []byte(`{}`)...))
	require.ErrorIs(t, err, ErrCatalog)
}

func TestVerifyRejectsACatalogueOutsideItsContract(t *testing.T) {
	t.Parallel()

	// Every Verify failure wraps ErrCatalog, so the sentinel alone cannot tell
	// one check from another: a case that trips an earlier check than the one it
	// names still passes. Each case therefore also pins the message.
	for name, test := range map[string]struct {
		corrupt func(*Catalog)
		message string
	}{
		"wrong schema version": {
			corrupt: func(c *Catalog) { c.SchemaVersion = "spotinfo.gcp-catalog/v0" },
			message: "schema",
		},
		"unapproved region": {
			corrupt: func(c *Catalog) { c.Region = "europe-west1" },
			message: "europe-west1",
		},
		"unapproved os": {
			corrupt: func(c *Catalog) { c.OS = cloud.OSWindows },
			message: "windows",
		},
		"wrong currency": {
			corrupt: func(c *Catalog) { c.Currency = "EUR" },
			message: "EUR",
		},
		"empty": {
			corrupt: func(c *Catalog) { c.Machines = nil },
			message: "machines",
		},
		"not a machine identifier": {
			corrupt: func(c *Catalog) { c.Machines[0].ID = "Not A Machine" },
			message: "is not a machine type identifier",
		},
		"unapproved series": {
			corrupt: func(c *Catalog) { c.Machines[0].ID = "z3-standard-2" },
			message: "unapproved series",
		},
		"architecture outside the contract": {
			corrupt: func(c *Catalog) { c.Machines[0].Architecture = "riscv64" },
			message: "unapproved architecture",
		},
		"architecture contradicts the series": {
			corrupt: func(c *Catalog) { c.Machines[0].Architecture = cloud.ArchitectureARM64 },
			message: "is recorded as",
		},
		"missing price": {
			corrupt: func(c *Catalog) { c.Machines[0].OnDemand = cloud.Money{} },
			message: "missing a spot or on-demand price",
		},
		"spot at or above list price": {
			corrupt: func(c *Catalog) { c.Machines[0].Spot = c.Machines[0].OnDemand },
			message: "spot against",
		},
		"more precision than the contract allows": {
			// Below the machine's 0.117660 list price on purpose: a higher amount
			// would trip the spot-at-or-above-list check first and never reach the
			// fractional-digit gate this case exists to prove.
			corrupt: func(c *Catalog) { c.Machines[0].Spot = money(t, "0.058121789") },
			message: "fractional digits",
		},
		"duplicate machine": {
			corrupt: func(c *Catalog) { c.Machines = append(c.Machines, c.Machines[0]) },
			message: "twice",
		},
		"missing specification": {
			corrupt: func(c *Catalog) { c.Machines[0].VCPU = 0 },
			message: "no usable specification",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			catalog := buildTestCatalog(t)
			test.corrupt(catalog)

			err := catalog.Verify(testContract())
			require.ErrorIs(t, err, ErrCatalog)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestVerifyAcceptsTheCommittedShape(t *testing.T) {
	t.Parallel()

	require.NoError(t, buildTestCatalog(t).Verify(testContract()))
}

func TestPriceRecordsPublishBothClassesPerMachine(t *testing.T) {
	t.Parallel()

	records := buildTestCatalog(t).PriceRecords()
	require.Len(t, records, 4)

	classes := map[cloud.PriceClass]int{}
	for _, record := range records {
		classes[record.Class]++
		assert.Equal(t, contractedRegion, record.Region)
		assert.Equal(t, cloud.OSLinux, record.OS)
		assert.Equal(t, cloud.CurrencyUSD, record.Currency)
	}
	assert.Equal(t, map[cloud.PriceClass]int{cloud.PriceClassSpot: 2, cloud.PriceClassOnDemand: 2}, classes)
}
