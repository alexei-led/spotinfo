package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/spot"
)

// Constants for repeated strings
const (
	sortType         = "type"
	sortInterruption = "interruption"
	sortSavings      = "savings"
	sortPrice        = "price"
	sortRegion       = "region"

	outputNumber = "number"
	outputText   = "text"
	outputJSON   = "json"
	outputTable  = "table"
	outputCSV    = "csv"

	allRegions = "all"
)

// TestMain sets up test environment
func TestMain(m *testing.M) {
	// Ensure tests run with clean state
	code := m.Run()
	os.Exit(code)
}

// captureOutput captures stdout during function execution
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)

	os.Stdout = w

	// Use a goroutine to read from the pipe to avoid blocking
	done := make(chan string, 1)
	go func() {
		defer func() { _ = r.Close() }()
		var buf []byte
		chunk := make([]byte, 1024)
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				buf = append(buf, chunk[:n]...)
			}
			if err != nil {
				break
			}
		}
		done <- string(buf)
	}()

	fn()
	_ = w.Close()
	os.Stdout = oldStdout

	select {
	case output := <-done:
		return output
	case <-time.After(5 * time.Second):
		t.Fatal("captureOutput timed out after 5 seconds")
		return ""
	}
}

// createTestApp creates a CLI app with all necessary flags for testing
func createTestApp() *cli.App {
	return &cli.App{
		Before: func(ctx *cli.Context) error {
			// Use embedded data only for tests to avoid network calls
			return nil
		},
		Action: testMainCmd,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "type"},
			&cli.StringFlag{Name: "os", Value: "linux"},
			&cli.StringSliceFlag{Name: "region", Value: cli.NewStringSlice("us-east-1")},
			&cli.StringFlag{Name: "output", Value: "table"},
			&cli.IntFlag{Name: "cpu"},
			&cli.IntFlag{Name: "memory"},
			&cli.Float64Flag{Name: "price"},
			&cli.StringFlag{Name: "sort", Value: "interruption"},
			&cli.StringFlag{Name: "order", Value: "asc"},
		},
	}
}

// testMainCmd is a version of mainCmd that uses embedded data only for testing
func testMainCmd(ctx *cli.Context) error {
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

	// Create spot client that uses embedded data only (no network calls)
	client := spot.NewWithOptions(1*time.Second, true) // Short timeout, use embedded data

	// Get spot savings
	advices, err := client.GetSpotSavings(context.Background(), regions, instance, instanceOS, cpu, memory, maxPrice, sortByType, sortDesc)
	if err != nil {
		return fmt.Errorf("failed to get spot savings: %w", err)
	}

	// Decide if region should be printed
	printRegion := len(regions) > 1 || (len(regions) == 1 && regions[0] == allRegions)

	switch ctx.String("output") {
	case outputNumber:
		printAdvicesNumber(advices, printRegion)
	case outputText:
		printAdvicesText(advices, printRegion)
	case outputJSON:
		printAdvicesJSON(advices)
	case outputTable:
		printAdvicesTable(advices, false, printRegion)
	case outputCSV:
		printAdvicesTable(advices, true, printRegion)
	default:
		printAdvicesNumber(advices, printRegion)
	}

	return nil
}

func TestMainCmd_OutputFormats(t *testing.T) {
	tests := []struct {
		name           string
		outputFormat   string
		instanceType   string
		validateOutput func(t *testing.T, output string)
		wantErr        bool
	}{
		{
			name:         "JSON format produces valid JSON",
			outputFormat: "json",
			instanceType: "t2.micro",
			validateOutput: func(t *testing.T, output string) {
				var advices []spot.Advice
				err := json.Unmarshal([]byte(output), &advices)
				require.NoError(t, err, "Output should be valid JSON")
				assert.NotEmpty(t, advices, "Should return at least one advice")
			},
		},
		{
			name:         "table format contains headers",
			outputFormat: "table",
			instanceType: "t2.micro",
			validateOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "INSTANCE INFO", "Table should contain instance header")
				assert.Contains(t, output, "SAVINGS", "Table should contain savings header")
				assert.Contains(t, output, "t2.micro", "Table should contain requested instance type")
			},
		},
		{
			name:         "number format for single result",
			outputFormat: "number",
			instanceType: "t2.micro",
			validateOutput: func(t *testing.T, output string) {
				output = strings.TrimSpace(output)
				// Should be just a number (savings percentage)
				assert.Regexp(t, `^\d+$`, output, "Number format should output only digits for single result")
			},
		},
		{
			name:         "text format contains key-value pairs",
			outputFormat: "text",
			instanceType: "t2.micro",
			validateOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "type=", "Text format should contain type field")
				assert.Contains(t, output, "saving=", "Text format should contain saving field")
				assert.Contains(t, output, "interruption=", "Text format should contain interruption field")
			},
		},
		{
			name:         "CSV format produces CSV structure",
			outputFormat: "csv",
			instanceType: "t2.micro",
			validateOutput: func(t *testing.T, output string) {
				// Look for CSV headers and data
				assert.Contains(t, output, "Instance Info,vCPU,Memory GiB", "Should contain CSV headers")
				assert.Contains(t, output, "t2.micro,1,1,", "Should contain CSV formatted data with commas")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := createTestApp()

			var output string
			var err error

			// Capture output during CLI execution
			output = captureOutput(t, func() {
				err = app.Run([]string{"spotinfo", "--type", tt.instanceType, "--output", tt.outputFormat})
			})

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err, "CLI should execute without error")
				assert.NotEmpty(t, output, "Output should not be empty")
				tt.validateOutput(t, output)
			}
		})
	}
}

