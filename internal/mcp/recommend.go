package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"

	"spotinfo/internal/cloud"
)

// recommendArgs is the closed set of arguments the v3 input contract declares
// (docs/plans/contracts/recommend-v3-input.schema.json, the normative input
// contract). rejectUnknownArgs enforces it; see the note beside listArgs for
// why the host does not.
var recommendArgs = map[string]struct{}{
	argCloud:        {},
	argRegions:      {},
	argMachine:      {},
	argArchitecture: {},
	argOS:           {},
	argMinVCPU:      {},
	argMinMemoryGiB: {},
	argMaxPrice:     {},
	argWorkload:     {},
	argTop:          {},
	argOffline:      {},
	argRefresh:      {},
}

// registerRecommendTool advertises the provider-neutral recommendation tool.
// The schema mirrors the published v3 input contract; the MCP host rejects
// values outside each enum before a request reaches the handler.
//
//nolint:funlen // one statement per advertised argument; splitting it hides the schema
func (s *Server) registerRecommendTool() {
	tool := mcp.NewTool(recommendToolName, append(readOnlyToolAnnotations(),
		mcp.WithDescription("Recommend Spot machines from committed multi-cloud price data. "+
			"Returns a spotinfo.recommend/v3 payload with explicit risk availability per cloud."),
		mcp.WithString(argCloud,
			mcp.Description("Cloud provider to query"),
			mcp.Enum(providerEnum()...),
			mcp.DefaultString(string(cloud.ProviderAWS))),
		mcp.WithArray(argRegions,
			mcp.Description("Provider regions to search. Use ['all'] for every region the provider publishes"),
			mcp.Items(map[string]any{jsonSchemaType: jsonTypeString, jsonSchemaMinLength: 1}),
			mcp.MinItems(1),
			mcp.UniqueItems(true),
			mcp.DefaultArray([]string{string(cloud.RegionAll)})),
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
			exclusiveMinimum(0),
			mcp.Required()),
		mcp.WithNumber(argMaxPrice,
			mcp.Description("Maximum USD per machine-hour. Omit for no price ceiling"),
			exclusiveMinimum(0)),
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
		mcp.WithBoolean(argOffline,
			mcp.Description("Answer from the committed snapshots and make no request at all"),
			mcp.DefaultBool(false)),
		mcp.WithBoolean(argRefresh,
			mcp.Description("Ignore any locally cached provider document for this call"),
			mcp.DefaultBool(false)),
		// The contract declares a closed object. This advertises it to the
		// client; rejectUnknownArgs is what enforces it, because the host does
		// not validate a request against the schema for us.
		mcp.WithSchemaAdditionalProperties(false),
	)...)

	s.mcpServer.AddTool(tool, NewRecommendTool(s.providers, s.logger).Handle)
}

// RecommendTool implements the recommend_spot_machines MCP tool.
type RecommendTool struct {
	providers providerRegistry
	logger    *slog.Logger
}

// NewRecommendTool creates a new recommend_spot_machines tool handler.
func NewRecommendTool(providers providerRegistry, logger *slog.Logger) *RecommendTool {
	return &RecommendTool{providers: providers, logger: logger}
}

// Handle answers one recommendation request. Every failure is reported as a
// spotinfo.error/v1 payload with a stable code, and no failure carries partial
// recommendations.
//
//nolint:gocritic // hugeParam: signature dictated by mcp-go's server.ToolHandlerFunc
func (t *RecommendTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := argumentsOf(req)
	t.logger.Debug("handling "+recommendToolName+" request", slog.Any("arguments", args))

	request, err := parseRecommendRequest(args)
	if err != nil {
		return toolError(cloud.CodeInvalidArgument, err, rawCloud(args)), nil
	}

	policy, err := requestedPolicy(args)
	if err != nil {
		return toolError(cloud.CodeInvalidArgument, err, rawCloud(args)), nil
	}

	report, err := t.recommend(ctx, request, policy)
	if err != nil {
		t.logger.Debug(recommendToolName+" failed",
			slog.String(argCloud, string(request.Cloud)), slog.Any("error", err))

		return toolError(cloud.CodeOf(err), err, rawCloud(args)), nil
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		return toolError(cloud.CodeInternal, err, rawCloud(args)), nil
	}

	t.logger.Debug(recommendToolName+" completed",
		slog.String(argCloud, string(request.Cloud)),
		slog.Int("recommendations", len(report.Recommendations)))

	return mcp.NewToolResultText(string(encoded)), nil
}

