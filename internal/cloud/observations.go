package cloud

import (
	"slices"
	"time"
)

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
	// Scope narrows which candidates this source is the provenance for. The
	// zero value covers all of them.
	Scope SourceScope
}

// SourceScope narrows the candidates one source is the provenance for, so a
// published answer can carry the provenance it actually used instead of every
// document the snapshot was built from. Azure reads 81: 55 region-scoped price
// queries and 26 machine-series pages, which is far more provenance than a
// three-row answer has values.
//
// It is derived by the provider that owns the source, from the source URL. No
// manifest declares it: ParseManifest rejects unknown fields, so a scope field
// in the manifest format would force every committed manifest to be rebuilt
// from the network before this code could compile.
//
// Each dimension is an independent filter and an empty dimension is
// unconstrained, so a region-scoped source covers every machine in that region
// and a size-page source covers its machines in every region. A URL whose shape
// the provider does not recognise therefore yields the zero scope and is
// retained: trimming can never drop the provenance of a value a candidate
// carries, even when a vendor changes a URL shape.
type SourceScope struct {
	Region   Region
	Machines []MachineID
}

// Covers reports whether this source is provenance for a candidate.
func (s SourceScope) Covers(candidate *Candidate) bool {
	if s.Region != "" && s.Region != candidate.Location.Region {
		return false
	}

	if len(s.Machines) > 0 && !slices.Contains(s.Machines, candidate.Machine.ID) {
		return false
	}

	return true
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

// PlacementKind names the measurement a placement figure represents.
//
// Kinds from different providers are not comparable and are never normalised
// into a shared scale. A common 1-10 would invent precision no vendor
// published: AWS ranks capacity into ten buckets whose boundaries it does not
// state, and Google publishes a probability. Three incomparable vendor
// measurements stay distinguishable, which is the whole point of naming the
// kind.
//
// One kind per producer. Azure's High/Medium/Low placement label is deferred
// with its fetcher — reading it needs an Azure subscription this build does not
// authenticate to — and it deliberately has no kind here, because a kind with
// no producer is a value nothing can ever set.
type PlacementKind string

const (
	// PlacementKindPlacementScore is the AWS Spot placement score: an integer
	// 1-10 rating how likely a request for that capacity is to be fulfilled.
	PlacementKindPlacementScore PlacementKind = "placement_score"
	// PlacementKindObtainability is the GCP obtainability figure from compute
	// advice.capacity: a probability in 0.0-1.0. It is not a decile of the AWS
	// score and must never be filtered or ranked as if it were.
	PlacementKindObtainability PlacementKind = "obtainability"
)

// PlacementStatus reports whether placement figures were asked for and whether
// the provider produced any. It exists because Placements is a slice, so
// "nobody asked" and "asked, and this cloud produced nothing" would otherwise
// both be the empty slice — and a caller has to be able to tell them apart.
//
// It does not restate Capabilities.PlacementScore. A cloud that publishes no
// placement figure at all is refused before acquisition, so the reachable
// meaning of PlacementStatusUnavailable is that the lookup ran and produced
// nothing for this candidate — one region of many failed, or the provider
// scored none of that machine.
type PlacementStatus string

const (
	// PlacementStatusNotRequested is the zero value: no placement lookup was
	// asked for, so the absence of observations says nothing about capacity.
	PlacementStatusNotRequested PlacementStatus = ""
	// PlacementStatusAvailable means the candidate carries at least one
	// observation.
	PlacementStatusAvailable PlacementStatus = "available"
	// PlacementStatusUnavailable means a lookup was asked for and produced no
	// figure for this candidate. It is never rendered as a zero or a low score.
	PlacementStatusUnavailable PlacementStatus = "unavailable"
)

// PlacementObservation is a provider capacity-placement figure for one
// location, fetched from a live API rather than read from a snapshot.
//
// Kind says which of the value fields carries the measurement: Score for
// PlacementKindPlacementScore, Obtainability for PlacementKindObtainability. A
// consumer switches on the kind rather than reading whichever field is
// non-zero, so an observation whose kind nothing recognises is published by
// nobody instead of being read on a scale it does not use.
type PlacementObservation struct {
	FetchedAt *time.Time
	// Obtainability is the PlacementKindObtainability value, 0.0-1.0. It is a
	// pointer because 0.0 is a real obtainability, not an absent one.
	Obtainability *float64
	// EstimatedUptime accompanies an obtainability: the minimum time most of the
	// requested Spot machines are expected to run before preemption. Google
	// publishes it beside the probability, and the two answer different halves of
	// "is this machine worth asking for" — how likely the request is to succeed,
	// and how long it is likely to last. Absent unless the provider published it.
	EstimatedUptime *time.Duration
	Location        Location
	Kind            PlacementKind
	// Score is the PlacementKindPlacementScore value, an integer 1-10.
	Score int
}