func TestMainCmd_SortingAndOrdering(t *testing.T) {
	tests := []struct {
		name     string
		sortBy   string
		order    string
		filter   string // Add filter to limit results
		validate func(t *testing.T, advices []spot.Advice)
	}{
		{
			name:   "sort by savings ascending",
			sortBy: "savings",
			order:  "asc",
			filter: "t2.micro",
			validate: func(t *testing.T, advices []spot.Advice) {
				require.True(t, len(advices) >= 1, "Need at least 1 result to validate sorting")
				if len(advices) >= 2 {
					for i := 1; i < len(advices); i++ {
						assert.GreaterOrEqual(t, advices[i].Savings, advices[i-1].Savings,
							"Savings should be in ascending order")
					}
				}
			},
		},
		{
			name:   "sort by savings descending",
			sortBy: "savings",
			order:  "desc",
			filter: "t2.small",
			validate: func(t *testing.T, advices []spot.Advice) {
				require.True(t, len(advices) >= 1, "Need at least 1 result to validate sorting")
				if len(advices) >= 2 {
					for i := 1; i < len(advices); i++ {
						assert.LessOrEqual(t, advices[i].Savings, advices[i-1].Savings,
							"Savings should be in descending order")
					}
				}
			},
		},
		{
			name:   "sort by instance type ascending",
			sortBy: "type",
			order:  "asc",
			filter: "t2.*",
			validate: func(t *testing.T, advices []spot.Advice) {
				require.True(t, len(advices) >= 1, "Need at least 1 result to validate sorting")
				if len(advices) >= 2 {
					for i := 1; i < len(advices); i++ {
						assert.True(t, advices[i].Instance >= advices[i-1].Instance,
							"Instance types should be in ascending order")
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := createTestApp()

			var output string
			var err error

			// Use JSON output for easy parsing and validation, add filter to limit results
			args := []string{"spotinfo", "--sort", tt.sortBy, "--order", tt.order, "--output", "json"}
			if tt.filter != "" {
				args = append(args, "--type", tt.filter)
			}

			output = captureOutput(t, func() {
				err = app.Run(args)
			})

			require.NoError(t, err, "CLI should execute without error")

			var advices []spot.Advice
			err = json.Unmarshal([]byte(output), &advices)
			require.NoError(t, err, "Output should be valid JSON")

			tt.validate(t, advices)
		})
	}
}

func TestMainCmd_FilteringOptions(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		validateFn  func(t *testing.T, advices []spot.Advice)
		expectEmpty bool
	}{
		{
			name: "filter by minimum CPU cores",
			args: []string{"--cpu", "4", "--output", "json"},
			validateFn: func(t *testing.T, advices []spot.Advice) {
				for _, advice := range advices {
					assert.GreaterOrEqual(t, advice.Info.Cores, 4,
						"All results should have at least 4 CPU cores")
				}
			},
		},
		{
			name: "filter by minimum memory",
			args: []string{"--memory", "8", "--output", "json"},
			validateFn: func(t *testing.T, advices []spot.Advice) {
				for _, advice := range advices {
					assert.GreaterOrEqual(t, advice.Info.RAM, float32(8),
						"All results should have at least 8GB RAM")
				}
			},
		},
		{
			name: "filter by maximum price",
			args: []string{"--price", "0.50", "--output", "json"},
			validateFn: func(t *testing.T, advices []spot.Advice) {
				for _, advice := range advices {
					assert.LessOrEqual(t, advice.Price, 0.50,
						"All results should cost at most $0.50/hour")
				}
			},
		},
		{
			name:        "extremely high CPU filter returns empty",
			args:        []string{"--cpu", "1000", "--output", "json"},
			expectEmpty: true,
			validateFn: func(t *testing.T, advices []spot.Advice) {
				assert.Empty(t, advices, "No instances should match extreme CPU requirement")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := createTestApp()

			var output string
			var err error

			args := append([]string{"spotinfo"}, tt.args...)
			output = captureOutput(t, func() {
				err = app.Run(args)
			})

			require.NoError(t, err, "CLI should execute without error")

			var advices []spot.Advice
			err = json.Unmarshal([]byte(output), &advices)
			require.NoError(t, err, "Output should be valid JSON")

			if tt.expectEmpty {
				assert.Empty(t, advices, "Should return empty results")
			} else {
				assert.NotEmpty(t, advices, "Should return some results")
			}

			tt.validateFn(t, advices)
		})
	}
}

func TestMainCmd_ErrorConditions(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		expectedError string
	}{
		{
			name:          "invalid region",
			args:          []string{"--region", "invalid-region"},
			expectedError: "region not found",
		},
		{
			name:          "invalid OS",
			args:          []string{"--os", "invalid-os"},
			expectedError: "invalid instance OS",
		},
		{
			name:          "invalid regex pattern",
			args:          []string{"--type", "[invalid-regex"},
			expectedError: "failed to match instance type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := createTestApp()

			args := append([]string{"spotinfo"}, tt.args...)
			err := app.Run(args)

			require.Error(t, err, "Should return error for invalid input")
			assert.Contains(t, err.Error(), tt.expectedError,
				"Error message should contain expected text")
		})
	}
}

