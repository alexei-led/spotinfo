package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

// The recommend tool answers for any registered provider, so the contract is
// exercised against AWS and against fake GCP and Azure providers with the
// offline shape those clouds will have: Linux spot prices, no risk.

func gcpCandidates() []cloud.Candidate {
	candidates := buildCandidates(
		testCandidate{Region: "europe-west1", Machine: "n2-standard-2", Price: 0.021, VCPU: 2, MemoryGiB: 8},
		testCandidate{Region: "us-central1", Machine: "t2a-standard-2", Price: 0.014, VCPU: 2, MemoryGiB: 8,
			Architecture: cloud.ArchitectureARM64},
	)
	for i := range candidates {
		candidates[i].Provider = cloud.ProviderGCP
	}

	return candidates
}

// A ceiling finer than the fixed-point scale filters here exactly as it does in
// find_spot_instances. A client that turns a monthly budget into an hourly one
// sends 0.041666666666666664 routinely, and the two tools must not disagree
// about whether that is a usable ceiling or an invalid argument.
func TestFinerThanScalePriceCeilingStillFiltersOnV2(t *testing.T) {
	for name, test := range map[string]struct {
		price float64
		want  string
	}{
		"within the scale":       {price: 0.04, want: "0.040000000"},
		"exactly at the scale":   {price: 0.040000001, want: "0.040000001"},
		"one digit past":         {price: 0.0400000001, want: "0.040000000"},
		"monthly budget an hour": {price: 30.0 / 720.0, want: "0.041666666"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			provider := offlineProvider(cloud.ProviderGCP, gcpCandidates())
			result := callRecommend(t, newStubRegistry(provider), map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"max_price_per_hour": test.price,
			})
			require.False(t, result.IsError)

			ceiling := provider.lastQuery().MaxPrice
			require.NotNil(t, ceiling, "the caller's ceiling must reach the provider")
			assert.Equal(t, test.want, ceiling.String())
		})
	}
}

func offlineProvider(id cloud.ProviderID, candidates []cloud.Candidate) *stubProvider {
	for i := range candidates {
		candidates[i].Provider = id
	}

	return &stubProvider{
		id:           id,
		capabilities: offlineLinuxCapabilities(),
		result: cloud.Result{
			Provider:   id,
			Mode:       cloud.DataModeEmbeddedSnapshot,
			Sources:    testSources(),
			Candidates: candidates,
		},
	}
}

