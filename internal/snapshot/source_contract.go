package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"spotinfo/internal/cloud"
)

// SourceContractSchemaVersion versions the provider source approval contract.
//
// The normative definition is docs/plans/contracts/provider-source-contract.schema.json.
// The checks below mirror it field for field; changing one without the other is
// a defect. A JSON Schema library is deliberately not used: this binary ships as
// a scratch image with no runtime dependencies, and the schema is small enough
// that mirroring it costs less than the dependency.
const SourceContractSchemaVersion = "spotinfo.source-contract/v1"

// Approval vocabulary. Anything else is a rejection.
const (
	reviewStatusApproved         = "approved"
	redistributionDecisionApprov = "approved"
	updateCadenceWeekly          = "weekly"
)

// SourceType names the kind of document a provider source is. Only official,
// stable documents qualify: no pricing calculators, undocumented JavaScript
// endpoints, or third-party aggregators.
type SourceType string

const (
	// SourceTypeDocumentedRESTAPI is a documented, anonymous REST API.
	SourceTypeDocumentedRESTAPI SourceType = "documented-rest-api"
	// SourceTypeOfficialServerRenderedHTML is an official server-rendered page.
	SourceTypeOfficialServerRenderedHTML SourceType = "official-server-rendered-html"
)

// DataKind names what a contracted source supplies.
type DataKind string

const (
	// DataKindSpotPrice is interruptible spare-capacity pricing.
	DataKindSpotPrice DataKind = "spot-price"
	// DataKindOnDemandPrice is uninterruptible list pricing.
	DataKindOnDemandPrice DataKind = "on-demand-price"
	// DataKindMachineSpec is vCPU and memory per machine.
	DataKindMachineSpec DataKind = "machine-spec"
	// DataKindArchitecture is the machine processor architecture.
	DataKindArchitecture DataKind = "architecture"
)

var (
	// ErrInvalidSourceContract identifies a malformed or incomplete contract.
	ErrInvalidSourceContract = errors.New("invalid provider source contract")
	// ErrUnapprovedSource identifies a contract whose review or redistribution
	// decision is anything other than an explicit approval. A provider must not
	// ship data from an unapproved source.
	ErrUnapprovedSource = errors.New("provider source is not approved for redistribution")
	// ErrContractMismatch identifies a snapshot that contradicts its contract.
	ErrContractMismatch = errors.New("snapshot contradicts its provider source contract")
)

// ContractSource is one approved upstream document.
type ContractSource struct {
	URL        string     `json:"url"`
	SourceType SourceType `json:"source_type"`
	DataKinds  []DataKind `json:"data_kinds"`
}

// Terms records the redistribution decision and the evidence behind it.
type Terms struct {
	RedistributionDecision string `json:"redistribution_decision"`
	EvidenceURL            string `json:"evidence_url"`
	Notes                  string `json:"notes"`
}

// Support is the exact matrix a provider claims to cover offline. It is
// enumerated rather than described, so an unlisted region or machine series is
// out of contract instead of a judgement call.
type Support struct {
	RiskStatus       cloud.RiskStatus        `json:"risk_status"`
	OperatingSystems []cloud.OperatingSystem `json:"operating_systems"`
	Architectures    []cloud.Architecture    `json:"architectures"`
	PriceClasses     []cloud.PriceClass      `json:"price_classes"`
	Regions          []cloud.Region          `json:"regions"`
	MachineSeries    []string                `json:"machine_series"`
}

// Thresholds are the gate limits a committed snapshot must satisfy.
type Thresholds struct {
	MinRegions          int `json:"min_regions"`
	MinMachines         int `json:"min_machines"`
	MaxCompressedBytes  int `json:"max_compressed_bytes"`
	MaxFractionalDigits int `json:"max_fractional_digits"`
}

