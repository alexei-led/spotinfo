package gcp

import (
	"encoding/json"
	"errors"
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

// adviceServer stands in for compute advice.capacity. It records every request
// so the tests can assert what was asked for, not only what came back.
type adviceServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []capacityAdviceRequest
	paths    []string
}

func newAdviceServer(t *testing.T, respond func(machines []string) (int, string)) *adviceServer {
	t.Helper()

	probe := &adviceServer{}
	probe.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var decoded capacityAdviceRequest
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		probe.mu.Lock()
		probe.requests = append(probe.requests, decoded)
		probe.paths = append(probe.paths, r.URL.Path)
		probe.mu.Unlock()

		status, body := respond(requestedMachines(&decoded))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(probe.server.Close)

	return probe
}

// endpoint mirrors capacityAdviceEndpoint's two verbs, so the production format
// string and the test one cannot drift apart silently.
func (p *adviceServer) endpoint() string {
	return p.server.URL + "/projects/%s/regions/%s/advice/capacity"
}

func (p *adviceServer) seen() ([]capacityAdviceRequest, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.requests), slices.Clone(p.paths)
}

// requestedMachines flattens the flexibility policy back into the machine types
// one request asked about, in a stable order.
func requestedMachines(request *capacityAdviceRequest) []string {
	machines := make([]string, 0, len(request.InstanceFlexibilityPolicy.InstanceSelections))
	for _, selection := range request.InstanceFlexibilityPolicy.InstanceSelections {
		machines = append(machines, selection.MachineTypes...)
	}
	slices.Sort(machines)

	return machines
}

func adviceBody(obtainability float64, uptime string) string {
	return fmt.Sprintf(`{"recommendations":[{"scores":{"obtainability":%v,"estimatedUptime":%q},`+
		`"shards":[{"instanceCount":1,"machineType":"x","provisioningModel":"SPOT","zone":"z"}]}]}`,
		obtainability, uptime)
}

func placementCandidate(machine, region string) *cloud.Candidate {
	return &cloud.Candidate{
		Provider: cloud.ProviderGCP,
		Location: cloud.Location{Region: cloud.Region(region)},
		Machine:  cloud.MachineSpec{ID: cloud.MachineID(machine)},
		Risk:     cloud.UnavailableRisk(),
	}
}

func placementProvider(t *testing.T, probe *adviceServer) *Provider {
	t.Helper()

	offline, err := New()
	require.NoError(t, err)

	return offline.WithPlacement(PlacementConfig{
		ProjectID: "example-project",
		Endpoint:  probe.endpoint(),
		Client:    probe.server.Client(),
	})
}

// The offline provider must stay offline. WithPlacement returns a copy so a
// registry-shared provider is never mutated into making network calls.
func TestWithPlacementDoesNotMutateTheSharedProvider(t *testing.T) {
	t.Parallel()

	offline, err := New()
	require.NoError(t, err)

	live := offline.WithPlacement(PlacementConfig{ProjectID: "example"})

	assert.Nil(t, offline.placement, "the shared offline provider must not gain a live configuration")
	require.NotNil(t, live.placement)
	assert.Equal(t, "example", live.placement.ProjectID)

	// A provider without the configuration performs no lookup and reports no
	// error: enrichment is optional and must never fail an answer.
	require.NoError(t, offline.EnrichPlacement(t.Context(), nil))
}

// An authenticated call must be billed to a project the caller named, never to
// one guessed from the environment gcloud happens to be pointing at.
func TestEnrichPlacementNeedsAProject(t *testing.T) {
	t.Parallel()

	offline, err := New()
	require.NoError(t, err)

	live := offline.WithPlacement(PlacementConfig{})
	require.ErrorIs(t, live.EnrichPlacement(t.Context(), nil), ErrNoProject)
}