func recommendTool(providers providerRegistry) *RecommendTool {
	return NewRecommendTool(providers, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

func callRecommend(t *testing.T, providers providerRegistry, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	result, err := recommendTool(providers).Handle(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: args},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Content, 1)

	return result
}

func payloadOf(t *testing.T, result *mcp.CallToolResult) []byte {
	t.Helper()

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	return []byte(text.Text)
}

func decodeReport(t *testing.T, result *mcp.CallToolResult) cloud.RecommendReport {
	t.Helper()

	require.False(t, result.IsError)

	var report cloud.RecommendReport
	require.NoError(t, json.Unmarshal(payloadOf(t, result), &report))

	return report
}

func decodeError(t *testing.T, result *mcp.CallToolResult) cloud.ErrorReport {
	t.Helper()

	require.True(t, result.IsError, "a failure must be reported as an error result")

	var report cloud.ErrorReport
	require.NoError(t, json.Unmarshal(payloadOf(t, result), &report))

	return report
}

// The registered input schema is the client contract. It is recorded as a
// golden so a change is reviewed rather than shipped by accident.
func TestRecommendInputSchemaMatchesTheRecordedContract(t *testing.T) {
	server, err := NewServer(Config{Version: "1.0.0", Logger: slog.Default(), Providers: newEmbeddedRegistry()})
	require.NoError(t, err)

	registered, ok := server.mcpServer.ListTools()[recommendToolName]
	require.True(t, ok, "recommend_spot_instances must be registered")

	encoded, err := json.MarshalIndent(registered.Tool, "", "  ")
	require.NoError(t, err)
	assertGolden(t, "recommend-spot-instances-v2-input-schema.json", append(encoded, '\n'))
}

// boundKeywords are the validation keywords the contract uses to bound an
// input. They are compared alongside type and enum because a dropped bound is
// what lets a host forward 0 or [] as if the contract allowed it.
var boundKeywords = []string{
	"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
	"minItems", "uniqueItems", "minLength", "items",
}

// The registered schema must agree with the normative input contract on the
// fields a client depends on: required inputs, declared types, enums, bounds,
// defaults and whether unknown arguments are accepted. Type matters as much as
// the enum: a contract integer advertised as a number lets a host pass 2.5 where
// the handler will truncate, and an open object silently drops a misspelled key.
func TestRegisteredInputSchemaAgreesWithTheNormativeContract(t *testing.T) {
	server, err := NewServer(Config{Version: "1.0.0", Logger: slog.Default(), Providers: newEmbeddedRegistry()})
	require.NoError(t, err)

	registered, ok := server.mcpServer.ListTools()[recommendToolName]
	require.True(t, ok)

	encoded, err := json.Marshal(registered.Tool.InputSchema)
	require.NoError(t, err)

	var advertised map[string]any
	require.NoError(t, json.Unmarshal(encoded, &advertised))

	contract := loadContractSchema(t, "recommend-spot-instances-v2-input.schema.json")

	contractRequired, ok := contract["required"].([]any)
	require.True(t, ok)
	assert.ElementsMatch(t, contractRequired, advertised["required"])

	contractProperties, ok := contract["properties"].(map[string]any)
	require.True(t, ok)
	advertisedProperties, ok := advertised["properties"].(map[string]any)
	require.True(t, ok)

	for name, declared := range contractProperties {
		property, present := advertisedProperties[name].(map[string]any)
		require.True(t, present, "input %q is missing from the advertised schema", name)

		expected, _ := declared.(map[string]any)
		assert.Equal(t, expected["type"], property["type"], "input %q type", name)
		if enum, has := expected["enum"]; has {
			assert.ElementsMatch(t, enum, property["enum"], "input %q enum", name)
		}
		if fallback, has := expected["default"]; has {
			assert.EqualValues(t, fallback, property["default"], "input %q default", name)
		}
		for _, keyword := range boundKeywords {
			bound, has := expected[keyword]
			if !has {
				continue
			}
			assert.EqualValues(t, bound, property[keyword], "input %q %s", name, keyword)
		}
	}
	assert.Len(t, advertisedProperties, len(contractProperties), "the advertised schema must not add inputs")
	assert.Equal(t, contract["additionalProperties"], advertised["additionalProperties"],
		"the advertised schema must close the object exactly as the contract does")

	// The advertised closed object is only a promise to the client — the host
	// never checks it. rejectUnknownArgs is what keeps it, so its allow-list
	// must be the contract's property set exactly: a name missing from it
	// refuses a documented argument, and a name it adds reopens the object.
	accepted := make([]string, 0, len(recommendArgs))
	for name := range recommendArgs {
		accepted = append(accepted, name)
	}

	declared := make([]string, 0, len(contractProperties))
	for name := range contractProperties {
		declared = append(declared, name)
	}

	assert.ElementsMatch(t, declared, accepted, "the handler must accept exactly the contract's arguments")
}

// A successful answer validates against the published success schema, for every
// provider shape.
func TestRecommendSuccessPayloadValidatesAgainstTheContract(t *testing.T) {
	schema := loadContractSchema(t, "recommend-spot-instances-v2-success.schema.json")

	for _, test := range []struct {
		name     string
		registry providerRegistry
		args     map[string]any
	}{
		{
			name: "aws with published risk",
			registry: func() providerRegistry {
				_, registry := awsStub(buildCandidates(testCandidate{
					Region: "us-east-1", Machine: "m6i.large", Price: 0.0416, Savings: 72,
					RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 2, MemoryGiB: 8,
				})...)

				return registry
			}(),
			args: map[string]any{
				"cloud": "aws", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"workload": "web", "max_price_per_hour": 0.5,
			},
		},
		{
			name:     "gcp without risk",
			registry: newStubRegistry(offlineProvider(cloud.ProviderGCP, gcpCandidates())),
			args:     map[string]any{"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8},
		},
		{
			name: "azure without risk",
			registry: newStubRegistry(offlineProvider(cloud.ProviderAzure, buildCandidates(testCandidate{
				Region: "westeurope", Machine: "Standard_D2s_v5", Price: 0.0192, VCPU: 2, MemoryGiB: 8,
			}))),
			args: map[string]any{"cloud": "azure", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := callRecommend(t, test.registry, test.args)
			require.False(t, result.IsError)
			assertValidAgainstSchema(t, schema, payloadOf(t, result))
		})
	}
}

// Every documented default is applied by the handler, so a minimal request is
// answered exactly as the contract describes.
func TestRecommendAppliesTheDocumentedDefaults(t *testing.T) {
	_, registry := awsStub(buildCandidates(testCandidate{
		Region: "us-east-1", Machine: "m6i.large", Price: 0.0416, Savings: 72,
		RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 2, MemoryGiB: 8,
	})...)

	report := decodeReport(t, callRecommend(t, registry, map[string]any{
		"architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
	}))

	assert.Equal(t, cloud.SchemaVersionRecommendV2, report.SchemaVersion)
	assert.Equal(t, cloud.ProviderAWS, report.Request.Cloud, "cloud defaults to aws")
	assert.Equal(t, []cloud.Region{cloud.RegionAll}, report.Request.Regions, "regions default to all")
	assert.Equal(t, cloud.OSLinux, report.Request.OS)
	assert.Equal(t, cloud.WorkloadCost, report.Request.Workload, "workload defaults to the risk-free cost policy")
	assert.Equal(t, cloud.DefaultTop, report.Request.Top)
	assert.Empty(t, report.Request.Machine)
	assert.Nil(t, report.Request.MaxPricePerHour, "an omitted ceiling is null, not zero")
	assert.Equal(t, []string{}, report.Warnings)
	assert.Equal(t, cloud.RankingPolicy(), report.RankingPolicy)
}

// Canonical prices are decimal strings with nine fractional digits, so a
// consumer never reconstructs an amount from a float.
func TestRecommendPublishesCanonicalPriceStrings(t *testing.T) {
	_, registry := awsStub(buildCandidates(testCandidate{
		Region: "us-east-1", Machine: "m6i.large", Price: 0.0416, Savings: 72,
		RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 2, MemoryGiB: 8,
	})...)

	report := decodeReport(t, callRecommend(t, registry, map[string]any{
		"architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
	}))
	require.Len(t, report.Recommendations, 1)

	recommendation := report.Recommendations[0]
	assert.Equal(t, "0.041600000", recommendation.SpotUSDPerHour)
	assert.Nil(t, recommendation.OnDemandUSDPerHour, "the AWS feeds publish savings, not an on-demand price")
	require.NotNil(t, recommendation.SavingsPercent)
	assert.InDelta(t, 72.0, *recommendation.SavingsPercent, 0)
	assert.Equal(t, cloud.RiskStatusAvailable, recommendation.Risk.Status)
	require.NotNil(t, recommendation.Risk.Kind)
	assert.Equal(t, "interruption_bucket", *recommendation.Risk.Kind)
}

// A provider without risk data reports it explicitly. Nothing renders unknown
// risk as a zero, and the cost policy makes no interruption claim.
func TestRecommendReportsUnavailableRiskExplicitly(t *testing.T) {
	registry := newStubRegistry(offlineProvider(cloud.ProviderGCP, gcpCandidates()))

	report := decodeReport(t, callRecommend(t, registry, map[string]any{
		"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
	}))
	require.Len(t, report.Recommendations, 1)

	risk := report.Recommendations[0].Risk
	assert.Equal(t, cloud.RiskStatusUnavailable, risk.Status)
	assert.Nil(t, risk.Kind)
	assert.Nil(t, risk.MinPercent)
	assert.Nil(t, risk.MaxPercent)
	assert.Nil(t, risk.WindowDays)
	assert.Contains(t, report.Recommendations[0].RationaleCodes, "COST_POLICY")
}

// Every failure is a published error payload with a stable code, and no failure
// carries recommendations.
func TestRecommendErrorContract(t *testing.T) {
	schema := loadContractSchema(t, "recommend-spot-instances-v2-error.schema.json")
	riskFree := newStubRegistry(offlineProvider(cloud.ProviderGCP, gcpCandidates()))

	for _, test := range []struct {
		name      string
		registry  providerRegistry
		args      map[string]any
		wantCode  cloud.ErrorCode
		wantCloud *string
	}{
		{
			name: "unknown cloud", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args:      map[string]any{"cloud": "ibm", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8},
			wantCloud: stringPtr("ibm"),
		},
		{
			name: "unparsable cloud argument", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{"cloud": 42, "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8},
		},
		{
			name: "missing architecture", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args:      map[string]any{"cloud": "gcp", "min_vcpu": 2, "min_memory_gib": 8},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "zero vcpu", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args:      map[string]any{"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 0, "min_memory_gib": 8},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "negative price ceiling", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"max_price_per_hour": -1,
			},
			wantCloud: stringPtr("gcp"),
		},
		// The MCP server does not enforce the advertised input schema, so every
		// mistyped argument arrives at the handler. None of them may be coerced
		// into a filter, a default, or a ceiling the caller never sent.
		{
			name: "machine wrapped in an array", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"machine": []any{"n2-standard-2"},
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "unreadable top", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8, "top": "xyz",
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "explicit zero top", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8, "top": 0,
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			// A quoted number is accepted, so the non-finite spellings ParseFloat
			// also accepts must be refused by the bounds rather than becoming a
			// comparison every machine passes.
			name: "quoted non-finite minimum memory", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": "NaN",
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "quoted non-finite price ceiling", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"max_price_per_hour": "+Inf",
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "fractional top", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8, "top": 1.5,
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "boolean minimum vcpu", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": true, "min_memory_gib": 8,
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "boolean price ceiling", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"max_price_per_hour": true,
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "regions given as an object", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"regions": map[string]any{"name": "us-central1"},
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			// cast would split this on whitespace into two regions the caller
			// never named, and one region name is not the array the contract
			// declares.
			name: "regions given as a bare string", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"regions": "us-central1 us-east1",
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			// cast would render this as the region "1", which no provider
			// publishes, so the request would fail as "no candidates" instead of
			// naming the malformed argument.
			name: "region element that is not a string", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"regions": []any{"us-central1", 1},
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "empty region name", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"regions": []any{""},
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			// The schema declares minItems 1. Widening `[]` to every region
			// answers a broader question than the caller asked, with status ok.
			name: "empty regions array", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"regions": []any{},
			},
			wantCloud: stringPtr("gcp"),
		},
		// An explicit "" is not one of the enum values the contract declares.
		// Read as an omission it would answer a different question than the
		// caller asked — as AWS, as Linux, as the cost policy — with status ok.
		{
			// The echoed cloud is null: "" is not a cloud, and the payload never
			// invents one the caller did not name.
			name: "explicit empty cloud", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
			},
		},
		{
			name: "explicit empty os", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8, "os": "",
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "explicit empty workload", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8, "workload": "",
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			// The advertised object is closed but nothing upstream enforces it,
			// so a misspelled ceiling must fail here. Dropped, it would return
			// recommendations with no ceiling applied and status ok.
			name: "undeclared argument", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"budget": 0.05,
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "unregistered cloud", registry: riskFree, wantCode: cloud.CodeDataUnavailable,
			args:      map[string]any{"cloud": "azure", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8},
			wantCloud: stringPtr("azure"),
		},
		{
			// A malformed request is malformed whichever cloud it names. Resolving
			// the provider first would answer DATA_UNAVAILABLE and hide the bound
			// the caller actually got wrong, so the same request would change code
			// the day that cloud's snapshot is enabled.
			name: "invalid bound on an unregistered cloud", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args:      map[string]any{"cloud": "azure", "architecture": "x86_64", "min_vcpu": 0, "min_memory_gib": 8},
			wantCloud: stringPtr("azure"),
		},
		{
			name:     "region keyword mixed with an explicit region on an unregistered cloud",
			registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{
				"cloud": "azure", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"regions": []any{"all", "eastus"},
			},
			wantCloud: stringPtr("azure"),
		},
		{
			name: "risk-aware workload on a risk-free provider", registry: riskFree, wantCode: cloud.CodeUnsupportedCapability,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8, "workload": "web",
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "windows on a linux-only provider", registry: riskFree, wantCode: cloud.CodeUnsupportedCapability,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8, "os": "windows",
			},
			wantCloud: stringPtr("gcp"),
		},
		{
			name: "no matching candidate", registry: riskFree, wantCode: cloud.CodeNoCandidates,
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 64, "min_memory_gib": 8,
			},
			wantCloud: stringPtr("gcp"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := callRecommend(t, test.registry, test.args)

			assertValidAgainstSchema(t, schema, payloadOf(t, result))

			report := decodeError(t, result)
			assert.Equal(t, cloud.SchemaVersionErrorV1, report.SchemaVersion)
			assert.Equal(t, test.wantCode, report.Code)
			assert.NotEmpty(t, report.Message)
			assert.Equal(t, test.wantCloud, report.Cloud)
			assert.NotContains(t, string(payloadOf(t, result)), "recommendations")
		})
	}
}

