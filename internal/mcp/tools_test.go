package mcp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"spotinfo/internal/spot"
)

func TestParseParameters(t *testing.T) {
	tests := []struct {
		name     string
		args     interface{}
		expected *params
	}{
		{
			name: "valid parameters",
			args: map[string]interface{}{
				"regions":               []interface{}{"us-east-1", "eu-west-1"},
				"instance_types":        "m5.large",
				"min_vcpu":              4,
				"min_memory_gb":         8,
				"max_price_per_hour":    0.5,
				"max_interruption_rate": 20.0,
				"sort_by":               "price",
				"limit":                 5,
			},
			expected: &params{
				regions:         []string{"us-east-1", "eu-west-1"},
				instanceTypes:   "m5.large",
				minVCPU:         4,
				minMemoryGB:     8,
				maxPrice:        0.5,
				maxInterruption: 20.0,
				sortBy:          "price",
				limit:           5,
			},
		},
		{
			name: "empty parameters use defaults",
			args: map[string]interface{}{},
			expected: &params{
				regions:         []string{"all"},
				instanceTypes:   "",
				minVCPU:         0,
				minMemoryGB:     0,
				maxPrice:        0,
				maxInterruption: 0,
				sortBy:          "reliability",
				limit:           defaultLimit,
			},
		},
		{
			name: "all regions special case",
			args: map[string]interface{}{
				"regions": []interface{}{"all"},
			},
			expected: &params{
				regions:         []string{"all"},
				instanceTypes:   "",
				minVCPU:         0,
				minMemoryGB:     0,
				maxPrice:        0,
				maxInterruption: 0,
				sortBy:          "reliability",
				limit:           defaultLimit,
			},
		},
		{
			name: "limit exceeds maximum",
			args: map[string]interface{}{
				"limit": 100,
			},
			expected: &params{
				regions:         []string{"all"},
				instanceTypes:   "",
				minVCPU:         0,
				minMemoryGB:     0,
				maxPrice:        0,
				maxInterruption: 0,
				sortBy:          "reliability",
				limit:           maxLimit,
			},
		},
		{
			name: "invalid arguments type",
			args: "invalid",
			expected: &params{
				regions:         []string{"all"},
				instanceTypes:   "",
				minVCPU:         0,
				minMemoryGB:     0,
				maxPrice:        0,
				maxInterruption: 0,
				sortBy:          "reliability",
				limit:           defaultLimit,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseParameters(tt.args)

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertSortParams(t *testing.T) {
	tests := []struct {
		name         string
		sortBy       string
		expectedSort spot.SortBy
		expectedDesc bool
	}{
		{"price", "price", spot.SortByPrice, false},
		{"reliability", "reliability", spot.SortByRange, false},
		{"savings", "savings", spot.SortBySavings, true},
		{"default", "unknown", spot.SortByRange, false},
		{"empty", "", spot.SortByRange, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sortBy, sortDesc := convertSortParams(tt.sortBy)
			assert.Equal(t, tt.expectedSort, sortBy)
			assert.Equal(t, tt.expectedDesc, sortDesc)
		})
	}
}

func TestFilterByInterruption(t *testing.T) {
	testAdvices := []spot.Advice{
		{Range: spot.Range{Min: 10, Max: 20}}, // avg = 15
		{Range: spot.Range{Min: 30, Max: 40}}, // avg = 35
		{Range: spot.Range{Min: 5, Max: 15}},  // avg = 10
	}

	tests := []struct {
		name            string
		advices         []spot.Advice
		maxInterruption float64
		expectedCount   int
	}{
		{
			name:            "filter by 25 - should keep 2",
			advices:         testAdvices,
			maxInterruption: 25,
			expectedCount:   2,
		},
		{
			name:            "filter by 12 - should keep 1",
			advices:         testAdvices,
			maxInterruption: 12,
			expectedCount:   1,
		},
		{
			name:            "no filter (0) - should keep all",
			advices:         testAdvices,
			maxInterruption: 0,
			expectedCount:   3,
		},
		{
			name:            "no filter (>=100) - should keep all",
			advices:         testAdvices,
			maxInterruption: 100,
			expectedCount:   3,
		},
		{
			name:            "empty slice",
			advices:         []spot.Advice{},
			maxInterruption: 25,
			expectedCount:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterByInterruption(tt.advices, tt.maxInterruption)
			assert.Len(t, result, tt.expectedCount)
		})
	}
}

func TestApplyLimit(t *testing.T) {
	testAdvices := make([]spot.Advice, 20)
	for i := range testAdvices {
		testAdvices[i] = spot.Advice{Instance: "test"}
	}

	tests := []struct {
		name          string
		advices       []spot.Advice
		limit         int
		expectedCount int
	}{
		{"limit less than slice length", testAdvices, 5, 5},
		{"limit equal to slice length", testAdvices, 20, 20},
		{"limit greater than slice length", testAdvices, 30, 20},
		{"empty slice", []spot.Advice{}, 5, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyLimit(tt.advices, tt.limit)
			assert.Len(t, result, tt.expectedCount)
		})
	}
}

func TestCalculateAvgInterruption(t *testing.T) {
	tests := []struct {
		name     string
		r        spot.Range
		expected float64
	}{
		{"normal range", spot.Range{Min: 10, Max: 20}, 15.0},
		{"zero range", spot.Range{Min: 0, Max: 0}, 0.0},
		{"single value", spot.Range{Min: 5, Max: 5}, 5.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateAvgInterruption(tt.r)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCalculateReliabilityScore(t *testing.T) {
	tests := []struct {
		name            string
		avgInterruption float64
		expected        int
	}{
		{"low interruption", 10.0, 90},
		{"high interruption", 80.0, 20},
		{"zero interruption", 0.0, 100},
		{"above max", 110.0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateReliabilityScore(tt.avgInterruption)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildResponse(t *testing.T) {
	startTime := time.Now()
	testAdvices := []spot.Advice{
		{
			Instance: "m5.large",
			Region:   "us-east-1",
			Price:    0.0928,
			Savings:  70,
			Range:    spot.Range{Min: 5, Max: 10, Label: "<5%"},
			Info:     spot.TypeInfo{Cores: 2, RAM: 8.0},
		},
		{
			Instance: "t3.medium",
			Region:   "eu-west-1",
			Price:    0.0416,
			Savings:  65,
			Range:    spot.Range{Min: 10, Max: 15, Label: "5-10%"},
			Info:     spot.TypeInfo{Cores: 2, RAM: 4.0},
		},
	}

	response := buildResponse(testAdvices, startTime)

	// Check response structure
	assert.Contains(t, response, "results")
	assert.Contains(t, response, "metadata")

	// Check results
	results, ok := response["results"].([]map[string]interface{})
	assert.True(t, ok, "results should be a slice of maps")
	assert.Len(t, results, 2)

	// Check first result
	firstResult := results[0]
	assert.Equal(t, "m5.large", firstResult["instance_type"])
	assert.Equal(t, "us-east-1", firstResult["region"])
	assert.Equal(t, 0.0928, firstResult["spot_price_per_hour"])
	assert.Equal(t, 70, firstResult["savings_percentage"])
	assert.Equal(t, 7.5, firstResult["interruption_rate"]) // (5+10)/2
	assert.Equal(t, 92, firstResult["reliability_score"])  // 100-7.5

	// Check metadata
	metadata, ok := response["metadata"].(map[string]interface{})
	assert.True(t, ok, "metadata should be a map")
	assert.Equal(t, 2, metadata["total_results"])
	assert.Equal(t, "embedded", metadata["data_source"])
	regionsSearched, ok := metadata["regions_searched"].([]string)
	assert.True(t, ok, "regions_searched should be a string slice")
	assert.Contains(t, regionsSearched, "us-east-1")
	assert.Contains(t, regionsSearched, "eu-west-1")
}

func TestGetStringWithDefault(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]interface{}
		key      string
		def      string
		expected string
	}{
		{"existing key", map[string]interface{}{"test": "value"}, "test", "default", "value"},
		{"missing key", map[string]interface{}{}, "test", "default", "default"},
		{"empty value", map[string]interface{}{"test": ""}, "test", "default", "default"},
		{"nil value", map[string]interface{}{"test": nil}, "test", "default", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getStringWithDefault(tt.args, tt.key, tt.def)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetLimitWithDefault(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]interface{}
		key      string
		def      int
		expected int
	}{
		{"valid limit", map[string]interface{}{"limit": 15}, "limit", 10, 15},
		{"zero limit uses default", map[string]interface{}{"limit": 0}, "limit", 10, 10},
		{"negative limit uses default", map[string]interface{}{"limit": -5}, "limit", 10, 10},
		{"over max uses max", map[string]interface{}{"limit": 100}, "limit", 10, maxLimit},
		{"missing key uses default", map[string]interface{}{}, "limit", 10, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getLimitWithDefault(tt.args, tt.key, tt.def)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetStringSliceWithDefault(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]interface{}
		key      string
		def      []string
		expected []string
	}{
		{
			"valid slice",
			map[string]interface{}{"regions": []interface{}{"us-east-1", "eu-west-1"}},
			"regions",
			[]string{"default"},
			[]string{"us-east-1", "eu-west-1"},
		},
		{
			"empty slice uses default",
			map[string]interface{}{"regions": []interface{}{}},
			"regions",
			[]string{"default"},
			[]string{"default"},
		},
		{
			"missing key uses default",
			map[string]interface{}{},
			"regions",
			[]string{"default"},
			[]string{"default"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getStringSliceWithDefault(tt.args, tt.key, tt.def)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMarshalResponse(t *testing.T) {
	tests := []struct {
		name      string
		response  interface{}
		expectErr bool
	}{
		{
			name:      "valid response",
			response:  map[string]interface{}{"test": "value"},
			expectErr: false,
		},
		{
			name:      "nil response",
			response:  nil,
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := marshalResponse(tt.response)

			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.False(t, result.IsError)
			}
		})
	}
}