// SourceContract is a provider's approved data-source agreement. No provider
// implementation may read a source that is not enumerated here.
type SourceContract struct {
	SchemaVersion  string           `json:"schema_version"`
	Provider       cloud.ProviderID `json:"provider"`
	ReviewStatus   string           `json:"review_status"`
	Reviewer       string           `json:"reviewer"`
	ReviewedAt     string           `json:"reviewed_at"`
	ParserVersion  string           `json:"parser_version"`
	UpdateCadence  string           `json:"update_cadence"`
	Terms          Terms            `json:"terms"`
	Support        Support          `json:"support"`
	Sources        []ContractSource `json:"sources"`
	ExpectedFields []string         `json:"expected_fields"`
	NoGoConditions []string         `json:"no_go_conditions"`
	Thresholds     Thresholds       `json:"thresholds"`
}

// ParseSourceContract decodes and fully validates a provider source contract.
// Unknown fields are rejected so a renamed key fails loudly.
func ParseSourceContract(data []byte) (*SourceContract, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var contract SourceContract
	if err := decoder.Decode(&contract); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSourceContract, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSourceContract, err)
	}

	if err := contract.validate(); err != nil {
		return nil, err
	}

	return &contract, nil
}

// VerifySnapshot reports whether a committed snapshot is consistent with the
// contract that approved its sources: same provider, same parser, approved
// source set, coverage at or above the contracted floor, and within the size
// limit.
func (c *SourceContract) VerifySnapshot(manifest *Manifest, payloadBytes int) error {
	if manifest.Provider != c.Provider {
		return fmt.Errorf("%w: contract covers %q, manifest describes %q",
			ErrContractMismatch, c.Provider, manifest.Provider)
	}

	if manifest.ParserVersion != c.ParserVersion {
		return fmt.Errorf("%w: contract approves parser %q, manifest declares %q",
			ErrContractMismatch, c.ParserVersion, manifest.ParserVersion)
	}

	if err := c.verifySources(manifest.Sources); err != nil {
		return err
	}

	if manifest.MinRecords.Regions < c.Thresholds.MinRegions ||
		manifest.MinRecords.Machines < c.Thresholds.MinMachines {
		return fmt.Errorf("%w: manifest floor (%d regions, %d machines) is below the contracted minimum (%d, %d)",
			ErrContractMismatch, manifest.MinRecords.Regions, manifest.MinRecords.Machines,
			c.Thresholds.MinRegions, c.Thresholds.MinMachines)
	}

	if payloadBytes > c.Thresholds.MaxCompressedBytes {
		return fmt.Errorf("%w: payload is %d bytes, contract allows %d",
			ErrContractMismatch, payloadBytes, c.Thresholds.MaxCompressedBytes)
	}

	return nil
}

// verifySources binds every manifest source to an approved contract source and
// rejects missing, extra, or duplicate provenance entries. Contract API URLs
// may carry provider-specific query parameters in the manifest; their scheme,
// host, and path must still match exactly.
func (c *SourceContract) verifySources(sources []Source) error {
	if len(sources) == 0 {
		return fmt.Errorf("%w: snapshot has no sources", ErrContractMismatch)
	}

	matched := make([]bool, len(c.Sources))
	seenURLs := make(map[string]struct{}, len(sources))
	for i := range sources {
		if _, duplicate := seenURLs[sources[i].URL]; duplicate {
			return fmt.Errorf("%w: manifest source %q is duplicated",
				ErrContractMismatch, sources[i].URL)
		}
		seenURLs[sources[i].URL] = struct{}{}

		matches := -1
		for j := range c.Sources {
			if sourceURLMatches(c.Sources[j].URL, sources[i].URL) {
				matches = j
				break
			}
		}
		if matches == -1 {
			return fmt.Errorf("%w: manifest source %q is not approved",
				ErrContractMismatch, sources[i].URL)
		}
		matched[matches] = true
	}

	for i, source := range c.Sources {
		if !matched[i] {
			return fmt.Errorf("%w: approved source %q is missing from manifest",
				ErrContractMismatch, source.URL)
		}
	}

	return nil
}

