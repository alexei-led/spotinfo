package azure

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"spotinfo/internal/cloud"
)

// Provider answers neutral queries from the committed Azure catalogue, and
// refreshes the prices of a named region from the anonymous Retail Prices API
// when a caller has not asked for a snapshot-only answer.
//
// It still needs no credentials, subscription, or tenant. The live path reads
// the same contracted endpoint the snapshot was built from — see liveprice.go —
// and any failure there falls back to the committed catalogue rather than
// failing the query.
//
//nolint:govet // grouped by role: the snapshot, then how it is refreshed.
type Provider struct {
	catalog *Catalog
	specs   map[cloud.MachineID]*CatalogMachine
	regions map[cloud.Region]struct{}
	sources []cloud.SourceRef
	live    LivePriceConfig
	// retailBase is the contracted price endpoint, empty when the contract
	// names none; minMachines is the reviewed per-region floor a live sweep is
	// held to, the same one verifyRegions holds the snapshot to.
	retailBase  string
	minMachines int
}

// New builds the Azure provider from the committed snapshot. A snapshot that
// fails any of its gates returns an error, which the registry turns into a
// disabled provider rather than a process failure.
func New() (*Provider, error) {
	loaded, err := LoadEmbeddedSnapshot()
	if err != nil {
		return nil, err
	}

	specs := make(map[cloud.MachineID]*CatalogMachine, len(loaded.Catalog.Machines))
	for i := range loaded.Catalog.Machines {
		machine := &loaded.Catalog.Machines[i]
		specs[machine.ID] = machine
	}

	regions := make(map[cloud.Region]struct{}, len(loaded.Catalog.Regions))
	for i := range loaded.Catalog.Regions {
		regions[loaded.Catalog.Regions[i].ID] = struct{}{}
	}

	// A contract that names no price endpoint disables live enrichment rather
	// than failing the provider: the committed catalogue still answers
	// everything it answered before.
	base, err := RetailPriceBase(loaded.Contract)
	if err != nil {
		slog.Debug("azure contract names no live price endpoint", slog.Any("error", err))
	}

	return &Provider{
		catalog:     loaded.Catalog,
		specs:       specs,
		regions:     regions,
		sources:     scopedSources(loaded.Manifest.SourceRefs(), loaded.Catalog.Machines),
		retailBase:  base,
		minMachines: loaded.Contract.Thresholds.MinMachines,
	}, nil
}

// ID identifies this provider.
func (p *Provider) ID() cloud.ProviderID { return cloud.ProviderAzure }

// Capabilities reports what the committed snapshot can answer.
//
// The operating systems are read from the catalogue rather than declared here,
// so the provider offers exactly what it prices: Azure sells Linux and Windows
// meters for the same size, and a catalogue built from a source that stopped
// publishing one of them must stop offering it in the same run.
//
// Risk is false and stays false: Azure publishes eviction rates through Resource
// Graph and Resource SKUs, both of which require a subscription, so this
// provider has nothing to report and must not let silence be ranked as low
// interruption. Placement scores and zone detail are the same shape — both are
// published, and both need a subscription to read.
//
// Live enrichment is the one that is not, and that is why it is declared. The
// Retail Prices API is anonymous, so refreshing a named region's prices costs a
// request and no credential. Declaring the capability is also what tells the
// CLI that --offline and --refresh mean something here.
func (p *Provider) Capabilities() cloud.Capabilities {
	return cloud.Capabilities{
		OperatingSystems: slices.Clone(p.catalog.OperatingSystems),
		Architectures:    []cloud.Architecture{cloud.ArchitectureX8664, cloud.ArchitectureARM64},
		SpotPrice:        true,
		OnDemandPrice:    true,
		MachineSpec:      true,
		LiveEnrichment:   p.liveEnabled(),
	}
}

// Query filters the committed catalogue. A region the snapshot does not cover
// yields no candidates rather than an error: "spotinfo has no Azure data for
// francecentral" is an empty result, not a malformed request, and the caller
// reports it as NO_CANDIDATES.
func (p *Provider) Query(ctx context.Context, query *cloud.Query) (cloud.Result, error) {
	if query == nil {
		return cloud.Result{}, fmt.Errorf("%w: query is required", cloud.ErrInvalidArgument)
	}

	capabilities := p.Capabilities()
	// Guarded on non-empty, like the architecture check below and
	// Capabilities.Require: cloud.Query documents a zero-valued filter as
	// inactive, so an unset OS is "any", not the OS named "".
	if query.OS != "" && !capabilities.SupportsOS(query.OS) {
		return cloud.Result{}, fmt.Errorf("%w: azure does not support os %q", cloud.ErrInvalidArgument, query.OS)
	}
	if query.Architecture != "" && !capabilities.SupportsArchitecture(query.Architecture) {
		return cloud.Result{}, fmt.Errorf("%w: azure does not support architecture %q",
			cloud.ErrInvalidArgument, query.Architecture)
	}
	// Runs after the OS and architecture checks above, which own those two errors
	// with their own wording. What is left is what this catalogue used to drop
	// silently: a risk or placement sort key fell through candidateComparator's
	// nil arm and came back as unsorted candidates with status ok, and Placement
	// was ignored outright — a caller cannot tell either from a real answer.
	if err := capabilities.Require(query.CapabilityNeeds()); err != nil {
		return cloud.Result{}, fmt.Errorf("azure: %w", err)
	}

	pattern, err := machinePattern(query.MachinePattern)
	if err != nil {
		return cloud.Result{}, err
	}

	// Acquisition, after every capability check above: a request this provider
	// cannot answer must cost no request at all.
	overlay := p.livePrices(ctx, query)

	var candidates []cloud.Candidate

	for i := range p.catalog.Regions {
		region := &p.catalog.Regions[i]
		if !covers(query.Regions, region.ID) {
			continue
		}

		prices := overlay.pricesFor(region)
		for j := range prices {
			price := &prices[j]

			machine := p.specs[price.Machine]
			if machine == nil || !accepts(machine, price, query, pattern) {
				continue
			}
			candidates = append(candidates, p.toCandidate(region.ID, machine, price))
		}
	}

	sortCandidates(candidates, query.Sort)

	return cloud.Result{
		Provider:   cloud.ProviderAzure,
		Mode:       overlay.dataMode(),
		Sources:    overlay.sources(p.sources),
		Candidates: candidates,
	}, nil
}

