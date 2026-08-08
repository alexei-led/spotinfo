// Package snapshot defines the manifest contract every embedded spotinfo data
// snapshot must satisfy, together with the fail-closed validators and the atomic
// writer the update commands use.
//
// A snapshot is two committed artifacts: the data file and a sidecar manifest
// recording which document the bytes came from, when, which parser read them,
// and how much data must be present. The manifest exists so a refreshed or
// hand-edited data file cannot silently replace reviewed data with partial,
// duplicated, or differently shaped content.
//
// This package depends only on the standard library and internal/cloud. It must
// not import a provider SDK, the legacy AWS package, the CLI, or the MCP server.
package snapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"spotinfo/internal/cloud"
)

// ManifestSchemaVersion is the version of the manifest format itself, not of the
// data it describes.
const ManifestSchemaVersion = "spotinfo.snapshot-manifest/v1"

// httpsScheme is the only accepted source scheme.
const httpsScheme = "https"

// Errors a caller distinguishes. Everything else is an invalid manifest.
var (
	// ErrInvalidManifest identifies a manifest that is missing, malformed, or
	// internally inconsistent. A snapshot with such a manifest is unusable.
	ErrInvalidManifest = errors.New("invalid snapshot manifest")
	// ErrHashMismatch identifies a data file that does not match the hash its
	// manifest declares: the pair was not updated together.
	ErrHashMismatch = errors.New("snapshot payload does not match its manifest hash")
	// ErrCoverage identifies a snapshot that parsed but carries less data than
	// its manifest requires.
	ErrCoverage = errors.New("snapshot coverage below the manifest floor")
	// ErrDuplicateRecord identifies one machine priced two different ways in the
	// same region, OS, and class. An exact repeat is not an error; a conflict is,
	// because nothing can choose between the two amounts.
	ErrDuplicateRecord = errors.New("ambiguous snapshot record")
)

// Kind names what a snapshot contains. Each kind has its own parser contract.
type Kind string

const (
	// KindSpotPrice is interruptible spare-capacity pricing.
	KindSpotPrice Kind = "spot-price"
	// KindOnDemandPrice is uninterruptible list pricing.
	KindOnDemandPrice Kind = "on-demand-price"
	// KindInterruptionRisk is provider-published interruption or eviction risk.
	KindInterruptionRisk Kind = "interruption-risk"
	// KindMachineArchitecture is the reviewed machine-to-architecture mapping.
	KindMachineArchitecture Kind = "machine-architecture"
)

// Kinds returns every recognised snapshot kind in stable lexical order.
func Kinds() []Kind {
	return []Kind{KindInterruptionRisk, KindMachineArchitecture, KindOnDemandPrice, KindSpotPrice}
}

// IsPrice reports whether a kind carries monetary amounts. Only those kinds
// declare a currency and billing unit.
func (k Kind) IsPrice() bool {
	return k == KindSpotPrice || k == KindOnDemandPrice
}

// PayloadForm records how the committed bytes relate to their upstream sources.
type PayloadForm string

const (
	// PayloadFormRawSource means the committed file is the upstream document
	// byte for byte, as the AWS feeds are. The payload hash and the source hash
	// then describe the same artifact and must be equal.
	PayloadFormRawSource PayloadForm = "raw-source"
	// PayloadFormParsedCatalog means a parser derived the committed file from
	// its sources. The payload hash then describes a different artifact from
	// every source hash and must differ from all of them.
	PayloadFormParsedCatalog PayloadForm = "parsed-catalog"
)

// Source records the provenance of one upstream document.
type Source struct {
	// FetchedAt is when the document was retrieved.
	FetchedAt time.Time `json:"fetched_at"`
	// ObservedAt is when the provider says the data was measured. It is nil when
	// the document publishes no observation time; it is never inferred.
	ObservedAt *time.Time `json:"observed_at"`
	URL        string     `json:"url"`
	// SHA256 is the hash of the retrieved bytes. It is empty only for a reviewed
	// catalogue built by hand from human-readable documentation, which has no
	// stable byte-level source. A raw-source payload always has it.
	SHA256 string `json:"sha256"`
}

// Payload describes the committed data file.
type Payload struct {
	// File is the data file name, resolved relative to the manifest's directory.
	File   string      `json:"file"`
	Form   PayloadForm `json:"form"`
	SHA256 string      `json:"sha256"`
}

