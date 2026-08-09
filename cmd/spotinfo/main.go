// Package main provides the CLI application for spotinfo.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	"spotinfo/internal/mcp"
	"spotinfo/internal/providers"
	awsprovider "spotinfo/internal/providers/aws"
	azureprovider "spotinfo/internal/providers/azure"
	gcpprovider "spotinfo/internal/providers/gcp"
	"spotinfo/internal/spot"
)

var (
	// main context
	mainCtx context.Context
	// logger instance
	log *slog.Logger
	// Version contains the current version.
	Version = "dev"
	// BuildDate contains a string with the build date.
	BuildDate = unknownBuildValue
	// GitCommit git commit SHA
	GitCommit = "dirty"
	// GitBranch git branch
	GitBranch = "master"
	// GitHubRelease indicates if this is a GitHub release build
	GitHubRelease = ""
)

const (
	// Table column headers
	regionColumn        = "Region"
	instanceTypeColumn  = "Instance Info"
	vCPUColumn          = "vCPU"
	memoryColumn        = "Memory GiB"
	savingsColumn       = "Savings over On-Demand"
	interruptionColumn  = "Frequency of interruption"
	priceColumn         = "USD/Hour"
	priceSourceColumn   = "Price Source"
	scoreColumn         = "Score"
	scoreHeaderAZ       = "Placement Score (AZ)"
	scoreHeaderRegional = "Placement Score (Regional)"
	scoreHeaderGeneric  = "Placement Score"

	// Sort types
	sortType         = "type"
	sortInterruption = "interruption"
	sortSavings      = "savings"
	sortPrice        = "price"
	sortRegion       = "region"
	sortScore        = "score"

	// Score thresholds
	excellentScoreThreshold = 8 // Scores 8-10 are excellent
	moderateScoreThreshold  = 5 // Scores 5-7 are moderate
	poorScoreThreshold      = 1 // Scores 1-4 are poor

	// Build constants
	unknownBuildValue = "unknown"

	// MCP mode constants
	mcpModeEnv      = "SPOTINFO_MODE"
	mcpTransportEnv = "MCP_TRANSPORT"
	mcpPortEnv      = "MCP_PORT"
	mcpModeValue    = "mcp"
	stdioTransport  = "stdio"
	sseTransport    = "sse"
	defaultMCPPort  = "8080"

	// Output formats
	outputNumber = "number"
	outputText   = "text"
	outputJSON   = "json"
	outputTable  = "table"
	outputCSV    = "csv"

	// CLI flag names, shared by the flag definitions and the value lookups
	flagMCP          = "mcp"
	flagDebug        = "debug"
	flagQuiet        = "quiet"
	flagJSONLog      = "json-log"
	flagType         = "type"
	flagOS           = "os"
	flagRegion       = "region"
	flagOutput       = "output"
	flagCPU          = "cpu"
	flagMemory       = "memory"
	flagPrice        = "price"
	flagSort         = "sort"
	flagOrder        = "order"
	flagWithScore    = "with-score"
	flagMinScore     = "min-score"
	flagAZ           = "az"
	flagScoreTimeout = "score-timeout"
	flagArchitecture = "architecture"
	flagInstance     = "instance"
	flagBudget       = "budget"
	flagWorkload     = "workload"
	flagTop          = "top"

	// Sort order values
	orderAsc  = "asc"
	orderDesc = "desc"

	// defaultInstanceOS is the --os flag default
	defaultInstanceOS = "linux"

	// allRegions is the --region value selecting every published region.
	allRegions = "all"

	// appName is the CLI application name.
	appName = "spotinfo"

	recommendCommandName = "recommend"
	regionFlagUsage      = "set one or more provider regions, use \"all\" for all published regions"
)

//nolint:cyclop
func mainCmd(ctx *cli.Context) error {
	// Check for MCP mode before running CLI
	if isMCPMode(ctx) {
		return runMCPServer(ctx, mainCtx)
	}

	client := spot.New()

	registry, err := newProviderRegistry(client)
	if err != nil {
		return err
	}

	return execMainCmd(ctx, mainCtx, registry, client, os.Stdout)
}

