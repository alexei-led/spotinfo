package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	"spotinfo/internal/mcp"
	"spotinfo/internal/providers"
	azureprovider "spotinfo/internal/providers/azure"
	gcpprovider "spotinfo/internal/providers/gcp"
)

// shippedRegistry is the wiring the binary uses, minus AWS and pinned offline.
// AWS needs a live client, and these tests answer from the committed snapshots;
// the AWS paths are covered by their own tests.
//
// Azure goes through WithLivePrices because a bare azureprovider.New() has
// Offline: false and liveBase() falls back to the contracted endpoint, so a test
// naming one or two covered regions swept prices.azure.com for real — and, the
// path being fail-soft, passed while doing it. The data_source.mode assertions
// in the region-naming tests below go red if this pin is ever dropped.
func shippedRegistry(t *testing.T) *providers.Registry {
	t.Helper()

	registry, err := providers.New(
		providers.Registration{
			ID:    cloud.ProviderGCP,
			Build: func() (cloud.Provider, error) { return gcpprovider.New() },
		},
		providers.Registration{
			ID: cloud.ProviderAzure,
			Build: func() (cloud.Provider, error) {
				provider, buildErr := azureprovider.New()
				if buildErr != nil {
					return nil, buildErr
				}

				return provider.WithLivePrices(azureprovider.LivePriceConfig{
					Policy: cloud.FetchPolicy{Offline: true},
				}), nil
			},
		},
	)
	require.NoError(t, err)

	return registry
}

// gcpOnlyRegistry is a build that left one recognised cloud out. It is the only
// way to reach the DATA_UNAVAILABLE branch now that every provider ships with a
// valid snapshot.
func gcpOnlyRegistry(t *testing.T) *providers.Registry {
	t.Helper()

	registry, err := providers.New(providers.Registration{
		ID:    cloud.ProviderGCP,
		Build: func() (cloud.Provider, error) { return gcpprovider.New() },
	})
	require.NoError(t, err)

	return registry
}

// runMulticloudRecommend runs the real recommend command. Its callers are
// deliberately not parallel: urfave/cli appends its package-level HelpFlag to
// every command it parses and writes to it in Apply, so two concurrent app.Run
// calls race inside the library.
func runMulticloudRecommend(t *testing.T, args ...string) (string, error) {
	t.Helper()

	return runRecommendWith(t, shippedRegistry(t), args...)
}

func runRecommendWith(t *testing.T, registry *providers.Registry, args ...string) (string, error) {
	t.Helper()

	var output bytes.Buffer

	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), registry, &output)
		},
	)
	err := app.Run(append([]string{"spotinfo", "recommend"}, args...))

	return output.String(), err
}

func TestOfflineCloudsRegisterFromTheirCommittedSnapshots(t *testing.T) {
	t.Parallel()

	registry := shippedRegistry(t)

	// Providers are built on first use, so ask for each one: that is what proves
	// its committed snapshot loads, and Status then reports the result.
	for _, id := range []cloud.ProviderID{cloud.ProviderGCP, cloud.ProviderAzure} {
		provider, err := registry.Get(id)
		require.NoError(t, err)
		assert.Equal(t, id, provider.ID())
	}

	byID := make(map[cloud.ProviderID]providers.Status)
	for _, entry := range registry.Status() {
		byID[entry.ID] = entry
	}

	for _, id := range []cloud.ProviderID{cloud.ProviderGCP, cloud.ProviderAzure} {
		require.Contains(t, byID, id)
		assert.True(t, byID[id].Enabled, byID[id].Detail)
	}
}