func sourceURLMatches(approved, observed string) bool {
	approvedURL, approvedErr := url.Parse(approved)
	observedURL, observedErr := url.Parse(observed)
	if approvedErr != nil || observedErr != nil ||
		approvedURL.Scheme != observedURL.Scheme ||
		approvedURL.Host != observedURL.Host ||
		approvedURL.Path != observedURL.Path {
		return false
	}

	// A contract with an explicit query approves that exact URL. A queryless
	// API contract approves the provider's documented query variants.
	return approvedURL.RawQuery == "" || approvedURL.RawQuery == observedURL.RawQuery
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}

	return nil
}

func (c *SourceContract) validate() error {
	if c.SchemaVersion != SourceContractSchemaVersion {
		return fmt.Errorf("%w: schema_version must be %q, got %q",
			ErrInvalidSourceContract, SourceContractSchemaVersion, c.SchemaVersion)
	}

	// AWS is absent by design: it predates the contract and embeds raw feeds
	// under its own parser contract in internal/spot.
	if c.Provider != cloud.ProviderGCP && c.Provider != cloud.ProviderAzure {
		return fmt.Errorf("%w: contracts cover %s and %s, got %q",
			ErrInvalidSourceContract, cloud.ProviderGCP, cloud.ProviderAzure, c.Provider)
	}

	if c.ReviewStatus != reviewStatusApproved {
		return fmt.Errorf("%w: review_status is %q", ErrUnapprovedSource, c.ReviewStatus)
	}

	if err := c.validateReview(); err != nil {
		return err
	}

	if err := c.validateSources(); err != nil {
		return err
	}

	if err := c.validateTerms(); err != nil {
		return err
	}

	if err := c.Support.validate(); err != nil {
		return err
	}

	if err := c.Thresholds.validate(); err != nil {
		return err
	}

	if err := uniqueNonEmpty("expected_fields", c.ExpectedFields); err != nil {
		return err
	}

	return uniqueNonEmpty("no_go_conditions", c.NoGoConditions)
}

func (c *SourceContract) validateReview() error {
	if strings.TrimSpace(c.Reviewer) == "" {
		return fmt.Errorf("%w: reviewer is required", ErrInvalidSourceContract)
	}

	if _, err := time.Parse(time.DateOnly, c.ReviewedAt); err != nil {
		return fmt.Errorf("%w: reviewed_at %q is not a %s date",
			ErrInvalidSourceContract, c.ReviewedAt, time.DateOnly)
	}

	if strings.TrimSpace(c.ParserVersion) == "" {
		return fmt.Errorf("%w: parser_version is required", ErrInvalidSourceContract)
	}

	if c.UpdateCadence != updateCadenceWeekly {
		return fmt.Errorf("%w: update_cadence must be %q, got %q",
			ErrInvalidSourceContract, updateCadenceWeekly, c.UpdateCadence)
	}

	return nil
}

func (c *SourceContract) validateSources() error {
	if len(c.Sources) == 0 {
		return fmt.Errorf("%w: at least one source is required", ErrInvalidSourceContract)
	}

	for i := range c.Sources {
		source := &c.Sources[i]
		if err := requireHTTPS(fmt.Sprintf("sources[%d].url", i), source.URL); err != nil {
			return err
		}

		if source.SourceType != SourceTypeDocumentedRESTAPI &&
			source.SourceType != SourceTypeOfficialServerRenderedHTML {
			return fmt.Errorf("%w: sources[%d] has unapproved source_type %q",
				ErrInvalidSourceContract, i, source.SourceType)
		}

		if err := uniqueNonEmpty(fmt.Sprintf("sources[%d].data_kinds", i), source.DataKinds); err != nil {
			return err
		}

		for _, kind := range source.DataKinds {
			if !slices.Contains(dataKinds(), kind) {
				return fmt.Errorf("%w: sources[%d] declares unknown data kind %q",
					ErrInvalidSourceContract, i, kind)
			}
		}
	}

	return nil
}

