package gcp

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"spotinfo/internal/cloud"
)

// Provider answers neutral queries from the committed GCP catalogue, and prices
// the regions a caller asked for from the Cloud Billing Catalog API when that
// caller supplied a key. With no key it makes no network request and needs no
// credentials: everything it serves is in the binary.
//
//nolint:govet // grouped by role: the snapshot, then the opt-in lookups.
type Provider struct {
	catalog *Catalog
	// liveRisk is nil unless the caller opted into the authenticated preemption
	// lookup. See live.go: it is per-project data and cannot be committed.
	liveRisk *LiveRiskConfig
	// placement is nil unless the caller opted into the authenticated capacity
	// advice lookup. See placement.go: it is a real-time reading, valid only when
	// it is taken, and it is fetched for a ranked page rather than a catalogue.
	placement *PlacementConfig
	// series is the contract's reviewed machine-series vocabulary, used by the
	// live price path to attribute a billing SKU to a series by exact match
	// rather than by guessing from its description.
	series  map[string]struct{}
	sources []cloud.SourceRef
	// live carries the billing-catalogue key and data policy. Its zero value is
	// the offline provider; see liveprice.go.
	live LivePriceConfig
	// minMachines is the reviewed coverage floor, reused as the floor a live
	// answer for the *committed* region is held to.
	minMachines int
}

// New builds the GCP provider from the committed snapshot. A snapshot that
// fails any of its gates returns an error, which the registry turns into a
// disabled provider rather than a process failure.
func New() (*Provider, error) {
	loaded, err := LoadEmbeddedSnapshot()
	if err != nil {
		return nil, err
	}

	series := make(map[string]struct{}, len(loaded.Contract.Support.MachineSeries))
	for _, name := range loaded.Contract.Support.MachineSeries {
		series[name] = struct{}{}
	}

	// The sources keep the zero cloud.SourceScope on purpose, which publishes
	// every one of them with every answer. Google's pricing pages are
	// whole-catalogue documents for the single region this snapshot covers, so
	// there is no narrower scope to derive and nothing to trim: all five back
	// every candidate. Azure, whose 81 sources are one per region and one per
	// machine series, is the provider that needs the scope.
	return &Provider{
		catalog:     loaded.Catalog,
		series:      series,
		sources:     loaded.Manifest.SourceRefs(),
		minMachines: loaded.Contract.Thresholds.MinMachines,
	}, nil
}

// ID identifies this provider.
func (p *Provider) ID() cloud.ProviderID { return cloud.ProviderGCP }

// Capabilities reports what this provider can answer.
//
// Risk is false and stays false: GCP publishes preemption history only through
// the authenticated `advice.capacityHistory` beta, so this provider has nothing
// to report and must not let silence be ranked as low interruption. Zone detail
// stays false for the same offline-contract reason.
//
// LiveEnrichment is true, and — like PlacementScore below — it is declared
// unconditionally rather than from whether a key was supplied. It says this
// build has a live price path at all, which it now does: the Cloud Billing
// Catalog API, reached with an API key. Both surfaces read capabilities on the
// provider they resolved, before a key is wired onto it, so a conditional
// declaration would refuse --offline and --refresh on the very invocation that
// can act on them. What a caller with no key gets is what they always got: the
// committed snapshot, which is also what --offline guarantees.
//
// PlacementScore is true, and it is declared unconditionally rather than from
// whether a lookup is wired. Both surfaces check capabilities on the provider
// they resolved, before the ranked path attaches the fetcher, so a conditional
// declaration would refuse the one request that can be served. What is not
// served — a placement figure on a browse answer — is refused by Query, which
// is the one place both surfaces pass through.
func (p *Provider) Capabilities() cloud.Capabilities {
	return cloud.Capabilities{
		OperatingSystems: []cloud.OperatingSystem{p.catalog.OS},
		Architectures:    []cloud.Architecture{cloud.ArchitectureX8664, cloud.ArchitectureARM64},
		// compute advice.capacity returns a probability in 0.0-1.0, not the
		// integer 1-10 AWS publishes, which is why SupportsScoreFloor refuses an
		// integer --min-score here rather than mapping one onto it.
		PlacementKind:  cloud.PlacementKindObtainability,
		SpotPrice:      true,
		OnDemandPrice:  true,
		MachineSpec:    true,
		PlacementScore: true,
		LiveEnrichment: true,
		// The advice API has no v1 form; beta is the only channel that serves it,
		// so a caller reading the figure is told the interface can still change.
		PlacementBeta: true,
	}
}

// Query filters the committed catalogue. A region the snapshot does not cover
// yields no candidates rather than an error: "spotinfo has no GCP data for
// europe-west1" is an empty result, not a malformed request, and the caller
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
		return cloud.Result{}, fmt.Errorf("%w: gcp does not support os %q", cloud.ErrInvalidArgument, query.OS)
	}
	if query.Architecture != "" && !capabilities.SupportsArchitecture(query.Architecture) {
		return cloud.Result{}, fmt.Errorf("%w: gcp does not support architecture %q",
			cloud.ErrInvalidArgument, query.Architecture)
	}
	// Runs after the OS and architecture checks above, which own those two errors
	// with their own wording. What is left is what this catalogue used to drop
	// silently: a risk or placement sort key fell through candidateComparator's
	// nil arm and came back as unsorted candidates with status ok, and Placement
	// was ignored outright — a caller cannot tell either from a real answer.
	if err := capabilities.Require(query.CapabilityNeeds()); err != nil {
		return cloud.Result{}, fmt.Errorf("gcp: %w", err)
	}
	if err := p.acceptsPlacement(query, capabilities); err != nil {
		return cloud.Result{}, err
	}

	pattern, err := machinePattern(query.MachinePattern)
	if err != nil {
		return cloud.Result{}, err
	}

	// Acquisition, after every capability check above: a request this provider
	// cannot answer must cost no request at all.
	overlay := p.livePrices(ctx, query)
	candidates := p.candidates(overlay, query, pattern)

	sortCandidates(candidates, query.Sort)

	return cloud.Result{
		Provider:   cloud.ProviderGCP,
		Mode:       overlay.dataMode(),
		Sources:    overlay.sources(p.sources),
		Candidates: candidates,
	}, nil
}

