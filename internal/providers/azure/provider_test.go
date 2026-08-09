package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/snapshot"
)

// TestEmbeddedSnapshotPassesItsOwnGate is the data gate make verify-data runs:
// the committed contract, manifest and payload must agree with each other and
// with the parser this build ships.
func TestEmbeddedSnapshotPassesItsOwnGate(t *testing.T) {
	t.Parallel()

	loaded, err := LoadEmbeddedSnapshot()
	require.NoError(t, err)

	assert.Equal(t, cloud.ProviderAzure, loaded.Contract.Provider)
	assert.Equal(t, ParserVersion, loaded.Manifest.ParserVersion)
	assert.Equal(t, snapshot.PayloadFormParsedCatalog, loaded.Manifest.Payload.Form)
	assert.Equal(t, cloud.RiskStatusUnavailable, loaded.Contract.Support.RiskStatus,
		"an offline provider must not claim to publish risk")

	coverage := loaded.Catalog.Coverage()
	assert.GreaterOrEqual(t, coverage.Regions, loaded.Contract.Thresholds.MinRegions)
	assert.NotEmpty(t, loaded.Manifest.SourceRefs(), "a published answer must be able to name its sources")
}

// TestACorruptedSnapshotDisablesTheProvider proves the loader fails closed on
// each artifact rather than serving data nobody approved.
func TestACorruptedSnapshotDisablesTheProvider(t *testing.T) {
	t.Parallel()

	for name, corrupt := range map[string]func(contract, manifest, payload []byte) (a, b, c []byte){
		"unparseable contract": func(_, m, p []byte) ([]byte, []byte, []byte) {
			return []byte(`{"schema_version":"nope"}`), m, p
		},
		"unparseable manifest": func(c, _, p []byte) ([]byte, []byte, []byte) {
			return c, []byte(`{}`), p
		},
		"payload hash mismatch": func(c, m, p []byte) ([]byte, []byte, []byte) {
			return c, m, append(append([]byte{}, p...), 0)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			contract, manifest, payload := corrupt(embeddedContract, embeddedManifest, embeddedCatalog)

			_, err := loadSnapshot(contract, manifest, payload)
			assert.Error(t, err)
		})
	}
}

func newTestProvider(t *testing.T) *Provider {
	t.Helper()

	provider, err := New()
	require.NoError(t, err)

	return provider
}

func TestCapabilitiesNeverClaimRisk(t *testing.T) {
	t.Parallel()

	capabilities := newTestProvider(t).Capabilities()

	assert.True(t, capabilities.SpotPrice)
	assert.True(t, capabilities.OnDemandPrice)
	assert.True(t, capabilities.MachineSpec)
	assert.False(t, capabilities.Risk,
		"Azure publishes eviction rates only behind a subscription, so this provider has none")
	assert.False(t, capabilities.PlacementScore)
	assert.False(t, capabilities.ZoneDetail)
	assert.False(t, capabilities.LiveEnrichment)
	assert.True(t, capabilities.SupportsOS(cloud.OSLinux))
	assert.False(t, capabilities.SupportsOS(cloud.OSWindows))
}

func TestQueryServesEveryCoveredRegion(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)

	result, err := provider.Query(t.Context(), &cloud.Query{
		OS:      cloud.OSLinux,
		Regions: []cloud.Region{cloud.RegionAll},
	})
	require.NoError(t, err)

	assert.Equal(t, cloud.ProviderAzure, result.Provider)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)
	require.NotEmpty(t, result.Candidates)

	regions := make(map[cloud.Region]struct{})
	for i := range result.Candidates {
		candidate := &result.Candidates[i]
		regions[candidate.Location.Region] = struct{}{}

		require.NotNil(t, candidate.Spot)
		require.NotNil(t, candidate.OnDemand)
		assert.False(t, candidate.Spot.Amount.IsZero(), "an unknown price is absent, never zero")
		assert.Equal(t, cloud.RiskStatusUnavailable, candidate.Risk.Status)
		assert.Equal(t, cloud.OSLinux, candidate.OS)
	}

	assert.Len(t, regions, len(provider.catalog.Regions))
}

