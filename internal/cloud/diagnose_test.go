package cloud

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What an empty answer costs to explain.
//
// The diagnosis asks the provider again, wider, and that second query is free
// against data already in memory and not free against a live API. The test is
// therefore the mode the failed answer reported, not whether the provider has a
// live path at all: every provider declares one now, so a capability test alone
// would leave this whole diagnosis unreachable.

// An answer served from the committed snapshot is explained: the second query
// reads the same bytes.
func TestAnEmptySnapshotAnswerIsExplained(t *testing.T) {
	t.Parallel()

	provider := riskFreeProvider()
	provider.capabilities.LiveEnrichment = true

	request := gcpCostRequest()
	request.Regions = []Region{"europe-west1"}

	_, err := Recommend(t.Context(), provider, request)
	require.ErrorIs(t, err, ErrNoCandidates)
	assert.Contains(t, err.Error(), "europe-west1",
		"the diagnosis must name the constraint that emptied the set")
	assert.Len(t, provider.queries, 2, "the ranked query, then the widened one")
}

// An answer served from a live API is not: a wider query there can mean more
// requests, and a better message is not worth a slower failure.
func TestAnEmptyLiveAnswerIsNotReQueried(t *testing.T) {
	t.Parallel()

	for _, mode := range []DataMode{DataModeLive, DataModeCached} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()

			provider := riskFreeProvider()
			provider.capabilities.LiveEnrichment = true
			provider.mode = mode

			request := gcpCostRequest()
			request.Regions = []Region{"europe-west1"}

			_, err := Recommend(t.Context(), provider, request)
			require.ErrorIs(t, err, ErrNoCandidates)
			assert.NotContains(t, err.Error(), "europe-west1")
			assert.Len(t, provider.queries, 1, "the failed request must not pay for a second live query")
		})
	}
}

// A provider with no live path at all is explained whatever it claims, because
// there is nothing a second query could fetch.
func TestAProviderWithNoLivePathIsAlwaysExplained(t *testing.T) {
	t.Parallel()

	provider := riskFreeProvider()
	provider.mode = DataModeLive

	request := gcpCostRequest()
	request.Regions = []Region{"europe-west1"}

	_, err := Recommend(t.Context(), provider, request)
	require.ErrorIs(t, err, ErrNoCandidates)
	assert.Contains(t, err.Error(), "europe-west1")
	assert.Len(t, provider.queries, 2)
}
