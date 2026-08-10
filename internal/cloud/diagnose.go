package cloud

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Diagnosing an empty result set.
//
// `no candidates for architecture x86_64 and workload cost` was reported for
// three unrelated causes — a region the provider does not publish, a budget
// below every price, and a genuine architecture mismatch. It named the two
// constraints that were satisfied and never the one that eliminated the
// results, so a caller asking GCP for europe-west1 concluded that GCP publishes
// no x86_64 spot capacity in Europe.
//
// Providers narrow before returning, so by the time ranking sees an empty set
// there is nothing left to inspect. The diagnosis therefore re-queries with
// every filter cleared except the regions — which decide what a provider
// acquires, and would turn an AWS diagnosis into a fetch of every region — and
// narrows that catalogue one constraint at a time, reporting the first that
// empties it.
//
// This runs only after ranking has already found nothing. The success path pays
// nothing, and the failure path pays one extra query against data the provider
// has usually just read.

// maxReportedValues bounds how many observed values an error lists, so a
// provider with sixty regions still produces a readable sentence.
const maxReportedValues = 8

// rejectionStage is one filter, what it demands, and how to describe what the
// candidates that reached it were carrying instead. summarize is optional: some
// constraints (a vCPU minimum) are self-explanatory once named.
type rejectionStage struct {
	accept     func(*Candidate) bool
	summarize  func([]*Candidate) string
	constraint string
}

// diagnoseNoCandidates explains an empty result set in terms of the request.
//
// Every failure to diagnose falls back to the plain message: this runs on an
// error path and must never replace one error with a different one.
func diagnoseNoCandidates(ctx context.Context, provider Provider, request *RecommendRequest) error {
	plain := fmt.Errorf("%w for architecture %s and workload %s",
		ErrNoCandidates, request.Architecture, request.Workload)

	// A second query is free against a committed snapshot and is not free
	// against a provider that enriches from a live API: clearing the vCPU and
	// memory minima widens the candidate set, and on AWS every widened candidate
	// without a static price falls through to DescribeSpotPriceHistory. That
	// would spend a live-price timeout to improve the wording of a request that
	// has already failed. A better message is not worth a slower failure.
	if provider.Capabilities().LiveEnrichment {
		return plain
	}

	result, err := provider.Query(ctx, request.diagnosticQuery())
	if err != nil {
		return plain
	}

	// Nothing at all in the requested regions. The provider was asked for its
	// whole catalogue there, so the regions are what eliminated everything.
	if len(result.Candidates) == 0 {
		return fmt.Errorf("%w: %s publishes no machines in %s",
			ErrNoCandidates, provider.ID(), describeRegions(request.Regions))
	}

	pattern, err := request.machinePattern()
	if err != nil {
		return plain
	}

	remaining := make([]*Candidate, 0, len(result.Candidates))
	for i := range result.Candidates {
		remaining = append(remaining, &result.Candidates[i])
	}

	for _, stage := range rejectionStages(request, pattern) {
		kept := make([]*Candidate, 0, len(remaining))
		for _, candidate := range remaining {
			if stage.accept(candidate) {
				kept = append(kept, candidate)
			}
		}

		if len(kept) > 0 {
			remaining = kept

			continue
		}

		if stage.summarize == nil {
			return fmt.Errorf("%w: %s", ErrNoCandidates, stage.constraint)
		}

		return fmt.Errorf("%w: %s; %s publishes %s",
			ErrNoCandidates, stage.constraint, provider.ID(), stage.summarize(remaining))
	}

	// Every stage kept something, yet ranking kept nothing: the stages here and
	// accepts disagree, which is a bug in this package rather than a bad
	// request. Report the plain message instead of blaming the caller's input.
	return plain
}

// diagnosticQuery asks for the widest catalogue that costs no more to acquire
// than the request already did: the same regions, the same OS, and no other
// filter.
//
// The OS stays because providers stamp it onto the candidates they return
// rather than reading it off the machine — clearing it would hand back
// candidates labelled with the empty OS, which no request matches. It is not a
// filter that can silently eliminate anything either way: an OS the provider
// does not publish is refused by the capability gate, with its own wording,
// before acquisition.
func (r *RecommendRequest) diagnosticQuery() *Query {
	return &Query{Regions: slices.Clone(r.Regions), OS: r.OS}
}

