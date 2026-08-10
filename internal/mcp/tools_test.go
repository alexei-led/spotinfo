package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestParseParameters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     any
		expected *params
	}{
		{
			name: "complete parameters including score options",
			args: map[string]any{
				"regions":               []any{"us-east-1", "eu-west-1"},
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
			args: map[string]any{},
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
			args: map[string]any{
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
			args: map[string]any{
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

func TestSortOrderFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		sortBy string
		want   cloud.SortOrder
	}{
		{"price", "price", cloud.SortOrder{Key: cloud.SortByPrice}},
		{"reliability", "reliability", cloud.SortOrder{Key: cloud.SortByRisk}},
		{"savings", "savings", cloud.SortOrder{Key: cloud.SortBySavings, Descending: true}},
		{"score", "score", cloud.SortOrder{Key: cloud.SortByPlacementScore}},
		{"default", "unknown", cloud.SortOrder{Key: cloud.SortByRisk}},
		{"empty", "", cloud.SortOrder{Key: cloud.SortByRisk}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sortOrderFor(tt.sortBy))
		})
	}
}

// Every acquisition-shaping parameter must reach the provider. Asserting the
// response alone would pass even if score, zone, ordering and price options
// were dropped, because a fixture can carry scores the request never asked for.
func TestToolParametersReachTheProviderQuery(t *testing.T) {
	t.Parallel()

	provider, registry := awsStub()

	_, err := NewFindSpotInstancesTool(registry, testLogger()).Handle(context.Background(),
		createTestCallToolRequest(map[string]any{
			"regions":            []any{"us-east-1", "eu-west-1"},
			"instance_types":     "m5.*",
			"min_vcpu":           4,
			"min_memory_gb":      16,
			"max_price_per_hour": 0.25,
			"sort_by":            "savings",
			"with_score":         true,
			"min_score":          7,
			"az":                 true,
			"score_timeout":      60,
		}))
	require.NoError(t, err)

	query := provider.lastQuery()
	assert.Equal(t, []cloud.Region{"us-east-1", "eu-west-1"}, query.Regions)
	assert.Equal(t, "m5.*", query.MachinePattern)
	assert.Equal(t, cloud.OSLinux, query.OS)
	assert.Equal(t, 4, query.MinVCPU)
	assert.InDelta(t, 16.0, query.MinMemoryGiB, 0)
	require.NotNil(t, query.MaxPrice)
	assert.Equal(t, "0.250000000", query.MaxPrice.String())
	assert.Equal(t, cloud.SortOrder{Key: cloud.SortBySavings, Descending: true}, query.Sort)
	assert.Equal(t, cloud.PlacementRequest{
		Timeout:    60 * time.Second,
		MinScore:   7,
		SingleZone: true,
		Enabled:    true,
	}, query.Placement)
}

// A price ceiling finer than the fixed-point scale still filters. A caller
// computing a budget — a monthly figure divided by 720 hours — sends one of
// these routinely, and dropping it would hand back machines above the ceiling
// with no error and no warning.
func TestFinerThanScalePriceCeilingStillFilters(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		price float64
		want  string
	}{
		"within the scale":       {price: 0.04, want: "0.040000000"},
		"exactly at the scale":   {price: 0.040000001, want: "0.040000001"},
		"one digit past":         {price: 0.0400000001, want: "0.040000000"},
		"monthly budget an hour": {price: 30.0 / 720.0, want: "0.041666666"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			provider, registry := awsStub()

			_, err := NewFindSpotInstancesTool(registry, testLogger()).Handle(t.Context(),
				createTestCallToolRequest(map[string]any{"max_price_per_hour": test.price}))
			require.NoError(t, err)

			query := provider.lastQuery()
			require.NotNil(t, query.MaxPrice, "the caller's ceiling must reach the provider")
			assert.Equal(t, test.want, query.MaxPrice.String())
		})
	}
}

// A server can be built with no registry — NewServer accepts Providers: nil and
// still registers every tool. The v1 tools must report that as a failed result,
// not dereference it: a panic inside a handler takes the stdio or SSE process
// down for every connected client.
func TestV1ToolsFailClosedWithoutARegistry(t *testing.T) {
	t.Parallel()

	for name, handle := range map[string]func(context.Context) (*mcp.CallToolResult, error){
		"find_spot_instances": func(ctx context.Context) (*mcp.CallToolResult, error) {
			return NewFindSpotInstancesTool(nil, testLogger()).Handle(ctx, createTestCallToolRequest(map[string]any{}))
		},
		"list_spot_regions": func(ctx context.Context) (*mcp.CallToolResult, error) {
			return NewListSpotRegionsTool(nil, testLogger()).Handle(ctx, createTestCallToolRequest(map[string]any{}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result, err := handle(t.Context())
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.IsError, "a missing registry must be reported, not ignored")
		})
	}
}