// covers reports whether the requested regions include this one. An empty list
// applies no region filter.
func covers(requested []cloud.Region, region cloud.Region) bool {
	if len(requested) == 0 {
		return true
	}

	return slices.Contains(requested, cloud.RegionAll) || slices.Contains(requested, region)
}

func accepts(machine *CatalogMachine, price *CatalogPrice, query *cloud.Query, pattern *regexp.Regexp) bool {
	// The catalogue prices the same size for Linux and for Windows, so the OS is
	// a row filter, not only a capability check. An unset OS is "any", the same
	// as every other zero-valued filter on cloud.Query.
	if query.OS != "" && price.OS != query.OS {
		return false
	}
	if query.Architecture != "" && machine.Architecture != query.Architecture {
		return false
	}
	if machine.VCPU < query.MinVCPU || machine.MemoryGiB < query.MinMemoryGiB {
		return false
	}
	if pattern != nil && !pattern.MatchString(string(machine.ID)) {
		return false
	}
	if query.MaxPrice != nil && price.Spot.Nanos() > query.MaxPrice.Nanos() {
		return false
	}

	return true
}

func (p *Provider) toCandidate(region cloud.Region, machine *CatalogMachine, price *CatalogPrice) cloud.Candidate {
	location := cloud.Location{Region: region}
	observation := func(class cloud.PriceClass, amount cloud.Money) *cloud.PriceObservation {
		return &cloud.PriceObservation{
			Location: location,
			Class:    class,
			Currency: p.catalog.Currency,
			Unit:     p.catalog.BillingUnit,
			Amount:   amount,
		}
	}

	return cloud.Candidate{
		Provider: cloud.ProviderAzure,
		Location: location,
		OS:       price.OS,
		Machine: cloud.MachineSpec{
			ID:           machine.ID,
			Architecture: machine.Architecture,
			MemoryGiB:    machine.MemoryGiB,
			VCPU:         machine.VCPU,
		},
		Spot:           observation(cloud.PriceClassSpot, price.Spot),
		OnDemand:       observation(cloud.PriceClassOnDemand, price.OnDemand),
		SavingsPercent: price.SavingsPercent(),
		Risk:           cloud.UnavailableRisk(),
	}
}

// machinePattern compiles the machine-name filter. An unset filter matches
// every machine.
func machinePattern(expression string) (*regexp.Regexp, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, nil //nolint:nilnil // no pattern is the absence of a filter
	}

	pattern, err := regexp.Compile(expression)
	if err != nil {
		return nil, fmt.Errorf("%w: machine pattern: %w", cloud.ErrInvalidArgument, err)
	}

	return pattern, nil
}

// sortCandidates applies the requested order. Keys this provider cannot serve —
// risk and placement score — leave the catalogue's own region-then-machine
// order, because the capability gate rejects those requests before acquisition.
func sortCandidates(candidates []cloud.Candidate, order cloud.SortOrder) {
	compare := candidateComparator(order.Key)
	if compare == nil {
		return
	}

	slices.SortStableFunc(candidates, func(left, right cloud.Candidate) int {
		if order.Descending {
			return compare(&right, &left)
		}

		return compare(&left, &right)
	})
}

func candidateComparator(key cloud.SortKey) func(left, right *cloud.Candidate) int {
	switch key {
	case cloud.SortByPrice:
		return func(left, right *cloud.Candidate) int {
			return cmp.Compare(left.Spot.Amount.Nanos(), right.Spot.Amount.Nanos())
		}
	case cloud.SortBySavings:
		return func(left, right *cloud.Candidate) int {
			return cmp.Compare(derefSavings(left), derefSavings(right))
		}
	case cloud.SortByMachine:
		return func(left, right *cloud.Candidate) int {
			return strings.Compare(string(left.Machine.ID), string(right.Machine.ID))
		}
	case cloud.SortByRegion:
		return func(left, right *cloud.Candidate) int {
			return strings.Compare(string(left.Location.Region), string(right.Location.Region))
		}
	case cloud.SortByRisk, cloud.SortByPlacementScore:
		return nil
	default:
		return nil
	}
}

func derefSavings(candidate *cloud.Candidate) int {
	if candidate.SavingsPercent == nil {
		return 0
	}

	return *candidate.SavingsPercent
}
