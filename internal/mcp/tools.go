// Package mcp provides MCP tools for spotinfo functionality.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"spotinfo/internal/cloud"
)

// The three tools mirror the CLI and none of them bakes a cloud into its name:
// the cloud is an argument. find_spot_instances, list_spot_regions and
// recommend_spot_instances are retired with the v1 schema they served.
const (
	listToolName      = "list_spot_machines"
	recommendToolName = "recommend_spot_machines"
	regionsToolName   = "list_cloud_regions"
)

// Argument names. Every one is mcpArgName of the CLI flag naming the same
// concept — strip the leading dashes, replace "-" with "_", pluralise the one
// repeatable flag. cmd/spotinfo/mcp_vocabulary_test.go asserts that against the
// vocabulary itself rather than against this list, because mcpArgName lives in
// package main and cannot be imported here.
const (
	argCloud        = "cloud"
	argRegions      = "regions"
	argMachine      = "machine"
	argArchitecture = "architecture"
	argOS           = "os"
	argMinVCPU      = "min_vcpu"
	argMinMemoryGiB = "min_memory_gib"
	argMaxPrice     = "max_price"
	argWorkload     = "workload"
	argTop          = "top"
	argSort         = "sort"
	argOrder        = "order"
	argWithScore    = "with_score"
	argMinScore     = "min_score"
	argAZ           = "az"
	argScoreTimeout = "score_timeout"
	argOffline      = "offline"
	argRefresh      = "refresh"
)

// JSON Schema keywords mcp-go has no helper for. jsonSchemaType and
// jsonTypeString declare an array item's element type; jsonSchemaMinLength
// keeps an empty string out of such an array.
const (
	jsonSchemaType      = "type"
	jsonTypeString      = "string"
	jsonSchemaMinLength = "minLength"
)

// listArgs and regionsArgs are the closed argument sets their tools declare,
// beside recommendArgs in recommend.go. mcp-go checks a request against the
// advertised schema only under server.WithInputSchemaValidation, which reports
// a protocol error rather than the published spotinfo.error/v1 body every tool
// here promises — so each tool enforces its own object.
var listArgs = map[string]struct{}{
	argCloud:        {},
	argRegions:      {},
	argMachine:      {},
	argArchitecture: {},
	argOS:           {},
	argMinVCPU:      {},
	argMinMemoryGiB: {},
	argMaxPrice:     {},
	argSort:         {},
	argOrder:        {},
	argWithScore:    {},
	argMinScore:     {},
	argAZ:           {},
	argScoreTimeout: {},
	argOffline:      {},
	argRefresh:      {},
}

var regionsArgs = map[string]struct{}{argCloud: {}}

// readOnlyToolAnnotations are the hints every tool here carries. All three read
// published price data and nothing else: nothing they do changes state, the
// same request answers the same way, and the data comes from outside this
// process — which is what openWorldHint says.
func readOnlyToolAnnotations() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	}
}

// exclusiveMinimum declares the contract's exclusive lower bound, which mcp-go
// has no helper for. `minimum: 0` is not the same promise: it advertises zero as
// an acceptable memory floor or price ceiling, and the handlers reject both.
func exclusiveMinimum(bound float64) mcp.PropertyOption {
	return func(schema map[string]any) { schema["exclusiveMinimum"] = bound }
}

// providerEnum names every cloud a tool accepts, in stable order.
func providerEnum() []string {
	ids := cloud.ProviderIDs()
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, string(id))
	}

	return names
}

// ListSpotMachinesTool implements the list_spot_machines MCP tool. It answers
// the same spotinfo.list/v1 document `spotinfo list --output json` prints, from
// the same neutral provider: one question, one answer, whichever surface asked.
type ListSpotMachinesTool struct {
	providers providerRegistry
	logger    *slog.Logger
}

// NewListSpotMachinesTool creates a new list_spot_machines tool handler.
func NewListSpotMachinesTool(providers providerRegistry, logger *slog.Logger) *ListSpotMachinesTool {
	return &ListSpotMachinesTool{providers: providers, logger: logger}
}