// The capability gate runs before acquisition, so a provider that cannot answer
// the request costs no I/O. Asserting the error alone would not prove that —
// the stub records every call, so a zero call count is the proof.
func TestV1ToolsRejectAnUnsupportedCapabilityBeforeAcquisition(t *testing.T) {
	t.Parallel()

	// The v1 query needs risk for its interruption column; an offline provider
	// publishing none is the shape GCP and Azure have.
	provider := &stubProvider{id: cloud.ProviderAWS, capabilities: offlineLinuxCapabilities()}
	registry := newStubRegistry(provider)

	result, err := NewFindSpotInstancesTool(registry, testLogger()).
		Handle(t.Context(), createTestCallToolRequest(map[string]any{}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Zero(t, provider.callCount(), "the capability check must run before acquisition")
}

// A non-finite or negative ceiling is neither a ceiling nor the v1 "no ceiling"
// value. cast.ToFloat64 accepts "NaN" and "-Inf" from a quoted argument, and the
// `> 0` test in query() reads both as unset, which would answer an unfiltered
// query as if the caller's ceiling had been applied. Zero stays accepted — see
// TestZeroMaxPriceRequestsNoCeiling.
func TestUnusableMaxPriceIsRejectedBeforeAcquisition(t *testing.T) {
	t.Parallel()

	for name, price := range map[string]any{
		"quoted not a number":      "NaN",
		"quoted positive infinity": "+Inf",
		"quoted negative infinity": "-Inf",
		"negative":                 -0.01,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			provider, registry := awsStub()

			result, err := NewFindSpotInstancesTool(registry, testLogger()).Handle(t.Context(),
				createTestCallToolRequest(map[string]any{"max_price_per_hour": price}))
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.IsError, "an unusable ceiling must be reported, not silently dropped")
			assert.Zero(t, provider.callCount(), "the argument check must run before acquisition")
		})
	}
}

// A non-finite max_interruption_rate drops every candidate inside the filter
// loop, which a caller cannot tell apart from "nothing matched".
func TestUnusableMaxInterruptionRateIsRejectedBeforeAcquisition(t *testing.T) {
	t.Parallel()

	for name, rate := range map[string]any{
		"quoted not a number": "NaN",
		"quoted infinity":     "-Inf",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			provider, registry := awsStub()

			result, err := NewFindSpotInstancesTool(registry, testLogger()).Handle(t.Context(),
				createTestCallToolRequest(map[string]any{"max_interruption_rate": rate}))
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.True(t, result.IsError)
			assert.Zero(t, provider.callCount())
		})
	}
}

// A zero max_price_per_hour is the v1 "no ceiling" value, not a ceiling of zero.
func TestZeroMaxPriceRequestsNoCeiling(t *testing.T) {
	t.Parallel()

	provider, registry := awsStub()

	_, err := NewFindSpotInstancesTool(registry, testLogger()).Handle(context.Background(),
		createTestCallToolRequest(map[string]any{"max_price_per_hour": 0}))
	require.NoError(t, err)

	assert.Nil(t, provider.lastQuery().MaxPrice)
}

func TestFilterByInterruption(t *testing.T) {
	t.Parallel()

	candidates := buildCandidates(
		testCandidate{RiskLabel: "<5%", RiskMin: 0, RiskMax: 5},      // avg = 2.5
		testCandidate{RiskLabel: "10-20%", RiskMin: 10, RiskMax: 20}, // avg = 15
		testCandidate{RiskLabel: "30-40%", RiskMin: 30, RiskMax: 40}, // avg = 35
	)

	tests := []struct {
		name            string
		maxInterruption float64
		expectedCount   int
	}{
		{name: "filter by 10 - should keep 1", maxInterruption: 10, expectedCount: 1},
		{name: "filter by 25 - should keep 2", maxInterruption: 25, expectedCount: 2},
		{name: "no filter (0) - should keep all", maxInterruption: 0, expectedCount: 3},
		{name: "no filter (>=100) - should keep all", maxInterruption: 100, expectedCount: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Len(t, filterByInterruption(candidates, tt.maxInterruption), tt.expectedCount)
		})
	}
}

