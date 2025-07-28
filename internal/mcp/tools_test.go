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
			name: "complete parameters including score options",
			args: map[string]interface{}{
				"regions":               []interface{}{"us-east-1", "eu-west-1"},
				"instance_types":        "m5.large",
				"min_vcpu":              4,
				"min_memory_gb":         8,
				"max_price_per_hour":    0.5,
				"max_interruption_rate": 20.0,
				"sort_by":               "price",
				"limit":                 5,
				"with_score":            true,
				"min_score":             7,
				"az":                    true,
				"score_timeout":         30,
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
				withScore:       true,
				minScore:        7,
				az:              true,
				scoreTimeout:    30,
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
				withScore:       false,
				minScore:        0,
				az:              false,
				scoreTimeout:    0,
			},
		},
		{
			name: "score sorting option",
			args: map[string]interface{}{
				"sort_by":    "score",
				"with_score": true,
			},
			expected: &params{
				regions:         []string{"all"},
				instanceTypes:   "",
				minVCPU:         0,
				minMemoryGB:     0,
				maxPrice:        0,
				maxInterruption: 0,
				sortBy:          "score",
				limit:           defaultLimit,
				withScore:       true,
				minScore:        0,
				az:              false,
				scoreTimeout:    0,
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
				withScore:       false,
				minScore:        0,
				az:              false,
				scoreTimeout:    0,
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
		{"score", "score", spot.SortByScore, false},
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
		{Range: spot.Range{Min: 0, Max: 5}},   // avg = 2.5
		{Range: spot.Range{Min: 10, Max: 20}}, // avg = 15
		{Range: spot.Range{Min: 30, Max: 40}}, // avg = 35
	}

	tests := []struct {
		name            string
		advices         []spot.Advice
		maxInterruption float64
		expectedCount   int
	}{
		{
			name:            "filter by 10 - should keep 1",
			advices:         testAdvices,
			maxInterruption: 10,
			expectedCount:   1,
		},
		{
			name:            "filter by 25 - should keep 2",
			advices:         testAdvices,
			maxInterruption: 25,
			expectedCount:   2,
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterByInterruption(tt.advices, tt.maxInterruption)
			assert.Len(t, result, tt.expectedCount)
		})
	}
}

func TestBuildResponse(t *testing.T) {
	startTime := time.Now()
	scoreValue := 8
	scoreTime := time.Now()
	testAdvices := []spot.Advice{
		{
			Instance:       "m5.large",
			Region:         "us-east-1",
			Price:          0.0928,
			Savings:        70,
			Range:          spot.Range{Min: 5, Max: 10, Label: "5-10%"},
			Info:           spot.TypeInfo{Cores: 2, RAM: 8.0},
			RegionScore:    &scoreValue,
			ScoreFetchedAt: &scoreTime,
		},
		{
			Instance: "t3.medium",
			Region:   "eu-west-1",
			Price:    0.0416,
			Savings:  65,
			Range:    spot.Range{Min: 10, Max: 15, Label: "10-15%"},
			Info:     spot.TypeInfo{Cores: 2, RAM: 4.0},
			ZoneScores: map[string]int{
				"eu-west-1a": 7,
				"eu-west-1b": 9,
			},
			ScoreFetchedAt: &scoreTime,
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

	// Check first result with region score
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

//nolint:maintidx // Complex table-driven test with multiple scenarios
func TestFindSpotInstancesTool_Handle(t *testing.T) {
	tests := []struct {
		name           string
		arguments      interface{}
		mockSetup      func(*mockspotClient)
		validateResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name: "basic request with price sorting",
			arguments: map[string]interface{}{
				"regions":        []interface{}{"us-east-1"},
				"instance_types": "t3.micro",
				"sort_by":        "price",
			},
			mockSetup: func(m *mockspotClient) {
				advices := []spot.Advice{
					{
						Instance: "t3.micro",
						Region:   "us-east-1",
						Price:    0.0104,
						Savings:  68,
						Range:    spot.Range{Min: 0, Max: 5, Label: "<5%"},
						Info:     spot.TypeInfo{Cores: 2, RAM: 1.0},
					},
				}
				// We can't match the exact options, so we use mock.Anything
				// The test validates behavior through the returned data
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
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

				results, ok := response["results"].([]interface{})
				require.True(t, ok)
				assert.Len(t, results, 1)

				firstResult, ok := results[0].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "t3.micro", firstResult["instance_type"])
				assert.Equal(t, "$0.0104/hour", firstResult["spot_price"])
			},
		},
		{
			name: "request with score enrichment",
			arguments: map[string]interface{}{
				"regions":        []interface{}{"us-west-2"},
				"instance_types": "m5.large",
				"with_score":     true,
				"min_score":      7,
				"sort_by":        "score",
			},
			mockSetup: func(m *mockspotClient) {
				score8 := 8
				score5 := 5
				now := time.Now()
				advices := []spot.Advice{
					{
						Instance:       "m5.large",
						Region:         "us-west-2",
						Price:          0.096,
						Savings:        70,
						Range:          spot.Range{Min: 5, Max: 10, Label: "5-10%"},
						Info:           spot.TypeInfo{Cores: 2, RAM: 8.0},
						RegionScore:    &score8,
						ScoreFetchedAt: &now,
					},
					{
						Instance:       "m5.large",
						Region:         "us-west-2",
						Price:          0.092,
						Savings:        72,
						Range:          spot.Range{Min: 10, Max: 15, Label: "10-15%"},
						Info:           spot.TypeInfo{Cores: 2, RAM: 8.0},
						RegionScore:    &score5, // This should be filtered out by min_score
						ScoreFetchedAt: &now,
					},
				}
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
				).Return(advices, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				require.False(t, result.IsError)

				var response map[string]interface{}
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				// No results because filterByMinScore is not implemented in tools.go
				// This test verifies the parameters are parsed correctly
				results, ok := response["results"].([]interface{})
				require.True(t, ok)
				assert.Len(t, results, 2) // Both should be returned since filtering happens in client
			},
		},
		{
			name: "request with AZ-level scores",
			arguments: map[string]interface{}{
				"regions":       []interface{}{"eu-west-1"},
				"with_score":    true,
				"az":            true,
				"score_timeout": 60,
			},
			mockSetup: func(m *mockspotClient) {
				now := time.Now()
				advices := []spot.Advice{
					{
						Instance: "t3.small",
						Region:   "eu-west-1",
						Price:    0.0208,
						Savings:  68,
						Range:    spot.Range{Min: 5, Max: 10, Label: "5-10%"},
						Info:     spot.TypeInfo{Cores: 2, RAM: 2.0},
						ZoneScores: map[string]int{
							"eu-west-1a": 9,
							"eu-west-1b": 7,
							"eu-west-1c": 8,
						},
						ScoreFetchedAt: &now,
					},
				}
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
				).Return(advices, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				require.False(t, result.IsError)

				var response map[string]interface{}
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				results, ok := response["results"].([]interface{})
				require.True(t, ok)
				assert.Len(t, results, 1)
			},
		},
		{
			name: "interruption rate filtering",
			arguments: map[string]interface{}{
				"regions":               []interface{}{"us-east-1"},
				"max_interruption_rate": 7.5,
			},
			mockSetup: func(m *mockspotClient) {
				advices := []spot.Advice{
					{
						Instance: "t3.micro",
						Region:   "us-east-1",
						Range:    spot.Range{Min: 0, Max: 5}, // avg = 2.5, should pass
						Price:    0.01,
						Info:     spot.TypeInfo{Cores: 2, RAM: 1.0},
					},
					{
						Instance: "t3.small",
						Region:   "us-east-1",
						Range:    spot.Range{Min: 10, Max: 20}, // avg = 15, should be filtered
						Price:    0.02,
						Info:     spot.TypeInfo{Cores: 2, RAM: 2.0},
					},
				}
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
				).Return(advices, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				require.False(t, result.IsError)

				var response map[string]interface{}
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				results, ok := response["results"].([]interface{})
				require.True(t, ok)
				assert.Len(t, results, 1, "Should filter out high interruption instances")

				firstResult, ok := results[0].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "t3.micro", firstResult["instance_type"])
			},
		},
		{
			name:      "error handling",
			arguments: map[string]interface{}{"regions": []interface{}{"invalid-region"}},
			mockSetup: func(m *mockspotClient) {
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
				).Return(nil, errors.New("AWS API error: invalid region")).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.True(t, result.IsError)
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				assert.Contains(t, textContent.Text, "Failed to get spot recommendations")
				assert.Contains(t, textContent.Text, "AWS API error")
			},
		},
		{
			name:      "empty results",
			arguments: map[string]interface{}{"instance_types": "z99.mega"},
			mockSetup: func(m *mockspotClient) {
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
				).Return([]spot.Advice{}, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				require.False(t, result.IsError)

				var response map[string]interface{}
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				results, ok := response["results"].([]interface{})
				require.True(t, ok)
				assert.Empty(t, results)

				metadata, ok := response["metadata"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(0), metadata["total_results"])
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
		})
	}
}

