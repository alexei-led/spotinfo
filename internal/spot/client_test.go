package spot

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProviders holds all mocks for a test case
type mockProviders struct {
	advisor *mockadvisorProvider
	pricing *mockpricingProvider
}

// Helper functions to reduce test complexity and repetition
func setupSingleInstanceTest(region, instance, os string) func(*mockProviders) {
	return func(m *mockProviders) {
		m.advisor.EXPECT().getRegionAdvice(region, os).Return(map[string]spotAdvice{
			instance: {Range: 0, Savings: 50},
		}, nil).Once()
		m.advisor.EXPECT().getInstanceType(instance).Return(TypeInfo{
			Cores: 1, RAM: 1.0, EMR: false,
		}, nil).Once()
		m.advisor.EXPECT().getRange(0).Return(Range{
			Label: "<5%", Min: 0, Max: 5,
		}, nil).Once()
		m.pricing.EXPECT().getSpotPrice(instance, region, os).Return(0.0116, nil).Once()
	}
}

func assertStandardMocks(t *testing.T, m *mockProviders) {
	m.advisor.AssertExpectations(t)
	m.pricing.AssertExpectations(t)
}

func TestClient_GetSpotSavings(t *testing.T) { //nolint:maintidx // Complex test table with good helper function extraction
	tests := []struct {
		name           string
		regions        []string
		pattern        string
		instanceOS     string
		cpu            int
		memory         int
		maxPrice       float64
		sortBy         SortBy
		sortDesc       bool
		setupMocks     func(*mockProviders)
		expectedResult []Advice
		expectedError  string
		assertMocks    func(*testing.T, *mockProviders)
	}{
		{
			name:       "successful single region query with t2.micro",
			regions:    []string{"us-east-1"},
			pattern:    "t2.micro",
			instanceOS: "linux",
			cpu:        0,
			memory:     0,
			maxPrice:   0,
			sortBy:     SortByRange,
			sortDesc:   false,
			setupMocks: setupSingleInstanceTest("us-east-1", "t2.micro", "linux"),
			expectedResult: []Advice{
				{
					Region:   "us-east-1",
					Instance: "t2.micro",
					Range:    Range{Label: "<5%", Min: 0, Max: 5},
					Savings:  50,
					Info:     TypeInfo{Cores: 1, RAM: 1.0, EMR: false},
					Price:    0.0116,
				},
			},
			assertMocks: assertStandardMocks,
		},
		{
			name:       "all regions query returns multiple results",
			regions:    []string{"all"},
			pattern:    "t2.micro",
			instanceOS: "linux",
			setupMocks: func(m *mockProviders) {
				m.advisor.EXPECT().getRegions().Return([]string{"us-east-1", "us-west-2"}).Once()

				m.advisor.EXPECT().getRegionAdvice("us-east-1", "linux").Return(map[string]spotAdvice{
					"t2.micro": {Range: 0, Savings: 50},
				}, nil).Once()

				m.advisor.EXPECT().getRegionAdvice("us-west-2", "linux").Return(map[string]spotAdvice{
					"t2.micro": {Range: 1, Savings: 60},
				}, nil).Once()

				m.advisor.EXPECT().getInstanceType("t2.micro").Return(TypeInfo{
					Cores: 1,
					RAM:   1.0,
					EMR:   false,
				}, nil).Times(2)

				m.advisor.EXPECT().getRange(0).Return(Range{Label: "<5%", Min: 0, Max: 5}, nil).Once()
				m.advisor.EXPECT().getRange(1).Return(Range{Label: "5-10%", Min: 5, Max: 10}, nil).Once()

				m.pricing.EXPECT().getSpotPrice("t2.micro", "us-east-1", "linux").Return(0.0116, nil).Once()
				m.pricing.EXPECT().getSpotPrice("t2.micro", "us-west-2", "linux").Return(0.0117, nil).Once()
			},
			expectedResult: []Advice{
				{
					Region:   "us-east-1",
					Instance: "t2.micro",
					Range:    Range{Label: "<5%", Min: 0, Max: 5},
					Savings:  50,
					Info:     TypeInfo{Cores: 1, RAM: 1.0, EMR: false},
					Price:    0.0116,
				},
				{
					Region:   "us-west-2",
					Instance: "t2.micro",
					Range:    Range{Label: "5-10%", Min: 5, Max: 10},
					Savings:  60,
					Info:     TypeInfo{Cores: 1, RAM: 1.0, EMR: false},
					Price:    0.0117,
				},
			},
			assertMocks: assertStandardMocks,
		},
		{
			name:       "cpu filtering excludes instances with insufficient cores",
			regions:    []string{"us-east-1"},
			pattern:    "",
			instanceOS: "linux",
			cpu:        2,
			setupMocks: func(m *mockProviders) {
				m.advisor.EXPECT().getRegionAdvice("us-east-1", "linux").Return(map[string]spotAdvice{
					"t2.micro":  {Range: 0, Savings: 50},
					"t2.small":  {Range: 0, Savings: 40},
					"t2.medium": {Range: 1, Savings: 35},
				}, nil).Once()

				// These instances will be filtered out due to insufficient CPU
				m.advisor.EXPECT().getInstanceType("t2.micro").Return(TypeInfo{Cores: 1, RAM: 1.0}, nil).Once()
				m.advisor.EXPECT().getInstanceType("t2.small").Return(TypeInfo{Cores: 1, RAM: 2.0}, nil).Once()

				// This instance passes the CPU filter
				m.advisor.EXPECT().getInstanceType("t2.medium").Return(TypeInfo{Cores: 2, RAM: 4.0}, nil).Once()
				m.advisor.EXPECT().getRange(1).Return(Range{Label: "5-10%", Min: 5, Max: 10}, nil).Once()
				m.pricing.EXPECT().getSpotPrice("t2.medium", "us-east-1", "linux").Return(0.0464, nil).Once()
			},
			expectedResult: []Advice{
				{
					Region:   "us-east-1",
					Instance: "t2.medium",
					Range:    Range{Label: "5-10%", Min: 5, Max: 10},
					Savings:  35,
					Info:     TypeInfo{Cores: 2, RAM: 4.0},
					Price:    0.0464,
				},
			},
			assertMocks: assertStandardMocks,
		},
		{
			name:       "memory filtering excludes instances with insufficient RAM",
			regions:    []string{"us-east-1"},
			pattern:    "",
			instanceOS: "linux",
			memory:     4,
			setupMocks: func(m *mockProviders) {
				m.advisor.EXPECT().getRegionAdvice("us-east-1", "linux").Return(map[string]spotAdvice{
					"t2.micro":  {Range: 0, Savings: 50},
					"t2.medium": {Range: 1, Savings: 35},
				}, nil).Once()

				// This instance will be filtered out due to insufficient memory
				m.advisor.EXPECT().getInstanceType("t2.micro").Return(TypeInfo{Cores: 1, RAM: 1.0}, nil).Once()

				// This instance passes the memory filter
				m.advisor.EXPECT().getInstanceType("t2.medium").Return(TypeInfo{Cores: 2, RAM: 4.0}, nil).Once()
				m.advisor.EXPECT().getRange(1).Return(Range{Label: "5-10%", Min: 5, Max: 10}, nil).Once()
				m.pricing.EXPECT().getSpotPrice("t2.medium", "us-east-1", "linux").Return(0.0464, nil).Once()
			},
			expectedResult: []Advice{
				{
					Region:   "us-east-1",
					Instance: "t2.medium",
					Range:    Range{Label: "5-10%", Min: 5, Max: 10},
					Savings:  35,
					Info:     TypeInfo{Cores: 2, RAM: 4.0},
					Price:    0.0464,
				},
			},
			assertMocks: assertStandardMocks,
		},
		{
			name:       "price filtering excludes expensive instances",
			regions:    []string{"us-east-1"},
			pattern:    "",
			instanceOS: "linux",
			maxPrice:   0.05,
			setupMocks: func(m *mockProviders) {
				m.advisor.EXPECT().getRegionAdvice("us-east-1", "linux").Return(map[string]spotAdvice{
					"t2.micro": {Range: 0, Savings: 50},
					"t2.large": {Range: 1, Savings: 30},
				}, nil).Once()

				// t2.micro passes price filter (cheap)
				m.advisor.EXPECT().getInstanceType("t2.micro").Return(TypeInfo{Cores: 1, RAM: 1.0}, nil).Once()
				m.advisor.EXPECT().getRange(0).Return(Range{Label: "<5%", Min: 0, Max: 5}, nil).Once()
				m.pricing.EXPECT().getSpotPrice("t2.micro", "us-east-1", "linux").Return(0.0116, nil).Once()

				// t2.large fails price filter (expensive)
				m.advisor.EXPECT().getInstanceType("t2.large").Return(TypeInfo{Cores: 2, RAM: 8.0}, nil).Once()
				m.pricing.EXPECT().getSpotPrice("t2.large", "us-east-1", "linux").Return(0.0928, nil).Once()
			},
			expectedResult: []Advice{
				{
					Region:   "us-east-1",
					Instance: "t2.micro",
					Range:    Range{Label: "<5%", Min: 0, Max: 5},
					Savings:  50,
					Info:     TypeInfo{Cores: 1, RAM: 1.0},
					Price:    0.0116,
				},
			},
			assertMocks: assertStandardMocks,
		},
		{
			name:       "regex pattern filters instances correctly",
			regions:    []string{"us-east-1"},
			pattern:    "t2\\.(micro|small)",
			instanceOS: "linux",
			setupMocks: func(m *mockProviders) {
				m.advisor.EXPECT().getRegionAdvice("us-east-1", "linux").Return(map[string]spotAdvice{
					"t2.micro":  {Range: 0, Savings: 50},
					"t2.small":  {Range: 0, Savings: 40},
					"t2.medium": {Range: 1, Savings: 35}, // Should be filtered out by regex
				}, nil).Once()

				// Only micro and small should match the pattern
				m.advisor.EXPECT().getInstanceType("t2.micro").Return(TypeInfo{Cores: 1, RAM: 1.0}, nil).Once()
				m.advisor.EXPECT().getInstanceType("t2.small").Return(TypeInfo{Cores: 1, RAM: 2.0}, nil).Once()

				m.advisor.EXPECT().getRange(0).Return(Range{Label: "<5%", Min: 0, Max: 5}, nil).Times(2)

				m.pricing.EXPECT().getSpotPrice("t2.micro", "us-east-1", "linux").Return(0.0116, nil).Once()
				m.pricing.EXPECT().getSpotPrice("t2.small", "us-east-1", "linux").Return(0.0232, nil).Once()
			},
			expectedResult: []Advice{
				{
					Region:   "us-east-1",
					Instance: "t2.micro",
					Range:    Range{Label: "<5%", Min: 0, Max: 5},
					Savings:  50,
					Info:     TypeInfo{Cores: 1, RAM: 1.0},
					Price:    0.0116,
				},
				{
					Region:   "us-east-1",
					Instance: "t2.small",
					Range:    Range{Label: "<5%", Min: 0, Max: 5},
					Savings:  40,
					Info:     TypeInfo{Cores: 1, RAM: 2.0},
					Price:    0.0232,
				},
			},
			assertMocks: assertStandardMocks,
		},
		{
			name:       "region not found returns error",
			regions:    []string{"invalid-region"},
			instanceOS: "linux",
			setupMocks: func(m *mockProviders) {
				m.advisor.EXPECT().getRegionAdvice("invalid-region", "linux").Return(
					nil, errors.New("region not found: invalid-region")).Once()
			},
			expectedError: "region not found: invalid-region",
			assertMocks:   assertStandardMocks,
		},
		{
			name:       "invalid regex pattern returns error",
			regions:    []string{"us-east-1"},
			pattern:    "[invalid-regex",
			instanceOS: "linux",
			setupMocks: func(m *mockProviders) {
				m.advisor.EXPECT().getRegionAdvice("us-east-1", "linux").Return(map[string]spotAdvice{
					"t2.micro": {Range: 0, Savings: 50},
				}, nil).Once()
			},
			expectedError: "failed to match instance type",
			assertMocks:   assertStandardMocks,
		},
		{
			name:       "missing instance type data is handled gracefully",
			regions:    []string{"us-east-1"},
			pattern:    "",
			instanceOS: "linux",
			setupMocks: func(m *mockProviders) {
				m.advisor.EXPECT().getRegionAdvice("us-east-1", "linux").Return(map[string]spotAdvice{
					"t2.micro":   {Range: 0, Savings: 50},
					"unknown.xl": {Range: 1, Savings: 40},
				}, nil).Once()

				// t2.micro exists and should be included
				m.advisor.EXPECT().getInstanceType("t2.micro").Return(TypeInfo{Cores: 1, RAM: 1.0}, nil).Once()
				m.advisor.EXPECT().getRange(0).Return(Range{Label: "<5%", Min: 0, Max: 5}, nil).Once()
				m.pricing.EXPECT().getSpotPrice("t2.micro", "us-east-1", "linux").Return(0.0116, nil).Once()

				// unknown.xl doesn't exist and should be skipped
				m.advisor.EXPECT().getInstanceType("unknown.xl").Return(TypeInfo{}, errors.New("instance type not found")).Once()
			},
			expectedResult: []Advice{
				{
					Region:   "us-east-1",
					Instance: "t2.micro",
					Range:    Range{Label: "<5%", Min: 0, Max: 5},
					Savings:  50,
					Info:     TypeInfo{Cores: 1, RAM: 1.0},
					Price:    0.0116,
				},
			},
			assertMocks: assertStandardMocks,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fresh mocks for each test case
			mocks := &mockProviders{
				advisor: newMockadvisorProvider(t),
				pricing: newMockpricingProvider(t),
			}

			// Setup mock expectations for this test case
			if tt.setupMocks != nil {
				tt.setupMocks(mocks)
			}

			// Create client with mocks
			client := NewWithProviders(mocks.advisor, mocks.pricing)

			// Execute the method under test
			result, err := client.GetSpotSavings(
				context.Background(),
				tt.regions,
				tt.pattern,
				tt.instanceOS,
				tt.cpu,
				tt.memory,
				tt.maxPrice,
				tt.sortBy,
				tt.sortDesc,
			)

			// Assert results
			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
			}

			// Assert mock expectations were met
			if tt.assertMocks != nil {
				tt.assertMocks(t, mocks)
			}
		})
	}
}

