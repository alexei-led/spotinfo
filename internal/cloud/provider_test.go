package cloud

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// linuxSpotOnly is a deliberately narrow provider: Linux spot prices and
// machine specifications, no risk, score, zone detail, or live enrichment.
func linuxSpotOnly() Capabilities {
	return Capabilities{
		OperatingSystems: []OperatingSystem{OSLinux},
		Architectures:    []Architecture{ArchitectureX8664},
		SpotPrice:        true,
		MachineSpec:      true,
	}
}

func TestHasCoversEveryCapabilityAndRejectsUnknownOnes(t *testing.T) {
	t.Parallel()

	full := Capabilities{
		SpotPrice: true, OnDemandPrice: true, MachineSpec: true, Risk: true,
		PlacementScore: true, ZoneDetail: true, LiveEnrichment: true,
	}
	every := []Capability{
		CapabilitySpotPrice, CapabilityOnDemandPrice, CapabilityMachineSpec, CapabilityRisk,
		CapabilityPlacementScore, CapabilityZoneDetail, CapabilityLiveEnrichment,
	}

	for _, capability := range every {
		assert.True(t, full.Has(capability), "%s must be reported as supported", capability)
		assert.False(t, Capabilities{}.Has(capability), "%s must not be supported by default", capability)
	}
	assert.False(t, full.Has("teleportation"), "an unrecognised capability is never supported")
}

func TestRequireReportsTheFirstShortfallInAFixedOrder(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		request CapabilityRequest
		want    string
	}{
		{
			name:    "os is checked before architecture and capabilities",
			request: CapabilityRequest{OS: OSWindows, Architecture: "riscv64", Needed: []Capability{CapabilityRisk}},
			want:    "os windows",
		},
		{
			name:    "architecture is checked before capabilities",
			request: CapabilityRequest{OS: OSLinux, Architecture: ArchitectureARM64, Needed: []Capability{CapabilityRisk}},
			want:    "architecture arm64",
		},
		{
			name:    "capabilities are checked in the order given",
			request: CapabilityRequest{Needed: []Capability{CapabilityZoneDetail, CapabilityRisk}},
			want:    "zone_detail",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := linuxSpotOnly().Require(test.request)
			require.ErrorIs(t, err, ErrUnsupportedCapability)
			assert.Contains(t, err.Error(), test.want)
			assert.Equal(t, CodeUnsupportedCapability, CodeOf(err))
		})
	}
}

// Zero-valued fields are inactive, matching Query. An empty OS is not a request
// for an unnamed operating system.
func TestRequireTreatsZeroValuedFieldsAsUnrequested(t *testing.T) {
	t.Parallel()

	require.NoError(t, linuxSpotOnly().Require(CapabilityRequest{}))
	require.NoError(t, linuxSpotOnly().Require(CapabilityRequest{
		OS:           OSLinux,
		Architecture: ArchitectureX8664,
		Needed:       []Capability{CapabilitySpotPrice, CapabilityMachineSpec},
	}))
}
