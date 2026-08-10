package gcp

import (
	"cmp"
	"context"
	"regexp"
	"slices"
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

// TestQueryAppliesEveryNeutralFilter derives every filter value from the loaded
// snapshot. The catalogue is refreshed weekly by PR, so a hardcoded machine name
// or price ceiling eventually fails on a price rise rather than on a defect.
// Each case compares the filtered answer against the subset computed here, so a
// filter that stops being applied returns extra candidates and fails.
func TestQueryAppliesEveryNeutralFilter(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	all := queryCandidates(t, provider, &cloud.Query{OS: cloud.OSLinux})
	require.NotEmpty(t, all)

	architecture := all[0].Machine.Architecture
	series := SeriesOf(all[len(all)/2].Machine.ID)
	ceiling := lowerMedianPrice(all)
	minVCPU := upperMedian(valuesOf(all, func(c *cloud.Candidate) int { return c.Machine.VCPU }))
	minMemory := upperMedian(valuesOf(all, func(c *cloud.Candidate) float64 { return c.Machine.MemoryGiB }))

	for name, tc := range map[string]struct {
		query *cloud.Query
		keep  func(*cloud.Candidate) bool
	}{
		"architecture": {
			query: &cloud.Query{OS: cloud.OSLinux, Architecture: architecture},
			keep:  func(c *cloud.Candidate) bool { return c.Machine.Architecture == architecture },
		},
		"machine pattern": {
			query: &cloud.Query{OS: cloud.OSLinux, MachinePattern: `^` + regexp.QuoteMeta(series) + `-`},
			keep:  func(c *cloud.Candidate) bool { return SeriesOf(c.Machine.ID) == series },
		},
		"budget": {
			query: &cloud.Query{OS: cloud.OSLinux, MaxPrice: &ceiling},
			keep:  func(c *cloud.Candidate) bool { return c.Spot.Amount.Nanos() <= ceiling.Nanos() },
		},
		"minimum vcpu": {
			query: &cloud.Query{OS: cloud.OSLinux, MinVCPU: minVCPU},
			keep:  func(c *cloud.Candidate) bool { return c.Machine.VCPU >= minVCPU },
		},
		"minimum memory": {
			query: &cloud.Query{OS: cloud.OSLinux, MinMemoryGiB: minMemory},
			keep:  func(c *cloud.Candidate) bool { return c.Machine.MemoryGiB >= minMemory },
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			want := keptCandidates(all, tc.keep)
			require.NotEmpty(t, want)
			require.Less(t, len(want), len(all), "a filter that excludes nothing proves nothing")

			assert.Equal(t, machineIDs(want), machineIDs(queryCandidates(t, provider, tc.query)))
		})
	}
}

// TestQuerySortsDescendingForEveryKeyItServes proves the descending branch
// reverses the comparator instead of being dropped: an ascending answer would
// leave the last candidate on top.
func TestQuerySortsDescendingForEveryKeyItServes(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)

	for key, before := range map[cloud.SortKey]func(left, right *cloud.Candidate) bool{
		cloud.SortByPrice: func(left, right *cloud.Candidate) bool {
			return left.Spot.Amount.Nanos() < right.Spot.Amount.Nanos()
		},
		cloud.SortBySavings: func(left, right *cloud.Candidate) bool { return savingsOf(left) < savingsOf(right) },
		cloud.SortByMachine: func(left, right *cloud.Candidate) bool { return left.Machine.ID < right.Machine.ID },
	} {
		t.Run(string(key), func(t *testing.T) {
			t.Parallel()

			ascending := queryCandidates(t, provider, &cloud.Query{OS: cloud.OSLinux, Sort: cloud.SortOrder{Key: key}})
			descending := queryCandidates(t, provider, &cloud.Query{
				OS:   cloud.OSLinux,
				Sort: cloud.SortOrder{Key: key, Descending: true},
			})

			require.Len(t, descending, len(ascending))
			require.True(t, before(&ascending[0], &ascending[len(ascending)-1]),
				"the snapshot must hold two different values or the direction proves nothing")

			for i := 1; i < len(ascending); i++ {
				assert.False(t, before(&ascending[i], &ascending[i-1]), "ascending fell at %d", i)
				assert.False(t, before(&descending[i-1], &descending[i]), "descending rose at %d", i)
			}
		})
	}
}

