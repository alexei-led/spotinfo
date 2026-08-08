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
// Ceiling: providers leave Result.Sources empty until manifest-backed snapshots
// exist (#36); that task is what fills every field here from the sidecar manifest.
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
