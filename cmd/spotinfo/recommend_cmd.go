package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/spot"
)

// recommendCmd handles the recommend subcommand.
func recommendCmd(ctx *cli.Context) error {
	client := spot.New()
	return execRecommendCmd(ctx, context.Background(), client, os.Stdout)
}

//nolint:cyclop // Complex command with many branches for output formats
func execRecommendCmd(ctx *cli.Context, execCtx context.Context, client spotClient, output io.Writer) error {
	// Parse flags
	vCPU := ctx.Int("cpu")
	memory := ctx.Int("memory")
	regions := ctx.StringSlice("region")
	workload := ctx.String("workload")
	topN := ctx.Int("top")
	outputFormat := ctx.String("output")
	maxPrice := ctx.Float64("price")
	sortBy := ctx.String("sort")
	budget := ctx.String("budget")

	// Validate workload
	if !isValidWorkload(workload) {
		return fmt.Errorf("invalid workload type: %s (valid: web, batch, ml, ci)", workload)
	}

	// Validate topN
	if topN <= 0 {
		topN = 3
	}

	// Build options for GetSpotSavings
	var opts []spot.GetSpotSavingsOption
	
	opts = append(opts, spot.WithRegions(regions))
	
	if vCPU > 0 {
		opts = append(opts, spot.WithCPU(vCPU))
	}
	if memory > 0 {
		opts = append(opts, spot.WithMemory(memory))
	}
	if maxPrice > 0 {
		opts = append(opts, spot.WithMaxPrice(maxPrice))
	}

	// Set sort order if specified
	sortByType := spot.SortByRange // default
	switch sortBy {
	case sortPrice:
		sortByType = spot.SortByPrice
	case sortSavings:
		sortByType = spot.SortBySavings
	case sortRegion:
		sortByType = spot.SortByRegion
	case sortScore:
		sortByType = spot.SortByScore
	case sortInterruption:
		sortByType = spot.SortByRange
	}
	opts = append(opts, spot.WithSort(sortByType, false))

	// Get spot savings data
	advices, err := client.GetSpotSavings(execCtx, opts...)
	if err != nil {
		return fmt.Errorf("failed to get spot savings: %w", err)
	}

	if len(advices) == 0 {
		fmt.Fprintln(output, "No matching instances found")
		return nil
	}

	// Create recommendation engine
	engine := spot.NewRecommendationEngine(workload)

	// Generate recommendations
	rec := engine.Recommend(advices, topN)

	// Filter by interruption tolerance if requested
	filtered := engine.FilterByInterruptionTolerance(rec.Candidates)
	if len(filtered) > 0 {
		rec.Candidates = filtered
	}

	// Format and output
	switch strings.ToLower(outputFormat) {
	case "json":
		return outputRecommendationJSON(rec, output)
	case "table", "text":
		return outputRecommendationTable(rec, output, workload, vCPU, memory, budget)
	case "csv":
		return outputRecommendationCSV(rec, output)
	default:
		return outputRecommendationTable(rec, output, workload, vCPU, memory, budget)
	}
}

// outputRecommendationJSON outputs recommendations in JSON format.
func outputRecommendationJSON(rec *spot.Recommendation, output io.Writer) error {
	data := map[string]interface{}{
		"workload":   rec.Workload,
		"candidates": rec.Candidates,
		"summary":    rec.Summary,
	}

	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

// outputRecommendationTable outputs recommendations in human-readable table format.
func outputRecommendationTable(rec *spot.Recommendation, output io.Writer, workload string, vCPU, memory int, budget string) error {
	const scorePercentage = 100
	const interruptionPercentage = 100

	// Print header
	fmt.Fprintf(output, "\n")
	fmt.Fprintf(output, "RECOMMENDATION for: %s", workload)
	if vCPU > 0 {
		fmt.Fprintf(output, " / %d vCPU", vCPU)
	}
	if memory > 0 {
		fmt.Fprintf(output, " / %d GB", memory)
	}
	if budget != "" {
		fmt.Fprintf(output, " / budget %s/hr", budget)
	}
	fmt.Fprintf(output, "\n\n")

	// Create table
	t := table.NewWriter()
	t.SetOutputMirror(output)
	t.AppendHeader(table.Row{"RANK", "INSTANCE", "SCORE", "PRICE", "SAVINGS", "INTERRUPTION", "WHY"})

	for _, item := range rec.Candidates {
		t.AppendRow(table.Row{
			item.Rank,
			item.Instance,
			fmt.Sprintf("%.1f", item.Score*scorePercentage),
			fmt.Sprintf("$%.3f", item.Price),
			fmt.Sprintf("%d%%", item.Savings),
			fmt.Sprintf("%.1f%%", item.InterruptionRate*interruptionPercentage),
			item.Rationale,
		})

		// Alternate row colors for readability
		if item.Rank%2 == 0 {
			t.AppendRow(table.Row{"", "", "", "", "", "", ""})
		}
	}

	t.SetStyle(table.StyleLight)
	t.Render()

	// Print summary
	fmt.Fprintf(output, "\n%s\n\n", rec.Summary)

	return nil
}

// outputRecommendationCSV outputs recommendations in CSV format.
func outputRecommendationCSV(rec *spot.Recommendation, output io.Writer) error {
	const scorePercentage = 100
	const interruptionPercentage = 100

	// Header
	fmt.Fprintln(output, "Rank,Instance,Score,Price,Savings,InterruptionRate,Rationale")

	// Data rows
	for _, item := range rec.Candidates {
		fmt.Fprintf(output, "%d,%s,%.1f,%.3f,%d,%.1f,\"%s\"\n",
			item.Rank,
			item.Instance,
			item.Score*scorePercentage,
			item.Price,
			item.Savings,
			item.InterruptionRate*interruptionPercentage,
			item.Rationale,
		)
	}

	return nil
}

// isValidWorkload checks if the workload type is valid.
func isValidWorkload(workload string) bool {
	validWorkloads := []string{"web", "batch", "ml", "ci"}
	for _, valid := range validWorkloads {
		if strings.EqualFold(workload, valid) {
			return true
		}
	}
	return false
}
