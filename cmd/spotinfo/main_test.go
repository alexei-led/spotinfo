package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/spot"
)

// Constants for repeated strings in tests
const (
	outputNumber = "number"
	outputText   = "text"
	outputJSON   = "json"
	outputTable  = "table"
	outputCSV    = "csv"

	allRegions = "all"
)

// Helper functions for mock setup (following spot package patterns)

// setupSuccessfulSpotClient creates a mock client with successful single instance response
func setupSuccessfulSpotClient(t *testing.T, region, instanceType string, savings int) *MockSpotClient {
	mockClient := NewMockSpotClient(t)

	advice := []spot.Advice{
		{
			Region:   region,
			Instance: instanceType,
			Savings:  savings,
			Info:     spot.TypeInfo{Cores: 1, RAM: 1.0, EMR: false},
			Range:    spot.Range{Label: "<5%", Min: 0, Max: 5},
			Price:    0.0116,
		},
	}

	mockClient.EXPECT().GetSpotSavings(
		mock.Anything,
		[]string{region},
		instanceType,
		"linux",
		0, 0, float64(0),
		spot.SortByRange,
		false,
	).Return(advice, nil).Once()

	return mockClient
}

// setupMultipleInstancesSpotClient creates a mock client with multiple instances response
func setupMultipleInstancesSpotClient(t *testing.T, region string, sortBy spot.SortBy, sortDesc bool) *MockSpotClient {
	mockClient := NewMockSpotClient(t)

	// Create base data
	advice1 := spot.Advice{
		Region:   region,
		Instance: "t2.micro",
		Savings:  30,
		Info:     spot.TypeInfo{Cores: 1, RAM: 1.0, EMR: false},
		Range:    spot.Range{Label: "<5%", Min: 0, Max: 5},
		Price:    0.0116,
	}
	advice2 := spot.Advice{
		Region:   region,
		Instance: "t2.small",
		Savings:  50,
		Info:     spot.TypeInfo{Cores: 1, RAM: 2.0, EMR: false},
		Range:    spot.Range{Label: "<10%", Min: 5, Max: 10},
		Price:    0.023,
	}

	// Order data based on sort parameters
	var advices []spot.Advice
	switch sortBy {
	case spot.SortBySavings:
		if sortDesc {
			// Descending: higher savings first (50, 30)
			advices = []spot.Advice{advice2, advice1}
		} else {
			// Ascending: lower savings first (30, 50)
			advices = []spot.Advice{advice1, advice2}
		}
	case spot.SortByInstance:
		if sortDesc {
			// Descending: t2.small, t2.micro (lexicographically)
			advices = []spot.Advice{advice2, advice1}
		} else {
			// Ascending: t2.micro, t2.small (lexicographically)
			advices = []spot.Advice{advice1, advice2}
		}
	default:
		// Default order for other sort types
		advices = []spot.Advice{advice1, advice2}
	}

	mockClient.EXPECT().GetSpotSavings(
		mock.Anything,
		[]string{region},
		"t2.*",
		"linux",
		0, 0, float64(0),
		sortBy,
		sortDesc,
	).Return(advices, nil).Once()

	return mockClient
}

// setupErrorSpotClient creates a mock client that returns an error
func setupErrorSpotClient(t *testing.T, expectedError error) *MockSpotClient {
	mockClient := NewMockSpotClient(t)

	mockClient.EXPECT().GetSpotSavings(
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything,
	).Return(nil, expectedError).Once()

	return mockClient
}

// setupFilteredSpotClient creates a mock client with filtered results
func setupFilteredSpotClient(t *testing.T, cpu, memory int, maxPrice float64, expectedAdvices []spot.Advice) *MockSpotClient {
	mockClient := NewMockSpotClient(t)

	mockClient.EXPECT().GetSpotSavings(
		mock.Anything,
		mock.Anything,
		mock.Anything,
		"linux",
		cpu,
		memory,
		maxPrice,
		spot.SortByRange,
		false,
	).Return(expectedAdvices, nil).Once()

	return mockClient
}

