package azure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"spotinfo/internal/cloud"
	"spotinfo/internal/snapshot"
)

const (
	// percentScale converts a price ratio to whole percent.
	percentScale = 100
	// pricesPerMachine is the two classes every catalogue row publishes.
	pricesPerMachine = 2
)

// Catalog is the committed Azure price catalogue.
//
// Specifications are stored once and prices per region, because the same 200-odd
// sizes are sold in every covered region and repeating vCPU and memory beside
// each price would multiply the payload for no added fact.
//
//nolint:govet // Metadata reads before the rows it describes in a review diff.
type Catalog struct {
	SchemaVersion string `json:"schema_version"`
	// OperatingSystems names every OS the rows below price. It is a list, not a
	// single value: Azure sells one size twice, bare and with a Windows licence,
	// and each price row names which one it is.
	OperatingSystems []cloud.OperatingSystem `json:"operating_systems"`
	Currency         cloud.Currency          `json:"currency"`
	BillingUnit      cloud.BillingUnit       `json:"billing_unit"`
	Machines         []CatalogMachine        `json:"machines"`
	Regions          []CatalogRegion         `json:"regions"`
}

// CatalogMachine is one VM size's reviewed specification. Series is stored
// rather than derived: an Azure size name does not carry its series in a form
// that can be parsed back out, and the series is what the source contract
// enumerates.
type CatalogMachine struct {
	ID           cloud.MachineID    `json:"id"`
	Series       string             `json:"series"`
	Architecture cloud.Architecture `json:"architecture"`
	MemoryGiB    float64            `json:"memory_gib"`
	VCPU         int                `json:"vcpu"`
}

// CatalogRegion is one region's price list.
type CatalogRegion struct {
	ID     cloud.Region   `json:"id"`
	Prices []CatalogPrice `json:"prices"`
}

// CatalogPrice is one machine's paired prices in one region, for one operating
// system. Both classes are present by construction: a savings figure without its
// denominator is a number no consumer can check, and both must be the same OS or
// the discount is a licence, not spare capacity.
type CatalogPrice struct {
	Machine  cloud.MachineID       `json:"machine"`
	OS       cloud.OperatingSystem `json:"os"`
	Spot     cloud.Money           `json:"spot_usd_per_hour"`
	OnDemand cloud.Money           `json:"on_demand_usd_per_hour"`
}

// SavingsPercent is the whole-percent discount of the Spot price against the
// paired On-Demand price. It is absent when the two prices cannot produce a
// figure a consumer can read as a percentage.
func (p *CatalogPrice) SavingsPercent() *int {
	if p.OnDemand.IsZero() || p.Spot.Nanos() >= p.OnDemand.Nanos() {
		return nil
	}

	saved := int((p.OnDemand.Nanos() - p.Spot.Nanos()) * percentScale / p.OnDemand.Nanos())
	if saved <= 0 {
		return nil
	}

	return &saved
}

// pair collects one machine's two prices while a region is being built.
type pair struct {
	spot     cloud.Money
	onDemand cloud.Money
}

// machineOS is a catalogue row's identity inside one region. Pairing on the
// machine alone would join a Windows Spot price to a Linux list price and
// publish the licence as a saving.
type machineOS struct {
	machine cloud.MachineID
	os      cloud.OperatingSystem
}

// BuildReport names what the join read but did not publish, so a refresh that
// shrinks says why.
type BuildReport struct {
	// Unpaired lists region/machine/os priced in only one class.
	Unpaired []string
	// Unspecified lists machines priced with no reviewed specification.
	Unspecified []cloud.MachineID
}

