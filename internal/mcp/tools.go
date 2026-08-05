// Package mcp provides MCP tools for spotinfo functionality.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cast"

	"spotinfo/internal/spot"
)

// Constants for configuration values
const (
	defaultLimit    = 10
	maxLimit        = 50
	maxInterruption = 100
	avgDivisor      = 2
	maxReliability  = 100
)

// MCP sort_by parameter values.
const (
	sortByPrice       = "price"
	sortByReliability = "reliability"
	sortBySavings     = "savings"
	sortByScore       = "score"
)

// MCP request argument keys and their special values.
const (
	argRegions             = "regions"
	argInstanceTypes       = "instance_types"
	argMinVCPU             = "min_vcpu"
	argMinMemoryGB         = "min_memory_gb"
	argMaxPricePerHour     = "max_price_per_hour"
	argMaxInterruptionRate = "max_interruption_rate"
	argSortBy              = "sort_by"
	argLimit               = "limit"
	argWithScore           = "with_score"
	argMinScore            = "min_score"
	argAZ                  = "az"
	argScoreTimeout        = "score_timeout"
	// allRegions is the "regions" argument value meaning every AWS region.
	allRegions = "all"
)

// JSON field names of the MCP tool responses.
const (
	fieldInstanceType      = "instance_type"
	fieldRegion            = "region"
	fieldSpotPricePerHour  = "spot_price_per_hour"
	fieldSpotPrice         = "spot_price"
	fieldSavingsPercentage = "savings_percentage"
	fieldSavings           = "savings"
	fieldInterruptionRate  = "interruption_rate"
	fieldInterruptionFreq  = "interruption_frequency"
	fieldInterruptionRange = "interruption_range"
	fieldVCPU              = "vcpu"
	fieldMemoryGB          = "memory_gb"
	fieldSpecs             = "specs"
	fieldReliabilityScore  = "reliability_score"
	fieldLivePrice         = "live_price"
	fieldRegionScore       = "region_score"
	fieldZoneScores        = "zone_scores"
	fieldScoreFetchedAt    = "score_fetched_at"
	fieldResults           = "results"
	fieldMetadata          = "metadata"
	fieldTotalResults      = "total_results"
	fieldRegionsSearched   = "regions_searched"
	fieldQueryTimeMS       = "query_time_ms"
	fieldDataSource        = "data_source"
	fieldDataFreshness     = "data_freshness"
	fieldRegions           = "regions"
	fieldTotal             = "total"
)

// Static values reported in the find_spot_instances response metadata.
const (
	dataSourceEmbedded   = "embedded"
	dataFreshnessCurrent = "current"
)

// FindSpotInstancesTool implements the find_spot_instances MCP tool
type FindSpotInstancesTool struct {
	client spotClient
	logger *slog.Logger
}

// NewFindSpotInstancesTool creates a new find_spot_instances tool handler
func NewFindSpotInstancesTool(client spotClient, logger *slog.Logger) *FindSpotInstancesTool {
	return &FindSpotInstancesTool{
		client: client,
		logger: logger,
	}
}

// Handle implements the find_spot_instances tool
// Signature is fixed by mcp-go's server.ToolHandlerFunc, which takes the request
// by value, so gocritic's hugeParam suggestion cannot be applied here.
//
//nolint:gocritic // hugeParam: signature dictated by mcp-go
func (t *FindSpotInstancesTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	startTime := time.Now()
	t.logger.Debug("handling find_spot_instances request", slog.Any("arguments", req.Params.Arguments))

	params := parseParameters(req.Params.Arguments)
	spotSortBy, sortDesc := convertSortParams(params.sortBy)

	// Build options from parameters
	opts := []spot.GetSpotSavingsOption{
		spot.WithRegions(params.regions),
		spot.WithPattern(params.instanceTypes),
		spot.WithOS("linux"),
		spot.WithCPU(params.minVCPU),
		spot.WithMemory(params.minMemoryGB),
		spot.WithMaxPrice(params.maxPrice),
		spot.WithSort(spotSortBy, sortDesc),
	}

	// Add score-related options if requested
	if params.withScore {
		scoreOpts := []spot.GetSpotSavingsOption{
			spot.WithScores(true),
			spot.WithSingleAvailabilityZone(params.az),
		}
		if params.scoreTimeout > 0 {
			scoreOpts = append(scoreOpts, spot.WithScoreTimeout(time.Duration(params.scoreTimeout)*time.Second))
		}
		opts = append(opts, scoreOpts...)
	}
	if params.minScore > 0 {
		opts = append(opts, spot.WithMinScore(params.minScore))
	}

	advices, err := t.client.GetSpotSavings(ctx, opts...)
	if err != nil {
		t.logger.Error("failed to get spot savings", slog.Any("error", err))
		return createErrorResult(fmt.Sprintf("Failed to get spot recommendations: %v", err)), nil
	}

	filteredAdvices := filterByInterruption(advices, params.maxInterruption)
	limitedAdvices := applyLimit(filteredAdvices, params.limit)
	response := buildResponse(limitedAdvices, startTime)

	results, ok := response[fieldResults].([]map[string]interface{})
	if !ok {
		results = []map[string]interface{}{}
	}
	t.logger.Debug("find_spot_instances completed",
		slog.Int(fieldResults, len(results)),
		slog.Int64(fieldQueryTimeMS, time.Since(startTime).Milliseconds()))

	return marshalResponse(response)
}