// registerListTool advertises the browse tool. The schema mirrors `spotinfo
// list`: every flag the command carries, under the name the derivation rule
// gives it, with the same default.
//
//nolint:funlen // one statement per advertised argument; splitting it hides the schema
func (s *Server) registerListTool() {
	tool := mcp.NewTool(listToolName, append(readOnlyToolAnnotations(),
		mcp.WithDescription("List every Spot machine matching a filter, with its price and its risk status. "+
			"Returns a spotinfo.list/v1 payload; a cloud that publishes no interruption figure reports "+
			"that absence rather than a zero."),
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
			mcp.Description("Filter: processor architecture. Omit for no architecture filter"),
			mcp.Enum(string(cloud.ArchitectureX8664), string(cloud.ArchitectureARM64))),
		mcp.WithString(argOS,
			mcp.Description("Machine operating system"),
			mcp.Enum(string(cloud.OSLinux), string(cloud.OSWindows)),
			mcp.DefaultString(string(cloud.OSLinux))),
		mcp.WithInteger(argMinVCPU,
			mcp.Description("Filter: minimum vCPU cores. Omit for no floor"),
			mcp.Min(0)),
		mcp.WithNumber(argMinMemoryGiB,
			mcp.Description("Filter: minimum memory in GiB. Omit for no floor"),
			mcp.Min(0)),
		mcp.WithNumber(argMaxPrice,
			mcp.Description("Maximum USD per machine-hour. Omit for no price ceiling"),
			exclusiveMinimum(0)),
		mcp.WithString(argSort,
			mcp.Description("Sort results by one neutral observation. Omit to keep the order the cloud publishes"),
			mcp.Enum(cloud.SortKeyNames()...)),
		mcp.WithString(argOrder,
			mcp.Description("Sort direction"),
			mcp.Enum(cloud.OrderAsc, cloud.OrderDesc),
			mcp.DefaultString(cloud.OrderAsc)),
		mcp.WithBoolean(argWithScore,
			mcp.Description("Include spot placement scores (experimental)"),
			mcp.DefaultBool(false)),
		mcp.WithInteger(argMinScore,
			mcp.Description("Filter: minimum spot placement score (1-10). Needs with_score, which is what fetches them"),
			mcp.Min(0),
			mcp.Max(cloud.MaxPlacementScore)),
		mcp.WithBoolean(argAZ,
			mcp.Description("Request zone-level scores instead of region-level (use with with_score)"),
			mcp.DefaultBool(false)),
		mcp.WithInteger(argScoreTimeout,
			mcp.Description("Timeout for score enrichment in seconds"),
			mcp.DefaultNumber(cloud.DefaultScoreTimeoutSeconds),
			mcp.Min(1),
			mcp.Max(cloud.MaxScoreTimeoutSeconds)),
		mcp.WithBoolean(argOffline,
			mcp.Description("Answer from the committed snapshots and make no request at all"),
			mcp.DefaultBool(false)),
		mcp.WithBoolean(argRefresh,
			mcp.Description("Ignore any locally cached provider document for this call"),
			mcp.DefaultBool(false)),
		// The advertised object is closed. rejectUnknownArgs is what enforces
		// it, because the host does not validate a request against the schema.
		mcp.WithSchemaAdditionalProperties(false),
	)...)

	s.mcpServer.AddTool(tool, NewListSpotMachinesTool(s.providers, s.logger).Handle)
}

// Handle answers one browse request. Every failure is reported as a
// spotinfo.error/v1 payload with a stable code, and no failure carries
// candidates.
//
//nolint:gocritic // hugeParam: signature dictated by mcp-go's server.ToolHandlerFunc
func (t *ListSpotMachinesTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := argumentsOf(req)
	t.logger.Debug("handling "+listToolName+" request", slog.Any("arguments", args))

	id, query, policy, err := parseListRequest(args)
	if err != nil {
		return toolError(cloud.CodeInvalidArgument, err, rawCloud(args)), nil
	}

	report, err := t.list(ctx, id, query, policy)
	if err != nil {
		t.logger.Debug(listToolName+" failed", slog.String(argCloud, string(id)), slog.Any("error", err))

		return toolError(cloud.CodeOf(err), err, string(id)), nil
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		return toolError(cloud.CodeInternal, err, string(id)), nil
	}

	t.logger.Debug(listToolName+" completed",
		slog.String(argCloud, string(id)), slog.Int("candidates", len(report.Candidates)))

	return mcp.NewToolResultText(string(encoded)), nil
}