// BuildCatalog joins reviewed specifications with resolved prices.
//
// A priced size with no specification cannot be published — the architecture and
// resource figures come from the Learn pages, not from the price feed — so it is
// dropped and named in the report. Callers filter with RetainSpecified first, so
// a name arriving here means the two agree on nothing, which is worth seeing.
// A size that has a specification and a Spot price but no On-Demand price for
// the same OS in that region is reported as unpaired rather than published with
// a missing denominator.
func BuildCatalog(series []SeriesSpec, rows []PriceRow) (*Catalog, BuildReport, error) {
	specs, err := indexSpecs(series)
	if err != nil {
		return nil, BuildReport{}, err
	}

	byRegion := make(map[cloud.Region]map[machineOS]*pair)
	unspecified := make(map[cloud.MachineID]struct{})

	for i := range rows {
		row := &rows[i]
		if _, known := specs[row.Machine]; !known {
			unspecified[row.Machine] = struct{}{}

			continue
		}

		machines, seen := byRegion[row.Region]
		if !seen {
			machines = make(map[machineOS]*pair)
			byRegion[row.Region] = machines
		}

		key := machineOS{machine: row.Machine, os: row.OS}
		if machines[key] == nil {
			machines[key] = &pair{}
		}

		if row.Class == cloud.PriceClassSpot {
			machines[key].spot = row.Amount
		} else {
			machines[key].onDemand = row.Amount
		}
	}

	regions, priced, unpaired := buildRegions(byRegion)

	machines := make([]CatalogMachine, 0, len(priced))
	for _, id := range slices.Sorted(maps.Keys(priced)) {
		machines = append(machines, specs[id])
	}

	return &Catalog{
			SchemaVersion:    CatalogSchemaVersion,
			OperatingSystems: publishedOperatingSystems(regions),
			Currency:         cloud.CurrencyUSD,
			BillingUnit:      cloud.BillingUnitInstanceHour,
			Machines:         machines,
			Regions:          regions,
		}, BuildReport{
			Unpaired:    unpaired,
			Unspecified: slices.Sorted(maps.Keys(unspecified)),
		}, nil
}

// publishedOperatingSystems reads the operating systems out of the rows that
// were actually published, so the catalogue cannot claim one it does not price.
func publishedOperatingSystems(regions []CatalogRegion) []cloud.OperatingSystem {
	seen := make(map[cloud.OperatingSystem]struct{}, pricesPerMachine)
	for i := range regions {
		for j := range regions[i].Prices {
			seen[regions[i].Prices[j].OS] = struct{}{}
		}
	}

	return slices.Sorted(maps.Keys(seen))
}

// buildRegions turns the collected pairs into sorted region price lists, and
// reports which machines were priced in only one class.
func buildRegions(byRegion map[cloud.Region]map[machineOS]*pair,
) ([]CatalogRegion, map[cloud.MachineID]struct{}, []string) {
	var (
		regions  = make([]CatalogRegion, 0, len(byRegion))
		priced   = make(map[cloud.MachineID]struct{})
		unpaired []string
	)

	for _, region := range slices.Sorted(maps.Keys(byRegion)) {
		machines := byRegion[region]
		prices := make([]CatalogPrice, 0, len(machines))

		for _, key := range sortedRows(machines) {
			amounts := machines[key]
			if amounts.spot.IsZero() || amounts.onDemand.IsZero() {
				unpaired = append(unpaired, fmt.Sprintf("%s/%s/%s", region, key.machine, key.os))

				continue
			}

			prices = append(prices, CatalogPrice{
				Machine:  key.machine,
				OS:       key.os,
				Spot:     amounts.spot,
				OnDemand: amounts.onDemand,
			})
			priced[key.machine] = struct{}{}
		}

		if len(prices) > 0 {
			regions = append(regions, CatalogRegion{ID: region, Prices: prices})
		}
	}

	return regions, priced, unpaired
}

// sortedRows orders one region's rows by machine, then operating system, so a
// refresh that changes no price produces the same catalogue bytes.
func sortedRows(machines map[machineOS]*pair) []machineOS {
	keys := slices.Collect(maps.Keys(machines))
	slices.SortFunc(keys, func(left, right machineOS) int {
		if order := strings.Compare(string(left.machine), string(right.machine)); order != 0 {
			return order
		}

		return strings.Compare(string(left.os), string(right.os))
	})

	return keys
}