func TestAnUnregisteredCloudIsReportedRatherThanAnsweredByAnotherCloud(t *testing.T) {
	_, err := runRecommendWith(t, gcpOnlyRegistry(t),
		"--cloud", "azure", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8")

	require.ErrorIs(t, err, cloud.ErrDataUnavailable)
}

// An unset --region on a non-AWS cloud means every region that cloud publishes.
// The declared default is an AWS region name, which GCP would never match.
func TestGCPRecommendationServesTheV2ContractOffline(t *testing.T) {
	output, err := runMulticloudRecommend(t,
		"--cloud", "gcp", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
		"--top", "3", "--output", "json")
	require.NoError(t, err)

	var report cloud.RecommendReport
	require.NoError(t, json.Unmarshal([]byte(output), &report))

	assert.Equal(t, cloud.SchemaVersionRecommendV3, report.SchemaVersion)
	assert.Equal(t, cloud.ProviderGCP, report.Request.Cloud)
	assert.Equal(t, []cloud.Region{cloud.RegionAll}, report.Request.Regions)
	assert.Equal(t, cloud.WorkloadCost, report.Request.Workload,
		"a cloud that publishes no risk cannot claim an interruption ceiling")
	assert.Equal(t, cloud.ProviderGCP, report.DataSource.Provider)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, report.DataSource.Mode)
	assert.NotEmpty(t, report.DataSource.Sources)
	require.Len(t, report.Recommendations, 3)

	for _, recommendation := range report.Recommendations {
		assert.Equal(t, cloud.ProviderGCP, recommendation.Cloud)
		assert.Equal(t, cloud.Region("us-central1"), recommendation.Region)
		assert.Equal(t, cloud.ArchitectureX8664, recommendation.Architecture)
		assert.GreaterOrEqual(t, recommendation.VCPU, 2)
		assert.GreaterOrEqual(t, recommendation.MemoryGiB, 8.0)
		assert.NotEmpty(t, recommendation.SpotUSDPerHour)
		assert.NotNil(t, recommendation.OnDemandUSDPerHour, "gcp publishes a paired list price")
		assert.NotNil(t, recommendation.SavingsPercent)
		assert.Equal(t, cloud.RiskStatusUnavailable, recommendation.Risk.Status)
		assert.Nil(t, recommendation.Risk.Label)
	}
}

// A --budget computed as a monthly figure divided by 720 carries more
// fractional digits than the fixed-point scale. It is a ceiling, not a measured
// price, so it is truncated and still filters — the v1 recommend path already
// accepts that same value, and rejecting it here would split the two.
func TestRecommendationAcceptsABudgetFinerThanTheScale(t *testing.T) {
	output, err := runMulticloudRecommend(t,
		"--cloud", "gcp", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
		"--max-price", "0.041666666666666664", "--top", "3", "--output", "json")
	require.NoError(t, err)

	var report cloud.RecommendReport
	require.NoError(t, json.Unmarshal([]byte(output), &report))

	require.NotNil(t, report.Request.MaxPrice)
	assert.InDelta(t, 0.041666666, *report.Request.MaxPrice, 1e-9,
		"the echoed ceiling is the truncated one the query actually used")
	require.NotEmpty(t, report.Recommendations)

	ceiling, err := cloud.ParseMoney("0.041666666")
	require.NoError(t, err)

	for _, recommendation := range report.Recommendations {
		require.NotNil(t, recommendation.SpotUSDPerHour)

		price, parseErr := cloud.ParseMoney(*recommendation.SpotUSDPerHour)
		require.NoError(t, parseErr)
		assert.LessOrEqual(t, price.Nanos(), ceiling.Nanos(),
			"every recommendation must sit under the truncated ceiling")
	}
}

func TestGCPRecommendationHonoursAnExplicitRegion(t *testing.T) {
	_, err := runMulticloudRecommend(t,
		"--cloud", "gcp", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
		"--region", "europe-west1", "--output", "json")
	require.ErrorIs(t, err, cloud.ErrNoCandidates,
		"the committed snapshot covers one region and never substitutes another")
}

func TestGCPRecommendationRejectsARiskAwareWorkload(t *testing.T) {
	_, err := runMulticloudRecommend(t,
		"--cloud", "gcp", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
		"--workload", "web")
	require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
}

func TestGCPRecommendationRejectsWindows(t *testing.T) {
	_, err := runMulticloudRecommend(t,
		"--cloud", "gcp", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
		"--os", "windows")
	require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
}

