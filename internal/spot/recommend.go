package spot

import (
	"fmt"
	"math"
	"sort"
)

// WorkloadPresetType defines the type of workload for recommendations.
type WorkloadPresetType string

const (
	// WorkloadWeb is for web applications requiring low interruption.
	WorkloadWeb WorkloadPresetType = "web"
	// WorkloadBatch is for batch jobs tolerating high interruption.
	WorkloadBatch WorkloadPresetType = "batch"
	// WorkloadML is for machine learning workloads.
	WorkloadML WorkloadPresetType = "ml"
	// WorkloadCI is for CI/CD pipelines.
	WorkloadCI WorkloadPresetType = "ci"
)

// WorkloadPreset defines weight vectors for recommendation scoring.
type WorkloadPreset struct {
	Type              WorkloadPresetType
	MaxInterruption   float64 // max acceptable interruption rate (0-1)
	PriceWeight       float64 // weight for price in composite score
	InterruptionWeight float64 // weight for stability (1 - interruption_rate)
	SavingsWeight     float64 // weight for savings percentage
	PlacementWeight   float64 // weight for placement score
}

// GetWorkloadPreset returns the preset configuration for a given workload type.
func GetWorkloadPreset(workloadType string) *WorkloadPreset {
	switch WorkloadPresetType(workloadType) {
	case WorkloadWeb:
		return &WorkloadPreset{
			Type:              WorkloadWeb,
			MaxInterruption:   0.05, // <5%
			PriceWeight:       0.25,
			InterruptionWeight: 0.40,
			SavingsWeight:     0.20,
			PlacementWeight:   0.15,
		}
	case WorkloadBatch:
		return &WorkloadPreset{
			Type:              WorkloadBatch,
			MaxInterruption:   0.20, // <20%
			PriceWeight:       0.45,
			InterruptionWeight: 0.15,
			SavingsWeight:     0.25,
			PlacementWeight:   0.15,
		}
	case WorkloadML:
		return &WorkloadPreset{
			Type:              WorkloadML,
			MaxInterruption:   0.10, // <10%
			PriceWeight:       0.25,
			InterruptionWeight: 0.40,
			SavingsWeight:     0.15,
			PlacementWeight:   0.20,
		}
	case WorkloadCI:
		return &WorkloadPreset{
			Type:              WorkloadCI,
			MaxInterruption:   0.15, // <15%
			PriceWeight:       0.40,
			InterruptionWeight: 0.25,
			SavingsWeight:     0.20,
			PlacementWeight:   0.15,
		}
	default:
		// Default to batch if unknown
		return GetWorkloadPreset(string(WorkloadBatch))
	}
}

// RecommendationItem represents a single recommended instance.
type RecommendationItem struct {
	Instance           string
	Rank               int
	Score              float64
	Price              float64
	Savings            int // savings percentage
	InterruptionRate   float64
	PlacementScore     int
	Rationale          string
	BreakdownComponents map[string]float64 // detailed scoring breakdown
}

// Recommendation contains the result of a recommendation query.
type Recommendation struct {
	Query       string // human-readable query description
	Workload    WorkloadPresetType
	Candidates  []RecommendationItem
	Summary     string
}

// RecommendationEngine performs instance recommendations based on workload specs.
type RecommendationEngine struct {
	preset *WorkloadPreset
}

// NewRecommendationEngine creates a new recommendation engine.
func NewRecommendationEngine(workloadType string) *RecommendationEngine {
	return &RecommendationEngine{
		preset: GetWorkloadPreset(workloadType),
	}
}

// Recommend generates instance recommendations for given specs.
func (re *RecommendationEngine) Recommend(advices []Advice, topN int) *Recommendation {
	if topN <= 0 {
		topN = 3
	}

	candidates := make([]RecommendationItem, 0, len(advices))
	
	// Score each candidate
	priceNorm := re.normalizePrices(advices)
	for i, advice := range advices {
		item := re.scoreAdvice(advice, priceNorm)
		item.Rank = i + 1
		candidates = append(candidates, item)
	}

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	// Renumber ranks after sort
	for i := range candidates {
		candidates[i].Rank = i + 1
	}

	// Limit to topN
	if topN < len(candidates) {
		candidates = candidates[:topN]
	}

	rec := &Recommendation{
		Workload:   re.preset.Type,
		Candidates: candidates,
		Query:      fmt.Sprintf("Workload: %s", re.preset.Type),
		Summary:    re.generateSummary(candidates),
	}

	return rec
}