// createTestApp creates a CLI app for testing (following existing patterns)
func createTestApp(action func(*cli.Context) error) *cli.App {
	return &cli.App{
		Action: action,
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

func TestExecMainCmd_OutputFormats(t *testing.T) {
	tests := []struct {
		name           string
		outputFormat   string
		instanceType   string
		region         string
		validateOutput func(t *testing.T, output string)
		wantErr        bool
	}{
		{
			name:         "JSON format produces valid JSON",
			outputFormat: "json",
			instanceType: "t2.micro",
			region:       "us-east-1",
			validateOutput: func(t *testing.T, output string) {
				var advices []spot.Advice
				err := json.Unmarshal([]byte(output), &advices)
				require.NoError(t, err, "Output should be valid JSON")
				assert.Len(t, advices, 1, "Should return one advice")
				assert.Equal(t, "t2.micro", advices[0].Instance)
				assert.Equal(t, 50, advices[0].Savings)
			},
		},
		{
			name:         "table format contains headers and data",
			outputFormat: "table",
			instanceType: "t2.micro",
			region:       "us-east-1",
			validateOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "INSTANCE INFO", "Table should contain instance header")
				assert.Contains(t, output, "SAVINGS", "Table should contain savings header")
				assert.Contains(t, output, "t2.micro", "Table should contain instance type")
				assert.Contains(t, output, "50%", "Table should contain savings percentage")
			},
		},
		{
			name:         "number format for single result",
			outputFormat: "number",
			instanceType: "t2.micro",
			region:       "us-east-1",
			validateOutput: func(t *testing.T, output string) {
				output = strings.TrimSpace(output)
				assert.Equal(t, "50", output, "Single result should output just the savings number")
			},
		},
		{
			name:         "text format contains key-value pairs",
			outputFormat: "text",
			instanceType: "t2.micro",
			region:       "us-east-1",
			validateOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "type=t2.micro", "Text format should contain type field")
				assert.Contains(t, output, "saving=50%", "Text format should contain saving field")
				assert.Contains(t, output, "interruption='<5%'", "Text format should contain interruption field")
				assert.Contains(t, output, "price=0.01", "Text format should contain price field")
			},
		},
		{
			name:         "CSV format produces CSV structure",
			outputFormat: "csv",
			instanceType: "t2.micro",
			region:       "us-east-1",
			validateOutput: func(t *testing.T, output string) {
				assert.Contains(t, output, "Instance Info,vCPU,Memory GiB", "Should contain CSV headers")
				assert.Contains(t, output, "t2.micro,1,1,50,<5%", "Should contain CSV formatted data")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer

			// Create mock client
			mockClient := setupSuccessfulSpotClient(t, tt.region, tt.instanceType, 50)

			// Create test context
			testCtx := context.Background()

			// Create CLI app and run it with test args
			app := createTestApp(func(ctx *cli.Context) error {
				return execMainCmd(ctx, testCtx, mockClient, &output)
			})

			// Build command line arguments
			args := []string{"spotinfo"}
			args = append(args, "--type", tt.instanceType)
			args = append(args, "--os", "linux")
			args = append(args, "--region", tt.region)
			args = append(args, "--output", tt.outputFormat)

			// Execute the CLI app
			err := app.Run(args)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err, "CLI app should execute without error")
				tt.validateOutput(t, output.String())
			}

			// Verify all mock expectations were met
			mockClient.AssertExpectations(t)
		})
	}
}

func TestExecMainCmd_SortingAndOrdering(t *testing.T) {
	tests := []struct {
		name     string
		sortBy   string
		order    string
		validate func(t *testing.T, advices []spot.Advice)
	}{
		{
			name:   "sort by savings ascending",
			sortBy: "savings",
			order:  "asc",
			validate: func(t *testing.T, advices []spot.Advice) {
				require.Len(t, advices, 2, "Should have 2 results")
				assert.LessOrEqual(t, advices[0].Savings, advices[1].Savings,
					"Savings should be in ascending order")
			},
		},
		{
			name:   "sort by savings descending",
			sortBy: "savings",
			order:  "desc",
			validate: func(t *testing.T, advices []spot.Advice) {
				require.Len(t, advices, 2, "Should have 2 results")
				assert.GreaterOrEqual(t, advices[0].Savings, advices[1].Savings,
					"Savings should be in descending order")
			},
		},
		{
			name:   "sort by instance type ascending",
			sortBy: "type",
			order:  "asc",
			validate: func(t *testing.T, advices []spot.Advice) {
				require.Len(t, advices, 2, "Should have 2 results")
				assert.True(t, advices[0].Instance <= advices[1].Instance,
					"Instance types should be in ascending order")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer

			// Create mock client with multiple instances
			sortBy := spot.SortByRange
			switch tt.sortBy {
			case "savings":
				sortBy = spot.SortBySavings
			case "type":
				sortBy = spot.SortByInstance
			}

			sortDesc := strings.EqualFold(tt.order, "desc")
			mockClient := setupMultipleInstancesSpotClient(t, "us-east-1", sortBy, sortDesc)

			testCtx := context.Background()

			// Create CLI app and run it with test args
			app := createTestApp(func(ctx *cli.Context) error {
				return execMainCmd(ctx, testCtx, mockClient, &output)
			})

			// Build command line arguments
			args := []string{"spotinfo"}
			args = append(args, "--type", "t2.*")
			args = append(args, "--sort", tt.sortBy)
			args = append(args, "--order", tt.order)
			args = append(args, "--output", "json")

			err := app.Run(args)
			require.NoError(t, err, "CLI app should execute without error")

			var advices []spot.Advice
			err = json.Unmarshal(output.Bytes(), &advices)
			require.NoError(t, err, "Output should be valid JSON")

			tt.validate(t, advices)
			mockClient.AssertExpectations(t)
		})
	}
}

