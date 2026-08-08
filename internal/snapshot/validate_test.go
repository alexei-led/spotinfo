package snapshot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

func money(t *testing.T, amount string) cloud.Money {
	t.Helper()

	parsed, err := cloud.ParseMoney(amount)
	require.NoError(t, err)

	return parsed
}

// priceManifest is a floor-free manifest, so a coverage failure in a record test
// is always the fault of the record under test.
func priceManifest(t *testing.T) *Manifest {
	t.Helper()

	manifest := validManifest(t)
	manifest.MinRecords = Coverage{Regions: 1, Machines: 1, Prices: 1}

	return manifest
}

func record(t *testing.T, region cloud.Region, machine cloud.MachineID, amount string) PriceRecord {
	t.Helper()

	return PriceRecord{
		Region:   region,
		Machine:  machine,
		OS:       cloud.OSLinux,
		Class:    cloud.PriceClassSpot,
		Currency: cloud.CurrencyUSD,
		Unit:     cloud.BillingUnitInstanceHour,
		Amount:   money(t, amount),
	}
}

func TestValidatePricesAcceptsDistinctPricedRecords(t *testing.T) {
	t.Parallel()

	records := []PriceRecord{
		record(t, "us-central1", "n2-standard-2", "0.0212"),
		record(t, "us-central1", "n2-standard-4", "0.0424"),
		record(t, "europe-west1", "n2-standard-2", "0.0231"),
	}

	require.NoError(t, ValidatePrices(records, priceManifest(t)))
}

func TestValidatePricesFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		wantErr error
		name    string
		records []PriceRecord
		floor   Coverage
	}{
		{
			name:    "no region",
			records: []PriceRecord{record(t, "", "n2-standard-2", "0.0212")},
			wantErr: ErrInvalidManifest,
		},
		{
			name:    "no machine",
			records: []PriceRecord{record(t, "us-central1", "", "0.0212")},
			wantErr: ErrInvalidManifest,
		},
		{
			name: "a frozen feed's zero price",
			// The AWS pricing feed publishes 0 for machines it no longer knows
			// about. An unknown price must be an absent record, never a free one.
			records: []PriceRecord{record(t, "us-central1", "n2-standard-2", "0")},
			wantErr: ErrInvalidManifest,
		},
		{
			name: "the same price twice",
			records: []PriceRecord{
				record(t, "us-central1", "n2-standard-2", "0.0212"),
				record(t, "us-central1", "n2-standard-2", "0.0299"),
			},
			wantErr: ErrDuplicateRecord,
		},
		{
			name:    "coverage below the manifest floor",
			records: []PriceRecord{record(t, "us-central1", "n2-standard-2", "0.0212")},
			floor:   Coverage{Regions: 5, Machines: 5, Prices: 5},
			wantErr: ErrCoverage,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			manifest := priceManifest(t)
			if tc.floor != (Coverage{}) {
				manifest.MinRecords = tc.floor
			}

			require.ErrorIs(t, ValidatePrices(tc.records, manifest), tc.wantErr)
		})
	}
}

func TestValidatePricesRejectsMismatchedDenomination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		currency cloud.Currency
		unit     cloud.BillingUnit
	}{
		{name: "another currency", currency: "EUR", unit: cloud.BillingUnitInstanceHour},
		{name: "another billing unit", currency: cloud.CurrencyUSD, unit: "month"},
		{name: "unknown operating system", currency: cloud.CurrencyUSD, unit: cloud.BillingUnitInstanceHour},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			entry := record(t, "us-central1", "n2-standard-2", "0.0212")
			entry.Currency = tc.currency
			entry.Unit = tc.unit

			if tc.name == "unknown operating system" {
				entry.OS = "plan9"
			}

			require.ErrorIs(t, ValidatePrices([]PriceRecord{entry}, priceManifest(t)), ErrInvalidManifest)
		})
	}
}

func TestValidatePricesRejectsANonPriceManifest(t *testing.T) {
	t.Parallel()

	manifest := priceManifest(t)
	manifest.Kind = KindMachineArchitecture

	err := ValidatePrices(nil, manifest)
	require.ErrorIs(t, err, ErrInvalidManifest)
	assert.Contains(t, err.Error(), "does not carry prices")
}

func TestValidateCoverageNamesTheMissingDimension(t *testing.T) {
	t.Parallel()

	floor := Coverage{Regions: 10, Machines: 20, Prices: 100}

	tests := []struct {
		name   string
		actual Coverage
		want   string
	}{
		{name: "at the floor", actual: floor},
		{name: "above the floor", actual: Coverage{Regions: 11, Machines: 21, Prices: 101}},
		{name: "short regions", actual: Coverage{Regions: 9, Machines: 20, Prices: 100}, want: "regions"},
		{name: "short machines", actual: Coverage{Regions: 10, Machines: 19, Prices: 100}, want: "machines"},
		{name: "short prices", actual: Coverage{Regions: 10, Machines: 20, Prices: 99}, want: "prices"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateCoverage(tc.actual, floor)
			if tc.want == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrCoverage)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
