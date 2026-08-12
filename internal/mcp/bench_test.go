package mcp

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"spotinfo/internal/cloud"
)

// The benchmarks below measure the two shapes that carry real cost: parsing and
// mapping one request, and the whole handler over the embedded AWS snapshot.
// The v1 response builders they used to measure — parseParameters,
// buildResponse, filterByInterruption, applyLimit and the reliability score —
// are gone with the v1 document, and a benchmark of a deleted function is not a
// benchmark worth keeping.

func benchLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// BenchmarkParseListRequest measures argument parsing and the neutral query it
// builds, which is what every list call pays before acquisition.
func BenchmarkParseListRequest(b *testing.B) {
	args := map[string]any{
		argCloud:        "aws",
		argRegions:      []any{"us-east-1", "eu-west-1", "ap-south-1"},
		argMachine:      "m5.large",
		argMinVCPU:      4,
		argMinMemoryGiB: 16.0,
		argMaxPrice:     0.5,
		argSort:         "savings",
		argOrder:        cloud.OrderDesc,
	}

	b.ReportAllocs()

	for range b.N {
		if _, _, _, err := parseListRequest(args); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListReport measures building the published spotinfo.list/v1 document
// from candidates a provider already returned.
func BenchmarkListReport(b *testing.B) {
	candidates := make([]cloud.Candidate, 100)
	for i := range candidates {
		candidates[i] = testCandidate{
			Machine: "t2.micro", Region: "us-east-1", Price: 0.0116, Savings: 50,
			RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 1, MemoryGiB: 1,
		}.build()
	}

	result := cloud.Result{
		Provider:   cloud.ProviderAWS,
		Mode:       cloud.DataModeEmbeddedSnapshot,
		Sources:    testSources(),
		Candidates: candidates,
	}
	query := &cloud.Query{OS: cloud.OSLinux, Regions: []cloud.Region{cloud.RegionAll}}

	b.ReportAllocs()

	for range b.N {
		if _, err := cloud.NewListReport(query, &result); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListToolComplete benchmarks the complete tool handler flow.
//
// Uses the embedded client, not spot.New(): the production client sends every
// instance the static feed does not price to the live-price API, which stalls
// for livePriceTimeout per call without AWS credentials and makes the numbers
// measure network failure rather than handler work.
func BenchmarkListToolComplete(b *testing.B) {
	tool := NewListSpotMachinesTool(newEmbeddedRegistry(), benchLogger())

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				argRegions: []any{"us-east-1"},
				argMachine: "t2.micro",
				argSort:    "price",
			},
		},
	}

	b.ReportAllocs()

	for range b.N {
		if _, err := tool.Handle(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkColdVsWarmStartup benchmarks first vs subsequent calls (sync.Once impact)
func BenchmarkColdVsWarmStartup(b *testing.B) {
	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{argRegions: []any{"us-east-1"}},
		},
	}

	b.Run("ColdStart", func(b *testing.B) {
		b.ReportAllocs()

		for range b.N {
			// Fresh client each time, to force the cold start.
			tool := NewListSpotMachinesTool(newEmbeddedRegistry(), benchLogger())
			if _, err := tool.Handle(context.Background(), req); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("WarmStart", func(b *testing.B) {
		tool := NewListSpotMachinesTool(newEmbeddedRegistry(), benchLogger())
		_, _ = tool.Handle(context.Background(), req)

		b.ResetTimer()
		b.ReportAllocs()

		for range b.N {
			if _, err := tool.Handle(context.Background(), req); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkDatasetSizes benchmarks performance with different dataset sizes
func BenchmarkDatasetSizes(b *testing.B) {
	tool := NewListSpotMachinesTool(newEmbeddedRegistry(), benchLogger())

	// Warm up.
	_, _ = tool.Handle(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{argRegions: []any{"us-east-1"}}},
	})

	for name, arguments := range map[string]map[string]any{
		"SmallDataset": {
			argRegions: []any{"us-east-1"}, argMachine: "t2.micro",
		},
		"MediumDataset": {
			argRegions: []any{"us-east-1", "us-west-2", "eu-west-1"}, argMachine: "t3.*",
		},
		"LargeDataset": {
			argRegions: []any{string(cloud.RegionAll)},
		},
		"FilteredLargeDataset": {
			argRegions: []any{string(cloud.RegionAll)}, argMachine: "t.*", argMinVCPU: 2,
		},
	} {
		b.Run(name, func(b *testing.B) {
			req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: arguments}}

			b.ReportAllocs()

			for range b.N {
				if _, err := tool.Handle(context.Background(), req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkConcurrentThroughput benchmarks concurrent throughput
func BenchmarkConcurrentThroughput(b *testing.B) {
	tool := NewListSpotMachinesTool(newEmbeddedRegistry(), benchLogger())

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{argRegions: []any{"us-east-1"}, argMachine: "t3.*"},
		},
	}

	// Warm up.
	_, _ = tool.Handle(context.Background(), req)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := tool.Handle(context.Background(), req); err != nil {
				b.Fatal(err)
			}
		}
	})
}
