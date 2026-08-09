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
	gcpprovider "spotinfo/internal/providers/gcp"
)

// shippedRegistry is the wiring the binary uses, minus AWS. AWS needs a live
// client, and these tests are about the offline clouds; the AWS paths are
// covered by their own tests.
func shippedRegistry(t *testing.T) *providers.Registry {
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

	var output bytes.Buffer

	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), shippedRegistry(t), newMockspotClient(t), &output)
		},
	)
	err := app.Run(append([]string{"spotinfo", "recommend"}, args...))

	return output.String(), err
}

func TestGCPRegistersFromItsCommittedSnapshot(t *testing.T) {
	t.Parallel()

	status := shippedRegistry(t).Status()

	byID := make(map[cloud.ProviderID]providers.Status, len(status))
	for _, entry := range status {
		byID[entry.ID] = entry
	}

	require.Contains(t, byID, cloud.ProviderGCP)
	assert.True(t, byID[cloud.ProviderGCP].Enabled, byID[cloud.ProviderGCP].Detail)
	assert.False(t, byID[cloud.ProviderAzure].Enabled)
	assert.Equal(t, providers.ReasonNotRegistered, byID[cloud.ProviderAzure].Reason)
}

// An unset --region on a non-AWS cloud means every region that cloud publishes.
// The declared default is an AWS region name, which GCP would never match.
func TestGCPRecommendationServesTheV2ContractOffline(t *testing.T) {
	output, err := runMulticloudRecommend(t,
		"--cloud", "gcp", "--architecture", "x86_64", "--cpu", "2", "--memory", "8",
		"--top", "3", "--output", "json")
	require.NoError(t, err)

	var report cloud.RecommendReport
	require.NoError(t, json.Unmarshal([]byte(output), &report))

	assert.Equal(t, cloud.SchemaVersionRecommendV2, report.SchemaVersion)
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

func TestGCPRecommendationHonoursAnExplicitRegion(t *testing.T) {
	_, err := runMulticloudRecommend(t,
		"--cloud", "gcp", "--architecture", "x86_64", "--cpu", "2", "--memory", "8",
		"--region", "europe-west1", "--output", "json")
	require.ErrorIs(t, err, cloud.ErrNoCandidates,
		"the committed snapshot covers one region and never substitutes another")
}

func TestGCPRecommendationRejectsARiskAwareWorkload(t *testing.T) {
	_, err := runMulticloudRecommend(t,
		"--cloud", "gcp", "--architecture", "x86_64", "--cpu", "2", "--memory", "8",
		"--workload", "web")
	require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
}

func TestGCPRecommendationRejectsWindows(t *testing.T) {
	_, err := runMulticloudRecommend(t,
		"--cloud", "gcp", "--architecture", "x86_64", "--cpu", "2", "--memory", "8",
		"--os", "windows")
	require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
}

func TestAzureIsReportedUnavailableRatherThanAnsweredByAnotherCloud(t *testing.T) {
	_, err := runMulticloudRecommend(t,
		"--cloud", "azure", "--architecture", "x86_64", "--cpu", "2", "--memory", "8")
	require.ErrorIs(t, err, cloud.ErrDataUnavailable)
}

// callRecommendTool exercises the MCP tool against the shipped registry, which
// is where the embedded GCP snapshot meets the published v2 contract.
func callRecommendTool(t *testing.T, args map[string]any) *mcpgo.CallToolResult {
	t.Helper()

	tool := mcp.NewRecommendTool(shippedRegistry(t), slog.New(slog.NewTextHandler(io.Discard, nil)))

	result, err := tool.Handle(context.Background(), mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{Name: "recommend_spot_instances", Arguments: args},
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
	assert.Equal(t, "spotinfo.recommend/v2", payload["schema_version"])
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
		"unregistered cloud": {
			args: map[string]any{"cloud": "azure", "architecture": "x86_64", "min_vcpu": 2, "min_memory_gib": 8},
			code: "DATA_UNAVAILABLE",
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
