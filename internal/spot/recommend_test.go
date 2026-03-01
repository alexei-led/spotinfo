package spot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetWorkloadPreset(t *testing.T) {
	tests := []struct {
		name          string
		workloadType  string
		expectedMax   float64
		expectedPrice float64
	}{
		{
			name:          "web preset",
			workloadType:  "web",
			expectedMax:   0.05,
			expectedPrice: 0.25,
		},
		{
			name:          "batch preset",
			workloadType:  "batch",
			expectedMax:   0.20,
			expectedPrice: 0.45,
		},
		{
			name:          "ml preset",
			workloadType:  "ml",
			expectedMax:   0.10,
			expectedPrice: 0.25,
		},
		{
			name:          "ci preset",
			workloadType:  "ci",
			expectedMax:   0.15,
			expectedPrice: 0.40,
		},
		{
			name:          "unknown preset defaults to batch",
			workloadType:  "unknown",
			expectedMax:   0.20,
			expectedPrice: 0.45,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preset := GetWorkloadPreset(tt.workloadType)
			assert.Equal(t, tt.expectedMax, preset.MaxInterruption)
			assert.Equal(t, tt.expectedPrice, preset.PriceWeight)
		})
	}
}

func TestNewRecommendationEngine(t *testing.T) {
	engine := NewRecommendationEngine("batch")
	require.NotNil(t, engine)
	assert.Equal(t, WorkloadBatch, engine.preset.Type)
}

func TestNormalizePrices(t *testing.T) {
	engine := NewRecommendationEngine("batch")

	advices := []Advice{
		{Instance: "m5.large", Price: 0.05},
		{Instance: "m6.large", Price: 0.03},
		{Instance: "m7.large", Price: 0.07},
	}

	result := engine.normalizePrices(advices)

	// m6.large (cheapest) should have highest score
	assert.True(t, result["m6.large"] > result["m5.large"])
	assert.True(t, result["m5.large"] > result["m7.large"])

	// All scores should be in valid range
	for _, score := range result {
		assert.GreaterOrEqual(t, score, 0.0)
		assert.LessOrEqual(t, score, 1.0)
	}
}

func TestScoreAdvice(t *testing.T) {
	engine := NewRecommendationEngine("batch")

	advice := Advice{
		Instance:    "m5.large",
		Price:       0.05,
		Savings:     65,
		Range:       Range{Min: 5, Max: 15},
		RegionScore: intPtr(7),
	}

	priceNorm := map[string]float64{
		"m5.large": 0.5,
	}

	item := engine.scoreAdvice(advice, priceNorm)

	// Check basic properties
	assert.Equal(t, "m5.large", item.Instance)
	assert.Equal(t, 0.05, item.Price)
	assert.Equal(t, 65, item.Savings)
	assert.Equal(t, 7, item.PlacementScore)

	// Score should be in reasonable range
	assert.GreaterOrEqual(t, item.Score, 0.0)
	assert.LessOrEqual(t, item.Score, 1.0)

	// Breakdown components should exist
	assert.NotEmpty(t, item.BreakdownComponents)
	assert.Equal(t, 0.5, item.BreakdownComponents["price"])
}

func TestRecommend(t *testing.T) {
	engine := NewRecommendationEngine("batch")

	advices := []Advice{
		{
			Instance:    "m6g.large",
			Price:       0.031,
			Savings:     68,
			Range:       Range{Min: 5, Max: 10},
			RegionScore: intPtr(8),
		},
		{
			Instance:    "m5.large",
			Price:       0.034,
			Savings:     65,
			Range:       Range{Min: 5, Max: 10},
			RegionScore: intPtr(7),
		},
		{
			Instance:    "m7.large",
			Price:       0.042,
			Savings:     62,
			Range:       Range{Min: 8, Max: 15},
			RegionScore: intPtr(6),
		},
	}

	rec := engine.Recommend(advices, 3)

	// Check recommendation structure
	require.NotNil(t, rec)
	assert.Equal(t, WorkloadBatch, rec.Workload)
	assert.Len(t, rec.Candidates, 3)

	// Candidates should be ranked 1-3
	for i, candidate := range rec.Candidates {
		assert.Equal(t, i+1, candidate.Rank)
	}

	// First candidate should have highest score
	assert.Greater(t, rec.Candidates[0].Score, rec.Candidates[1].Score)
	assert.Greater(t, rec.Candidates[1].Score, rec.Candidates[2].Score)
}

func TestRecommendTopN(t *testing.T) {
	engine := NewRecommendationEngine("batch")

	advices := []Advice{
		{
			Instance:    "m5.large",
			Price:       0.03,
			Savings:     65,
			Range:       Range{Min: 5, Max: 10},
			RegionScore: intPtr(7),
		},
		{
			Instance:    "m6.large",
			Price:       0.04,
			Savings:     64,
			Range:       Range{Min: 6, Max: 11},
			RegionScore: intPtr(7),
		},
		{
			Instance:    "m7.large",
			Price:       0.05,
			Savings:     63,
			Range:       Range{Min: 7, Max: 12},
			RegionScore: intPtr(7),
		},
	}

	rec := engine.Recommend(advices, 2)
	assert.Len(t, rec.Candidates, 2)

	rec = engine.Recommend(advices, 10)
	assert.Len(t, rec.Candidates, 3)
}

func TestGenerateRationale(t *testing.T) {
	engine := NewRecommendationEngine("batch")

	// High score, low interruption, high savings
	advice := Advice{
		Instance:    "m6g.large",
		Price:       0.031,
		Savings:     68,
		Range:       Range{Min: 3, Max: 5},
		RegionScore: intPtr(9),
	}

	item := RecommendationItem{
		Instance:         "m6g.large",
		Score:            0.85,
		InterruptionRate: 0.04,
		Savings:          68,
		PlacementScore:   9,
		BreakdownComponents: map[string]float64{
			"price":    0.8,
			"stability": 0.96,
			"savings":   0.68,
			"placement": 0.9,
		},
	}

	rationale := engine.generateRationale(advice, item, WorkloadBatch)
	assert.NotEmpty(t, rationale)
	assert.Contains(t, rationale, "Score")
}

func TestFilterByInterruptionTolerance(t *testing.T) {
	engine := NewRecommendationEngine("web") // 5% max

	candidates := []RecommendationItem{
		{Instance: "m5.large", InterruptionRate: 0.03},
		{Instance: "m6.large", InterruptionRate: 0.05},
		{Instance: "m7.large", InterruptionRate: 0.08},
		{Instance: "m8.large", InterruptionRate: 0.02},
	}

	filtered := engine.FilterByInterruptionTolerance(candidates)

	// Should include items with interruption rate <= 0.05
	assert.Len(t, filtered, 3)
	for _, item := range filtered {
		assert.LessOrEqual(t, item.InterruptionRate, 0.05)
	}
}

func TestGenerateSummary(t *testing.T) {
	engine := NewRecommendationEngine("batch")

	candidates := []RecommendationItem{
		{Instance: "m5.large", Score: 0.85},
		{Instance: "m6.large", Score: 0.75},
	}

	summary := engine.generateSummary(candidates)
	assert.NotEmpty(t, summary)
	assert.Contains(t, summary, "m5.large")
	assert.Contains(t, summary, "85")
}


