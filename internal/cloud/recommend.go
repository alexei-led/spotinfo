package cloud

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
)

// Workload selects the recommendation policy.
type Workload string

const (
	// WorkloadCost ranks by price alone and makes no interruption claim. It is
	// the only policy a provider without risk data can honestly serve.
	WorkloadCost Workload = "cost"
	// WorkloadWeb caps interruption frequency at webMaxInterruptionPercent.
	WorkloadWeb Workload = "web"
	// WorkloadCI caps interruption frequency at ciMaxInterruptionPercent.
	WorkloadCI Workload = "ci"
	// WorkloadBatch caps interruption frequency at batchMaxInterruptionPercent.
	WorkloadBatch Workload = "batch"
)

const (
	// DefaultTop is the number of recommendations returned when Top is omitted.
	DefaultTop = 3
	// MaxTop is the largest result set the v2 contract allows.
	MaxTop = 50

	// Interruption-frequency ceilings, in percent of a trailing observation
	// window. The values align with the AWS Spot Advisor bucket boundaries that
	// the v1 recommendation used (<5%, 10-15%, 15-20%) so a provider publishing
	// a comparable frequency range is capped the same way. A provider whose risk
	// figure measures something else must not be ranked under these policies.
	webMaxInterruptionPercent   = 5.0
	ciMaxInterruptionPercent    = 16.0
	batchMaxInterruptionPercent = 22.0

	rationaleArchitectureMatch    = "ARCHITECTURE_MATCH"
	rationaleBudgetCapMet         = "BUDGET_CAP_MET"
	rationaleCostPolicy           = "COST_POLICY"
	rationaleKnownPositivePrice   = "KNOWN_POSITIVE_PRICE"
	rationaleMachinePatternMatch  = "MACHINE_PATTERN_MATCH"
	rationaleResourceMinimumsMet  = "RESOURCE_MINIMUMS_MET"
	rationaleWorkloadCapMetFormat = "WORKLOAD_%s_CAP_MET"
)

// interruptionCappableKinds are the risk kinds the workload ceilings above are
// expressed in. Only a kind listed here may be compared against them; every
// other measurement is treated as unrankable under a risk-capped policy, the
// same as no measurement at all.
var interruptionCappableKinds = []RiskKind{RiskKindInterruptionFrequencyRange}

// rankingPolicy is the ordered, canonical v2 ranking criteria. There is no
// interruption term: the cost policy is risk-free, and the risk-aware policies
// express risk as a filter rather than as a ranking factor, so one ordering
// serves every workload.
var rankingPolicy = []string{
	CriterionSpotPriceAscending,
	CriterionExcessVCPUAscending,
	CriterionExcessMemoryAscending,
	CriterionRegionAscending,
	CriterionMachineAscending,
}

// The published ranking criteria, in the order they are applied.
const (
	// CriterionSpotPriceAscending orders by spot price, cheapest first.
	CriterionSpotPriceAscending = "spot_price_ascending"
	// CriterionExcessVCPUAscending prefers the least over-provisioned vCPU count.
	CriterionExcessVCPUAscending = "excess_vcpu_ascending"
	// CriterionExcessMemoryAscending prefers the least over-provisioned memory.
	CriterionExcessMemoryAscending = "excess_memory_gib_ascending"
	// CriterionRegionAscending breaks a tie by region identifier.
	CriterionRegionAscending = "region_ascending"
	// CriterionMachineAscending breaks a remaining tie by machine identifier.
	CriterionMachineAscending = "machine_ascending"
)

// RankingPolicy returns the canonical criteria. A fresh slice keeps a caller
// from mutating the package policy.
func RankingPolicy() []string {
	return slices.Clone(rankingPolicy)
}

// ParseWorkload accepts only a recognised policy.
func ParseWorkload(value string) (Workload, error) {
	switch workload := Workload(strings.ToLower(strings.TrimSpace(value))); workload {
	case WorkloadCost, WorkloadWeb, WorkloadCI, WorkloadBatch:
		return workload, nil
	default:
		return "", fmt.Errorf("%w: workload must be cost, web, ci, or batch", ErrInvalidArgument)
	}
}

