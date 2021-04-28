package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"spotinfo/public/spot"
	"strings"
	"syscall"

	"github.com/jedib0t/go-pretty/v6/text"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v2"
)

var (
	// main context
	mainCtx context.Context
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
	vCpuColumn         = "vCPU"
	memoryColumn       = "Memory GiB"
	savingsColumn      = "Savings over On-Demand"
	interruptionColumn = "Frequency of interruption"
	priceColumn        = "USD/Hour"
)

func mainCmd(c *cli.Context) error {
	if v := mainCtx.Value("key"); v != nil {
		log.Printf("context value = %v", v)
	}
	regions := c.StringSlice("region")
	instanceOS := c.String("os")
	instance := c.String("type")
	cpu := c.Int("cpu")
	memory := c.Int("memory")
	maxPrice := c.Float64("price")
	sortBy := c.String("sort")
	sort := spot.SortByRange
	order := c.String("order")
	sortDesc := strings.ToLower(order) == "desc"
	switch sortBy {
	case "type":
		sort = spot.SortByInstance
	case "interruption":
		sort = spot.SortByRange
	case "savings":
		sort = spot.SortBySavings
	case "price":
		sort = spot.SortByPrice
	case "region":
		sort = spot.SortByRegion
	default:
		sort = spot.SortByRange
	}
	// get spot savings
	advices, err := spot.GetSpotSavings(regions, instance, instanceOS, cpu, memory, maxPrice, sort, sortDesc)
	// decide if region should be printed
	printRegion := len(regions) > 1 || (len(regions) == 1 && regions[0] == "all")
	if err != nil {
		return err
	}
	switch c.String("output") {
	case "number":
		printAdvicesNumber(advices, printRegion)
	case "text":
		printAdvicesText(advices, printRegion)
	case "json":
		printAdvicesJson(advices)
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
				advice.Region, advice.Instance, advice.Info.Cores, advice.Info.Ram, advice.Savings, advice.Range.Label, advice.Price)
		} else {
			fmt.Printf("type=%s, vCPU=%d, memory=%vGiB, saving=%d%%, interruption='%s', price=%.2f\n",
				advice.Instance, advice.Info.Cores, advice.Info.Ram, advice.Savings, advice.Range.Label, advice.Price)
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

func printAdvicesJson(advices interface{}) {
	bytes, err := json.MarshalIndent(advices, "", "  ")
	if err != nil {
		panic(err)
	}
	txt := string(bytes)
	txt = strings.Replace(txt, "\\u003c", "<", -1)
	txt = strings.Replace(txt, "\\u003e", ">", -1)
	fmt.Println(txt)
}

func printAdvicesTable(advices []spot.Advice, csv, region bool) {
	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	header := table.Row{instanceTypeColumn, vCpuColumn, memoryColumn, savingsColumn, interruptionColumn, priceColumn}
	if region {
		header = append(table.Row{regionColumn}, header...)
	}
	t.AppendHeader(header)
	for _, advice := range advices {
		row := table.Row{advice.Instance, advice.Info.Cores, advice.Info.Ram, advice.Savings, advice.Range.Label, advice.Price}
		if region {
			row = append(table.Row{advice.Region}, row...)
		}
		t.AppendRow(row)
	}
	// render as CSV
	if csv {
		fmt.Println("rendering CSV")
		t.RenderCSV()
	} else { // render as pretty table
		t.SetColumnConfigs([]table.ColumnConfig{{
			Name:        savingsColumn,
			Transformer: text.NewNumberTransformer("%d%%"),
		}})
		t.SetStyle(table.StyleLight)
		t.Style().Options.SeparateRows = true
		t.Render()
	}
}

func init() {
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
		log.Printf("received signal: %d\n", sid)
		log.Println("canceling main command ...")
	}()

	return ctx
}

func main() {
	app := &cli.App{
		Flags: []cli.Flag{
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
	cli.VersionPrinter = func(c *cli.Context) {
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

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}
