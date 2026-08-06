// Package main provides the CLI application for spotinfo.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/mcp"
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

	// allRegions is the --region value selecting every AWS region
	allRegions = "all"

	// appName is the CLI application name
	appName = "spotinfo"

	recommendCommandName = "recommend"
	regionFlagUsage      = "set one or more AWS regions, use \"all\" for all AWS regions"
)

//nolint:cyclop
func mainCmd(ctx *cli.Context) error {
	// Check for MCP mode before running CLI
	if isMCPMode(ctx) {
		return runMCPServer(ctx, mainCtx)
	}
	return execMainCmd(ctx, mainCtx, spot.New(), os.Stdout)
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

	// Create MCP server
	mcpServer, err := mcp.NewServer(mcp.Config{
		Version:    Version,
		Transport:  transport,
		Port:       port,
		Logger:     log,
		SpotClient: spot.New(),
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

const recommendationSchemaVersion = "spotinfo.recommend/v1"

type recommendationRequest struct { //nolint:govet // JSON request field grouping is clearer than memory-layout optimization.
	Architecture              spot.Architecture `json:"architecture"`
	InstanceRegexp            string            `json:"instance_regexp"`
	Regions                   []string          `json:"regions"`
	OS                        string            `json:"os"`
	MinimumVCPU               int               `json:"minimum_vcpu"`
	MinimumMemoryGiB          int               `json:"minimum_memory_gib"`
	MaximumUSDPerInstanceHour *float64          `json:"maximum_usd_per_instance_hour"`
	Workload                  spot.Workload     `json:"workload"`
	Top                       int               `json:"top"`
}

type recommendationReport struct {
	SchemaVersion   string                `json:"schema_version"`
	Request         recommendationRequest `json:"request"`
	RankingPolicy   []string              `json:"ranking_policy"`
	Recommendations []spot.Recommendation `json:"recommendations"`
}

func normalizedRegions(regions []string) []string {
	unique := make(map[string]struct{}, len(regions))
	for _, region := range regions {
		unique[region] = struct{}{}
	}
	normalized := make([]string, 0, len(unique))
	for region := range unique {
		normalized = append(normalized, region)
	}
	sort.Strings(normalized)
	return normalized
}

func recommendationRankingPolicy() []string {
	return []string{
		"price_usd_per_hour_ascending",
		"interruption_frequency_ascending",
		"excess_vcpu_ascending",
		"excess_memory_gib_ascending",
		"region_ascending",
		"instance_ascending",
	}
}

// execMainCmd is the testable version of mainCmd that accepts dependencies.
//
//nolint:cyclop,gocyclo,funlen // CLI argument parsing inherently has high complexity due to comprehensive option handling
func execMainCmd(ctx *cli.Context, execCtx context.Context, client spotClient, output io.Writer) error {
	if v := execCtx.Value("key"); v != nil {
		log.Debug("context value received", slog.Any("value", v))
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

// execRecommendCmd fetches candidate advice and renders only the dedicated,
// deterministic recommendation DTO. Recommendation ranking itself lives in
// internal/spot and performs no I/O.
func execRecommendCmd(ctx *cli.Context, execCtx context.Context, client spotClient, output io.Writer) error { //nolint:gocyclo,cyclop // CLI validation and rendering have explicit error paths.
	budget := ctx.Float64(flagBudget)
	if ctx.IsSet(flagBudget) && budget <= 0 {
		return fmt.Errorf("%w: budget must be a positive USD instance-hour price", spot.ErrInvalidRecommendationInput)
	}
	if ctx.IsSet(flagTop) && ctx.Int(flagTop) <= 0 {
		return fmt.Errorf("%w: top must be positive", spot.ErrInvalidRecommendationInput)
	}
	outputFormat := ctx.String(flagOutput)
	if outputFormat != outputTable && outputFormat != outputJSON {
		return fmt.Errorf("%w: output must be table or json", spot.ErrInvalidRecommendationInput)
	}

	opts := spot.RecommendationOptions{
		Architecture: spot.Architecture(ctx.String(flagArchitecture)),
		Instance:     ctx.String(flagInstance),
		OS:           ctx.String(flagOS),
		CPU:          ctx.Int(flagCPU),
		Memory:       ctx.Int(flagMemory),
		Budget:       budget,
		Workload:     spot.Workload(ctx.String(flagWorkload)),
		Top:          ctx.Int(flagTop),
	}
	if err := spot.ValidateRecommendationOptions(&opts); err != nil {
		return err
	}

	lookup, err := spot.LoadEmbeddedArchitectureLookup()
	if err != nil {
		return fmt.Errorf("load recommendation architecture data: %w", err)
	}

	regions := ctx.StringSlice(flagRegion)
	queryOpts := []spot.GetSpotSavingsOption{
		spot.WithRegions(regions),
		spot.WithOS(opts.OS),
		spot.WithCPU(opts.CPU),
		spot.WithMemory(opts.Memory),
	}
	if opts.Budget > 0 {
		queryOpts = append(queryOpts, spot.WithMaxPrice(opts.Budget))
	}

	advices, err := client.GetSpotSavings(execCtx, queryOpts...)
	if err != nil {
		return fmt.Errorf("failed to get recommendation candidates: %w", err)
	}

	recommendations, err := spot.Recommend(advices, &opts, lookup)
	if err != nil {
		return err
	}

	report := recommendationReport{
		SchemaVersion: recommendationSchemaVersion,
		Request: recommendationRequest{
			Architecture:     opts.Architecture,
			InstanceRegexp:   opts.Instance,
			Regions:          normalizedRegions(regions),
			OS:               opts.OS,
			MinimumVCPU:      opts.CPU,
			MinimumMemoryGiB: opts.Memory,
			Workload:         opts.Workload,
			Top:              opts.Top,
		},
		RankingPolicy:   recommendationRankingPolicy(),
		Recommendations: recommendations,
	}
	if opts.Budget > 0 {
		report.Request.MaximumUSDPerInstanceHour = &opts.Budget
	}

	switch outputFormat {
	case outputTable:
		return writeRecommendationTable(report.Recommendations, output)
	case outputJSON:
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("render recommendation JSON: %w", err)
		}
		if _, err := fmt.Fprintln(output, string(encoded)); err != nil {
			return fmt.Errorf("write recommendation output: %w", err)
		}
	}

	return nil
}

func writeRecommendationTable(recommendations []spot.Recommendation, output io.Writer) error {
	if _, err := fmt.Fprintln(output, "RANK  REGION       INSTANCE       ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR  SAVINGS  INTERRUPTION  WHY"); err != nil {
		return fmt.Errorf("write recommendation output: %w", err)
	}
	for index, recommendation := range recommendations {
		if _, err := fmt.Fprintf(output, "%4d  %-11s %-14s %-12s %4d  %10.1f  %8.4f  %6d%%  %-12s  %s\n",
			index+1, recommendation.Region, recommendation.Instance, recommendation.Architecture,
			recommendation.VCPU, recommendation.MemoryGiB, recommendation.PriceUSDPerHour,
			recommendation.SavingsPercent, recommendation.InterruptionFrequency,
			strings.Join(recommendation.RationaleCodes, ",")); err != nil {
			return fmt.Errorf("write recommendation output: %w", err)
		}
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

//nolint:funlen // CLI main functions are inherently long due to comprehensive flag definitions
func recommendCommand() *cli.Command {
	return &cli.Command{
		Name:  recommendCommandName,
		Usage: "recommend individual Spot instances by architecture and workload",
		Action: func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, mainCtx, spot.New(), os.Stdout)
		},
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagArchitecture, Usage: "required instance architecture: x86_64|arm64", Required: true},
			&cli.StringFlag{Name: flagInstance, Usage: "instance type RE2 regexp (combined with architecture)"},
			&cli.StringSliceFlag{Name: flagRegion, Usage: regionFlagUsage, Value: cli.NewStringSlice("us-east-1")},
			&cli.IntFlag{Name: flagCPU, Aliases: []string{"vcpu"}, Usage: "required minimum vCPU cores", Required: true},
			&cli.IntFlag{Name: flagMemory, Aliases: []string{"memory-gib"}, Usage: "required minimum memory GiB", Required: true},
			&cli.Float64Flag{Name: flagBudget, Usage: "positive maximum USD per candidate instance-hour"},
			&cli.StringFlag{Name: flagOS, Usage: "instance operating system: linux|windows", Value: spot.OperatingSystemLinux},
			&cli.StringFlag{Name: flagWorkload, Usage: "interruption cap: web|ci|batch", Value: string(spot.WorkloadWeb)},
			&cli.IntFlag{Name: flagTop, Usage: "maximum recommendations to return", Value: spot.DefaultRecommendationTop},
			&cli.StringFlag{Name: flagOutput, Usage: "format output: table|json", Value: outputTable},
		},
	}
}

//nolint:funlen // CLI flag declarations are intentionally kept together.
func main() {
	app := &cli.App{
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
				Name:  flagType,
				Usage: "EC2 instance type (can be RE2 regexp patten)",
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
				Usage: "filter: maximum price per hour",
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
		Action:   mainCmd,
		Commands: []*cli.Command{recommendCommand()},
		Version:  Version,
	}
	cli.VersionPrinter = func(_ *cli.Context) {
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

	if err := app.Run(os.Args); err != nil {
		log.Error("application failed", slog.Any("error", err))
		os.Exit(1)
	}
}