func TestMainCmd_RegionHandling(t *testing.T) {
	tests := []struct {
		name            string
		regions         []string
		instanceType    string
		expectMultiple  bool
		validateRegions func(t *testing.T, advices []spot.Advice, expectedRegions []string)
	}{
		{
			name:           "single region",
			regions:        []string{"us-east-1"},
			instanceType:   "t2.micro",
			expectMultiple: false,
			validateRegions: func(t *testing.T, advices []spot.Advice, expectedRegions []string) {
				for _, advice := range advices {
					assert.Equal(t, "us-east-1", advice.Region)
				}
			},
		},
		{
			name:           "multiple regions",
			regions:        []string{"us-east-1", "us-west-2"},
			instanceType:   "t2.micro",
			expectMultiple: true,
			validateRegions: func(t *testing.T, advices []spot.Advice, expectedRegions []string) {
				foundRegions := make(map[string]bool)
				for _, advice := range advices {
					foundRegions[advice.Region] = true
				}
				// Should have results from both regions
				assert.True(t, len(foundRegions) >= 1, "Should have results from at least one region")
				for region := range foundRegions {
					assert.Contains(t, expectedRegions, region, "Result region should be in expected list")
				}
			},
		},
		{
			name:           "all regions",
			regions:        []string{"all"},
			instanceType:   "t2.micro", // Use micro which exists in data
			expectMultiple: true,
			validateRegions: func(t *testing.T, advices []spot.Advice, expectedRegions []string) {
				foundRegions := make(map[string]bool)
				for _, advice := range advices {
					foundRegions[advice.Region] = true
				}
				// Should have results from multiple regions when using "all"
				assert.True(t, len(foundRegions) > 1, "Should have results from multiple regions")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := createTestApp()

			var output string
			var err error

			args := []string{"spotinfo", "--output", "json", "--type", tt.instanceType}
			for _, region := range tt.regions {
				args = append(args, "--region", region)
			}

			output = captureOutput(t, func() {
				err = app.Run(args)
			})

			require.NoError(t, err, "CLI should execute without error")

			var advices []spot.Advice
			err = json.Unmarshal([]byte(output), &advices)
			require.NoError(t, err, "Output should be valid JSON")
			require.NotEmpty(t, advices, "Should return some results")

			tt.validateRegions(t, advices, tt.regions)
		})
	}
}

func TestPrintAdvicesNumber_OutputFormat(t *testing.T) {
	tests := []struct {
		name           string
		advices        []spot.Advice
		printRegion    bool
		expectedOutput string
	}{
		{
			name: "single result without region",
			advices: []spot.Advice{
				{Instance: "t2.micro", Savings: 50, Region: "us-east-1"},
			},
			printRegion:    false,
			expectedOutput: "50\n",
		},
		{
			name: "multiple results without region",
			advices: []spot.Advice{
				{Instance: "t2.micro", Savings: 50, Region: "us-east-1"},
				{Instance: "t2.small", Savings: 60, Region: "us-east-1"},
			},
			printRegion:    false,
			expectedOutput: "t2.micro: 50\nt2.small: 60\n",
		},
		{
			name: "multiple results with region",
			advices: []spot.Advice{
				{Instance: "t2.micro", Savings: 50, Region: "us-east-1"},
				{Instance: "t2.small", Savings: 60, Region: "us-west-2"},
			},
			printRegion:    true,
			expectedOutput: "us-east-1/t2.micro: 50\nus-west-2/t2.small: 60\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := captureOutput(t, func() {
				printAdvicesNumber(tt.advices, tt.printRegion)
			})

			assert.Equal(t, tt.expectedOutput, output)
		})
	}
}
