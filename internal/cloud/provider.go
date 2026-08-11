package cloud

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Capability names one optional provider ability. The vocabulary is fixed: a
// consumer that needs something outside this list must extend it rather than
// infer support from a provider identifier.
type Capability string

const (
	// CapabilitySpotPrice is a spot price per instance-hour.
	CapabilitySpotPrice Capability = "spot_price"
	// CapabilityOnDemandPrice is an on-demand price per instance-hour.
	CapabilityOnDemandPrice Capability = "on_demand_price"
	// CapabilityMachineSpec is vCPU and memory for a machine type.
	CapabilityMachineSpec Capability = "machine_spec"
	// CapabilityRisk is a provider-specific interruption or eviction observation.
	CapabilityRisk Capability = "risk"
	// CapabilityPlacementScore is a provider capacity score for a location.
	CapabilityPlacementScore Capability = "placement_score"
	// CapabilityZoneDetail is per-zone detail within a region.
	CapabilityZoneDetail Capability = "zone_detail"
	// CapabilityLiveEnrichment is a live provider API lookup for missing values.
	CapabilityLiveEnrichment Capability = "live_enrichment"
)

// Capabilities declares what a provider can answer. Consumers check it before
// acquisition so an unsupported request fails immediately instead of returning
// silently empty or partially populated candidates.
type Capabilities struct {
	OperatingSystems []OperatingSystem
	Architectures    []Architecture
	SpotPrice        bool
	OnDemandPrice    bool
	MachineSpec      bool
	Risk             bool
	PlacementScore   bool
	ZoneDetail       bool
	LiveEnrichment   bool
}

// SupportsOS reports whether the provider publishes prices for an OS.
func (c Capabilities) SupportsOS(instanceOS OperatingSystem) bool {
	return slices.Contains(c.OperatingSystems, instanceOS)
}

// SupportsArchitecture reports whether the provider classifies machines of an
// architecture from reviewed data.
func (c Capabilities) SupportsArchitecture(architecture Architecture) bool {
	return slices.Contains(c.Architectures, architecture)
}

// Has reports whether the provider publishes one optional capability. An
// unrecognised capability is never supported.
func (c Capabilities) Has(capability Capability) bool {
	switch capability {
	case CapabilitySpotPrice:
		return c.SpotPrice
	case CapabilityOnDemandPrice:
		return c.OnDemandPrice
	case CapabilityMachineSpec:
		return c.MachineSpec
	case CapabilityRisk:
		return c.Risk
	case CapabilityPlacementScore:
		return c.PlacementScore
	case CapabilityZoneDetail:
		return c.ZoneDetail
	case CapabilityLiveEnrichment:
		return c.LiveEnrichment
	default:
		return false
	}
}

// CapabilityRequest is what a consumer needs before it acquires candidates.
// Zero-valued fields are inactive, matching Query: an empty OS or Architecture
// is not requested rather than requested-and-empty.
type CapabilityRequest struct {
	OS           OperatingSystem
	Architecture Architecture
	Needed       []Capability
}

// Require returns the first shortfall between a request and what the provider
// declares, wrapped in ErrUnsupportedCapability. Checks run in a fixed order —
// OS, architecture, then Needed as given — so the reported shortfall is
// deterministic. Callers run this before acquisition, so an unsupported request
// costs no I/O.
func (c Capabilities) Require(request CapabilityRequest) error {
	if request.OS != "" && !c.SupportsOS(request.OS) {
		return fmt.Errorf("%w: os %s", ErrUnsupportedCapability, request.OS)
	}
	if request.Architecture != "" && !c.SupportsArchitecture(request.Architecture) {
		return fmt.Errorf("%w: architecture %s", ErrUnsupportedCapability, request.Architecture)
	}
	for _, capability := range request.Needed {
		if !c.Has(capability) {
			return fmt.Errorf("%w: %s", ErrUnsupportedCapability, capability)
		}
	}

	return nil
}

// SortKey names the observation a result is ordered by. An empty key leaves the
// order to the provider.
type SortKey string