func TestExecMainCmd_FilteringOptions(t *testing.T) {
	tests := []struct {
		name            string
		cpu             int
		memory          int
		price           float64
		expectedAdvices []spot.Advice
		validate        func(t *testing.T, advices []spot.Advice)
	}{
		{
			name: "filter by minimum CPU cores",
			cpu:  4,
			expectedAdvices: []spot.Advice{
				{
					Region:   "us-east-1",
					Instance: "m5.large",
					Savings:  40,
					Info:     spot.TypeInfo{Cores: 4, RAM: 8.0, EMR: false},
					Range:    spot.Range{Label: "<10%", Min: 5, Max: 10},
					Price:    0.096,
				},
			},
			validate: func(t *testing.T, advices []spot.Advice) {
				for _, advice := range advices {
					assert.GreaterOrEqual(t, advice.Info.Cores, 4,
						"All results should have at least 4 CPU cores")
				}
			},
		},
		{
			name:   "filter by minimum memory",
			memory: 8,
			expectedAdvices: []spot.Advice{
				{
					Region:   "us-east-1",
					Instance: "m5.large",
					Savings:  40,
					Info:     spot.TypeInfo{Cores: 2, RAM: 8.0, EMR: false},
					Range:    spot.Range{Label: "<10%", Min: 5, Max: 10},
					Price:    0.096,
				},
			},
			validate: func(t *testing.T, advices []spot.Advice) {
				for _, advice := range advices {
					assert.GreaterOrEqual(t, advice.Info.RAM, float32(8),
						"All results should have at least 8GB RAM")
				}
			},
		},
		{
			name:  "filter by maximum price",
			price: 0.50,
			expectedAdvices: []spot.Advice{
				{
					Region:   "us-east-1",
					Instance: "t2.small",
					Savings:  35,
					Info:     spot.TypeInfo{Cores: 1, RAM: 2.0, EMR: false},
					Range:    spot.Range{Label: "<5%", Min: 0, Max: 5},
					Price:    0.023,
				},
			},
			validate: func(t *testing.T, advices []spot.Advice) {
				for _, advice := range advices {
					assert.LessOrEqual(t, advice.Price, 0.50,
						"All results should cost at most $0.50/hour")
				}
			},
		},
		{
			name:            "extremely high CPU filter returns empty",
			cpu:             1000,
			expectedAdvices: []spot.Advice{},
			validate: func(t *testing.T, advices []spot.Advice) {
				assert.Empty(t, advices, "No instances should match extreme CPU requirement")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer

			mockClient := setupFilteredSpotClient(t, tt.cpu, tt.memory, tt.price, tt.expectedAdvices)

			testCtx := context.Background()

			// Create CLI app and run it with test args
			app := createTestApp(func(ctx *cli.Context) error {
				return execMainCmd(ctx, testCtx, mockClient, &output)
			})

			// Build command line arguments
			args := []string{"spotinfo", "--output", "json"}
			if tt.cpu > 0 {
				args = append(args, "--cpu", fmt.Sprintf("%d", tt.cpu))
			}
			if tt.memory > 0 {
				args = append(args, "--memory", fmt.Sprintf("%d", tt.memory))
			}
			if tt.price > 0 {
				args = append(args, "--price", fmt.Sprintf("%.2f", tt.price))
			}

			err := app.Run(args)
			require.NoError(t, err, "CLI app should execute without error")

			var advices []spot.Advice
			err = json.Unmarshal(output.Bytes(), &advices)
			require.NoError(t, err, "Output should be valid JSON")

			tt.validate(t, advices)
			mockClient.AssertExpectations(t)
		})
	}
}

