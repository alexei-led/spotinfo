package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"spotinfo/internal/cloud"
)

// BenchmarkParseParameters benchmarks parameter parsing performance
func BenchmarkParseParameters(b *testing.B) {
	args := map[string]any{
		"regions":               []any{"us-east-1", "eu-west-1", "ap-south-1"},
		"instance_types":        "m5.large",
		"min_vcpu":              4,
		"min_memory_gb":         16,
		"max_price_per_hour":    0.5,
		"max_interruption_rate": 10.0,
		"sort_by":               "savings",
		"limit":                 25,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := parseParameters(args)
		_ = result // Prevent optimization
	}
}

// BenchmarkBuildResponse benchmarks response building performance
func BenchmarkBuildResponse(b *testing.B) {
	candidates := buildCandidates(
		testCandidate{Machine: "m5.large", Region: "us-east-1", Price: 0.0928, Savings: 70,
			RiskLabel: "<5%", RiskMin: 5, RiskMax: 10, VCPU: 2, MemoryGiB: 8},
		testCandidate{Machine: "t3.medium", Region: "eu-west-1", Price: 0.0416, Savings: 65,
			RiskLabel: "5-10%", RiskMin: 10, RiskMax: 15, VCPU: 2, MemoryGiB: 4},
		testCandidate{Machine: "c5.xlarge", Region: "ap-south-1", Price: 0.192, Savings: 75,
			RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 4, MemoryGiB: 8},
	)

	startTime := time.Now()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		response := buildResponse(candidates, startTime, cloud.DataModeEmbeddedSnapshot)
		_ = response // Prevent optimization
	}
}

// BenchmarkMarshalResponse benchmarks JSON marshaling performance
func BenchmarkMarshalResponse(b *testing.B) {
	response := map[string]any{
		"results": []map[string]any{
			{
				"instance_type":          "m5.large",
				"region":                 "us-east-1",
				"spot_price_per_hour":    0.0928,
				"savings_percentage":     70,
				"interruption_rate":      7.5,
				"reliability_score":      92,
				"vcpu":                   2,
				"memory_gb":              8.0,
				"spot_price":             "$0.0928/hour",
				"savings":                "70% cheaper than on-demand",
				"interruption_frequency": "<5%",
				"interruption_range":     "5-10%",
				"specs":                  "2 vCPU, 8 GB RAM",
			},
		},
		"metadata": map[string]any{
			"total_results":    1,
			"regions_searched": []string{"us-east-1"},
			"query_time_ms":    int64(123),
			"data_source":      "embedded",
			"data_freshness":   "current",
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result, err := marshalResponse(response)
		if err != nil {
			b.Fatal(err)
		}
		_ = result // Prevent optimization
	}
}

// BenchmarkFilterByInterruption benchmarks interruption filtering performance
func BenchmarkFilterByInterruption(b *testing.B) {
	// Create a large slice of advices for realistic benchmarking
	candidates := make([]cloud.Candidate, 1000)
	for i := range candidates {
		candidates[i] = testCandidate{
			Machine: "test-instance", Region: "us-east-1",
			RiskLabel: "varying", RiskMin: float64(i % 50), RiskMax: float64((i % 50) + 10),
		}.build()
	}

	maxInterruption := 25.0

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result := filterByInterruption(candidates, maxInterruption)
		_ = result // Prevent optimization
	}
}

// BenchmarkApplyLimit benchmarks result limiting performance
func BenchmarkApplyLimit(b *testing.B) {
	// Create a large slice of advices
	candidates := make([]cloud.Candidate, 10000)
	for i := range candidates {
		candidates[i] = testCandidate{
			Machine: "test-instance", Region: "us-east-1", Savings: i % 100,
		}.build()
	}

	limit := 50

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result := applyLimit(candidates, limit)
		_ = result // Prevent optimization
	}
}

