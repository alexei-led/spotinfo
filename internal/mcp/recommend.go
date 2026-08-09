package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cast"

	"spotinfo/internal/cloud"
)

// recommend_spot_instances argument keys. They match
// docs/plans/contracts/recommend-spot-instances-v2-input.schema.json, which is
// the normative input contract.
const (
	argCloud        = "cloud"
	argMachine      = "machine"
	argArchitecture = "architecture"
	argOS           = "os"
	argMinMemoryGiB = "min_memory_gib"
	argWorkload     = "workload"
	argTop          = "top"
)

const recommendToolName = "recommend_spot_instances"

// registerRecommendTool advertises the provider-neutral recommendation tool.
// The schema mirrors the published v2 input contract; the MCP host rejects
// values outside each enum before a request reaches the handler.
func (s *Server) registerRecommendTool() {
	tool := mcp.NewTool(recommendToolName,
		mcp.WithDescription("Recommend Spot machines from committed multi-cloud price data. "+
			"Returns a spotinfo.recommend/v2 payload with explicit risk availability per cloud."),
		mcp.WithString(argCloud,
			mcp.Description("Cloud provider to query"),
			mcp.Enum(providerEnum()...),
			mcp.DefaultString(string(cloud.ProviderAWS))),
		mcp.WithArray(argRegions,
			mcp.Description("Provider regions to search. Use ['all'] for every region the provider publishes"),
			mcp.Items(map[string]any{jsonSchemaType: jsonTypeString}),
			mcp.DefaultArray([]string{allRegions})),
		mcp.WithString(argMachine,
			mcp.Description("Machine type RE2 regexp. Omit for no machine-name filter"),
			mcp.DefaultString("")),
		mcp.WithString(argArchitecture,
			mcp.Description("Required processor architecture"),
			mcp.Enum(string(cloud.ArchitectureX8664), string(cloud.ArchitectureARM64)),
			mcp.Required()),
		mcp.WithString(argOS,
			mcp.Description("Machine operating system"),
			mcp.Enum(string(cloud.OSLinux), string(cloud.OSWindows)),
			mcp.DefaultString(string(cloud.OSLinux))),
		mcp.WithInteger(argMinVCPU,
			mcp.Description("Required minimum vCPU cores"),
			mcp.Min(1),
			mcp.Required()),
		mcp.WithNumber(argMinMemoryGiB,
			mcp.Description("Required minimum memory in GiB"),
			mcp.Required()),
		mcp.WithNumber(argMaxPricePerHour,
			mcp.Description("Maximum USD per machine-hour. Omit for no price ceiling")),
		mcp.WithString(argWorkload,
			mcp.Description("Ranking policy. 'cost' ranks by price alone and makes no interruption claim; "+
				"'web', 'ci' and 'batch' cap interruption frequency and need a provider that publishes risk"),
			mcp.Enum(string(cloud.WorkloadCost), string(cloud.WorkloadWeb), string(cloud.WorkloadCI), string(cloud.WorkloadBatch)),
			mcp.DefaultString(string(cloud.WorkloadCost))),
		mcp.WithInteger(argTop,
			mcp.Description("Maximum recommendations to return"),
			mcp.Min(1),
			mcp.Max(cloud.MaxTop),
			mcp.DefaultNumber(cloud.DefaultTop)),
		// The contract declares a closed object. Without this a misspelled
		// argument — `budget` for `max_price_per_hour` — is silently dropped and
		// the answer ignores a ceiling the caller believes it set.
		mcp.WithSchemaAdditionalProperties(false),
	)

	s.mcpServer.AddTool(tool, NewRecommendTool(s.providers, s.logger).Handle)
}

func providerEnum() []string {
	ids := cloud.ProviderIDs()
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, string(id))
	}

	return names
}

// RecommendTool implements the recommend_spot_instances MCP tool.
type RecommendTool struct {
	providers providerRegistry
	logger    *slog.Logger
}

// NewRecommendTool creates a new recommend_spot_instances tool handler.
func NewRecommendTool(providers providerRegistry, logger *slog.Logger) *RecommendTool {
	return &RecommendTool{providers: providers, logger: logger}
}

// Handle answers one recommendation request. Every failure is reported as a
// spotinfo.error/v1 payload with a stable code, and no failure carries partial
// recommendations.
//
//nolint:gocritic // hugeParam: signature dictated by mcp-go's server.ToolHandlerFunc
func (t *RecommendTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.logger.Debug("handling recommend_spot_instances request", slog.Any("arguments", req.Params.Arguments))

	args, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		args = make(map[string]any)
	}

	request, err := parseRecommendRequest(args)
	if err != nil {
		return recommendError(cloud.CodeInvalidArgument, err, rawCloud(args)), nil
	}

	report, err := t.recommend(ctx, request)
	if err != nil {
		t.logger.Debug("recommend_spot_instances failed",
			slog.String(argCloud, string(request.Cloud)), slog.Any("error", err))

		return recommendError(cloud.CodeOf(err), err, rawCloud(args)), nil
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		return recommendError(cloud.CodeInternal, err, rawCloud(args)), nil
	}

	t.logger.Debug("recommend_spot_instances completed",
		slog.String(argCloud, string(request.Cloud)),
		slog.Int("recommendations", len(report.Recommendations)))

	return mcp.NewToolResultText(string(encoded)), nil
}

