package mcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/providers"
	awsprovider "spotinfo/internal/providers/aws"
	"spotinfo/internal/spot"
)

// The MCP package no longer imports internal/spot in production code: tools
// consume neutral candidates from the provider registry. The helpers below keep
// the two test shapes that need something concrete — a programmable stub for
// behaviour tests, and the real embedded AWS provider for the concurrency
// tests, whose whole point is the shared state inside spot.Client.

// stubProvider is a programmable cloud.Provider. It records the queries it was
// given so a test can assert what reached the provider, and it is safe to call
// from several goroutines because the race tests share one.
type stubProvider struct {
	err          error
	id           cloud.ProviderID
	capabilities cloud.Capabilities
	result       cloud.Result
	queries      []cloud.Query
	mutex        sync.Mutex
}

func (p *stubProvider) ID() cloud.ProviderID             { return p.id }
func (p *stubProvider) Capabilities() cloud.Capabilities { return p.capabilities }

func (p *stubProvider) Query(_ context.Context, query *cloud.Query) (cloud.Result, error) {
	p.mutex.Lock()
	p.queries = append(p.queries, *query)
	p.mutex.Unlock()

	if p.err != nil {
		return cloud.Result{}, p.err
	}

	return p.result, nil
}

// lastQuery returns the query the provider was called with most recently.
func (p *stubProvider) lastQuery() cloud.Query {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return p.queries[len(p.queries)-1]
}

func (p *stubProvider) callCount() int {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return len(p.queries)
}

// awsCapabilities mirrors the production AWS adapter, so a tool gated against
// this stub is gated against what the binary actually declares.
// TestStubAWSCapabilitiesMatchTheAdapter keeps the copy honest — without it the
// adapter could drop a capability and every tool test here would keep passing
// while the binary started refusing the request.
func awsCapabilities() cloud.Capabilities {
	return cloud.Capabilities{
		OperatingSystems: []cloud.OperatingSystem{cloud.OSLinux, cloud.OSWindows},
		Architectures:    []cloud.Architecture{cloud.ArchitectureX8664, cloud.ArchitectureARM64},
		PlacementKind:    cloud.PlacementKindPlacementScore,
		SpotPrice:        true,
		MachineSpec:      true,
		Risk:             true,
		PlacementScore:   true,
		ZoneDetail:       true,
		LiveEnrichment:   true,
	}
}

// offlineLinuxCapabilities is a provider with committed Linux spot prices and
// no risk, score, or zone observations — the shape GCP and Azure will have.
func offlineLinuxCapabilities() cloud.Capabilities {
	return cloud.Capabilities{
		OperatingSystems: []cloud.OperatingSystem{cloud.OSLinux},
		Architectures:    []cloud.Architecture{cloud.ArchitectureX8664, cloud.ArchitectureARM64},
		SpotPrice:        true,
		MachineSpec:      true,
	}
}

// stubRegistry serves the providers a test registered and reports every other
// recognised cloud as unavailable, exactly as the compiled registry does.
//
// It records the data policy each lookup asked for. Without that, a tool that
// accepts `offline` and ignores it is indistinguishable from one that honours
// it — which is the defect the retired include_names parameter was.
type stubRegistry struct {
	providers map[cloud.ProviderID]cloud.Provider
	policies  []cloud.FetchPolicy
	mutex     sync.Mutex
}

func newStubRegistry(provided ...cloud.Provider) *stubRegistry {
	registry := &stubRegistry{providers: make(map[cloud.ProviderID]cloud.Provider, len(provided))}
	for _, provider := range provided {
		registry.providers[provider.ID()] = provider
	}

	return registry
}

func (r *stubRegistry) Get(id cloud.ProviderID, policy cloud.FetchPolicy) (cloud.Provider, error) {
	r.mutex.Lock()
	r.policies = append(r.policies, policy)
	r.mutex.Unlock()

	if provider, ok := r.providers[id]; ok {
		return provider, nil
	}

	return nil, fmt.Errorf("%w: cloud provider %q is unavailable (PROVIDER_NOT_REGISTERED)", cloud.ErrDataUnavailable, id)
}