// list resolves the requested provider and acquires from it. Both the registry
// lookup and the capability check run before acquisition, so a disabled cloud
// or an unanswerable request costs no I/O and never falls back to another
// provider.
func (t *ListSpotMachinesTool) list(ctx context.Context,
	id cloud.ProviderID, query *cloud.Query, policy cloud.FetchPolicy,
) (*cloud.ListReport, error) {
	provider, err := providerFor(t.providers, id, policy)
	if err != nil {
		return nil, err
	}

	if capErr := provider.Capabilities().Require(query.CapabilityNeeds()); capErr != nil {
		return nil, fmt.Errorf("%s: %w", id, capErr)
	}

	result, err := provider.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	return cloud.NewListReport(query, &result)
}

// parseListRequest maps tool arguments onto the neutral query, applying every
// documented default. It rejects values outside the neutral vocabulary rather
// than coercing them: a dropped filter answers a broader question than the
// caller asked, with status ok.
func parseListRequest(args map[string]any) (cloud.ProviderID, *cloud.Query, cloud.FetchPolicy, error) {
	if err := rejectUnknownArgs(args, listArgs); err != nil {
		return "", nil, cloud.FetchPolicy{}, err
	}

	id, err := requestedCloud(args)
	if err != nil {
		return "", nil, cloud.FetchPolicy{}, err
	}

	query, err := listQuery(args)
	if err != nil {
		return "", nil, cloud.FetchPolicy{}, err
	}

	policy, err := requestedPolicy(args)
	if err != nil {
		return "", nil, cloud.FetchPolicy{}, err
	}

	return id, query, policy, nil
}

// requestedPolicy reads the two data-freshness arguments. They are per-call on
// this surface because one server answers many clients: a snapshot-only
// question from one of them must not put the process into snapshot-only mode
// for the rest. GCP and Azure honour both by construction — they have no live
// path, so there is nothing to skip and nothing to re-fetch.
func requestedPolicy(args map[string]any) (cloud.FetchPolicy, error) {
	offline, err := boolArg(args, argOffline)
	if err != nil {
		return cloud.FetchPolicy{}, err
	}

	refresh, err := boolArg(args, argRefresh)
	if err != nil {
		return cloud.FetchPolicy{}, err
	}

	return cloud.FetchPolicy{Offline: offline, Refresh: refresh}, nil
}

// listQuery reads the filters. The vocabulary is read before the numeric bounds
// so an argument of the wrong type is reported before a value that is merely
// out of range.
func listQuery(args map[string]any) (*cloud.Query, error) {
	query := &cloud.Query{}

	osName, err := stringArg(args, argOS, string(cloud.OSLinux))
	if err != nil {
		return nil, err
	}
	if query.OS, err = cloud.ParseOperatingSystem(osName); err != nil {
		return nil, err
	}

	// Absent means no architecture filter. An explicit value must be one the
	// vocabulary names: reading an unparsable one as "unfiltered" would answer
	// an unfiltered list as though it were filtered.
	if architecture, present := args[argArchitecture]; present && architecture != nil {
		name, isString := architecture.(string)
		if !isString {
			return nil, fmt.Errorf("%w: %s must be a string", cloud.ErrInvalidArgument, argArchitecture)
		}
		if query.Architecture, err = cloud.ParseArchitecture(name); err != nil {
			return nil, err
		}
	}

	if query.MachinePattern, err = stringArg(args, argMachine, ""); err != nil {
		return nil, err
	}
	if query.Regions, err = requestedRegions(args); err != nil {
		return nil, err
	}
	if query.Sort, err = requestedSort(args); err != nil {
		return nil, err
	}

	return listBounds(args, query)
}

// listBounds fills in the numeric filters and the placement request.
func listBounds(args map[string]any, query *cloud.Query) (*cloud.Query, error) {
	minVCPU, err := intArg(args, argMinVCPU, 0)
	if err != nil {
		return nil, err
	}
	if minVCPU < 0 {
		return nil, fmt.Errorf("%w: %s must be zero or a positive number of vCPU cores",
			cloud.ErrInvalidArgument, argMinVCPU)
	}

	minMemoryGiB, _, err := numberArg(args, argMinMemoryGiB)
	if err != nil {
		return nil, err
	}
	if minMemoryGiB < 0 {
		return nil, fmt.Errorf("%w: %s must be zero or a positive number of GiB",
			cloud.ErrInvalidArgument, argMinMemoryGiB)
	}

	maxPrice, err := optionalPrice(args, argMaxPrice)
	if err != nil {
		return nil, err
	}

	placement, err := requestedPlacement(args)
	if err != nil {
		return nil, err
	}

	query.MinVCPU = minVCPU
	query.MinMemoryGiB = minMemoryGiB
	query.MaxPrice = maxPrice
	query.Placement = placement

	return query, nil
}