func TestEnrichPlacementAttachesTheObtainabilityItFetched(t *testing.T) {
	t.Parallel()

	probe := newAdviceServer(t, func([]string) (int, string) {
		return http.StatusOK, adviceBody(0.9, "600s")
	})

	candidate := placementCandidate("n2-standard-4", "us-central1")
	require.NoError(t, placementProvider(t, probe).EnrichPlacement(t.Context(), []*cloud.Candidate{candidate}))

	assert.Equal(t, cloud.PlacementStatusAvailable, candidate.PlacementStatus)
	require.Len(t, candidate.Placements, 1)

	placement := candidate.Placements[0]
	assert.Equal(t, cloud.PlacementKindObtainability, placement.Kind)
	require.NotNil(t, placement.Obtainability)
	assert.InDelta(t, 0.9, *placement.Obtainability, 1e-9)
	assert.Zero(t, placement.Score, "an obtainability must never be written into the AWS integer field")
	assert.Equal(t, cloud.Region("us-central1"), placement.Location.Region)
	assert.Empty(t, placement.Location.Zone, "this provider declares no zone detail")
	require.NotNil(t, placement.FetchedAt)

	// The uptime estimate is the other half of the answer: how likely the
	// request is to succeed, and how long the machine is likely to last.
	require.NotNil(t, placement.EstimatedUptime)
	assert.Equal(t, 10*time.Minute, *placement.EstimatedUptime)

	requests, paths := probe.seen()
	require.Len(t, requests, 1)
	assert.Equal(t, []string{"n2-standard-4"}, requestedMachines(&requests[0]))
	assert.Equal(t, provisioningModelSpot, requests[0].InstanceProperties.Scheduling.ProvisioningModel,
		"only SPOT has an obtainability worth asking about")
	assert.Equal(t, anyZone, requests[0].DistributionPolicy.TargetShape,
		"a regional figure asks for capacity anywhere in the region")
	assert.Equal(t, requestedInstances, requests[0].Size,
		"obtainability is the likelihood of creating the requested number, so the number is part of the question")
	assert.Equal(t, "/projects/example-project/regions/us-central1/advice/capacity", paths[0],
		"the call must be billed to the named project and scoped to the candidate's own region")
}

// An answer with no uptime estimate still carries the obtainability: the two are
// separate readings and one missing must not discard the other.
func TestEnrichPlacementPublishesObtainabilityWithoutAnUptimeEstimate(t *testing.T) {
	t.Parallel()

	probe := newAdviceServer(t, func([]string) (int, string) {
		return http.StatusOK, `{"recommendations":[{"scores":{"obtainability":0.4}}]}`
	})

	candidate := placementCandidate("n2-standard-4", "us-central1")
	require.NoError(t, placementProvider(t, probe).EnrichPlacement(t.Context(), []*cloud.Candidate{candidate}))

	require.Len(t, candidate.Placements, 1)
	require.NotNil(t, candidate.Placements[0].Obtainability)
	assert.InDelta(t, 0.4, *candidate.Placements[0].Obtainability, 1e-9)
	assert.Nil(t, candidate.Placements[0].EstimatedUptime)
}

// Google documents a ceiling of five machine types per call. Over it the request
// is refused rather than shortened: a truncated request answers about a
// different set of machines than the caller asked about, and nothing downstream
// could tell.
func TestCapacityAdviceBodyHonoursGooglesMachineTypeCeiling(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		machines []string
		wantErr  error
	}{
		"none":         {machines: nil, wantErr: cloud.ErrInvalidArgument},
		"one":          {machines: []string{"a"}},
		"at the limit": {machines: []string{"a", "b", "c", "d", "e"}},
		"over":         {machines: []string{"a", "b", "c", "d", "e", "f"}, wantErr: ErrTooManyMachineTypes},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			encoded, err := capacityAdviceBody(tc.machines)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, encoded)

				return
			}

			require.NoError(t, err)

			var decoded capacityAdviceRequest
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			assert.Equal(t, tc.machines, requestedMachines(&decoded),
				"every requested machine type must reach the wire")
			assert.Len(t, decoded.InstanceFlexibilityPolicy.InstanceSelections, len(tc.machines),
				"each machine type is its own named selection")
		})
	}
}