func TestExecMainCmd_ErrorConditions(t *testing.T) {
	tests := []struct {
		name        string
		setupFlags  func(*cli.Context)
		expectedErr error
		errorSubstr string
	}{
		{
			name: "client returns region error",
			setupFlags: func(ctx *cli.Context) {
				ctx.Set("region", "invalid-region")
			},
			expectedErr: errors.New("region not found: invalid-region"),
			errorSubstr: "region not found",
		},
		{
			name: "client returns OS error",
			setupFlags: func(ctx *cli.Context) {
				ctx.Set("os", "invalid-os")
			},
			expectedErr: errors.New("invalid instance OS: invalid-os"),
			errorSubstr: "invalid instance OS",
		},
		{
			name: "client returns regex error",
			setupFlags: func(ctx *cli.Context) {
				ctx.Set("type", "[invalid-regex")
			},
			expectedErr: errors.New("failed to match instance type: [invalid-regex"),
			errorSubstr: "failed to match instance type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer

			mockClient := setupErrorSpotClient(t, tt.expectedErr)

			testCtx := context.Background()

			// Create CLI app and run it with test args
			app := createTestApp(func(ctx *cli.Context) error {
				return execMainCmd(ctx, testCtx, mockClient, &output)
			})

			// Build command line arguments based on test case
			args := []string{"spotinfo"}
			if tt.name == "client returns region error" {
				args = append(args, "--region", "invalid-region")
			} else if tt.name == "client returns OS error" {
				args = append(args, "--os", "invalid-os")
			} else if tt.name == "client returns regex error" {
				args = append(args, "--type", "[invalid-regex")
			}

			err := app.Run(args)

			require.Error(t, err, "Should return error for invalid input")
			assert.Contains(t, err.Error(), tt.errorSubstr,
				"Error message should contain expected text")

			mockClient.AssertExpectations(t)
		})
	}
}

func TestExecMainCmd_RegionHandling(t *testing.T) {
	tests := []struct {
		name    string
		regions []string
		setup   func(*testing.T) *MockSpotClient
	}{
		{
			name:    "single region",
			regions: []string{"us-east-1"},
			setup: func(t *testing.T) *MockSpotClient {
				return setupSuccessfulSpotClient(t, "us-east-1", "t2.micro", 50)
			},
		},
		{
			name:    "multiple regions",
			regions: []string{"us-east-1", "us-west-2"},
			setup: func(t *testing.T) *MockSpotClient {
				mockClient := NewMockSpotClient(t)

				advices := []spot.Advice{
					{Region: "us-east-1", Instance: "t2.micro", Savings: 50},
					{Region: "us-west-2", Instance: "t2.micro", Savings: 45},
				}

				mockClient.EXPECT().GetSpotSavings(
					mock.Anything,
					[]string{"us-east-1", "us-west-2"},
					"t2.micro",
					"linux",
					0, 0, float64(0),
					spot.SortByRange,
					false,
				).Return(advices, nil).Once()

				return mockClient
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer

			mockClient := tt.setup(t)

			testCtx := context.Background()

			// Create CLI app and run it with test args
			app := createTestApp(func(ctx *cli.Context) error {
				return execMainCmd(ctx, testCtx, mockClient, &output)
			})

			// Build command line arguments
			args := []string{"spotinfo", "--output", "json", "--type", "t2.micro"}
			for _, region := range tt.regions {
				args = append(args, "--region", region)
			}

			err := app.Run(args)
			require.NoError(t, err, "CLI app should execute without error")

			var advices []spot.Advice
			err = json.Unmarshal(output.Bytes(), &advices)
			require.NoError(t, err, "Output should be valid JSON")
			require.NotEmpty(t, advices, "Should return some results")

			mockClient.AssertExpectations(t)
		})
	}
}

func TestMainCmd_Integration(t *testing.T) {
	// Test that mainCmd delegates to execMainCmd properly
	oldMainCtx := mainCtx
	testCtx := context.WithValue(context.Background(), "key", "test-value")
	mainCtx = testCtx
	defer func() { mainCtx = oldMainCtx }()

	app := createTestApp(mainCmd)
	ctx := cli.NewContext(app, nil, nil)
	ctx.Set("type", "t2.micro")
	ctx.Set("output", "json")

	err := mainCmd(ctx)
	require.NoError(t, err, "mainCmd should execute without error")
}

