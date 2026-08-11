package azure

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

func testSeries() []SeriesSpec {
	return []SeriesSpec{
		{
			Series:       "dsv5",
			Architecture: cloud.ArchitectureX8664,
			Sizes: []SizeSpec{
				{ID: "Standard_D2s_v5", VCPU: 2, MemoryGiB: 8},
				{ID: "Standard_D4s_v5", VCPU: 4, MemoryGiB: 16},
			},
		},
		{
			Series:       "dpsv5",
			Architecture: cloud.ArchitectureARM64,
			Sizes:        []SizeSpec{{ID: "Standard_D2ps_v5", VCPU: 2, MemoryGiB: 8}},
		},
	}
}

// testRows prices three sizes in two regions for Linux, and the one x86 size
// Azure also licenses for Windows in both. The Windows list price carries the
// licence, so it is far above every Linux figure — which is what makes a
// cross-OS pairing visible rather than plausible.
func testRows(t *testing.T) []PriceRow {
	t.Helper()

	rows := make([]PriceRow, 0, 16)
	for _, region := range []cloud.Region{"eastus", "westeurope"} {
		for _, priced := range []struct {
			machine cloud.MachineID
			os      cloud.OperatingSystem
			spot    string
			list    string
		}{
			{machine: "Standard_D2s_v5", os: cloud.OSLinux, spot: "0.0412", list: "0.096"},
			{machine: "Standard_D4s_v5", os: cloud.OSLinux, spot: "0.0824", list: "0.192"},
			{machine: "Standard_D2ps_v5", os: cloud.OSLinux, spot: "0.0308", list: "0.077"},
			{machine: "Standard_D2s_v5", os: cloud.OSWindows, spot: "0.1783", list: "0.216"},
		} {
			rows = append(rows,
				PriceRow{Machine: priced.machine, Region: region, OS: priced.os,
					Class: cloud.PriceClassSpot, Amount: money(t, priced.spot)},
				PriceRow{Machine: priced.machine, Region: region, OS: priced.os,
					Class: cloud.PriceClassOnDemand, Amount: money(t, priced.list)},
			)
		}
	}

	return rows
}

func testContract() *snapshot.SourceContract {
	return &snapshot.SourceContract{
		Provider:      cloud.ProviderAzure,
		ParserVersion: ParserVersion,
		Support: snapshot.Support{
			RiskStatus:       cloud.RiskStatusUnavailable,
			OperatingSystems: []cloud.OperatingSystem{cloud.OSLinux, cloud.OSWindows},
			Architectures:    []cloud.Architecture{cloud.ArchitectureX8664, cloud.ArchitectureARM64},
			PriceClasses:     []cloud.PriceClass{cloud.PriceClassSpot, cloud.PriceClassOnDemand},
			Regions:          []cloud.Region{"eastus", "westeurope"},
			MachineSeries:    []string{"dpsv5", "dsv5"},
		},
		Thresholds: snapshot.Thresholds{
			MinRegions:          2,
			MinMachines:         3,
			MaxCompressedBytes:  65536,
			MaxFractionalDigits: 6,
		},
	}
}

func buildTestCatalog(t *testing.T) *Catalog {
	t.Helper()

	catalog, report, err := BuildCatalog(testSeries(), testRows(t))
	require.NoError(t, err)
	require.Empty(t, report.Unpaired)
	require.Empty(t, report.Unspecified)

	return catalog
}

func TestBuildCatalogStoresSpecificationsOnceAndPricesPerRegion(t *testing.T) {
	t.Parallel()

	catalog := buildTestCatalog(t)

	require.Len(t, catalog.Machines, 3)
	require.Len(t, catalog.Regions, 2)
	assert.Equal(t, cloud.Region("eastus"), catalog.Regions[0].ID, "regions are sorted")
	assert.Equal(t, cloud.MachineID("Standard_D2ps_v5"), catalog.Machines[0].ID, "machines are sorted")
	assert.Equal(t, cloud.ArchitectureARM64, catalog.Machines[0].Architecture,
		"architecture travels from the series page, not the size name")
	assert.Equal(t, []string{"dpsv5", "dsv5"}, catalog.Series())
	assert.Equal(t, []cloud.OperatingSystem{cloud.OSLinux, cloud.OSWindows}, catalog.OperatingSystems)
	assert.Equal(t, snapshot.Coverage{Regions: 2, Machines: 3, Prices: 16}, catalog.Coverage())
}

