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
func (t *FindSpotInstancesTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	startTime := time.Now()
	t.logger.Debug("handling find_spot_instances request", slog.Any("arguments", req.Params.Arguments))

	params := parseParameters(req.Params.Arguments)
	spotSortBy, sortDesc := convertSortParams(params.sortBy)

	advices, err := t.client.GetSpotSavings(ctx, params.regions, params.instanceTypes, "linux", params.minVCPU, params.minMemoryGB, params.maxPrice, spotSortBy, sortDesc)
	if err != nil {
		t.logger.Error("failed to get spot savings", slog.Any("error", err))
		return createErrorResult(fmt.Sprintf("Failed to get spot recommendations: %v", err)), nil
	}

	filteredAdvices := filterByInterruption(advices, params.maxInterruption)
	limitedAdvices := applyLimit(filteredAdvices, params.limit)
	response := buildResponse(limitedAdvices, startTime)

	results, ok := response["results"].([]map[string]interface{})
	if !ok {
		results = []map[string]interface{}{}
	}
	t.logger.Debug("find_spot_instances completed",
		slog.Int("results", len(results)),
		slog.Int64("query_time_ms", time.Since(startTime).Milliseconds()))

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
}

// parseParameters extracts all parameters from the request arguments
func parseParameters(arguments interface{}) *params {
	args, ok := arguments.(map[string]interface{})
	if !ok {
		args = make(map[string]interface{})
	}

	regions := getStringSliceWithDefault(args, "regions", []string{"all"})
	if len(regions) == 1 && regions[0] == "all" {
		regions = []string{"all"}
	}

	return &params{
		regions:         regions,
		instanceTypes:   cast.ToString(args["instance_types"]),
		minVCPU:         cast.ToInt(args["min_vcpu"]),
		minMemoryGB:     cast.ToInt(args["min_memory_gb"]),
		maxPrice:        cast.ToFloat64(args["max_price_per_hour"]),
		maxInterruption: cast.ToFloat64(args["max_interruption_rate"]),
		sortBy:          getStringWithDefault(args, "sort_by", "reliability"),
		limit:           getLimitWithDefault(args, "limit", defaultLimit),
	}
}

// convertSortParams converts string sort parameter to internal types
func convertSortParams(sortBy string) (spot.SortBy, bool) {
	switch sortBy {
	case "price":
		return spot.SortByPrice, false
	case "reliability":
		return spot.SortByRange, false
	case "savings":
		return spot.SortBySavings, true
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

		results[i] = map[string]interface{}{
			"instance_type":          advice.Instance,
			"region":                 advice.Region,
			"spot_price_per_hour":    advice.Price,
			"spot_price":             fmt.Sprintf("$%.4f/hour", advice.Price),
			"savings_percentage":     advice.Savings,
			"savings":                fmt.Sprintf("%d%% cheaper than on-demand", advice.Savings),
			"interruption_rate":      avgInterruption,
			"interruption_frequency": advice.Range.Label,
			"interruption_range":     fmt.Sprintf("%d-%d%%", advice.Range.Min, advice.Range.Max),
			"vcpu":                   advice.Info.Cores,
			"memory_gb":              advice.Info.RAM,
			"specs":                  fmt.Sprintf("%d vCPU, %.0f GB RAM", advice.Info.Cores, advice.Info.RAM),
			"reliability_score":      calculateReliabilityScore(avgInterruption),
		}
	}

	searchedRegions := make([]string, 0, len(regionsSearched))
	for region := range regionsSearched {
		searchedRegions = append(searchedRegions, region)
	}

	return map[string]interface{}{
		"results": results,
		"metadata": map[string]interface{}{
			"total_results":    len(results),
			"regions_searched": searchedRegions,
			"query_time_ms":    time.Since(startTime).Milliseconds(),
			"data_source":      "embedded",
			"data_freshness":   "current",
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
func (t *ListSpotRegionsTool) Handle(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.logger.Debug("handling list_spot_regions request")

	regions, err := t.fetchRegions(ctx)
	if err != nil {
		t.logger.Error("failed to get regions", slog.Any("error", err))
		return createErrorResult(fmt.Sprintf("Failed to retrieve regions: %v", err)), nil
	}

	response := map[string]interface{}{
		"regions": regions,
		"total":   len(regions),
	}

	t.logger.Debug("list_spot_regions completed", slog.Int("total", len(regions)))
	return marshalResponse(response)
}

// fetchRegions gets all available regions from the spot client
func (t *ListSpotRegionsTool) fetchRegions(ctx context.Context) ([]string, error) {
	allAdvices, err := t.client.GetSpotSavings(ctx, []string{"all"}, "", "linux", 0, 0, 0, spot.SortByRegion, false)
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