// The snapshot covers one region, so a descending region sort has nothing to
// reverse. What it must still do is answer with the same candidates rather than
// drop or duplicate any.
func TestADescendingRegionSortLeavesASingleRegionSnapshotIntact(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)

	ascending := queryCandidates(t, provider, &cloud.Query{
		OS: cloud.OSLinux, Sort: cloud.SortOrder{Key: cloud.SortByRegion},
	})
	descending := queryCandidates(t, provider, &cloud.Query{
		OS: cloud.OSLinux, Sort: cloud.SortOrder{Key: cloud.SortByRegion, Descending: true},
	})

	require.NotEmpty(t, ascending)
	assert.Equal(t, ascending, descending)
}

func queryCandidates(t *testing.T, provider *Provider, query *cloud.Query) []cloud.Candidate {
	t.Helper()

	result, err := provider.Query(context.Background(), query)
	require.NoError(t, err)

	return result.Candidates
}

func keptCandidates(candidates []cloud.Candidate, keep func(*cloud.Candidate) bool) []cloud.Candidate {
	kept := make([]cloud.Candidate, 0, len(candidates))
	for i := range candidates {
		if keep(&candidates[i]) {
			kept = append(kept, candidates[i])
		}
	}

	return kept
}

func machineIDs(candidates []cloud.Candidate) []cloud.MachineID {
	ids := make([]cloud.MachineID, 0, len(candidates))
	for i := range candidates {
		ids = append(ids, candidates[i].Machine.ID)
	}

	return ids
}

func savingsOf(candidate *cloud.Candidate) int {
	if candidate.SavingsPercent == nil {
		return 0
	}

	return *candidate.SavingsPercent
}

func valuesOf[T cmp.Ordered](candidates []cloud.Candidate, of func(*cloud.Candidate) T) []T {
	values := make([]T, 0, len(candidates))
	for i := range candidates {
		values = append(values, of(&candidates[i]))
	}

	return values
}

// upperMedian is the filter value for a floor: at least one snapshot value sits
// below it and at least one meets it, whatever the refresh does to the numbers.
func upperMedian[T cmp.Ordered](values []T) T {
	slices.Sort(values)
	distinct := slices.Compact(values)

	return distinct[len(distinct)/2]
}

// lowerMedianPrice is the mirror image for a ceiling: at least one price is at
// or below it and at least one is above.
func lowerMedianPrice(candidates []cloud.Candidate) cloud.Money {
	prices := make([]cloud.Money, 0, len(candidates))
	for i := range candidates {
		prices = append(prices, candidates[i].Spot.Amount)
	}
	slices.SortFunc(prices, func(left, right cloud.Money) int { return cmp.Compare(left.Nanos(), right.Nanos()) })
	distinct := slices.Compact(prices)

	return distinct[(len(distinct)-1)/2]
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

// GCP publishes neither interruption risk nor placement scores. Asking it to
// order by either, or to score placements, used to come back as candidates with
// status ok — silently unsorted and unscored — because candidateComparator
// returned nil for those keys and Placement was never read.
func TestQueryRefusesRiskAndPlacementInsteadOfIgnoringThem(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)

	for name, query := range map[string]*cloud.Query{
		"sort by risk":            {OS: cloud.OSLinux, Sort: cloud.SortOrder{Key: cloud.SortByRisk}},
		"sort by placement score": {OS: cloud.OSLinux, Sort: cloud.SortOrder{Key: cloud.SortByPlacementScore}},
		"placement requested":     {OS: cloud.OSLinux, Placement: cloud.PlacementRequest{Enabled: true}},
		"minimum placement score": {OS: cloud.OSLinux, Placement: cloud.PlacementRequest{MinScore: 5}},
		"zone detail":             {OS: cloud.OSLinux, Placement: cloud.PlacementRequest{SingleZone: true}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := provider.Query(context.Background(), query)
			require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
		})
	}
}
