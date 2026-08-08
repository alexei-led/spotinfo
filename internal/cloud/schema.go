package cloud

import (
	"fmt"
	"time"
)

// Published schema versions. They are the contract every spotinfo surface
// serialises; the normative JSON Schemas live in docs/plans/contracts/.
const (
	// SchemaVersionRecommendV2 is the provider-neutral recommendation payload.
	SchemaVersionRecommendV2 = "spotinfo.recommend/v2"
	// SchemaVersionErrorV1 is the shared error payload.
	SchemaVersionErrorV1 = "spotinfo.error/v1"

	// statusOK is the only status a successful recommendation reports.
	statusOK = "ok"

	// maxSavingsPercent is the ceiling the published schema puts on a savings
	// figure. Anything above it is not a percentage of an on-demand price.
	maxSavingsPercent = 100
)

// riskKindWireNames maps a domain risk kind onto the published enum value. The
// table is deliberate rather than a rename of the domain constants: the wire
// enum is frozen by recommend-spot-instances-v2-success.schema.json, so a risk
// kind added for a new provider must be given a reviewed wire name here instead
// of leaking a Go constant into a published payload.
var riskKindWireNames = map[RiskKind]string{
	RiskKindInterruptionFrequencyRange: riskKindWireInterruptionBucket,
}

// riskKindWireInterruptionBucket is the published name of a bucketed
// interruption-frequency range.
const riskKindWireInterruptionBucket = "interruption_bucket"

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
type SourceDTO struct { //nolint:govet // field order follows the published schema
	URL           string  `json:"url"`
	FetchedAt     string  `json:"fetched_at"`
	ObservedAt    *string `json:"observed_at"`
	ContentSHA256 string  `json:"content_sha256"`
	ParserVersion string  `json:"parser_version"`
	SchemaVersion string  `json:"schema_version"`
}

// DataSourceDTO describes where a result came from.
type DataSourceDTO struct {
	Provider ProviderID  `json:"provider"`
	Mode     DataMode    `json:"mode"`
	Sources  []SourceDTO `json:"sources"`
}

// RequestDTO echoes the request a report answers, with every default resolved.
type RequestDTO struct { //nolint:govet // field order follows the published schema
	Cloud           ProviderID      `json:"cloud"`
	Regions         []Region        `json:"regions"`
	Machine         string          `json:"machine"`
	Architecture    Architecture    `json:"architecture"`
	OS              OperatingSystem `json:"os"`
	MinVCPU         int             `json:"min_vcpu"`
	MinMemoryGiB    float64         `json:"min_memory_gib"`
	MaxPricePerHour *float64        `json:"max_price_per_hour"`
	Workload        Workload        `json:"workload"`
	Top             int             `json:"top"`
}

// RecommendationDTO is one ranked machine. Prices are canonical decimal strings
// so a consumer never has to reconstruct them from a float.
type RecommendationDTO struct { //nolint:govet // field order follows the published schema
	Rank               int             `json:"rank"`
	Cloud              ProviderID      `json:"cloud"`
	Region             Region          `json:"region"`
	Machine            MachineID       `json:"machine"`
	Architecture       Architecture    `json:"architecture"`
	OS                 OperatingSystem `json:"os"`
	VCPU               int             `json:"vcpu"`
	MemoryGiB          float64         `json:"memory_gib"`
	SpotUSDPerHour     string          `json:"spot_usd_per_hour"`
	OnDemandUSDPerHour *string         `json:"on_demand_usd_per_hour"`
	SavingsPercent     *float64        `json:"savings_percent"`
	Risk               RiskDTO         `json:"risk"`
	RationaleCodes     []string        `json:"rationale_codes"`
}

