package spot

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

//go:embed data/architecture-snapshot.json
var architectureSnapshotFS embed.FS

// Architecture identifies an EC2 instance processor architecture.
type Architecture string

const (
	// ArchitectureX8664 is the 64-bit x86 EC2 architecture.
	ArchitectureX8664 Architecture = "x86_64"
	// ArchitectureARM64 is the 64-bit Arm EC2 architecture.
	ArchitectureARM64 Architecture = "arm64"
)

// ArchitectureLookup maps an Advisor instance family to its reviewed architecture.
type ArchitectureLookup struct {
	families map[string]Architecture
}

type architectureSnapshot struct {
	Families   map[string]Architecture `json:"families"`
	Provenance string                  `json:"provenance"`
	ReviewedAt string                  `json:"reviewed_at"`
}

// LoadEmbeddedArchitectureLookup parses the committed, reviewed architecture
// snapshot. It makes no AWS metadata calls and unknown families are deliberately
// omitted rather than inferred from their names.
func LoadEmbeddedArchitectureLookup() (*ArchitectureLookup, error) {
	contents, err := architectureSnapshotFS.ReadFile("data/architecture-snapshot.json")
	if err != nil {
		return nil, fmt.Errorf("read embedded architecture snapshot: %w", err)
	}

	// Verified against its sidecar manifest first, like the advisor and price
	// payloads: a snapshot that drifted from the hash this binary publishes as
	// its provenance must not be parsed and served.
	if err := verifyEmbeddedPayload(architectureManifest, architectureManifestFile, contents); err != nil {
		return nil, err
	}

	return parseArchitectureSnapshot(contents)
}

func parseArchitectureSnapshot(contents []byte) (*ArchitectureLookup, error) {
	var snapshot architectureSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		return nil, fmt.Errorf("parse architecture snapshot: %w", err)
	}
	if strings.TrimSpace(snapshot.Provenance) == "" {
		return nil, errors.New("architecture snapshot has no provenance")
	}
	if strings.TrimSpace(snapshot.ReviewedAt) == "" {
		return nil, errors.New("architecture snapshot has no reviewed_at date")
	}
	if _, err := time.Parse(time.DateOnly, snapshot.ReviewedAt); err != nil {
		return nil, fmt.Errorf("invalid architecture snapshot reviewed_at %q: %w", snapshot.ReviewedAt, err)
	}
	if len(snapshot.Families) == 0 {
		return nil, errors.New("architecture snapshot contains no families")
	}

	lookup := &ArchitectureLookup{families: make(map[string]Architecture, len(snapshot.Families))}
	for family, architecture := range snapshot.Families {
		if family == "" || strings.Contains(family, ".") {
			return nil, fmt.Errorf("invalid architecture family %q", family)
		}
		if architecture != ArchitectureX8664 && architecture != ArchitectureARM64 {
			return nil, fmt.Errorf("invalid architecture %q for family %q", architecture, family)
		}
		lookup.families[family] = architecture
	}

	return lookup, nil
}

// ArchitectureForInstance returns an architecture only when its family is in
// the reviewed snapshot. It fails closed for unknown or malformed instance types.
func (l *ArchitectureLookup) ArchitectureForInstance(instance string) (Architecture, bool) {
	family, _, ok := strings.Cut(instance, ".")
	if !ok || family == "" || l == nil {
		return "", false
	}

	architecture, ok := l.families[family]

	return architecture, ok
}
