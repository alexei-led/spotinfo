package spot

import (
	"errors"
	"testing"
	"time"

	"github.com/bluele/gcache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// newTestScoreCache builds a scoreCache around a caller-supplied provider,
// bypassing newScoreCache's AWS lookup. A nil provider models "AWS unavailable".
func newTestScoreCache(provider awsAPIProvider) *scoreCache {
	return &scoreCache{
		cache:    gcache.New(defaultCacheSize).LRU().Expiration(defaultCacheExpiration).Build(),
		limiter:  rate.NewLimiter(rate.Every(time.Nanosecond), defaultRateLimitBurst),
		provider: provider,
	}
}

// The whole point of removing the synthetic-score fallback: when AWS is
// unreachable the caller must be told, never handed an invented number.
func TestGetSpotPlacementScores_NoProviderReportsUnavailable(t *testing.T) {
	t.Parallel()

	sc := newTestScoreCache(nil)

	scores, err := sc.getSpotPlacementScores(t.Context(), testRegionUSEast1, []string{testInstanceT2Micro}, false)

	require.ErrorIs(t, err, errScoreProviderUnavailable)
	assert.Nil(t, scores, "no scores may be returned when the provider is unavailable")
}

func TestEnrichWithScores_NoProviderFailsLoudly(t *testing.T) {
	t.Parallel()

	sc := newTestScoreCache(nil)
	advices := []Advice{{Region: testRegionUSEast1, Instance: testInstanceT2Micro}}

	err := sc.enrichWithScores(t.Context(), advices, false, time.Second)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unavailable")
	assert.Nil(t, advices[0].RegionScore, "advice must not gain a fabricated score")
}

func TestEnrichWithScores(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		singleAZ   bool
		fetch      []placementScore
		fetchErr   error
		wantErr    bool
		wantRegion *int
		wantZone   map[string]int
	}{
		{
			name:       "region level score applied",
			fetch:      []placementScore{{placement: testRegionUSEast1, score: 8}},
			wantRegion: new(8),
		},
		{
			name:     "az level score applied",
			singleAZ: true,
			fetch:    []placementScore{{placement: testRegionUSEast1 + "a", score: 6}},
			wantZone: map[string]int{testRegionUSEast1 + "a": 6},
		},
		{
			name:     "provider error surfaces and leaves advice unscored",
			fetchErr: errors.New("access denied"),
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := newMockawsAPIProvider(t)
			provider.EXPECT().
				fetchScores(mock.Anything, testRegionUSEast1, []string{testInstanceT2Micro}, tc.singleAZ).
				Return(tc.fetch, tc.fetchErr).
				Once()

			sc := newTestScoreCache(provider)
			advices := []Advice{{Region: testRegionUSEast1, Instance: testInstanceT2Micro}}

			err := sc.enrichWithScores(t.Context(), advices, tc.singleAZ, time.Second)

			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, advices[0].RegionScore)
				assert.Nil(t, advices[0].ZoneScores)

				return
			}

			require.NoError(t, err)

			if tc.wantRegion != nil {
				require.NotNil(t, advices[0].RegionScore)
				assert.Equal(t, *tc.wantRegion, *advices[0].RegionScore)
			}

			if tc.wantZone != nil {
				assert.Equal(t, tc.wantZone, advices[0].ZoneScores)
			}

			assert.NotNil(t, advices[0].ScoreFetchedAt, "a scored advice records when it was fetched")
		})
	}
}

// A second call for the same region/types must be served from cache, so the
// provider is expected exactly Once() — the mock fails the test otherwise.
func TestGetSpotPlacementScores_CachesByKey(t *testing.T) {
	t.Parallel()

	provider := newMockawsAPIProvider(t)
	provider.EXPECT().
		fetchScores(mock.Anything, testRegionUSEast1, []string{testInstanceT2Micro}, false).
		Return([]placementScore{{placement: testRegionUSEast1, score: 4}}, nil).
		Once()

	sc := newTestScoreCache(provider)

	first, err := sc.getSpotPlacementScores(t.Context(), testRegionUSEast1, []string{testInstanceT2Micro}, false)
	require.NoError(t, err)

	second, err := sc.getSpotPlacementScores(t.Context(), testRegionUSEast1, []string{testInstanceT2Micro}, false)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestGetCacheKey(t *testing.T) {
	t.Parallel()

	sc := newTestScoreCache(nil)

	tests := []struct {
		name      string
		aTypes    []string
		aSingleAZ bool
		bTypes    []string
		bSingleAZ bool
		wantSame  bool
	}{
		{
			name:   "instance type order does not matter",
			aTypes: []string{"a.large", "b.large"}, bTypes: []string{"b.large", "a.large"},
			wantSame: true,
		},
		{
			name:   "az flag changes the key",
			aTypes: []string{"a.large"}, aSingleAZ: false,
			bTypes: []string{"a.large"}, bSingleAZ: true,
			wantSame: false,
		},
		{
			name:   "different instance sets differ",
			aTypes: []string{"a.large"}, bTypes: []string{"b.large"},
			wantSame: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a := sc.getCacheKey(testRegionUSEast1, tc.aTypes, tc.aSingleAZ)
			b := sc.getCacheKey(testRegionUSEast1, tc.bTypes, tc.bSingleAZ)

			if tc.wantSame {
				assert.Equal(t, a, b)
			} else {
				assert.NotEqual(t, a, b)
			}
		})
	}
}