// newProviderRegistry composes the providers this binary serves. The AWS
// client is shared with the legacy acquisition path so a single invocation
// opens one client. A provider whose snapshot fails its gates is reported
// disabled with its reason, never substituted by another cloud.
func newProviderRegistry(client *spot.Client) (*providers.Registry, error) {
	registry, err := providers.New(
		providers.Registration{
			ID: cloud.ProviderAWS,
			Build: func() (cloud.Provider, error) {
				lookup, err := spot.LoadEmbeddedArchitectureLookup()
				if err != nil {
					return nil, fmt.Errorf("load aws architecture data: %w", err)
				}

				return awsprovider.New(client, lookup)
			},
		},
		providers.Registration{
			ID:    cloud.ProviderGCP,
			Build: func() (cloud.Provider, error) { return gcpprovider.New() },
		},
		providers.Registration{
			ID:    cloud.ProviderAzure,
			Build: func() (cloud.Provider, error) { return azureprovider.New() },
		},
	)
	if err != nil {
		return nil, err
	}

	// Why a cloud is missing is a --debug question, not an error: the request
	// that selects it reports the same reason code on stderr.
	for _, status := range registry.Status() {
		if !status.Enabled {
			log.Debug("cloud provider unavailable",
				slog.String("provider", string(status.ID)),
				slog.String("reason", string(status.Reason)),
				slog.String("detail", status.Detail))
		}
	}

	return registry, nil
}

// isMCPMode checks if the application should run in MCP server mode
func isMCPMode(ctx *cli.Context) bool {
	// Check CLI flag first
	if ctx.Bool(flagMCP) {
		return true
	}

	// Check environment variable
	if mode, exists := os.LookupEnv(mcpModeEnv); exists && strings.EqualFold(mode, mcpModeValue) {
		return true
	}

	return false
}

// runMCPServer starts the MCP server
func runMCPServer(_ *cli.Context, execCtx context.Context) error {
	log.Info("starting MCP server mode")

	// Get transport mode
	transport := getMCPTransport()
	port := getMCPPort()

	log.Info("MCP server configuration",
		slog.String("transport", transport),
		slog.String("port", port))

	client := spot.New()

	registry, err := newProviderRegistry(client)
	if err != nil {
		return err
	}

	// Create MCP server
	mcpServer, err := mcp.NewServer(mcp.Config{
		Version:   Version,
		Transport: transport,
		Port:      port,
		Logger:    log,
		Providers: registry,
	})
	if err != nil {
		return fmt.Errorf("failed to create MCP server: %w", err)
	}

	// Start server based on transport
	switch transport {
	case stdioTransport:
		return mcpServer.ServeStdio(execCtx)
	case sseTransport:
		return mcpServer.ServeSSE(execCtx, port)
	default:
		return fmt.Errorf("unsupported transport: %s", transport)
	}
}

// getMCPTransport returns the configured MCP transport mode
func getMCPTransport() string {
	if transport, exists := os.LookupEnv(mcpTransportEnv); exists && transport != "" {
		return transport
	}
	return stdioTransport // default
}

// getMCPPort returns the configured MCP port for SSE transport
func getMCPPort() string {
	if port, exists := os.LookupEnv(mcpPortEnv); exists && port != "" {
		return port
	}
	return defaultMCPPort
}

type spotClient interface {
	GetSpotSavings(ctx context.Context, opts ...spot.GetSpotSavingsOption) ([]spot.Advice, error)
}

// flagLineageContext returns the nearest context that explicitly set one of
// names. LocalFlagNames reports only flags a context actually parsed, so this
// preserves root flags placed before a subcommand without letting the
// subcommand's own defaults shadow them.
func flagLineageContext(ctx *cli.Context, names ...string) *cli.Context {
	for _, candidate := range ctx.Lineage() {
		for _, setName := range candidate.LocalFlagNames() {
			for _, name := range names {
				if setName == name {
					return candidate
				}
			}
		}
	}

	return ctx
}

func lineageString(ctx *cli.Context, name string) string {
	return flagLineageContext(ctx, name).String(name)
}

func lineageStringSlice(ctx *cli.Context, name string) []string {
	return flagLineageContext(ctx, name).StringSlice(name)
}

// lineageIsSet reports whether a flag was given on any context in the lineage,
// as opposed to carrying its declared default.
func lineageIsSet(ctx *cli.Context, name string) bool {
	for _, candidate := range ctx.Lineage() {
		if slices.Contains(candidate.LocalFlagNames(), name) {
			return true
		}
	}

	return false
}

func lineageInt(ctx *cli.Context, name string, aliases ...string) int {
	return flagLineageContext(ctx, append([]string{name}, aliases...)...).Int(name)
}