// RequiresRisk reports whether the policy caps interruption frequency, and so
// needs a provider that publishes risk.
func (w Workload) RequiresRisk() bool { return w != WorkloadCost }

// maxInterruptionPercent is the ceiling this policy applies to a candidate's
// worst published interruption frequency.
func (w Workload) maxInterruptionPercent() float64 {
	switch w {
	case WorkloadCI:
		return ciMaxInterruptionPercent
	case WorkloadBatch:
		return batchMaxInterruptionPercent
	case WorkloadWeb:
		return webMaxInterruptionPercent
	case WorkloadCost:
		return math.Inf(1)
	default:
		return math.Inf(1)
	}
}

// RecommendRequest is a provider-neutral recommendation request. It is
// validated before any provider is queried.
type RecommendRequest struct {
	MaxPrice     *Money
	Cloud        ProviderID
	Machine      string
	Architecture Architecture
	OS           OperatingSystem
	Workload     Workload
	Regions      []Region
	MinMemoryGiB float64
	MinVCPU      int
	Top          int
}

// Validate rejects every input the v2 contract does not allow. It performs no
// I/O, so an invalid request never reaches a provider.
func (r *RecommendRequest) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: request is required", ErrInvalidArgument)
	}
	if err := r.validateVocabulary(); err != nil {
		return err
	}
	if err := r.validateBounds(); err != nil {
		return err
	}
	if _, err := r.machinePattern(); err != nil {
		return err
	}

	return r.validateRegions()
}

// validateVocabulary rejects values outside the neutral enumerations.
func (r *RecommendRequest) validateVocabulary() error {
	if !slices.Contains(ProviderIDs(), r.Cloud) {
		return fmt.Errorf("%w: unknown cloud provider %q", ErrInvalidArgument, r.Cloud)
	}
	if r.Architecture != ArchitectureX8664 && r.Architecture != ArchitectureARM64 {
		return fmt.Errorf("%w: architecture must be %s or %s", ErrInvalidArgument, ArchitectureX8664, ArchitectureARM64)
	}
	if r.OS != OSLinux && r.OS != OSWindows {
		return fmt.Errorf("%w: os must be %s or %s", ErrInvalidArgument, OSLinux, OSWindows)
	}
	_, err := ParseWorkload(string(r.Workload))

	return err
}

// validateBounds rejects numeric constraints the contract bounds.
func (r *RecommendRequest) validateBounds() error {
	if r.MinVCPU < 1 {
		return fmt.Errorf("%w: min_vcpu must be at least 1", ErrInvalidArgument)
	}
	if r.MinMemoryGiB <= 0 || math.IsNaN(r.MinMemoryGiB) || math.IsInf(r.MinMemoryGiB, 0) {
		return fmt.Errorf("%w: min_memory_gib must be a positive number", ErrInvalidArgument)
	}
	if r.MaxPrice != nil && r.MaxPrice.IsZero() {
		return fmt.Errorf("%w: max_price_per_hour must be positive", ErrInvalidArgument)
	}
	if r.Top < 1 || r.Top > MaxTop {
		return fmt.Errorf("%w: top must be between 1 and %d", ErrInvalidArgument, MaxTop)
	}

	return nil
}

// validateRegions rejects an empty region list and the one combination the
// contract calls out: the "all" keyword mixed with explicit regions, which
// cannot mean anything more specific than "all".
func (r *RecommendRequest) validateRegions() error {
	if len(r.Regions) == 0 {
		return fmt.Errorf("%w: at least one region is required", ErrInvalidArgument)
	}

	seen := make(map[Region]struct{}, len(r.Regions))
	for _, region := range r.Regions {
		if strings.TrimSpace(string(region)) == "" {
			return fmt.Errorf("%w: region must not be empty", ErrInvalidArgument)
		}
		if _, duplicate := seen[region]; duplicate {
			return fmt.Errorf("%w: region %q is listed twice", ErrInvalidArgument, region)
		}
		seen[region] = struct{}{}
	}
	if _, hasAll := seen[RegionAll]; hasAll && len(seen) > 1 {
		return fmt.Errorf("%w: region %s cannot be combined with explicit regions", ErrInvalidArgument, RegionAll)
	}

	return nil
}

