package spot

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

//go:embed data/architecture-snapshot.json
var architectureSnapshotFS embed.FS

// Architecture identifies an EC2 instance processor architecture.
type Architecture string

const (
	// ArchitectureX8664 is the 64-bit x86 EC2 architecture.
	ArchitectureX8664 Architecture = "x86_64"
	// ArchitectureARM64 is the 64-bit Arm EC2 architecture.
	ArchitectureARM64 Architecture = "arm64"

	// WorkloadWeb limits interruption frequency to the <5% Advisor range.
	WorkloadWeb Workload = "web"
	// WorkloadCI limits interruption frequency to the 10-15% Advisor range.
	WorkloadCI Workload = "ci"
	// WorkloadBatch limits interruption frequency to the 15-20% Advisor range.
	WorkloadBatch Workload = "batch"

	// OperatingSystemLinux selects Linux Spot prices.
	OperatingSystemLinux = "linux"
	// OperatingSystemWindows selects Windows Spot prices.
	OperatingSystemWindows = "windows"

	// DefaultRecommendationTop is the number of candidates returned when --top is omitted.
	DefaultRecommendationTop = 3

	webMaxInterruption   = 5
	ciMaxInterruption    = 16
	batchMaxInterruption = 22

	rationaleArchitectureMatch  = "ARCHITECTURE_MATCH"
	rationaleKnownPositivePrice = "KNOWN_POSITIVE_PRICE"
	rationaleMemoryMinimumMet   = "MEMORY_MINIMUM_MET"
	rationaleVCPUMinimumMet     = "VCPU_MINIMUM_MET"
)

// Workload selects the interruption-frequency cap used for a recommendation.
type Workload string

// RecommendationOptions defines constraints for single-instance recommendations.
type RecommendationOptions struct { //nolint:govet // Field order follows CLI constraint groupings.
	Architecture Architecture
	Instance     string
	OS           string
	CPU          int
	Memory       int
	Budget       float64
	Workload     Workload
	Top          int
}

// Recommendation is the deterministic recommendation DTO. It intentionally
// contains only single-instance facts; placement scores and composite scores
// are not part of recommendation ranking.
type Recommendation struct { //nolint:govet // JSON field grouping is clearer than memory-layout optimization.
	Region                string       `json:"region"`
	Instance              string       `json:"instance"`
	Architecture          Architecture `json:"architecture"`
	VCPU                  int          `json:"vcpu"`
	MemoryGiB             float32      `json:"memory_gib"`
	PriceUSDPerHour       float64      `json:"price_usd_per_hour"`
	SavingsPercent        int          `json:"savings_percent"`
	InterruptionFrequency string       `json:"interruption_frequency"`
	RationaleCodes        []string     `json:"rationale_codes"`
}

// ArchitectureLookup maps an Advisor instance family to its reviewed architecture.
type ArchitectureLookup struct {
	families map[string]Architecture
}

type architectureSnapshot struct {
	Families map[string]Architecture `json:"families"`
}

var (
	// ErrInvalidRecommendationInput identifies an invalid recommendation constraint.
	ErrInvalidRecommendationInput = errors.New("invalid recommendation input")
	// ErrNoRecommendationCandidates identifies a valid query with no matching instance.
	ErrNoRecommendationCandidates = errors.New("no recommendation candidates")
)

// LoadEmbeddedArchitectureLookup parses the committed, reviewed architecture
// snapshot. It makes no AWS metadata calls and unknown families are deliberately
// omitted rather than inferred from their names.
func LoadEmbeddedArchitectureLookup() (*ArchitectureLookup, error) {
	contents, err := architectureSnapshotFS.ReadFile("data/architecture-snapshot.json")
	if err != nil {
		return nil, fmt.Errorf("read embedded architecture snapshot: %w", err)
	}

	return parseArchitectureSnapshot(contents)
}

