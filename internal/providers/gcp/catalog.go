package gcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"

	"spotinfo/internal/cloud"
	"spotinfo/internal/snapshot"
)

const (
	// percentScale converts a price ratio to whole percent.
	percentScale = 100
	// pricesPerMachine is the two classes every catalogue row publishes.
	pricesPerMachine = 2
)

// Catalog is the committed GCP price catalogue: one region, one operating
// system, one row per machine type. Every row carries both a Spot and an
// On-Demand price, because a savings figure without its denominator is a number
// no consumer can check.
//
//nolint:govet // Metadata reads before the rows it describes in a review diff.
type Catalog struct {
	SchemaVersion string                `json:"schema_version"`
	Region        cloud.Region          `json:"region"`
	OS            cloud.OperatingSystem `json:"os"`
	Currency      cloud.Currency        `json:"currency"`
	BillingUnit   cloud.BillingUnit     `json:"billing_unit"`
	Machines      []CatalogMachine      `json:"machines"`
}

// CatalogMachine is one priced machine type.
type CatalogMachine struct {
	ID           cloud.MachineID    `json:"id"`
	Architecture cloud.Architecture `json:"architecture"`
	Spot         cloud.Money        `json:"spot_usd_per_hour"`
	OnDemand     cloud.Money        `json:"on_demand_usd_per_hour"`
	MemoryGiB    float64            `json:"memory_gib"`
	VCPU         int                `json:"vcpu"`
}

// SavingsPercent is the whole-percent discount of the Spot price against the
// paired On-Demand price. It is absent when the two prices cannot produce a
// figure in the 1..100 range a consumer can read as a percentage.
func (m *CatalogMachine) SavingsPercent() *int {
	return savingsPercent(m.Spot, m.OnDemand)
}

// savingsPercent is the same figure for a pair of prices that did not come from
// a catalogue row — a live answer composes both from the billing catalogue, and
// its savings must be derived from the prices it publishes rather than from the
// snapshot's.
func savingsPercent(spot, onDemand cloud.Money) *int {
	if onDemand.IsZero() || spot.Nanos() >= onDemand.Nanos() {
		return nil
	}

	saved := int((onDemand.Nanos() - spot.Nanos()) * percentScale / onDemand.Nanos())
	if saved <= 0 {
		return nil
	}

	return &saved
}

// ExcludedMachine is a machine the join refused to publish, with the reason.
// The caller reports these for review, and the coverage floor decides whether
// what remains is still a snapshot worth committing.
type ExcludedMachine struct {
	ID     cloud.MachineID
	Reason string
}

// BuildCatalog joins Spot rows with their On-Demand prices. It returns the
// machines priced in both classes plus the ones it refused to publish.
//
// Two refusals, treated alike: a Spot price with no On-Demand pair has no
// denominator for its savings figure, and a machine whose two pages disagree
// about its vCPU or memory has no defensible specification. Neither can be
// published, and neither is grounds for discarding the other 300 machines that
// parsed cleanly — that is what the per-snapshot coverage floor is for.
//
// The contradiction used to abort the whole refresh. On 2026-08-10 Google's
// general-purpose page listed c3d-standard-8 as 8 vCPU / 16 GiB while its Spot
// page said 32 GiB — a single wrong cell, confirmed against
// compute.machineTypes.list, in a row whose own price is exactly twice
// c3d-standard-4's. One typo in one cell froze every GCP price in the binary,
// and they drifted 5-10% below the published rates before anyone noticed.
func BuildCatalog(region cloud.Region, spotRows, onDemandRows []MachineRow) (*Catalog, []ExcludedMachine, error) {
	onDemand, err := indexRows(onDemandRows, cloud.PriceClassOnDemand)
	if err != nil {
		return nil, nil, err
	}

	spot, err := indexRows(spotRows, cloud.PriceClassSpot)
	if err != nil {
		return nil, nil, err
	}

	var (
		machines = make([]CatalogMachine, 0, len(spot))
		excluded []ExcludedMachine
	)

	for _, id := range slices.Sorted(maps.Keys(spot)) {
		row := spot[id]

		paired, found := onDemand[id]
		if !found {
			excluded = append(excluded, ExcludedMachine{ID: id, Reason: "spot price with no on-demand pair"})

			continue
		}
		if row.VCPU != paired.VCPU || row.MemoryGiB != paired.MemoryGiB {
			excluded = append(excluded, ExcludedMachine{ID: id, Reason: fmt.Sprintf(
				"spot page says %d vCPU/%.3f GiB, on-demand page says %d vCPU/%.3f GiB",
				row.VCPU, row.MemoryGiB, paired.VCPU, paired.MemoryGiB)})

			continue
		}

		architecture, classified := ArchitectureOf(id)
		if !classified {
			return nil, nil, fmt.Errorf("%w: %s belongs to series %q, which has no reviewed architecture",
				ErrSourceContract, id, SeriesOf(id))
		}

		machines = append(machines, CatalogMachine{
			ID:           id,
			Architecture: architecture,
			VCPU:         row.VCPU,
			MemoryGiB:    row.MemoryGiB,
			Spot:         row.Price,
			OnDemand:     paired.Price,
		})
	}

	return &Catalog{
		SchemaVersion: CatalogSchemaVersion,
		Region:        region,
		OS:            cloud.OSLinux,
		Currency:      cloud.CurrencyUSD,
		BillingUnit:   cloud.BillingUnitInstanceHour,
		Machines:      machines,
	}, excluded, nil
}