// A page carrying more machines than one request may name is asked in several
// requests, and none of them is dropped. Seven is over Google's ceiling of five,
// which is the case a batching mistake would truncate.
func TestEnrichPlacementAsksAboutEveryMachineOverTheRequestCeiling(t *testing.T) {
	t.Parallel()

	probe := newAdviceServer(t, func([]string) (int, string) {
		return http.StatusOK, adviceBody(0.75, "1800s")
	})

	machines := []string{"n2-standard-2", "n2-standard-4", "n2-standard-8", "c3-standard-4",
		"c3-standard-8", "e2-standard-2", "e2-standard-4"}
	require.Greater(t, len(machines), maxMachineTypesPerRequest, "the page must exceed one request's ceiling")

	candidates := make([]*cloud.Candidate, 0, len(machines))
	for _, machine := range machines {
		candidates = append(candidates, placementCandidate(machine, "us-central1"))
	}

	require.NoError(t, placementProvider(t, probe).EnrichPlacement(t.Context(), candidates))

	requests, _ := probe.seen()
	assert.Len(t, requests, len(machines),
		"the API scores a whole request, so each machine needs its own question")

	asked := make([]string, 0, len(requests))
	for i := range requests {
		assert.LessOrEqual(t, len(requestedMachines(&requests[i])), maxMachineTypesPerRequest)
		asked = append(asked, requestedMachines(&requests[i])...)
	}
	assert.ElementsMatch(t, machines, asked, "no machine may be silently dropped from the batch")

	for _, candidate := range candidates {
		assert.Equal(t, cloud.PlacementStatusAvailable, candidate.PlacementStatus)
		require.Len(t, candidate.Placements, 1)
	}
}

// A lookup that fails leaves the candidate saying so. "Unavailable" is the
// honest report; a zero obtainability would read as "you will not get this".
func TestEnrichPlacementLeavesPlacementUnavailableWhenALookupFails(t *testing.T) {
	t.Parallel()

	for name, respond := range map[string]func([]string) (int, string){
		"server error":   func([]string) (int, string) { return http.StatusInternalServerError, "boom" },
		"not found":      func([]string) (int, string) { return http.StatusNotFound, `{"error":{"code":404}}` },
		"malformed json": func([]string) (int, string) { return http.StatusOK, "{not json" },
		"no advice":      func([]string) (int, string) { return http.StatusOK, `{"recommendations":[]}` },
		"absent score":   func([]string) (int, string) { return http.StatusOK, `{"recommendations":[{}]}` },
		"out of range":   func([]string) (int, string) { return http.StatusOK, adviceBody(1.4, "600s") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			probe := newAdviceServer(t, respond)
			candidate := placementCandidate("n2-standard-4", "us-central1")

			err := placementProvider(t, probe).EnrichPlacement(t.Context(), []*cloud.Candidate{candidate})

			require.Error(t, err, "a page where every lookup failed must say so, not read as no capacity")
			assert.Contains(t, err.Error(), "all 1 capacity advice lookups failed")
			assert.Equal(t, cloud.PlacementStatusUnavailable, candidate.PlacementStatus)
			assert.Empty(t, candidate.Placements, "an unfetched figure must never read as a measured one")
		})
	}
}

// One candidate failing must not deny the others their figure.
func TestEnrichPlacementIsPerCandidate(t *testing.T) {
	t.Parallel()

	probe := newAdviceServer(t, func(machines []string) (int, string) {
		if slices.Contains(machines, "n2-standard-4") {
			return http.StatusNotFound, "no advice"
		}

		return http.StatusOK, adviceBody(0.6, "900s")
	})

	failing := placementCandidate("n2-standard-4", "us-central1")
	working := placementCandidate("c3-standard-4", "us-central1")

	require.NoError(t, placementProvider(t, probe).EnrichPlacement(t.Context(),
		[]*cloud.Candidate{failing, working}))

	assert.Equal(t, cloud.PlacementStatusUnavailable, failing.PlacementStatus)
	assert.Empty(t, failing.Placements)
	assert.Equal(t, cloud.PlacementStatusAvailable, working.PlacementStatus)
	assert.Len(t, working.Placements, 1)
}

