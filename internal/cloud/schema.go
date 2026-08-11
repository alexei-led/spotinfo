package cloud

import (
	"fmt"
	"time"
)

// Published schema versions. They are the contract every spotinfo surface
// serialises; the normative JSON Schemas live in docs/plans/contracts/.
const (
	// SchemaVersionRecommendV3 is the recommendation payload. It answers every
	// cloud under every workload: the AWS-only spotinfo.recommend/v1 document,
	// and the branch that picked between the two by workload, are retired.
	SchemaVersionRecommendV3 = "spotinfo.recommend/v3"
	// SchemaVersionListV1 is the browse payload. It shares the candidate, risk,
	// price and source blocks with v3 and drops the ranking: a browse answer
	// states what is there, not what to pick.
	SchemaVersionListV1 = "spotinfo.list/v1"
	// SchemaVersionRegionsV1 is the region-enumeration payload. It carries the
	// *complete* source list of one cloud, which is what makes the
	// sources_omitted count of a trimmed list or recommend answer resolvable.
	SchemaVersionRegionsV1 = "spotinfo.regions/v1"
	// SchemaVersionErrorV1 is the shared error payload.
	SchemaVersionErrorV1 = "spotinfo.error/v1"

	// statusOK is the only status a successful answer reports.
	statusOK = "ok"

	// maxSavingsPercent is the ceiling the published schema puts on a savings
	// figure. Anything above it is not a percentage of an on-demand price.
	maxSavingsPercent = 100
)

// riskKindWireNames maps a domain risk kind onto the published enum value. The
// table is deliberate rather than a rename of the domain constants: the wire
// enum is frozen by recommend-v3-success.schema.json, so a risk kind added for
// a new provider must be given a reviewed wire name here instead of leaking a
// Go constant into a published payload.
var riskKindWireNames = map[RiskKind]string{
	RiskKindInterruptionFrequencyRange: riskKindWireInterruptionBucket,
	RiskKindPreemptionRate:             riskKindWirePreemptionRate,
}

const (
	// riskKindWireInterruptionBucket is the published name of a bucketed
	// interruption-frequency range.
	riskKindWireInterruptionBucket = "interruption_bucket"
	// riskKindWirePreemptionRate is the published name of a GCP preemption
	// rate. Both names were already in the frozen enum, which is why adding
	// this provider needs no contract change — only a reviewed mapping.
	riskKindWirePreemptionRate = "preemption_rate"
)

// The published payload structs below declare their fields in schema order:
// encoding/json emits them in declaration order, so a reader can diff a payload
// against the JSON Schema without reordering it first. That costs a few bytes
// of struct padding, which is why fieldalignment is silenced where it applies.

// RiskDTO is the published risk block. Every field is present; an absent
// measurement is null, never zero.
type RiskDTO struct { //nolint:govet // field order follows the published schema
	Status     RiskStatus `json:"status"`
	Kind       *string    `json:"kind"`
	Label      *string    `json:"label"`
	MinPercent *float64   `json:"min_percent"`
	MaxPercent *float64   `json:"max_percent"`
	WindowDays *int       `json:"window_days"`
	SourceURL  *string    `json:"source_url"`
	ObservedAt *string    `json:"observed_at"`
}

// SourceDTO is the published provenance of one snapshot.
//
// ContentSHA256 is required by the schema but nullable, and both halves are
// deliberate: the key is always present so a consumer can rely on its position,
// and it is null for a source whose content this repository does not hash. The
// AWS architecture snapshot is one — its source is a documentation page, not a
// fetched payload — so null is a real value on the AWS path, not a theoretical
// one. A consumer that verifies provenance must test the value, not the key.
type SourceDTO struct { //nolint:govet // field order follows the published schema
	URL           string  `json:"url"`
	FetchedAt     string  `json:"fetched_at"`
	ObservedAt    *string `json:"observed_at"`
	ContentSHA256 *string `json:"content_sha256"`
	ParserVersion string  `json:"parser_version"`
	SchemaVersion string  `json:"schema_version"`
}

