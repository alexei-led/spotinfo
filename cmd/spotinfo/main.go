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

	"spotinfo/internal/mcp"  //nolint:gci // local import group
	"spotinfo/internal/spot" //nolint:gci // local import group

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/urfave/cli/v2" //nolint:gci
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
)

const (
	// Table column headers
	regionColumn       = "Region"
	instanceTypeColumn = "Instance Info"
	vCPUColumn         = "vCPU"
	memoryColumn       = "Memory GiB"
	savingsColumn      = "Savings over On-Demand"
	interruptionColumn = "Frequency of interruption"
	priceColumn        = "USD/Hour"

	// Sort types
	sortType         = "type"
	sortInterruption = "interruption"
	sortSavings      = "savings"
	sortPrice        = "price"
	sortRegion       = "region"

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
		Version:   Version,
		Transport: transport,
		Port:      port,
		Logger:    log,
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

// SpotClient interface defined close to consumer for testing (following codebase patterns)
type SpotClient interface {
	GetSpotSavings(ctx context.Context, regions []string, pattern, instanceOS string,
		cpu, memory int, maxPrice float64, sortBy spot.SortBy, sortDesc bool) ([]spot.Advice, error)
}

// execMainCmd is the testable version of mainCmd that accepts dependencies.
//
//nolint:cyclop
func execMainCmd(ctx *cli.Context, execCtx context.Context, client SpotClient, output io.Writer) error {
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
	default:
		sortByType = spot.SortByRange
	}

	// get spot savings
	advices, err := client.GetSpotSavings(execCtx, regions, instance, instanceOS, cpu, memory, maxPrice, sortByType, sortDesc)
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
		if region {
			fmt.Fprintf(output, "region=%s, type=%s, vCPU=%d, memory=%vGiB, saving=%d%%, interruption='%s', price=%.2f\n", //nolint:errcheck
				advice.Region, advice.Instance, advice.Info.Cores, advice.Info.RAM, advice.Savings, advice.Range.Label, advice.Price)
		} else {
			fmt.Fprintf(output, "type=%s, vCPU=%d, memory=%vGiB, saving=%d%%, interruption='%s', price=%.2f\n", //nolint:errcheck
				advice.Instance, advice.Info.Cores, advice.Info.RAM, advice.Savings, advice.Range.Label, advice.Price)
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

func printAdvicesTable(advices []spot.Advice, csv, region bool, output io.Writer) {
	tbl := table.NewWriter()
	tbl.SetOutputMirror(output)

	header := table.Row{instanceTypeColumn, vCPUColumn, memoryColumn, savingsColumn, interruptionColumn, priceColumn}
	if region {
		header = append(table.Row{regionColumn}, header...)
	}

	tbl.AppendHeader(header)

	for _, advice := range advices {
		row := table.Row{advice.Instance, advice.Info.Cores, advice.Info.RAM, advice.Savings, advice.Range.Label, advice.Price}
		if region {
			row = append(table.Row{advice.Region}, row...)
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
				Usage: "sort results by interruption|type|savings|price|region",
				Value: "interruption",
			},
			&cli.StringFlag{
				Name:  "order",
				Usage: "sort order asc|desc",
				Value: "asc",
			},
		},
		Name:    "spotinfo",
		Usage:   "explore AWS EC2 Spot instances",
		Action:  mainCmd,
		Version: Version,
	}
	cli.VersionPrinter = func(_ *cli.Context) {
		fmt.Printf("spotinfo %s\n", Version)

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
