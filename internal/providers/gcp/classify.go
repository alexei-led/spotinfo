package gcp

import (
	"errors"
	"regexp"
	"strings"

	"spotinfo/internal/cloud"
)

const (
	// ParserVersion identifies the HTML contract the committed snapshot was
	// produced by. The parser itself is build-time only and lives in
	// cmd/update-gcp-data; this stays here because embed.go checks it against
	// the manifest at load time. Bump it whenever a header, a column position,
	// or the region rule changes, so a committed snapshot can never be read as
	// the product of a different parser.
	ParserVersion = "gcp-pricing-html/1"
	// CatalogSchemaVersion versions the committed catalogue shape.
	CatalogSchemaVersion = "spotinfo.gcp-catalog/v1"
)

var (
	// ErrSourceContract reports a page that no longer matches the approved
	// parser contract: no region selector, no recognised table, or a row whose
	// cells cannot be read.
	ErrSourceContract = errors.New("gcp pricing page does not match its parser contract")
	// ErrCatalog reports a committed catalogue that contradicts the contract
	// that approved it.
	ErrCatalog = errors.New("invalid gcp catalogue")
)

// machineIDPattern is the exact shape of a Compute Engine machine type. It
// rejects the annotated rows the pages carry, such as
// "n1-standard-96 Skylake Platform only", which name a platform variant rather
// than a machine type a user can request.
//
// The catalogue verifier and the pricing-page parser have to agree on this
// exactly — the parser decides which table rows are machines, and the verifier
// re-checks every committed machine — so it is defined once here and the parser,
// which lives in cmd/update-gcp-data, asks through ValidMachineID.
var machineIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)+$`)

// ValidMachineID reports whether a string is shaped like a Compute Engine
// machine type identifier.
func ValidMachineID(id string) bool { return machineIDPattern.MatchString(id) }

// seriesArchitecture classifies every machine series the source contract
// approves. The pricing tables publish no architecture column, so it comes from
// this reviewed list — recorded as a source in the committed manifest — rather
// than from a guess about the machine name.
//
// The map is total on purpose: an unclassified series has no architecture and
// fails. Defaulting to x86_64 would let a new Arm series approved in the
// contract ship as x86_64 and pass every other gate, recommending machines that
// cannot run the caller's binaries.
// TestEveryContractedSeriesIsClassified pins the two lists together.
var seriesArchitecture = map[string]cloud.Architecture{
	"c2":  cloud.ArchitectureX8664,
	"c3":  cloud.ArchitectureX8664,
	"c3d": cloud.ArchitectureX8664,
	"c4":  cloud.ArchitectureX8664,
	"c4a": cloud.ArchitectureARM64,
	"c4d": cloud.ArchitectureX8664,
	"e2":  cloud.ArchitectureX8664,
	"m1":  cloud.ArchitectureX8664,
	"m2":  cloud.ArchitectureX8664,
	"m3":  cloud.ArchitectureX8664,
	"n1":  cloud.ArchitectureX8664,
	"n2":  cloud.ArchitectureX8664,
	"n2d": cloud.ArchitectureX8664,
	"n4":  cloud.ArchitectureX8664,
	"n4a": cloud.ArchitectureARM64,
	"n4d": cloud.ArchitectureX8664,
	"t2a": cloud.ArchitectureARM64,
	"t2d": cloud.ArchitectureX8664,
}

// MachineRow is one machine as a pricing page publishes it: identifier,
// specification, and the single price that page's contracted column carries.
//
// The type stays in the runtime package because the committed catalogue is built
// from it; the parser that produces one from HTML is build-time only and lives
// in cmd/update-gcp-data, so the shipped binary links no HTML parser.
type MachineRow struct {
	ID        cloud.MachineID
	Price     cloud.Money
	MemoryGiB float64
	VCPU      int
}

// SeriesOf returns the series prefix of a machine identifier: "c4" for
// "c4-standard-2". The series is what the source contract enumerates and what
// the architecture list is keyed by.
func SeriesOf(id cloud.MachineID) string {
	series, _, _ := strings.Cut(string(id), "-")

	return series
}

// ArchitectureOf resolves a machine series to its reviewed processor
// architecture. A series this package has not classified reports false: the
// caller must fail on it rather than assume one.
func ArchitectureOf(id cloud.MachineID) (cloud.Architecture, bool) {
	architecture, classified := seriesArchitecture[SeriesOf(id)]

	return architecture, classified
}