// DataSourceDTO describes where a result came from.
//
// SourcesOmitted counts the documents the snapshot was read from that no
// published candidate draws a value from. Azure reads 81 of them, so a
// three-row answer that listed all of them would be mostly provenance for rows
// it does not contain; the count keeps the omission visible and resolvable
// rather than silent.
type DataSourceDTO struct {
	Provider       ProviderID  `json:"provider"`
	Mode           DataMode    `json:"mode"`
	Sources        []SourceDTO `json:"sources"`
	SourcesOmitted int         `json:"sources_omitted"`
}

// RequestDTO echoes the recommendation request a report answers, with every
// default resolved.
type RequestDTO struct { //nolint:govet // field order follows the published schema
	Cloud        ProviderID      `json:"cloud"`
	Regions      []Region        `json:"regions"`
	Machine      string          `json:"machine"`
	Architecture Architecture    `json:"architecture"`
	OS           OperatingSystem `json:"os"`
	MinVCPU      int             `json:"min_vcpu"`
	MinMemoryGiB float64         `json:"min_memory_gib"`
	MaxPrice     *float64        `json:"max_price"`
	Workload     Workload        `json:"workload"`
	Top          int             `json:"top"`
}

// CandidateDTO is one priced machine. Prices are canonical decimal strings so a
// consumer never has to reconstruct them from a float. Both published schemas
// carry this block unchanged, which is what makes an answer from `list` and an
// answer from `recommend` comparable field for field.
//
// SpotUSDPerHour is nullable for the same reason OnDemandUSDPerHour is: an
// unknown price is the absence of an observation, never a zero and never an
// empty string. AWS's static price feed omits some families and every me-*
// region, so a browse answer really does carry rows nobody published a spot
// price for. `recommend` never publishes one — accepts() drops a candidate with
// no price before ranking — which is why recommend-v3-success.schema.json keeps
// the field non-nullable while list-v1.schema.json admits null.
//
// SavingsPercent is independent of both amounts on AWS, and stays published
// when they are absent. It is the Spot Advisor's own figure, read from a feed
// that is not the price feed; AWS publishes no on-demand price at all, so
// *every* AWS row already carries a discount without its denominator. Azure
// refuses a savings figure it would have to compute itself (catalog.go), which
// is a different shape: that one would be a number no consumer could check.
type CandidateDTO struct { //nolint:govet // field order follows the published schema
	Cloud              ProviderID      `json:"cloud"`
	Region             Region          `json:"region"`
	Machine            MachineID       `json:"machine"`
	Architecture       Architecture    `json:"architecture"`
	OS                 OperatingSystem `json:"os"`
	VCPU               int             `json:"vcpu"`
	MemoryGiB          float64         `json:"memory_gib"`
	SpotUSDPerHour     *string         `json:"spot_usd_per_hour"`
	OnDemandUSDPerHour *string         `json:"on_demand_usd_per_hour"`
	SavingsPercent     *float64        `json:"savings_percent"`
	Risk               RiskDTO         `json:"risk"`
}

// RecommendationDTO is one ranked machine: the shared candidate block, its
// position in the published ranking policy, and why it was kept.
type RecommendationDTO struct { //nolint:govet // field order follows the published schema
	Rank int `json:"rank"`
	CandidateDTO
	RationaleCodes []string `json:"rationale_codes"`
	PlacementDTO
}

