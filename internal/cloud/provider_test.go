package cloud

import (
	"context"
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

// The sort vocabulary is shared by both surfaces, so its words are pinned here
// rather than in either of them. "score" is the one word that is not its
// field's name; every other is the neutral key spelled out.
func TestParseSortKeyResolvesTheSharedVocabulary(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"machine", "price", "region", "risk", "savings", "score"}, SortKeyNames(),
		"the sort vocabulary is the plan's; adding a word is a vocabulary change")

	for word, want := range map[string]SortKey{
		"machine":     SortByMachine,
		"price":       SortByPrice,
		"region":      SortByRegion,
		"risk":        SortByRisk,
		"savings":     SortBySavings,
		sortWordScore: SortByPlacementScore,
		// An unset key is not an error: it leaves the order to the provider.
		"": "",
	} {
		key, err := ParseSortKey(word)
		require.NoError(t, err)
		assert.Equal(t, want, key)
	}

	// The retired CLI word, and the neutral field name behind "score". Neither
	// may be accepted: an unrecognised key used to fall through to the
	// interruption sort silently, with an exit code of 0.
	for _, word := range []string{"interruption", "placement_score", "SCORE"} {
		_, err := ParseSortKey(word)
		require.ErrorIs(t, err, ErrInvalidArgument, "%q must be refused", word)
	}
}

// regionStub answers a fixed result and records whether it was queried at all.
type regionStub struct {
	capabilities Capabilities
	result       Result
	queried      int
}

func (s *regionStub) ID() ProviderID             { return s.result.Provider }
func (s *regionStub) Capabilities() Capabilities { return s.capabilities }

func (s *regionStub) Query(_ context.Context, _ *Query) (Result, error) {
	s.queried++

	return s.result, nil
}

// RegionsOf derives the answer from one query rather than from a new interface
// method, so it must deduplicate and order what that query returns.
func TestRegionsOfDeduplicatesAndSortsWhatOneQueryReturns(t *testing.T) {
	t.Parallel()

	provider := &regionStub{
		capabilities: linuxSpotOnly(),
		result: Result{
			Provider: ProviderGCP,
			Mode:     DataModeEmbeddedSnapshot,
			Candidates: []Candidate{
				{Location: Location{Region: "us-central1"}},
				{Location: Location{Region: "europe-west1"}},
				{Location: Location{Region: "us-central1"}},
			},
		},
	}

	regions, result, err := RegionsOf(t.Context(), provider)
	require.NoError(t, err)
	assert.Equal(t, []Region{"europe-west1", "us-central1"}, regions)
	assert.Equal(t, ProviderGCP, result.Provider, "the caller gets the provenance of the same acquisition")
	assert.Equal(t, 1, provider.queried, "one query answers the whole question")
}

// An empty answer publishes [], never null: the slice is always allocated.
func TestRegionsOfPublishesAnEmptyListRatherThanNil(t *testing.T) {
	t.Parallel()

	provider := &regionStub{capabilities: linuxSpotOnly(), result: Result{Provider: ProviderAzure}}

	regions, _, err := RegionsOf(t.Context(), provider)
	require.NoError(t, err)
	assert.NotNil(t, regions)
	assert.Empty(t, regions)
}

// The capability check is inside the helper, so every caller gets Invariant 3
// from one place. A provider that cannot price Linux spot machines is refused
// before it is asked anything.
func TestRegionsOfChecksCapabilitiesBeforeAcquisition(t *testing.T) {
	t.Parallel()

	provider := &regionStub{capabilities: Capabilities{}, result: Result{Provider: ProviderAWS}}

	_, _, err := RegionsOf(t.Context(), provider)
	require.ErrorIs(t, err, ErrUnsupportedCapability)
	assert.Zero(t, provider.queried, "the capability check must run before acquisition")
}