// Azure covers several regions, so an unset --region has to produce candidates
// from more than one of them and rank them together by price.
func TestAzureRecommendationServesTheV2ContractOffline(t *testing.T) {
	output, err := runMulticloudRecommend(t,
		"--cloud", "azure", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
		"--top", "5", "--output", "json")
	require.NoError(t, err)

	var report cloud.RecommendReport
	require.NoError(t, json.Unmarshal([]byte(output), &report))

	assert.Equal(t, cloud.SchemaVersionRecommendV3, report.SchemaVersion)
	assert.Equal(t, cloud.ProviderAzure, report.Request.Cloud)
	assert.Equal(t, cloud.WorkloadCost, report.Request.Workload,
		"a cloud that publishes no risk cannot claim an interruption ceiling")
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, report.DataSource.Mode)
	assert.NotEmpty(t, report.DataSource.Sources)
	require.Len(t, report.Recommendations, 5)

	for _, recommendation := range report.Recommendations {
		assert.Equal(t, cloud.ProviderAzure, recommendation.Cloud)
		assert.Equal(t, cloud.ArchitectureX8664, recommendation.Architecture)
		assert.GreaterOrEqual(t, recommendation.VCPU, 2)
		assert.GreaterOrEqual(t, recommendation.MemoryGiB, 8.0)
		assert.NotEmpty(t, recommendation.SpotUSDPerHour)
		assert.NotNil(t, recommendation.OnDemandUSDPerHour, "azure publishes a paired list price")
		assert.Equal(t, cloud.RiskStatusUnavailable, recommendation.Risk.Status)
		assert.Nil(t, recommendation.Risk.Label)
	}
}

func TestAzureRecommendationHonoursAnExplicitRegion(t *testing.T) {
	output, err := runMulticloudRecommend(t,
		"--cloud", "azure", "--architecture", "arm64", "--min-vcpu", "2", "--min-memory-gib", "8",
		"--region", "westeurope", "--top", "3", "--output", "json")
	require.NoError(t, err)

	var report cloud.RecommendReport
	require.NoError(t, json.Unmarshal([]byte(output), &report))
	require.NotEmpty(t, report.Recommendations)

	// A named covered region is the one shape that triggers a live Azure sweep,
	// so this is where the offline pin in shippedRegistry is checked. Anything
	// but embedded-snapshot means the test reached prices.azure.com.
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, report.DataSource.Mode)

	for _, recommendation := range report.Recommendations {
		assert.Equal(t, cloud.Region("westeurope"), recommendation.Region)
		assert.Equal(t, cloud.ArchitectureARM64, recommendation.Architecture)
	}
}

func TestAzureRecommendationNeverSubstitutesAnUncoveredRegion(t *testing.T) {
	_, err := runMulticloudRecommend(t,
		"--cloud", "azure", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
		"--region", "nowhere-north-1", "--output", "json")

	require.ErrorIs(t, err, cloud.ErrNoCandidates)
}