// PlacementDTO publishes the capacity-placement figures a request asked for.
// Both schemas carry it, so a placement answer reads the same whether `list`
// or `recommend` produced it.
//
// The kinds are published under their own names and are never normalised onto
// one scale. An AWS placement score is an integer 1-10 whose bucket boundaries
// AWS does not state; a GCP obtainability is a probability in 0.0-1.0. One
// shared number would invent precision no vendor published, so a consumer reads
// whichever pair of fields is present and knows which measurement it has.
//
// PlacementStatus is published only when it is not already evident from the
// values, which leaves three wire states, one per domain status:
//
//   - a figure is present ....................... PlacementStatusAvailable
//   - "placement_status": "unavailable" ......... asked for, and none produced
//   - neither ................................... PlacementStatusNotRequested
//
// It does not restate Capabilities.PlacementScore. A cloud that publishes no
// placement figure at all is refused before acquisition, so the reachable
// meaning of "unavailable" is a lookup that produced nothing for this
// candidate: no credentials, a failed or timed-out call, or a region the
// provider scored none of.
//
// The first three fields are the AWS shape, and their names and order are the
// published spotinfo.list/v1 contract. A new kind is added after them.
// ScoreFetchedAt is shared: it is when the placement lookup ran, whichever kind
// it returned.
type PlacementDTO struct { //nolint:govet // field order follows the published schema
	RegionScore         *int               `json:"region_score,omitempty"`
	ZoneScores          map[string]int     `json:"zone_scores,omitempty"`
	ScoreFetchedAt      *string            `json:"score_fetched_at,omitempty"`
	RegionObtainability *float64           `json:"region_obtainability,omitempty"`
	ZoneObtainability   map[string]float64 `json:"zone_obtainability,omitempty"`
	// RegionEstimatedUptimeSeconds is the uptime estimate Google publishes beside
	// a regional obtainability. It is seconds rather than a duration string so a
	// consumer can compare it without parsing, and it is named for its unit the
	// way window_days and memory_gib are. Absent for every other kind: no other
	// vendor publishes it.
	RegionEstimatedUptimeSeconds *float64        `json:"region_estimated_uptime_seconds,omitempty"`
	PlacementStatus              PlacementStatus `json:"placement_status,omitempty"`
}

// ListCandidateDTO is one browsable machine: the shared candidate block plus
// the observations only `list` asks for.
//
// LivePrice is always emitted, where the four below it are omitted when unset.
// The split mirrors the other output formats: price provenance is carried by
// every one of them — a "*" suffix in text and table, a "Price Source" column
// in CSV — so a JSON form that dropped the key would be the only rendering
// where "this price came from the snapshot" and "this build does not report
// provenance" look identical. The others are genuinely absent unless the
// matching flag asked for them.
type ListCandidateDTO struct { //nolint:govet // field order follows the published schema
	CandidateDTO
	LivePrice  bool              `json:"live_price"`
	ZonePrices map[string]string `json:"zone_prices,omitempty"`
	PlacementDTO
}

// ListRequestDTO echoes the browse request an answer was built from. Every
// filter `list` accepts is present, so a consumer can tell an unfiltered answer
// from a filtered one without re-reading the command line.
type ListRequestDTO struct { //nolint:govet // field order follows the published schema
	Cloud        ProviderID      `json:"cloud"`
	Regions      []Region        `json:"regions"`
	Machine      string          `json:"machine"`
	Architecture Architecture    `json:"architecture"`
	OS           OperatingSystem `json:"os"`
	MinVCPU      int             `json:"min_vcpu"`
	MinMemoryGiB float64         `json:"min_memory_gib"`
	MaxPrice     *float64        `json:"max_price"`
	Sort         SortKey         `json:"sort"`
	Order        string          `json:"order"`
}

// RecommendReport is the spotinfo.recommend/v3 success payload.
type RecommendReport struct { //nolint:govet // field order follows the published schema
	SchemaVersion   string              `json:"schema_version"`
	Status          string              `json:"status"`
	Request         RequestDTO          `json:"request"`
	RankingPolicy   []string            `json:"ranking_policy"`
	DataSource      DataSourceDTO       `json:"data_source"`
	Recommendations []RecommendationDTO `json:"recommendations"`
	Warnings        []string            `json:"warnings"`
}

// ListReport is the spotinfo.list/v1 success payload. It publishes no ranking
// policy: nothing here is ranked, and an empty candidate list is a real answer
// rather than a failure.
type ListReport struct { //nolint:govet // field order follows the published schema
	SchemaVersion string             `json:"schema_version"`
	Status        string             `json:"status"`
	Request       ListRequestDTO     `json:"request"`
	DataSource    DataSourceDTO      `json:"data_source"`
	Candidates    []ListCandidateDTO `json:"candidates"`
	Warnings      []string           `json:"warnings"`
}

