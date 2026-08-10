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

// A zero-result query is part of the v1 contract too. regions_searched is built
// from a map, and the obvious spelling — slices.Sorted(maps.Keys(...)) — returns
// a nil slice for an empty map, which marshals as null rather than []. The
// recorded response golden only covers a non-empty search, so this pins the
// empty case directly against the raw JSON.
func TestFindSpotInstancesEmptyResultKeepsV1ArrayShape(t *testing.T) {
	t.Parallel()

	_, registry := awsStub()

	result, err := NewFindSpotInstancesTool(registry, slog.Default()).
		Handle(t.Context(), mcp.CallToolRequest{})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)

	assert.Contains(t, text.Text, `"regions_searched":[]`,
		"an empty search must publish [], not null, as v1 did")
	assert.NotContains(t, text.Text, `"regions_searched":null`)

	var response map[string]any
	require.NoError(t, json.Unmarshal([]byte(text.Text), &response))

	metadata, ok := response[fieldMetadata].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{}, metadata[fieldRegionsSearched])
	assert.InDelta(t, 0.0, metadata[fieldTotalResults], 0)
	assert.Equal(t, []any{}, response[fieldResults], "results was already an array and must stay one")
}

// v1 answered a negative max_interruption_rate as "no filter" and returned the
// full result set, and the published input schema sets no minimum on the field.
// Refusing it before acquisition would turn a working call into an error.
func TestFindSpotInstancesNegativeInterruptionRateKeepsV1NoFilter(t *testing.T) {
	t.Parallel()

	candidates := buildCandidates(
		testCandidate{
			Region: "us-east-1", Machine: "m6i.large", Price: 0.0416, Savings: 72,
			RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 2, MemoryGiB: 8,
		},
		testCandidate{
			Region: "us-west-2", Machine: "m5.xlarge", Price: 0.1234, Savings: 65,
			RiskLabel: "15-20%", RiskMin: 15, RiskMax: 20, VCPU: 4, MemoryGiB: 16,
		},
	)

	for _, rate := range []float64{-1, 0} {
		_, registry := awsStub(candidates...)

		result, err := NewFindSpotInstancesTool(registry, slog.Default()).Handle(t.Context(),
			mcp.CallToolRequest{Params: mcp.CallToolParams{
				Arguments: map[string]any{argMaxInterruptionRate: rate},
			}})
		require.NoError(t, err)
		require.False(t, result.IsError, "rate %v must not be refused; v1 read it as no filter", rate)

		text, ok := result.Content[0].(mcp.TextContent)
		require.True(t, ok)

		var response map[string]any
		require.NoError(t, json.Unmarshal([]byte(text.Text), &response))

		results, ok := response[fieldResults].([]any)
		require.True(t, ok)
		assert.Len(t, results, len(candidates),
			"rate %v must leave every candidate in the result set", rate)
	}
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

// assertGolden compares against a recorded contract file. A regeneration always
// fails the run: rewriting the file and then comparing it to itself would let a
// job that happens to set UPDATE_GOLDEN report a client-visible contract change
// as a pass.
func assertGolden(t *testing.T, name string, actual []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(path, actual, 0o600))
		t.Fatalf("%s regenerated; review the diff and re-run without UPDATE_GOLDEN", name)
	}

	expected, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(expected), string(actual),
		"%s records the AWS v1 MCP contract; regenerate with UPDATE_GOLDEN=1 and review the diff", name)
}
