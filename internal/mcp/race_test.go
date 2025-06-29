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

	"spotinfo/internal/spot"
)

// TestConcurrentSameClient tests concurrent access to the same spot client instance
// This tests the real shared state in spot.Client (sync.Once, cached data)
func TestConcurrentSameClient(t *testing.T) {
	// Use real client to test actual shared state concurrency issues
	realClient := spot.New() // Has internal sync.Once and shared data providers
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	findTool := NewFindSpotInstancesTool(realClient, logger)

	const numGoroutines = 20
	var wg sync.WaitGroup
	results := make([]*mcp.CallToolResult, numGoroutines)
	errors := make([]error, numGoroutines)

	// Test concurrent access with same parameters (realistic production scenario)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			req := createTestCallToolRequest(map[string]interface{}{
				"regions":        []interface{}{"us-east-1"},
				"instance_types": "t2.micro",
				"limit":          5,
			})
			result, err := findTool.Handle(context.Background(), req)
			results[index] = result
			errors[index] = err
		}(i)
	}

	wg.Wait()

	// Verify all calls succeeded and returned consistent results
	for i := 0; i < numGoroutines; i++ {
		assert.NoError(t, errors[i], "Goroutine %d should not have errors", i)
		require.NotNil(t, results[i], "Result %d should not be nil", i)
		assert.False(t, results[i].IsError, "Result %d should not be an error", i)
	}

	// Verify all results are consistent (same parameters should give same results)
	if len(results) > 1 {
		firstResult := results[0]
		for i := 1; i < len(results); i++ {
			assert.Equal(t, len(firstResult.Content), len(results[i].Content),
				"All results should have same content length")
		}
	}
}

// TestConcurrentDifferentParameters tests concurrent calls with different parameters
func TestConcurrentDifferentParameters(t *testing.T) {
	realClient := spot.New()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	findTool := NewFindSpotInstancesTool(realClient, logger)

	testCases := []map[string]interface{}{
		{"regions": []interface{}{"us-east-1"}, "instance_types": "t2.micro", "limit": 5},
		{"regions": []interface{}{"us-west-2"}, "instance_types": "t2.small", "limit": 3},
		{"regions": []interface{}{"eu-west-1"}, "instance_types": "t3.micro", "limit": 10},
		{"regions": []interface{}{"us-east-1", "us-west-2"}, "sort_by": "price", "limit": 8},
	}

	var wg sync.WaitGroup
	results := make([]*mcp.CallToolResult, len(testCases))
	errors := make([]error, len(testCases))

	// Run different parameter sets concurrently
	for i, testCase := range testCases {
		wg.Add(1)
		go func(index int, params map[string]interface{}) {
			defer wg.Done()
			req := createTestCallToolRequest(params)
			result, err := findTool.Handle(context.Background(), req)
			results[index] = result
			errors[index] = err
		}(i, testCase)
	}

	wg.Wait()

	// Verify all calls succeeded
	for i := range testCases {
		assert.NoError(t, errors[i], "Test case %d should not have errors", i)
		require.NotNil(t, results[i], "Result %d should not be nil", i)
	}
}

// TestConcurrentRegionsToolAccess tests concurrent access to regions tool
func TestConcurrentRegionsToolAccess(t *testing.T) {
	realClient := spot.New()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	regionsTool := NewListSpotRegionsTool(realClient, logger)

	const numGoroutines = 15
	var wg sync.WaitGroup
	results := make([]*mcp.CallToolResult, numGoroutines)
	errors := make([]error, numGoroutines)

	// Test concurrent regions listing (all using same empty parameters)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			req := createTestCallToolRequest(map[string]interface{}{})
			result, err := regionsTool.Handle(context.Background(), req)
			results[index] = result
			errors[index] = err
		}(i)
	}

	wg.Wait()

	// Verify all calls succeeded
	for i := 0; i < numGoroutines; i++ {
		assert.NoError(t, errors[i], "Goroutine %d should not have errors", i)
		require.NotNil(t, results[i], "Result %d should not be nil", i)
		assert.False(t, results[i].IsError, "Result %d should not be an error", i)
	}
}

// TestConcurrentServerCreation tests concurrent MCP server creation
func TestConcurrentServerCreation(t *testing.T) {
	const numGoroutines = 10
	var wg sync.WaitGroup
	servers := make([]*Server, numGoroutines)
	errors := make([]error, numGoroutines)

	// Test concurrent server creation with different spot clients
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			cfg := Config{
				Version:    "1.0.0",
				Logger:     slog.Default(),
				SpotClient: spot.New(), // Each gets its own client
			}
			server, err := NewServer(cfg)
			servers[index] = server
			errors[index] = err
		}(i)
	}

	wg.Wait()

	// Verify all servers were created successfully
	for i := 0; i < numGoroutines; i++ {
		assert.NoError(t, errors[i], "Server creation %d should succeed", i)
		assert.NotNil(t, servers[i], "Server %d should not be nil", i)
	}
}