// scoreAdvice calculates the composite score for a single advice.
func (re *RecommendationEngine) scoreAdvice(advice Advice, priceNorm map[string]float64) RecommendationItem {
	item := RecommendationItem{
		Instance:            advice.Instance,
		Price:               advice.Price,
		Savings:             advice.Savings,
		BreakdownComponents: make(map[string]float64),
	}

	// Extract interruption rate from range (use midpoint as estimate)
	interruptionRate := float64(advice.Range.Min+advice.Range.Max) / 200.0 // Convert 1-20 scale to 0-0.20
	item.InterruptionRate = interruptionRate

	// Extract placement score
	if advice.RegionScore != nil {
		item.PlacementScore = *advice.RegionScore
	}

	// Calculate normalized components
	priceScore := priceNorm[advice.Instance]
	item.BreakdownComponents["price"] = priceScore

	stabilityScore := 1.0 - interruptionRate // Higher is better (less interruption)
	item.BreakdownComponents["stability"] = stabilityScore

	savingsScore := float64(advice.Savings) / 100.0 // Normalize 0-100 to 0-1
	item.BreakdownComponents["savings"] = savingsScore

	placementScore := 0.5 // Default middle score
	if advice.RegionScore != nil {
		placementScore = float64(*advice.RegionScore) / 10.0 // Normalize 1-10 to 0-1
	}
	item.BreakdownComponents["placement"] = placementScore

	// Composite score
	item.Score = (re.preset.PriceWeight * priceScore) +
		(re.preset.InterruptionWeight * stabilityScore) +
		(re.preset.SavingsWeight * savingsScore) +
		(re.preset.PlacementWeight * placementScore)

	// Generate rationale
	item.Rationale = re.generateRationale(advice, item, re.preset.Type)

	return item
}

// normalizePrices normalizes prices to 0-1 scale (lower price = higher score).
func (re *RecommendationEngine) normalizePrices(advices []Advice) map[string]float64 {
	result := make(map[string]float64, len(advices))

	if len(advices) == 0 {
		return result
	}

	// Find min and max prices
	minPrice := math.MaxFloat64
	maxPrice := 0.0

	for _, advice := range advices {
		if advice.Price > 0 {
			if advice.Price < minPrice {
				minPrice = advice.Price
			}
			if advice.Price > maxPrice {
				maxPrice = advice.Price
			}
		}
	}

	// Normalize: lower price = higher score
	for _, advice := range advices {
		if maxPrice > minPrice && advice.Price > 0 {
			// Invert: low price should give high score
			normalized := 1.0 - ((advice.Price - minPrice) / (maxPrice - minPrice))
			result[advice.Instance] = normalized
		} else {
			result[advice.Instance] = 0.5 // Default if all prices equal
		}
	}

	return result
}

// generateRationale creates a human-friendly reason for the recommendation.
func (re *RecommendationEngine) generateRationale(advice Advice, item RecommendationItem, workload WorkloadPresetType) string {
	strengths := []string{}

	// Price strength
	if item.BreakdownComponents["price"] > 0.7 {
		strengths = append(strengths, fmt.Sprintf("best price/stability ratio"))
	} else if item.BreakdownComponents["price"] > 0.5 {
		strengths = append(strengths, fmt.Sprintf("good price"))
	}

	// Stability strength  
	if item.InterruptionRate < 0.05 {
		strengths = append(strengths, fmt.Sprintf("excellent stability (<5%% interruption)"))
	} else if item.InterruptionRate < 0.10 {
		strengths = append(strengths, fmt.Sprintf("good stability"))
	}

	// Savings strength
	if item.Savings > 70 {
		strengths = append(strengths, fmt.Sprintf("%d%% savings vs on-demand", item.Savings))
	}

	// Placement score strength
	if item.PlacementScore >= 8 {
		strengths = append(strengths, fmt.Sprintf("excellent placement score (%d/10)", item.PlacementScore))
	}

	rationale := fmt.Sprintf("Score: %.1f", item.Score*100)
	if len(strengths) > 0 {
		rationale = fmt.Sprintf("%s — %s", rationale, joinWithComma(strengths))
	}

	return rationale
}

// joinWithComma joins strings with commas and "and" for last element.
func joinWithComma(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	if len(strs) == 1 {
		return strs[0]
	}
	if len(strs) == 2 {
		return fmt.Sprintf("%s and %s", strs[0], strs[1])
	}
	return fmt.Sprintf("%s and %s", joinWithComma(strs[:len(strs)-1]), strs[len(strs)-1])
}

// generateSummary creates a brief summary of the recommendation.
func (re *RecommendationEngine) generateSummary(candidates []RecommendationItem) string {
	if len(candidates) == 0 {
		return "No recommendations available"
	}

	best := candidates[0]
	return fmt.Sprintf("Top recommendation: %s (score: %.1f)", best.Instance, best.Score*100)
}

// FilterByInterruptionTolerance filters candidates by max interruption rate for the workload.
func (re *RecommendationEngine) FilterByInterruptionTolerance(candidates []RecommendationItem) []RecommendationItem {
	filtered := make([]RecommendationItem, 0, len(candidates))
	for _, item := range candidates {
		if item.InterruptionRate <= re.preset.MaxInterruption {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