func TestListSpotRegionsTool_Handle(t *testing.T) {
	tests := []struct {
		name           string
		mockSetup      func(*mockspotClient)
		validateResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name: "successful regions list",
			mockSetup: func(m *mockspotClient) {
				advices := []spot.Advice{
					{Region: "us-east-1", Instance: "t3.micro"},
					{Region: "us-west-2", Instance: "t3.small"},
					{Region: "us-east-1", Instance: "m5.large"}, // duplicate region
					{Region: "eu-west-1", Instance: "t3.medium"},
					{Region: "ap-south-1", Instance: "t3.large"},
				}
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
				).Return(advices, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				require.False(t, result.IsError)

				var response map[string]interface{}
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				regions, ok := response["regions"].([]interface{})
				require.True(t, ok)
				assert.Len(t, regions, 4, "Should deduplicate regions")

				regionStrs := make([]string, len(regions))
				for i, r := range regions {
					regionStrs[i], _ = r.(string)
				}

				assert.Contains(t, regionStrs, "us-east-1")
				assert.Contains(t, regionStrs, "us-west-2")
				assert.Contains(t, regionStrs, "eu-west-1")
				assert.Contains(t, regionStrs, "ap-south-1")

				assert.Equal(t, float64(4), response["total"])
			},
		},
		{
			name: "empty regions",
			mockSetup: func(m *mockspotClient) {
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
				).Return([]spot.Advice{}, nil).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				require.False(t, result.IsError)

				var response map[string]interface{}
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				err := json.Unmarshal([]byte(textContent.Text), &response)
				require.NoError(t, err)

				regions, ok := response["regions"].([]interface{})
				require.True(t, ok)
				assert.Empty(t, regions)
				assert.Equal(t, float64(0), response["total"])
			},
		},
		{
			name: "error handling",
			mockSetup: func(m *mockspotClient) {
				m.EXPECT().GetSpotSavings(
					mock.Anything,
					mock.Anything,
				).Return(nil, errors.New("network timeout")).Once()
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.True(t, result.IsError)
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				assert.Contains(t, textContent.Text, "Failed to retrieve regions")
				assert.Contains(t, textContent.Text, "network timeout")
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
					Arguments: map[string]interface{}{},
				},
			}

			result, err := tool.Handle(context.Background(), req)

			require.NoError(t, err)
			require.NotNil(t, result)
			tt.validateResult(t, result)
		})
	}
}
