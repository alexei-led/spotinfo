package cloud

import (
	"context"
	"fmt"
	"slices"
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

// Query is a provider-neutral candidate request. Zero-valued filters are
// inactive: an empty Architecture matches any architecture, and a nil MaxPrice
// applies no ceiling.
type Query struct {
	MaxPrice       *Money
	MachinePattern string
	Architecture   Architecture
	OS             OperatingSystem
	Regions        []Region
	MinMemoryGiB   float64
	MinVCPU        int
}

// DataMode reports whether a result was built from a committed snapshot or from
// a live provider API.
type DataMode string

const (
	// DataModeEmbeddedSnapshot means the answer came from committed data.
	DataModeEmbeddedSnapshot DataMode = "embedded-snapshot"
	// DataModeLive means the answer came from a provider API.
	DataModeLive DataMode = "live"
)

// Candidate is one machine, in one location, priced for one OS. Optional
// observations are absent rather than zero-valued when the provider does not
// publish them.
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
// needs to describe how fresh the answer is.
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