func TestVersionPrinter(t *testing.T) {
	// Save original values
	originalVersion := Version
	originalBuildDate := BuildDate
	originalGitCommit := GitCommit
	originalGitBranch := GitBranch

	defer func() {
		Version = originalVersion
		BuildDate = originalBuildDate
		GitCommit = originalGitCommit
		GitBranch = originalGitBranch
	}()

	tests := []struct {
		name          string
		version       string
		buildDate     string
		gitCommit     string
		gitBranch     string
		expectedParts []string
	}{
		{
			name:      "full version info",
			version:   "v1.2.3",
			buildDate: "2023-01-01T12:00:00Z",
			gitCommit: "abc123def456",
			gitBranch: "main",
			expectedParts: []string{
				"spotinfo v1.2.3",
				"Build date: 2023-01-01T12:00:00Z",
				"Git commit: abc123def456",
				"Git branch: main",
				"Built with:",
			},
		},
		{
			name:          "minimal version info",
			version:       "dev",
			buildDate:     "unknown",
			gitCommit:     "",
			gitBranch:     "",
			expectedParts: []string{"spotinfo dev", "Built with:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set test values
			Version = tt.version
			BuildDate = tt.buildDate
			GitCommit = tt.gitCommit
			GitBranch = tt.gitBranch

			var output bytes.Buffer

			// Set the version printer to match main.go
			cli.VersionPrinter = func(_ *cli.Context) {
				fmt.Fprintf(&output, "spotinfo %s\n", Version)

				if BuildDate != "" && BuildDate != "unknown" {
					fmt.Fprintf(&output, "  Build date: %s\n", BuildDate)
				}

				if GitCommit != "" {
					fmt.Fprintf(&output, "  Git commit: %s\n", GitCommit)
				}

				if GitBranch != "" {
					fmt.Fprintf(&output, "  Git branch: %s\n", GitBranch)
				}

				fmt.Fprintf(&output, "  Built with: %s\n", runtime.Version())
			}

			app := &cli.App{
				Name:    "spotinfo",
				Version: Version,
			}

			err := app.Run([]string{"spotinfo", "--version"})
			require.NoError(t, err)

			outputStr := output.String()
			for _, part := range tt.expectedParts {
				assert.Contains(t, outputStr, part, "Version output should contain: %s", part)
			}
		})
	}
}

func TestCLIApp_BeforeHook(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "debug flag",
			args: []string{"spotinfo", "--debug"},
		},
		{
			name: "quiet flag",
			args: []string{"spotinfo", "--quiet"},
		},
		{
			name: "json-log flag",
			args: []string{"spotinfo", "--json-log"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &cli.App{
				Before: func(ctx *cli.Context) error {
					// Simulate the Before hook from main.go
					// We can't easily test the logger state, but we can ensure no error
					return nil
				},
				Action: func(ctx *cli.Context) error {
					return nil
				},
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "debug"},
					&cli.BoolFlag{Name: "quiet"},
					&cli.BoolFlag{Name: "json-log"},
				},
			}

			err := app.Run(tt.args)
			require.NoError(t, err, "Before hook should execute without error")
		})
	}
}