func TestClient_GetSpotSavings_SortingBehavior(t *testing.T) {
	tests := []struct {
		name     string
		sortBy   SortBy
		sortDesc bool
		validate func(t *testing.T, advices []Advice)
	}{
		{
			name:     "sort by savings ascending",
			sortBy:   SortBySavings,
			sortDesc: false,
			validate: func(t *testing.T, advices []Advice) {
				require.Len(t, advices, 3, "Should have exactly 3 results")
				// Verify ascending order: 30, 40, 50
				assert.Equal(t, 30, advices[0].Savings)
				assert.Equal(t, 40, advices[1].Savings)
				assert.Equal(t, 50, advices[2].Savings)
			},
		},
		{
			name:     "sort by savings descending",
			sortBy:   SortBySavings,
			sortDesc: true,
			validate: func(t *testing.T, advices []Advice) {
				require.Len(t, advices, 3, "Should have exactly 3 results")
				// Verify descending order: 50, 40, 30
				assert.Equal(t, 50, advices[0].Savings)
				assert.Equal(t, 40, advices[1].Savings)
				assert.Equal(t, 30, advices[2].Savings)
			},
		},
		{
			name:     "sort by instance type ascending",
			sortBy:   SortByInstance,
			sortDesc: false,
			validate: func(t *testing.T, advices []Advice) {
				require.Len(t, advices, 3, "Should have exactly 3 results")
				// Verify alphabetical order: t2.large, t2.medium, t2.micro
				assert.Equal(t, "t2.large", advices[0].Instance)
				assert.Equal(t, "t2.medium", advices[1].Instance)
				assert.Equal(t, "t2.micro", advices[2].Instance)
			},
		},
		{
			name:     "sort by price ascending",
			sortBy:   SortByPrice,
			sortDesc: false,
			validate: func(t *testing.T, advices []Advice) {
				require.Len(t, advices, 3, "Should have exactly 3 results")
				// Verify price order: 0.0116, 0.0464, 0.0928
				assert.Equal(t, 0.0116, advices[0].Price)
				assert.Equal(t, 0.0464, advices[1].Price)
				assert.Equal(t, 0.0928, advices[2].Price)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mocks
			mocks := &mockProviders{
				advisor: newMockadvisorProvider(t),
				pricing: newMockpricingProvider(t),
			}

			// Setup consistent mock data with multiple instances for sorting
			mocks.advisor.EXPECT().getRegionAdvice("us-east-1", "linux").Return(map[string]spotAdvice{
				"t2.large":  {Range: 1, Savings: 30},
				"t2.micro":  {Range: 0, Savings: 50},
				"t2.medium": {Range: 2, Savings: 40},
			}, nil).Once()

			// Mock instance types
			mocks.advisor.EXPECT().getInstanceType("t2.large").Return(TypeInfo{Cores: 2, RAM: 8.0}, nil).Once()
			mocks.advisor.EXPECT().getInstanceType("t2.micro").Return(TypeInfo{Cores: 1, RAM: 1.0}, nil).Once()
			mocks.advisor.EXPECT().getInstanceType("t2.medium").Return(TypeInfo{Cores: 2, RAM: 4.0}, nil).Once()

			// Mock ranges
			mocks.advisor.EXPECT().getRange(0).Return(Range{Label: "<5%", Min: 0, Max: 5}, nil).Once()
			mocks.advisor.EXPECT().getRange(1).Return(Range{Label: "5-10%", Min: 5, Max: 10}, nil).Once()
			mocks.advisor.EXPECT().getRange(2).Return(Range{Label: "10-15%", Min: 10, Max: 15}, nil).Once()

			// Mock pricing
			mocks.pricing.EXPECT().getSpotPrice("t2.large", "us-east-1", "linux").Return(0.0928, nil).Once()
			mocks.pricing.EXPECT().getSpotPrice("t2.micro", "us-east-1", "linux").Return(0.0116, nil).Once()
			mocks.pricing.EXPECT().getSpotPrice("t2.medium", "us-east-1", "linux").Return(0.0464, nil).Once()

			// Create client and execute
			client := NewWithProviders(mocks.advisor, mocks.pricing)
			result, err := client.GetSpotSavings(
				context.Background(),
				[]string{"us-east-1"},
				"",
				"linux",
				0, 0, 0,
				tt.sortBy,
				tt.sortDesc,
			)

			// Assert no error
			require.NoError(t, err)

			// Validate sorting behavior
			tt.validate(t, result)

			// Assert mock expectations
			mocks.advisor.AssertExpectations(t)
			mocks.pricing.AssertExpectations(t)
		})
	}
}
