package mcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"spotinfo/internal/cloud"
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
func awsCapabilities() cloud.Capabilities {
	return cloud.Capabilities{
		OperatingSystems: []cloud.OperatingSystem{cloud.OSLinux, cloud.OSWindows},
		Architectures:    []cloud.Architecture{cloud.ArchitectureX8664, cloud.ArchitectureARM64},
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
type stubRegistry struct {
	providers map[cloud.ProviderID]cloud.Provider
}

func newStubRegistry(provided ...cloud.Provider) *stubRegistry {
	registry := &stubRegistry{providers: make(map[cloud.ProviderID]cloud.Provider, len(provided))}
	for _, provider := range provided {
		registry.providers[provider.ID()] = provider
	}

	return registry
}

func (r *stubRegistry) Get(id cloud.ProviderID) (cloud.Provider, error) {
	if provider, ok := r.providers[id]; ok {
		return provider, nil
	}

	return nil, fmt.Errorf("%w: cloud provider %q is unavailable (PROVIDER_NOT_REGISTERED)", cloud.ErrDataUnavailable, id)
}

func (r *stubRegistry) Available() []cloud.Provider {
	available := make([]cloud.Provider, 0, len(r.providers))
	for _, id := range cloud.ProviderIDs() {
		if provider, ok := r.providers[id]; ok {
			available = append(available, provider)
		}
	}

	return available
}

// awsStub builds a registry holding one AWS provider that answers with the
// given candidates.
func awsStub(candidates ...cloud.Candidate) (*stubProvider, *stubRegistry) {
	provider := &stubProvider{
		id:           cloud.ProviderAWS,
		capabilities: awsCapabilities(),
		result: cloud.Result{
			Provider:   cloud.ProviderAWS,
			Mode:       cloud.DataModeEmbeddedSnapshot,
			Sources:    testSources(),
			Candidates: candidates,
		},
	}

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
		Risk:       c.risk(),
		Placements: c.placements(),
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
	placements := make([]cloud.PlacementObservation, 0, len(c.ZoneScores)+1)
	if c.RegionScore != nil {
		placements = append(placements, cloud.PlacementObservation{
			FetchedAt: c.ScoreFetchedAt,
			Location:  cloud.Location{Region: cloud.Region(c.Region)},
			Score:     *c.RegionScore,
		})
	}
	for _, zone := range slices.Sorted(maps.Keys(c.ZoneScores)) {
		placements = append(placements, cloud.PlacementObservation{
			FetchedAt: c.ScoreFetchedAt,
			Location:  cloud.Location{Region: cloud.Region(c.Region), Zone: zone},
			Score:     c.ZoneScores[zone],
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

// errAcquisition is the acquisition failure used where the message does not matter.
var errAcquisition = errors.New("acquisition failed")
