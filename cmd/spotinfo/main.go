// Package main provides the CLI application for spotinfo.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

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
	BuildDate = "unknown"
	// GitCommit git commit SHA
	GitCommit = "dirty"
	// GitBranch git branch
	GitBranch = "master"
)

const (
	regionColumn       = "Region"
	instanceTypeColumn = "Instance Info"
	vCPUColumn         = "vCPU"
	memoryColumn       = "Memory GiB"
	savingsColumn      = "Savings over On-Demand"
	interruptionColumn = "Frequency of interruption"
	priceColumn        = "USD/Hour"
)

//nolint:cyclop
func mainCmd(ctx *cli.Context) error {
	if v := mainCtx.Value("key"); v != nil {
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

	var sortType spot.SortBy

	switch sortBy {
	case "type":
		sortType = spot.SortByInstance
	case "interruption":
		sortType = spot.SortByRange
	case "savings":
		sortType = spot.SortBySavings
	case "price":
		sortType = spot.SortByPrice
	case "region":
		sortType = spot.SortByRegion
	default:
		sortType = spot.SortByRange
	}

	// create spot client
	client := spot.New()

	// get spot savings
	advices, err := client.GetSpotSavings(mainCtx, regions, instance, instanceOS, cpu, memory, maxPrice, sortType, sortDesc)
	if err != nil {
		return fmt.Errorf("failed to get spot savings: %w", err)
	}

	// decide if region should be printed
	printRegion := len(regions) > 1 || (len(regions) == 1 && regions[0] == "all")

	switch ctx.String("output") {
	case "number":
		printAdvicesNumber(advices, printRegion)
	case "text":
		printAdvicesText(advices, printRegion)
	case "json":
		printAdvicesJSON(advices)
	case "table":
		printAdvicesTable(advices, false, printRegion)
	case "csv":
		printAdvicesTable(advices, true, printRegion)
	default:
		printAdvicesNumber(advices, printRegion)
	}

	return nil
}

func printAdvicesText(advices []spot.Advice, region bool) {
	for _, advice := range advices {
		if region {
			fmt.Printf("region=%s, type=%s, vCPU=%d, memory=%vGiB, saving=%d%%, interruption='%s', price=%.2f\n",
				advice.Region, advice.Instance, advice.Info.Cores, advice.Info.RAM, advice.Savings, advice.Range.Label, advice.Price)
		} else {
			fmt.Printf("type=%s, vCPU=%d, memory=%vGiB, saving=%d%%, interruption='%s', price=%.2f\n",
				advice.Instance, advice.Info.Cores, advice.Info.RAM, advice.Savings, advice.Range.Label, advice.Price)
		}
	}
}

func printAdvicesNumber(advices []spot.Advice, region bool) {
	if len(advices) == 1 {
		fmt.Println(advices[0].Savings)

		return
	}

	for _, advice := range advices {
		if region {
			fmt.Printf("%s/%s: %d\n", advice.Region, advice.Instance, advice.Savings)
		} else {
			fmt.Printf("%s: %d\n", advice.Instance, advice.Savings)
		}
	}
}

func printAdvicesJSON(advices interface{}) {
	bytes, err := json.MarshalIndent(advices, "", "  ")
	if err != nil {
		panic(err)
	}

	txt := string(bytes)
	txt = strings.ReplaceAll(txt, "\\u003c", "<")
	txt = strings.ReplaceAll(txt, "\\u003e", ">")
	fmt.Println(txt)
}

func printAdvicesTable(advices []spot.Advice, csv, region bool) {
	tbl := table.NewWriter()
	tbl.SetOutputMirror(os.Stdout)

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

		if BuildDate != "" && BuildDate != "unknown" {
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