// execMainCmd is the testable version of mainCmd that accepts dependencies.
//
//nolint:cyclop,gocyclo,funlen // CLI argument parsing inherently has high complexity due to comprehensive option handling
func execMainCmd(ctx *cli.Context, execCtx context.Context, registry providerRegistry, client spotClient, output io.Writer) error {
	if v := execCtx.Value("key"); v != nil {
		log.Debug("context value received", slog.Any("value", v))
	}

	// Provider selection and capability checks run before acquisition, so an
	// unsupported request costs no I/O.
	if providerErr := resolveAWSProvider(ctx, registry, rootCapabilityRequest(ctx)); providerErr != nil {
		return providerErr
	}

	regions := ctx.StringSlice(flagRegion)
	instanceOS := ctx.String(flagOS)
	instance := ctx.String(flagType)
	cpu := ctx.Int(flagCPU)
	memory := ctx.Int(flagMemory)
	maxPrice := ctx.Float64(flagPrice)
	sortBy := ctx.String(flagSort)
	order := ctx.String(flagOrder)
	sortDesc := strings.EqualFold(order, orderDesc)
	withScore := ctx.Bool(flagWithScore)
	minScore := ctx.Int(flagMinScore)
	azLevel := ctx.Bool(flagAZ)
	scoreTimeout := ctx.Int(flagScoreTimeout)

	// ParseFloat accepts NaN and ±Inf, and neither is caught by the `> 0` test
	// below that decides whether to append the filter: NaN fails every
	// comparison and -Inf is not positive, so both drop the ceiling and answer
	// an unfiltered query as if it were filtered. +Inf would be stored raw and
	// compared against in the client, making the filter a silent no-op.
	if ctx.IsSet(flagPrice) && (math.IsNaN(maxPrice) || math.IsInf(maxPrice, 0) || maxPrice <= 0) {
		return fmt.Errorf("%w: price must be a positive USD instance-hour price", cloud.ErrInvalidArgument)
	}

	var sortByType spot.SortBy

	switch sortBy {
	case sortType:
		sortByType = spot.SortByInstance
	case sortInterruption:
		sortByType = spot.SortByRange
	case sortSavings:
		sortByType = spot.SortBySavings
	case sortPrice:
		sortByType = spot.SortByPrice
	case sortRegion:
		sortByType = spot.SortByRegion
	case sortScore:
		sortByType = spot.SortByScore
	default:
		sortByType = spot.SortByRange
	}

	// build options
	var opts []spot.GetSpotSavingsOption
	opts = append(opts, spot.WithRegions(regions))
	if instance != "" {
		opts = append(opts, spot.WithPattern(instance))
	}
	opts = append(opts, spot.WithOS(instanceOS))
	if cpu > 0 {
		opts = append(opts, spot.WithCPU(cpu))
	}
	if memory > 0 {
		opts = append(opts, spot.WithMemory(memory))
	}
	if maxPrice > 0 {
		opts = append(opts, spot.WithMaxPrice(maxPrice))
	}
	opts = append(opts, spot.WithSort(sortByType, sortDesc))
	if withScore {
		opts = append(opts, spot.WithScores(true), spot.WithSingleAvailabilityZone(azLevel))
		if scoreTimeout > 0 {
			opts = append(opts, spot.WithScoreTimeout(time.Duration(scoreTimeout)*time.Second))
		}
	}
	if minScore > 0 {
		opts = append(opts, spot.WithMinScore(minScore))
	}

	// get spot savings
	advices, err := client.GetSpotSavings(execCtx, opts...)
	if err != nil {
		return fmt.Errorf("failed to get spot savings: %w", err)
	}

	// decide if region should be printed
	printRegion := len(regions) > 1 || (len(regions) == 1 && regions[0] == allRegions)

	switch ctx.String(flagOutput) {
	case outputNumber:
		printAdvicesNumber(advices, printRegion, output)
	case outputText:
		printAdvicesText(advices, printRegion, output)
	case outputJSON:
		printAdvicesJSON(advices, output)
	case outputTable:
		printAdvicesTable(advices, false, printRegion, output)
	case outputCSV:
		printAdvicesTable(advices, true, printRegion, output)
	default:
		printAdvicesNumber(advices, printRegion, output)
	}

	return nil
}