func TestBuildResponse(t *testing.T) {
	t.Parallel()

	startTime := time.Now()
	scoreValue := 8
	scoreTime := time.Now()
	candidates := buildCandidates(
		testCandidate{
			Machine: "m5.large", Region: "us-east-1", Price: 0.0928, Savings: 70,
			RiskLabel: "5-10%", RiskMin: 5, RiskMax: 10, VCPU: 2, MemoryGiB: 8,
			RegionScore: &scoreValue, ScoreFetchedAt: &scoreTime,
		},
		testCandidate{
			Machine: "t3.medium", Region: "eu-west-1", Price: 0.0416, Savings: 65,
			RiskLabel: "10-15%", RiskMin: 10, RiskMax: 15, VCPU: 2, MemoryGiB: 4,
			ZoneScores:     map[string]int{"eu-west-1a": 7, "eu-west-1b": 9},
			ScoreFetchedAt: &scoreTime,
		},
	)

	response := buildResponse(candidates, startTime, cloud.DataModeEmbeddedSnapshot)

	assert.Contains(t, response, "results")
	assert.Contains(t, response, "metadata")

	results, ok := response["results"].([]map[string]any)
	require.True(t, ok, "results should be a slice of maps")
	require.Len(t, results, 2)

	firstResult := results[0]
	assert.Equal(t, "m5.large", firstResult["instance_type"])
	assert.Equal(t, "us-east-1", firstResult["region"])
	assert.InDelta(t, 0.0928, firstResult["spot_price_per_hour"], 0)
	assert.Equal(t, 70, firstResult["savings_percentage"])
	assert.InDelta(t, 7.5, firstResult["interruption_rate"], 0) // (5+10)/2
	assert.Equal(t, 92, firstResult["reliability_score"])       // 100-7.5
	assert.Equal(t, 8, firstResult["region_score"])

	assert.Equal(t, map[string]int{"eu-west-1a": 7, "eu-west-1b": 9}, results[1]["zone_scores"])

	metadata, ok := response["metadata"].(map[string]any)
	require.True(t, ok, "metadata should be a map")
	assert.Equal(t, 2, metadata["total_results"])
	assert.Equal(t, "embedded", metadata["data_source"])
	assert.Equal(t, dataFreshnessSnapshot, metadata["data_freshness"])
}

// An unknown price and an unpublished savings figure are absences in the
// neutral domain. The v1 response reported both as zero, and this tool is the
// compatibility surface, so they stay zero here.
func TestBuildResponseReportsAbsentObservationsAsTheV1Zero(t *testing.T) {
	t.Parallel()

	response := buildResponse(buildCandidates(testCandidate{
		Machine: "m5.large", Region: "us-east-1", VCPU: 2, MemoryGiB: 8,
	}), time.Now(), cloud.DataModeLive)

	results, ok := response["results"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, results, 1)

	assert.InDelta(t, 0.0, results[0]["spot_price_per_hour"], 0)
	assert.Equal(t, "$0.0000/hour", results[0]["spot_price"])
	assert.Equal(t, 0, results[0]["savings_percentage"])
	assert.Equal(t, "0-0%", results[0]["interruption_range"])
	assert.Equal(t, false, results[0]["live_price"])
	assert.NotContains(t, results[0], "region_score")

	metadata, ok := response["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "aws", metadata["data_source"])
	assert.Equal(t, dataFreshnessLive, metadata["data_freshness"])
}

func TestCalculateAvgInterruption(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fixture   testCandidate
		expected  float64
		published bool
	}{
		{"normal range", testCandidate{RiskLabel: "10-20%", RiskMin: 10, RiskMax: 20}, 15.0, true},
		// Zero here is not a measurement of zero. The second result is what keeps
		// a caller from ranking an unmeasured machine as the most reliable one.
		{"no published risk", testCandidate{}, 0.0, false},
		{"single value", testCandidate{RiskLabel: "5%", RiskMin: 5, RiskMax: 5}, 5.0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			risk := tt.fixture.build().Risk
			average, published := calculateAvgInterruption(&risk)
			assert.Equal(t, tt.published, published)
			assert.InDelta(t, tt.expected, average, 0)
		})
	}
}

