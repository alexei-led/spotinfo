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
	instanceTypeColumn = "Instance Info"
	vCpuColumn         = "vCPU"
	memoryColumn       = "Memory GiB"
	savingsColumn      = "Savings over On-Demand"
	interruptionColumn = "Frequency of interruption"
)

func mainCmd(c *cli.Context) error {
	log.Printf("running main command with %s", c.FlagNames())
	if v := mainCtx.Value("key"); v != nil {
		log.Printf("context value = %v", v)
	}
	region := c.String("region")
	instanceOS := c.String("os")
	instance := c.String("type")
	cpu := c.Int("cpu")
	memory := c.Int("memory")
	sortBy := c.String("sort")
	sort := spot.SortByRange
	switch sortBy {
	case "type":
		sort = spot.SortByInstance
	case "interruption":
		sort = spot.SortByRange
	case "savings":
		sort = spot.SortBySavings
	default:
		sort = spot.SortByRange
	}
	// get spot savings
	advices, err := spot.GetSpotSavings(instance, region, instanceOS, cpu, memory, sort)
	if err != nil {
		return err
	}
	switch c.String("output") {
	case "number":
		printAdvicesNumber(advices)
	case "text":
		printAdvicesText(advices)
	case "json":
		printAdvicesJson(advices)
	case "table":
		printAdvicesTable(advices, false)
	case "csv":
		printAdvicesTable(advices, true)
	default:
		printAdvicesNumber(advices)
	}
	return nil
}

func printAdvicesText(advices []spot.Advice) {
	for _, advice := range advices {
		fmt.Printf("%s vCPU=%d, memory=%vGiB, saving=%d%%, interruption='%s'\n",
			advice.Instance, advice.Info.Cores, advice.Info.Ram, advice.Savings, advice.Range.Label)
	}
}

func printAdvicesNumber(advices []spot.Advice) {
	if len(advices) == 1 {
		fmt.Println(advices[0].Savings)
		return
	}
	for _, advice := range advices {
		fmt.Printf("%s: %d\n", advice.Instance, advice.Savings)
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

func printAdvicesTable(advices []spot.Advice, csv bool) {
	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.AppendHeader(table.Row{instanceTypeColumn, vCpuColumn, memoryColumn, savingsColumn, interruptionColumn})
	for _, advice := range advices {
		t.AppendRow([]interface{}{advice.Instance, advice.Info.Cores, advice.Info.Ram, advice.Savings, advice.Range.Label})
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
			&cli.StringFlag{
				Name:  "region",
				Usage: "AWS region",
				Value: "us-east-1",
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
			&cli.StringFlag{
				Name:  "sort",
				Usage: "sort results by interruption(default)|type|savings",
				Value: "interruption",
			},
		},
		Name:    "spotinfo",
		Usage:   "spotinfo CLI",
		Action:  mainCmd,
		Version: Version,
	}
	cli.VersionPrinter = func(c *cli.Context) {
		fmt.Printf("spotinfo %s\n", Version)
		fmt.Printf("  Build date: %s\n", BuildDate)
		fmt.Printf("  Git commit: %s\n", GitCommit)
		fmt.Printf("  Git branch: %s\n", GitBranch)
		fmt.Printf("  Built with: %s\n", runtime.Version())
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}