// RegionsReport is the spotinfo.regions/v1 payload: every region one cloud
// publishes, with the complete list of documents that cloud was read from.
//
// It publishes no candidates, so nothing can be trimmed against a row set and
// sources_omitted is always zero. That is the point: a list or recommend answer
// scopes its provenance to the rows it carries and counts what it left out, and
// this is where the omitted entries are recovered from.
type RegionsReport struct { //nolint:govet // field order follows the published schema
	SchemaVersion string        `json:"schema_version"`
	Status        string        `json:"status"`
	Cloud         ProviderID    `json:"cloud"`
	Regions       []Region      `json:"regions"`
	DataSource    DataSourceDTO `json:"data_source"`
}

// NewRegionsReport assembles the region payload from the regions a provider
// published and the result they were derived from.
func NewRegionsReport(result *Result, regions []Region) (*RegionsReport, error) {
	// No published candidates, so sourceDTOs trims nothing: this answer
	// describes a cloud, not a set of rows.
	sources, omitted, err := sourceDTOs(result, nil)
	if err != nil {
		return nil, err
	}

	if regions == nil {
		regions = []Region{}
	}

	return &RegionsReport{
		SchemaVersion: SchemaVersionRegionsV1,
		Status:        statusOK,
		Cloud:         result.Provider,
		Regions:       regions,
		DataSource: DataSourceDTO{
			Provider:       result.Provider,
			Mode:           result.Mode,
			Sources:        sources,
			SourcesOmitted: omitted,
		},
	}, nil
}

// ErrorReport is the spotinfo.error/v1 payload. Cloud is null when the request
// named no parsable provider.
type ErrorReport struct { //nolint:govet // field order follows the published schema
	SchemaVersion string    `json:"schema_version"`
	Code          ErrorCode `json:"code"`
	Message       string    `json:"message"`
	Cloud         *string   `json:"cloud"`
}

// NewErrorReport builds an error payload. An empty cloud serialises as null;
// the message is the caller's stable summary and must not carry provider or
// source internals.
func NewErrorReport(code ErrorCode, message, cloudValue string) *ErrorReport {
	report := &ErrorReport{SchemaVersion: SchemaVersionErrorV1, Code: code, Message: message}
	if cloudValue != "" {
		report.Cloud = &cloudValue
	}

	return report
}

// OrderAsc and OrderDesc are the two sort directions. They are the words a
// caller writes, the values the published request echo reports, and the enum
// both surfaces advertise — one spelling, decided here.
const (
	OrderAsc  = "asc"
	OrderDesc = "desc"
)

// NewListReport assembles the spotinfo.list/v1 payload from the query that was
// asked and the result it was answered from.
func NewListReport(query *Query, result *Result) (*ListReport, error) {
	if query == nil {
		return nil, fmt.Errorf("%w: query is required", ErrInvalidArgument)
	}

	candidates := make([]ListCandidateDTO, 0, len(result.Candidates))
	published := make([]*Candidate, 0, len(result.Candidates))

	for i := range result.Candidates {
		candidate := &result.Candidates[i]

		shared, err := candidateDTO(candidate)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, listCandidateDTO(&shared, candidate))
		published = append(published, candidate)
	}

	sources, omitted, err := sourceDTOs(result, published)
	if err != nil {
		return nil, err
	}

	return &ListReport{
		SchemaVersion: SchemaVersionListV1,
		Status:        statusOK,
		Request:       listRequestDTO(query, result.Provider),
		DataSource: DataSourceDTO{
			Provider:       result.Provider,
			Mode:           result.Mode,
			Sources:        sources,
			SourcesOmitted: omitted,
		},
		Candidates: candidates,
		Warnings:   []string{},
	}, nil
}

func listRequestDTO(query *Query, provider ProviderID) ListRequestDTO {
	order := OrderAsc
	if query.Sort.Descending {
		order = OrderDesc
	}

	echoed := ListRequestDTO{
		Cloud:        provider,
		Regions:      query.Regions,
		Machine:      query.MachinePattern,
		Architecture: query.Architecture,
		OS:           query.OS,
		MinVCPU:      query.MinVCPU,
		MinMemoryGiB: query.MinMemoryGiB,
		Sort:         query.Sort.Key,
		Order:        order,
	}
	if echoed.Regions == nil {
		echoed.Regions = []Region{}
	}
	if query.MaxPrice != nil {
		ceiling := query.MaxPrice.Float64()
		echoed.MaxPrice = &ceiling
	}

	return echoed
}