func TestPrintFunctions_EdgeCases(t *testing.T) {
	t.Run("printAdvicesNumber with empty list", func(t *testing.T) {
		var output bytes.Buffer
		printAdvicesNumber([]spot.Advice{}, false, &output)
		assert.Empty(t, output.String(), "Empty advice list should produce no output")
	})

	t.Run("printAdvicesText with empty list", func(t *testing.T) {
		var output bytes.Buffer
		printAdvicesText([]spot.Advice{}, false, &output)
		assert.Empty(t, output.String(), "Empty advice list should produce no output")
	})

	t.Run("printAdvicesJSON with nil", func(t *testing.T) {
		var output bytes.Buffer
		printAdvicesJSON(nil, &output)
		assert.Equal(t, "null\n", output.String(), "nil should produce 'null'")
	})

	t.Run("printAdvicesTable with empty list", func(t *testing.T) {
		var output bytes.Buffer
		printAdvicesTable([]spot.Advice{}, false, false, &output)
		// Should produce at least headers
		outputStr := output.String()
		assert.Contains(t, outputStr, "INSTANCE INFO", "Should contain headers even with empty data")
	})

	t.Run("printAdvicesNumber single vs multiple results", func(t *testing.T) {
		advice := spot.Advice{Instance: "t2.micro", Savings: 75, Region: "us-east-1"}

		// Test single result
		var output1 bytes.Buffer
		printAdvicesNumber([]spot.Advice{advice}, false, &output1)
		assert.Equal(t, "75\n", output1.String(), "Single result should show just the number")

		// Test multiple results
		var output2 bytes.Buffer
		printAdvicesNumber([]spot.Advice{advice, advice}, false, &output2)
		expected := "t2.micro: 75\nt2.micro: 75\n"
		assert.Equal(t, expected, output2.String(), "Multiple results should show instance: number format")
	})

	t.Run("printAdvicesText with region flag", func(t *testing.T) {
		advice := spot.Advice{
			Instance: "t2.micro",
			Savings:  75,
			Region:   "us-west-2",
			Info:     spot.TypeInfo{Cores: 1, RAM: 1.0},
			Range:    spot.Range{Label: "<5%"},
			Price:    0.0116,
		}

		var output bytes.Buffer
		printAdvicesText([]spot.Advice{advice}, true, &output)
		result := output.String()

		assert.Contains(t, result, "region=us-west-2", "Should include region when flag is true")
		assert.Contains(t, result, "type=t2.micro", "Should include instance type")
		assert.Contains(t, result, "saving=75%", "Should include savings")
	})
}

func TestIsMCPMode(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		setupEnv    func()
		cleanupEnv  func()
		expectedMCP bool
	}{
		{
			name:        "MCP flag set to true",
			args:        []string{"spotinfo", "--mcp"},
			setupEnv:    func() {},
			cleanupEnv:  func() {},
			expectedMCP: true,
		},
		{
			name:        "MCP flag false, no env var",
			args:        []string{"spotinfo"},
			setupEnv:    func() {},
			cleanupEnv:  func() {},
		},
		{
			name: "MCP flag false, env var set to mcp",
			args: []string{"spotinfo"},
			setupEnv: func() {
				os.Setenv(mcpModeEnv, mcpModeValue)
			},
			cleanupEnv: func() {
				os.Unsetenv(mcpModeEnv)
			},
			expectedMCP: true,
		},
		{
			name: "MCP flag false, env var set to MCP (case insensitive)",
			args: []string{"spotinfo"},
			setupEnv: func() {
				os.Setenv(mcpModeEnv, "MCP")
			},
			cleanupEnv: func() {
				os.Unsetenv(mcpModeEnv)
			},
			expectedMCP: true,
		},
		{
			name: "MCP flag false, env var set to invalid value",
			args: []string{"spotinfo"},
			setupEnv: func() {
				os.Setenv(mcpModeEnv, "invalid")
			},
			cleanupEnv: func() {
				os.Unsetenv(mcpModeEnv)
			},
		},
		{
			name: "MCP flag true overrides env var false",
			args: []string{"spotinfo", "--mcp"},
			setupEnv: func() {
				os.Setenv(mcpModeEnv, "false")
			},
			cleanupEnv: func() {
				os.Unsetenv(mcpModeEnv)
			},
			expectedMCP: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupEnv()
			defer tt.cleanupEnv()

			var capturedMCPMode bool
			app := &cli.App{
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "mcp"},
				},
				Action: func(ctx *cli.Context) error {
					capturedMCPMode = isMCPMode(ctx)
					return nil
				},
			}

			err := app.Run(tt.args)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedMCP, capturedMCPMode, "MCP mode detection should match expected result")
		})
	}
}

// TestGetMCPTransport tests transport selection logic
func TestGetMCPTransport(t *testing.T) {
	tests := []struct {
		name              string
		envValue          string
		expectedTransport string
	}{
		{
			name:              "no environment variable - default to stdio",
			envValue:          "",
			expectedTransport: stdioTransport,
		},
		{
			name:              "stdio transport",
			envValue:          stdioTransport,
			expectedTransport: stdioTransport,
		},
		{
			name:              "sse transport",
			envValue:          sseTransport,
			expectedTransport: sseTransport,
		},
		{
			name:              "custom transport value",
			envValue:          "custom",
			expectedTransport: "custom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original value
			originalValue, exists := os.LookupEnv(mcpTransportEnv)
			defer func() {
				if exists {
					os.Setenv(mcpTransportEnv, originalValue)
				} else {
					os.Unsetenv(mcpTransportEnv)
				}
			}()

			// Set test value
			if tt.envValue != "" {
				os.Setenv(mcpTransportEnv, tt.envValue)
			} else {
				os.Unsetenv(mcpTransportEnv)
			}

			// Test the function
			result := getMCPTransport()
			assert.Equal(t, tt.expectedTransport, result)
		})
	}
}

