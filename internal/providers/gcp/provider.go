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

// Provider answers neutral queries from the committed GCP catalogue. It makes
// no network request and needs no credentials: everything it serves is in the
// binary.
type Provider struct {
	catalog *Catalog
	sources []cloud.SourceRef
}

// New builds the GCP provider from the committed snapshot. A snapshot that
// fails any of its gates returns an error, which the registry turns into a
// disabled provider rather than a process failure.
func New() (*Provider, error) {
	loaded, err := LoadEmbeddedSnapshot()
	if err != nil {
		return nil, err
	}

	return &Provider{catalog: loaded.Catalog, sources: loaded.Manifest.SourceRefs()}, nil
}

// ID identifies this provider.
func (p *Provider) ID() cloud.ProviderID { return cloud.ProviderGCP }

// Capabilities reports what the committed pages can answer.
//
// Risk is false and stays false: GCP publishes preemption history only through
// the authenticated `advice.capacityHistory` beta, so this provider has nothing
// to report and must not let silence be ranked as low interruption. Placement
// scores, zone detail and live enrichment would all require an authenticated
// API, which the offline contract rules out.
func (p *Provider) Capabilities() cloud.Capabilities {
	return cloud.Capabilities{
		OperatingSystems: []cloud.OperatingSystem{p.catalog.OS},
		Architectures:    []cloud.Architecture{cloud.ArchitectureX8664, cloud.ArchitectureARM64},
		SpotPrice:        true,
		OnDemandPrice:    true,
		MachineSpec:      true,
	}
}

// Query filters the committed catalogue. A region the snapshot does not cover
// yields no candidates rather than an error: "spotinfo has no GCP data for
// europe-west1" is an empty result, not a malformed request, and the caller
// reports it as NO_CANDIDATES.
func (p *Provider) Query(_ context.Context, query *cloud.Query) (cloud.Result, error) {
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

	pattern, err := machinePattern(query.MachinePattern)
	if err != nil {
		return cloud.Result{}, err
	}

	candidates := make([]cloud.Candidate, 0, len(p.catalog.Machines))
	if p.covers(query.Regions) {
		for i := range p.catalog.Machines {
			machine := &p.catalog.Machines[i]
			if !p.accepts(machine, query, pattern) {
				continue
			}
			candidates = append(candidates, p.toCandidate(machine, query.OS))
		}
	}

	sortCandidates(candidates, query.Sort)

	return cloud.Result{
		Provider:   cloud.ProviderGCP,
		Mode:       cloud.DataModeEmbeddedSnapshot,
		Sources:    slices.Clone(p.sources),
		Candidates: candidates,
	}, nil
}

// covers reports whether the requested regions include the one region this
// snapshot carries. An empty list applies no region filter.
func (p *Provider) covers(regions []cloud.Region) bool {
	if len(regions) == 0 {
		return true
	}

	return slices.Contains(regions, cloud.RegionAll) || slices.Contains(regions, p.catalog.Region)
}

func (p *Provider) accepts(machine *CatalogMachine, query *cloud.Query, pattern *regexp.Regexp) bool {
	if query.Architecture != "" && machine.Architecture != query.Architecture {
		return false
	}
	if machine.VCPU < query.MinVCPU || machine.MemoryGiB < query.MinMemoryGiB {
		return false
	}
	if pattern != nil && !pattern.MatchString(string(machine.ID)) {
		return false
	}
	if query.MaxPrice != nil && machine.Spot.Nanos() > query.MaxPrice.Nanos() {
		return false
	}

	return true
}

func (p *Provider) toCandidate(machine *CatalogMachine, machineOS cloud.OperatingSystem) cloud.Candidate {
	location := cloud.Location{Region: p.catalog.Region}
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
		Spot:           observation(cloud.PriceClassSpot, machine.Spot),
		OnDemand:       observation(cloud.PriceClassOnDemand, machine.OnDemand),
		SavingsPercent: machine.SavingsPercent(),
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