// requestedPlacement reads the four score arguments.
//
// A minimum score without with_score is refused rather than answered: scores
// are only fetched under with_score, so on its own the floor compares every
// candidate against an absent score and drops all of them — an empty answer
// with status ok, which a caller cannot tell from "nothing matched".
func requestedPlacement(args map[string]any) (cloud.PlacementRequest, error) {
	withScore, err := boolArg(args, argWithScore)
	if err != nil {
		return cloud.PlacementRequest{}, err
	}

	singleZone, err := boolArg(args, argAZ)
	if err != nil {
		return cloud.PlacementRequest{}, err
	}

	minScore, err := intArg(args, argMinScore, 0)
	if err != nil {
		return cloud.PlacementRequest{}, err
	}
	if minScore != 0 && !withScore {
		return cloud.PlacementRequest{}, fmt.Errorf(
			"%w: %s needs %s, which is what fetches the placement scores it filters on",
			cloud.ErrInvalidArgument, argMinScore, argWithScore)
	}
	if minScore < 0 || minScore > cloud.MaxPlacementScore {
		return cloud.PlacementRequest{}, fmt.Errorf("%w: %s must be between 1 and %d",
			cloud.ErrInvalidArgument, argMinScore, cloud.MaxPlacementScore)
	}

	timeout, err := intArg(args, argScoreTimeout, cloud.DefaultScoreTimeoutSeconds)
	if err != nil {
		return cloud.PlacementRequest{}, err
	}
	if timeout < 1 || timeout > cloud.MaxScoreTimeoutSeconds {
		return cloud.PlacementRequest{}, fmt.Errorf("%w: %s must be between 1 and %d seconds",
			cloud.ErrInvalidArgument, argScoreTimeout, cloud.MaxScoreTimeoutSeconds)
	}

	request := cloud.PlacementRequest{MinScore: minScore, SingleZone: singleZone, Enabled: withScore}
	if withScore {
		request.Timeout = time.Duration(timeout) * time.Second
	}

	return request, nil
}

// requestedSort resolves the shared sort vocabulary. An omitted key leaves the
// order to the provider, which is the only honest default across clouds that
// publish different observations; order alone reverses whatever that is.
func requestedSort(args map[string]any) (cloud.SortOrder, error) {
	name, err := stringArg(args, argSort, "")
	if err != nil {
		return cloud.SortOrder{}, err
	}

	key, err := cloud.ParseSortKey(name)
	if err != nil {
		return cloud.SortOrder{}, err
	}

	order, err := stringArg(args, argOrder, cloud.OrderAsc)
	if err != nil {
		return cloud.SortOrder{}, err
	}

	switch order {
	case cloud.OrderAsc:
		return cloud.SortOrder{Key: key}, nil
	case cloud.OrderDesc:
		return cloud.SortOrder{Key: key, Descending: true}, nil
	default:
		return cloud.SortOrder{}, fmt.Errorf("%w: unknown %s %q, want %s or %s",
			cloud.ErrInvalidArgument, argOrder, order, cloud.OrderAsc, cloud.OrderDesc)
	}
}

// ListCloudRegionsTool implements the list_cloud_regions MCP tool.
type ListCloudRegionsTool struct {
	providers providerRegistry
	logger    *slog.Logger
}

// NewListCloudRegionsTool creates a new list_cloud_regions tool handler.
func NewListCloudRegionsTool(providers providerRegistry, logger *slog.Logger) *ListCloudRegionsTool {
	return &ListCloudRegionsTool{providers: providers, logger: logger}
}

// registerRegionsTool advertises the region-enumeration tool. It mirrors no CLI
// command; its one argument is the cloud, under the same name every other tool
// spells it.
func (s *Server) registerRegionsTool() {
	tool := mcp.NewTool(regionsToolName, append(readOnlyToolAnnotations(),
		mcp.WithDescription("List every region a cloud publishes Spot machines in, with the complete list of "+
			"documents that cloud was read from. Returns a spotinfo.regions/v1 payload, which is where the "+
			"sources_omitted count of a list or recommend answer is resolved."),
		mcp.WithString(argCloud,
			mcp.Description("Cloud provider to enumerate"),
			mcp.Enum(providerEnum()...),
			mcp.DefaultString(string(cloud.ProviderAWS))),
		mcp.WithSchemaAdditionalProperties(false),
	)...)

	s.mcpServer.AddTool(tool, NewListCloudRegionsTool(s.providers, s.logger).Handle)
}