// TestOneSizeCarriesBothItsLinuxAndItsWindowsPrice is the row shape the key
// change exists for: one machine, one region, two operating systems, two
// separate pairs of prices.
func TestOneSizeCarriesBothItsLinuxAndItsWindowsPrice(t *testing.T) {
	t.Parallel()

	catalog := buildTestCatalog(t)

	priced := make(map[cloud.OperatingSystem]CatalogPrice)
	for _, price := range catalog.Regions[0].Prices {
		if price.Machine == "Standard_D2s_v5" {
			priced[price.OS] = price
		}
	}

	require.Len(t, priced, 2, "both meters survive; neither overwrites the other")
	assert.Equal(t, "0.041200000", priced[cloud.OSLinux].Spot.String())
	assert.Equal(t, "0.096000000", priced[cloud.OSLinux].OnDemand.String())
	assert.Equal(t, "0.178300000", priced[cloud.OSWindows].Spot.String())
	assert.Equal(t, "0.216000000", priced[cloud.OSWindows].OnDemand.String())
}

// TestAWindowsPriceIsNeverASavingAgainstALinuxPrice pins the pairing rule. The
// Windows Spot price here is above every Linux figure, so a machine-only join
// would either publish a negative discount or pair the Linux list price and
// report the licence as spare-capacity savings.
func TestAWindowsPriceIsNeverASavingAgainstALinuxPrice(t *testing.T) {
	t.Parallel()

	catalog := buildTestCatalog(t)

	for i := range catalog.Regions {
		for _, price := range catalog.Regions[i].Prices {
			if price.Machine != "Standard_D2s_v5" || price.OS != cloud.OSWindows {
				continue
			}

			saved := price.SavingsPercent()
			require.NotNil(t, saved)
			assert.Equal(t, 17, *saved, "17%% is the windows pair; the linux list price would give 46%%")
		}
	}
}

// TestAWindowsSpotPriceWithNoWindowsListPriceIsNotPublished is the same rule at
// its sharpest: the region carries a Linux list price for the size, and it must
// not be borrowed as the denominator.
func TestAWindowsSpotPriceWithNoWindowsListPriceIsNotPublished(t *testing.T) {
	t.Parallel()

	rows := []PriceRow{
		{Machine: "Standard_D2s_v5", Region: "eastus", OS: cloud.OSLinux,
			Class: cloud.PriceClassSpot, Amount: money(t, "0.0412")},
		{Machine: "Standard_D2s_v5", Region: "eastus", OS: cloud.OSLinux,
			Class: cloud.PriceClassOnDemand, Amount: money(t, "0.096")},
		{Machine: "Standard_D2s_v5", Region: "eastus", OS: cloud.OSWindows,
			Class: cloud.PriceClassSpot, Amount: money(t, "0.1783")},
	}

	catalog, report, err := BuildCatalog(testSeries(), rows)
	require.NoError(t, err)

	assert.Equal(t, []string{"eastus/Standard_D2s_v5/windows"}, report.Unpaired)
	require.Len(t, catalog.Regions, 1)
	require.Len(t, catalog.Regions[0].Prices, 1)
	assert.Equal(t, cloud.OSLinux, catalog.Regions[0].Prices[0].OS)
	assert.Equal(t, []cloud.OperatingSystem{cloud.OSLinux}, catalog.OperatingSystems,
		"the catalogue declares what it prices, not what it read")
}

func TestASpotPriceWithNoListPriceIsNotPublished(t *testing.T) {
	t.Parallel()

	rows := []PriceRow{
		{Machine: "Standard_D2s_v5", Region: "eastus", OS: cloud.OSLinux,
			Class: cloud.PriceClassSpot, Amount: money(t, "0.0412")},
	}

	catalog, report, err := BuildCatalog(testSeries(), rows)
	require.NoError(t, err)

	assert.Equal(t, []string{"eastus/Standard_D2s_v5/linux"}, report.Unpaired,
		"a savings figure with no denominator is reported, not published")
	assert.Empty(t, catalog.Machines)
	assert.Empty(t, catalog.Regions)
}

// TestPricedMachinesOutsideTheReviewedMatrixAreReported covers the drop the
// join cannot avoid: without a Learn page there is no architecture, vCPU or
// memory figure, so the size cannot be published — but it is named rather than
// dropped in silence, because callers filter with RetainSpecified first and a
// name here means the two disagree.
func TestPricedMachinesOutsideTheReviewedMatrixAreReported(t *testing.T) {
	t.Parallel()

	rows := append(testRows(t),
		PriceRow{Machine: "Standard_NC40ads_H100_v5", Region: "eastus", OS: cloud.OSLinux,
			Class: cloud.PriceClassSpot, Amount: money(t, "3.5")},
		PriceRow{Machine: "Standard_NC40ads_H100_v5", Region: "eastus", OS: cloud.OSLinux,
			Class: cloud.PriceClassOnDemand, Amount: money(t, "6.98")},
	)

	catalog, report, err := BuildCatalog(testSeries(), rows)
	require.NoError(t, err)

	assert.Empty(t, report.Unpaired, "an unspecified size is out of scope, not an unpaired one")
	assert.Equal(t, []cloud.MachineID{"Standard_NC40ads_H100_v5"}, report.Unspecified)
	assert.Len(t, catalog.Machines, 3)
}