// rejectionStages orders the filters from the outermost constraint inwards, so
// the error names the broadest thing the caller got wrong. Regions are absent:
// diagnoseNoCandidates has already reported them by the time it gets here.
func rejectionStages(request *RecommendRequest, pattern *regexp.Regexp) []rejectionStage {
	stages := []rejectionStage{
		{
			constraint: fmt.Sprintf("no machine is %s", request.Architecture),
			accept:     func(c *Candidate) bool { return c.Machine.Architecture == request.Architecture },
			summarize: listDistinct(func(c *Candidate) string {
				return string(c.Machine.Architecture)
			}),
		},
		{
			constraint: fmt.Sprintf("no machine has at least %d vCPU and %g GiB of memory",
				request.MinVCPU, request.MinMemoryGiB),
			accept: func(c *Candidate) bool {
				return c.Machine.VCPU >= request.MinVCPU && c.Machine.MemoryGiB >= request.MinMemoryGiB
			},
		},
		{
			constraint: "no machine carries a published spot price",
			accept:     func(c *Candidate) bool { return c.Spot != nil && !c.Spot.Amount.IsZero() },
		},
	}

	if pattern != nil {
		stages = append(stages, rejectionStage{
			constraint: fmt.Sprintf("no machine name matches %q", pattern.String()),
			accept:     func(c *Candidate) bool { return pattern.MatchString(string(c.Machine.ID)) },
			summarize:  listDistinct(func(c *Candidate) string { return string(c.Machine.ID) }),
		})
	}

	if request.MaxPrice != nil {
		stages = append(stages, rejectionStage{
			constraint: fmt.Sprintf("no machine costs %s USD per hour or less", request.MaxPrice),
			accept:     func(c *Candidate) bool { return c.Spot.Amount.Nanos() <= request.MaxPrice.Nanos() },
			// The cheapest, not a sample: listing arbitrary prices reads as a
			// claim about the provider's floor and is almost never one.
			summarize: cheapest,
		})
	}

	if request.Workload.RequiresRisk() {
		maxInterruption := request.Workload.maxInterruptionPercent()
		stages = append(stages, rejectionStage{
			constraint: fmt.Sprintf(
				"no machine publishes an interruption rate within the %s workload ceiling of %g%%",
				request.Workload, maxInterruption),
			accept: func(c *Candidate) bool { return acceptsRisk(c, request.Workload, maxInterruption) },
		})
	}

	return stages
}

// describeRegions renders the requested region list for an error message.
func describeRegions(regions []Region) string {
	if len(regions) == 0 || slices.Contains(regions, RegionAll) {
		return "any region"
	}

	names := make([]string, 0, len(regions))
	for _, region := range regions {
		names = append(names, string(region))
	}

	return strings.Join(names, ", ")
}

// listDistinct summarizes a constraint by naming the values on offer, so the
// caller sees what they could have asked for rather than only what was refused.
func listDistinct(value func(*Candidate) string) func([]*Candidate) string {
	return func(candidates []*Candidate) string {
		seen := make([]string, 0, maxReportedValues+1)
		for _, candidate := range candidates {
			observed := value(candidate)
			if observed == "" || slices.Contains(seen, observed) {
				continue
			}

			seen = append(seen, observed)
			if len(seen) > maxReportedValues {
				break
			}
		}
		slices.Sort(seen)

		if len(seen) > maxReportedValues {
			return strings.Join(seen[:maxReportedValues], ", ") + ", and more"
		}

		return strings.Join(seen, ", ")
	}
}

// cheapest summarizes a budget shortfall with the one number that answers it.
func cheapest(candidates []*Candidate) string {
	lowest := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.Spot.Amount.Nanos() < lowest.Spot.Amount.Nanos() {
			lowest = candidate
		}
	}

	return fmt.Sprintf("nothing below %s USD per hour, the price of %s in %s",
		lowest.Spot.Amount, lowest.Machine.ID, lowest.Location.Region)
}