func parseArchitectureSnapshot(contents []byte) (*ArchitectureLookup, error) {
	var snapshot architectureSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		return nil, fmt.Errorf("parse architecture snapshot: %w", err)
	}
	if len(snapshot.Families) == 0 {
		return nil, errors.New("architecture snapshot contains no families")
	}

	lookup := &ArchitectureLookup{families: make(map[string]Architecture, len(snapshot.Families))}
	for family, architecture := range snapshot.Families {
		if family == "" || strings.Contains(family, ".") {
			return nil, fmt.Errorf("invalid architecture family %q", family)
		}
		if architecture != ArchitectureX8664 && architecture != ArchitectureARM64 {
			return nil, fmt.Errorf("invalid architecture %q for family %q", architecture, family)
		}
		lookup.families[family] = architecture
	}

	return lookup, nil
}

// ArchitectureForInstance returns an architecture only when its family is in
// the reviewed snapshot. It fails closed for unknown or malformed instance types.
func (l *ArchitectureLookup) ArchitectureForInstance(instance string) (Architecture, bool) {
	family, _, ok := strings.Cut(instance, ".")
	if !ok || family == "" || l == nil {
		return "", false
	}

	architecture, ok := l.families[family]
	return architecture, ok
}

// ValidateRecommendationOptions validates constraints before any data fetch.
func ValidateRecommendationOptions(opts *RecommendationOptions) error { //nolint:gocyclo,cyclop // Each invalid constraint needs a specific error.
	if opts == nil {
		return fmt.Errorf("%w: options are required", ErrInvalidRecommendationInput)
	}
	if opts.Architecture != ArchitectureX8664 && opts.Architecture != ArchitectureARM64 {
		return fmt.Errorf("%w: architecture must be x86_64 or arm64", ErrInvalidRecommendationInput)
	}
	if opts.CPU <= 0 {
		return fmt.Errorf("%w: cpu must be positive", ErrInvalidRecommendationInput)
	}
	if opts.Memory <= 0 {
		return fmt.Errorf("%w: memory must be positive", ErrInvalidRecommendationInput)
	}
	if opts.OS != OperatingSystemLinux && opts.OS != OperatingSystemWindows {
		return fmt.Errorf("%w: os must be linux or windows", ErrInvalidRecommendationInput)
	}
	if opts.Budget < 0 || math.IsNaN(opts.Budget) || math.IsInf(opts.Budget, 0) {
		return fmt.Errorf("%w: budget must be a positive USD instance-hour price", ErrInvalidRecommendationInput)
	}
	if opts.Instance != "" {
		if _, err := regexp.Compile(opts.Instance); err != nil {
			return fmt.Errorf("%w: instance regexp: %w", ErrInvalidRecommendationInput, err)
		}
	}

	workload := opts.Workload
	if workload == "" {
		workload = WorkloadWeb
	}
	if workload != WorkloadWeb && workload != WorkloadCI && workload != WorkloadBatch {
		return fmt.Errorf("%w: workload must be web, ci, or batch", ErrInvalidRecommendationInput)
	}
	if opts.Top < 0 {
		return fmt.Errorf("%w: top must be positive", ErrInvalidRecommendationInput)
	}

	return nil
}