// An explicit --top 0 is a request for nothing, and the v1 path rejects it. The
// neutral path must not read it as "unset" and answer with the default instead.
func TestAnExplicitZeroTopIsRejected(t *testing.T) {
	_, err := runMulticloudRecommend(t,
		"--cloud", "gcp", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8", "--top", "0")

	require.ErrorIs(t, err, cloud.ErrInvalidArgument)
}

func TestAzureRecommendationRejectsWhatItCannotAnswer(t *testing.T) {
	for name, args := range map[string][]string{
		"risk-aware workload": {"--workload", "web"},
	} {
		base := []string{"--cloud", "azure", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8"}

		_, err := runMulticloudRecommend(t, append(base, args...)...)
		require.ErrorIs(t, err, cloud.ErrUnsupportedCapability, name)
	}
}

// TestWindowsIsAnsweredOnAzureAndRefusedOnGCP is the asymmetry the widened
// catalogue key buys: Azure publishes a licence-bundled meter for most sizes,
// Google publishes no Windows Spot price at all, and a cloud that has no figure
// refuses rather than answering with the Linux one.
func TestWindowsIsAnsweredOnAzureAndRefusedOnGCP(t *testing.T) {
	base := []string{"--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
		"--os", "windows", "--top", "3", "--output", "json"}

	output, err := runMulticloudRecommend(t, append([]string{"--cloud", "azure"}, base...)...)
	require.NoError(t, err)

	var report cloud.RecommendReport
	require.NoError(t, json.Unmarshal([]byte(output), &report))

	assert.Equal(t, cloud.OSWindows, report.Request.OS)
	require.Len(t, report.Recommendations, 3)

	for _, recommendation := range report.Recommendations {
		assert.Equal(t, cloud.ProviderAzure, recommendation.Cloud)
		assert.NotEmpty(t, recommendation.SpotUSDPerHour)
		assert.NotNil(t, recommendation.OnDemandUSDPerHour,
			"the windows spot price is paired with the windows list price, never the linux one")
	}

	_, err = runMulticloudRecommend(t, append([]string{"--cloud", "gcp"}, base...)...)
	require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
}

// TestListAnswersWindowsOnAzure covers the other command: --os is in both
// vocabularies, and list renders the rows the same key selects.
func TestListAnswersWindowsOnAzure(t *testing.T) {
	output, err := runListWith(t, shippedRegistry(t), nil,
		"--cloud", "azure", "--os", "windows", "--region", "westeurope", "--output", "json")
	require.NoError(t, err)

	var report cloud.ListReport
	require.NoError(t, json.Unmarshal([]byte(output), &report))

	assert.Equal(t, cloud.OSWindows, report.Request.OS)
	// Same check as the recommend path: a named covered region must be answered
	// from the committed catalogue, never from a live sweep.
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, report.DataSource.Mode)
	require.NotEmpty(t, report.Candidates)

	for _, candidate := range report.Candidates {
		assert.Equal(t, cloud.ProviderAzure, candidate.Cloud)
		assert.Equal(t, cloud.OSWindows, candidate.OS)
	}
}

// callRecommendTool exercises the MCP tool against the shipped registry, which
// is where the embedded GCP snapshot meets the published v2 contract.
func callRecommendTool(t *testing.T, args map[string]any) *mcpgo.CallToolResult {
	t.Helper()

	return callRecommendToolWith(t, shippedRegistry(t), args)
}

func callRecommendToolWith(t *testing.T, registry *providers.Registry, args map[string]any) *mcpgo.CallToolResult {
	t.Helper()

	tool := mcp.NewRecommendTool(fixedProviders{registry: registry}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	result, err := tool.Handle(context.Background(), mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{Name: "recommend_spot_machines", Arguments: args},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

func toolPayload(t *testing.T, result *mcpgo.CallToolResult) map[string]any {
	t.Helper()

	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(mcpgo.TextContent)
	require.True(t, ok)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text.Text), &payload))

	return payload
}

func TestMCPRecommendServesGCPFromTheCommittedSnapshot(t *testing.T) {
	t.Parallel()

	result := callRecommendTool(t, map[string]any{
		"cloud": "gcp", "architecture": "arm64", "min_vcpu": 4, "min_memory_gib": 8, "top": 2,
	})
	require.False(t, result.IsError)

	payload := toolPayload(t, result)
	assert.Equal(t, "spotinfo.recommend/v3", payload["schema_version"])
	assert.Equal(t, "ok", payload["status"])

	recommendations, ok := payload["recommendations"].([]any)
	require.True(t, ok)
	require.Len(t, recommendations, 2)

	first, ok := recommendations[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "gcp", first["cloud"])
	assert.Equal(t, "us-central1", first["region"])
	assert.Equal(t, "arm64", first["architecture"])
	assert.NotNil(t, first["on_demand_usd_per_hour"])

	risk, ok := first["risk"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "unavailable", risk["status"])
	assert.Nil(t, risk["label"])
}

func TestMCPRecommendReportsTheDocumentedGCPRefusals(t *testing.T) {
	t.Parallel()

	for name, expectation := range map[string]struct {
		args map[string]any
		code string
	}{
		"risk-aware workload": {
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8, "workload": "batch",
			},
			code: "UNSUPPORTED_CAPABILITY",
		},
		"windows": {
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8, "os": "windows",
			},
			code: "UNSUPPORTED_CAPABILITY",
		},
		"uncovered region": {
			args: map[string]any{
				"cloud": "gcp", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"regions": []any{"europe-west1"},
			},
			code: "NO_CANDIDATES",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := callRecommendTool(t, expectation.args)
			require.True(t, result.IsError)

			payload := toolPayload(t, result)
			assert.Equal(t, expectation.code, payload["code"])
			assert.Equal(t, expectation.args["cloud"], payload["cloud"])
		})
	}
}

