package mcp

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The goldens in testdata record the AWS v1 MCP contract before the
// provider-neutral seam exists: the exact input schema clients see, and a
// normalized response built from fixed advice.
//
// Regenerate deliberately with UPDATE_GOLDEN=1. A diff here is a client-visible
// contract change and needs review — including after an mcp-go upgrade, which
// can alter the schema without any change in this repository.

func TestFindSpotInstancesInputSchemaMatchesRecordedV1Contract(t *testing.T) {
	server, err := NewServer(Config{Version: "1.0.0", Logger: slog.Default(), Providers: newEmbeddedRegistry()})
	require.NoError(t, err)

	registered, ok := server.mcpServer.ListTools()["find_spot_instances"]
	require.True(t, ok, "find_spot_instances must stay registered under its v1 name")

	encoded, err := json.MarshalIndent(registered.Tool, "", "  ")
	require.NoError(t, err)
	assertGolden(t, "find-spot-instances-v1-input-schema.json", append(encoded, '\n'))
}

func TestFindSpotInstancesResponseMatchesRecordedV1Contract(t *testing.T) {
	regionScore := 8
	_, registry := awsStub(buildCandidates(
		testCandidate{
			Region: "us-east-1", Machine: "m6i.large", Price: 0.0416, Savings: 72,
			RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 2, MemoryGiB: 8,
		},
		testCandidate{
			Region: "us-west-2", Machine: "m5.xlarge", Price: 0.1234, Savings: 65, Live: true,
			RiskLabel: "5-10%", RiskMin: 5, RiskMax: 11, VCPU: 4, MemoryGiB: 16,
			RegionScore: &regionScore,
		},
	)...)

	result, err := NewFindSpotInstancesTool(registry, slog.Default()).
		Handle(t.Context(), mcp.CallToolRequest{})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	encoded, err := json.MarshalIndent(normalizeV1Response(t, text.Text), "", "  ")
	require.NoError(t, err)
	assertGolden(t, "find-spot-instances-v1-response.json", append(encoded, '\n'))
}

// normalizeV1Response removes the only two sources of run-to-run variation:
// elapsed query time, and the region list built from a map. Nothing else is
// reordered — results carry the sort order the v1 contract promises.
func normalizeV1Response(t *testing.T, payload string) map[string]any {
	t.Helper()

	var response map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &response))

	metadata, ok := response[fieldMetadata].(map[string]any)
	require.True(t, ok)
	metadata[fieldQueryTimeMS] = 0

	regions, ok := metadata[fieldRegionsSearched].([]any)
	require.True(t, ok)

	names := make([]string, 0, len(regions))
	for _, region := range regions {
		name, isString := region.(string)
		require.True(t, isString)
		names = append(names, name)
	}
	slices.Sort(names)
	metadata[fieldRegionsSearched] = names

	return response
}

func assertGolden(t *testing.T, name string, actual []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(path, actual, 0o600))
	}

	expected, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(expected), string(actual),
		"%s records the AWS v1 MCP contract; regenerate with UPDATE_GOLDEN=1 and review the diff", name)
}