// Handle answers one region request.
//
//nolint:gocritic // hugeParam: signature dictated by mcp-go's server.ToolHandlerFunc
func (t *ListCloudRegionsTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := argumentsOf(req)
	t.logger.Debug("handling "+regionsToolName+" request", slog.Any("arguments", args))

	if err := rejectUnknownArgs(args, regionsArgs); err != nil {
		return toolError(cloud.CodeInvalidArgument, err, rawCloud(args)), nil
	}

	id, err := requestedCloud(args)
	if err != nil {
		return toolError(cloud.CodeInvalidArgument, err, rawCloud(args)), nil
	}

	report, err := t.regions(ctx, id)
	if err != nil {
		t.logger.Debug(regionsToolName+" failed", slog.String(argCloud, string(id)), slog.Any("error", err))

		return toolError(cloud.CodeOf(err), err, string(id)), nil
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		return toolError(cloud.CodeInternal, err, string(id)), nil
	}

	t.logger.Debug(regionsToolName+" completed",
		slog.String(argCloud, string(id)), slog.Int("regions", len(report.Regions)))

	return mcp.NewToolResultText(string(encoded)), nil
}

func (t *ListCloudRegionsTool) regions(ctx context.Context, id cloud.ProviderID) (*cloud.RegionsReport, error) {
	// No data-policy argument: enumerating a cloud is not a question about
	// freshness, and the answer is the same from either copy.
	provider, err := providerFor(t.providers, id, cloud.FetchPolicy{})
	if err != nil {
		return nil, err
	}

	regions, result, err := cloud.RegionsOf(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}

	return cloud.NewRegionsReport(&result, regions)
}

// providerFor resolves the requested cloud. A binary composed without a
// registry reports that rather than dereferencing nil: a panic inside a handler
// takes the stdio or SSE process down for every connected client.
func providerFor(registry providerRegistry, id cloud.ProviderID,
	policy cloud.FetchPolicy,
) (cloud.Provider, error) {
	if registry == nil {
		return nil, fmt.Errorf("%w: no provider registry is configured", cloud.ErrDataUnavailable)
	}

	return registry.Get(id, policy)
}

// argumentsOf reads the request's argument object. A request carrying anything
// else is answered as one carrying nothing, which is what every default is for.
//
//nolint:gocritic // hugeParam: signature dictated by mcp-go's server.ToolHandlerFunc
func argumentsOf(req mcp.CallToolRequest) map[string]any {
	args, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		return make(map[string]any)
	}

	return args
}

// toolError renders a failure as the published spotinfo.error/v1 body.
//
// The message is the error's own text. Most are domain errors written for a
// caller, but an acquisition failure wraps whatever the provider returned — for
// AWS that can be SDK text carrying an operation name, endpoint and request ID.
// The detail is what makes a failure actionable; the stable machine-readable
// part is the code.
func toolError(code cloud.ErrorCode, err error, cloudValue string) *mcp.CallToolResult {
	report := cloud.NewErrorReport(code, err.Error(), cloudValue)

	encoded, marshalErr := json.Marshal(report)
	if marshalErr != nil {
		return mcp.NewToolResultError(err.Error())
	}

	return mcp.NewToolResultError(string(encoded))
}

// rejectUnknownArgs fails a request carrying an argument the tool does not
// declare. Dropping one silently answers a different question than the caller
// asked: `budget` in place of `max_price` returns machines with no ceiling
// applied and status ok, and only a client that diffs the echoed request
// against what it sent would ever notice.
func rejectUnknownArgs(args map[string]any, declared map[string]struct{}) error {
	unknown := make([]string, 0, len(args))

	for key := range args {
		if _, ok := declared[key]; !ok {
			unknown = append(unknown, key)
		}
	}

	if len(unknown) == 0 {
		return nil
	}

	// Map iteration order is random; a caller comparing two failures needs the
	// same message for the same request.
	slices.Sort(unknown)

	return fmt.Errorf("%w: unknown argument: %s", cloud.ErrInvalidArgument, strings.Join(unknown, ", "))
}