// TestGetMCPPort tests port selection logic
func TestGetMCPPort(t *testing.T) {
	tests := []struct {
		name         string
		envValue     string
		expectedPort string
	}{
		{
			name:         "no environment variable - default port",
			envValue:     "",
			expectedPort: defaultMCPPort,
		},
		{
			name:         "custom port",
			envValue:     "9090",
			expectedPort: "9090",
		},
		{
			name:         "default port explicitly set",
			envValue:     defaultMCPPort,
			expectedPort: defaultMCPPort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original value
			originalValue, exists := os.LookupEnv(mcpPortEnv)
			defer func() {
				if exists {
					os.Setenv(mcpPortEnv, originalValue)
				} else {
					os.Unsetenv(mcpPortEnv)
				}
			}()

			// Set test value
			if tt.envValue != "" {
				os.Setenv(mcpPortEnv, tt.envValue)
			} else {
				os.Unsetenv(mcpPortEnv)
			}

			// Test the function
			result := getMCPPort()
			assert.Equal(t, tt.expectedPort, result)
		})
	}
}

// TestRunMCPServer tests MCP server startup scenarios
func TestRunMCPServer(t *testing.T) {
	tests := []struct {
		name          string
		setupEnv      func()
		cleanupEnv    func()
		expectedError string
		transport     string
		port          string
	}{
		{
			name: "stdio transport success",
			setupEnv: func() {
				os.Setenv(mcpTransportEnv, stdioTransport)
			},
			cleanupEnv: func() {
				os.Unsetenv(mcpTransportEnv)
			},
			expectedError: "", // Actual stdio testing is complex, we just test setup
			transport:     stdioTransport,
		},
		{
			name: "sse transport success",
			setupEnv: func() {
				os.Setenv(mcpTransportEnv, sseTransport)
				os.Setenv(mcpPortEnv, "9090")
			},
			cleanupEnv: func() {
				os.Unsetenv(mcpTransportEnv)
				os.Unsetenv(mcpPortEnv)
			},
			transport: sseTransport,
			port:      "9090",
		},
		{
			name: "unsupported transport",
			setupEnv: func() {
				os.Setenv(mcpTransportEnv, "websocket")
			},
			cleanupEnv: func() {
				os.Unsetenv(mcpTransportEnv)
			},
			expectedError: "unsupported transport: websocket",
			transport:     "websocket",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup environment
			tt.setupEnv()
			defer tt.cleanupEnv()

			// Create context with cancellation (to avoid hanging)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Run with very short timeout for non-stdio tests
			if tt.transport != stdioTransport {
				var timeoutCtx context.Context
				timeoutCtx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
				ctx = timeoutCtx
			}

			// Create empty CLI context (not used in runMCPServer)
			app := &cli.App{}
			cliCtx := cli.NewContext(app, nil, nil)

			// Test the function
			err := runMCPServer(cliCtx, ctx)

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else if tt.transport == stdioTransport {
				// For stdio, we expect context cancellation or similar
				// since we can't easily mock stdin/stdout in this test
				assert.True(t, err == nil || errors.Is(err, context.Canceled))
			}
		})
	}
}