// The API answers per machine and region, so a page that repeats either must not
// repeat the call.
func TestEnrichPlacementDeduplicatesByMachineAndRegion(t *testing.T) {
	t.Parallel()

	probe := newAdviceServer(t, func([]string) (int, string) {
		return http.StatusOK, adviceBody(0.5, "600s")
	})

	candidates := make([]*cloud.Candidate, 0, 10)
	for range 5 {
		candidates = append(candidates,
			placementCandidate("n2-standard-4", "us-central1"),
			placementCandidate("n2-standard-4", "europe-west1"))
	}

	require.NoError(t, placementProvider(t, probe).EnrichPlacement(t.Context(), candidates))

	requests, paths := probe.seen()
	assert.Len(t, requests, 2, "one lookup per distinct (machine, region), not one per row")
	assert.ElementsMatch(t,
		[]string{
			"/projects/example-project/regions/us-central1/advice/capacity",
			"/projects/example-project/regions/europe-west1/advice/capacity",
		}, paths)

	for _, candidate := range candidates {
		assert.Equal(t, cloud.PlacementStatusAvailable, candidate.PlacementStatus)
	}
}

// A machine with no credentials must cost one discovery and report it, not a
// figure. The Application Default Credentials resolution is the seam swapped
// here, which is the only way to exercise it without a machine that has them.
//
// Serial: it replaces package state that every authenticated lookup reads.
func TestEnrichPlacementReportsMissingCredentials(t *testing.T) {
	original := httpClient
	t.Cleanup(func() { httpClient = original })

	noCredentials := errors.New("no Google Cloud credentials")
	httpClient = func() (*http.Client, error) { return nil, noCredentials }

	offline, err := New()
	require.NoError(t, err)

	// No Client injected, so the configuration falls through to the credential
	// resolution the production path uses.
	live := offline.WithPlacement(PlacementConfig{ProjectID: "example-project"})
	candidate := placementCandidate("n2-standard-4", "us-central1")

	require.ErrorIs(t, live.EnrichPlacement(t.Context(), []*cloud.Candidate{candidate}), noCredentials)
	assert.Equal(t, cloud.PlacementStatusUnavailable, candidate.PlacementStatus,
		"a candidate nobody could score reports the absence, never a zero")
	assert.Empty(t, candidate.Placements)
}

// The whole point of enriching the ranked page: a recommendation costs one
// request per row, not one per catalogue entry.
func TestRecommendFetchesObtainabilityForTheRankedPageOnly(t *testing.T) {
	t.Parallel()

	probe := newAdviceServer(t, func([]string) (int, string) {
		return http.StatusOK, adviceBody(0.8, "3600s")
	})

	provider := placementProvider(t, probe)

	catalogue, err := provider.Query(t.Context(), &cloud.Query{
		OS:      cloud.OSLinux,
		Regions: []cloud.Region{cloud.RegionAll},
	})
	require.NoError(t, err)

	const top = 3

	report, err := cloud.Recommend(t.Context(), provider, &cloud.RecommendRequest{
		Cloud:        cloud.ProviderGCP,
		Architecture: cloud.ArchitectureX8664,
		OS:           cloud.OSLinux,
		Workload:     cloud.WorkloadCost,
		Regions:      []cloud.Region{cloud.RegionAll},
		MinVCPU:      2,
		MinMemoryGiB: 4,
		Top:          top,
		Placement:    cloud.PlacementRequest{Enabled: true},
	})
	require.NoError(t, err)
	require.Len(t, report.Recommendations, top)

	requests, _ := probe.seen()
	assert.LessOrEqual(t, len(requests), top,
		"one request per recommendation at most, never per catalogue entry")
	assert.Less(t, len(requests), len(catalogue.Candidates)/2,
		"the catalogue is far larger than the page, and only the page is asked about")

	for _, recommendation := range report.Recommendations {
		require.NotNil(t, recommendation.RegionObtainability)
		assert.InDelta(t, 0.8, *recommendation.RegionObtainability, 1e-9)
		require.NotNil(t, recommendation.RegionEstimatedUptimeSeconds)
		assert.InDelta(t, 3600, *recommendation.RegionEstimatedUptimeSeconds, 1e-9)
		assert.Nil(t, recommendation.RegionScore, "an obtainability is not an AWS placement score")
		assert.Empty(t, recommendation.PlacementStatus, "a published figure already says it is available")
	}
}