func printAdvicesText(advices []spot.Advice, region bool, output io.Writer) {
	for _, advice := range advices {
		scoreStr := ""
		if advice.RegionScore != nil || len(advice.ZoneScores) > 0 {
			scoreStr = fmt.Sprintf(", score=%s", getScoreDisplayValue(&advice))
		}

		priceStr := formatPrice(advice.Price, advice.LivePrice)

		if region {
			fmt.Fprintf(output, "region=%s, type=%s, vCPU=%d, memory=%vGiB, saving=%d%%, interruption='%s', price=%s%s\n", //nolint:errcheck
				advice.Region, advice.Instance, advice.Info.Cores, advice.Info.RAM, advice.Savings, advice.Range.Label, priceStr, scoreStr)
		} else {
			fmt.Fprintf(output, "type=%s, vCPU=%d, memory=%vGiB, saving=%d%%, interruption='%s', price=%s%s\n", //nolint:errcheck
				advice.Instance, advice.Info.Cores, advice.Info.RAM, advice.Savings, advice.Range.Label, priceStr, scoreStr)
		}
	}
}

// formatPrice formats a price value with an asterisk suffix for live-fetched prices.
func formatPrice(price float64, live bool) string {
	s := fmt.Sprintf("%.4f", price)
	if live {
		return s + "*"
	}
	return s
}

func printAdvicesNumber(advices []spot.Advice, region bool, output io.Writer) {
	if len(advices) == 1 {
		fmt.Fprintln(output, advices[0].Savings) //nolint:errcheck,gosec // G602: false positive, length checked above
		return
	}

	for _, advice := range advices {
		if region {
			fmt.Fprintf(output, "%s/%s: %d\n", advice.Region, advice.Instance, advice.Savings) //nolint:errcheck
		} else {
			fmt.Fprintf(output, "%s: %d\n", advice.Instance, advice.Savings) //nolint:errcheck
		}
	}
}

// getScoreIndicator returns an emoji indicator based on the score value.
func getScoreIndicator(score int) string {
	switch {
	case score >= excellentScoreThreshold:
		return "🟢" // Excellent (8-10)
	case score >= moderateScoreThreshold:
		return "🟡" // Moderate (5-7)
	case score >= poorScoreThreshold:
		return "🔴" // Poor (1-4)
	default:
		return "❓" // Unknown
	}
}

// formatScoreWithIndicator formats a score with its visual indicator.
func formatScoreWithIndicator(score int) string {
	return fmt.Sprintf("%s %d", getScoreIndicator(score), score)
}

// getScoreDataValue returns raw score data without visual formatting.
func getScoreDataValue(advice *spot.Advice) string {
	if advice.RegionScore != nil {
		score := fmt.Sprintf("%d", *advice.RegionScore)
		return addFreshnessInfo(score, advice.ScoreFetchedAt)
	}
	if len(advice.ZoneScores) > 0 {
		var scores []string
		for zone, score := range advice.ZoneScores {
			scoreStr := fmt.Sprintf("%d", score)
			scoreWithFreshness := addFreshnessInfo(scoreStr, advice.ScoreFetchedAt)
			scores = append(scores, fmt.Sprintf("%s:%s", zone, scoreWithFreshness))
		}
		return strings.Join(scores, ",")
	}
	return "-"
}

// getScoreDisplayValue returns formatted score with visual indicators for table display.
func getScoreDisplayValue(advice *spot.Advice) string {
	if advice.RegionScore != nil {
		scoreStr := formatScoreWithIndicator(*advice.RegionScore)
		return addFreshnessInfo(scoreStr, advice.ScoreFetchedAt)
	}
	if len(advice.ZoneScores) > 0 {
		var scores []string
		for zone, score := range advice.ZoneScores {
			scoreStr := formatScoreWithIndicator(score)
			scoreWithFreshness := addFreshnessInfo(scoreStr, advice.ScoreFetchedAt)
			scores = append(scores, fmt.Sprintf("%s:%s", zone, scoreWithFreshness))
		}
		return strings.Join(scores, ",")
	}
	return "-"
}

// addFreshnessInfo adds subtle freshness indicator to score display.
func addFreshnessInfo(scoreStr string, fetchedAt *time.Time) string {
	if fetchedAt == nil {
		return scoreStr
	}

	age := time.Since(*fetchedAt)
	if age > 30*time.Minute {
		// Only show indicator for stale data
		return scoreStr + "*"
	}
	return scoreStr
}