// TestConcurrentParameterParsing tests concurrent parameter parsing with complex data
func TestConcurrentParameterParsing(t *testing.T) {
	// Different parameter sets to test concurrent parsing
	testArgSets := []map[string]interface{}{
		{
			"regions":        []interface{}{"us-east-1", "eu-west-1"},
			"instance_types": "m5.large",
			"limit":          10,
			"sort_by":        "price",
		},
		{
			"regions":               []interface{}{"us-west-2"},
			"min_vcpu":              4,
			"max_price_per_hour":    0.5,
			"max_interruption_rate": 15.0,
		},
		{
			"regions":       []interface{}{"ap-south-1", "ap-southeast-1"},
			"sort_by":       "savings",
			"min_memory_gb": 8,
		},
	}

	const numGoroutines = 30
	var wg sync.WaitGroup
	results := make([]*params, numGoroutines)

	// Test concurrent parameter parsing with different arg sets
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			argSet := testArgSets[index%len(testArgSets)]
			result := parseParameters(argSet)
			results[index] = result
		}(i)
	}

	wg.Wait()

	// Verify all results are valid and consistent for same inputs
	for i, result := range results {
		assert.NotNil(t, result, "Result %d should not be nil", i)
		assert.NotEmpty(t, result.regions, "Result %d should have regions", i)

		// Verify results are consistent for same input set
		expectedResult := parseParameters(testArgSets[i%len(testArgSets)])
		assert.Equal(t, expectedResult, result, "Result %d should match expected", i)
	}
}

// TestConcurrentResponseBuilding tests concurrent response building with shared data
func TestConcurrentResponseBuilding(t *testing.T) {
	// Shared test advice data (simulates what would come from spot client)
	testAdvices := []spot.Advice{
		{
			Instance: "t2.micro",
			Region:   "us-east-1",
			Price:    0.0116,
			Savings:  50,
			Range:    spot.Range{Min: 0, Max: 5, Label: "<5%"},
			Info:     spot.TypeInfo{Cores: 1, RAM: 1.0},
		},
		{
			Instance: "t2.small",
			Region:   "us-west-2",
			Price:    0.023,
			Savings:  45,
			Range:    spot.Range{Min: 5, Max: 10, Label: "5-10%"},
			Info:     spot.TypeInfo{Cores: 1, RAM: 2.0},
		},
	}

	const numGoroutines = 25
	var wg sync.WaitGroup
	responses := make([]map[string]interface{}, numGoroutines)

	startTime := time.Now()

	// Test concurrent response building with same data
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			response := buildResponse(testAdvices, startTime)
			responses[index] = response
		}(i)
	}

	wg.Wait()

	// Verify all responses have consistent structure and content
	for i, response := range responses {
		assert.Contains(t, response, "results", "Response %d should contain results", i)
		assert.Contains(t, response, "metadata", "Response %d should contain metadata", i)

		results, ok := response["results"].([]map[string]interface{})
		require.True(t, ok, "Results should be a slice of maps")
		assert.Len(t, results, len(testAdvices), "Should have correct number of results")

		// Verify content consistency across all concurrent calls
		if i > 0 {
			firstResponse := responses[0]
			firstResults, ok := firstResponse["results"].([]map[string]interface{})
			require.True(t, ok, "First response results should be a slice of maps")
			for j, result := range results {
				assert.Equal(t, firstResults[j]["instance_type"], result["instance_type"],
					"Instance type should be consistent")
				assert.Equal(t, firstResults[j]["region"], result["region"],
					"Region should be consistent")
			}
		}
	}
}

// TestConcurrentHelperFunctions tests concurrent access to helper functions
func TestConcurrentHelperFunctions(t *testing.T) {
	// Shared test arguments
	testArgs := map[string]interface{}{
		"test_string": "value",
		"test_limit":  15,
		"test_slice":  []interface{}{"a", "b", "c"},
	}

	const numGoroutines = 50
	var wg sync.WaitGroup

	// Test all helper functions concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(3)

		go func() {
			defer wg.Done()
			result := getStringWithDefault(testArgs, "test_string", "default")
			assert.Equal(t, "value", result)
		}()

		go func() {
			defer wg.Done()
			result := getLimitWithDefault(testArgs, "test_limit", 10)
			assert.Equal(t, 15, result)
		}()

		go func() {
			defer wg.Done()
			result := getStringSliceWithDefault(testArgs, "test_slice", []string{"default"})
			assert.Equal(t, []string{"a", "b", "c"}, result)
		}()
	}

	wg.Wait()
}