const (
	// SortByPrice orders by spot price.
	SortByPrice SortKey = "price"
	// SortBySavings orders by savings against on-demand.
	SortBySavings SortKey = "savings"
	// SortByRisk orders by the provider's own risk figure. Risk figures from
	// different providers are not comparable, so this only ever orders one
	// provider's own candidates.
	SortByRisk SortKey = "risk"
	// SortByMachine orders by machine identifier.
	SortByMachine SortKey = "machine"
	// SortByRegion orders by region identifier.
	SortByRegion SortKey = "region"
	// SortByPlacementScore orders by placement score.
	SortByPlacementScore SortKey = "placement_score"
)

// sortWordScore is the single exception to "the word is the field's own name":
// the field is placement_score, and a caller writes score.
const sortWordScore = "score"

// sortVocabulary maps the word a caller writes onto the neutral key it selects.
// It lives beside the keys rather than on either surface because `--sort` and
// the `sort` tool argument must accept the same words: a second table is how
// "score" comes to mean two things depending on which surface was asked. Every
// other word is spelled from its key rather than beside it, so the two cannot
// drift.
var sortVocabulary = map[string]SortKey{
	string(SortByMachine): SortByMachine,
	string(SortByPrice):   SortByPrice,
	string(SortByRegion):  SortByRegion,
	string(SortByRisk):    SortByRisk,
	string(SortBySavings): SortBySavings,
	sortWordScore:         SortByPlacementScore,
}

// SortKeyNames lists the sort vocabulary in stable order, for help text, the
// published enum and error messages.
func SortKeyNames() []string { return slices.Sorted(maps.Keys(sortVocabulary)) }

// ParseSortKey resolves one word of the sort vocabulary. An empty value is not
// an error: it leaves the order to the provider, which is the only honest
// default across clouds that publish different observations.
func ParseSortKey(value string) (SortKey, error) {
	if value == "" {
		return "", nil
	}
	if key, known := sortVocabulary[value]; known {
		return key, nil
	}

	return "", fmt.Errorf("%w: unknown sort %q, want one of %s",
		ErrInvalidArgument, value, strings.Join(SortKeyNames(), "|"))
}

// SortOrder asks a provider to return candidates in a given order. The zero
// value leaves ordering to the provider. Ordering is part of the query rather
// than a consumer-side step because a provider that already sorts its own data
// must not have that order silently re-derived from mapped observations.
type SortOrder struct {
	Key        SortKey
	Descending bool
}

// The placement bounds both surfaces advertise. They are declared here, in the
// neutral domain that owns PlacementRequest, so the CLI flag and the MCP
// argument cannot publish two different limits for the same request.
const (
	// MaxPlacementScore is the top of the placement-score scale a request may
	// filter on.
	MaxPlacementScore = 10
	// DefaultScoreTimeoutSeconds bounds a placement-score lookup when the caller
	// names no timeout.
	DefaultScoreTimeoutSeconds = 30
	// MaxScoreTimeoutSeconds is the largest lookup budget either surface accepts.
	MaxScoreTimeoutSeconds = 300
)

// PlacementRequest asks a provider for capacity placement scores. The zero
// value requests none; a provider that does not declare CapabilityPlacementScore
// must be rejected before the query is issued.
type PlacementRequest struct {
	// Timeout bounds the live lookup. Zero means the provider's own default.
	Timeout time.Duration
	// MinScore drops candidates scoring below it. Zero applies no floor.
	MinScore int
	// SingleZone requests per-zone scores instead of one regional score.
	SingleZone bool
	// Enabled requests scores at all.
	Enabled bool
}

// Query is a provider-neutral candidate request. Zero-valued filters are
// inactive: an empty Architecture matches any architecture, and a nil MaxPrice
// applies no ceiling.
type Query struct {
	MaxPrice       *Money
	MachinePattern string
	Architecture   Architecture
	OS             OperatingSystem
	Regions        []Region
	Sort           SortOrder
	Placement      PlacementRequest
	MinMemoryGiB   float64
	MinVCPU        int
}

// CapabilityNeeds is what this query requires of a provider. Spot prices and
// machine specifications are needed by every query; the rest is asked for only
// when a filter or ordering actually depends on it.
func (q *Query) CapabilityNeeds() CapabilityRequest {
	needed := []Capability{CapabilitySpotPrice, CapabilityMachineSpec}
	if q.Sort.Key == SortByRisk {
		needed = append(needed, CapabilityRisk)
	}
	if q.Placement.Enabled || q.Placement.MinScore > 0 || q.Sort.Key == SortByPlacementScore {
		needed = append(needed, CapabilityPlacementScore)
	}
	if q.Placement.SingleZone {
		needed = append(needed, CapabilityZoneDetail)
	}

	return CapabilityRequest{OS: q.OS, Architecture: q.Architecture, Needed: needed}
}