// lastPolicy reports the data policy the most recent lookup asked for.
func (r *stubRegistry) lastPolicy() cloud.FetchPolicy {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return r.policies[len(r.policies)-1]
}

// compiledRegistry adapts the production provider registry, whose Get takes no
// data policy, to the seam the server needs. Only cmd/spotinfo builds a policy
// aware one; a test that just needs the real providers uses this.
type compiledRegistry struct{ registry *providers.Registry }

func (c compiledRegistry) Get(id cloud.ProviderID, _ cloud.FetchPolicy) (cloud.Provider, error) {
	return c.registry.Get(id)
}
func (c compiledRegistry) Registered() []cloud.ProviderID { return c.registry.Registered() }

func (r *stubRegistry) Registered() []cloud.ProviderID {
	ids := make([]cloud.ProviderID, 0, len(r.providers))
	for _, id := range cloud.ProviderIDs() {
		if _, ok := r.providers[id]; ok {
			ids = append(ids, id)
		}
	}

	return ids
}

// stubFor builds a provider of one cloud answering with the given candidates,
// stamped with that cloud so the published document is self-consistent.
//
// The fixtures are copied before they are stamped. Parallel subtests routinely
// share one fixture slice, and stamping it in place is a write the race
// detector reports against whichever sibling reads it.
func stubFor(id cloud.ProviderID, capabilities cloud.Capabilities, fixtures []cloud.Candidate) *stubProvider {
	candidates := slices.Clone(fixtures)
	for i := range candidates {
		candidates[i].Provider = id
	}

	return &stubProvider{
		id:           id,
		capabilities: capabilities,
		result: cloud.Result{
			Provider:   id,
			Mode:       cloud.DataModeEmbeddedSnapshot,
			Sources:    testSources(),
			Candidates: candidates,
		},
	}
}

// awsStub builds a registry holding one AWS provider that answers with the
// given candidates.
func awsStub(candidates ...cloud.Candidate) (*stubProvider, *stubRegistry) {
	provider := stubFor(cloud.ProviderAWS, awsCapabilities(), candidates)

	return provider, newStubRegistry(provider)
}

// failingAWSStub builds a registry whose AWS provider fails acquisition.
func failingAWSStub(err error) *stubRegistry {
	return newStubRegistry(&stubProvider{id: cloud.ProviderAWS, capabilities: awsCapabilities(), err: err})
}

// testSources is provenance shaped like a real sidecar manifest: the v2 schema
// requires at least one complete source, and a report cannot be built without it.
func testSources() []cloud.SourceRef {
	return []cloud.SourceRef{{
		FetchedAt:     time.Date(2026, time.August, 6, 8, 58, 27, 0, time.UTC),
		URL:           "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json",
		ContentSHA256: "f42df66cd52c9dc3ac28b6bb7e525627696eec60692d5cf56658c679f0012393",
		ParserVersion: "aws-spot-advisor-json/1",
		SchemaVersion: "aws.spot-advisor-feed/v1",
	}}
}

// testCandidate is a compact fixture description. build() fills the neutral
// shape, including the absences that matter: an unknown price and an
// unpublished savings figure are missing observations, not zeros.
type testCandidate struct { //nolint:govet // fixture readability beats field packing
	Region         string
	Machine        string
	Architecture   cloud.Architecture
	OS             cloud.OperatingSystem
	Price          float64
	Live           bool
	Savings        int
	RiskLabel      string
	RiskMin        float64
	RiskMax        float64
	VCPU           int
	MemoryGiB      float64
	RegionScore    *int
	ZoneScores     map[string]int
	ScoreFetchedAt *time.Time
	// RegionObtainability is the other placement kind: a probability in
	// 0.0-1.0, published under its own field so a consumer can tell which
	// measurement it was handed.
	RegionObtainability *float64
	// PlacementStatus is set only for the case a lookup came back empty. A row
	// carrying a figure is available by construction, and one nobody asked
	// about carries the zero value.
	PlacementStatus cloud.PlacementStatus
}

