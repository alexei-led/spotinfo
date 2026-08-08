package cloud

import (
	"context"
	"slices"
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
