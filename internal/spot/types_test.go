package spot

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test constants
const (
	testInstanceT3Large  = "t3.large"
	testInstanceT3Medium = "t3.medium"
	testInstanceT3Small  = "t3.small"
	testInstanceT3Nano   = "t3.nano"
)

// Helper to create int pointers
func intPtr(i int) *int { return &i }

// Helper to create test advice with minimal required fields
func createAdvice(instance string, regionScore *int) Advice {
	return Advice{
		Instance:    instance,
		RegionScore: regionScore,
		Region:      "us-east-1",
		Range:       Range{Label: "<5%", Min: 0, Max: 5},
		Savings:     50,
		Info:        TypeInfo{Cores: 2, RAM: 4.0, EMR: false},
		Price:       0.05,
	}
}

func TestByScore_NilSafeSorting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       []Advice
		expected    []string // Instance names in expected order
		description string
	}{
		{
			name: "all_nil_scores_maintain_order",
			input: []Advice{
				createAdvice(testInstanceT3Large, nil),
				createAdvice(testInstanceT3Medium, nil),
				createAdvice(testInstanceT3Small, nil),
			},
			expected:    []string{testInstanceT3Large, testInstanceT3Medium, testInstanceT3Small},
			description: "When all scores are nil, original order should be maintained",
		},
		{
			name: "all_valid_scores_sorted_descending",
			input: []Advice{
				createAdvice(testInstanceT3Large, intPtr(5)),
				createAdvice(testInstanceT3Medium, intPtr(8)),
				createAdvice(testInstanceT3Small, intPtr(3)),
			},
			expected:    []string{testInstanceT3Medium, testInstanceT3Large, testInstanceT3Small},
			description: "Valid scores should be sorted in descending order (highest first)",
		},
		{
			name: "mixed_nil_and_valid_scores",
			input: []Advice{
				createAdvice(testInstanceT3Large, intPtr(5)),
				createAdvice(testInstanceT3Medium, nil),
				createAdvice(testInstanceT3Small, intPtr(8)),
				createAdvice(testInstanceT3Nano, nil),
			},
			expected:    []string{testInstanceT3Small, testInstanceT3Large, testInstanceT3Medium, testInstanceT3Nano},
			description: "Valid scores should come first (sorted desc), then nil scores maintain order",
		},
		{
			name: "equal_scores_maintain_stable_order",
			input: []Advice{
				createAdvice(testInstanceT3Large, intPtr(7)),
				createAdvice(testInstanceT3Medium, intPtr(7)),
				createAdvice(testInstanceT3Small, intPtr(9)),
			},
			expected:    []string{testInstanceT3Small, testInstanceT3Large, testInstanceT3Medium},
			description: "Equal scores should maintain their relative order (stable sort)",
		},
		{
			name: "score_boundary_values",
			input: []Advice{
				createAdvice(testInstanceT3Large, intPtr(1)),   // Min score
				createAdvice(testInstanceT3Medium, intPtr(10)), // Max score
				createAdvice(testInstanceT3Small, intPtr(5)),   // Mid score
			},
			expected:    []string{testInstanceT3Medium, testInstanceT3Small, testInstanceT3Large},
			description: "Boundary values (1, 10) should be handled correctly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Make a copy to avoid modifying original
			advices := make([]Advice, len(tt.input))
			copy(advices, tt.input)

			// Act
			sortAdvices(advices, SortByScore, false)

			// Assert - check the order by instance names
			require.Len(t, advices, len(tt.expected), "Length should be preserved")

			actual := make([]string, len(advices))
			for i, advice := range advices {
				actual[i] = advice.Instance
			}

			assert.Equal(t, tt.expected, actual, tt.description)
		})
	}
}