// A candidate whose provider publishes no risk must not pass an interruption
// ceiling. Reading its silence as 0% admitted it under every ceiling and scored
// it 100 reliability — the most reliable result in the list, unmeasured.
func TestFilterByInterruptionDropsUnpublishedRisk(t *testing.T) {
	t.Parallel()

	measured := testCandidate{Region: "us-east-1", Machine: "m6i.large", RiskLabel: "<5%", RiskMin: 0, RiskMax: 5}.build()
	unmeasured := testCandidate{Region: "us-central1", Machine: "n2-standard-2"}.build()
	require.Equal(t, cloud.RiskStatusUnavailable, unmeasured.Risk.Status)

	filtered := filterByInterruption([]cloud.Candidate{measured, unmeasured}, 10)
	require.Len(t, filtered, 1)
	assert.Equal(t, cloud.MachineID("m6i.large"), filtered[0].Machine.ID)

	// With no ceiling asked for, v1 returned everything and still does.
	assert.Len(t, filterByInterruption([]cloud.Candidate{measured, unmeasured}, 0), 2)
}

func TestCalculateReliabilityScore(t *testing.T) {
	t.Parallel()

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
			assert.Equal(t, tt.expected, calculateReliabilityScore(tt.avgInterruption))
		})
	}
}

func TestFindSpotInstancesTool_Handle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		arguments      any
		registry       providerRegistry
		validateResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name: "basic request with price sorting",
			arguments: map[string]any{
				"regions":        []any{"us-east-1"},
				"instance_types": "t3.micro",
				"sort_by":        "price",
			},
			registry: registryOf(testCandidate{
				Machine: "t3.micro", Region: "us-east-1", Price: 0.0104, Savings: 68,
				RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 2, MemoryGiB: 1,
			}),
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				results := decodeResults(t, result)
				require.Len(t, results, 1)

				firstResult, ok := results[0].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "t3.micro", firstResult["instance_type"])
				assert.Equal(t, "$0.0104/hour", firstResult["spot_price"])
			},
		},
		{
			name: "request with score enrichment",
			arguments: map[string]any{
				"regions":        []any{"us-west-2"},
				"instance_types": "m5.large",
				"with_score":     true,
				"min_score":      7,
				"sort_by":        "score",
			},
			registry: registryOf(
				testCandidate{
					Machine: "m5.large", Region: "us-west-2", Price: 0.096, Savings: 70,
					RiskLabel: "5-10%", RiskMin: 5, RiskMax: 10, VCPU: 2, MemoryGiB: 8,
					RegionScore: scorePtr(8),
				},
			),
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				results := decodeResults(t, result)
				require.Len(t, results, 1, "the minimum-score filter is applied by the provider")

				firstResult, ok := results[0].(map[string]any)
				require.True(t, ok)
				assert.InDelta(t, 8.0, firstResult["region_score"], 0)
			},
		},
		{
			name: "request with AZ-level scores",
			arguments: map[string]any{
				"regions":       []any{"eu-west-1"},
				"with_score":    true,
				"az":            true,
				"score_timeout": 60,
			},
			registry: registryOf(testCandidate{
				Machine: "t3.small", Region: "eu-west-1", Price: 0.0208, Savings: 68,
				RiskLabel: "5-10%", RiskMin: 5, RiskMax: 10, VCPU: 2, MemoryGiB: 2,
				ZoneScores: map[string]int{"eu-west-1a": 9, "eu-west-1b": 7, "eu-west-1c": 8},
			}),
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				results := decodeResults(t, result)
				require.Len(t, results, 1)

				firstResult, ok := results[0].(map[string]any)
				require.True(t, ok)
				assert.Len(t, firstResult["zone_scores"], 3)
			},
		},
		{
			name: "interruption rate filtering",
			arguments: map[string]any{
				"regions":               []any{"us-east-1"},
				"max_interruption_rate": 7.5,
			},
			registry: registryOf(
				testCandidate{
					Machine: "t3.micro", Region: "us-east-1", Price: 0.01, VCPU: 2, MemoryGiB: 1,
					RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, // avg = 2.5, should pass
				},
				testCandidate{
					Machine: "t3.small", Region: "us-east-1", Price: 0.02, VCPU: 2, MemoryGiB: 2,
					RiskLabel: "10-20%", RiskMin: 10, RiskMax: 20, // avg = 15, should be filtered
				},
			),
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				results := decodeResults(t, result)
				require.Len(t, results, 1, "Should filter out high interruption instances")

				firstResult, ok := results[0].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "t3.micro", firstResult["instance_type"])
			},
		},
		{
			name:      "acquisition failure",
			arguments: map[string]any{"regions": []any{"invalid-region"}},
			registry:  failingAWSStub(errAcquisition),
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.True(t, result.IsError)
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				assert.Contains(t, textContent.Text, "Failed to get spot recommendations")
				assert.Contains(t, textContent.Text, "acquisition failed")
			},
		},
		{
			name:      "unavailable provider",
			arguments: map[string]any{},
			registry:  newStubRegistry(),
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.True(t, result.IsError)
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				assert.Contains(t, textContent.Text, "PROVIDER_NOT_REGISTERED")
			},
		},
		{
			name:      "empty results",
			arguments: map[string]any{"instance_types": "z99.mega"},
			registry:  registryOf(),
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.Empty(t, decodeResults(t, result))

				metadata := decodeMetadata(t, result)
				assert.InDelta(t, 0.0, metadata["total_results"], 0)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewFindSpotInstancesTool(tt.registry, testLogger())

			result, err := tool.Handle(context.Background(), mcp.CallToolRequest{
				Params: mcp.CallToolParams{Arguments: tt.arguments},
			})

			require.NoError(t, err)
			require.NotNil(t, result)
			tt.validateResult(t, result)
		})
	}
}