// listCandidateDTO adds the observations `list` renders on top of the shared
// candidate block. Each is absent unless the flag that fetches it was given.
func listCandidateDTO(shared *CandidateDTO, candidate *Candidate) ListCandidateDTO {
	published := ListCandidateDTO{CandidateDTO: *shared}
	if candidate.Spot != nil {
		published.LivePrice = candidate.Spot.Live
	}

	if len(candidate.ZonePrices) > 0 {
		published.ZonePrices = make(map[string]string, len(candidate.ZonePrices))
		for i := range candidate.ZonePrices {
			published.ZonePrices[candidate.ZonePrices[i].Location.Zone] = candidate.ZonePrices[i].Amount.String()
		}
	}

	published.PlacementDTO = placementDTO(candidate)

	return published
}

// placementDTO publishes the placement figures a candidate carries, each under
// the name of its own kind.
//
// An observation whose kind this package does not recognise publishes nothing.
// Reading it as a score would put a figure from an unknown scale in the field a
// consumer reads as an AWS 1-10, which is the one mistake the kind exists to
// prevent — so a provider that adds a measurement has to name it here before it
// can be published at all.
func placementDTO(candidate *Candidate) PlacementDTO {
	published := PlacementDTO{PlacementStatus: candidate.PlacementStatus}

	// Available is evident from the values themselves; publishing it as well
	// would say the same thing twice. See the type comment for the mapping.
	if published.PlacementStatus == PlacementStatusAvailable {
		published.PlacementStatus = PlacementStatusNotRequested
	}

	for i := range candidate.Placements {
		placement := &candidate.Placements[i]
		zone := placement.Location.Zone

		switch placement.Kind {
		case PlacementKindPlacementScore:
			publishScore(&published, zone, placement.Score, len(candidate.Placements))
		case PlacementKindObtainability:
			if placement.Obtainability != nil {
				publishObtainability(&published, zone, *placement.Obtainability, len(candidate.Placements))
			}
			// Published only beside a regional obtainability, because that is the
			// only shape it is measured in: the estimate belongs to one advice
			// answer, and a zone-level answer would need its own field.
			if zone == "" && placement.EstimatedUptime != nil {
				seconds := placement.EstimatedUptime.Seconds()
				published.RegionEstimatedUptimeSeconds = &seconds
			}
		}
	}

	// Every observation on one candidate comes from the same call, so the first
	// timestamp present is the answer.
	for i := range candidate.Placements {
		if candidate.Placements[i].FetchedAt != nil {
			published.ScoreFetchedAt = timestamp(*candidate.Placements[i].FetchedAt)

			break
		}
	}

	return published
}

func publishScore(published *PlacementDTO, zone string, score, capacity int) {
	if zone == "" {
		published.RegionScore = &score

		return
	}
	if published.ZoneScores == nil {
		published.ZoneScores = make(map[string]int, capacity)
	}
	published.ZoneScores[zone] = score
}

func publishObtainability(published *PlacementDTO, zone string, value float64, capacity int) {
	if zone == "" {
		published.RegionObtainability = &value

		return
	}
	if published.ZoneObtainability == nil {
		published.ZoneObtainability = make(map[string]float64, capacity)
	}
	published.ZoneObtainability[zone] = value
}

// newRecommendReport assembles the published payload from a validated request,
// the provider result it was answered from, and the ranked candidates.
func newRecommendReport(request *RecommendRequest, result *Result, ranked []scored) (*RecommendReport, error) {
	recommendations := make([]RecommendationDTO, len(ranked))
	published := make([]*Candidate, len(ranked))

	for i := range ranked {
		recommendation, err := recommendationDTO(&ranked[i], request.Workload, i+1)
		if err != nil {
			return nil, err
		}
		recommendations[i] = recommendation
		published[i] = ranked[i].candidate
	}

	// Trimmed against the ranked page rather than against everything the
	// provider matched: a --region all Azure query matches 55 regions and
	// publishes three, and provenance for the other 52 describes no value in
	// this document.
	sources, omitted, err := sourceDTOs(result, published)
	if err != nil {
		return nil, err
	}

	return &RecommendReport{
		SchemaVersion: SchemaVersionRecommendV3,
		Status:        statusOK,
		Request:       requestDTO(request),
		RankingPolicy: RankingPolicy(),
		DataSource: DataSourceDTO{
			Provider:       result.Provider,
			Mode:           result.Mode,
			Sources:        sources,
			SourcesOmitted: omitted,
		},
		Recommendations: recommendations,
		Warnings:        []string{},
	}, nil
}