// A failed lookup must never lose the ranked page: the recommendation is
// complete without it, and the placement figure is the optional extra.
func TestPlacementFailureDoesNotFailTheRecommendation(t *testing.T) {
	t.Parallel()

	probe := newAdviceServer(t, func([]string) (int, string) {
		return http.StatusForbidden, `{"error":{"code":403,"message":"compute.advice denied"}}`
	})

	report, err := cloud.Recommend(t.Context(), placementProvider(t, probe), &cloud.RecommendRequest{
		Cloud:        cloud.ProviderGCP,
		Architecture: cloud.ArchitectureX8664,
		OS:           cloud.OSLinux,
		Workload:     cloud.WorkloadCost,
		Regions:      []cloud.Region{cloud.RegionAll},
		MinVCPU:      2,
		MinMemoryGiB: 4,
		Top:          2,
		Placement:    cloud.PlacementRequest{Enabled: true},
	})

	require.NoError(t, err, "a 403 on the placement extra must not lose the ranked page")
	require.NotEmpty(t, report.Recommendations)

	for _, recommendation := range report.Recommendations {
		assert.Equal(t, cloud.PlacementStatusUnavailable, recommendation.PlacementStatus)
		assert.Nil(t, recommendation.RegionObtainability)
	}
}

// Asking a browse answer for obtainability is refused, not answered with an
// empty column: the figure is fetched for a ranked page and needs a project, and
// both surfaces reach this provider through Query.
func TestQueryRefusesPlacementWithoutARankedPageFetcher(t *testing.T) {
	t.Parallel()

	offline, err := New()
	require.NoError(t, err)

	_, err = offline.Query(t.Context(), &cloud.Query{
		OS:        cloud.OSLinux,
		Placement: cloud.PlacementRequest{Enabled: true},
	})

	require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
	assert.Contains(t, err.Error(), "ranked recommendation")
}

func TestReadAdviceRejectsAnUnreadableAnswer(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		body    string
		wantErr error
	}{
		"no recommendations": {body: `{"recommendations":[]}`, wantErr: ErrNoAdvice},
		"no score":           {body: `{"recommendations":[{"scores":{}}]}`, wantErr: ErrNoAdvice},
		"above one":          {body: adviceBody(1.01, "600s"), wantErr: cloud.ErrDataUnavailable},
		"below zero":         {body: adviceBody(-0.1, "600s"), wantErr: cloud.ErrDataUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var decoded capacityAdviceResponse
			require.NoError(t, json.NewDecoder(strings.NewReader(tc.body)).Decode(&decoded))

			_, err := readAdvice(&decoded, "us-central1")
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// Zero is a real reading — "you are very unlikely to get this" — and must be
// published rather than treated as an absent figure.
func TestReadAdvicePublishesAZeroObtainability(t *testing.T) {
	t.Parallel()

	var decoded capacityAdviceResponse
	require.NoError(t, json.NewDecoder(strings.NewReader(adviceBody(0, "60s"))).Decode(&decoded))

	placement, err := readAdvice(&decoded, "us-central1")
	require.NoError(t, err)
	require.NotNil(t, placement.Obtainability)
	assert.InDelta(t, 0.0, *placement.Obtainability, 1e-9)
}