// RetainSpecified drops observations for machines outside the reviewed
// specification set.
//
// The region sweep prices every size Azure sells — a few thousand per region, of
// which the contracted series are a couple of hundred. An expired interval or
// two conflicting prices in a size this catalogue never publishes is upstream
// noise, and failing the weekly refresh on it would make the ambiguity gate fire
// for machines no user can ask this binary about.
func RetainSpecified(observations []Observation, series []SeriesSpec) []Observation {
	specified := make(map[cloud.MachineID]struct{})
	for i := range series {
		for _, size := range series[i].Sizes {
			specified[size.ID] = struct{}{}
		}
	}

	retained := make([]Observation, 0, len(observations))
	for _, observation := range observations {
		if _, known := specified[observation.Machine]; known {
			retained = append(retained, observation)
		}
	}

	return retained
}

// indexSpecs keys reviewed specifications by size. One size documented by two
// series pages is ambiguous: the two pages may disagree on architecture, and
// nothing can choose between them.
func indexSpecs(series []SeriesSpec) (map[cloud.MachineID]CatalogMachine, error) {
	indexed := make(map[cloud.MachineID]CatalogMachine)

	for i := range series {
		spec := &series[i]
		for _, size := range spec.Sizes {
			if previous, repeated := indexed[size.ID]; repeated && previous.Series != spec.Series {
				return nil, fmt.Errorf("%w: %s is documented by both the %s and %s series",
					ErrSourceContract, size.ID, previous.Series, spec.Series)
			}

			indexed[size.ID] = CatalogMachine{
				ID:           size.ID,
				Series:       spec.Series,
				Architecture: spec.Architecture,
				VCPU:         size.VCPU,
				MemoryGiB:    size.MemoryGiB,
			}
		}
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

// Encode renders the catalogue as compact JSON. Machines and regions are in the
// sorted order BuildCatalog produced, so a refresh that changes no price
// produces the same bytes.
func (c *Catalog) Encode() ([]byte, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode azure catalogue: %w", err)
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
	if err := c.verifyOperatingSystems(contract); err != nil {
		return err
	}
	if c.Currency != cloud.CurrencyUSD || c.BillingUnit != cloud.BillingUnitInstanceHour {
		return fmt.Errorf("%w: catalogue is priced in %s per %s, not %s per %s",
			ErrCatalog, c.Currency, c.BillingUnit, cloud.CurrencyUSD, cloud.BillingUnitInstanceHour)
	}

	known, err := c.verifyMachines(contract)
	if err != nil {
		return err
	}

	return c.verifyRegions(contract, known)
}

// verifyOperatingSystems checks the declared operating systems both ways, the
// same as verifyMachines does for series and architectures. Every declared OS
// must be approved, and every approved OS must appear — the second half is what
// makes the undocumented productName suffix load-bearing: a rename that stopped
// producing Windows rows fails the refresh instead of quietly shipping a
// Linux-only catalogue that still calls itself complete.
func (c *Catalog) verifyOperatingSystems(contract *snapshot.SourceContract) error {
	if len(c.OperatingSystems) == 0 {
		return fmt.Errorf("%w: catalogue names no operating system", ErrCatalog)
	}

	for _, declared := range c.OperatingSystems {
		if !slices.Contains(contract.Support.OperatingSystems, declared) {
			return fmt.Errorf("%w: operating system %q is not in the approved support matrix", ErrCatalog, declared)
		}
	}
	for _, approved := range contract.Support.OperatingSystems {
		if !slices.Contains(c.OperatingSystems, approved) {
			return fmt.Errorf("%w: catalogue prices no machine for approved operating system %q", ErrCatalog, approved)
		}
	}

	return nil
}

func (c *Catalog) verifyMachines(contract *snapshot.SourceContract) (map[cloud.MachineID]struct{}, error) {
	if len(c.Machines) == 0 {
		return nil, fmt.Errorf("%w: catalogue has no machines", ErrCatalog)
	}

	known := make(map[cloud.MachineID]struct{}, len(c.Machines))
	seriesSeen := make(map[string]struct{})
	architecturesSeen := make(map[cloud.Architecture]struct{})

	for i := range c.Machines {
		machine := &c.Machines[i]
		if _, duplicate := known[machine.ID]; duplicate {
			return nil, fmt.Errorf("%w: %s appears twice", ErrCatalog, machine.ID)
		}
		known[machine.ID] = struct{}{}
		seriesSeen[machine.Series] = struct{}{}
		architecturesSeen[machine.Architecture] = struct{}{}

		if !sizeName.MatchString(string(machine.ID)) {
			return nil, fmt.Errorf("%w: %q is not an azure vm size identifier", ErrCatalog, machine.ID)
		}
		if !slices.Contains(contract.Support.MachineSeries, machine.Series) {
			return nil, fmt.Errorf("%w: %s belongs to unapproved series %q", ErrCatalog, machine.ID, machine.Series)
		}
		if !slices.Contains(contract.Support.Architectures, machine.Architecture) {
			return nil, fmt.Errorf("%w: %s declares unapproved architecture %q",
				ErrCatalog, machine.ID, machine.Architecture)
		}
		if machine.VCPU <= 0 || machine.MemoryGiB <= 0 {
			return nil, fmt.Errorf("%w: %s has no usable specification", ErrCatalog, machine.ID)
		}
	}
	for _, series := range contract.Support.MachineSeries {
		if _, ok := seriesSeen[series]; !ok {
			return nil, fmt.Errorf("%w: catalogue has no machine in approved series %q", ErrCatalog, series)
		}
	}
	for _, architecture := range contract.Support.Architectures {
		if _, ok := architecturesSeen[architecture]; !ok {
			return nil, fmt.Errorf("%w: catalogue has no machine for approved architecture %q", ErrCatalog, architecture)
		}
	}

	return known, nil
}

// verifyRegions applies the reviewed floor to every region separately. A global
// count would let one region return three sizes and be absorbed by seven healthy
// ones — the failure this gate exists to catch.
func (c *Catalog) verifyRegions(contract *snapshot.SourceContract, known map[cloud.MachineID]struct{}) error {
	if len(c.Regions) != len(contract.Support.Regions) {
		return fmt.Errorf("%w: catalogue covers %d regions, contract requires exactly %d",
			ErrCatalog, len(c.Regions), len(contract.Support.Regions))
	}

	seen := make(map[cloud.Region]struct{}, len(c.Regions))
	referenced := make(map[cloud.MachineID]struct{}, len(known))

	for i := range c.Regions {
		region := &c.Regions[i]
		if _, duplicate := seen[region.ID]; duplicate {
			return fmt.Errorf("%w: region %s appears twice", ErrCatalog, region.ID)
		}
		seen[region.ID] = struct{}{}

		if !slices.Contains(contract.Support.Regions, region.ID) {
			return fmt.Errorf("%w: region %q is not in the approved support matrix", ErrCatalog, region.ID)
		}
		if len(region.Prices) < contract.Thresholds.MinMachines {
			return fmt.Errorf("%w: region %s prices %d machines, contract requires at least %d",
				ErrCatalog, region.ID, len(region.Prices), contract.Thresholds.MinMachines)
		}

		if err := c.verifyPrices(region, known, contract.Thresholds.MaxFractionalDigits, referenced); err != nil {
			return err
		}
	}
	for _, region := range contract.Support.Regions {
		if _, ok := seen[region]; !ok {
			return fmt.Errorf("%w: approved region %q is missing from catalogue", ErrCatalog, region)
		}
	}

	if len(referenced) != len(known) {
		return fmt.Errorf("%w: %d machines carry a specification but no price",
			ErrCatalog, len(known)-len(referenced))
	}

	return nil
}

func (c *Catalog) verifyPrices(region *CatalogRegion, known map[cloud.MachineID]struct{},
	maxDigits int, referenced map[cloud.MachineID]struct{},
) error {
	priced := make(map[machineOS]struct{}, len(region.Prices))

	for i := range region.Prices {
		price := &region.Prices[i]
		// Keyed by machine and OS: the same size priced for Linux and for
		// Windows is two rows, and two rows for one pair is a duplicate.
		row := machineOS{machine: price.Machine, os: price.OS}
		if _, duplicate := priced[row]; duplicate {
			return fmt.Errorf("%w: %s is priced twice for %s in %s",
				ErrCatalog, price.Machine, price.OS, region.ID)
		}
		priced[row] = struct{}{}

		if !slices.Contains(c.OperatingSystems, price.OS) {
			return fmt.Errorf("%w: %s in %s is priced for %q, which the catalogue does not declare",
				ErrCatalog, price.Machine, region.ID, price.OS)
		}

		if _, described := known[price.Machine]; !described {
			return fmt.Errorf("%w: %s is priced in %s with no specification",
				ErrCatalog, price.Machine, region.ID)
		}
		referenced[price.Machine] = struct{}{}

		if price.Spot.IsZero() || price.OnDemand.IsZero() {
			return fmt.Errorf("%w: %s in %s is missing a spot or on-demand price",
				ErrCatalog, price.Machine, region.ID)
		}
		// A Spot price at or above list price means the two sources were joined
		// on different meters, not that spare capacity got expensive.
		if price.Spot.Nanos() >= price.OnDemand.Nanos() {
			return fmt.Errorf("%w: %s in %s is priced %s spot against %s on-demand",
				ErrCatalog, price.Machine, region.ID, price.Spot, price.OnDemand)
		}

		for _, amount := range []cloud.Money{price.Spot, price.OnDemand} {
			if digits := amount.FractionalDigits(); digits > maxDigits {
				return fmt.Errorf("%w: %s in %s needs %d fractional digits, contract allows %d",
					ErrCatalog, price.Machine, region.ID, digits, maxDigits)
			}
		}
	}

	return nil
}

// PriceRecords flattens the catalogue into the neutral records the shared
// snapshot validator checks for duplicates and coverage.
func (c *Catalog) PriceRecords() []snapshot.PriceRecord {
	records := make([]snapshot.PriceRecord, 0, c.priceCount())

	for i := range c.Regions {
		region := &c.Regions[i]
		for j := range region.Prices {
			price := &region.Prices[j]
			for _, priced := range []struct {
				class  cloud.PriceClass
				amount cloud.Money
			}{
				{class: cloud.PriceClassSpot, amount: price.Spot},
				{class: cloud.PriceClassOnDemand, amount: price.OnDemand},
			} {
				records = append(records, snapshot.PriceRecord{
					Region:   region.ID,
					Machine:  price.Machine,
					OS:       price.OS,
					Class:    priced.class,
					Currency: c.Currency,
					Unit:     c.BillingUnit,
					Amount:   priced.amount,
				})
			}
		}
	}

	return records
}

// Coverage counts what the catalogue actually carries, for the manifest floor.
func (c *Catalog) Coverage() snapshot.Coverage {
	return snapshot.Coverage{
		Regions:  len(c.Regions),
		Machines: len(c.Machines),
		Prices:   c.priceCount(),
	}
}

func (c *Catalog) priceCount() int {
	count := 0
	for i := range c.Regions {
		count += len(c.Regions[i].Prices) * pricesPerMachine
	}

	return count
}

// Series lists the machine series present, in stable order.
func (c *Catalog) Series() []string {
	series := make([]string, 0, len(c.Machines))
	for i := range c.Machines {
		if name := c.Machines[i].Series; !slices.Contains(series, name) {
			series = append(series, name)
		}
	}
	slices.Sort(series)

	return series
}
