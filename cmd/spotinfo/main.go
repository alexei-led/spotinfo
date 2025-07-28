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
	if ctx.Bool("mcp") {
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

// execMainCmd is the testable version of mainCmd that accepts dependencies.
//
//nolint:cyclop,gocyclo,funlen // CLI argument parsing inherently has high complexity due to comprehensive option handling
func execMainCmd(ctx *cli.Context, execCtx context.Context, client spotClient, output io.Writer) error {
	if v := execCtx.Value("key"); v != nil {
		log.Debug("context value received", slog.Any("value", v))
	}

	regions := ctx.StringSlice("region")
	instanceOS := ctx.String("os")
	instance := ctx.String("type")
	cpu := ctx.Int("cpu")
	memory := ctx.Int("memory")
	maxPrice := ctx.Float64("price")
	sortBy := ctx.String("sort")
	order := ctx.String("order")
	sortDesc := strings.EqualFold(order, "desc")
	withScore := ctx.Bool("with-score")
	minScore := ctx.Int("min-score")
	azLevel := ctx.Bool("az")
	scoreTimeout := ctx.Int("score-timeout")

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
	printRegion := len(regions) > 1 || (len(regions) == 1 && regions[0] == "all")

	switch ctx.String("output") {
	case "number":
		printAdvicesNumber(advices, printRegion, output)
	case "text":
		printAdvicesText(advices, printRegion, output)
	case "json":
		printAdvicesJSON(advices, output)
	case "table":
		printAdvicesTable(advices, false, printRegion, output)
	case "csv":
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

		if region {
			fmt.Fprintf(output, "region=%s, type=%s, vCPU=%d, memory=%vGiB, saving=%d%%, interruption='%s', price=%.2f%s\n", //nolint:errcheck
				advice.Region, advice.Instance, advice.Info.Cores, advice.Info.RAM, advice.Savings, advice.Range.Label, advice.Price, scoreStr)
		} else {
			fmt.Fprintf(output, "type=%s, vCPU=%d, memory=%vGiB, saving=%d%%, interruption='%s', price=%.2f%s\n", //nolint:errcheck
				advice.Instance, advice.Info.Cores, advice.Info.RAM, advice.Savings, advice.Range.Label, advice.Price, scoreStr)
		}
	}
}

func printAdvicesNumber(advices []spot.Advice, region bool, output io.Writer) {
	if len(advices) == 1 {
		fmt.Fprintln(output, advices[0].Savings) //nolint:errcheck
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

func printAdvicesJSON(advices interface{}, output io.Writer) {
	bytes, err := json.MarshalIndent(advices, "", "  ")
	if err != nil {
		panic(err)
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
func buildTableHeader(scoreInfo scoreTypeInfo, region bool) table.Row {
	header := table.Row{instanceTypeColumn, vCPUColumn, memoryColumn, savingsColumn, interruptionColumn, priceColumn}
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
func buildTableRow(advice *spot.Advice, scoreInfo scoreTypeInfo, region bool, options ...TableRowOption) table.Row {
	opts := &tableRowOptions{}
	for _, opt := range options {
		opt(opts)
	}

	row := table.Row{advice.Instance, advice.Info.Cores, advice.Info.RAM, advice.Savings, advice.Range.Label, advice.Price}
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
	header := buildTableHeader(scoreInfo, region)
	tbl.AppendHeader(header)

	// Build rows with appropriate formatting
	for _, advice := range advices {
		var row table.Row
		if csv {
			// CSV output: data only, no visual formatting
			row = buildTableRow(&advice, scoreInfo, region)
		} else {
			// Table output: include visual formatting
			row = buildTableRow(&advice, scoreInfo, region, WithVisualFormatting())
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
func main() {
	app := &cli.App{
		Before: func(ctx *cli.Context) error {
			// Update logger based on flags
			logLevel := slog.LevelInfo
			if ctx.Bool("debug") {
				logLevel = slog.LevelDebug
			} else if ctx.Bool("quiet") {
				logLevel = slog.LevelError
			}

			opts := &slog.HandlerOptions{Level: logLevel}
			if ctx.Bool("json-log") {
				log = slog.New(slog.NewJSONHandler(os.Stderr, opts))
			} else {
				log = slog.New(slog.NewTextHandler(os.Stderr, opts))
			}

			return nil
		},
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "mcp",
				Usage: "run as MCP server instead of CLI",
			},
			&cli.BoolFlag{
				Name:  "debug",
				Usage: "enable debug logging",
			},
			&cli.BoolFlag{
				Name:  "quiet",
				Usage: "quiet mode (errors only)",
			},
			&cli.BoolFlag{
				Name:  "json-log",
				Usage: "output logs in JSON format",
			},
			&cli.StringFlag{
				Name:  "type",
				Usage: "EC2 instance type (can be RE2 regexp patten)",
			},
			&cli.StringFlag{
				Name:  "os",
				Usage: "instance operating system (windows/linux)",
				Value: "linux",
			},
			&cli.StringSliceFlag{
				Name:  "region",
				Usage: "set one or more AWS regions, use \"all\" for all AWS regions",
				Value: cli.NewStringSlice("us-east-1"),
			},
			&cli.StringFlag{
				Name:  "output",
				Usage: "format output: number|text|json|table|csv",
				Value: "table",
			},
			&cli.IntFlag{
				Name:  "cpu",
				Usage: "filter: minimal vCPU cores",
			},
			&cli.IntFlag{
				Name:  "memory",
				Usage: "filter: minimal memory GiB",
			},
			&cli.Float64Flag{
				Name:  "price",
				Usage: "filter: maximum price per hour",
			},
			&cli.StringFlag{
				Name:  "sort",
				Usage: "sort results by interruption|type|savings|price|region|score",
				Value: "interruption",
			},
			&cli.StringFlag{
				Name:  "order",
				Usage: "sort order asc|desc",
				Value: "asc",
			},
			&cli.BoolFlag{
				Name:  "with-score",
				Usage: "include AWS spot placement scores (experimental)",
			},
			&cli.IntFlag{
				Name:  "min-score",
				Usage: "filter: minimum spot placement score (1-10)",
			},
			&cli.BoolFlag{
				Name:  "az",
				Usage: "request AZ-level scores instead of region-level (use with --with-score)",
			},
			&cli.IntFlag{
				Name:  "score-timeout",
				Usage: "timeout for score enrichment in seconds",
				Value: spot.DefaultScoreTimeoutSeconds,
			},
		},
		Name:    "spotinfo",
		Usage:   "explore AWS EC2 Spot instances",
		Action:  mainCmd,
		Version: Version,
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