// Coverage counts the records a snapshot carries. In a manifest it is a floor,
// not a census: a refresh may add regions and machines, but dropping below the
// reviewed floor is a coverage failure rather than an update. Keeping it a floor
// is also what stops a weekly refresh from rewriting the manifest's review
// metadata on every run.
type Coverage struct {
	Regions  int `json:"regions"`
	Machines int `json:"machines"`
	Prices   int `json:"prices"`
}

// Manifest is the sidecar contract for one committed data file.
type Manifest struct {
	// SchemaVersion versions this manifest format.
	SchemaVersion string `json:"schema_version"`
	// DataSchemaVersion versions the shape of the data file, so a source that
	// changes shape can be told apart from a source that changed values.
	DataSchemaVersion string           `json:"data_schema_version"`
	ParserVersion     string           `json:"parser_version"`
	Provider          cloud.ProviderID `json:"provider"`
	Kind              Kind             `json:"kind"`
	// Currency and BillingUnit are set only for price kinds, so an amount can
	// never be read without knowing what it is denominated in.
	Currency    cloud.Currency    `json:"currency,omitempty"`
	BillingUnit cloud.BillingUnit `json:"billing_unit,omitempty"`
	Payload     Payload           `json:"payload"`
	Sources     []Source          `json:"sources"`
	MinRecords  Coverage          `json:"min_records"`
}

// ParseManifest decodes and fully validates a manifest. Unknown fields are
// rejected: a renamed key must fail loudly rather than validate as a zero value.
func ParseManifest(data []byte) (*Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
	}

	if err := manifest.Validate(); err != nil {
		return nil, err
	}

	return &manifest, nil
}

// Validate reports whether the manifest is self-consistent and complete.
func (m *Manifest) Validate() error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("%w: schema_version must be %q, got %q",
			ErrInvalidManifest, ManifestSchemaVersion, m.SchemaVersion)
	}

	if !slices.Contains(cloud.ProviderIDs(), m.Provider) {
		return fmt.Errorf("%w: unknown provider %q", ErrInvalidManifest, m.Provider)
	}

	if !slices.Contains(Kinds(), m.Kind) {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidManifest, m.Kind)
	}

	if strings.TrimSpace(m.DataSchemaVersion) == "" || strings.TrimSpace(m.ParserVersion) == "" {
		return fmt.Errorf("%w: data_schema_version and parser_version are required", ErrInvalidManifest)
	}

	if err := m.validateSources(); err != nil {
		return err
	}

	if err := m.validatePayload(); err != nil {
		return err
	}

	if err := m.validateAmountFields(); err != nil {
		return err
	}

	return m.validateFloor()
}

// VerifyPayload recomputes the payload hash over the committed bytes.
//
// This is a gate check, not a runtime one: once a data file is compiled into the
// binary its bytes cannot drift, so re-hashing megabytes on every process start
// would cost real time to prove something the commit already fixed.
func (m *Manifest) VerifyPayload(payload []byte) error {
	if sum := SHA256Hex(payload); sum != m.Payload.SHA256 {
		return fmt.Errorf("%w: %s hashes to %s, manifest declares %s",
			ErrHashMismatch, m.Payload.File, sum, m.Payload.SHA256)
	}

	return nil
}

// SourceRefs converts manifest provenance into neutral source references, so a
// provider result can report where its answer came from without inventing one.
//
// A reviewed catalogue built by reading a documentation page has no upstream
// hash — nobody fetched and hashed the HTML. Those refs carry the payload hash
// instead, so every published reference identifies bytes this repository can
// verify rather than being empty. For a raw feed the two hashes are equal by
// construction, so the fallback changes nothing there.
func (m *Manifest) SourceRefs() []cloud.SourceRef {
	refs := make([]cloud.SourceRef, 0, len(m.Sources))
	for i := range m.Sources {
		contentSHA256 := m.Sources[i].SHA256
		if contentSHA256 == "" {
			contentSHA256 = m.Payload.SHA256
		}

		refs = append(refs, cloud.SourceRef{
			FetchedAt:     m.Sources[i].FetchedAt,
			ObservedAt:    m.Sources[i].ObservedAt,
			URL:           m.Sources[i].URL,
			ContentSHA256: contentSHA256,
			ParserVersion: m.ParserVersion,
			SchemaVersion: m.DataSchemaVersion,
		})
	}

	return refs
}

