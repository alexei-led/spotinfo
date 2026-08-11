package azure

import (
	"cmp"
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
	assert.True(t, capabilities.SupportsOS(cloud.OSWindows),
		"the Retail API prices a licence-bundled meter for most sizes, and the catalogue carries it")
}

// TestWindowsAndLinuxAreSeparatelyPricedRows drives the widened key through the
// committed snapshot: the same size answers both queries, at different prices,
// and each candidate reports the OS its own row priced rather than the one the
// question asked for.
func TestWindowsAndLinuxAreSeparatelyPricedRows(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	ctx := t.Context()

	byOS := make(map[cloud.OperatingSystem]map[cloud.MachineID]cloud.Money)

	for _, instanceOS := range []cloud.OperatingSystem{cloud.OSLinux, cloud.OSWindows} {
		result, err := provider.Query(ctx, &cloud.Query{
			OS:      instanceOS,
			Regions: []cloud.Region{provider.catalog.Regions[0].ID},
		})
		require.NoError(t, err)
		require.NotEmpty(t, result.Candidates)

		priced := make(map[cloud.MachineID]cloud.Money, len(result.Candidates))
		for i := range result.Candidates {
			candidate := &result.Candidates[i]
			assert.Equal(t, instanceOS, candidate.OS)
			priced[candidate.Machine.ID] = candidate.Spot.Amount
		}
		byOS[instanceOS] = priced
	}

	shared := 0

	for machine, windows := range byOS[cloud.OSWindows] {
		linux, both := byOS[cloud.OSLinux][machine]
		if !both {
			continue
		}
		shared++

		assert.NotEqual(t, linux.Nanos(), windows.Nanos(),
			"%s: a licence-bundled meter priced identically means the two rows collapsed", machine)
	}

	require.NotZero(t, shared, "most sizes are sold both ways, so the overlap cannot be empty")
}

// TestAnUnsetOperatingSystemAnswersWithEveryPricedRow pins the zero-value rule
// cloud.Query documents: an unset filter is inactive, so both operating systems
// answer rather than an accidental Linux default.
func TestAnUnsetOperatingSystemAnswersWithEveryPricedRow(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	region := provider.catalog.Regions[0].ID

	result, err := provider.Query(t.Context(), &cloud.Query{Regions: []cloud.Region{region}})
	require.NoError(t, err)

	seen := make(map[cloud.OperatingSystem]struct{})
	for i := range result.Candidates {
		seen[result.Candidates[i].OS] = struct{}{}
	}

	assert.Len(t, seen, 2)
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

		// A name Azure does not publish, deliberately. This used to be a real
		// region the snapshot merely did not carry, and it broke the day that
		// region was added — testing the roster instead of the behaviour.
		result, err := provider.Query(ctx, &cloud.Query{OS: cloud.OSLinux, Regions: []cloud.Region{"nowhere-north-1"}})

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

		// The size comes from the snapshot: the catalogue is refreshed weekly by
		// PR, and a hardcoded name fails the day Azure stops selling it. Every
		// catalogued machine is priced somewhere, so the answer cannot be empty.
		machine := provider.catalog.Machines[0].ID

		result, err := provider.Query(ctx, &cloud.Query{
			OS:             cloud.OSLinux,
			MachinePattern: "^" + regexp.QuoteMeta(string(machine)) + "$",
			Regions:        []cloud.Region{cloud.RegionAll},
		})
		require.NoError(t, err)
		require.NotEmpty(t, result.Candidates)

		for i := range result.Candidates {
			assert.Equal(t, machine, result.Candidates[i].Machine.ID)
		}
	})

	t.Run("budget", func(t *testing.T) {
		t.Parallel()

		// The ceiling is the snapshot's own median spot price, so candidates sit
		// on both sides of it whatever the weekly refresh does to the numbers.
		unfiltered, err := provider.Query(ctx, &cloud.Query{OS: cloud.OSLinux, Regions: []cloud.Region{cloud.RegionAll}})
		require.NoError(t, err)
		ceiling := lowerMedianSpotPrice(unfiltered.Candidates)

		result, err := provider.Query(ctx, &cloud.Query{
			OS: cloud.OSLinux, MaxPrice: &ceiling, Regions: []cloud.Region{cloud.RegionAll},
		})
		require.NoError(t, err)
		require.NotEmpty(t, result.Candidates)
		require.Less(t, len(result.Candidates), len(unfiltered.Candidates),
			"a ceiling that excludes nothing proves nothing")

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
		"unknown os":           {OS: "plan9"},
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
		cloud.SortByRegion: func(left, right *cloud.Candidate) bool {
			return left.Location.Region < right.Location.Region
		},
	} {
		t.Run(string(key), func(t *testing.T) {
			t.Parallel()

			ascending := queryCandidates(t, provider, &cloud.Query{
				OS:      cloud.OSLinux,
				Regions: []cloud.Region{cloud.RegionAll},
				Sort:    cloud.SortOrder{Key: key},
			})
			descending := queryCandidates(t, provider, &cloud.Query{
				OS:      cloud.OSLinux,
				Regions: []cloud.Region{cloud.RegionAll},
				Sort:    cloud.SortOrder{Key: key, Descending: true},
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

// TestSavingsAreDerivedFromThePairedListPrice checks the one number this
// provider computes rather than reads. The count is asserted because a
// regression to an always-nil savings figure would otherwise skip every
// assertion below and pass.
func TestSavingsAreDerivedFromThePairedListPrice(t *testing.T) {
	t.Parallel()

	result, err := newTestProvider(t).Query(t.Context(), &cloud.Query{
		OS: cloud.OSLinux, Regions: []cloud.Region{cloud.RegionAll},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Candidates)

	derived := 0

	for i := range result.Candidates {
		candidate := &result.Candidates[i]
		if candidate.SavingsPercent == nil {
			continue
		}
		derived++

		assert.Positive(t, *candidate.SavingsPercent)
		assert.LessOrEqual(t, *candidate.SavingsPercent, 100)
		assert.Less(t, candidate.Spot.Amount.Nanos(), candidate.OnDemand.Amount.Nanos())
	}

	require.NotZero(t, derived, "every catalogued price pairs spot against list, so savings must be derivable")
}

func queryCandidates(t *testing.T, provider *Provider, query *cloud.Query) []cloud.Candidate {
	t.Helper()

	result, err := provider.Query(t.Context(), query)
	require.NoError(t, err)

	return result.Candidates
}

func savingsOf(candidate *cloud.Candidate) int {
	if candidate.SavingsPercent == nil {
		return 0
	}

	return *candidate.SavingsPercent
}

// lowerMedianSpotPrice is a ceiling the snapshot itself supplies: at least one
// price is at or below it and at least one is above.
func lowerMedianSpotPrice(candidates []cloud.Candidate) cloud.Money {
	prices := make([]cloud.Money, 0, len(candidates))
	for i := range candidates {
		prices = append(prices, candidates[i].Spot.Amount)
	}
	slices.SortFunc(prices, func(left, right cloud.Money) int { return cmp.Compare(left.Nanos(), right.Nanos()) })
	distinct := slices.Compact(prices)

	return distinct[(len(distinct)-1)/2]
}
