// Package aws adapts the legacy internal/spot AWS client to the provider-neutral
// cloud domain. Every AWS-specific mapping decision lives here so that
// internal/cloud stays free of AWS types and semantics.
//
// The adapter is additive: it neither replaces nor weakens spot.Advice,
// GetSpotSavings, placement scores, or live-price enrichment.
package aws

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"

	"spotinfo/internal/cloud"
	"spotinfo/internal/spot"
)

const (
	// spotAdvisorURL is the feed the interruption buckets are read from.
	spotAdvisorURL = "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json"
	// advisorWindowDays is the trailing window Spot Advisor computes its
	// interruption frequency over: the last month.
	advisorWindowDays = 30

	bitSize32 = 32
	bitSize64 = 64

	// maxSavingsPercent is the largest savings figure that can be a percentage
	// of an on-demand price. The feed publishes an unvalidated integer.
	maxSavingsPercent = 100
)

// savingsClient is the slice of the legacy AWS client this adapter consumes.
type savingsClient interface {
	GetSpotSavings(ctx context.Context, opts ...spot.GetSpotSavingsOption) ([]spot.Advice, error)
	DataSource() string
}

// architectureLookup resolves an instance type to its reviewed architecture.
// Unknown instance types stay unclassified; nothing infers an architecture from
// an instance name.
type architectureLookup interface {
	ArchitectureForInstance(instance string) (spot.Architecture, bool)
}

// Provider answers neutral queries from AWS Spot Advisor and pricing data.
type Provider struct {
	client        savingsClient
	architectures architectureLookup
	sources       []cloud.SourceRef
}

// New builds the AWS provider. Both collaborators are required: without the
// architecture lookup the provider would have to guess architectures, and
// guessing is what the reviewed snapshot exists to prevent.
//
// Provenance is read once, from the committed sidecar manifests. A build that
// cannot describe where its data came from fails here, so the registry disables
// AWS rather than serving answers with invented provenance.
func New(client savingsClient, architectures architectureLookup) (*Provider, error) {
	if client == nil {
		return nil, errors.New("aws provider requires a spot client")
	}
	if architectures == nil {
		return nil, errors.New("aws provider requires an architecture lookup")
	}

	sources, err := spot.EmbeddedSourceRefs()
	if err != nil {
		return nil, fmt.Errorf("aws snapshot provenance: %w", err)
	}

	return &Provider{client: client, architectures: architectures, sources: sources}, nil
}

// ID identifies this provider.
func (p *Provider) ID() cloud.ProviderID { return cloud.ProviderAWS }

// Capabilities reports what the AWS feeds can answer. OnDemandPrice is false:
// the feeds publish a savings percentage against on-demand, not the on-demand
// price itself, and deriving one from the other would invent a figure AWS never
// published.
func (p *Provider) Capabilities() cloud.Capabilities {
	return cloud.Capabilities{
		OperatingSystems: []cloud.OperatingSystem{cloud.OSLinux, cloud.OSWindows},
		Architectures:    []cloud.Architecture{cloud.ArchitectureX8664, cloud.ArchitectureARM64},
		SpotPrice:        true,
		OnDemandPrice:    false,
		MachineSpec:      true,
		Risk:             true,
		PlacementScore:   true,
		ZoneDetail:       true,
		LiveEnrichment:   true,
	}
}

// Query acquires AWS advice and maps it to neutral candidates. Capability
// checks run before acquisition so an unsupported request costs no I/O.
func (p *Provider) Query(ctx context.Context, query *cloud.Query) (cloud.Result, error) {
	if query == nil {
		return cloud.Result{}, fmt.Errorf("%w: query is required", cloud.ErrInvalidArgument)
	}

	capabilities := p.Capabilities()
	if !capabilities.SupportsOS(query.OS) {
		return cloud.Result{}, fmt.Errorf("%w: aws does not support os %q", cloud.ErrInvalidArgument, query.OS)
	}
	if query.Architecture != "" && !capabilities.SupportsArchitecture(query.Architecture) {
		return cloud.Result{}, fmt.Errorf("%w: aws does not support architecture %q", cloud.ErrInvalidArgument, query.Architecture)
	}

	advices, err := p.client.GetSpotSavings(ctx, legacyOptions(query)...)
	if err != nil {
		return cloud.Result{}, fmt.Errorf("aws candidate acquisition: %w", err)
	}

	candidates := make([]cloud.Candidate, 0, len(advices))
	for i := range advices {
		candidate, err := p.toCandidate(&advices[i], query.OS)
		if err != nil {
			return cloud.Result{}, err
		}
		if !accepts(&candidate, query) {
			continue
		}
		candidates = append(candidates, candidate)
	}

	return cloud.Result{
		Provider: cloud.ProviderAWS,
		Mode:     dataMode(p.client.DataSource()),
		// The committed manifests describe the snapshots this binary parses. A
		// live answer still reports them: they are the provenance of the data the
		// parser was written against, and the live feed publishes none of its own.
		Sources:    slices.Clone(p.sources),
		Candidates: candidates,
	}, nil
}

