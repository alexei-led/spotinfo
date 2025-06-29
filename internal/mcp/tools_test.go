package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

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

//nolint:maintidx // Complex table-driven test with multiple scenarios
func TestFindSpotInstancesTool_Handle(t *testing.T) {
	tests := []struct {
		name           string
		arguments      interface{}
		mockSetup      func(*mockspotClient)
		validateResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name: "successful request with complete response validation",
			arguments: map[string]interface{}{
				"regions":        []interface{}{"us-east-1"},
				"instance_types": "t2.micro",
				"limit":          2,
				"sort_by":        "price",
			},
			mockSetup: func(m *mockspotClient) {
				advices := []spot.Advice{
					{
						Instance: "t2.micro",
						Region:   "us-east-1",
						Price:    0.0116,
						Savings:  50,
						Range:    spot.Range{Min: 0, Max: 5, Label: "<5%"},
						Info:     spot.TypeInfo{Cores: 1, RAM: 1.0},
					},
				}
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					[]string{"us-east-1"},
					"t2.micro",
					"linux",
					0, 0, float64(0),
					spot.SortByPrice,
					false,
				).Return(advices, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				require.False(t, result.IsError)
				require.Len(t, result.Content, 1)

				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok, "Content should be TextContent")
				assert.Equal(t, "text", textContent.Type)

				// Validate JSON structure contains expected fields
				var response map[string]interface{}
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				assert.Contains(t, response, "results")
				assert.Contains(t, response, "metadata")

				results, ok := response["results"].([]interface{})
				require.True(t, ok)
				assert.Len(t, results, 1)

				metadata, ok := response["metadata"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(1), metadata["total_results"])
			},
		},
		{
			name: "parameter validation with multiple filters",
			arguments: map[string]interface{}{
				"regions":               []interface{}{"us-east-1", "eu-west-1"},
				"instance_types":        "m5.*",
				"min_vcpu":              4,
				"min_memory_gb":         16,
				"max_price_per_hour":    0.5,
				"max_interruption_rate": 10.0,
				"sort_by":               "savings",
				"limit":                 5,
			},
			mockSetup: func(m *mockspotClient) {
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					[]string{"us-east-1", "eu-west-1"},
					"m5.*",
					"linux",
					4, 16, 0.5,
					spot.SortBySavings,
					true, // savings sort is descending
				).Return([]spot.Advice{}, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.False(t, result.IsError)
				assert.NotNil(t, result.Content)
			},
		},
		{
			name:      "client error handling",
			arguments: map[string]interface{}{"regions": []interface{}{"invalid-region"}},
			mockSetup: func(m *mockspotClient) {
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					[]string{"invalid-region"},
					"",
					"linux",
					0, 0, float64(0),
					spot.SortByRange,
					false,
				).Return(nil, errors.New("region not found: invalid-region")).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.True(t, result.IsError, "Should be an error result")
				require.Len(t, result.Content, 1)

				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				assert.Contains(t, textContent.Text, "Failed to get spot recommendations")
				assert.Contains(t, textContent.Text, "region not found")
			},
		},
		{
			name:      "default parameters behavior",
			arguments: map[string]interface{}{},
			mockSetup: func(m *mockspotClient) {
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					[]string{"all"},
					"", // empty instance types
					"linux",
					0, 0, float64(0), // no filters
					spot.SortByRange, // default sort
					false,
				).Return([]spot.Advice{}, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.False(t, result.IsError)

				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)

				var response map[string]interface{}
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				metadata, ok := response["metadata"].(map[string]interface{})
				require.True(t, ok, "metadata should be a map")
				assert.Equal(t, float64(0), metadata["total_results"])
			},
		},
		{
			name: "interruption filtering works correctly",
			arguments: map[string]interface{}{
				"regions":               []interface{}{"us-east-1"},
				"max_interruption_rate": 7.5, // Should filter out avg > 7.5
			},
			mockSetup: func(m *mockspotClient) {
				advices := []spot.Advice{
					{
						Instance: "t2.micro",
						Region:   "us-east-1",
						Range:    spot.Range{Min: 0, Max: 5}, // avg = 2.5, should pass
						Savings:  50,
						Price:    0.01,
					},
					{
						Instance: "t2.small",
						Region:   "us-east-1",
						Range:    spot.Range{Min: 10, Max: 20}, // avg = 15, should be filtered
						Savings:  40,
						Price:    0.02,
					},
				}
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
				).Return(advices, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.False(t, result.IsError)

				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)

				var response map[string]interface{}
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				results, ok := response["results"].([]interface{})
				require.True(t, ok, "results should be a slice")
				assert.Len(t, results, 1, "Should filter out high interruption instances")

				firstResult, ok := results[0].(map[string]interface{})
				require.True(t, ok, "first result should be a map")
				assert.Equal(t, "t2.micro", firstResult["instance_type"])
			},
		},
		{
			name: "limit parameter works correctly",
			arguments: map[string]interface{}{
				"regions": []interface{}{"us-east-1"},
				"limit":   2,
			},
			mockSetup: func(m *mockspotClient) {
				advices := []spot.Advice{
					{Instance: "t2.micro", Region: "us-east-1", Savings: 50},
					{Instance: "t2.small", Region: "us-east-1", Savings: 40},
					{Instance: "t2.medium", Region: "us-east-1", Savings: 30},
				}
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
				).Return(advices, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.False(t, result.IsError)

				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)

				var response map[string]interface{}
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				results, ok := response["results"].([]interface{})
				require.True(t, ok, "results should be a slice")
				assert.Len(t, results, 2, "Should limit results to 2")

				metadata, ok := response["metadata"].(map[string]interface{})
				require.True(t, ok, "metadata should be a map")
				assert.Equal(t, float64(2), metadata["total_results"])
			},
		},
		{
			name:      "invalid argument types handled gracefully",
			arguments: "not a map", // Invalid argument type
			mockSetup: func(m *mockspotClient) {
				// Should use defaults when arguments are invalid
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					[]string{"all"},
					"",
					"linux",
					0, 0, float64(0),
					spot.SortByRange,
					false,
				).Return([]spot.Advice{}, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.False(t, result.IsError, "Should handle invalid arguments gracefully")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := newMockspotClient(t)
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			tool := NewFindSpotInstancesTool(mockClient, logger)

			if tt.mockSetup != nil {
				tt.mockSetup(mockClient)
			}

			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: tt.arguments,
				},
			}

			result, err := tool.Handle(context.Background(), req)

			require.NoError(t, err)
			require.NotNil(t, result)
			tt.validateResult(t, result)
			mockClient.AssertExpectations(t)
		})
	}
}

