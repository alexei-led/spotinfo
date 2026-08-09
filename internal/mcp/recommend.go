package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

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
	cloudName, err := stringArg(args, argCloud, string(cloud.ProviderAWS))
	if err != nil {
		return nil, err
	}

	provider, err := cloud.ParseProviderID(cloudName)
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
	maxPrice, err := optionalPrice(args, argMaxPricePerHour)
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

	request.MaxPrice = maxPrice
	request.MinMemoryGiB = minMemoryGiB
	request.MinVCPU = minVCPU
	request.Top = top

	return request, nil
}

// requestedRegions defaults to every region the provider publishes, matching
// the documented input contract. Every element must be a region name: a nested
// array or object cannot be one, and silently searching everywhere instead
// would answer a narrower question than the caller asked.
func requestedRegions(args map[string]any) ([]cloud.Region, error) {
	value, present := args[argRegions]
	if !present || value == nil {
		return []cloud.Region{allRegions}, nil
	}

	names, err := cast.ToStringSliceE(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %s must be an array of region names", cloud.ErrInvalidArgument, argRegions)
	}
	if len(names) == 0 {
		return []cloud.Region{allRegions}, nil
	}

	regions := make([]cloud.Region, 0, len(names))
	for _, name := range names {
		regions = append(regions, cloud.Region(name))
	}

	return regions, nil
}

// stringArg reads a string argument. The MCP server does not check a request
// against the advertised schema, so a caller that wraps a value in an array or
// sends a number reaches the handler; coercing that to "" would answer a
// question the caller did not ask — silently, and with status ok.
func stringArg(args map[string]any, key, defaultValue string) (string, error) {
	value, present := args[key]
	if !present || value == nil {
		return defaultValue, nil
	}

	text, isString := value.(string)
	if !isString {
		return "", fmt.Errorf("%w: %s must be a string", cloud.ErrInvalidArgument, key)
	}
	if text == "" {
		return defaultValue, nil
	}

	return text, nil
}

// numberArg reads a numeric argument, reporting whether it was present. A
// number quoted as a string is accepted because callers routinely send one;
// everything else is refused rather than coerced. cast would turn true into 1
// and a misspelled number into the zero that means "argument omitted", so the
// caller would be answered against a default it never asked for.
func numberArg(args map[string]any, key string) (float64, bool, error) {
	value, present := args[key]
	if !present || value == nil {
		return 0, false, nil
	}

	switch number := value.(type) {
	case float64:
		return number, true, nil
	case int:
		return float64(number), true, nil
	case json.Number:
		if amount, err := number.Float64(); err == nil {
			return amount, true, nil
		}
	case string:
		if amount, err := strconv.ParseFloat(number, 64); err == nil {
			return amount, true, nil
		}
	}

	return 0, false, fmt.Errorf("%w: %s must be a number", cloud.ErrInvalidArgument, key)
}

// intArg reads a whole-number argument. An absent argument takes the documented
// default; a present one must be an integer, so an unusable value is reported
// rather than replaced by that default.
func intArg(args map[string]any, key string, defaultValue int) (int, error) {
	number, present, err := numberArg(args, key)
	if err != nil {
		return 0, err
	}
	if !present {
		return defaultValue, nil
	}

	whole := int(number)
	if float64(whole) != number {
		return 0, fmt.Errorf("%w: %s must be a whole number", cloud.ErrInvalidArgument, key)
	}

	return whole, nil
}

// optionalPrice converts a price ceiling. An absent argument means no ceiling;
// a present one must be a representable positive amount.
func optionalPrice(args map[string]any, key string) (*cloud.Money, error) {
	amount, present, err := numberArg(args, key)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil //nolint:nilnil // absence is the mapping for "no ceiling"
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