func (c *SourceContract) validateTerms() error {
	if c.Terms.RedistributionDecision != redistributionDecisionApprov {
		return fmt.Errorf("%w: terms.redistribution_decision is %q",
			ErrUnapprovedSource, c.Terms.RedistributionDecision)
	}

	if err := requireHTTPS("terms.evidence_url", c.Terms.EvidenceURL); err != nil {
		return err
	}

	if strings.TrimSpace(c.Terms.Notes) == "" {
		return fmt.Errorf("%w: terms.notes must record the decision", ErrInvalidSourceContract)
	}

	return nil
}

func (s *Support) validate() error {
	// Offline providers publish no risk. Claiming otherwise would let a neutral
	// consumer rank one provider's silence against another's measurement.
	if s.RiskStatus != cloud.RiskStatusUnavailable {
		return fmt.Errorf("%w: support.risk_status must be %q, got %q",
			ErrInvalidSourceContract, cloud.RiskStatusUnavailable, s.RiskStatus)
	}

	if err := allowedValues("support.operating_systems", s.OperatingSystems, cloud.OSLinux, cloud.OSWindows); err != nil {
		return err
	}

	if err := allowedValues("support.architectures", s.Architectures,
		cloud.ArchitectureX8664, cloud.ArchitectureARM64); err != nil {
		return err
	}

	if err := allowedValues("support.price_classes", s.PriceClasses,
		cloud.PriceClassSpot, cloud.PriceClassOnDemand); err != nil {
		return err
	}

	// Both classes, because a savings percentage without a paired On-Demand
	// price is a number with no denominator.
	if !slices.Contains(s.PriceClasses, cloud.PriceClassSpot) ||
		!slices.Contains(s.PriceClasses, cloud.PriceClassOnDemand) {
		return fmt.Errorf("%w: support.price_classes must contain both %q and %q",
			ErrInvalidSourceContract, cloud.PriceClassSpot, cloud.PriceClassOnDemand)
	}

	if err := uniqueNonEmpty("support.regions", s.Regions); err != nil {
		return err
	}

	return uniqueNonEmpty("support.machine_series", s.MachineSeries)
}

func (t *Thresholds) validate() error {
	if t.MinRegions < 1 || t.MinMachines < 1 || t.MaxCompressedBytes < 1 {
		return fmt.Errorf("%w: thresholds min_regions, min_machines and max_compressed_bytes must be positive",
			ErrInvalidSourceContract)
	}

	// Bounded by the fixed-point scale in internal/cloud: a source needing more
	// precision must raise that scale deliberately, not round at the parser.
	if t.MaxFractionalDigits < 0 || t.MaxFractionalDigits > cloud.MoneyScale {
		return fmt.Errorf("%w: thresholds.max_fractional_digits must be between 0 and %d",
			ErrInvalidSourceContract, cloud.MoneyScale)
	}

	return nil
}

func dataKinds() []DataKind {
	return []DataKind{DataKindArchitecture, DataKindMachineSpec, DataKindOnDemandPrice, DataKindSpotPrice}
}

func requireHTTPS(field, value string) error {
	if !isHTTPSURL(value) {
		return fmt.Errorf("%w: %s needs an absolute https url, got %q",
			ErrInvalidSourceContract, field, value)
	}

	return nil
}

func uniqueNonEmpty[T ~string](field string, values []T) error {
	if len(values) == 0 {
		return fmt.Errorf("%w: %s must list at least one value", ErrInvalidSourceContract, field)
	}

	seen := make(map[T]struct{}, len(values))

	for _, value := range values {
		if strings.TrimSpace(string(value)) == "" {
			return fmt.Errorf("%w: %s contains an empty value", ErrInvalidSourceContract, field)
		}

		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%w: %s repeats %q", ErrInvalidSourceContract, field, value)
		}

		seen[value] = struct{}{}
	}

	return nil
}

func allowedValues[T ~string](field string, values []T, allowed ...T) error {
	if err := uniqueNonEmpty(field, values); err != nil {
		return err
	}

	for _, value := range values {
		if !slices.Contains(allowed, value) {
			return fmt.Errorf("%w: %s contains unsupported value %q", ErrInvalidSourceContract, field, value)
		}
	}

	return nil
}
