package mcp

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

// These tests exist for the shared state inside spot.Client — its sync.Once and
// its cached feed data — reached through newEmbeddedRegistry. The tools in front
// of it changed; the state behind them did not, so each test is pointed at the
// handler that replaced the one it used to drive.

func raceLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestConcurrentSameClient tests concurrent access to the same spot client
// instance: the real shared state in spot.Client (sync.Once, cached data).
func TestConcurrentSameClient(t *testing.T) {
	t.Parallel()

	tool := NewListSpotMachinesTool(newEmbeddedRegistry(), raceLogger())

	const numGoroutines = 20

	var wg sync.WaitGroup

	results := make([]*mcp.CallToolResult, numGoroutines)
	errors := make([]error, numGoroutines)

	for i := range numGoroutines {
		wg.Add(1)

		go func(index int) {
			defer wg.Done()

			req := createTestCallToolRequest(map[string]any{
				argRegions: []any{"us-east-1"},
				argMachine: "t2.micro",
			})
			results[index], errors[index] = tool.Handle(context.Background(), req)
		}(i)
	}

	wg.Wait()

	for i := range numGoroutines {
		assert.NoError(t, errors[i], "Goroutine %d should not have errors", i)
		require.NotNil(t, results[i], "Result %d should not be nil", i)
		assert.False(t, results[i].IsError, "Result %d should not be an error", i)
	}

	// The same question asked concurrently must answer the same way.
	for i := 1; i < numGoroutines; i++ {
		assert.Len(t, results[i].Content, len(results[0].Content),
			"All results should have same content length")
	}
}

// TestConcurrentDifferentParameters tests concurrent calls with different parameters
func TestConcurrentDifferentParameters(t *testing.T) {
	t.Parallel()

	tool := NewListSpotMachinesTool(newEmbeddedRegistry(), raceLogger())

	testCases := []map[string]any{
		{argRegions: []any{"us-east-1"}, argMachine: "t2.micro"},
		{argRegions: []any{"us-west-2"}, argMachine: "t2.small"},
		{argRegions: []any{"eu-west-1"}, argMachine: "t3.micro"},
		{argRegions: []any{"us-east-1", "us-west-2"}, argSort: "price"},
	}

	var wg sync.WaitGroup

	results := make([]*mcp.CallToolResult, len(testCases))
	errors := make([]error, len(testCases))

	for i, testCase := range testCases {
		wg.Add(1)

		go func(index int, args map[string]any) {
			defer wg.Done()

			results[index], errors[index] = tool.Handle(context.Background(), createTestCallToolRequest(args))
		}(i, testCase)
	}

	wg.Wait()

	for i := range testCases {
		assert.NoError(t, errors[i], "Test case %d should not have errors", i)
		require.NotNil(t, results[i], "Result %d should not be nil", i)
		assert.False(t, results[i].IsError, "Result %d should not be an error", i)
	}
}

// TestConcurrentRegionsToolAccess tests concurrent access to the regions tool
func TestConcurrentRegionsToolAccess(t *testing.T) {
	t.Parallel()

	tool := NewListCloudRegionsTool(newEmbeddedRegistry(), raceLogger())

	const numGoroutines = 15

	var wg sync.WaitGroup

	results := make([]*mcp.CallToolResult, numGoroutines)
	errors := make([]error, numGoroutines)

	for i := range numGoroutines {
		wg.Add(1)

		go func(index int) {
			defer wg.Done()

			req := createTestCallToolRequest(map[string]any{})
			results[index], errors[index] = tool.Handle(context.Background(), req)
		}(i)
	}

	wg.Wait()

	for i := range numGoroutines {
		assert.NoError(t, errors[i], "Goroutine %d should not have errors", i)
		require.NotNil(t, results[i], "Result %d should not be nil", i)
		assert.False(t, results[i].IsError, "Result %d should not be an error", i)
	}
}