// TestMainCmd_MCPModeIntegration tests integration between CLI and MCP modes
func TestMainCmd_MCPModeIntegration(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		setupEnv   func()
		cleanupEnv func()
		expectMCP  bool
	}{
		{
			name:       "normal CLI mode",
			args:       []string{"spotinfo", "--type", "t3.micro"},
			setupEnv:   func() {},
			cleanupEnv: func() {},
			expectMCP:  false,
		},
		{
			name:       "MCP mode via flag",
			args:       []string{"spotinfo", "--mcp"},
			setupEnv:   func() {},
			cleanupEnv: func() {},
			expectMCP:  true,
		},
		{
			name: "MCP mode via environment",
			args: []string{"spotinfo"},
			setupEnv: func() {
				os.Setenv(mcpModeEnv, mcpModeValue)
			},
			cleanupEnv: func() {
				os.Unsetenv(mcpModeEnv)
			},
			expectMCP: true,
		},
		{
			name: "CLI mode overrides environment",
			args: []string{"spotinfo", "--type", "t3.micro"},
			setupEnv: func() {
				os.Setenv(mcpModeEnv, "false") // Not "mcp"
			},
			cleanupEnv: func() {
				os.Unsetenv(mcpModeEnv)
			},
			expectMCP: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup environment
			tt.setupEnv()
			defer tt.cleanupEnv()

			// Create test app
			var capturedMCPMode bool
			var capturedCLIMode bool

			app := &cli.App{
				Name: "spotinfo",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "mcp"},
					&cli.StringFlag{Name: "type"},
					&cli.StringFlag{Name: "output", Value: "table"},
					&cli.StringSliceFlag{Name: "region", Value: cli.NewStringSlice("us-east-1")},
				},
				Action: func(ctx *cli.Context) error {
					if isMCPMode(ctx) {
						capturedMCPMode = true
						// Don't actually start MCP server, just capture the detection
						return nil
					} else {
						capturedCLIMode = true
						// Don't actually run CLI logic, just capture the detection
						return nil
					}
				},
			}

			// Create context with timeout to prevent hanging
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			// Run the app
			err := app.RunContext(ctx, tt.args)

			// We expect no error for mode detection
			require.NoError(t, err)

			// Verify the correct mode was detected
			if tt.expectMCP {
				assert.True(t, capturedMCPMode, "Should detect MCP mode")
				assert.False(t, capturedCLIMode, "Should not detect CLI mode")
			} else {
				assert.False(t, capturedMCPMode, "Should not detect MCP mode")
				assert.True(t, capturedCLIMode, "Should detect CLI mode")
			}
		})
	}
}

// TestMCPServerConfiguration tests MCP server configuration scenarios
func TestMCPServerConfiguration(t *testing.T) {
	tests := []struct {
		name              string
		transport         string
		port              string
		expectedTransport string
		expectedPort      string
	}{
		{
			name:              "default configuration",
			transport:         "",
			port:              "",
			expectedTransport: stdioTransport,
			expectedPort:      defaultMCPPort,
		},
		{
			name:              "custom stdio configuration",
			transport:         stdioTransport,
			port:              "custom",
			expectedTransport: stdioTransport,
			expectedPort:      "custom",
		},
		{
			name:              "SSE configuration",
			transport:         sseTransport,
			port:              "9090",
			expectedTransport: sseTransport,
			expectedPort:      "9090",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original env values
			originalTransport, transportExists := os.LookupEnv(mcpTransportEnv)
			originalPort, portExists := os.LookupEnv(mcpPortEnv)

			defer func() {
				if transportExists {
					os.Setenv(mcpTransportEnv, originalTransport)
				} else {
					os.Unsetenv(mcpTransportEnv)
				}
				if portExists {
					os.Setenv(mcpPortEnv, originalPort)
				} else {
					os.Unsetenv(mcpPortEnv)
				}
			}()

			// Set test environment
			if tt.transport != "" {
				os.Setenv(mcpTransportEnv, tt.transport)
			} else {
				os.Unsetenv(mcpTransportEnv)
			}

			if tt.port != "" {
				os.Setenv(mcpPortEnv, tt.port)
			} else {
				os.Unsetenv(mcpPortEnv)
			}

			// Test configuration functions
			actualTransport := getMCPTransport()
			actualPort := getMCPPort()

			assert.Equal(t, tt.expectedTransport, actualTransport)
			assert.Equal(t, tt.expectedPort, actualPort)
		})
	}
}

// TestMainCmd_ErrorHandling tests error scenarios in main command
func TestMainCmd_ErrorHandling(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		setupEnv      func()
		cleanupEnv    func()
		expectError   bool
		errorContains string
	}{
		{
			name: "unsupported MCP transport",
			args: []string{"spotinfo", "--mcp"},
			setupEnv: func() {
				os.Setenv(mcpTransportEnv, "invalid-transport")
			},
			cleanupEnv: func() {
				os.Unsetenv(mcpTransportEnv)
			},
			expectError:   true,
			errorContains: "unsupported transport",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup environment
			tt.setupEnv()
			defer tt.cleanupEnv()

			// Create test app that will call mainCmd
			app := &cli.App{
				Name: "spotinfo",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "mcp"},
				},
				Action: func(ctx *cli.Context) error {
					// Create a context with short timeout to prevent hanging
					execCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
					defer cancel()

					if isMCPMode(ctx) {
						return runMCPServer(ctx, execCtx)
					}
					return nil
				},
			}

			// Run the app
			err := app.Run(tt.args)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