func TestQueryFiltersTheCommittedCatalogue(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	ctx := t.Context()

	t.Run("one region", func(t *testing.T) {
		t.Parallel()

		region := provider.catalog.Regions[0].ID

		result, err := provider.Query(ctx, &cloud.Query{OS: cloud.OSLinux, Regions: []cloud.Region{region}})
		require.NoError(t, err)
		require.NotEmpty(t, result.Candidates)

		for i := range result.Candidates {
			assert.Equal(t, region, result.Candidates[i].Location.Region)
		}
	})

	t.Run("uncovered region yields no candidates", func(t *testing.T) {
		t.Parallel()

		result, err := provider.Query(ctx, &cloud.Query{OS: cloud.OSLinux, Regions: []cloud.Region{"francecentral"}})

		require.NoError(t, err, "a region outside the snapshot is an empty answer, not a malformed request")
		assert.Empty(t, result.Candidates)
	})

	t.Run("architecture", func(t *testing.T) {
		t.Parallel()

		result, err := provider.Query(ctx, &cloud.Query{
			OS:           cloud.OSLinux,
			Architecture: cloud.ArchitectureARM64,
			Regions:      []cloud.Region{cloud.RegionAll},
		})
		require.NoError(t, err)
		require.NotEmpty(t, result.Candidates)

		for i := range result.Candidates {
			assert.Equal(t, cloud.ArchitectureARM64, result.Candidates[i].Machine.Architecture)
		}
	})

	t.Run("resource minimums", func(t *testing.T) {
		t.Parallel()

		result, err := provider.Query(ctx, &cloud.Query{
			OS: cloud.OSLinux, MinVCPU: 8, MinMemoryGiB: 32, Regions: []cloud.Region{cloud.RegionAll},
		})
		require.NoError(t, err)
		require.NotEmpty(t, result.Candidates)

		for i := range result.Candidates {
			assert.GreaterOrEqual(t, result.Candidates[i].Machine.VCPU, 8)
			assert.GreaterOrEqual(t, result.Candidates[i].Machine.MemoryGiB, 32.0)
		}
	})

	t.Run("machine pattern", func(t *testing.T) {
		t.Parallel()

		result, err := provider.Query(ctx, &cloud.Query{
			OS: cloud.OSLinux, MachinePattern: "^Standard_D2s_v5$", Regions: []cloud.Region{cloud.RegionAll},
		})
		require.NoError(t, err)
		require.NotEmpty(t, result.Candidates)

		for i := range result.Candidates {
			assert.Equal(t, cloud.MachineID("Standard_D2s_v5"), result.Candidates[i].Machine.ID)
		}
	})

	t.Run("budget", func(t *testing.T) {
		t.Parallel()

		ceiling, err := cloud.ParseMoney("0.02")
		require.NoError(t, err)

		result, err := provider.Query(ctx, &cloud.Query{
			OS: cloud.OSLinux, MaxPrice: &ceiling, Regions: []cloud.Region{cloud.RegionAll},
		})
		require.NoError(t, err)

		for i := range result.Candidates {
			assert.LessOrEqual(t, result.Candidates[i].Spot.Amount.Nanos(), ceiling.Nanos())
		}
	})
}

func TestQueryRejectsWhatTheSnapshotCannotAnswer(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	ctx := t.Context()

	for name, query := range map[string]*cloud.Query{
		"windows":              {OS: cloud.OSWindows},
		"unknown architecture": {OS: cloud.OSLinux, Architecture: "riscv64"},
		"bad pattern":          {OS: cloud.OSLinux, MachinePattern: "("},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := provider.Query(ctx, query)
			assert.ErrorIs(t, err, cloud.ErrInvalidArgument)
		})
	}

	_, err := provider.Query(ctx, nil)
	assert.ErrorIs(t, err, cloud.ErrInvalidArgument)
}

func TestQuerySortsByTheRequestedKey(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)

	result, err := provider.Query(t.Context(), &cloud.Query{
		OS:      cloud.OSLinux,
		Regions: []cloud.Region{cloud.RegionAll},
		Sort:    cloud.SortOrder{Key: cloud.SortByPrice},
	})
	require.NoError(t, err)
	require.Greater(t, len(result.Candidates), 1)

	for i := 1; i < len(result.Candidates); i++ {
		assert.LessOrEqual(t,
			result.Candidates[i-1].Spot.Amount.Nanos(),
			result.Candidates[i].Spot.Amount.Nanos())
	}
}

// TestSavingsAreDerivedFromThePairedListPrice checks the one number this
// provider computes rather than reads.
func TestSavingsAreDerivedFromThePairedListPrice(t *testing.T) {
	t.Parallel()

	result, err := newTestProvider(t).Query(t.Context(), &cloud.Query{
		OS: cloud.OSLinux, Regions: []cloud.Region{cloud.RegionAll},
	})
	require.NoError(t, err)

	for i := range result.Candidates {
		candidate := &result.Candidates[i]
		if candidate.SavingsPercent == nil {
			continue
		}

		assert.Positive(t, *candidate.SavingsPercent)
		assert.LessOrEqual(t, *candidate.SavingsPercent, 100)
		assert.Less(t, candidate.Spot.Amount.Nanos(), candidate.OnDemand.Amount.Nanos())
	}
}