func requestDTO(request *RecommendRequest) RequestDTO {
	echoed := RequestDTO{
		Cloud:        request.Cloud,
		Machine:      request.Machine,
		Architecture: request.Architecture,
		OS:           request.OS,
		Workload:     request.Workload,
		Regions:      request.Regions,
		MinMemoryGiB: request.MinMemoryGiB,
		MinVCPU:      request.MinVCPU,
		Top:          request.Top,
	}
	if request.MaxPrice != nil {
		// Float64 is documented as lossy and not for rendering, and this is the
		// one place it is correct anyway: the published schema types max_price
		// as a number, so a canonical decimal string — what every amount in the
		// payload uses — would not validate.
		//
		// It is exact here. Money carries 9 fractional digits, and float64 holds
		// any such value below 2^53 nanos (about $9,007,199) without rounding,
		// which every instance-hour price is. The echo also reports the ceiling
		// actually applied rather than the float the caller sent, so a value
		// finer than the scale reads back truncated on purpose.
		ceiling := request.MaxPrice.Float64()
		echoed.MaxPrice = &ceiling
	}

	return echoed
}

func recommendationDTO(candidate *scored, workload Workload, rank int) (RecommendationDTO, error) {
	shared, err := candidateDTO(candidate.candidate)
	if err != nil {
		return RecommendationDTO{}, err
	}

	return RecommendationDTO{
		Rank:           rank,
		CandidateDTO:   shared,
		RationaleCodes: candidate.rationaleCodes(workload),
		PlacementDTO:   placementDTO(candidate.candidate),
	}, nil
}

// candidateDTO publishes the block both schemas share.
func candidateDTO(candidate *Candidate) (CandidateDTO, error) {
	risk, err := riskDTO(&candidate.Risk)
	if err != nil {
		return CandidateDTO{}, err
	}

	machine := candidate.Machine
	published := CandidateDTO{
		Cloud:        candidate.Provider,
		Region:       candidate.Location.Region,
		Machine:      machine.ID,
		Architecture: machine.Architecture,
		OS:           candidate.OS,
		VCPU:         machine.VCPU,
		MemoryGiB:    machine.MemoryGiB,
		Risk:         risk,
	}
	if candidate.Spot != nil {
		price := candidate.Spot.Amount.String()
		published.SpotUSDPerHour = &price
	}
	if onDemand := candidate.OnDemand; onDemand != nil {
		price := onDemand.Amount.String()
		published.OnDemandUSDPerHour = &price
	}
	if savings := candidate.SavingsPercent; savings != nil {
		// The published schema bounds savings to 0..100. A provider that maps a
		// figure outside it has a mapping bug, and publishing the number anyway
		// would hand the client a payload its own schema rejects.
		if *savings < 0 || *savings > maxSavingsPercent {
			return CandidateDTO{}, fmt.Errorf("savings %d%% for %s is not a percentage of on-demand",
				*savings, machine.ID)
		}
		percent := float64(*savings)
		published.SavingsPercent = &percent
	}

	return published, nil
}