// RecommendReport is the spotinfo.recommend/v2 success payload.
type RecommendReport struct { //nolint:govet // field order follows the published schema
	SchemaVersion   string              `json:"schema_version"`
	Status          string              `json:"status"`
	Request         RequestDTO          `json:"request"`
	RankingPolicy   []string            `json:"ranking_policy"`
	DataSource      DataSourceDTO       `json:"data_source"`
	Recommendations []RecommendationDTO `json:"recommendations"`
	Warnings        []string            `json:"warnings"`
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

// newRecommendReport assembles the published payload from a validated request,
// the provider result it was answered from, and the ranked candidates.
func newRecommendReport(request *RecommendRequest, result *Result, ranked []scored) (*RecommendReport, error) {
	sources, err := sourceDTOs(result)
	if err != nil {
		return nil, err
	}

	recommendations := make([]RecommendationDTO, len(ranked))
	for i := range ranked {
		recommendation, err := recommendationDTO(&ranked[i], request.Workload, i+1)
		if err != nil {
			return nil, err
		}
		recommendations[i] = recommendation
	}

	return &RecommendReport{
		SchemaVersion: SchemaVersionRecommendV2,
		Status:        statusOK,
		Request:       requestDTO(request),
		RankingPolicy: RankingPolicy(),
		DataSource: DataSourceDTO{
			Provider: result.Provider,
			Mode:     result.Mode,
			Sources:  sources,
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
		ceiling := request.MaxPrice.Float64()
		echoed.MaxPricePerHour = &ceiling
	}

	return echoed
}

func recommendationDTO(candidate *scored, workload Workload, rank int) (RecommendationDTO, error) {
	risk, err := riskDTO(&candidate.candidate.Risk)
	if err != nil {
		return RecommendationDTO{}, err
	}

	machine := candidate.candidate.Machine
	recommendation := RecommendationDTO{
		Rank:           rank,
		Cloud:          candidate.candidate.Provider,
		Region:         candidate.candidate.Location.Region,
		Machine:        machine.ID,
		Architecture:   machine.Architecture,
		OS:             candidate.candidate.OS,
		VCPU:           machine.VCPU,
		MemoryGiB:      machine.MemoryGiB,
		SpotUSDPerHour: candidate.candidate.Spot.Amount.String(),
		Risk:           risk,
		RationaleCodes: candidate.rationaleCodes(workload),
	}
	if onDemand := candidate.candidate.OnDemand; onDemand != nil {
		price := onDemand.Amount.String()
		recommendation.OnDemandUSDPerHour = &price
	}
	if savings := candidate.candidate.SavingsPercent; savings != nil {
		// The published schema bounds savings to 0..100. A provider that maps a
		// figure outside it has a mapping bug, and publishing the number anyway
		// would hand the client a payload its own schema rejects.
		if *savings < 0 || *savings > maxSavingsPercent {
			return RecommendationDTO{}, fmt.Errorf("savings %d%% for %s is not a percentage of on-demand",
				*savings, machine.ID)
		}
		percent := float64(*savings)
		recommendation.SavingsPercent = &percent
	}

	return recommendation, nil
}

// riskDTO publishes a risk observation. An unmapped kind fails closed: emitting
// a domain constant would publish a value outside the frozen enum.
func riskDTO(risk *RiskObservation) (RiskDTO, error) {
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

// sourceDTOs publishes the provenance of every snapshot a result was built
// from. A provider that cannot say where its data came from cannot serve a v2
// answer, so an empty or incomplete source list fails rather than publishing a
// payload with invented provenance.
func sourceDTOs(result *Result) ([]SourceDTO, error) {
	if len(result.Sources) == 0 {
		return nil, fmt.Errorf("%w: provider %q reported no data sources", ErrDataUnavailable, result.Provider)
	}

	sources := make([]SourceDTO, 0, len(result.Sources))
	for _, source := range result.Sources {
		if source.URL == "" || source.ContentSHA256 == "" || source.ParserVersion == "" ||
			source.SchemaVersion == "" || source.FetchedAt.IsZero() {
			return nil, fmt.Errorf("%w: provider %q reported an incomplete data source", ErrDataUnavailable, result.Provider)
		}

		published := SourceDTO{
			URL:           source.URL,
			FetchedAt:     *timestamp(source.FetchedAt),
			ContentSHA256: source.ContentSHA256,
			ParserVersion: source.ParserVersion,
			SchemaVersion: source.SchemaVersion,
		}
		if source.ObservedAt != nil {
			published.ObservedAt = timestamp(*source.ObservedAt)
		}
		sources = append(sources, published)
	}

	return sources, nil
}

// timestamp renders an instant in the one format the schemas accept.
func timestamp(instant time.Time) *string {
	formatted := instant.UTC().Format(time.RFC3339)

	return &formatted
}