// TestConcurrentServerCreation tests concurrent MCP server creation
func TestConcurrentServerCreation(t *testing.T) {
	t.Parallel()

	const numGoroutines = 10

	var wg sync.WaitGroup

	servers := make([]*Server, numGoroutines)
	errors := make([]error, numGoroutines)

	for i := range numGoroutines {
		wg.Add(1)

		go func(index int) {
			defer wg.Done()

			servers[index], errors[index] = NewServer(Config{
				Version:   "1.0.0",
				Logger:    slog.Default(),
				Providers: newEmbeddedRegistry(), // Each gets its own client
			})
		}(i)
	}

	wg.Wait()

	for i := range numGoroutines {
		assert.NoError(t, errors[i], "Server creation %d should succeed", i)
		assert.NotNil(t, servers[i], "Server %d should not be nil", i)
	}
}

// TestConcurrentRequestParsing tests concurrent argument parsing with complex data
func TestConcurrentRequestParsing(t *testing.T) {
	t.Parallel()

	argSets := []map[string]any{
		{
			argRegions: []any{"us-east-1", "eu-west-1"},
			argMachine: "m5.large",
			argSort:    "price",
		},
		{
			argRegions:  []any{"us-west-2"},
			argMinVCPU:  4,
			argMaxPrice: 0.5,
		},
		{
			argRegions:      []any{"ap-south-1", "ap-southeast-1"},
			argSort:         "savings",
			argOrder:        cloud.OrderDesc,
			argMinMemoryGiB: 8.0,
		},
	}

	const numGoroutines = 30

	var wg sync.WaitGroup

	queries := make([]*cloud.Query, numGoroutines)

	for i := range numGoroutines {
		wg.Add(1)

		go func(index int) {
			defer wg.Done()

			_, query, _, err := parseListRequest(argSets[index%len(argSets)])
			assert.NoError(t, err)
			queries[index] = query
		}(i)
	}

	wg.Wait()

	// Parsing is pure: the same arguments must map onto the same query every
	// time, whatever else is running.
	for i, query := range queries {
		require.NotNil(t, query, "Query %d should not be nil", i)

		_, expected, _, err := parseListRequest(argSets[i%len(argSets)])
		require.NoError(t, err)
		assert.Equal(t, expected, query, "Query %d should match expected", i)
	}
}

// TestConcurrentReportBuilding tests concurrent report building with shared data
func TestConcurrentReportBuilding(t *testing.T) {
	t.Parallel()

	result := cloud.Result{
		Provider: cloud.ProviderAWS,
		Mode:     cloud.DataModeEmbeddedSnapshot,
		Sources:  testSources(),
		Candidates: buildCandidates(
			testCandidate{
				Machine: "t2.micro", Region: "us-east-1", Price: 0.0116, Savings: 50,
				RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 1, MemoryGiB: 1,
			},
			testCandidate{
				Machine: "t2.small", Region: "us-west-2", Price: 0.023, Savings: 45,
				RiskLabel: "5-10%", RiskMin: 5, RiskMax: 10, VCPU: 1, MemoryGiB: 2,
			},
		),
	}
	query := &cloud.Query{OS: cloud.OSLinux, Regions: []cloud.Region{cloud.RegionAll}}

	const numGoroutines = 25

	var wg sync.WaitGroup

	reports := make([]*cloud.ListReport, numGoroutines)

	for i := range numGoroutines {
		wg.Add(1)

		go func(index int) {
			defer wg.Done()

			report, err := cloud.NewListReport(query, &result)
			assert.NoError(t, err)
			reports[index] = report
		}(i)
	}

	wg.Wait()

	for i, report := range reports {
		require.NotNil(t, report, "Report %d should not be nil", i)
		assert.Equal(t, reports[0], report, "Report %d should match the first", i)
	}
}

// TestConcurrentDataProviderInitialization tests the critical sync.Once race
// condition. This is the most important race test — it targets the real shared
// state in spot.Client.
func TestConcurrentDataProviderInitialization(t *testing.T) {
	t.Parallel()

	const numGoroutines = 50

	var wg sync.WaitGroup

	results := make([]*mcp.CallToolResult, numGoroutines)
	errors := make([]error, numGoroutines)

	for i := range numGoroutines {
		wg.Add(1)

		go func(index int) {
			defer wg.Done()

			// A fresh client per goroutine, to trigger the initialization race.
			tool := NewListSpotMachinesTool(newEmbeddedRegistry(), raceLogger())
			req := createTestCallToolRequest(map[string]any{argRegions: []any{"us-east-1"}, argMachine: "t2.micro"})
			results[index], errors[index] = tool.Handle(context.Background(), req)
		}(i)
	}

	wg.Wait()

	for i := range numGoroutines {
		assert.NoError(t, errors[i], "Initialization %d should not have errors", i)
		require.NotNil(t, results[i], "Result %d should not be nil", i)
	}
}