func TestByScore_ReverseSort(t *testing.T) {
	t.Parallel()

	input := []Advice{
		createAdvice(testInstanceT3Large, intPtr(5)),
		createAdvice(testInstanceT3Medium, intPtr(8)),
		createAdvice(testInstanceT3Small, intPtr(3)),
	}

	expected := []string{testInstanceT3Small, testInstanceT3Large, testInstanceT3Medium}

	// Act
	sortAdvices(input, SortByScore, true) // sortDesc = true

	// Assert
	actual := make([]string, len(input))
	for i, advice := range input {
		actual[i] = advice.Instance
	}

	assert.Equal(t, expected, actual, "Reverse sort should order scores ascending (lowest first)")
}

func TestFilterByMinScore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        []Advice
		minScore     int
		expectedLen  int
		expectedInst []string
		description  string
	}{
		{
			name: "filter_above_threshold",
			input: []Advice{
				createAdvice(testInstanceT3Large, intPtr(5)),
				createAdvice(testInstanceT3Medium, intPtr(8)),
				createAdvice(testInstanceT3Small, intPtr(3)),
				createAdvice(testInstanceT3Nano, intPtr(9)),
			},
			minScore:     7,
			expectedLen:  2,
			expectedInst: []string{testInstanceT3Medium, testInstanceT3Nano},
			description:  "Should only include instances with score >= minScore",
		},
		{
			name: "filter_excludes_nil_scores",
			input: []Advice{
				createAdvice(testInstanceT3Large, intPtr(8)),
				createAdvice(testInstanceT3Medium, nil),
				createAdvice(testInstanceT3Small, intPtr(3)),
			},
			minScore:     5,
			expectedLen:  1,
			expectedInst: []string{testInstanceT3Large},
			description:  "Nil scores should be excluded regardless of threshold",
		},
		{
			name: "no_matches_empty_result",
			input: []Advice{
				createAdvice(testInstanceT3Large, intPtr(3)),
				createAdvice(testInstanceT3Medium, intPtr(4)),
			},
			minScore:     8,
			expectedLen:  0,
			expectedInst: []string{},
			description:  "Should return empty slice when no scores meet threshold",
		},
		{
			name: "all_match_returns_all",
			input: []Advice{
				createAdvice(testInstanceT3Large, intPtr(8)),
				createAdvice(testInstanceT3Medium, intPtr(9)),
			},
			minScore:     5,
			expectedLen:  2,
			expectedInst: []string{testInstanceT3Large, testInstanceT3Medium},
			description:  "Should return all instances when all meet threshold",
		},
		{
			name: "boundary_score_included",
			input: []Advice{
				createAdvice(testInstanceT3Large, intPtr(7)),
				createAdvice(testInstanceT3Medium, intPtr(6)),
			},
			minScore:     7,
			expectedLen:  1,
			expectedInst: []string{testInstanceT3Large},
			description:  "Score equal to minScore should be included",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act
			result := filterByMinScore(tt.input, tt.minScore)

			// Assert
			assert.Len(t, result, tt.expectedLen, tt.description)

			if tt.expectedLen > 0 {
				actual := make([]string, len(result))
				for i, advice := range result {
					actual[i] = advice.Instance
					// Verify all returned scores meet the minimum
					require.NotNil(t, advice.RegionScore, "Filtered results should not have nil scores")
					assert.GreaterOrEqual(t, *advice.RegionScore, tt.minScore,
						"All filtered scores should meet minimum threshold")
				}
				assert.ElementsMatch(t, tt.expectedInst, actual, "Should contain expected instances")
			}
		})
	}
}