func printAdvicesJSON(advices any, output io.Writer) {
	bytes, err := json.MarshalIndent(advices, "", "  ")
	if err != nil {
		// Reachable in principle: json.Marshal rejects non-finite floats, and
		// prices come from an upstream feed. Prices are screened at parse time,
		// so this is a backstop — but a CLI should report it, not panic with a
		// stack trace.
		slog.Error("failed to render JSON output", slog.Any("error", err))

		return
	}

	txt := string(bytes)
	txt = strings.ReplaceAll(txt, "\\u003c", "<")
	txt = strings.ReplaceAll(txt, "\\u003e", ">")
	fmt.Fprintln(output, txt) //nolint:errcheck
}

// scoreTypeInfo holds information about score types present in advices.
type scoreTypeInfo struct {
	hasScores         bool
	hasRegionalScores bool
	hasAZScores       bool
}

// analyzeScoreTypes checks what types of scores are present in the advices.
func analyzeScoreTypes(advices []spot.Advice) scoreTypeInfo {
	info := scoreTypeInfo{}
	for _, advice := range advices {
		if advice.RegionScore != nil {
			info.hasScores = true
			info.hasRegionalScores = true
		}
		if len(advice.ZoneScores) > 0 {
			info.hasScores = true
			info.hasAZScores = true
		}
	}
	return info
}

// determineScoreHeader returns the appropriate score column header based on score types.
func determineScoreHeader(info scoreTypeInfo) string {
	if !info.hasScores {
		return scoreColumn
	}
	if info.hasAZScores && !info.hasRegionalScores {
		return scoreHeaderAZ
	}
	if info.hasRegionalScores && !info.hasAZScores {
		return scoreHeaderRegional
	}
	return scoreHeaderGeneric
}

// buildTableHeader creates the table header row.
func buildTableHeader(scoreInfo scoreTypeInfo, region, csv bool) table.Row {
	header := table.Row{instanceTypeColumn, vCPUColumn, memoryColumn, savingsColumn, interruptionColumn, priceColumn}
	if csv {
		header = append(header, priceSourceColumn)
	}
	if scoreInfo.hasScores {
		header = append(header, determineScoreHeader(scoreInfo))
	}
	if region {
		header = append(table.Row{regionColumn}, header...)
	}
	return header
}

// tableRowOptions configures how table rows are formatted.
type tableRowOptions struct {
	includeVisualFormatting bool
}

// TableRowOption defines a function type for configuring table row formatting.
type TableRowOption func(*tableRowOptions)

// WithVisualFormatting enables emoji indicators in table output.
func WithVisualFormatting() TableRowOption {
	return func(opts *tableRowOptions) {
		opts.includeVisualFormatting = true
	}
}

// buildTableRow creates a table row for an advice with configurable formatting.
func buildTableRow(advice *spot.Advice, scoreInfo scoreTypeInfo, region, csv bool, options ...TableRowOption) table.Row {
	opts := &tableRowOptions{}
	for _, opt := range options {
		opt(opts)
	}

	var priceValue any
	if csv {
		priceValue = advice.Price
	} else {
		priceValue = formatPrice(advice.Price, advice.LivePrice)
	}
	row := table.Row{advice.Instance, advice.Info.Cores, advice.Info.RAM, advice.Savings, advice.Range.Label, priceValue}
	if csv {
		row = append(row, priceSource(advice.LivePrice))
	}
	if scoreInfo.hasScores {
		var scoreValue string
		if opts.includeVisualFormatting {
			scoreValue = getScoreDisplayValue(advice)
		} else {
			scoreValue = getScoreDataValue(advice)
		}
		row = append(row, scoreValue)
	}
	if region {
		row = append(table.Row{advice.Region}, row...)
	}
	return row
}

// priceSource returns a human-readable label for the price data source.
func priceSource(live bool) string {
	if live {
		return "live"
	}
	return "static"
}

// expandAZ converts advices with multiple zone scores into separate rows per AZ.
func expandAZ(advices []spot.Advice) []spot.Advice {
	var result []spot.Advice

	for _, advice := range advices {
		if len(advice.ZoneScores) <= 1 {
			// No expansion needed - keep as-is
			result = append(result, advice)
			continue
		}

		for zone, score := range advice.ZoneScores {
			azAdvice := advice
			azAdvice.ZoneScores = map[string]int{zone: score}
			azAdvice.RegionScore = nil

			if advice.ZonePrice != nil {
				if zonePrice, exists := advice.ZonePrice[zone]; exists {
					azAdvice.Price = zonePrice
					azAdvice.ZonePrice = map[string]float64{zone: zonePrice}
				}
			}

			result = append(result, azAdvice)
		}
	}

	return result
}