func TestMCPRecommendServesAzureFromTheCommittedSnapshot(t *testing.T) {
	t.Parallel()

	result := callRecommendTool(t, map[string]any{
		"cloud": "azure", "architecture": "arm64", "min_vcpu": 4, "min_memory_gib": 8, "top": 2,
	})
	require.False(t, result.IsError)

	payload := toolPayload(t, result)
	assert.Equal(t, "spotinfo.recommend/v3", payload["schema_version"])
	assert.Equal(t, "ok", payload["status"])

	recommendations, ok := payload["recommendations"].([]any)
	require.True(t, ok)
	require.Len(t, recommendations, 2)

	first, ok := recommendations[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "azure", first["cloud"])
	assert.Equal(t, "arm64", first["architecture"])
	assert.NotNil(t, first["on_demand_usd_per_hour"])
	assert.Regexp(t, `^Standard_`, first["machine"])

	risk, ok := first["risk"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "unavailable", risk["status"])
	assert.Nil(t, risk["label"])
}

func TestMCPRecommendReportsTheDocumentedAzureRefusals(t *testing.T) {
	t.Parallel()

	for name, expectation := range map[string]struct {
		args map[string]any
		code string
	}{
		"risk-aware workload": {
			args: map[string]any{
				"cloud": "azure", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8, "workload": "ci",
			},
			code: "UNSUPPORTED_CAPABILITY",
		},
		// Windows is no longer here: the committed catalogue prices the
		// licence-bundled meters, and TestWindowsIsAnsweredOnAzureAndRefusedOnGCP
		// covers the answer and the refusal it is still paired with.
		"uncovered region": {
			args: map[string]any{
				"cloud": "azure", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
				"regions": []any{"nowhere-north-1"},
			},
			code: "NO_CANDIDATES",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := callRecommendTool(t, expectation.args)
			require.True(t, result.IsError)

			payload := toolPayload(t, result)
			assert.Equal(t, expectation.code, payload["code"])
			assert.Equal(t, "azure", payload["cloud"])
		})
	}
}

// A cloud the binary was built without must be reported, never answered by a
// different cloud's prices.
func TestMCPRecommendReportsAnUnregisteredCloud(t *testing.T) {
	t.Parallel()

	result := callRecommendToolWith(t, gcpOnlyRegistry(t), map[string]any{
		"cloud": "azure", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8,
	})
	require.True(t, result.IsError)

	payload := toolPayload(t, result)
	assert.Equal(t, "DATA_UNAVAILABLE", payload["code"])
	assert.Equal(t, "azure", payload["cloud"])
}

// fixedProviders serves one compiled registry whatever data policy a tool call
// asks for. The policy decides which acquisition client the server builds, and
// these tests hand it a client they already made — see mcpProviders in main.go
// for the production wiring, and TestTheMCPToolsPassTheDataPolicyToAcquisition
// for the assertion that the policy is not merely accepted.
type fixedProviders struct{ registry *providers.Registry }

func (f fixedProviders) Get(id cloud.ProviderID, _ cloud.FetchPolicy) (cloud.Provider, error) {
	return f.registry.Get(id)
}
func (f fixedProviders) Registered() []cloud.ProviderID { return f.registry.Registered() }
