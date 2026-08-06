package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