func (r *RecommendRequest) machinePattern() (*regexp.Regexp, error) {
	if r.Machine == "" {
		return nil, nil //nolint:nilnil // no pattern is the absence of a filter
	}

	pattern, err := regexp.Compile(r.Machine)
	if err != nil {
		return nil, fmt.Errorf("%w: machine pattern: %w", ErrInvalidArgument, err)
	}

	return pattern, nil
}

// CapabilityNeeds is what this request requires of a provider. A risk-aware
// workload needs published risk; the cost policy deliberately does not, which
// is what lets a provider without risk data serve it honestly.
func (r *RecommendRequest) CapabilityNeeds() CapabilityRequest {
	needed := []Capability{CapabilitySpotPrice, CapabilityMachineSpec}
	if r.Workload.RequiresRisk() {
		needed = append(needed, CapabilityRisk)
	}

	return CapabilityRequest{OS: r.OS, Architecture: r.Architecture, Needed: needed}
}

// Query is the acquisition request this recommendation needs. Ordering is left
// to the ranker, which applies the canonical policy over every candidate.
func (r *RecommendRequest) Query() *Query {
	return &Query{
		MaxPrice:       r.MaxPrice,
		MachinePattern: r.Machine,
		Architecture:   r.Architecture,
		OS:             r.OS,
		Regions:        slices.Clone(r.Regions),
		MinMemoryGiB:   r.MinMemoryGiB,
		MinVCPU:        r.MinVCPU,
	}
}