// Recommend filters and ranks individual Advisor entries without performing I/O.
// Candidates rank by price, interruption, right-sizing excess, region, and type.
// Savings is displayed but never used as a ranking factor.
func Recommend(advices []Advice, opts *RecommendationOptions, architectures *ArchitectureLookup) ([]Recommendation, error) { //nolint:gocyclo,cyclop // Constraint screening is deliberately kept in this pure core.
	if err := ValidateRecommendationOptions(opts); err != nil {
		return nil, err
	}
	if architectures == nil {
		return nil, errors.New("architecture lookup is required")
	}

	workload := opts.Workload
	if workload == "" {
		workload = WorkloadWeb
	}
	top := opts.Top
	if top == 0 {
		top = DefaultRecommendationTop
	}
	maxInterruption := workloadMaxInterruption(workload)

	var pattern *regexp.Regexp
	if opts.Instance != "" {
		pattern = regexp.MustCompile(opts.Instance) // validated above
	}

	type candidate struct {
		recommendation  Recommendation
		maxInterruption int
		excessCPU       int
		excessMemory    float32
	}
	candidates := make([]candidate, 0, len(advices))
	for _, advice := range advices {
		architecture, ok := recommendationArchitecture(&advice, opts, pattern, architectures, maxInterruption)
		if !ok {
			continue
		}

		candidates = append(candidates, candidate{
			recommendation: Recommendation{
				Region:                advice.Region,
				Instance:              advice.Instance,
				Architecture:          architecture,
				VCPU:                  advice.Info.Cores,
				MemoryGiB:             advice.Info.RAM,
				PriceUSDPerHour:       advice.Price,
				SavingsPercent:        advice.Savings,
				InterruptionFrequency: advice.Range.Label,
				RationaleCodes:        rationaleCodes(workload, opts.Budget > 0),
			},
			maxInterruption: advice.Range.Max,
			excessCPU:       advice.Info.Cores - opts.CPU,
			excessMemory:    advice.Info.RAM - float32(opts.Memory),
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.recommendation.PriceUSDPerHour != right.recommendation.PriceUSDPerHour {
			return left.recommendation.PriceUSDPerHour < right.recommendation.PriceUSDPerHour
		}
		if left.maxInterruption != right.maxInterruption {
			return left.maxInterruption < right.maxInterruption
		}
		if left.excessCPU != right.excessCPU {
			return left.excessCPU < right.excessCPU
		}
		if left.excessMemory != right.excessMemory {
			return left.excessMemory < right.excessMemory
		}
		if left.recommendation.Region != right.recommendation.Region {
			return left.recommendation.Region < right.recommendation.Region
		}
		return left.recommendation.Instance < right.recommendation.Instance
	})

	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w for architecture %s and workload %s", ErrNoRecommendationCandidates, opts.Architecture, workload)
	}
	if len(candidates) > top {
		candidates = candidates[:top]
	}

	recommendations := make([]Recommendation, len(candidates))
	for i := range candidates {
		recommendations[i] = candidates[i].recommendation
	}

	return recommendations, nil
}

func recommendationArchitecture(advice *Advice, opts *RecommendationOptions, pattern *regexp.Regexp,
	architectures *ArchitectureLookup, maxInterruption int) (Architecture, bool) {
	architecture, known := architectures.ArchitectureForInstance(advice.Instance)
	if !known || architecture != opts.Architecture || !knownPositivePrice(advice.Price) {
		return "", false
	}
	if pattern != nil && !pattern.MatchString(advice.Instance) {
		return "", false
	}
	if advice.Info.Cores < opts.CPU || advice.Info.RAM < float32(opts.Memory) {
		return "", false
	}
	if (opts.Budget > 0 && advice.Price > opts.Budget) || advice.Range.Max > maxInterruption {
		return "", false
	}

	return architecture, true
}

func rationaleCodes(workload Workload, budgeted bool) []string {
	codes := []string{
		rationaleArchitectureMatch,
		rationaleKnownPositivePrice,
		rationaleMemoryMinimumMet,
		rationaleVCPUMinimumMet,
		"WORKLOAD_" + strings.ToUpper(string(workload)) + "_CAP_MET",
	}
	if budgeted {
		codes = append(codes, "BUDGET_CAP_MET")
	}
	sort.Strings(codes)
	return codes
}

func knownPositivePrice(price float64) bool {
	return price > 0 && !math.IsNaN(price) && !math.IsInf(price, 0)
}

func workloadMaxInterruption(workload Workload) int {
	switch workload {
	case WorkloadCI:
		return ciMaxInterruption // Advisor's 10-15% range
	case WorkloadBatch:
		return batchMaxInterruption // Advisor's 15-20% range
	default:
		return webMaxInterruption // Advisor's <5% range
	}
}
