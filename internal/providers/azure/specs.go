package azure

import (
	"regexp"

	"spotinfo/internal/cloud"
)

// sizeName is the exact shape of an Azure VM size identifier. It is the same
// string the Retail Prices API publishes as armSkuName, which is what lets the
// two sources be joined at all.
var sizeName = regexp.MustCompile(`^Standard_[A-Za-z0-9]+(?:_[A-Za-z0-9]+)*$`)

// ValidSizeName reports whether a string is shaped like an Azure VM size
// identifier.
//
// The catalogue verifier and the Learn size-page parser have to agree on this
// exactly — the parser decides which table rows are sizes, and the verifier
// re-checks every committed machine — so the pattern is defined once here and
// the parser, which lives in cmd/update-azure-data, asks through this function.
func ValidSizeName(name string) bool { return sizeName.MatchString(name) }

// SeriesSpec is one machine series as its Learn page publishes it: a processor
// architecture shared by every size, and the sizes themselves.
//
// The type stays in the runtime package because the committed catalogue is
// built from it; the parser that produces one from HTML is build-time only and
// lives in cmd/update-azure-data, so the shipped binary links no HTML parser.
type SeriesSpec struct {
	Series       string
	Architecture cloud.Architecture
	Sizes        []SizeSpec
}

// SizeSpec is one VM size's specification.
type SizeSpec struct {
	ID        cloud.MachineID
	MemoryGiB float64
	VCPU      int
}