// indexRows keys rows by machine. An exact repeat is harmless redundancy — the
// pages list some machines under more than one heading — but two rows that
// disagree are ambiguity with no safe resolution. The whole row is compared, not
// just the price: two identical prices with different vCPU or memory would
// otherwise publish whichever row the page happened to list last.
func indexRows(rows []MachineRow, class cloud.PriceClass) (map[cloud.MachineID]MachineRow, error) {
	indexed := make(map[cloud.MachineID]MachineRow, len(rows))

	for _, row := range rows {
		previous, repeated := indexed[row.ID]
		if repeated && previous != row {
			return nil, fmt.Errorf("%w: %s is listed twice in the %s prices with different values, %+v and %+v",
				ErrSourceContract, row.ID, class, previous, row)
		}
		indexed[row.ID] = row
	}

	return indexed, nil
}

// DecodeCatalog decodes a committed catalogue. Unknown fields are rejected so a
// renamed key fails loudly instead of validating as a zero value.
func DecodeCatalog(data []byte) (*Catalog, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCatalog, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%w: trailing JSON value", ErrCatalog)
		}
		return nil, fmt.Errorf("%w: %w", ErrCatalog, err)
	}

	return &catalog, nil
}

// Encode renders the catalogue as compact JSON. The machine order is the order
// BuildCatalog produced — sorted by identifier — so a refresh that changes no
// price produces the same bytes.
func (c *Catalog) Encode() ([]byte, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode gcp catalogue: %w", err)
	}

	return data, nil
}

// Verify checks a catalogue against the contract that approved its sources.
// Everything the contract enumerates is checked: an unlisted region, operating
// system, architecture, or machine series is out of contract rather than a
// judgement call.
func (c *Catalog) Verify(contract *snapshot.SourceContract) error {
	if c.SchemaVersion != CatalogSchemaVersion {
		return fmt.Errorf("%w: schema_version must be %q, got %q", ErrCatalog, CatalogSchemaVersion, c.SchemaVersion)
	}
	if len(contract.Support.Regions) != 1 || c.Region != contract.Support.Regions[0] {
		return fmt.Errorf("%w: catalogue region %q does not exactly cover the approved region set %v",
			ErrCatalog, c.Region, contract.Support.Regions)
	}
	if !slices.Contains(contract.Support.OperatingSystems, c.OS) {
		return fmt.Errorf("%w: operating system %q is not in the approved support matrix", ErrCatalog, c.OS)
	}
	if c.Currency != cloud.CurrencyUSD || c.BillingUnit != cloud.BillingUnitInstanceHour {
		return fmt.Errorf("%w: catalogue is priced in %s per %s, not %s per %s",
			ErrCatalog, c.Currency, c.BillingUnit, cloud.CurrencyUSD, cloud.BillingUnitInstanceHour)
	}
	if len(c.Machines) == 0 {
		return fmt.Errorf("%w: catalogue has no machines", ErrCatalog)
	}

	seen := make(map[cloud.MachineID]struct{}, len(c.Machines))
	seriesSeen := make(map[string]struct{})
	architecturesSeen := make(map[cloud.Architecture]struct{})
	for i := range c.Machines {
		machine := &c.Machines[i]
		if _, duplicate := seen[machine.ID]; duplicate {
			return fmt.Errorf("%w: %s appears twice", ErrCatalog, machine.ID)
		}
		seen[machine.ID] = struct{}{}
		seriesSeen[SeriesOf(machine.ID)] = struct{}{}
		architecturesSeen[machine.Architecture] = struct{}{}

		if err := machine.verify(contract); err != nil {
			return err
		}
	}
	for _, series := range contract.Support.MachineSeries {
		if _, ok := seriesSeen[series]; !ok {
			return fmt.Errorf("%w: catalogue has no machine in approved series %q", ErrCatalog, series)
		}
	}
	for _, architecture := range contract.Support.Architectures {
		if _, ok := architecturesSeen[architecture]; !ok {
			return fmt.Errorf("%w: catalogue has no machine for approved architecture %q", ErrCatalog, architecture)
		}
	}

	return nil
}

