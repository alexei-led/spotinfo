package gcp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

func record(start string, days int, rate float64) struct {
	Interval struct {
		StartTime time.Time `json:"startTime"`
		EndTime   time.Time `json:"endTime"`
	} `json:"interval"`
	PreemptionRate float64 `json:"preemptionRate"`
} {
	var out struct {
		Interval struct {
			StartTime time.Time `json:"startTime"`
			EndTime   time.Time `json:"endTime"`
		} `json:"interval"`
		PreemptionRate float64 `json:"preemptionRate"`
	}

	begin, err := time.Parse(time.RFC3339, start)
	if err != nil {
		panic(err)
	}

	out.Interval.StartTime = begin
	out.Interval.EndTime = begin.AddDate(0, 0, days)
	out.PreemptionRate = rate

	return out
}

// The published figure has to come from the series that was actually returned.
// The live API gave 16 to 30 daily records depending on the machine, so a
// hardcoded month would claim a span the data does not cover.
func TestSummarisePreemptionDerivesTheWindowFromTheSeries(t *testing.T) {
	t.Parallel()

	var response capacityHistoryResponse
	response.PreemptionHistory = append(response.PreemptionHistory,
		record("2026-07-11T07:00:00Z", 1, 0.07),
		record("2026-07-12T07:00:00Z", 1, 0.12),
		record("2026-07-13T07:00:00Z", 1, 0.04),
	)

	risk, err := summarisePreemption(&response)
	require.NoError(t, err)

	assert.Equal(t, cloud.RiskStatusAvailable, risk.Status)
	assert.Equal(t, cloud.RiskKindPreemptionRate, risk.Kind)
	require.NotNil(t, risk.MinPercent)
	require.NotNil(t, risk.MaxPercent)
	assert.InDelta(t, 4.0, *risk.MinPercent, 1e-9)
	assert.InDelta(t, 12.0, *risk.MaxPercent, 1e-9)
	assert.Equal(t, "7.7% avg", risk.Label)

	require.NotNil(t, risk.Window)
	assert.Equal(t, 3, risk.Window.Days, "window must span the observed records, not a fixed month")

	require.NotNil(t, risk.ObservedAt)
	assert.Equal(t, "2026-07-14T07:00:00Z", risk.ObservedAt.Format(time.RFC3339))
	assert.NotEmpty(t, risk.SourceURL)
}

// An empty series is what the API returns for a machine it has not observed. It
// must read as "no data", never as a rate of zero.
func TestSummarisePreemptionRejectsAnEmptySeries(t *testing.T) {
	t.Parallel()

	_, err := summarisePreemption(&capacityHistoryResponse{})
	require.ErrorIs(t, err, errNoHistory)
}

// The offline provider must stay offline. WithLiveRisk returns a copy so a
// registry-shared provider is never mutated into making network calls.
func TestWithLiveRiskDoesNotMutateTheSharedProvider(t *testing.T) {
	t.Parallel()

	offline, err := New()
	require.NoError(t, err)

	live := offline.WithLiveRisk(LiveRiskConfig{ProjectID: "example"})

	assert.Nil(t, offline.liveRisk, "the shared offline provider must not gain a live configuration")
	require.NotNil(t, live.liveRisk)
	assert.Equal(t, "example", live.liveRisk.ProjectID)

	// A provider without the configuration performs no lookup and reports no
	// error: enrichment is optional and must never fail an answer.
	require.NoError(t, offline.EnrichRisk(t.Context(), nil))
}

// Live risk without a project must be refused rather than billed somewhere.
func TestEnrichRiskNeedsAProject(t *testing.T) {
	t.Parallel()

	offline, err := New()
	require.NoError(t, err)

	live := offline.WithLiveRisk(LiveRiskConfig{})
	require.ErrorIs(t, live.EnrichRisk(t.Context(), nil), ErrNoProject)
}