func TestSortAdvices_AllSortTypes(t *testing.T) {
	t.Parallel()

	// Create test data with various fields
	input := []Advice{
		{
			Instance:    testInstanceT3Large,
			Region:      "us-west-2",
			RegionScore: intPtr(5),
			Range:       Range{Min: 10, Max: 15},
			Savings:     30,
			Price:       0.10,
		},
		{
			Instance:    testInstanceT3Medium,
			Region:      "us-east-1",
			RegionScore: intPtr(8),
			Range:       Range{Min: 5, Max: 10},
			Savings:     50,
			Price:       0.05,
		},
	}

	tests := []struct {
		name     string
		sortBy   SortBy
		sortDesc bool
		checkFn  func(t *testing.T, result []Advice)
	}{
		{
			name:     "sort_by_score_ascending",
			sortBy:   SortByScore,
			sortDesc: false,
			checkFn: func(t *testing.T, result []Advice) {
				assert.Equal(t, 8, *result[0].RegionScore, "Higher score should be first")
				assert.Equal(t, 5, *result[1].RegionScore, "Lower score should be second")
			},
		},
		{
			name:     "sort_by_score_descending",
			sortBy:   SortByScore,
			sortDesc: true,
			checkFn: func(t *testing.T, result []Advice) {
				assert.Equal(t, 5, *result[0].RegionScore, "Lower score should be first when reversed")
				assert.Equal(t, 8, *result[1].RegionScore, "Higher score should be second when reversed")
			},
		},
		{
			name:     "sort_by_region_still_works",
			sortBy:   SortByRegion,
			sortDesc: false,
			checkFn: func(t *testing.T, result []Advice) {
				assert.Equal(t, "us-east-1", result[0].Region, "us-east-1 should come before us-west-2")
				assert.Equal(t, "us-west-2", result[1].Region, "us-west-2 should come after us-east-1")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Make a copy
			advices := make([]Advice, len(input))
			copy(advices, input)

			// Act
			sortAdvices(advices, tt.sortBy, tt.sortDesc)

			// Assert
			require.Len(t, advices, len(input), "Length should be preserved")
			tt.checkFn(t, advices)
		})
	}
}

// Benchmark tests for performance validation
func BenchmarkByScore_Sort(b *testing.B) {
	// Setup various dataset sizes
	sizes := []int{10, 100, 1000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size_%d", size), func(b *testing.B) {
			// Create test data
			advices := make([]Advice, size)
			for i := 0; i < size; i++ {
				score := (i % 10) + 1 // Scores 1-10
				advices[i] = createAdvice(fmt.Sprintf("instance-%d", i), intPtr(score))
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				// Copy to avoid sorting already sorted data
				testData := make([]Advice, len(advices))
				copy(testData, advices)

				sort.Sort(ByScore(testData))
			}
		})
	}
}

func BenchmarkByScore_SortWithNils(b *testing.B) {
	const size = 1000

	// Create test data with mixed nil and valid scores
	advices := make([]Advice, size)
	for i := 0; i < size; i++ {
		var score *int
		if i%3 != 0 { // 2/3 have scores, 1/3 are nil
			scoreVal := (i % 10) + 1
			score = &scoreVal
		}
		advices[i] = createAdvice(fmt.Sprintf("instance-%d", i), score)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		testData := make([]Advice, len(advices))
		copy(testData, advices)

		sort.Sort(ByScore(testData))
	}
}

func BenchmarkFilterByMinScore(b *testing.B) {
	const size = 1000

	// Create test data
	advices := make([]Advice, size)
	for i := 0; i < size; i++ {
		score := (i % 10) + 1 // Scores 1-10
		advices[i] = createAdvice(fmt.Sprintf("instance-%d", i), intPtr(score))
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = filterByMinScore(advices, 7) // Should filter to ~30% of items
	}
}