func (c testCandidate) build() cloud.Candidate {
	instanceOS := c.OS
	if instanceOS == "" {
		instanceOS = cloud.OSLinux
	}
	architecture := c.Architecture
	if architecture == "" {
		architecture = cloud.ArchitectureX8664
	}

	candidate := cloud.Candidate{
		Provider: cloud.ProviderAWS,
		OS:       instanceOS,
		Location: cloud.Location{Region: cloud.Region(c.Region)},
		Machine: cloud.MachineSpec{
			ID:           cloud.MachineID(c.Machine),
			Architecture: architecture,
			MemoryGiB:    c.MemoryGiB,
			VCPU:         c.VCPU,
		},
		Risk:            c.risk(),
		Placements:      c.placements(),
		PlacementStatus: c.PlacementStatus,
	}
	if c.Price > 0 {
		amount, err := cloud.MoneyFromFloat(c.Price)
		if err != nil {
			panic(err)
		}
		candidate.Spot = &cloud.PriceObservation{
			Location: candidate.Location,
			Class:    cloud.PriceClassSpot,
			Currency: cloud.CurrencyUSD,
			Unit:     cloud.BillingUnitInstanceHour,
			Amount:   amount,
			Live:     c.Live,
		}
	}
	if c.Savings > 0 {
		savings := c.Savings
		candidate.SavingsPercent = &savings
	}

	return candidate
}

func (c testCandidate) risk() cloud.RiskObservation {
	if c.RiskLabel == "" && c.RiskMax == 0 {
		return cloud.UnavailableRisk()
	}

	low, high := c.RiskMin, c.RiskMax

	return cloud.RiskObservation{
		MinPercent: &low,
		MaxPercent: &high,
		Window:     &cloud.HistoryWindow{Days: 30},
		Status:     cloud.RiskStatusAvailable,
		Kind:       cloud.RiskKindInterruptionFrequencyRange,
		Label:      c.RiskLabel,
		SourceURL:  "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json",
	}
}

func (c testCandidate) placements() []cloud.PlacementObservation {
	// Every score a stub publishes names its kind: the neutral DTO publishes
	// nothing for an observation whose kind it cannot read, because the number
	// would be on a scale nothing can name.
	placements := make([]cloud.PlacementObservation, 0, len(c.ZoneScores)+1)
	if c.RegionScore != nil {
		placements = append(placements, cloud.PlacementObservation{
			FetchedAt: c.ScoreFetchedAt,
			Location:  cloud.Location{Region: cloud.Region(c.Region)},
			Kind:      cloud.PlacementKindPlacementScore,
			Score:     *c.RegionScore,
		})
	}
	for _, zone := range slices.Sorted(maps.Keys(c.ZoneScores)) {
		placements = append(placements, cloud.PlacementObservation{
			FetchedAt: c.ScoreFetchedAt,
			Location:  cloud.Location{Region: cloud.Region(c.Region), Zone: zone},
			Kind:      cloud.PlacementKindPlacementScore,
			Score:     c.ZoneScores[zone],
		})
	}
	if c.RegionObtainability != nil {
		placements = append(placements, cloud.PlacementObservation{
			FetchedAt:     c.ScoreFetchedAt,
			Obtainability: c.RegionObtainability,
			Location:      cloud.Location{Region: cloud.Region(c.Region)},
			Kind:          cloud.PlacementKindObtainability,
		})
	}
	if len(placements) == 0 {
		return nil
	}

	return placements
}

func buildCandidates(fixtures ...testCandidate) []cloud.Candidate {
	candidates := make([]cloud.Candidate, 0, len(fixtures))
	for _, fixture := range fixtures {
		candidates = append(candidates, fixture.build())
	}

	return candidates
}