func (m *CatalogMachine) verify(contract *snapshot.SourceContract) error {
	if !machineIDPattern.MatchString(string(m.ID)) {
		return fmt.Errorf("%w: %q is not a machine type identifier", ErrCatalog, m.ID)
	}
	if !slices.Contains(contract.Support.MachineSeries, SeriesOf(m.ID)) {
		return fmt.Errorf("%w: %s belongs to unapproved series %q", ErrCatalog, m.ID, SeriesOf(m.ID))
	}
	if !slices.Contains(contract.Support.Architectures, m.Architecture) {
		return fmt.Errorf("%w: %s declares unapproved architecture %q", ErrCatalog, m.ID, m.Architecture)
	}
	// A series with no reviewed architecture fails rather than defaulting: a new
	// Arm series added to the contract alone would otherwise ship as x86_64 and
	// pass every remaining gate.
	reviewed, classified := ArchitectureOf(m.ID)
	if !classified {
		return fmt.Errorf("%w: %s belongs to series %q, which has no reviewed architecture",
			ErrCatalog, m.ID, SeriesOf(m.ID))
	}
	if m.Architecture != reviewed {
		return fmt.Errorf("%w: %s is recorded as %q but its series is %q",
			ErrCatalog, m.ID, m.Architecture, reviewed)
	}
	if m.VCPU <= 0 || m.MemoryGiB <= 0 {
		return fmt.Errorf("%w: %s has no usable specification", ErrCatalog, m.ID)
	}
	if m.Spot.IsZero() || m.OnDemand.IsZero() {
		return fmt.Errorf("%w: %s is missing a spot or on-demand price", ErrCatalog, m.ID)
	}
	// A Spot price at or above list price means the two pages were joined on
	// different machines, not that spare capacity got expensive.
	if m.Spot.Nanos() >= m.OnDemand.Nanos() {
		return fmt.Errorf("%w: %s is priced %s spot against %s on-demand", ErrCatalog, m.ID, m.Spot, m.OnDemand)
	}

	limit := contract.Thresholds.MaxFractionalDigits
	for _, amount := range []cloud.Money{m.Spot, m.OnDemand} {
		if digits := amount.FractionalDigits(); digits > limit {
			return fmt.Errorf("%w: %s needs %d fractional digits, contract allows %d",
				ErrCatalog, m.ID, digits, limit)
		}
	}

	return nil
}

// PriceRecords flattens the catalogue into the neutral records the shared
// snapshot validator checks for duplicates and coverage.
func (c *Catalog) PriceRecords() []snapshot.PriceRecord {
	records := make([]snapshot.PriceRecord, 0, len(c.Machines)*pricesPerMachine)

	for i := range c.Machines {
		machine := &c.Machines[i]
		for _, priced := range []struct {
			class  cloud.PriceClass
			amount cloud.Money
		}{
			{class: cloud.PriceClassSpot, amount: machine.Spot},
			{class: cloud.PriceClassOnDemand, amount: machine.OnDemand},
		} {
			records = append(records, snapshot.PriceRecord{
				Region:   c.Region,
				Machine:  machine.ID,
				OS:       c.OS,
				Class:    priced.class,
				Currency: c.Currency,
				Unit:     c.BillingUnit,
				Amount:   priced.amount,
			})
		}
	}

	return records
}

// Coverage counts what the catalogue actually carries, for the manifest floor.
func (c *Catalog) Coverage() snapshot.Coverage {
	return snapshot.Coverage{
		Regions:  1,
		Machines: len(c.Machines),
		Prices:   len(c.Machines) * pricesPerMachine,
	}
}

// Series lists the machine series present, in stable order.
func (c *Catalog) Series() []string {
	series := make([]string, 0, len(c.Machines))
	for i := range c.Machines {
		if name := SeriesOf(c.Machines[i].ID); !slices.Contains(series, name) {
			series = append(series, name)
		}
	}
	slices.Sort(series)

	return series
}
