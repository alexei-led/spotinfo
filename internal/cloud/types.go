// Package cloud defines the provider-neutral domain shared by every spotinfo
// cloud provider: identifiers, machine specifications, fixed-point money, typed
// observations, and the query seam providers implement.
//
// This package deliberately depends on nothing but the standard library. It must
// not import a provider SDK, the legacy AWS package (internal/spot), the CLI, or
// the MCP server — internal/cloud/dependencies_test.go enforces that.
package cloud

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrInvalidArgument identifies a value that is not part of the neutral vocabulary.
var ErrInvalidArgument = errors.New("invalid argument")

// ProviderID names a supported cloud provider.
type ProviderID string

const (
	// ProviderAWS identifies Amazon Web Services.
	ProviderAWS ProviderID = "aws"
	// ProviderAzure identifies Microsoft Azure.
	ProviderAzure ProviderID = "azure"
	// ProviderGCP identifies Google Cloud Platform.
	ProviderGCP ProviderID = "gcp"
)

// ProviderIDs returns every recognised provider in stable lexical order.
func ProviderIDs() []ProviderID {
	return []ProviderID{ProviderAWS, ProviderAzure, ProviderGCP}
}

// ParseProviderID accepts only a recognised provider identifier. Unknown values
// fail rather than defaulting to AWS: a silent fallback would answer a GCP
// question with AWS prices.
func ParseProviderID(value string) (ProviderID, error) {
	provider := ProviderID(strings.ToLower(strings.TrimSpace(value)))
	if slices.Contains(ProviderIDs(), provider) {
		return provider, nil
	}

	return "", fmt.Errorf("%w: unknown cloud provider %q", ErrInvalidArgument, value)
}

// Region is a provider region identifier in that provider's own naming.
type Region string

// RegionAll is the query value selecting every region a provider publishes.
// It is a keyword, never an actual region.
const RegionAll Region = "all"

// MachineID is a provider machine type identifier: an EC2 instance type, a GCE
// machine type, or an Azure VM size.
type MachineID string

// Architecture identifies a machine processor architecture.
type Architecture string

const (
	// ArchitectureX8664 is the 64-bit x86 architecture.
	ArchitectureX8664 Architecture = "x86_64"
	// ArchitectureARM64 is the 64-bit Arm architecture.
	ArchitectureARM64 Architecture = "arm64"
)

// ParseArchitecture accepts only a reviewed architecture value.
func ParseArchitecture(value string) (Architecture, error) {
	switch architecture := Architecture(strings.TrimSpace(value)); architecture {
	case ArchitectureX8664, ArchitectureARM64:
		return architecture, nil
	default:
		return "", fmt.Errorf("%w: architecture must be %s or %s", ErrInvalidArgument, ArchitectureX8664, ArchitectureARM64)
	}
}

// OperatingSystem identifies the machine operating system a price applies to.
type OperatingSystem string

const (
	// OSLinux selects Linux prices.
	OSLinux OperatingSystem = "linux"
	// OSWindows selects Windows prices.
	OSWindows OperatingSystem = "windows"
)

// ParseOperatingSystem accepts only a supported operating system value.
func ParseOperatingSystem(value string) (OperatingSystem, error) {
	switch instanceOS := OperatingSystem(strings.ToLower(strings.TrimSpace(value))); instanceOS {
	case OSLinux, OSWindows:
		return instanceOS, nil
	default:
		return "", fmt.Errorf("%w: os must be %s or %s", ErrInvalidArgument, OSLinux, OSWindows)
	}
}

// MachineSpec describes a machine type. Architecture is empty when the provider
// cannot classify the machine from reviewed data; it is never guessed from the
// machine name.
type MachineSpec struct {
	ID           MachineID
	Architecture Architecture
	MemoryGiB    float64
	VCPU         int
}
