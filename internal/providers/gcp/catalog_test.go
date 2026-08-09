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
			MachineSeries:    []string{"c4", "t2a"},
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
	assert.Equal(t, []string{"c4", "t2a"}, catalog.Series())
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

func TestVerifyRejectsACatalogueOutsideItsContract(t *testing.T) {
	t.Parallel()

	for name, corrupt := range map[string]func(*Catalog){
		"wrong schema version": func(c *Catalog) { c.SchemaVersion = "spotinfo.gcp-catalog/v0" },
		"unapproved region":    func(c *Catalog) { c.Region = "europe-west1" },
		"unapproved os":        func(c *Catalog) { c.OS = cloud.OSWindows },
		"wrong currency":       func(c *Catalog) { c.Currency = "EUR" },
		"empty":                func(c *Catalog) { c.Machines = nil },
		"unapproved series": func(c *Catalog) {
			c.Machines[0].ID = "z3-standard-2"
		},
		"architecture contradicts the series": func(c *Catalog) {
			c.Machines[0].Architecture = cloud.ArchitectureARM64
		},
		"spot at or above list price": func(c *Catalog) {
			c.Machines[0].Spot = c.Machines[0].OnDemand
		},
		"more precision than the contract allows": func(c *Catalog) {
			c.Machines[0].Spot = money(t, "0.123456789")
		},
		"duplicate machine": func(c *Catalog) {
			c.Machines = append(c.Machines, c.Machines[0])
		},
		"missing specification": func(c *Catalog) { c.Machines[0].VCPU = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			catalog := buildTestCatalog(t)
			corrupt(catalog)
			require.ErrorIs(t, catalog.Verify(testContract()), ErrCatalog)
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