// The rejection names every undeclared argument, in a stable order. The code
// alone cannot tell a caller which key was misspelled, and a sentinel-only
// assertion would pass on any other INVALID_ARGUMENT branch.
func TestRecommendNamesEveryUndeclaredArgument(t *testing.T) {
	provider := offlineProvider(cloud.ProviderGCP, gcpCandidates())

	result := callRecommend(t, newStubRegistry(provider), map[string]any{
		"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
		"budget": 0.05, "arch": "arm64",
	})

	report := decodeError(t, result)
	assert.Equal(t, cloud.CodeInvalidArgument, report.Code)
	assert.Contains(t, report.Message, "unknown argument: arch, budget")
	assert.Equal(t, 0, provider.callCount(), "an undeclared argument costs no acquisition")
}

// Input is validated before any provider is queried, so an invalid request
// costs no acquisition.
func TestRecommendValidatesBeforeAcquisition(t *testing.T) {
	provider := offlineProvider(cloud.ProviderGCP, gcpCandidates())
	registry := newStubRegistry(provider)

	for _, args := range []map[string]any{
		{"cloud": "gcp", "architecture": "riscv64", "min_vcpu": 2, "min_memory_gib": 8},
		{"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8, "workload": "batch"},
		{"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8, "machine": "n2-["},
	} {
		result := callRecommend(t, registry, args)
		assert.True(t, result.IsError)
	}

	assert.Equal(t, 0, provider.callCount(), "no invalid request may reach the provider")
}

// A binary composed without a registry reports it rather than panicking.
func TestRecommendWithoutARegistryReportsDataUnavailable(t *testing.T) {
	result := callRecommend(t, nil, map[string]any{
		"architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
	})

	assert.Equal(t, cloud.CodeDataUnavailable, decodeError(t, result).Code)
}

// The recorded success and error payloads document what a client receives.
func TestRecommendGoldenPayloads(t *testing.T) {
	_, registry := awsStub(buildCandidates(testCandidate{
		Region: "us-east-1", Machine: "m6i.large", Price: 0.0416, Savings: 72,
		RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 2, MemoryGiB: 8,
	})...)

	success := callRecommend(t, registry, map[string]any{
		"cloud": "aws", "regions": []any{"us-east-1"}, "architecture": "x86_64",
		"min_vcpu": 2, "min_memory_gib": 8, "workload": "web", "top": 3,
	})
	assertGolden(t, "recommend-spot-instances-v2-success.json", indentJSON(t, payloadOf(t, success)))

	failure := callRecommend(t, registry, map[string]any{
		"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
	})
	assertGolden(t, "recommend-spot-instances-v2-error.json", indentJSON(t, payloadOf(t, failure)))
}

func indentJSON(t *testing.T, payload []byte) []byte {
	t.Helper()

	var document any
	require.NoError(t, json.Unmarshal(payload, &document))

	encoded, err := json.MarshalIndent(document, "", "  ")
	require.NoError(t, err)

	return append(encoded, '\n')
}

func stringPtr(value string) *string { return &value }