// TestConcurrentMixedOperations tests different operations triggering different code paths
func TestConcurrentMixedOperations(t *testing.T) {
	t.Parallel()

	sharedRegistry := newEmbeddedRegistry()
	listTool := NewListSpotMachinesTool(sharedRegistry, raceLogger())
	regionsTool := NewListCloudRegionsTool(sharedRegistry, raceLogger())
	recommendTool := NewRecommendTool(sharedRegistry, raceLogger())

	operations := []func() error{
		func() error {
			req := createTestCallToolRequest(map[string]any{
				argRegions: []any{"us-east-1", "us-west-2"},
				argMachine: "t3.*",
				argSort:    "price",
			})
			_, err := listTool.Handle(context.Background(), req)

			return err
		},
		func() error {
			_, err := regionsTool.Handle(context.Background(), createTestCallToolRequest(map[string]any{}))

			return err
		},
		func() error {
			req := createTestCallToolRequest(map[string]any{
				argRegions: []any{"eu-west-1"}, argArchitecture: "x86_64",
				argMinVCPU: 2, argMinMemoryGiB: 4.0,
			})
			_, err := recommendTool.Handle(context.Background(), req)

			return err
		},
	}

	const numGoroutines = 30

	var wg sync.WaitGroup

	errors := make([]error, numGoroutines)

	for i := range numGoroutines {
		wg.Add(1)

		go func(index int) {
			defer wg.Done()

			errors[index] = operations[index%len(operations)]()
		}(i)
	}

	wg.Wait()

	for i, err := range errors {
		assert.NoError(t, err, "Operation %d should not have errors", i)
	}
}

// TestConcurrentContextCancellation tests concurrent context cancellation scenarios
func TestConcurrentContextCancellation(t *testing.T) {
	t.Parallel()

	tool := NewListSpotMachinesTool(newEmbeddedRegistry(), raceLogger())

	const numGoroutines = 20

	var wg sync.WaitGroup

	for i := range numGoroutines {
		wg.Add(1)

		go func(index int) {
			defer wg.Done()

			var (
				ctx    context.Context
				cancel context.CancelFunc
			)

			switch index % 3 {
			case 0:
				ctx, cancel = context.WithCancel(context.Background())
				cancel() // Cancelled before the call.
			case 1:
				ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
				defer cancel()
			default:
				ctx = context.Background()
			}

			req := createTestCallToolRequest(map[string]any{argRegions: []any{"us-east-1"}, argMachine: "t2.micro"})

			// Some succeed and some fail; neither may panic or race.
			_, _ = tool.Handle(ctx, req)
		}(i)
	}

	wg.Wait()
}

// TestConcurrentLargeDatasets tests concurrent access with realistic large datasets
func TestConcurrentLargeDatasets(t *testing.T) {
	t.Parallel()

	tool := NewListSpotMachinesTool(newEmbeddedRegistry(), raceLogger())

	largeRequests := []map[string]any{
		{argRegions: []any{"us-east-1", "us-west-1", "us-west-2", "eu-west-1", "eu-central-1"}},
		{argRegions: []any{string(cloud.RegionAll)}, argMachine: ".*"},
		{argRegions: []any{"us-east-1", "us-west-2", "eu-west-1"}, argMinVCPU: 1, argMinMemoryGiB: 1.0},
	}

	const numGoroutines = 15

	var wg sync.WaitGroup

	for i := range numGoroutines {
		wg.Add(1)

		go func(index int) {
			defer wg.Done()

			req := createTestCallToolRequest(largeRequests[index%len(largeRequests)])

			result, err := tool.Handle(context.Background(), req)
			assert.NoError(t, err, "Large dataset request %d should succeed", index)
			require.NotNil(t, result, "Result should not be nil")
			assert.False(t, result.IsError, "Result should not be an error")
		}(i)
	}

	wg.Wait()
}

// createTestCallToolRequest creates a test MCP call tool request
func createTestCallToolRequest(args any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: args,
		},
	}
}