// FetchPolicy asks for data of a given freshness. It is a property of the
// acquisition client rather than of a Query: it decides which documents are
// read, not which candidates are selected. The zero value is the default —
// live data, with the committed snapshot as the fallback.
//
// A provider with no live path honours it by construction: there is nothing to
// skip and nothing to re-fetch.
type FetchPolicy struct {
	// Offline answers from the committed snapshots and makes no request at all.
	Offline bool
	// Refresh ignores any cached copy for this call.
	Refresh bool
}

// DataMode reports whether a result was built from a committed snapshot or from
// a live provider API.
type DataMode string

const (
	// DataModeEmbeddedSnapshot means the answer came from committed data.
	DataModeEmbeddedSnapshot DataMode = "embedded-snapshot"
	// DataModeLive means the answer came from a provider API this run, or the
	// origin confirmed a cached copy is still current.
	DataModeLive DataMode = "live"
	// DataModeCached means the answer came from a locally cached provider
	// document that was not revalidated this run. It is distinct from live
	// because the data is the provider's but its recency is bounded by a
	// time-to-live rather than confirmed.
	DataModeCached DataMode = "cached"
)

// Candidate is one machine, in one location, priced for one OS. Optional
// observations are absent rather than zero-valued when the provider does not
// publish them. SavingsPercent, when present, is a percentage of the on-demand
// price in the range 1..100; a provider that cannot produce one in that range
// leaves it absent rather than publishing a figure no consumer can read.
type Candidate struct {
	Spot           *PriceObservation
	OnDemand       *PriceObservation
	SavingsPercent *int
	Provider       ProviderID
	OS             OperatingSystem
	Risk           RiskObservation
	Location       Location
	ZonePrices     []PriceObservation
	Placements     []PlacementObservation
	Machine        MachineSpec
}

// Result is one provider's answer to a Query, with the provenance a consumer
// needs to describe how fresh the answer is. Candidates carry the order the
// query asked for; a consumer that needs a different order sorts them itself.
type Result struct {
	Provider   ProviderID
	Mode       DataMode
	Sources    []SourceRef
	Candidates []Candidate
}

// Provider acquires neutral candidates. It is the smallest interface a
// candidate consumer needs, and it is owned by the consumer side of the seam.
// Query is passed by pointer because it is large, not because it is mutable:
// an implementation must treat it as read-only.
type Provider interface {
	ID() ProviderID
	Capabilities() Capabilities
	Query(ctx context.Context, query *Query) (Result, error)
}

// RegionsOf lists every region a provider publishes candidates for, together
// with the result they were derived from so a caller can publish the
// provenance without acquiring twice.
//
// It is a helper and deliberately not a Regions() method on Provider: a query
// over every region already yields the answer, so a method would oblige all
// three providers and every test stub to implement what one query derives,
// for no capability the seam does not already have.
//
// The capability check runs here, before acquisition, so every caller gets it.
// The OS is Linux because every provider declares it — an empty OS would take
// an untested path through three different catalogues to answer a question
// that is not about an operating system at all.
func RegionsOf(ctx context.Context, provider Provider) ([]Region, Result, error) {
	query := &Query{
		OS:      OSLinux,
		Regions: []Region{RegionAll},
		Sort:    SortOrder{Key: SortByRegion},
	}

	if err := provider.Capabilities().Require(query.CapabilityNeeds()); err != nil {
		return nil, Result{}, err
	}

	result, err := provider.Query(ctx, query)
	if err != nil {
		return nil, Result{}, err
	}

	seen := make(map[Region]struct{}, len(result.Candidates))
	// Always allocated, so an empty answer publishes [] rather than null.
	regions := make([]Region, 0, len(result.Candidates))

	for i := range result.Candidates {
		region := result.Candidates[i].Location.Region
		if _, duplicate := seen[region]; duplicate {
			continue
		}
		seen[region] = struct{}{}
		regions = append(regions, region)
	}

	slices.Sort(regions)

	return regions, result, nil
}