// BenchmarkCalculateAvgInterruption benchmarks interruption calculation performance
func BenchmarkCalculateAvgInterruption(b *testing.B) {
	testRisk := testCandidate{RiskLabel: "10-20%", RiskMin: 10, RiskMax: 20}.build().Risk

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, _ := calculateAvgInterruption(&testRisk)
		_ = result // Prevent optimization
	}
}

// BenchmarkCalculateReliabilityScore benchmarks reliability score calculation performance
func BenchmarkCalculateReliabilityScore(b *testing.B) {
	avgInterruption := 15.5

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := calculateReliabilityScore(avgInterruption)
		_ = result // Prevent optimization
	}
}

// BenchmarkHelperFunctions benchmarks helper function performance
func BenchmarkHelperFunctions(b *testing.B) {
	args := map[string]any{
		"string_key": "test_value",
		"limit_key":  25,
		"slice_key":  []any{"item1", "item2", "item3"},
	}

	b.Run("getStringWithDefault", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			result := getStringWithDefault(args, "string_key", "default")
			_ = result
		}
	})

	b.Run("getLimitWithDefault", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			result := getLimitWithDefault(args, "limit_key", 10)
			_ = result
		}
	})

	b.Run("getStringSliceWithDefault", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			result := getStringSliceWithDefault(args, "slice_key", []string{"default"})
			_ = result
		}
	})
}

// BenchmarkToolHandlerComplete benchmarks the complete tool handler flow.
//
// Uses the embedded client, not spot.New(): the production client sends every
// instance the static feed does not price to the live-price API, which stalls
// for livePriceTimeout per call without AWS credentials and makes the numbers
// measure network failure rather than handler work.
func BenchmarkToolHandlerComplete(b *testing.B) {
	registry := newEmbeddedRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tool := NewFindSpotInstancesTool(registry, logger)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"regions":        []any{"us-east-1"},
				"instance_types": "t2.micro",
				"sort_by":        "price",
				"limit":          5,
			},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result, err := tool.Handle(context.Background(), req)
		if err != nil {
			b.Fatal(err)
		}
		_ = result // Prevent optimization
	}
}

// BenchmarkJSONOperations benchmarks JSON marshal/unmarshal operations
func BenchmarkJSONOperations(b *testing.B) {
	testData := map[string]any{
		"results": []map[string]any{
			{
				"instance_type":       "m5.large",
				"region":              "us-east-1",
				"spot_price_per_hour": 0.0928,
				"savings_percentage":  70,
				"interruption_rate":   7.5,
				"reliability_score":   92,
			},
		},
		"metadata": map[string]any{
			"total_results": 1,
			"data_source":   "embedded",
		},
	}

	b.Run("JSONMarshal", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			data, err := json.Marshal(testData)
			if err != nil {
				b.Fatal(err)
			}
			_ = data
		}
	})

	// Get JSON data for unmarshaling benchmark
	jsonData, _ := json.Marshal(testData)

	b.Run("JSONUnmarshal", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var result map[string]any
			err := json.Unmarshal(jsonData, &result)
			if err != nil {
				b.Fatal(err)
			}
			_ = result
		}
	})
}

// BenchmarkSortOrderFor benchmarks sort parameter conversion
func BenchmarkSortOrderFor(b *testing.B) {
	testCases := []string{"price", "reliability", "savings", "unknown"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		order := sortOrderFor(testCases[i%len(testCases)])
		_ = order
	}
}

// BenchmarkColdVsWarmStartup benchmarks first vs subsequent calls (sync.Once impact)
func BenchmarkColdVsWarmStartup(b *testing.B) {
	b.Run("ColdStart", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			// Create fresh client each time to force cold start
			registry := newEmbeddedRegistry()
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			tool := NewFindSpotInstancesTool(registry, logger)

			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"regions": []any{"us-east-1"},
						"limit":   3,
					},
				},
			}

			result, err := tool.Handle(context.Background(), req)
			if err != nil {
				b.Fatal(err)
			}
			_ = result
		}
	})

	b.Run("WarmStart", func(b *testing.B) {
		// Pre-warm the client
		registry := newEmbeddedRegistry()
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
		tool := NewFindSpotInstancesTool(registry, logger)

		// Warm up call
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"regions": []any{"us-east-1"},
					"limit":   3,
				},
			},
		}
		_, _ = tool.Handle(context.Background(), req)

		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			result, err := tool.Handle(context.Background(), req)
			if err != nil {
				b.Fatal(err)
			}
			_ = result
		}
	})
}

