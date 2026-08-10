package spot

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Diagnosing an empty v1 recommendation.
//
// `no recommendation candidates for architecture arm64 and workload web` named
// the two constraints that were satisfied and never the one that eliminated the
// results, so a budget below every price read as an architecture with no
// capacity. The narrowing below re-applies the same screens as
// recommendationArchitecture, one at a time, and reports the first that empties
// the set.
//
// It runs only after the ranking loop has already found nothing, over advices
// that are already in memory. The success path pays nothing and neither path
// performs I/O.

// maxReportedInstances bounds how many instance types an error lists.
const maxReportedInstances = 8

// noRecommendationCandidates explains an empty result in terms of the request.
func noRecommendationCandidates(advices []Advice, opts *RecommendationOptions, pattern *regexp.Regexp,
	architectures *ArchitectureLookup, workload Workload, maxInterruption int,
) error {
	plain := fmt.Errorf("%w for architecture %s and workload %s",
		ErrNoRecommendationCandidates, opts.Architecture, workload)

	// Acquisition applies the vCPU, memory, budget and pattern screens too, so an
	// empty set means one of those emptied it before this package saw anything.
	// There is nothing left to narrow, and re-acquiring to find out would cost a
	// second AWS fetch on a path that has already failed — so report the
	// constraints that were in force instead of blaming architecture and
	// workload, which are the two that demonstrably were not applied yet.
	if len(advices) == 0 {
		return fmt.Errorf("%w: nothing in the queried regions satisfies %s",
			ErrNoRecommendationCandidates, describeConstraints(opts))
	}

	remaining := make([]*Advice, 0, len(advices))
	for i := range advices {
		remaining = append(remaining, &advices[i])
	}

	for _, stage := range recommendationStages(opts, pattern, architectures, workload, maxInterruption) {
		kept := make([]*Advice, 0, len(remaining))
		for _, advice := range remaining {
			if stage.accept(advice) {
				kept = append(kept, advice)
			}
		}

		if len(kept) > 0 {
			remaining = kept

			continue
		}

		if stage.summarize == nil {
			return fmt.Errorf("%w: %s", ErrNoRecommendationCandidates, stage.constraint)
		}

		return fmt.Errorf("%w: %s; %s", ErrNoRecommendationCandidates, stage.constraint, stage.summarize(remaining))
	}

	return plain
}

// describeConstraints names the screens acquisition applied, in flag terms.
func describeConstraints(opts *RecommendationOptions) string {
	described := []string{"os " + opts.OS}
	if opts.CPU > 0 {
		described = append(described, fmt.Sprintf("at least %d vCPU", opts.CPU))
	}
	if opts.Memory > 0 {
		described = append(described, fmt.Sprintf("at least %d GiB of memory", opts.Memory))
	}
	if opts.Budget > 0 {
		described = append(described, fmt.Sprintf("a budget of %.4f USD per hour", opts.Budget))
	}
	if opts.Instance != "" {
		described = append(described, fmt.Sprintf("a machine name matching %q", opts.Instance))
	}

	return strings.Join(described, ", ")
}

type adviceStage struct {
	accept     func(*Advice) bool
	summarize  func([]*Advice) string
	constraint string
}

// recommendationStages orders the screens from the outermost constraint inwards.
func recommendationStages(opts *RecommendationOptions, pattern *regexp.Regexp,
	architectures *ArchitectureLookup, workload Workload, maxInterruption int,
) []adviceStage {
	stages := []adviceStage{
		{
			constraint: fmt.Sprintf("no instance type is classified as %s", opts.Architecture),
			accept: func(a *Advice) bool {
				architecture, known := architectures.ArchitectureForInstance(a.Instance)

				return known && architecture == opts.Architecture
			},
		},
		{
			constraint: "no instance type carries a known positive price",
			accept:     func(a *Advice) bool { return knownPositivePrice(a.Price) },
		},
		{
			constraint: fmt.Sprintf("no instance type has at least %d vCPU and %d GiB of memory",
				opts.CPU, opts.Memory),
			accept: func(a *Advice) bool {
				return a.Info.Cores >= opts.CPU && a.Info.RAM >= float32(opts.Memory)
			},
		},
	}

	if pattern != nil {
		stages = append(stages, adviceStage{
			constraint: fmt.Sprintf("no instance type matches %q", pattern.String()),
			accept:     func(a *Advice) bool { return pattern.MatchString(a.Instance) },
			summarize:  listInstances,
		})
	}

	if opts.Budget > 0 {
		stages = append(stages, adviceStage{
			constraint: fmt.Sprintf("no instance type costs %.4f USD per hour or less", opts.Budget),
			accept:     func(a *Advice) bool { return a.Price <= opts.Budget },
			// The cheapest, not a sample: an arbitrary price reads as a claim
			// about the floor and is almost never one.
			summarize: cheapestAdvice,
		})
	}

	return append(stages, adviceStage{
		constraint: fmt.Sprintf(
			"no instance type has an interruption frequency within the %s workload ceiling of %d%%",
			workload, maxInterruption),
		accept:    func(a *Advice) bool { return a.Range.Max <= maxInterruption },
		summarize: lowestInterruption,
	})
}

func listInstances(advices []*Advice) string {
	seen := make([]string, 0, maxReportedInstances+1)
	for _, advice := range advices {
		if slices.Contains(seen, advice.Instance) {
			continue
		}

		seen = append(seen, advice.Instance)
		if len(seen) > maxReportedInstances {
			break
		}
	}
	slices.Sort(seen)

	if len(seen) > maxReportedInstances {
		return "available: " + strings.Join(seen[:maxReportedInstances], ", ") + ", and more"
	}

	return "available: " + strings.Join(seen, ", ")
}

func cheapestAdvice(advices []*Advice) string {
	lowest := advices[0]
	for _, advice := range advices[1:] {
		if advice.Price < lowest.Price {
			lowest = advice
		}
	}

	return fmt.Sprintf("nothing below %.4f USD per hour, the price of %s in %s",
		lowest.Price, lowest.Instance, lowest.Region)
}

func lowestInterruption(advices []*Advice) string {
	lowest := advices[0]
	for _, advice := range advices[1:] {
		if advice.Range.Max < lowest.Range.Max {
			lowest = advice
		}
	}

	return fmt.Sprintf("the calmest is %s in %s at %s", lowest.Instance, lowest.Region, lowest.Range.Label)
}