func TestListSpotRegionsTool_Handle(t *testing.T) {
	tests := []struct {
		name           string
		arguments      interface{}
		mockSetup      func(*mockspotClient)
		validateResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name:      "successful regions list with deduplication",
			arguments: map[string]interface{}{},
			mockSetup: func(m *mockspotClient) {
				advices := []spot.Advice{
					{Region: "us-east-1", Instance: "t2.micro"},
					{Region: "us-west-2", Instance: "t2.small"},
					{Region: "us-east-1", Instance: "t2.medium"}, // duplicate region
					{Region: "eu-west-1", Instance: "t2.large"},
					{Region: "us-west-2", Instance: "t2.xlarge"}, // another duplicate
				}
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					[]string{"all"},
					"",
					"linux",
					0, 0, float64(0),
					spot.SortByRegion,
					false,
				).Return(advices, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				require.False(t, result.IsError)
				require.Len(t, result.Content, 1)

				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)

				var response map[string]interface{}
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				assert.Contains(t, response, "regions")
				assert.Contains(t, response, "total")

				regions, ok := response["regions"].([]interface{})
				require.True(t, ok)
				assert.Len(t, regions, 3, "Should deduplicate regions")

				// Convert to string slice for easier testing
				regionStrs := make([]string, len(regions))
				for i, r := range regions {
					if str, ok := r.(string); ok {
						regionStrs[i] = str
					}
				}

				assert.Contains(t, regionStrs, "us-east-1")
				assert.Contains(t, regionStrs, "us-west-2")
				assert.Contains(t, regionStrs, "eu-west-1")

				assert.Equal(t, float64(3), response["total"])
			},
		},
		{
			name:      "empty results handled correctly",
			arguments: map[string]interface{}{},
			mockSetup: func(m *mockspotClient) {
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					[]string{"all"},
					"",
					"linux",
					0, 0, float64(0),
					spot.SortByRegion,
					false,
				).Return([]spot.Advice{}, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.False(t, result.IsError)

				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)

				var response map[string]interface{}
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				regions, ok := response["regions"].([]interface{})
				require.True(t, ok, "regions should be a slice")
				assert.Empty(t, regions)
				assert.Equal(t, float64(0), response["total"])
			},
		},
		{
			name:      "client error handling with proper error message",
			arguments: map[string]interface{}{},
			mockSetup: func(m *mockspotClient) {
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					[]string{"all"},
					"",
					"linux",
					0, 0, float64(0),
					spot.SortByRegion,
					false,
				).Return(nil, errors.New("network timeout while fetching data")).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.True(t, result.IsError, "Should be an error result")
				require.Len(t, result.Content, 1)

				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				assert.Contains(t, textContent.Text, "Failed to retrieve regions")
				assert.Contains(t, textContent.Text, "network timeout")
			},
		},
		{
			name:      "single region returned correctly",
			arguments: map[string]interface{}{},
			mockSetup: func(m *mockspotClient) {
				advices := []spot.Advice{
					{Region: "ap-south-1", Instance: "t3.micro"},
				}
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					[]string{"all"},
					"",
					"linux",
					0, 0, float64(0),
					spot.SortByRegion,
					false,
				).Return(advices, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.False(t, result.IsError)

				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)

				var response map[string]interface{}
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				regions, ok := response["regions"].([]interface{})
				require.True(t, ok, "regions should be a slice")
				assert.Len(t, regions, 1)
				assert.Equal(t, "ap-south-1", regions[0])
				assert.Equal(t, float64(1), response["total"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := newMockspotClient(t)
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			tool := NewListSpotRegionsTool(mockClient, logger)

			if tt.mockSetup != nil {
				tt.mockSetup(mockClient)
			}

			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: tt.arguments,
				},
			}

			result, err := tool.Handle(context.Background(), req)

			require.NoError(t, err)
			require.NotNil(t, result)
			tt.validateResult(t, result)
			mockClient.AssertExpectations(t)
		})
	}
}