// recommend validates the request, resolves the requested provider and runs the
// neutral engine. The registry lookup happens before acquisition, so a disabled
// cloud costs no I/O and never falls back to another provider.
func (t *RecommendTool) recommend(ctx context.Context,
	request *cloud.RecommendRequest, policy cloud.FetchPolicy,
) (*cloud.RecommendReport, error) {
	// Every bound is checked before the provider is resolved. The other way
	// round, a malformed request aimed at a disabled cloud reports
	// DATA_UNAVAILABLE and hides the argument the caller got wrong — and the
	// same request would change code the day that snapshot is enabled.
	// cloud.Recommend validates again for callers that reach it directly.
	if err := request.Validate(); err != nil {
		return nil, err
	}

	provider, err := providerFor(t.providers, request.Cloud, policy)
	if err != nil {
		return nil, err
	}

	return cloud.Recommend(ctx, provider, request)
}

// parseRecommendRequest maps tool arguments onto a neutral request, applying
// every documented default. It rejects values outside the neutral vocabulary;
// bounds and combinations are the request's own job to validate.
func parseRecommendRequest(args map[string]any) (*cloud.RecommendRequest, error) {
	if err := rejectUnknownArgs(args, recommendArgs); err != nil {
		return nil, err
	}

	provider, err := requestedCloud(args)
	if err != nil {
		return nil, err
	}

	architectureName, err := stringArg(args, argArchitecture, "")
	if err != nil {
		return nil, err
	}

	architecture, err := cloud.ParseArchitecture(architectureName)
	if err != nil {
		return nil, err
	}

	osName, err := stringArg(args, argOS, string(cloud.OSLinux))
	if err != nil {
		return nil, err
	}

	instanceOS, err := cloud.ParseOperatingSystem(osName)
	if err != nil {
		return nil, err
	}

	workloadName, err := stringArg(args, argWorkload, string(cloud.WorkloadCost))
	if err != nil {
		return nil, err
	}

	workload, err := cloud.ParseWorkload(workloadName)
	if err != nil {
		return nil, err
	}

	machine, err := stringArg(args, argMachine, "")
	if err != nil {
		return nil, err
	}

	regions, err := requestedRegions(args)
	if err != nil {
		return nil, err
	}

	return recommendConstraints(args, &cloud.RecommendRequest{
		Cloud:        provider,
		Machine:      machine,
		Architecture: architecture,
		OS:           instanceOS,
		Workload:     workload,
		Regions:      regions,
	})
}

// recommendConstraints fills in the numeric bounds. They are read after the
// vocabulary so an argument of the wrong type is reported before a value that
// is merely out of range.
func recommendConstraints(args map[string]any, request *cloud.RecommendRequest) (*cloud.RecommendRequest, error) {
	maxPrice, err := optionalPrice(args, argMaxPrice)
	if err != nil {
		return nil, err
	}

	minMemoryGiB, _, err := numberArg(args, argMinMemoryGiB)
	if err != nil {
		return nil, err
	}

	minVCPU, err := intArg(args, argMinVCPU, 0)
	if err != nil {
		return nil, err
	}

	top, err := intArg(args, argTop, cloud.DefaultTop)
	if err != nil {
		return nil, err
	}

	// The published input schema declares "maximum": 50, and the host does not
	// validate a request against the schema for us — the same reason
	// rejectUnknownArgs exists. The neutral request only requires a positive Top,
	// because the CLI has no upper bound; this is the MCP surface honouring what
	// it advertises.
	if top > cloud.MaxTop {
		return nil, fmt.Errorf("%w: %s must be between 1 and %d", cloud.ErrInvalidArgument, argTop, cloud.MaxTop)
	}

	request.MaxPrice = maxPrice
	request.MinMemoryGiB = minMemoryGiB
	request.MinVCPU = minVCPU
	request.Top = top

	return request, nil
}