// SHA256Hex is the canonical hash encoding used throughout snapshot manifests.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func (m *Manifest) validateSources() error {
	if len(m.Sources) == 0 {
		return fmt.Errorf("%w: at least one source is required", ErrInvalidManifest)
	}

	for i := range m.Sources {
		source := &m.Sources[i]

		if !isHTTPSURL(source.URL) {
			return fmt.Errorf("%w: source %d needs an absolute https url, got %q",
				ErrInvalidManifest, i, source.URL)
		}

		if source.FetchedAt.IsZero() {
			return fmt.Errorf("%w: source %d has no fetched_at", ErrInvalidManifest, i)
		}

		if source.ObservedAt != nil && source.ObservedAt.After(source.FetchedAt) {
			return fmt.Errorf("%w: source %d claims to be observed after it was fetched", ErrInvalidManifest, i)
		}

		if source.SHA256 != "" && !isSHA256Hex(source.SHA256) {
			return fmt.Errorf("%w: source %d has a malformed sha256", ErrInvalidManifest, i)
		}
	}

	return nil
}

func (m *Manifest) validatePayload() error {
	if m.Payload.File == "" || m.Payload.File != filepath.Base(m.Payload.File) {
		return fmt.Errorf("%w: payload file must be a plain file name, got %q",
			ErrInvalidManifest, m.Payload.File)
	}

	if !isSHA256Hex(m.Payload.SHA256) {
		return fmt.Errorf("%w: payload sha256 is malformed", ErrInvalidManifest)
	}

	switch m.Payload.Form {
	case PayloadFormRawSource:
		if len(m.Sources) != 1 {
			return fmt.Errorf("%w: a raw-source payload has exactly one source, got %d",
				ErrInvalidManifest, len(m.Sources))
		}

		if m.Sources[0].SHA256 != m.Payload.SHA256 {
			return fmt.Errorf("%w: a raw-source payload is its source, so their hashes must match",
				ErrInvalidManifest)
		}
	case PayloadFormParsedCatalog:
		for i := range m.Sources {
			if m.Sources[i].SHA256 == m.Payload.SHA256 {
				return fmt.Errorf("%w: a parsed-catalog payload must differ from source %d",
					ErrInvalidManifest, i)
			}
		}
	default:
		return fmt.Errorf("%w: unknown payload form %q", ErrInvalidManifest, m.Payload.Form)
	}

	return nil
}

// validateAmountFields keeps currency and billing unit tied to the kinds that
// actually carry amounts. A risk snapshot claiming USD would let a later change
// read a percentage as a price.
func (m *Manifest) validateAmountFields() error {
	if !m.Kind.IsPrice() {
		if m.Currency != "" || m.BillingUnit != "" {
			return fmt.Errorf("%w: kind %q carries no amounts, so currency and billing_unit must be empty",
				ErrInvalidManifest, m.Kind)
		}

		return nil
	}

	if m.Currency != cloud.CurrencyUSD {
		return fmt.Errorf("%w: unsupported currency %q", ErrInvalidManifest, m.Currency)
	}

	if m.BillingUnit != cloud.BillingUnitInstanceHour {
		return fmt.Errorf("%w: unsupported billing unit %q", ErrInvalidManifest, m.BillingUnit)
	}

	return nil
}

func (m *Manifest) validateFloor() error {
	floor := m.MinRecords
	if floor.Regions < 0 || floor.Machines < 0 || floor.Prices < 0 {
		return fmt.Errorf("%w: min_records cannot be negative", ErrInvalidManifest)
	}

	if floor.Machines == 0 {
		return fmt.Errorf("%w: min_records.machines must be positive", ErrInvalidManifest)
	}

	if m.Kind.IsPrice() && (floor.Regions == 0 || floor.Prices == 0) {
		return fmt.Errorf("%w: a price snapshot must require at least one region and one price",
			ErrInvalidManifest)
	}

	return nil
}

// isHTTPSURL requires an absolute https source. Plain http is rejected because
// every contracted provider document is served over TLS, and a snapshot's
// provenance is worthless if the bytes could have been rewritten in transit.
func isHTTPSURL(value string) bool {
	parsed, err := url.Parse(value)

	return err == nil && parsed.Scheme == httpsScheme && parsed.Host != ""
}

func isSHA256Hex(value string) bool {
	if len(value) != hex.EncodedLen(sha256.Size) {
		return false
	}

	_, err := hex.DecodeString(value)

	return err == nil && value == strings.ToLower(value)
}
