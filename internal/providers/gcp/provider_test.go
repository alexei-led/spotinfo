package gcp

import (
	"context"
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

	assert.Equal(t, cloud.ProviderGCP, loaded.Contract.Provider)
	assert.Equal(t, ParserVersion, loaded.Manifest.ParserVersion)
	assert.Equal(t, snapshot.PayloadFormParsedCatalog, loaded.Manifest.Payload.Form)
	assert.Equal(t, cloud.OSLinux, loaded.Catalog.OS)
	assert.Contains(t, loaded.Contract.Support.Regions, loaded.Catalog.Region)
	assert.GreaterOrEqual(t, len(loaded.Catalog.Machines), loaded.Contract.Thresholds.MinMachines)

	for _, series := range loaded.Catalog.Series() {
		assert.Contains(t, loaded.Contract.Support.MachineSeries, series,
			"every committed series is enumerated in the approved contract")
	}
}

func TestEmbeddedSnapshotIsRejectedWhenThePayloadDoesNotMatchItsManifest(t *testing.T) {
	t.Parallel()

	tampered := append([]byte(nil), embeddedCatalog...)
	tampered[len(tampered)-1] ^= 0xFF

	_, err := loadSnapshot(embeddedContract, embeddedManifest, tampered)
	require.ErrorIs(t, err, snapshot.ErrHashMismatch)
}

func TestEmbeddedSnapshotIsRejectedWithoutAnApprovedContract(t *testing.T) {
	t.Parallel()

	_, err := loadSnapshot([]byte(`{"schema_version":"spotinfo.source-contract/v1","provider":"gcp","review_status":"pending"}`),
		embeddedManifest, embeddedCatalog)
	require.ErrorIs(t, err, snapshot.ErrUnapprovedSource)
}

func newTestProvider(t *testing.T) *Provider {
	t.Helper()

	provider, err := New()
	require.NoError(t, err)

	return provider
}

func TestProviderDeclaresOnlyWhatTheCommittedPagesPublish(t *testing.T) {
	t.Parallel()

	capabilities := newTestProvider(t).Capabilities()

	assert.True(t, capabilities.SupportsOS(cloud.OSLinux))
	assert.False(t, capabilities.SupportsOS(cloud.OSWindows))
	assert.True(t, capabilities.Has(cloud.CapabilitySpotPrice))
	assert.True(t, capabilities.Has(cloud.CapabilityOnDemandPrice))
	assert.True(t, capabilities.Has(cloud.CapabilityMachineSpec))
	assert.False(t, capabilities.Has(cloud.CapabilityRisk), "gcp publishes no anonymous preemption history")
	assert.False(t, capabilities.Has(cloud.CapabilityPlacementScore))
	assert.False(t, capabilities.Has(cloud.CapabilityZoneDetail))
	assert.False(t, capabilities.Has(cloud.CapabilityLiveEnrichment))
}

func TestQueryPublishesBothPricesAndAnExplicitlyUnavailableRisk(t *testing.T) {
	t.Parallel()

	result, err := newTestProvider(t).Query(context.Background(), &cloud.Query{
		OS:      cloud.OSLinux,
		Regions: []cloud.Region{cloud.RegionAll},
		Sort:    cloud.SortOrder{Key: cloud.SortByPrice},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Candidates)

	assert.Equal(t, cloud.ProviderGCP, result.Provider)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)
	assert.NotEmpty(t, result.Sources)

	candidate := result.Candidates[0]
	require.NotNil(t, candidate.Spot)
	require.NotNil(t, candidate.OnDemand)
	assert.Equal(t, cloud.PriceClassSpot, candidate.Spot.Class)
	assert.Equal(t, cloud.PriceClassOnDemand, candidate.OnDemand.Class)
	assert.False(t, candidate.Spot.Live)
	assert.Equal(t, cloud.RiskStatusUnavailable, candidate.Risk.Status)
	assert.Empty(t, candidate.Risk.Label)
	assert.Empty(t, candidate.Placements)

	for i := 1; i < len(result.Candidates); i++ {
		assert.LessOrEqual(t, result.Candidates[i-1].Spot.Amount.Nanos(), result.Candidates[i].Spot.Amount.Nanos())
	}
}

func TestQueryAppliesEveryNeutralFilter(t *testing.T) {
	t.Parallel()

	ceiling, err := cloud.ParseMoney("0.05")
	require.NoError(t, err)

	result, err := newTestProvider(t).Query(context.Background(), &cloud.Query{
		OS:             cloud.OSLinux,
		Architecture:   cloud.ArchitectureARM64,
		MachinePattern: `^c4a-`,
		MaxPrice:       &ceiling,
		MinVCPU:        2,
		MinMemoryGiB:   4,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Candidates)

	for _, candidate := range result.Candidates {
		assert.Equal(t, cloud.ArchitectureARM64, candidate.Machine.Architecture)
		assert.Regexp(t, `^c4a-`, string(candidate.Machine.ID))
		assert.GreaterOrEqual(t, candidate.Machine.VCPU, 2)
		assert.GreaterOrEqual(t, candidate.Machine.MemoryGiB, 4.0)
		assert.LessOrEqual(t, candidate.Spot.Amount.Nanos(), ceiling.Nanos())
	}
}

func TestQueryReturnsNothingForARegionTheSnapshotDoesNotCover(t *testing.T) {
	t.Parallel()

	result, err := newTestProvider(t).Query(context.Background(), &cloud.Query{
		OS:      cloud.OSLinux,
		Regions: []cloud.Region{"europe-west1"},
	})
	require.NoError(t, err, "an uncovered region is an empty answer, not a malformed request")
	assert.Empty(t, result.Candidates)
}

func TestQueryRejectsWhatTheProviderCannotAnswer(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)

	for name, query := range map[string]*cloud.Query{
		"windows":              {OS: cloud.OSWindows},
		"unknown os":           {OS: "plan9"},
		"unknown architecture": {OS: cloud.OSLinux, Architecture: "riscv64"},
		"invalid pattern":      {OS: cloud.OSLinux, MachinePattern: "("},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := provider.Query(context.Background(), query)
			require.ErrorIs(t, err, cloud.ErrInvalidArgument)
		})
	}

	_, err := provider.Query(context.Background(), nil)
	require.ErrorIs(t, err, cloud.ErrInvalidArgument)
}