func printAdvicesTable(advices []spot.Advice, csv, region bool, output io.Writer) {
	tbl := table.NewWriter()
	tbl.SetOutputMirror(output)

	// Expand AZ scores to separate rows for better display
	advices = expandAZ(advices)

	// Analyze score types and build header
	scoreInfo := analyzeScoreTypes(advices)
	header := buildTableHeader(scoreInfo, region, csv)
	tbl.AppendHeader(header)

	// Build rows with appropriate formatting
	for _, advice := range advices {
		var row table.Row
		if csv {
			// CSV output: data only, no visual formatting, with price source column
			row = buildTableRow(&advice, scoreInfo, region, csv)
		} else {
			// Table output: include visual formatting
			row = buildTableRow(&advice, scoreInfo, region, csv, WithVisualFormatting())
		}
		tbl.AppendRow(row)
	}
	// render as CSV
	if csv {
		tbl.RenderCSV()
	} else { // render as pretty table
		tbl.SetColumnConfigs([]table.ColumnConfig{{
			Name:        savingsColumn,
			Transformer: text.NewNumberTransformer("%d%%"),
		}})
		tbl.SetStyle(table.StyleLight)
		tbl.Style().Options.SeparateRows = true
		tbl.Render()
	}
}

func init() {
	// Initialize logger with default level
	log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// handle termination signal
	mainCtx = handleSignals()
}

func handleSignals() context.Context {
	// Graceful shut-down on SIGINT/SIGTERM
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// create cancelable context
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer cancel()

		sid := <-sig

		log.Info("received signal", slog.String("signal", sid.String()))
		log.Info("canceling main command")
	}()

	return ctx
}

func defaultRecommendAction(ctx *cli.Context) error {
	client := spot.New()

	registry, err := newProviderRegistry(client)
	if err != nil {
		return err
	}

	return execRecommendCmd(ctx, mainCtx, registry, client, os.Stdout)
}

//nolint:funlen // CLI flag declarations are intentionally kept together.
func recommendCommand(action cli.ActionFunc) *cli.Command {
	return &cli.Command{
		Name:   recommendCommandName,
		Usage:  "recommend individual Spot instances by architecture and workload",
		Action: action,
		Flags: []cli.Flag{
			cloudFlag(),
			&cli.StringFlag{Name: flagArchitecture, Usage: "required instance architecture: x86_64|arm64", Required: true},
			&cli.StringFlag{
				Name:    flagInstance,
				Aliases: []string{flagMachine},
				Usage:   "machine type RE2 regexp (combined with architecture)",
			},
			&cli.StringSliceFlag{Name: flagRegion, Usage: regionFlagUsage, Value: cli.NewStringSlice("us-east-1")},
			&cli.IntFlag{Name: flagCPU, Aliases: []string{"vcpu"}, Usage: "required minimum vCPU cores"},
			&cli.IntFlag{Name: flagMemory, Aliases: []string{"memory-gib"}, Usage: "required minimum memory GiB"},
			&cli.Float64Flag{Name: flagBudget, Usage: "positive maximum USD per candidate instance-hour"},
			&cli.StringFlag{Name: flagOS, Usage: "instance operating system: linux|windows", Value: spot.OperatingSystemLinux},
			// No default value: "not set" must stay distinguishable from "set to
			// web", because the default depends on the provider. A cloud that
			// publishes no risk cannot honestly claim an interruption ceiling, so
			// it defaults to the risk-free cost policy instead.
			&cli.StringFlag{
				Name:  flagWorkload,
				Usage: "ranking policy: cost|web|ci|batch (default: web on a cloud with interruption data, otherwise cost)",
			},
			&cli.IntFlag{Name: flagTop, Usage: "maximum recommendations to return", Value: spot.DefaultRecommendationTop},
			&cli.StringFlag{Name: flagOutput, Usage: "format output: table|json", Value: outputTable},
		},
	}
}