// requestedCloud resolves the cloud argument, defaulting to AWS.
func requestedCloud(args map[string]any) (cloud.ProviderID, error) {
	name, err := stringArg(args, argCloud, string(cloud.ProviderAWS))
	if err != nil {
		return "", err
	}

	return cloud.ParseProviderID(name)
}

// requestedRegions defaults to every region the provider publishes, matching
// the documented input contract. Only an absent argument takes that default:
// the schema declares an array of at least one non-empty string, and folding an
// explicit `[]` into every region would answer a broader question than the
// caller asked.
//
// The shape is checked here because the host does not check it for us, and cast
// is too permissive to do it: ToStringSlice splits a bare "us-east-1 us-west-2"
// on whitespace into two regions the caller never named, and turns a number or
// a boolean element into a region name.
func requestedRegions(args map[string]any) ([]cloud.Region, error) {
	value, present := args[argRegions]
	if !present || value == nil {
		return []cloud.Region{cloud.RegionAll}, nil
	}

	elements, err := regionElements(value)
	if err != nil {
		return nil, err
	}
	if len(elements) == 0 {
		return nil, fmt.Errorf("%w: %s must name at least one region", cloud.ErrInvalidArgument, argRegions)
	}

	regions := make([]cloud.Region, 0, len(elements))

	for _, element := range elements {
		name, isString := element.(string)
		if !isString || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("%w: every %s element must be a non-empty region name",
				cloud.ErrInvalidArgument, argRegions)
		}

		regions = append(regions, cloud.Region(name))
	}

	return regions, nil
}

// regionElements accepts the two shapes the argument reaches the handler in:
// []any from a decoded JSON request, and []string from an in-process caller.
// Anything else is not the array the contract declares.
func regionElements(value any) ([]any, error) {
	switch typed := value.(type) {
	case []any:
		return typed, nil
	case []string:
		elements := make([]any, 0, len(typed))
		for _, name := range typed {
			elements = append(elements, name)
		}

		return elements, nil
	default:
		return nil, fmt.Errorf("%w: %s must be an array of region names", cloud.ErrInvalidArgument, argRegions)
	}
}

// stringArg reads a string argument. The MCP server does not check a request
// against the advertised schema, so a caller that wraps a value in an array or
// sends a number reaches the handler; coercing that to "" would answer a
// question the caller did not ask — silently, and with status ok.
//
// Only an absent or null argument takes the documented default. An explicit ""
// is a value none of the enums list, so it is passed on to the vocabulary
// parser and refused there: folding it into the default would answer
// `cloud: ""` as AWS and `workload: ""` as cost. `machine` is the one argument
// whose contract gives "" a meaning — no machine-name filter — and its
// documented default is "" already.
func stringArg(args map[string]any, key, defaultValue string) (string, error) {
	value, present := args[key]
	if !present || value == nil {
		return defaultValue, nil
	}

	text, isString := value.(string)
	if !isString {
		return "", fmt.Errorf("%w: %s must be a string", cloud.ErrInvalidArgument, key)
	}

	return text, nil
}

// boolArg reads a boolean argument. An absent argument is false; a present one
// must be a boolean, so a mistyped flag is reported rather than read as off.
func boolArg(args map[string]any, key string) (bool, error) {
	value, present := args[key]
	if !present || value == nil {
		return false, nil
	}

	flag, isBool := value.(bool)
	if !isBool {
		return false, fmt.Errorf("%w: %s must be a boolean", cloud.ErrInvalidArgument, key)
	}

	return flag, nil
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
// a present one must be a positive amount.
//
// A ceiling is truncated to the fixed-point scale rather than rejected for
// precision: a client that divides a monthly budget by 720 sends
// 0.041666666666666664, and a budget a caller can express must not become an
// invalid argument. NaN and out-of-range amounts still fail — those are not
// ceilings.
func optionalPrice(args map[string]any, key string) (*cloud.Money, error) {
	amount, present, err := numberArg(args, key)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil //nolint:nilnil // absence is the mapping for "no ceiling"
	}

	if amount <= 0 {
		return nil, fmt.Errorf("%w: %s must be positive", cloud.ErrInvalidArgument, key)
	}

	price, err := cloud.MoneyCeilingFromFloat(amount)
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