func TestRetainSpecifiedDropsOutOfScopeObservations(t *testing.T) {
	t.Parallel()

	observations := []Observation{
		{Machine: "Standard_D2s_v5", Region: "eastus", Class: cloud.PriceClassSpot, Amount: money(t, "0.04")},
		{Machine: "Standard_NC40ads_H100_v5", Region: "eastus", Class: cloud.PriceClassSpot, Amount: money(t, "3.5")},
	}

	retained := RetainSpecified(observations, testSeries())

	require.Len(t, retained, 1)
	assert.Equal(t, cloud.MachineID("Standard_D2s_v5"), retained[0].Machine)
}

func TestOneSizeDocumentedByTwoSeriesIsAmbiguous(t *testing.T) {
	t.Parallel()

	series := append(testSeries(), SeriesSpec{
		Series:       "dasv5",
		Architecture: cloud.ArchitectureX8664,
		Sizes:        []SizeSpec{{ID: "Standard_D2s_v5", VCPU: 2, MemoryGiB: 8}},
	})

	_, _, err := BuildCatalog(series, testRows(t))

	require.ErrorIs(t, err, ErrSourceContract)
	assert.Contains(t, err.Error(), "Standard_D2s_v5")
}

// TestTheRegionFloorCountsMachinesRatherThanRows keeps the per-region floor at
// the strength it had when each machine was one row. Keying by OS roughly
// doubled the row count, so a row-count floor would let a region lose half its
// sizes and still clear a number that was chosen against the old shape.
func TestTheRegionFloorCountsMachinesRatherThanRows(t *testing.T) {
	t.Parallel()

	catalog, contract := buildTestCatalog(t), testContract()
	region := &catalog.Regions[1]

	kept := make([]CatalogPrice, 0, len(region.Prices))
	for _, price := range region.Prices {
		if price.Machine != "Standard_D4s_v5" {
			kept = append(kept, price)
		}
	}
	region.Prices = kept

	require.Len(t, region.Prices, contract.Thresholds.MinMachines, "the row count still clears the floor")

	err := catalog.Verify(contract)

	require.ErrorIs(t, err, ErrCatalog)
	assert.Contains(t, err.Error(), "prices 2 machines, contract requires at least 3")
}

// TestOneSizePricedTwiceForOneOperatingSystemIsADuplicate keeps the check the
// wider key could have blunted: two rows for one machine in one region are
// still a duplicate when they name the same OS, and only then.
func TestOneSizePricedTwiceForOneOperatingSystemIsADuplicate(t *testing.T) {
	t.Parallel()

	catalog := buildTestCatalog(t)
	region := &catalog.Regions[0]
	repeated := region.Prices[0]
	region.Prices = append(region.Prices, repeated)

	err := catalog.Verify(testContract())

	require.ErrorIs(t, err, ErrCatalog)
	assert.Contains(t, err.Error(), "priced twice")
	assert.Contains(t, err.Error(), string(repeated.OS))
}

func TestCatalogRoundTripsThroughItsCommittedForm(t *testing.T) {
	t.Parallel()

	catalog := buildTestCatalog(t)

	encoded, err := catalog.Encode()
	require.NoError(t, err)

	decoded, err := DecodeCatalog(encoded)
	require.NoError(t, err)
	assert.Equal(t, catalog, decoded)

	_, err = DecodeCatalog([]byte(`{"schema_version":"spotinfo.azure-catalog/v2","surprise":1}`))
	require.ErrorIs(t, err, ErrCatalog, "an unknown key must fail rather than decode as a zero value")

	_, err = DecodeCatalog(append(encoded, []byte(`{}`)...))
	require.ErrorIs(t, err, ErrCatalog, "trailing JSON must fail closed")
}

func TestVerifyAcceptsAnInContractCatalogue(t *testing.T) {
	t.Parallel()

	require.NoError(t, buildTestCatalog(t).Verify(testContract()))
}