// legacyOptions translates the neutral query into legacy client options. The
// resource and price filters are only a coarse pre-filter here: the legacy
// client compares whole GiB and float prices, so accepts() re-applies both
// exactly against the mapped candidate.
func legacyOptions(query *cloud.Query) []spot.GetSpotSavingsOption {
	regions := make([]string, 0, len(query.Regions))
	for _, region := range query.Regions {
		regions = append(regions, string(region))
	}

	opts := []spot.GetSpotSavingsOption{
		spot.WithRegions(regions),
		spot.WithOS(string(query.OS)),
	}
	if query.MachinePattern != "" {
		opts = append(opts, spot.WithPattern(query.MachinePattern))
	}
	if query.MinVCPU > 0 {
		opts = append(opts, spot.WithCPU(query.MinVCPU))
	}
	if query.MinMemoryGiB > 0 {
		opts = append(opts, spot.WithMemory(int(math.Floor(query.MinMemoryGiB))))
	}
	if query.MaxPrice != nil {
		opts = append(opts, spot.WithMaxPrice(query.MaxPrice.Float64()))
	}
	opts = append(opts, spot.WithSort(legacySortBy(query.Sort.Key), query.Sort.Descending))

	return append(opts, legacyPlacementOptions(&query.Placement)...)
}

// legacySortBy maps a neutral sort key onto the legacy client's own ordering.
// An unset key keeps the legacy default, so a query that expresses no
// preference gets the order every existing AWS caller already sees.
func legacySortBy(key cloud.SortKey) spot.SortBy {
	switch key {
	case cloud.SortByPrice:
		return spot.SortByPrice
	case cloud.SortBySavings:
		return spot.SortBySavings
	case cloud.SortByMachine:
		return spot.SortByInstance
	case cloud.SortByRegion:
		return spot.SortByRegion
	case cloud.SortByPlacementScore:
		return spot.SortByScore
	case cloud.SortByRisk:
		return spot.SortByRange
	default:
		return spot.SortByRange
	}
}

// legacyPlacementOptions mirrors the legacy split exactly: score enrichment and
// its zone and timeout settings are requested together, while a minimum score
// filters whatever scores are present.
func legacyPlacementOptions(placement *cloud.PlacementRequest) []spot.GetSpotSavingsOption {
	var opts []spot.GetSpotSavingsOption

	if placement.Enabled {
		opts = append(opts, spot.WithScores(true), spot.WithSingleAvailabilityZone(placement.SingleZone))
		if placement.Timeout > 0 {
			opts = append(opts, spot.WithScoreTimeout(placement.Timeout))
		}
	}
	if placement.MinScore > 0 {
		opts = append(opts, spot.WithMinScore(placement.MinScore))
	}

	return opts
}

func (p *Provider) toCandidate(advice *spot.Advice, instanceOS cloud.OperatingSystem) (cloud.Candidate, error) {
	location := cloud.Location{Region: cloud.Region(advice.Region)}

	machine := cloud.MachineSpec{
		ID:        cloud.MachineID(advice.Instance),
		VCPU:      advice.Info.Cores,
		MemoryGiB: memoryGiB(advice.Info.RAM),
	}
	if architecture, known := p.architectures.ArchitectureForInstance(advice.Instance); known {
		machine.Architecture = cloud.Architecture(architecture)
	}

	spotPrice, err := priceObservation(advice.Price, location, advice.LivePrice)
	if err != nil {
		return cloud.Candidate{}, fmt.Errorf("aws spot price for %s in %s: %w", advice.Instance, advice.Region, err)
	}

	zonePrices, err := zonePriceObservations(advice)
	if err != nil {
		return cloud.Candidate{}, err
	}

	return cloud.Candidate{
		Provider:       cloud.ProviderAWS,
		Location:       location,
		Machine:        machine,
		OS:             instanceOS,
		Spot:           spotPrice,
		SavingsPercent: savingsPercent(advice.Savings),
		ZonePrices:     zonePrices,
		Placements:     placements(advice),
		Risk:           risk(advice.Range),
	}, nil
}

