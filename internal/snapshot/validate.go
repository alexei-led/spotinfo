package snapshot

import (
	"fmt"

	"spotinfo/internal/cloud"
)

// The coverage dimensions a snapshot is measured on.
const (
	dimensionRegions  = "regions"
	dimensionMachines = "machines"
	dimensionPrices   = "prices"
)

// PriceRecord is one price a snapshot publishes, in the neutral vocabulary.
// Providers flatten their parsed catalogue into these so a single validator
// covers every provider's amount, currency, duplicate, and coverage rules.
//
// An unknown price has no record. A zero amount is therefore always a defect:
// it is how a frozen or partially parsed feed presents itself.
type PriceRecord struct {
	Region   cloud.Region
	Machine  cloud.MachineID
	OS       cloud.OperatingSystem
	Class    cloud.PriceClass
	Currency cloud.Currency
	Unit     cloud.BillingUnit
	Amount   cloud.Money
}

// priceKey is the identity of a price: one machine, in one region, for one OS
// and purchase class. A snapshot may publish each of these exactly once.
type priceKey struct {
	region  cloud.Region
	machine cloud.MachineID
	os      cloud.OperatingSystem
	class   cloud.PriceClass
}

// ValidatePrices checks parsed price records against the manifest that describes
// them, then checks that they still cover the manifest's floor.
func ValidatePrices(records []PriceRecord, manifest *Manifest) error {
	if !manifest.Kind.IsPrice() {
		return fmt.Errorf("%w: kind %q does not carry prices", ErrInvalidManifest, manifest.Kind)
	}

	seen := make(map[priceKey]struct{}, len(records))
	regions := make(map[cloud.Region]struct{})
	machines := make(map[cloud.MachineID]struct{})

	for i := range records {
		record := &records[i]
		if err := record.validate(manifest); err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}

		key := priceKey{region: record.Region, machine: record.Machine, os: record.OS, class: record.Class}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: %s/%s/%s/%s appears more than once",
				ErrDuplicateRecord, record.Region, record.Machine, record.OS, record.Class)
		}

		seen[key] = struct{}{}
		regions[record.Region] = struct{}{}
		machines[record.Machine] = struct{}{}
	}

	actual := Coverage{Regions: len(regions), Machines: len(machines), Prices: len(records)}

	return ValidateCoverage(actual, manifest.MinRecords)
}

// ValidateCoverage reports whether a parsed snapshot still carries at least as
// much data as its manifest requires. It is the guard against a source that
// returns HTTP 200 with a truncated or partially rendered document.
func ValidateCoverage(actual, floor Coverage) error {
	for _, dimension := range []struct {
		name   string
		actual int
		floor  int
	}{
		{name: dimensionRegions, actual: actual.Regions, floor: floor.Regions},
		{name: dimensionMachines, actual: actual.Machines, floor: floor.Machines},
		{name: dimensionPrices, actual: actual.Prices, floor: floor.Prices},
	} {
		if dimension.actual < dimension.floor {
			return fmt.Errorf("%w: %d %s, manifest requires at least %d",
				ErrCoverage, dimension.actual, dimension.name, dimension.floor)
		}
	}

	return nil
}

func (r *PriceRecord) validate(manifest *Manifest) error {
	if r.Region == "" || r.Machine == "" {
		return fmt.Errorf("%w: region and machine are required", ErrInvalidManifest)
	}

	if r.OS != cloud.OSLinux && r.OS != cloud.OSWindows {
		return fmt.Errorf("%w: unknown operating system %q", ErrInvalidManifest, r.OS)
	}

	if r.Class != cloud.PriceClassSpot && r.Class != cloud.PriceClassOnDemand {
		return fmt.Errorf("%w: unknown price class %q", ErrInvalidManifest, r.Class)
	}

	if r.Currency != manifest.Currency || r.Unit != manifest.BillingUnit {
		return fmt.Errorf("%w: %s/%s is priced in %s per %s, manifest declares %s per %s",
			ErrInvalidManifest, r.Region, r.Machine, r.Currency, r.Unit, manifest.Currency, manifest.BillingUnit)
	}

	// Money is non-negative by construction, so zero is the only unusable value
	// left — and it is exactly what a frozen feed publishes for a machine it no
	// longer knows about.
	if r.Amount.IsZero() {
		return fmt.Errorf("%w: %s/%s has no usable price", ErrInvalidManifest, r.Region, r.Machine)
	}

	return nil
}