// TestConcurrentDataProviderInitialization tests the critical sync.Once race condition
// This is the most important race test - targets real shared state in spot.Client
func TestConcurrentDataProviderInitialization(t *testing.T) {
	const numGoroutines = 50
	var wg sync.WaitGroup
	results := make([]*mcp.CallToolResult, numGoroutines)
	errors := make([]error, numGoroutines)

	// Create multiple fresh clients to force sync.Once initialization races
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			// Create fresh client for each goroutine to trigger initialization race
			freshClient := spot.New()
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			tool := NewFindSpotInstancesTool(freshClient, logger)

			req := createTestCallToolRequest(map[string]interface{}{
				"regions": []interface{}{"us-east-1"},
				"limit":   3,
			})

			result, err := tool.Handle(context.Background(), req)
			results[index] = result
			errors[index] = err
		}(i)
	}

	wg.Wait()

	// Verify all initializations succeeded without race conditions
	for i := 0; i < numGoroutines; i++ {
		assert.NoError(t, errors[i], "Initialization %d should not have errors", i)
		require.NotNil(t, results[i], "Result %d should not be nil", i)
	}
}

// TestConcurrentMixedOperations tests different operations triggering different code paths
func TestConcurrentMixedOperations(t *testing.T) {
	sharedClient := spot.New()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	findTool := NewFindSpotInstancesTool(sharedClient, logger)
	regionsTool := NewListSpotRegionsTool(sharedClient, logger)

	const numGoroutines = 30
	var wg sync.WaitGroup
	errors := make([]error, numGoroutines)

	// Mix of different operations that stress different code paths
	operations := []func(int) error{
		func(i int) error {
			req := createTestCallToolRequest(map[string]interface{}{
				"regions": []interface{}{"us-east-1", "us-west-2"},
				"sort_by": "price",
			})
			_, err := findTool.Handle(context.Background(), req)
			return err
		},
		func(i int) error {
			req := createTestCallToolRequest(map[string]interface{}{})
			_, err := regionsTool.Handle(context.Background(), req)
			return err
		},
		func(i int) error {
			req := createTestCallToolRequest(map[string]interface{}{
				"regions":        []interface{}{"eu-west-1"},
				"instance_types": "t3.*",
				"min_vcpu":       2,
			})
			_, err := findTool.Handle(context.Background(), req)
			return err
		},
	}

	// Run mixed operations concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			op := operations[index%len(operations)]
			errors[index] = op(index)
		}(i)
	}

	wg.Wait()

	// Verify all operations succeeded
	for i, err := range errors {
		assert.NoError(t, err, "Operation %d should not have errors", i)
	}
}

// TestConcurrentContextCancellation tests concurrent context cancellation scenarios
func TestConcurrentContextCancellation(t *testing.T) {
	client := spot.New()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tool := NewFindSpotInstancesTool(client, logger)

	const numGoroutines = 20
	var wg sync.WaitGroup

	// Test mix of cancelled and non-cancelled contexts
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			var ctx context.Context
			var cancel context.CancelFunc

			if index%3 == 0 {
				// Some contexts get cancelled immediately
				ctx, cancel = context.WithCancel(context.Background())
				cancel() // Cancel immediately
			} else if index%3 == 1 {
				// Some get cancelled after short timeout
				ctx, cancel = context.WithTimeout(context.Background(), 1*time.Millisecond)
				defer cancel()
			} else {
				// Some don't get cancelled
				ctx = context.Background()
			}

			req := createTestCallToolRequest(map[string]interface{}{
				"regions": []interface{}{"us-east-1"},
				"limit":   5,
			})

			// Call should handle context cancellation gracefully
			_, err := tool.Handle(ctx, req)

			// We expect some to succeed, some to fail due to cancellation
			// But no panics or races should occur
			_ = err // Ignore specific errors, just ensure no races
		}(i)
	}

	wg.Wait()
}

// TestConcurrentLargeDatasets tests concurrent access with realistic large datasets
func TestConcurrentLargeDatasets(t *testing.T) {
	client := spot.New()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tool := NewFindSpotInstancesTool(client, logger)

	const numGoroutines = 15
	var wg sync.WaitGroup

	// Large dataset requests that stress memory and processing
	largeRequests := []map[string]interface{}{
		{
			"regions": []interface{}{"us-east-1", "us-west-1", "us-west-2", "eu-west-1", "eu-central-1"},
			"limit":   50, // Large result set
		},
		{
			"regions":        []interface{}{"all"}, // All regions
			"instance_types": ".*",                 // All instance types
			"limit":          30,
		},
		{
			"regions":       []interface{}{"us-east-1", "us-west-2", "eu-west-1"},
			"min_vcpu":      1,
			"min_memory_gb": 1,
			"limit":         25,
		},
	}

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			req := createTestCallToolRequest(largeRequests[index%len(largeRequests)])
			result, err := tool.Handle(context.Background(), req)

			// Verify large datasets are handled correctly
			assert.NoError(t, err, "Large dataset request %d should succeed", index)
			if err == nil {
				assert.NotNil(t, result, "Result should not be nil")
				assert.False(t, result.IsError, "Result should not be an error")
			}
		}(i)
	}

	wg.Wait()
}

// createTestCallToolRequest creates a test MCP call tool request
func createTestCallToolRequest(args interface{}) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: args,
		},
	}
}