func TestCreateErrorResult(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{"simple error", "test error message"},
		{"empty message", ""},
		{"complex error", "Failed to process request: invalid parameter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := createErrorResult(tt.message)

			assert.True(t, result.IsError, "createErrorResult should create error results")
			assert.Len(t, result.Content, 1)

			textContent, ok := result.Content[0].(mcp.TextContent)
			require.True(t, ok)
			assert.Equal(t, "text", textContent.Type)
			assert.Contains(t, textContent.Text, tt.message)
		})
	}
}

func TestMarshalResponse_ErrorCases(t *testing.T) {
	tests := []struct {
		name     string
		response interface{}
		expected bool
	}{
		{
			name: "unmarshalable response with channel",
			response: map[string]interface{}{
				"invalid": make(chan int),
			},
			expected: true,
		},
		{
			name:     "function in response",
			response: map[string]interface{}{"fn": func() {}},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := marshalResponse(tt.response)

			if tt.expected {
				// marshalResponse returns error result with IsError=true, not a nil result
				assert.NoError(t, err, "marshalResponse should always return successful response")
				assert.NotNil(t, result)
				assert.True(t, result.IsError, "Should be an error result for unmarshalable data")
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.False(t, result.IsError)
			}
		})
	}
}

func TestFetchRegions(t *testing.T) {
	tests := []struct {
		name          string
		mockSetup     func(*mockspotClient)
		expectedError bool
		expectedCount int
	}{
		{
			name: "successful fetch with multiple regions",
			mockSetup: func(m *mockspotClient) {
				advices := []spot.Advice{
					{Region: "us-east-1"},
					{Region: "us-west-2"},
					{Region: "us-east-1"}, // duplicate should be handled
					{Region: "eu-west-1"},
				}
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
				).Return(advices, nil).Once()
			},
			expectedCount: 3,
		},
		{
			name: "client error",
			mockSetup: func(m *mockspotClient) {
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
				).Return(nil, errors.New("network error")).Once()
			},
			expectedError: true,
		},
		{
			name: "empty results",
			mockSetup: func(m *mockspotClient) {
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
					mock.Anything,
				).Return([]spot.Advice{}, nil).Once()
			},
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := newMockspotClient(t)
			if tt.mockSetup != nil {
				tt.mockSetup(mockClient)
			}

			tool := &ListSpotRegionsTool{client: mockClient, logger: slog.Default()}
			regions, err := tool.fetchRegions(context.Background())

			if tt.expectedError {
				assert.Error(t, err)
				assert.Nil(t, regions)
			} else {
				assert.NoError(t, err)
				assert.Len(t, regions, tt.expectedCount)
			}

			mockClient.AssertExpectations(t)
		})
	}
}
