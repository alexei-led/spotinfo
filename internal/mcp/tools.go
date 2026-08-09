// Package mcp provides MCP tools for spotinfo functionality.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cast"

	"spotinfo/internal/cloud"
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
	// jsonSchemaType and jsonTypeString declare an array item's element type;
	// jsonSchemaMinLength keeps an empty string out of such an array.
	jsonSchemaType      = "type"
	jsonTypeString      = "string"
	jsonSchemaMinLength = "minLength"
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

// data_source values of the v1 AWS tools. They are the legacy vocabulary the
// tool published before providers existed and are kept verbatim: a client
// matching on "embedded" must keep working.
const (
	dataSourceLive     = "aws"
	dataSourceEmbedded = "embedded"
)

// Freshness reported alongside the data source. Derived from the source rather
// than asserted: metadata claiming "current" while serving a months-old
// embedded snapshot is worse than no metadata at all.
const (
	dataFreshnessLive     = "live"
	dataFreshnessSnapshot = "embedded-snapshot"
)

// dataSourceFor maps a neutral data mode onto the v1 data_source value.
func dataSourceFor(mode cloud.DataMode) string {
	if mode == cloud.DataModeLive {
		return dataSourceLive
	}

	return dataSourceEmbedded
}

// freshnessFor maps a neutral data mode onto the freshness it can honestly claim.
func freshnessFor(mode cloud.DataMode) string {
	if mode == cloud.DataModeLive {
		return dataFreshnessLive
	}

	return dataFreshnessSnapshot
}

// FindSpotInstancesTool implements the find_spot_instances MCP tool
type FindSpotInstancesTool struct {
	providers providerRegistry
	logger    *slog.Logger
}