func TestCachedScoreDataGetFreshness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		age  time.Duration
		want FreshnessLevel
	}{
		{name: "just fetched", age: 0, want: Fresh},
		{name: "just under five minutes", age: 4*time.Minute + 59*time.Second, want: Fresh},
		{name: "exactly five minutes", age: 5 * time.Minute, want: Recent},
		{name: "just under thirty minutes", age: 29 * time.Minute, want: Recent},
		{name: "exactly thirty minutes", age: 30 * time.Minute, want: Stale},
		{name: "hours old", age: 5 * time.Hour, want: Stale},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data := &CachedScoreData{FetchTime: time.Now().Add(-tc.age)}
			assert.Equal(t, tc.want, data.GetFreshness())
		})
	}
}

func TestCreateAPIProviderNeverFabricates(t *testing.T) {
	t.Parallel()

	// Whatever createAPIProvider returns, it must be a real AWS provider or nil.
	// A non-nil provider that is not *awsScoreProvider would mean synthetic
	// scores were reintroduced.
	provider := createAPIProvider()
	if provider == nil {
		return
	}

	_, ok := provider.(*awsScoreProvider)
	assert.True(t, ok, "createAPIProvider must return only *awsScoreProvider or nil, got %T", provider)
}

// Client.enrichWithScores delegates to its scoreProvider, lazily creating one
// when absent. Both branches matter: the lazy path is what production uses.
func TestClientEnrichWithScoresDelegates(t *testing.T) {
	t.Parallel()

	t.Run("delegates to the configured provider", func(t *testing.T) {
		t.Parallel()

		advices := []Advice{{Region: testRegionUSEast1, Instance: testInstanceT2Micro}}
		provider := newMockscoreProvider(t)
		provider.EXPECT().
			enrichWithScores(mock.Anything, advices, true, time.Second).
			Return(nil).
			Once()

		client := &Client{scoreProvider: provider}

		require.NoError(t, client.enrichWithScores(t.Context(), advices, true, time.Second))
	})

	t.Run("propagates provider errors", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("scores denied")
		provider := newMockscoreProvider(t)
		provider.EXPECT().
			enrichWithScores(mock.Anything, mock.Anything, false, time.Second).
			Return(wantErr).
			Once()

		client := &Client{scoreProvider: provider}

		require.ErrorIs(t, client.enrichWithScores(t.Context(), nil, false, time.Second), wantErr)
	})

	t.Run("creates a provider when none is set", func(t *testing.T) {
		t.Parallel()

		client := &Client{}

		// No advices, so nothing is fetched; this pins the lazy-initialisation
		// branch rather than any particular scoring outcome.
		require.NoError(t, client.enrichWithScores(t.Context(), nil, false, time.Second))
		assert.NotNil(t, client.scoreProvider, "a score provider must be created on demand")
	})
}

func TestSortAndFilterByScore(t *testing.T) {
	t.Parallel()

	t.Run("ByScore orders high first and sinks nil scores", func(t *testing.T) {
		t.Parallel()

		advices := []Advice{
			{Instance: "unscored"},
			{Instance: "low", RegionScore: new(2)},
			{Instance: "high", RegionScore: new(9)},
		}

		sortAdvices(advices, SortByScore, false)

		assert.Equal(t, "high", advices[0].Instance)
		assert.Equal(t, "low", advices[1].Instance)
		assert.Equal(t, "unscored", advices[2].Instance, "nil scores sort last")
	})

	t.Run("filterByMinScore drops unscored and below-threshold entries", func(t *testing.T) {
		t.Parallel()

		advices := []Advice{
			{Instance: "unscored"},
			{Instance: "low", RegionScore: new(3)},
			{Instance: "high", RegionScore: new(9)},
		}

		got := filterByMinScore(advices, 5)

		require.Len(t, got, 1)
		assert.Equal(t, "high", got[0].Instance)
	})
}