// newEmbeddedRegistry builds the production AWS provider over a spot client
// that does not wait on AWS.
//
// Pricing comes from the embedded copy. The advisor feed is still requested —
// useEmbedded gates pricing only — but the 1ms timeout expires almost at once
// and the fetch falls back to the embedded copy, so the request never delays a
// test meaningfully.
//
// These tests exist to exercise the client's sync.Once and shared-provider
// concurrency, which the embedded path does faithfully, without the AWS
// dependency that made them slow and reliant on network reachability.
func newEmbeddedRegistry() *stubRegistry {
	client := spot.NewWithOptions(time.Millisecond, true)
	// No AWS in unit tests. Without this, every instance the embedded feed does
	// not price triggers live-price enrichment, which blocks for livePriceTimeout
	// (10s) waiting on an API call that cannot succeed here.
	client.SetLivePriceProvider(nil)

	lookup, err := spot.LoadEmbeddedArchitectureLookup()
	if err != nil {
		panic(err)
	}

	provider, err := awsprovider.New(client, lookup)
	if err != nil {
		panic(err)
	}

	return newStubRegistry(provider)
}

// fixedSavingsClient returns advice the test wrote, so the real AWS adapter can
// be driven without AWS and without the embedded feeds.
type fixedSavingsClient struct{ advices []spot.Advice }

func (c *fixedSavingsClient) GetSpotSavings(context.Context, ...spot.GetSpotSavingsOption) ([]spot.Advice, error) {
	return c.advices, nil
}
func (*fixedSavingsClient) DataSource() string { return spot.DataSourceEmbedded }

// awsAdapterRegistry wires the production AWS adapter over fixed advice.
//
// The v1 response golden used to be built from a cloud.Provider stub, which
// meant it recorded the MCP renderer over candidates that were already neutral
// and never executed the spot.Advice -> cloud.Candidate conversion this PR
// introduces. Every conversion in that adapter — the float32 memory widening,
// the savings clamp, the price and zone-price handling, the region/zone
// placement split — was unpinned by the test whose whole purpose is to prove
// v1 byte compatibility. Driving it through the real adapter is what makes the
// golden able to observe a regression there.
func awsAdapterRegistry(t *testing.T, advices []spot.Advice) *stubRegistry {
	t.Helper()

	provider, err := awsprovider.New(&fixedSavingsClient{advices: advices}, noopArchitectures{})
	require.NoError(t, err)

	return newStubRegistry(provider)
}

// errAcquisition is the acquisition failure used where the message does not matter.
var errAcquisition = errors.New("acquisition failed")

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
		"%s records the published MCP contract; regenerate with UPDATE_GOLDEN=1 and review the diff", name)
}

// Every capability gate in this package is asserted against awsCapabilities().
// If the real adapter and this fixture drift, those gates prove nothing about
// the binary — a capability dropped upstream would start returning
// UNSUPPORTED_CAPABILITY to callers while every test here stayed green.
func TestStubAWSCapabilitiesMatchTheAdapter(t *testing.T) {
	t.Parallel()

	provider, err := awsprovider.New(&noopSavingsClient{}, noopArchitectures{})
	require.NoError(t, err)

	assert.Equal(t, provider.Capabilities(), awsCapabilities())
}

// noopSavingsClient and noopArchitectures build the real AWS adapter without
// touching AWS or the embedded snapshot; only its declared capabilities matter.
type noopSavingsClient struct{}

func (*noopSavingsClient) GetSpotSavings(context.Context, ...spot.GetSpotSavingsOption) ([]spot.Advice, error) {
	return nil, nil
}
func (*noopSavingsClient) DataSource() string { return spot.DataSourceEmbedded }

type noopArchitectures struct{}

func (noopArchitectures) ArchitectureForInstance(string) (spot.Architecture, bool) { return "", false }