// riskDTO publishes a risk observation. An unmapped kind fails closed: emitting
// a domain constant would publish a value outside the frozen enum.
//
// A status other than available publishes the status alone. This is the one
// place the payload could contradict the rule that a cloud's silence must never
// render as a number, so a stale percentage left on an unavailable observation
// is dropped here rather than shipped to a consumer that would rank it.
func riskDTO(risk *RiskObservation) (RiskDTO, error) {
	if risk.Status != RiskStatusAvailable {
		return RiskDTO{Status: risk.Status}, nil
	}

	published := RiskDTO{
		Status:     risk.Status,
		MinPercent: risk.MinPercent,
		MaxPercent: risk.MaxPercent,
	}
	if risk.Kind != "" {
		wire, mapped := riskKindWireNames[risk.Kind]
		if !mapped {
			// No neutral sentinel: an unmapped kind is a bug in this repository,
			// which CodeOf classifies as INTERNAL rather than as user error.
			return RiskDTO{}, fmt.Errorf("risk kind %q has no published name", risk.Kind)
		}
		published.Kind = &wire
	}
	if risk.Label != "" {
		published.Label = &risk.Label
	}
	if risk.SourceURL != "" {
		published.SourceURL = &risk.SourceURL
	}
	if risk.Window != nil {
		days := risk.Window.Days
		published.WindowDays = &days
	}
	if risk.ObservedAt != nil {
		published.ObservedAt = timestamp(*risk.ObservedAt)
	}

	return published, nil
}

// sourceDTOs publishes the provenance of the snapshots a published answer draws
// its values from, and counts the ones it does not.
//
// A provider that cannot say where its data came from cannot serve an answer,
// so an empty or incomplete source list fails rather than publishing a payload
// with invented provenance. Trimming is per scope and each provider derives the
// scope from its own URLs; an unrecognised URL carries no scope and is kept, so
// this can only ever drop provenance for a value no published candidate has.
func sourceDTOs(result *Result, published []*Candidate) ([]SourceDTO, int, error) {
	if len(result.Sources) == 0 {
		return nil, 0, fmt.Errorf("%w: provider %q reported no data sources", ErrDataUnavailable, result.Provider)
	}

	sources := make([]SourceDTO, 0, len(result.Sources))
	omitted := 0

	for i := range result.Sources {
		source := &result.Sources[i]
		if source.URL == "" || source.ParserVersion == "" ||
			source.SchemaVersion == "" || source.FetchedAt.IsZero() {
			return nil, 0, fmt.Errorf("%w: provider %q reported an incomplete data source",
				ErrDataUnavailable, result.Provider)
		}

		// An answer with no candidates has nothing to trim against: every source
		// stays, because "which of these describes a row" has no answer when
		// there are no rows.
		if len(published) > 0 && !coversAny(source.Scope, published) {
			omitted++

			continue
		}

		var contentSHA256 *string
		if source.ContentSHA256 != "" {
			value := source.ContentSHA256
			contentSHA256 = &value
		}
		entry := SourceDTO{
			URL:           source.URL,
			FetchedAt:     *timestamp(source.FetchedAt),
			ContentSHA256: contentSHA256,
			ParserVersion: source.ParserVersion,
			SchemaVersion: source.SchemaVersion,
		}
		if source.ObservedAt != nil {
			entry.ObservedAt = timestamp(*source.ObservedAt)
		}
		sources = append(sources, entry)
	}

	// Both published schemas declare data_source.sources with minItems 1. A
	// trim that empties the list has lost the provenance of every row it kept,
	// which is the one outcome this must never publish.
	if len(sources) == 0 {
		return nil, 0, fmt.Errorf("%w: provider %q published no source for the rows it answered with",
			ErrDataUnavailable, result.Provider)
	}

	return sources, omitted, nil
}

// coversAny is O(sources x candidates) and short-circuits on the first row a
// source backs. Measured ceiling: `spotinfo list --cloud azure --output json`
// over every region is 11,204 rows against 81 sources and is not distinguishable
// from the same query rendered as a table, which does no trimming at all; the
// worst shape found — 950 rows with 37 sources omitted — is the same. Upgrade
// trigger: a provider whose sources outnumber a browse answer's rows, where
// collecting the published regions and machine IDs into two sets first turns
// this into O(sources + candidates).
func coversAny(scope SourceScope, published []*Candidate) bool {
	for _, candidate := range published {
		if scope.Covers(candidate) {
			return true
		}
	}

	return false
}

// timestamp renders an instant in the one format the schemas accept.
func timestamp(instant time.Time) *string {
	formatted := instant.UTC().Format(time.RFC3339)

	return &formatted
}
