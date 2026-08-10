package cloud

import "time"

// Currency names the currency an amount is denominated in. It is recorded
// explicitly so a future non-USD feed cannot be compared as if it were USD.
type Currency string

// CurrencyUSD is the only currency spotinfo publishes today.
const CurrencyUSD Currency = "USD"

// BillingUnit names what an amount is charged per.
type BillingUnit string

// BillingUnitInstanceHour is one machine running for one hour.
const BillingUnitInstanceHour BillingUnit = "instance-hour"

// PriceClass distinguishes the purchase models a price belongs to.
type PriceClass string

const (
	// PriceClassSpot is interruptible spare-capacity pricing.
	PriceClassSpot PriceClass = "spot"
	// PriceClassOnDemand is uninterruptible list pricing.
	PriceClassOnDemand PriceClass = "on-demand"
)

// Location identifies where an observation applies. Zone is empty for a
// regional observation; providers without zone detail never set it.
type Location struct {
	Region Region
	Zone   string
}

// SourceRef records the provenance of a parsed snapshot: which document was
// read, when, and with which parser. Nothing synthesises these fields — an
// unknown provenance stays empty rather than being invented.
//
// Every provider fills these from its sidecar manifest, and sourceDTOs refuses
// to publish a v2 answer whose Result.Sources is empty or partial.
type SourceRef struct {
	FetchedAt     time.Time
	ObservedAt    *time.Time
	URL           string
	ContentSHA256 string
	ParserVersion string
	SchemaVersion string
}

// PriceObservation is one price a provider published for one machine, class,
// and location. An unknown price is represented by the absence of an
// observation, never by a zero amount.
type PriceObservation struct {
	Location Location
	Class    PriceClass
	Currency Currency
	Unit     BillingUnit
	Amount   Money
	// Live marks a price fetched from a provider API rather than read from the
	// embedded snapshot.
	Live bool
}

// RiskStatus reports whether interruption risk is known for a candidate.
type RiskStatus string

const (
	// RiskStatusAvailable means the provider published a risk figure.
	RiskStatusAvailable RiskStatus = "available"
	// RiskStatusUnavailable means no risk data exists for this candidate. It is
	// never rendered as a zero risk.
	RiskStatusUnavailable RiskStatus = "unavailable"
)

// RiskKind names the measurement a risk figure represents. Kinds from different
// providers are not comparable and must not be ranked against each other.
type RiskKind string

// RiskKindInterruptionFrequencyRange is the AWS Spot Advisor bucketed
// interruption frequency, expressed as a percentage range over a trailing month.
const RiskKindInterruptionFrequencyRange RiskKind = "interruption_frequency_range"

// RiskKindPreemptionRate is the GCP Spot preemption rate, from
// compute advice.capacityHistory.
//
// It is a separate kind from the AWS bucket, and deliberately so: Google
// defines it as (total preempted Spots) / (total Spots that stopped running),
// while the AWS figure is the fraction of *running* instances interrupted over
// a trailing month. GCP's denominator counts instances their owner shut down
// normally, so the same percentage means something different on each cloud and
// the two must never be ranked against each other.
//
// That is also why it is absent from interruptionCappableKinds: the web, ci and
// batch ceilings are AWS Advisor bucket boundaries, and applying them to this
// measurement would filter one cloud's numbers against another cloud's scale.
const RiskKindPreemptionRate RiskKind = "preemption_rate"

// HistoryWindow is the trailing observation window a risk figure covers.
type HistoryWindow struct {
	Days int
}

// RiskObservation carries a provider risk figure together with the semantics
// needed to read it: what it measures, over which window, from which source.
type RiskObservation struct {
	ObservedAt *time.Time
	MinPercent *float64
	MaxPercent *float64
	Window     *HistoryWindow
	Status     RiskStatus
	Kind       RiskKind
	Label      string
	SourceURL  string
}

// UnavailableRisk is the explicit "no risk data" value.
func UnavailableRisk() RiskObservation {
	return RiskObservation{Status: RiskStatusUnavailable}
}

// PlacementObservation is a provider capacity-placement score for one location,
// fetched from a live API rather than read from a snapshot.
type PlacementObservation struct {
	FetchedAt *time.Time
	Location  Location
	Score     int
}