// params holds parsed parameters for easier handling
type params struct { //nolint:govet
	regions         []string
	instanceTypes   string
	sortBy          string
	maxPrice        float64
	maxInterruption float64
	minVCPU         int
	minMemoryGB     int
	limit           int
	withScore       bool
	minScore        int
	az              bool
	scoreTimeout    int
}

// parseParameters extracts all parameters from the request arguments
func parseParameters(arguments interface{}) *params {
	args, ok := arguments.(map[string]interface{})
	if !ok {
		args = make(map[string]interface{})
	}

	regions := getStringSliceWithDefault(args, argRegions, []string{allRegions})
	if len(regions) == 1 && regions[0] == allRegions {
		regions = []string{allRegions}
	}

	return &params{
		regions:         regions,
		instanceTypes:   cast.ToString(args[argInstanceTypes]),
		minVCPU:         cast.ToInt(args[argMinVCPU]),
		minMemoryGB:     cast.ToInt(args[argMinMemoryGB]),
		maxPrice:        cast.ToFloat64(args[argMaxPricePerHour]),
		maxInterruption: cast.ToFloat64(args[argMaxInterruptionRate]),
		sortBy:          getStringWithDefault(args, argSortBy, sortByReliability),
		limit:           getLimitWithDefault(args, argLimit, defaultLimit),
		withScore:       cast.ToBool(args[argWithScore]),
		minScore:        cast.ToInt(args[argMinScore]),
		az:              cast.ToBool(args[argAZ]),
		scoreTimeout:    cast.ToInt(args[argScoreTimeout]),
	}
}

// convertSortParams converts string sort parameter to internal types
func convertSortParams(sortBy string) (spot.SortBy, bool) {
	switch sortBy {
	case sortByPrice:
		return spot.SortByPrice, false
	case sortByReliability:
		return spot.SortByRange, false
	case sortBySavings:
		return spot.SortBySavings, true
	case sortByScore:
		return spot.SortByScore, false
	default:
		return spot.SortByRange, false
	}
}

// filterByInterruption filters advices by maximum interruption rate
func filterByInterruption(advices []spot.Advice, maxInterruptionParam float64) []spot.Advice {
	if maxInterruptionParam <= 0 || maxInterruptionParam >= maxInterruption {
		return advices
	}

	filtered := make([]spot.Advice, 0, len(advices))
	for _, advice := range advices {
		if calculateAvgInterruption(advice.Range) <= maxInterruptionParam {
			filtered = append(filtered, advice)
		}
	}
	return filtered
}

// applyLimit limits the number of results
func applyLimit(advices []spot.Advice, limit int) []spot.Advice {
	if len(advices) <= limit {
		return advices
	}
	return advices[:limit]
}

// buildResponse creates the response map from filtered advices
func buildResponse(advices []spot.Advice, startTime time.Time) map[string]interface{} {
	results := make([]map[string]interface{}, len(advices))
	regionsSearched := make(map[string]bool)

	for i, advice := range advices {
		regionsSearched[advice.Region] = true
		avgInterruption := calculateAvgInterruption(advice.Range)

		result := map[string]interface{}{
			fieldInstanceType:      advice.Instance,
			fieldRegion:            advice.Region,
			fieldSpotPricePerHour:  advice.Price,
			fieldSpotPrice:         fmt.Sprintf("$%.4f/hour", advice.Price),
			fieldSavingsPercentage: advice.Savings,
			fieldSavings:           fmt.Sprintf("%d%% cheaper than on-demand", advice.Savings),
			fieldInterruptionRate:  avgInterruption,
			fieldInterruptionFreq:  advice.Range.Label,
			fieldInterruptionRange: fmt.Sprintf("%d-%d%%", advice.Range.Min, advice.Range.Max),
			fieldVCPU:              advice.Info.Cores,
			fieldMemoryGB:          advice.Info.RAM,
			fieldSpecs:             fmt.Sprintf("%d vCPU, %.0f GB RAM", advice.Info.Cores, advice.Info.RAM),
			fieldReliabilityScore:  calculateReliabilityScore(avgInterruption),
			fieldLivePrice:         advice.LivePrice,
		}

		// Add score-related fields when available
		if advice.RegionScore != nil {
			result[fieldRegionScore] = *advice.RegionScore
		}
		if len(advice.ZoneScores) > 0 {
			result[fieldZoneScores] = advice.ZoneScores
		}
		if advice.ScoreFetchedAt != nil {
			result[fieldScoreFetchedAt] = advice.ScoreFetchedAt.Format(time.RFC3339)
		}

		results[i] = result
	}

	searchedRegions := make([]string, 0, len(regionsSearched))
	for region := range regionsSearched {
		searchedRegions = append(searchedRegions, region)
	}

	return map[string]interface{}{
		fieldResults: results,
		fieldMetadata: map[string]interface{}{
			fieldTotalResults:    len(results),
			fieldRegionsSearched: searchedRegions,
			fieldQueryTimeMS:     time.Since(startTime).Milliseconds(),
			fieldDataSource:      dataSourceEmbedded,
			fieldDataFreshness:   dataFreshnessCurrent,
		},
	}
}