// recommend resolves the requested provider and runs the neutral engine. The
// registry lookup happens before acquisition, so a disabled cloud costs no I/O
// and never falls back to another provider.
func (t *RecommendTool) recommend(ctx context.Context, request *cloud.RecommendRequest) (*cloud.RecommendReport, error) {
	if t.providers == nil {
		return nil, fmt.Errorf("%w: no provider registry is configured", cloud.ErrDataUnavailable)
	}

	provider, err := t.providers.Get(request.Cloud)
	if err != nil {
		return nil, err
	}

	return cloud.Recommend(ctx, provider, request)
}

// parseRecommendRequest maps tool arguments onto a neutral request, applying
// every documented default. It rejects values outside the neutral vocabulary;
// bounds and combinations are the request's own job to validate.
func parseRecommendRequest(args map[string]any) (*cloud.RecommendRequest, error) {
	provider, err := cloud.ParseProviderID(getStringWithDefault(args, argCloud, string(cloud.ProviderAWS)))
	if err != nil {
		return nil, err
	}

	architecture, err := cloud.ParseArchitecture(cast.ToString(args[argArchitecture]))
	if err != nil {
		return nil, err
	}

	instanceOS, err := cloud.ParseOperatingSystem(getStringWithDefault(args, argOS, string(cloud.OSLinux)))
	if err != nil {
		return nil, err
	}

	workload, err := cloud.ParseWorkload(getStringWithDefault(args, argWorkload, string(cloud.WorkloadCost)))
	if err != nil {
		return nil, err
	}

	maxPrice, err := optionalPrice(args[argMaxPricePerHour])
	if err != nil {
		return nil, err
	}

	top := cast.ToInt(args[argTop])
	if top == 0 {
		top = cloud.DefaultTop
	}

	return &cloud.RecommendRequest{
		MaxPrice:     maxPrice,
		Cloud:        provider,
		Machine:      cast.ToString(args[argMachine]),
		Architecture: architecture,
		OS:           instanceOS,
		Workload:     workload,
		Regions:      requestedRegions(args),
		MinMemoryGiB: cast.ToFloat64(args[argMinMemoryGiB]),
		MinVCPU:      cast.ToInt(args[argMinVCPU]),
		Top:          top,
	}, nil
}

// requestedRegions defaults to every region the provider publishes, matching
// the documented input contract.
func requestedRegions(args map[string]any) []cloud.Region {
	names := getStringSliceWithDefault(args, argRegions, []string{allRegions})

	regions := make([]cloud.Region, 0, len(names))
	for _, name := range names {
		regions = append(regions, cloud.Region(name))
	}

	return regions
}

// optionalPrice converts a price ceiling. An absent argument means no ceiling;
// a present one must be a representable positive amount.
func optionalPrice(value any) (*cloud.Money, error) {
	if value == nil {
		return nil, nil //nolint:nilnil // absence is the mapping for "no ceiling"
	}

	amount, err := cast.ToFloat64E(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %s must be a number", cloud.ErrInvalidArgument, argMaxPricePerHour)
	}
	if amount <= 0 {
		return nil, fmt.Errorf("%w: %s must be positive", cloud.ErrInvalidArgument, argMaxPricePerHour)
	}

	price, err := cloud.MoneyFromFloat(amount)
	if err != nil {
		return nil, err
	}

	return &price, nil
}

// rawCloud echoes the cloud the caller named. A non-string argument cannot be
// echoed, so the error reports a null cloud rather than inventing one.
func rawCloud(args map[string]any) string {
	value, ok := args[argCloud]
	if !ok {
		return string(cloud.ProviderAWS)
	}

	name, isString := value.(string)
	if !isString {
		return ""
	}

	return name
}

// recommendError renders a failure as the published error payload.
//
// The message is the error's own text. Most are domain errors written for a
// caller, but an acquisition failure wraps whatever the provider returned — for
// AWS that can be SDK text carrying an operation name, endpoint and request ID.
// That matches what find_spot_instances has always returned, and the detail is
// what makes a failure actionable; the stable machine-readable part is the code.
func recommendError(code cloud.ErrorCode, err error, cloudValue string) *mcp.CallToolResult {
	report := cloud.NewErrorReport(code, err.Error(), cloudValue)

	encoded, marshalErr := json.Marshal(report)
	if marshalErr != nil {
		return mcp.NewToolResultError(err.Error())
	}

	return mcp.NewToolResultError(string(encoded))
}
