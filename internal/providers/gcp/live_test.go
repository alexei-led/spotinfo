package gcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
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

// liveRiskServer stands in for compute advice.capacityHistory. It records every
// request so the tests can assert what was asked for, not only what came back.
type liveRiskServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []capacityHistoryRequest
	paths    []string
}

func newLiveRiskServer(t *testing.T, respond func(machine string) (int, string)) *liveRiskServer {
	t.Helper()

	probe := &liveRiskServer{}
	probe.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var decoded capacityHistoryRequest
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		probe.mu.Lock()
		probe.requests = append(probe.requests, decoded)
		probe.paths = append(probe.paths, r.URL.Path)
		probe.mu.Unlock()

		status, body := respond(decoded.InstanceProperties.MachineType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(probe.server.Close)

	return probe
}

// endpoint mirrors capacityHistoryEndpoint's two verbs, so the production format
// string and the test one cannot drift apart silently.
func (p *liveRiskServer) endpoint() string {
	return p.server.URL + "/projects/%s/regions/%s/advice/capacityHistory"
}

func (p *liveRiskServer) seen() ([]capacityHistoryRequest, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.requests), slices.Clone(p.paths)
}

func historyBody(rates ...float64) string {
	records := make([]string, 0, len(rates))
	for i, rate := range rates {
		records = append(records, fmt.Sprintf(
			`{"interval":{"startTime":"2026-07-%02dT07:00:00Z","endTime":"2026-07-%02dT07:00:00Z"},"preemptionRate":%v}`,
			i+1, i+2, rate))
	}

	return `{"machineType":"x","preemptionHistory":[` + strings.Join(records, ",") + `]}`
}

func liveCandidate(machine, region string) *cloud.Candidate {
	return &cloud.Candidate{
		Provider: cloud.ProviderGCP,
		Location: cloud.Location{Region: cloud.Region(region)},
		Machine:  cloud.MachineSpec{ID: cloud.MachineID(machine)},
		Risk:     cloud.UnavailableRisk(),
	}
}

func liveProvider(t *testing.T, probe *liveRiskServer) *Provider {
	t.Helper()

	offline, err := New()
	require.NoError(t, err)

	return offline.WithLiveRisk(LiveRiskConfig{
		ProjectID: "example-project",
		Endpoint:  probe.endpoint(),
		Client:    probe.server.Client(),
	})
}

func TestEnrichRiskAttachesThePreemptionRateItFetched(t *testing.T) {
	t.Parallel()

	probe := newLiveRiskServer(t, func(string) (int, string) {
		return http.StatusOK, historyBody(0.07, 0.12, 0.04)
	})

	candidate := liveCandidate("n2-standard-4", "us-central1")
	require.NoError(t, liveProvider(t, probe).EnrichRisk(t.Context(), []*cloud.Candidate{candidate}))

	assert.Equal(t, cloud.RiskStatusAvailable, candidate.Risk.Status)
	assert.Equal(t, cloud.RiskKindPreemptionRate, candidate.Risk.Kind)
	assert.Equal(t, "7.7% avg", candidate.Risk.Label)

	requests, paths := probe.seen()
	require.Len(t, requests, 1)
	assert.Equal(t, "n2-standard-4", requests[0].InstanceProperties.MachineType)
	assert.Equal(t, "SPOT", requests[0].InstanceProperties.Scheduling.ProvisioningModel,
		"the API rejects the call without a provisioning model, and only SPOT has a preemption history")
	assert.Equal(t, []string{"PREEMPTION"}, requests[0].Types,
		"price history is not requested: the snapshot already carries prices")
	assert.Equal(t, "/projects/example-project/regions/us-central1/advice/capacityHistory", paths[0],
		"the call must be billed to the named project and scoped to the candidate's own region")
}

// A lookup that fails leaves the candidate with the risk it already had. The
// recommendation is complete without it, and reporting "unavailable" is honest
// where inventing a rate would not be.
func TestEnrichRiskLeavesRiskUnavailableWhenALookupFails(t *testing.T) {
	t.Parallel()

	for name, respond := range map[string]func(string) (int, string){
		"server error":   func(string) (int, string) { return http.StatusInternalServerError, "boom" },
		"not found":      func(string) (int, string) { return http.StatusNotFound, `{"error":{"code":404}}` },
		"malformed json": func(string) (int, string) { return http.StatusOK, "{not json" },
		"empty history":  func(string) (int, string) { return http.StatusOK, `{"preemptionHistory":[]}` },
		"absent history": func(string) (int, string) { return http.StatusOK, `{"machineType":"n2-standard-4"}` },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			probe := newLiveRiskServer(t, respond)
			candidate := liveCandidate("n2-standard-4", "us-central1")

			require.NoError(t, liveProvider(t, probe).EnrichRisk(t.Context(), []*cloud.Candidate{candidate}),
				"a failed lookup must not fail the recommendation")
			assert.Equal(t, cloud.RiskStatusUnavailable, candidate.Risk.Status)
			assert.Nil(t, candidate.Risk.MaxPercent, "an unfetched rate must never read as a measured one")
		})
	}
}

// One candidate failing must not deny the others their risk.
func TestEnrichRiskIsPerCandidate(t *testing.T) {
	t.Parallel()

	probe := newLiveRiskServer(t, func(machine string) (int, string) {
		if machine == "n2-standard-4" {
			return http.StatusNotFound, "no history"
		}

		return http.StatusOK, historyBody(0.10)
	})

	failing := liveCandidate("n2-standard-4", "us-central1")
	working := liveCandidate("c3-standard-4", "us-central1")

	require.NoError(t, liveProvider(t, probe).EnrichRisk(t.Context(),
		[]*cloud.Candidate{failing, working}))

	assert.Equal(t, cloud.RiskStatusUnavailable, failing.Risk.Status)
	assert.Equal(t, cloud.RiskStatusAvailable, working.Risk.Status)
}

// The API is one call per machine and region, so a page that repeats either must
// not repeat the call. Ten rows across two pairs is two requests, not ten.
func TestEnrichRiskDeduplicatesByMachineAndRegion(t *testing.T) {
	t.Parallel()

	probe := newLiveRiskServer(t, func(string) (int, string) {
		return http.StatusOK, historyBody(0.09)
	})

	candidates := make([]*cloud.Candidate, 0, 10)
	for range 5 {
		candidates = append(candidates,
			liveCandidate("n2-standard-4", "us-central1"),
			liveCandidate("n2-standard-4", "europe-west1"))
	}

	require.NoError(t, liveProvider(t, probe).EnrichRisk(t.Context(), candidates))

	requests, paths := probe.seen()
	assert.Len(t, requests, 2, "one lookup per distinct (machine, region), not one per row")
	assert.ElementsMatch(t,
		[]string{
			"/projects/example-project/regions/us-central1/advice/capacityHistory",
			"/projects/example-project/regions/europe-west1/advice/capacityHistory",
		}, paths)

	for _, candidate := range candidates {
		assert.Equal(t, cloud.RiskStatusAvailable, candidate.Risk.Status,
			"every row sharing a pair gets the answer that pair fetched")
	}
}