// TestVerifyRejectsEveryOutOfContractCatalogue walks the ways a committed
// catalogue can contradict the contract that approved it.
func TestVerifyRejectsEveryOutOfContractCatalogue(t *testing.T) {
	t.Parallel()

	for name, corrupt := range map[string]func(*Catalog, *snapshot.SourceContract){
		"schema version": func(c *Catalog, _ *snapshot.SourceContract) { c.SchemaVersion = "spotinfo.azure-catalog/v1" },
		"unapproved operating system": func(c *Catalog, _ *snapshot.SourceContract) {
			c.OperatingSystems = append(c.OperatingSystems, "plan9")
		},
		"approved operating system with no price": func(_ *Catalog, contract *snapshot.SourceContract) {
			contract.Support.OperatingSystems = append(contract.Support.OperatingSystems, "plan9")
		},
		"price for an undeclared operating system": func(c *Catalog, _ *snapshot.SourceContract) {
			c.Regions[0].Prices[0].OS = "plan9"
		},
		"currency": func(c *Catalog, _ *snapshot.SourceContract) { c.Currency = "EUR" },
		"unapproved series": func(c *Catalog, _ *snapshot.SourceContract) {
			c.Machines[0].Series = "hbv4"
		},
		"unapproved architecture": func(_ *Catalog, contract *snapshot.SourceContract) {
			contract.Support.Architectures = []cloud.Architecture{cloud.ArchitectureX8664}
		},
		"unapproved region": func(c *Catalog, _ *snapshot.SourceContract) {
			c.Regions[0].ID = "nowhere-north-1"
		},
		"missing specification": func(c *Catalog, _ *snapshot.SourceContract) {
			c.Machines = c.Machines[1:]
		},
		"unpriced specification": func(c *Catalog, _ *snapshot.SourceContract) {
			c.Machines = append(c.Machines, CatalogMachine{
				ID: "Standard_D8s_v5", Series: "dsv5",
				Architecture: cloud.ArchitectureX8664, VCPU: 8, MemoryGiB: 32,
			})
		},
		"no usable specification": func(c *Catalog, _ *snapshot.SourceContract) { c.Machines[0].VCPU = 0 },
		"spot at or above list": func(c *Catalog, _ *snapshot.SourceContract) {
			c.Regions[0].Prices[0].Spot = c.Regions[0].Prices[0].OnDemand
		},
		"region below the floor": func(c *Catalog, _ *snapshot.SourceContract) {
			c.Regions[1].Prices = c.Regions[1].Prices[:1]
		},
		"too few regions":  func(c *Catalog, _ *snapshot.SourceContract) { c.Regions = c.Regions[:1] },
		"duplicate region": func(c *Catalog, _ *snapshot.SourceContract) { c.Regions[1].ID = c.Regions[0].ID },
		"too much precision": func(_ *Catalog, contract *snapshot.SourceContract) {
			contract.Thresholds.MaxFractionalDigits = 2
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			catalog, contract := buildTestCatalog(t), testContract()
			corrupt(catalog, contract)

			assert.Error(t, catalog.Verify(contract))
		})
	}
}

// TestPriceRecordsSatisfyTheNeutralValidator runs the shared snapshot validator
// over the flattened catalogue, which is what the update gate and the embedded
// loader both do.
func TestPriceRecordsSatisfyTheNeutralValidator(t *testing.T) {
	t.Parallel()

	catalog := buildTestCatalog(t)
	records := catalog.PriceRecords()
	require.Len(t, records, 16)

	manifest := &snapshot.Manifest{
		Kind:        snapshot.KindSpotPrice,
		Currency:    cloud.CurrencyUSD,
		BillingUnit: cloud.BillingUnitInstanceHour,
		MinRecords:  snapshot.Coverage{Regions: 2, Machines: 3, Prices: 16},
	}

	// The neutral validator keys on the OS too, so the four Windows records for
	// a size that also has Linux prices are distinct rows rather than duplicates.
	require.NoError(t, snapshot.ValidatePrices(records, manifest))

	manifest.MinRecords.Prices = 17
	assert.ErrorIs(t, snapshot.ValidatePrices(records, manifest), snapshot.ErrCoverage)
}

func TestSavingsPercentNeedsABelievableDenominator(t *testing.T) {
	t.Parallel()

	for name, price := range map[string]CatalogPrice{
		"normal":       {Spot: money(t, "0.0412"), OnDemand: money(t, "0.096")},
		"no list":      {Spot: money(t, "0.0412")},
		"spot is list": {Spot: money(t, "0.096"), OnDemand: money(t, "0.096")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			saved := price.SavingsPercent()
			if name == "normal" {
				require.NotNil(t, saved)
				assert.Equal(t, 57, *saved)

				return
			}
			assert.Nil(t, saved)
		})
	}
}