// candidates renders the matching machines, from the live overlay when one was
// fetched and from the committed catalogue otherwise.
//
// The two are never blended. An overlay covers every queried region or it does
// not exist, because the live path composes a price from per-core and per-GiB
// rates while the snapshot carries a scraped machine-type price: two
// derivations of the same figure, and one answer must be all of one.
func (p *Provider) candidates(overlay *liveOverlay, query *cloud.Query, pattern *regexp.Regexp) []cloud.Candidate {
	candidates := make([]cloud.Candidate, 0, len(p.catalog.Machines))

	if overlay != nil {
		for _, region := range overlay.regions {
			priced := overlay.prices[region]

			for i := range p.catalog.Machines {
				machine := &p.catalog.Machines[i]

				price, covered := priced[machine.ID]
				if !covered || !accepts(machine, price.spot, query, pattern) {
					continue
				}

				candidates = append(candidates, p.toCandidate(machine, query.OS, region, price.spot, price.onDemand))
			}
		}

		return candidates
	}

	if !p.covers(query.Regions) {
		return candidates
	}

	for i := range p.catalog.Machines {
		machine := &p.catalog.Machines[i]
		if !accepts(machine, machine.Spot, query, pattern) {
			continue
		}

		candidates = append(candidates,
			p.toCandidate(machine, query.OS, p.catalog.Region, machine.Spot, machine.OnDemand))
	}

	return candidates
}

// acceptsPlacement refuses the placement requests this catalogue cannot answer,
// before acquisition and rather than answering an empty column with an exit code
// of zero.
//
// Three different refusals, because the reasons differ. A figure asked for here
// is fetched afterwards, for a ranked page: one authenticated request per
// machine against a 333-machine catalogue is hundreds of calls billed to the
// caller's own project for an answer nobody browses. A sort by placement can
// never be honoured at acquisition, because the catalogue carries no such
// column at all. And an integer floor states no reviewed mapping onto a
// probability — the surfaces refuse it too, and this is where a surface that
// forgot is caught.
func (p *Provider) acceptsPlacement(query *cloud.Query, capabilities cloud.Capabilities) error {
	if query.Placement.Enabled && p.placement == nil {
		return fmt.Errorf(
			"%w: gcp obtainability is fetched for a ranked recommendation only, and needs a Google Cloud project",
			cloud.ErrUnsupportedCapability)
	}
	if query.Sort.Key == cloud.SortByPlacementScore {
		return fmt.Errorf("%w: gcp cannot order a catalogue by %s, which it fetches for a ranked page instead",
			cloud.ErrUnsupportedCapability, capabilities.PlacementKind)
	}
	if query.Placement.MinScore > 0 && !capabilities.SupportsScoreFloor() {
		return fmt.Errorf("%w: gcp publishes %s, and an integer 1-%d floor states no reviewed mapping onto it",
			cloud.ErrUnsupportedCapability, capabilities.PlacementKind, cloud.MaxPlacementScore)
	}

	return nil
}

// covers reports whether the requested regions include the one region this
// snapshot carries. An empty list applies no region filter.
func (p *Provider) covers(regions []cloud.Region) bool {
	if len(regions) == 0 {
		return true
	}

	return slices.Contains(regions, cloud.RegionAll) || slices.Contains(regions, p.catalog.Region)
}

// accepts applies the row filters. The Spot price is a parameter rather than
// read off the machine, because a live answer filters on the price it is about
// to publish — a ceiling applied to the snapshot's number while the answer
// carries the API's would drop rows that satisfy it and keep rows that do not.
func accepts(machine *CatalogMachine, spot cloud.Money, query *cloud.Query, pattern *regexp.Regexp) bool {
	if query.Architecture != "" && machine.Architecture != query.Architecture {
		return false
	}
	if machine.VCPU < query.MinVCPU || machine.MemoryGiB < query.MinMemoryGiB {
		return false
	}
	if pattern != nil && !pattern.MatchString(string(machine.ID)) {
		return false
	}
	if query.MaxPrice != nil && spot.Nanos() > query.MaxPrice.Nanos() {
		return false
	}

	return true
}

func (p *Provider) toCandidate(machine *CatalogMachine, machineOS cloud.OperatingSystem,
	region cloud.Region, spot, onDemand cloud.Money,
) cloud.Candidate {
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
		Provider: cloud.ProviderGCP,
		Location: location,
		OS:       machineOS,
		Machine: cloud.MachineSpec{
			ID:           machine.ID,
			Architecture: machine.Architecture,
			MemoryGiB:    machine.MemoryGiB,
			VCPU:         machine.VCPU,
		},
		Spot:           observation(cloud.PriceClassSpot, spot),
		OnDemand:       observation(cloud.PriceClassOnDemand, onDemand),
		SavingsPercent: savingsPercent(spot, onDemand),
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
// risk and placement score — leave the catalogue's own machine-identifier order,
// because the capability gate rejects those requests before acquisition.
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