// accepts re-applies the neutral filters exactly. An unknown price cannot
// satisfy a ceiling: spot prices are never zero, so a missing price means
// "unknown", not "free".
func accepts(candidate *cloud.Candidate, query *cloud.Query) bool {
	if query.Architecture != "" && candidate.Machine.Architecture != query.Architecture {
		return false
	}
	if candidate.Machine.VCPU < query.MinVCPU || candidate.Machine.MemoryGiB < query.MinMemoryGiB {
		return false
	}
	if query.MaxPrice != nil {
		if candidate.Spot == nil || candidate.Spot.Amount.Nanos() > query.MaxPrice.Nanos() {
			return false
		}
	}

	return true
}

// priceObservation maps a legacy price. A non-positive, NaN, or infinite price
// means the feed has no price for this instance; the observation is absent
// rather than zero.
func priceObservation(price float64, location cloud.Location, live bool) (*cloud.PriceObservation, error) {
	if price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return nil, nil //nolint:nilnil // absence is the mapping for an unknown price
	}

	amount, err := cloud.MoneyFromFloat(price)
	if err != nil {
		return nil, err
	}

	return &cloud.PriceObservation{
		Location: location,
		Class:    cloud.PriceClassSpot,
		Currency: cloud.CurrencyUSD,
		Unit:     cloud.BillingUnitInstanceHour,
		Amount:   amount,
		Live:     live,
	}, nil
}

func zonePriceObservations(advice *spot.Advice) ([]cloud.PriceObservation, error) {
	if len(advice.ZonePrice) == 0 {
		return nil, nil
	}

	observations := make([]cloud.PriceObservation, 0, len(advice.ZonePrice))
	for _, zone := range slices.Sorted(maps.Keys(advice.ZonePrice)) {
		location := cloud.Location{Region: cloud.Region(advice.Region), Zone: zone}

		observation, err := priceObservation(advice.ZonePrice[zone], location, advice.LivePrice)
		if err != nil {
			return nil, fmt.Errorf("aws zone price for %s in %s: %w", advice.Instance, zone, err)
		}
		if observation == nil {
			continue
		}
		observations = append(observations, *observation)
	}

	return observations, nil
}

// placements maps regional and zonal placement scores, keeping the location
// each score was measured for. Zones are ordered so results are deterministic.
func placements(advice *spot.Advice) []cloud.PlacementObservation {
	observations := make([]cloud.PlacementObservation, 0, len(advice.ZoneScores)+1)
	if advice.RegionScore != nil {
		observations = append(observations, cloud.PlacementObservation{
			FetchedAt: advice.ScoreFetchedAt,
			Location:  cloud.Location{Region: cloud.Region(advice.Region)},
			Score:     *advice.RegionScore,
		})
	}
	for _, zone := range slices.Sorted(maps.Keys(advice.ZoneScores)) {
		observations = append(observations, cloud.PlacementObservation{
			FetchedAt: advice.ScoreFetchedAt,
			Location:  cloud.Location{Region: cloud.Region(advice.Region), Zone: zone},
			Score:     advice.ZoneScores[zone],
		})
	}
	if len(observations) == 0 {
		return nil
	}

	return observations
}

// risk maps the Spot Advisor interruption bucket, keeping its own semantics: a
// percentage range over a trailing month, not a score comparable with another
// provider's risk figure. An unlabelled range means the advisor published none.
func risk(advisorRange spot.Range) cloud.RiskObservation {
	if advisorRange.Label == "" {
		return cloud.UnavailableRisk()
	}

	minPercent := float64(advisorRange.Min)
	maxPercent := float64(advisorRange.Max)

	return cloud.RiskObservation{
		MinPercent: &minPercent,
		MaxPercent: &maxPercent,
		Window:     &cloud.HistoryWindow{Days: advisorWindowDays},
		Status:     cloud.RiskStatusAvailable,
		Kind:       cloud.RiskKindInterruptionFrequencyRange,
		Label:      advisorRange.Label,
		SourceURL:  spotAdvisorURL,
	}
}

// savingsPercent keeps a savings figure only when the feed published a usable
// one. Zero is the advisor's "no data" value, not a claim that spot costs list
// price, and a figure above 100 is not a percentage of on-demand at all — both
// are absences rather than measurements.
func savingsPercent(savings int) *int {
	if savings <= 0 || savings > maxSavingsPercent {
		return nil
	}

	return &savings
}

// memoryGiB widens the advisor's float32 RAM without inheriting its binary
// representation error: 1.7 held as a float32 widens to 1.7000000476837158.
// Shortest round-trip formatting recovers the decimal the feed published.
func memoryGiB(ram float32) float64 {
	widened, _ := strconv.ParseFloat(strconv.FormatFloat(float64(ram), 'f', -1, bitSize32), bitSize64)

	return widened
}

func dataMode(source string) cloud.DataMode {
	if source == spot.DataSourceAWS {
		return cloud.DataModeLive
	}

	return cloud.DataModeEmbeddedSnapshot
}