// NewFindSpotInstancesTool creates a new find_spot_instances tool handler
func NewFindSpotInstancesTool(providers providerRegistry, logger *slog.Logger) *FindSpotInstancesTool {
	return &FindSpotInstancesTool{
		providers: providers,
		logger:    logger,
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

	result, err := queryAWS(ctx, t.providers, params.query())
	if err != nil {
		t.logger.Error("failed to get spot savings", slog.Any("error", err))

		return createErrorResult(fmt.Sprintf("Failed to get spot recommendations: %v", err)), nil
	}

	filtered := filterByInterruption(result.Candidates, params.maxInterruption)
	limited := applyLimit(filtered, params.limit)
	response := buildResponse(limited, startTime, result.Mode)

	results, ok := response[fieldResults].([]map[string]any)
	if !ok {
		results = []map[string]any{}
	}
	t.logger.Debug("find_spot_instances completed",
		slog.Int(fieldResults, len(results)),
		slog.Int64(fieldQueryTimeMS, time.Since(startTime).Milliseconds()))

	return marshalResponse(response)
}

// queryAWS resolves the AWS provider and acquires candidates. The capability
// check runs before acquisition, so an unsupported request costs no I/O.
func queryAWS(ctx context.Context, registry providerRegistry, query *cloud.Query) (cloud.Result, error) {
	if registry == nil {
		return cloud.Result{}, fmt.Errorf("%w: no provider registry is configured", cloud.ErrDataUnavailable)
	}

	provider, err := registry.Get(cloud.ProviderAWS)
	if err != nil {
		return cloud.Result{}, err
	}
	if err := provider.Capabilities().Require(query.CapabilityNeeds()); err != nil {
		return cloud.Result{}, err
	}

	return provider.Query(ctx, query)
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

// query builds the neutral acquisition request. A zero maximum price is the v1
// "no ceiling" value, not a ceiling of nothing; an unparsable price is dropped
// rather than turned into a filter the caller did not ask for. A price finer
// than the fixed-point scale is a ceiling, not an unparsable value: it is
// truncated to the scale, which filters exactly as v1's float comparison did.
func (p *params) query() *cloud.Query {
	regions := make([]cloud.Region, 0, len(p.regions))
	for _, region := range p.regions {
		regions = append(regions, cloud.Region(region))
	}

	query := &cloud.Query{
		MachinePattern: p.instanceTypes,
		OS:             cloud.OSLinux,
		Regions:        regions,
		Sort:           sortOrderFor(p.sortBy),
		Placement: cloud.PlacementRequest{
			Timeout:    time.Duration(p.scoreTimeout) * time.Second,
			MinScore:   p.minScore,
			SingleZone: p.az,
			Enabled:    p.withScore,
		},
		MinMemoryGiB: float64(p.minMemoryGB),
		MinVCPU:      p.minVCPU,
	}
	if p.maxPrice > 0 {
		if ceiling, err := cloud.MoneyCeilingFromFloat(p.maxPrice); err == nil {
			query.MaxPrice = &ceiling
		}
	}

	return query
}

// parseParameters extracts all parameters from the request arguments
func parseParameters(arguments any) *params {
	args, ok := arguments.(map[string]any)
	if !ok {
		args = make(map[string]any)
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

// sortOrderFor converts the tool's sort_by parameter to a neutral sort order.
func sortOrderFor(sortBy string) cloud.SortOrder {
	switch sortBy {
	case sortByPrice:
		return cloud.SortOrder{Key: cloud.SortByPrice}
	case sortByReliability:
		return cloud.SortOrder{Key: cloud.SortByRisk}
	case sortBySavings:
		return cloud.SortOrder{Key: cloud.SortBySavings, Descending: true}
	case sortByScore:
		return cloud.SortOrder{Key: cloud.SortByPlacementScore}
	default:
		return cloud.SortOrder{Key: cloud.SortByRisk}
	}
}

// filterByInterruption filters candidates by maximum interruption rate
func filterByInterruption(candidates []cloud.Candidate, maxInterruptionParam float64) []cloud.Candidate {
	if maxInterruptionParam <= 0 || maxInterruptionParam >= maxInterruption {
		return candidates
	}

	filtered := make([]cloud.Candidate, 0, len(candidates))
	for i := range candidates {
		if calculateAvgInterruption(&candidates[i].Risk) <= maxInterruptionParam {
			filtered = append(filtered, candidates[i])
		}
	}

	return filtered
}

// applyLimit limits the number of results
func applyLimit(candidates []cloud.Candidate, limit int) []cloud.Candidate {
	if len(candidates) <= limit {
		return candidates
	}

	return candidates[:limit]
}

// buildResponse creates the response map from filtered candidates
func buildResponse(candidates []cloud.Candidate, startTime time.Time, mode cloud.DataMode) map[string]any {
	results := make([]map[string]any, len(candidates))
	regionsSearched := make(map[string]bool)

	for i := range candidates {
		candidate := &candidates[i]
		regionsSearched[string(candidate.Location.Region)] = true
		avgInterruption := calculateAvgInterruption(&candidate.Risk)
		price := spotPrice(candidate)

		result := map[string]any{
			fieldInstanceType:      string(candidate.Machine.ID),
			fieldRegion:            string(candidate.Location.Region),
			fieldSpotPricePerHour:  price,
			fieldSpotPrice:         fmt.Sprintf("$%.4f/hour", price),
			fieldSavingsPercentage: savingsPercent(candidate),
			fieldSavings:           fmt.Sprintf("%d%% cheaper than on-demand", savingsPercent(candidate)),
			fieldInterruptionRate:  avgInterruption,
			fieldInterruptionFreq:  candidate.Risk.Label,
			fieldInterruptionRange: interruptionRange(&candidate.Risk),
			fieldVCPU:              candidate.Machine.VCPU,
			fieldMemoryGB:          candidate.Machine.MemoryGiB,
			fieldSpecs:             fmt.Sprintf("%d vCPU, %.0f GB RAM", candidate.Machine.VCPU, candidate.Machine.MemoryGiB),
			fieldReliabilityScore:  calculateReliabilityScore(avgInterruption),
			fieldLivePrice:         candidate.Spot != nil && candidate.Spot.Live,
		}
		addPlacementFields(result, candidate.Placements)

		results[i] = result
	}

	return map[string]any{
		fieldResults: results,
		fieldMetadata: map[string]any{
			fieldTotalResults:    len(results),
			fieldRegionsSearched: slices.Collect(maps.Keys(regionsSearched)),
			fieldQueryTimeMS:     time.Since(startTime).Milliseconds(),
			fieldDataSource:      dataSourceFor(mode),
			fieldDataFreshness:   freshnessFor(mode),
		},
	}
}

// addPlacementFields republishes placement scores in their v1 shape: one
// regional score, or a zone-to-score map, with the fetch time they share.
func addPlacementFields(result map[string]any, placements []cloud.PlacementObservation) {
	zoneScores := make(map[string]int)

	for i := range placements {
		placement := &placements[i]
		if placement.Location.Zone == "" {
			result[fieldRegionScore] = placement.Score
		} else {
			zoneScores[placement.Location.Zone] = placement.Score
		}
		if placement.FetchedAt != nil {
			result[fieldScoreFetchedAt] = placement.FetchedAt.Format(time.RFC3339)
		}
	}

	if len(zoneScores) > 0 {
		result[fieldZoneScores] = zoneScores
	}
}

// spotPrice reports the v1 price. An unknown price stays zero: that is what the
// v1 contract published, and this tool is the compatibility surface.
func spotPrice(candidate *cloud.Candidate) float64 {
	if candidate.Spot == nil {
		return 0
	}

	return candidate.Spot.Amount.Float64()
}

// savingsPercent reports the v1 savings figure, where zero means "not published".
func savingsPercent(candidate *cloud.Candidate) int {
	if candidate.SavingsPercent == nil {
		return 0
	}

	return *candidate.SavingsPercent
}

// interruptionRange renders the v1 percentage range. A candidate with no
// published risk renders the same "0-0%" v1 produced for an unlabelled range.
func interruptionRange(risk *cloud.RiskObservation) string {
	low, high := interruptionBounds(risk)

	return fmt.Sprintf("%d-%d%%", int(low), int(high))
}

func interruptionBounds(risk *cloud.RiskObservation) (low, high float64) {
	if risk.MinPercent != nil {
		low = *risk.MinPercent
	}
	if risk.MaxPercent != nil {
		high = *risk.MaxPercent
	}

	return low, high
}

// calculateAvgInterruption calculates average interruption rate
func calculateAvgInterruption(risk *cloud.RiskObservation) float64 {
	low, high := interruptionBounds(risk)

	return (low + high) / avgDivisor
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
func marshalResponse(response any) (*mcp.CallToolResult, error) {
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
func getStringWithDefault(args map[string]any, key, defaultValue string) string {
	if val := cast.ToString(args[key]); val != "" {
		return val
	}
	return defaultValue
}

func getLimitWithDefault(args map[string]any, key string, defaultValue int) int {
	limit := cast.ToInt(args[key])
	if limit <= 0 {
		limit = defaultValue
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return limit
}

func getStringSliceWithDefault(args map[string]any, key string, defaultValue []string) []string {
	if slice := cast.ToStringSlice(args[key]); len(slice) > 0 {
		return slice
	}
	return defaultValue
}

// ListSpotRegionsTool implements the list_spot_regions MCP tool
type ListSpotRegionsTool struct {
	providers providerRegistry
	logger    *slog.Logger
}

// NewListSpotRegionsTool creates a new list_spot_regions tool handler
func NewListSpotRegionsTool(providers providerRegistry, logger *slog.Logger) *ListSpotRegionsTool {
	return &ListSpotRegionsTool{
		providers: providers,
		logger:    logger,
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

	response := map[string]any{
		fieldRegions: regions,
		fieldTotal:   len(regions),
	}

	t.logger.Debug("list_spot_regions completed", slog.Int(fieldTotal, len(regions)))
	return marshalResponse(response)
}

// fetchRegions gets every region the AWS provider publishes candidates for.
func (t *ListSpotRegionsTool) fetchRegions(ctx context.Context) ([]string, error) {
	query := &cloud.Query{
		OS:      cloud.OSLinux,
		Regions: []cloud.Region{allRegions},
		Sort:    cloud.SortOrder{Key: cloud.SortByRegion},
	}

	result, err := queryAWS(ctx, t.providers, query)
	if err != nil {
		return nil, err
	}

	regionSet := make(map[string]bool)
	for i := range result.Candidates {
		regionSet[string(result.Candidates[i].Location.Region)] = true
	}

	return regionNames(regionSet), nil
}

// regionNames flattens a region set. The slice is always allocated so an empty
// result serialises as [] rather than null, which is what the v1 contract
// published.
func regionNames(regionSet map[string]bool) []string {
	names := make([]string, 0, len(regionSet))

	return append(names, slices.Collect(maps.Keys(regionSet))...)
}