// Test functional options pattern
func TestFunctionalOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options []GetSpotSavingsOption
		checkFn func(t *testing.T, cfg *getSpotSavingsConfig)
	}{
		{
			name:    "default_configuration",
			options: []GetSpotSavingsOption{},
			checkFn: func(t *testing.T, cfg *getSpotSavingsConfig) {
				assert.Equal(t, "linux", cfg.instanceOS, "Default OS should be linux")
				assert.Equal(t, SortByRange, cfg.sortBy, "Default sort should be by range")
				assert.Equal(t, 30*time.Second, cfg.scoreTimeout, "Default timeout should be 30s")
				assert.False(t, cfg.withScores, "Scores should be disabled by default")
				assert.False(t, cfg.singleAvailabilityZone, "AZ mode should be disabled by default")
			},
		},
		{
			name: "with_regions_and_pattern",
			options: []GetSpotSavingsOption{
				WithRegions([]string{"us-east-1", "us-west-2"}),
				WithPattern("t3.*"),
			},
			checkFn: func(t *testing.T, cfg *getSpotSavingsConfig) {
				assert.Equal(t, []string{"us-east-1", "us-west-2"}, cfg.regions)
				assert.Equal(t, "t3.*", cfg.pattern)
			},
		},
		{
			name: "with_cpu_memory_price_filters",
			options: []GetSpotSavingsOption{
				WithCPU(4),
				WithMemory(16),
				WithMaxPrice(0.50),
			},
			checkFn: func(t *testing.T, cfg *getSpotSavingsConfig) {
				assert.Equal(t, 4, cfg.cpu)
				assert.Equal(t, 16, cfg.memory)
				assert.Equal(t, 0.50, cfg.maxPrice)
			},
		},
		{
			name: "with_sorting_options",
			options: []GetSpotSavingsOption{
				WithSort(SortByPrice, true),
			},
			checkFn: func(t *testing.T, cfg *getSpotSavingsConfig) {
				assert.Equal(t, SortByPrice, cfg.sortBy)
				assert.True(t, cfg.sortDesc)
			},
		},
		{
			name: "with_score_options",
			options: []GetSpotSavingsOption{
				WithScores(true),
				WithMinScore(7),
				WithScoreTimeout(60 * time.Second),
			},
			checkFn: func(t *testing.T, cfg *getSpotSavingsConfig) {
				assert.True(t, cfg.withScores)
				assert.Equal(t, 7, cfg.minScore)
				assert.Equal(t, 60*time.Second, cfg.scoreTimeout)
			},
		},
		{
			name: "with_availability_zone_mode",
			options: []GetSpotSavingsOption{
				WithScores(true),
				WithSingleAvailabilityZone(true),
			},
			checkFn: func(t *testing.T, cfg *getSpotSavingsConfig) {
				assert.True(t, cfg.withScores)
				assert.True(t, cfg.singleAvailabilityZone)
			},
		},
		{
			name: "combined_options",
			options: []GetSpotSavingsOption{
				WithRegions([]string{"us-west-1"}),
				WithPattern("m5.*"),
				WithOS("windows"),
				WithCPU(2),
				WithMemory(8),
				WithMaxPrice(0.25),
				WithSort(SortBySavings, false),
				WithScores(true),
				WithMinScore(8),
				WithScoreTimeout(45 * time.Second),
			},
			checkFn: func(t *testing.T, cfg *getSpotSavingsConfig) {
				assert.Equal(t, []string{"us-west-1"}, cfg.regions)
				assert.Equal(t, "m5.*", cfg.pattern)
				assert.Equal(t, "windows", cfg.instanceOS)
				assert.Equal(t, 2, cfg.cpu)
				assert.Equal(t, 8, cfg.memory)
				assert.Equal(t, 0.25, cfg.maxPrice)
				assert.Equal(t, SortBySavings, cfg.sortBy)
				assert.False(t, cfg.sortDesc)
				assert.True(t, cfg.withScores)
				assert.Equal(t, 8, cfg.minScore)
				assert.Equal(t, 45*time.Second, cfg.scoreTimeout)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create default config and apply options
			cfg := &getSpotSavingsConfig{
				instanceOS:   "linux",
				sortBy:       SortByRange,
				scoreTimeout: 30 * time.Second,
			}

			for _, opt := range tt.options {
				opt(cfg)
			}

			// Run the test validation
			tt.checkFn(t, cfg)
		})
	}
}