// Recommend validates the request, checks the provider can answer it, acquires
// candidates, and ranks them. Every failure happens before acquisition except
// the two that cannot: an acquisition error and an empty candidate set.
func Recommend(ctx context.Context, provider Provider, request *RecommendRequest) (*RecommendReport, error) {
	if provider == nil {
		return nil, fmt.Errorf("%w: provider is required", ErrInvalidArgument)
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.Cloud != provider.ID() {
		return nil, fmt.Errorf("%w: request names %q but provider is %q", ErrInvalidArgument, request.Cloud, provider.ID())
	}
	if err := provider.Capabilities().Require(request.CapabilityNeeds()); err != nil {
		return nil, fmt.Errorf("%s: %w", provider.ID(), err)
	}

	result, err := provider.Query(ctx, request.Query())
	if err != nil {
		return nil, err
	}

	ranked, err := rank(request, result.Candidates)
	if err != nil {
		return nil, err
	}

	return newRecommendReport(request, &result, ranked)
}

// scored pairs a candidate with the right-sizing excess the ranking policy
// orders by, so the comparator recomputes nothing.
type scored struct {
	candidate     *Candidate
	excessMemory  float64
	priceNanos    int64
	excessVCPU    int
	budgeted      bool
	patterned     bool
	riskConstrain bool
}

// rank filters candidates against every constraint and orders the survivors by
// the canonical policy. Filters are re-applied here rather than trusted to the
// provider: a provider that ignores a filter must not produce a recommendation
// that violates it.
func rank(request *RecommendRequest, candidates []Candidate) ([]scored, error) {
	pattern, err := request.machinePattern()
	if err != nil {
		return nil, err
	}

	maxInterruption := request.Workload.maxInterruptionPercent()
	kept := make([]scored, 0, len(candidates))

	for i := range candidates {
		candidate := &candidates[i]
		if !accepts(candidate, request, pattern, maxInterruption) {
			continue
		}

		kept = append(kept, scored{
			candidate:     candidate,
			excessMemory:  candidate.Machine.MemoryGiB - request.MinMemoryGiB,
			priceNanos:    candidate.Spot.Amount.Nanos(),
			excessVCPU:    candidate.Machine.VCPU - request.MinVCPU,
			budgeted:      request.MaxPrice != nil,
			patterned:     pattern != nil,
			riskConstrain: request.Workload.RequiresRisk(),
		})
	}

	if len(kept) == 0 {
		return nil, fmt.Errorf("%w for architecture %s and workload %s",
			ErrNoCandidates, request.Architecture, request.Workload)
	}

	slices.SortFunc(kept, compareScored)
	if len(kept) > request.Top {
		kept = kept[:request.Top]
	}

	return kept, nil
}

// accepts screens one candidate. An unknown price cannot satisfy any request:
// spot prices are never zero, so a missing price means unknown, not free.
func accepts(candidate *Candidate, request *RecommendRequest, pattern *regexp.Regexp, maxInterruption float64) bool {
	switch {
	case candidate.Spot == nil || candidate.Spot.Amount.IsZero():
		return false
	case candidate.Machine.Architecture != request.Architecture:
		return false
	case candidate.OS != request.OS:
		return false
	case candidate.Machine.VCPU < request.MinVCPU || candidate.Machine.MemoryGiB < request.MinMemoryGiB:
		return false
	case request.MaxPrice != nil && candidate.Spot.Amount.Nanos() > request.MaxPrice.Nanos():
		return false
	case pattern != nil && !pattern.MatchString(string(candidate.Machine.ID)):
		return false
	case !acceptsRegion(candidate.Location.Region, request.Regions):
		return false
	}

	return acceptsRisk(candidate, request.Workload, maxInterruption)
}

// acceptsRegion re-applies the region filter. An empty list and RegionAll both
// mean "no filter"; anything else must name the candidate's own region, so a
// provider that mishandles RegionAll cannot return a region nobody asked for.
func acceptsRegion(region Region, requested []Region) bool {
	if len(requested) == 0 || slices.Contains(requested, RegionAll) {
		return true
	}

	return slices.Contains(requested, region)
}

// acceptsRisk applies the workload's interruption ceiling. A risk-aware policy
// drops a candidate whose risk is unknown: ranking it would present an
// unmeasured machine as if it had cleared the cap.
//
// The kind is checked too, not just the status. The ceilings are AWS Spot
// Advisor bucket boundaries, so a provider publishing some other measurement —
// a GCP preemption rate, an Azure eviction rate — would be filtered against
// thresholds that mean nothing for it. Comparability is declared here, once,
// rather than assumed of every kind that happens to carry a percentage.
func acceptsRisk(candidate *Candidate, workload Workload, maxInterruption float64) bool {
	if !workload.RequiresRisk() {
		return true
	}
	if candidate.Risk.Status != RiskStatusAvailable || candidate.Risk.MaxPercent == nil {
		return false
	}
	if !slices.Contains(interruptionCappableKinds, candidate.Risk.Kind) {
		return false
	}

	return *candidate.Risk.MaxPercent <= maxInterruption
}

// compareScored is the canonical total order. Every field is compared, so the
// result does not depend on the order candidates arrived in.
func compareScored(left, right scored) int {
	if order := cmp.Compare(left.priceNanos, right.priceNanos); order != 0 {
		return order
	}
	if order := cmp.Compare(left.excessVCPU, right.excessVCPU); order != 0 {
		return order
	}
	if order := cmp.Compare(left.excessMemory, right.excessMemory); order != 0 {
		return order
	}
	if order := cmp.Compare(left.candidate.Location.Region, right.candidate.Location.Region); order != 0 {
		return order
	}

	return cmp.Compare(left.candidate.Machine.ID, right.candidate.Machine.ID)
}

// rationaleCodes explains why a candidate was kept. The codes name the
// constraints that were actually applied, so a cost recommendation never
// carries a claim about interruption safety.
func (s scored) rationaleCodes(workload Workload) []string {
	codes := []string{rationaleArchitectureMatch, rationaleKnownPositivePrice, rationaleResourceMinimumsMet}
	if workload == WorkloadCost {
		codes = append(codes, rationaleCostPolicy)
	} else {
		codes = append(codes, fmt.Sprintf(rationaleWorkloadCapMetFormat, strings.ToUpper(string(workload))))
	}
	if s.budgeted {
		codes = append(codes, rationaleBudgetCapMet)
	}
	if s.patterned {
		codes = append(codes, rationaleMachinePatternMatch)
	}
	slices.Sort(codes)

	return codes
}