func TestListSpotRegionsTool_Handle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		registry       providerRegistry
		validateResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name: "successful regions list",
			registry: registryOf(
				testCandidate{Region: "us-east-1", Machine: "t3.micro"},
				testCandidate{Region: "us-west-2", Machine: "t3.small"},
				testCandidate{Region: "us-east-1", Machine: "m5.large"}, // duplicate region
				testCandidate{Region: "eu-west-1", Machine: "t3.medium"},
				testCandidate{Region: "ap-south-1", Machine: "t3.large"},
			),
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				require.False(t, result.IsError)

				response := decodeResponse(t, result)
				regions, ok := response["regions"].([]any)
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

				assert.InDelta(t, 4.0, response["total"], 0)
			},
		},
		{
			name:     "empty regions",
			registry: registryOf(),
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				require.False(t, result.IsError)

				response := decodeResponse(t, result)
				regions, ok := response["regions"].([]any)
				require.True(t, ok)
				assert.Empty(t, regions)
				assert.InDelta(t, 0.0, response["total"], 0)
			},
		},
		{
			name:     "acquisition failure",
			registry: failingAWSStub(errAcquisition),
			validateResult: func(t *testing.T, result *mcp.CallToolResult) {
				assert.True(t, result.IsError)
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				assert.Contains(t, textContent.Text, "Failed to retrieve regions")
				assert.Contains(t, textContent.Text, "acquisition failed")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewListSpotRegionsTool(tt.registry, testLogger())

			result, err := tool.Handle(context.Background(), mcp.CallToolRequest{
				Params: mcp.CallToolParams{Arguments: map[string]any{}},
			})

			require.NoError(t, err)
			require.NotNil(t, result)
			tt.validateResult(t, result)
		})
	}
}

// Freshness is derived from the data mode, never asserted. Metadata claiming
// "current" while serving a months-old embedded snapshot is worse than none.
func TestFreshnessAndDataSourceAreDerivedFromTheDataMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mode          cloud.DataMode
		wantFreshness string
		wantSource    string
	}{
		{name: "live provider data", mode: cloud.DataModeLive, wantFreshness: dataFreshnessLive, wantSource: dataSourceLive},
		{
			name: "embedded snapshot", mode: cloud.DataModeEmbeddedSnapshot,
			wantFreshness: dataFreshnessSnapshot, wantSource: dataSourceEmbedded,
		},
		{
			name: "unknown mode is not claimed as live", mode: "something-else",
			wantFreshness: dataFreshnessSnapshot, wantSource: dataSourceEmbedded,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.wantFreshness, freshnessFor(tc.mode))
			assert.Equal(t, tc.wantSource, dataSourceFor(tc.mode))
		})
	}
}

func registryOf(fixtures ...testCandidate) providerRegistry {
	_, registry := awsStub(buildCandidates(fixtures...)...)

	return registry
}

func scorePtr(score int) *int { return &score }

func decodeResponse(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()

	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	textContent, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	var response map[string]any
	require.NoError(t, json.Unmarshal([]byte(textContent.Text), &response))

	return response
}

func decodeResults(t *testing.T, result *mcp.CallToolResult) []any {
	t.Helper()

	results, ok := decodeResponse(t, result)["results"].([]any)
	require.True(t, ok)

	return results
}

func decodeMetadata(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()

	metadata, ok := decodeResponse(t, result)["metadata"].(map[string]any)
	require.True(t, ok)

	return metadata
}