// BenchmarkDatasetSizes benchmarks performance with different dataset sizes
func BenchmarkDatasetSizes(b *testing.B) {
	registry := newEmbeddedRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tool := NewFindSpotInstancesTool(registry, logger)

	// Warm up
	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{"regions": []any{"us-east-1"}, "limit": 1},
		},
	}
	_, _ = tool.Handle(context.Background(), req)

	testCases := []struct {
		name    string
		request map[string]any
	}{
		{
			name: "SmallDataset",
			request: map[string]any{
				"regions": []any{"us-east-1"},
				"limit":   5,
			},
		},
		{
			name: "MediumDataset",
			request: map[string]any{
				"regions": []any{"us-east-1", "us-west-2", "eu-west-1"},
				"limit":   20,
			},
		},
		{
			name: "LargeDataset",
			request: map[string]any{
				"regions": []any{"all"},
				"limit":   50,
			},
		},
		{
			name: "FilteredLargeDataset",
			request: map[string]any{
				"regions":        []any{"all"},
				"instance_types": "t.*",
				"min_vcpu":       2,
				"limit":          30,
			},
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{Arguments: tc.request},
			}

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				result, err := tool.Handle(context.Background(), req)
				if err != nil {
					b.Fatal(err)
				}
				_ = result
			}
		})
	}
}

// BenchmarkConcurrentThroughput benchmarks concurrent throughput
func BenchmarkConcurrentThroughput(b *testing.B) {
	registry := newEmbeddedRegistry()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tool := NewFindSpotInstancesTool(registry, logger)

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"regions": []any{"us-east-1"},
				"limit":   10,
			},
		},
	}

	// Warm up
	_, _ = tool.Handle(context.Background(), req)

	b.ResetTimer()
	b.ReportAllocs()

	// Run concurrent operations
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			result, err := tool.Handle(context.Background(), req)
			if err != nil {
				b.Fatal(err)
			}
			_ = result
		}
	})
}

// BenchmarkMemoryAllocations focuses on allocation patterns
func BenchmarkMemoryAllocations(b *testing.B) {
	// Create realistic test data for memory allocation analysis
	testCandidates := make([]cloud.Candidate, 100)
	for i := range testCandidates {
		testCandidates[i] = testCandidate{
			Machine: "t2.micro", Region: "us-east-1", Price: 0.0116, Savings: 50,
			RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 1, MemoryGiB: 1,
		}.build()
	}

	b.Run("ParameterParsing", func(b *testing.B) {
		args := map[string]any{
			"regions":        []any{"us-east-1", "eu-west-1", "ap-south-1"},
			"instance_types": "m5.*",
			"min_vcpu":       4,
			"min_memory_gb":  16,
			"sort_by":        "savings",
			"limit":          25,
		}

		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			result := parseParameters(args)
			_ = result
		}
	})

	b.Run("ResponseBuilding", func(b *testing.B) {
		startTime := time.Now()

		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			response := buildResponse(testCandidates, startTime, cloud.DataModeEmbeddedSnapshot)
			_ = response
		}
	})

	b.Run("JSONMarshaling", func(b *testing.B) {
		startTime := time.Now()
		response := buildResponse(testCandidates, startTime, cloud.DataModeEmbeddedSnapshot)

		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			result, err := marshalResponse(response)
			if err != nil {
				b.Fatal(err)
			}
			_ = result
		}
	})

	b.Run("FilteringOperations", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			filtered := filterByInterruption(testCandidates, 25.0)
			limited := applyLimit(filtered, 20)
			_ = limited
		}
	})
}