// newSpotinfoApp assembles the production root and recommend command wiring.
// Actions are injected so assembly tests exercise the production flags and commands
// without opening network clients or writing to process stdout.
//
//nolint:funlen // CLI flag declarations are intentionally kept together.
func newSpotinfoApp(rootAction, recommendationAction cli.ActionFunc) *cli.App {
	return &cli.App{
		Before: func(ctx *cli.Context) error {
			// Update logger based on flags
			logLevel := slog.LevelInfo
			if ctx.Bool(flagDebug) {
				logLevel = slog.LevelDebug
			} else if ctx.Bool(flagQuiet) {
				logLevel = slog.LevelError
			}

			opts := &slog.HandlerOptions{Level: logLevel}
			if ctx.Bool(flagJSONLog) {
				log = slog.New(slog.NewJSONHandler(os.Stderr, opts))
			} else {
				log = slog.New(slog.NewTextHandler(os.Stderr, opts))
			}

			return nil
		},
		Flags: []cli.Flag{
			cloudFlag(),
			&cli.BoolFlag{
				Name:  flagMCP,
				Usage: "run as MCP server instead of CLI",
			},
			&cli.BoolFlag{
				Name:  flagDebug,
				Usage: "enable debug logging",
			},
			&cli.BoolFlag{
				Name:  flagQuiet,
				Usage: "quiet mode (errors only)",
			},
			&cli.BoolFlag{
				Name:  flagJSONLog,
				Usage: "output logs in JSON format",
			},
			&cli.StringFlag{
				Name:    flagType,
				Aliases: []string{flagMachine},
				Usage:   "EC2 instance type (can be RE2 regexp pattern)",
			},
			&cli.StringFlag{
				Name:  flagOS,
				Usage: "instance operating system (windows/linux)",
				Value: defaultInstanceOS,
			},
			&cli.StringSliceFlag{
				Name:  flagRegion,
				Usage: regionFlagUsage,
				Value: cli.NewStringSlice("us-east-1"),
			},
			&cli.StringFlag{
				Name:  flagOutput,
				Usage: "format output: number|text|json|table|csv",
				Value: outputTable,
			},
			&cli.IntFlag{
				Name:  flagCPU,
				Usage: "filter: minimal vCPU cores",
			},
			&cli.IntFlag{
				Name:  flagMemory,
				Usage: "filter: minimal memory GiB",
			},
			&cli.Float64Flag{
				Name:  flagPrice,
				Usage: "filter: positive maximum USD price per hour",
			},
			&cli.StringFlag{
				Name:  flagSort,
				Usage: "sort results by interruption|type|savings|price|region|score",
				Value: sortInterruption,
			},
			&cli.StringFlag{
				Name:  flagOrder,
				Usage: "sort order asc|desc",
				Value: orderAsc,
			},
			&cli.BoolFlag{
				Name:  flagWithScore,
				Usage: "include AWS spot placement scores (experimental)",
			},
			&cli.IntFlag{
				Name:  flagMinScore,
				Usage: "filter: minimum spot placement score (1-10)",
			},
			&cli.BoolFlag{
				Name:  flagAZ,
				Usage: "request AZ-level scores instead of region-level (use with --with-score)",
			},
			&cli.IntFlag{
				Name:  flagScoreTimeout,
				Usage: "timeout for score enrichment in seconds",
				Value: spot.DefaultScoreTimeoutSeconds,
			},
		},
		Name:     appName,
		Usage:    "explore AWS EC2 Spot instances",
		Action:   rootAction,
		Commands: []*cli.Command{recommendCommand(recommendationAction)},
		Version:  Version,
	}
}

func printVersion(_ *cli.Context) {
	fmt.Printf("spotinfo %s\n", Version)

	if GitHubRelease != "" {
		fmt.Printf("  GitHub release: %s\n", GitHubRelease)
	}

	if BuildDate != "" && BuildDate != unknownBuildValue {
		fmt.Printf("  Build date: %s\n", BuildDate)
	}

	if GitCommit != "" {
		fmt.Printf("  Git commit: %s\n", GitCommit)
	}

	if GitBranch != "" {
		fmt.Printf("  Git branch: %s\n", GitBranch)
	}

	fmt.Printf("  Built with: %s\n", runtime.Version())
}

func main() {
	app := newSpotinfoApp(mainCmd, defaultRecommendAction)
	cli.VersionPrinter = printVersion

	if err := app.Run(os.Args); err != nil {
		log.Error("application failed", slog.Any("error", err))
		os.Exit(1)
	}
}