// calculateAvgInterruption calculates average interruption rate
func calculateAvgInterruption(r spot.Range) float64 {
	return float64(r.Min+r.Max) / avgDivisor
}

// calculateReliabilityScore creates a reliability score based on interruption frequency
func calculateReliabilityScore(avgInterruption float64) int {
	reliabilityScore := maxReliability - avgInterruption
	if reliabilityScore < 0 {
		reliabilityScore = 0
	}
	return int(reliabilityScore)
}

// marshalResponse marshals response to JSON and creates MCP result
func marshalResponse(response interface{}) (*mcp.CallToolResult, error) {
	jsonData, err := json.Marshal(response)
	if err != nil {
		return createErrorResult(fmt.Sprintf("failed to marshal response: %v", err)), nil
	}
	return mcp.NewToolResultText(string(jsonData)), nil
}

// createErrorResult creates a standardized error result
func createErrorResult(message string) *mcp.CallToolResult {
	return mcp.NewToolResultError(message)
}

// Helper functions using spf13/cast with defaults
func getStringWithDefault(args map[string]interface{}, key, defaultValue string) string {
	if val := cast.ToString(args[key]); val != "" {
		return val
	}
	return defaultValue
}

func getLimitWithDefault(args map[string]interface{}, key string, defaultValue int) int {
	limit := cast.ToInt(args[key])
	if limit <= 0 {
		limit = defaultValue
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return limit
}

func getStringSliceWithDefault(args map[string]interface{}, key string, defaultValue []string) []string {
	if slice := cast.ToStringSlice(args[key]); len(slice) > 0 {
		return slice
	}
	return defaultValue
}

// ListSpotRegionsTool implements the list_spot_regions MCP tool
type ListSpotRegionsTool struct {
	client spotClient
	logger *slog.Logger
}

// NewListSpotRegionsTool creates a new list_spot_regions tool handler
func NewListSpotRegionsTool(client spotClient, logger *slog.Logger) *ListSpotRegionsTool {
	return &ListSpotRegionsTool{
		client: client,
		logger: logger,
	}
}

// Handle implements the list_spot_regions tool
//
//nolint:gocritic // hugeParam: signature dictated by mcp-go's server.ToolHandlerFunc
func (t *ListSpotRegionsTool) Handle(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.logger.Debug("handling list_spot_regions request")

	regions, err := t.fetchRegions(ctx)
	if err != nil {
		t.logger.Error("failed to get regions", slog.Any("error", err))
		return createErrorResult(fmt.Sprintf("Failed to retrieve regions: %v", err)), nil
	}

	response := map[string]interface{}{
		fieldRegions: regions,
		fieldTotal:   len(regions),
	}

	t.logger.Debug("list_spot_regions completed", slog.Int("total", len(regions)))
	return marshalResponse(response)
}

// fetchRegions gets all available regions from the spot client
func (t *ListSpotRegionsTool) fetchRegions(ctx context.Context) ([]string, error) {
	opts := []spot.GetSpotSavingsOption{
		spot.WithRegions([]string{allRegions}),
		spot.WithPattern(""),
		spot.WithOS("linux"),
		spot.WithSort(spot.SortByRegion, false),
	}

	allAdvices, err := t.client.GetSpotSavings(ctx, opts...)
	if err != nil {
		return nil, err
	}

	regionSet := make(map[string]bool)
	for _, advice := range allAdvices {
		regionSet[advice.Region] = true
	}

	regions := make([]string, 0, len(regionSet))
	for region := range regionSet {
		regions = append(regions, region)
	}

	return regions, nil
}
