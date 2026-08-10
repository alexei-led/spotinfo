package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

// The CLI and the MCP tool emit the same spotinfo.recommend/v2 document, but
// only the MCP surface was validated against the published schema — every
// schema check lives in internal/mcp, and cmd/spotinfo never read the contract
// at all. `--top 999` therefore produced a payload whose request.top exceeded
// the schema's published maximum, and nothing failed.
//
// These tests bind the CLI's bound to the number in the contract file, so
// raising one without the other fails here rather than in a consumer.

func publishedTopBound(t *testing.T) float64 {
	t.Helper()

	path := filepath.Join("..", "..", "docs", "plans", "contracts",
		"recommend-spot-instances-v2-success.schema.json")
	contents, err := os.ReadFile(path) //nolint:gosec // G304: fixed repository path
	require.NoError(t, err)

	var schema struct {
		Properties struct {
			Request struct {
				Properties struct {
					Top struct {
						Maximum float64 `json:"maximum"`
						Minimum float64 `json:"minimum"`
					} `json:"top"`
				} `json:"properties"`
			} `json:"request"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(contents, &schema))

	top := schema.Properties.Request.Properties.Top
	require.Positive(t, top.Maximum, "the v2 contract must publish a request.top maximum")
	require.InDelta(t, 1.0, top.Minimum, 0, "the v2 contract must publish a request.top minimum of 1")

	return top.Maximum
}

func TestCLITopBoundMatchesThePublishedV2Contract(t *testing.T) {
	assert.InDelta(t, float64(cloud.MaxTop), publishedTopBound(t), 0,
		"cloud.MaxTop and the published request.top maximum must agree; "+
			"the CLI enforces MaxTop and the contract is what consumers validate against")
}

func TestCLIRejectsATopAboveThePublishedV2Contract(t *testing.T) {
	bound := int(publishedTopBound(t))

	_, err := runMulticloudRecommend(t,
		"--cloud", "gcp", "--architecture", "x86_64", "--cpu", "2", "--memory", "8",
		"--top", strconv.Itoa(bound+1))
	require.Error(t, err)
	assert.ErrorIs(t, err, cloud.ErrInvalidArgument)
	assert.Contains(t, err.Error(), "top must be between 1 and")
}

// The bound is enforced before the report path is chosen, so an AWS request
// that would have been answered by v1 is rejected identically. That placement
// is the point: the flag is the same flag on the same command, and which schema
// answers is chosen by an unrelated flag, so a caller cannot be expected to know
// the bound moves with it.
func TestCLIRejectsATopAboveTheBoundBeforeChoosingASchema(t *testing.T) {
	_, err := runRecommendWith(t, shippedRegistry(t),
		"--architecture", "x86_64", "--cpu", "2", "--memory", "8",
		"--workload", "web", "--top", strconv.Itoa(cloud.MaxTop+1))
	require.Error(t, err)
	assert.ErrorIs(t, err, cloud.ErrInvalidArgument)
}

// The emitted document must satisfy the bound it is validated against, at the
// boundary as well as below it.
func TestCLIEmitsAV2RequestTopWithinTheContract(t *testing.T) {
	output, err := runMulticloudRecommend(t,
		"--cloud", "gcp", "--architecture", "x86_64", "--cpu", "2", "--memory", "8",
		"--top", strconv.Itoa(cloud.MaxTop), "--output", "json")
	require.NoError(t, err)

	var payload struct {
		SchemaVersion string `json:"schema_version"`
		Request       struct {
			Top int `json:"top"`
		} `json:"request"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &payload))

	assert.Equal(t, "spotinfo.recommend/v2", payload.SchemaVersion)
	assert.LessOrEqual(t, payload.Request.Top, int(publishedTopBound(t)))
}
