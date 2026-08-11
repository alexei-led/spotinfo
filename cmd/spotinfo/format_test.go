package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

func TestDetermineScoreHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info scoreTypeInfo
		want string
	}{
		{
			name: "no scores keeps the plain column",
			info: scoreTypeInfo{},
			want: scoreColumn,
		},
		{
			name: "az scores only",
			info: scoreTypeInfo{hasScores: true, hasAZScores: true},
			want: scoreHeaderAZ,
		},
		{
			name: "regional scores only",
			info: scoreTypeInfo{hasScores: true, hasRegionalScores: true},
			want: scoreHeaderRegional,
		},
		{
			name: "mixed az and regional falls back to the generic header",
			info: scoreTypeInfo{hasScores: true, hasAZScores: true, hasRegionalScores: true},
			want: scoreHeaderGeneric,
		},
		{
			name: "hasScores with neither kind set is still generic",
			info: scoreTypeInfo{hasScores: true},
			want: scoreHeaderGeneric,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, determineScoreHeader(tc.info))
		})
	}
}

// The score column reads placement observations, which carry their own location:
// a regional score has no zone, a zonal one does. The regional score wins when
// both are present, and a candidate with neither prints a dash rather than a
// zero, which would read as "no capacity" instead of "not asked for".
func TestScoreValueReadsPlacementObservations(t *testing.T) {
	t.Parallel()

	placement := func(zone string, score int) cloud.PlacementObservation {
		return cloud.PlacementObservation{
			Location: cloud.Location{Region: "us-east-1", Zone: zone},
			Score:    score,
		}
	}

	tests := []struct {
		name       string
		placements []cloud.PlacementObservation
		wantData   string
		wantShown  string
	}{
		{name: "no scores", wantData: "-", wantShown: "-"},
		{
			name:       "regional only",
			placements: []cloud.PlacementObservation{placement("", 8)},
			wantData:   "8", wantShown: "🟢 8",
		},
		{
			name:       "zonal only, in the order the provider published them",
			placements: []cloud.PlacementObservation{placement("us-east-1a", 6), placement("us-east-1b", 3)},
			wantData:   "us-east-1a:6,us-east-1b:3",
			wantShown:  "us-east-1a:🟡 6,us-east-1b:🔴 3",
		},
		{
			name:       "regional wins over zonal",
			placements: []cloud.PlacementObservation{placement("", 9), placement("us-east-1a", 2)},
			wantData:   "9", wantShown: "🟢 9",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			candidate := cloud.Candidate{Placements: tc.placements}
			assert.Equal(t, tc.wantData, getScoreDataValue(&candidate))
			assert.Equal(t, tc.wantShown, getScoreDisplayValue(&candidate))
		})
	}
}

// A candidate with more than one zonal score becomes one row per zone, each
// priced for its own zone when the provider published a zone price.
func TestExpandAZSplitsZonalScoresAndPrices(t *testing.T) {
	t.Parallel()

	zonePrice := func(zone, amount string) cloud.PriceObservation {
		money, err := cloud.ParseMoney(amount)
		require.NoError(t, err)

		return cloud.PriceObservation{
			Location: cloud.Location{Region: "us-east-1", Zone: zone},
			Class:    cloud.PriceClassSpot,
			Amount:   money,
		}
	}

	regionPrice := zonePrice("", "0.100000000")
	candidate := cloud.Candidate{
		Location: cloud.Location{Region: "us-east-1"},
		Machine:  cloud.MachineSpec{ID: "m5.large"},
		Spot:     &regionPrice,
		ZonePrices: []cloud.PriceObservation{
			zonePrice("us-east-1a", "0.090000000"),
			zonePrice("us-east-1b", "0.110000000"),
		},
		Placements: []cloud.PlacementObservation{
			{Location: cloud.Location{Region: "us-east-1"}, Score: 7},
			{Location: cloud.Location{Region: "us-east-1", Zone: "us-east-1a"}, Score: 6},
			{Location: cloud.Location{Region: "us-east-1", Zone: "us-east-1b"}, Score: 4},
		},
	}

	rows := expandAZ([]cloud.Candidate{candidate})
	require.Len(t, rows, 2)

	assert.Equal(t, "us-east-1a:6", getScoreDataValue(&rows[0]), "the regional score must not shadow the zone")
	assert.Equal(t, "us-east-1b:4", getScoreDataValue(&rows[1]))
	assert.Equal(t, "0.0900", candidatePriceCell(&rows[0]))
	assert.Equal(t, "0.1100", candidatePriceCell(&rows[1]))

	// A single zonal score is left alone, so the row keeps whatever the provider
	// published for the region.
	candidate.Placements = candidate.Placements[:2]
	assert.Len(t, expandAZ([]cloud.Candidate{candidate}), 1)
}

// The asterisk marks a score fetched long enough ago to be worth distrusting;
// the boundary is what matters, so it is pinned on both sides.
func TestAddFreshnessInfo(t *testing.T) {
	t.Parallel()

	ago := func(d time.Duration) *time.Time {
		ts := time.Now().Add(-d)

		return &ts
	}

	tests := []struct {
		name      string
		fetchedAt *time.Time
		want      string
	}{
		{name: "nil timestamp is left untouched", fetchedAt: nil, want: "7"},
		{name: "just fetched", fetchedAt: ago(0), want: "7"},
		{name: "just inside the staleness window", fetchedAt: ago(29 * time.Minute), want: "7"},
		{name: "just outside the staleness window", fetchedAt: ago(31 * time.Minute), want: "7*"},
		{name: "hours old", fetchedAt: ago(5 * time.Hour), want: "7*"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, addFreshnessInfo("7", tc.fetchedAt))
		})
	}
}
