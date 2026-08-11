package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	"spotinfo/internal/providers"
)

// Placement figures are the one observation two clouds publish on two different
// scales: an AWS placement score is an integer 1-10, a GCP obtainability is a
// probability in 0.0-1.0. These tests hold the property the kind exists for —
// each is rendered, named and filtered as its own measurement, and neither is
// converted onto the other's numbers.

// obtainabilityCapabilities is a cloud that publishes a placement figure which
// is not an integer score. GCP will be one from task 12; the shape is declared
// here so the surface that has to refuse an integer floor can be tested before
// the fetcher exists.
func obtainabilityCapabilities() cloud.Capabilities {
	capabilities := offlineLinuxCapabilities()
	capabilities.PlacementScore = true
	capabilities.PlacementKind = cloud.PlacementKindObtainability
	capabilities.ZoneDetail = true

	return capabilities
}

// scoredCandidate attaches a regional AWS placement score.
func scoredCandidate(candidate cloud.Candidate, score int) cloud.Candidate {
	candidate.Placements = []cloud.PlacementObservation{{
		Location: candidate.Location,
		Kind:     cloud.PlacementKindPlacementScore,
		Score:    score,
	}}
	candidate.PlacementStatus = cloud.PlacementStatusAvailable

	return candidate
}

// obtainableCandidate attaches a regional GCP obtainability figure.
func obtainableCandidate(candidate cloud.Candidate, obtainability float64) cloud.Candidate {
	candidate.Placements = []cloud.PlacementObservation{{
		Obtainability: &obtainability,
		Location:      candidate.Location,
		Kind:          cloud.PlacementKindObtainability,
	}}
	candidate.PlacementStatus = cloud.PlacementStatusAvailable

	return candidate
}

// stubOf builds a fixed provider for one cloud.
func stubOf(id cloud.ProviderID, capabilities cloud.Capabilities, candidates ...cloud.Candidate) stubProvider {
	return stubProvider{id: id, capabilities: capabilities, result: neutralResult(id, candidates...)}
}

// renderListFrom drives `spotinfo list` against a fixed provider, the way the
// recorded contract does. `list` builds its AWS provider from the acquisition
// client rather than from the registry, so a stubbed AWS answer has to be
// handed to the renderer directly.
func renderListFrom(t *testing.T, provider cloud.Provider, args ...string) (string, error) {
	t.Helper()

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(ctx *cli.Context) error {
			sortKey, err := parseSortBy(ctx.String(flagSort))
			if err != nil {
				return err
			}

			return answerList(ctx, context.Background(), provider, sortKey, &output)
		},
		func(*cli.Context) error { return nil },
	)
	err := app.Run(append([]string{appName, listCommandName}, args...))

	return output.String(), err
}

// namesColumn matches a column name however the renderer cased it: go-pretty
// upper-cases a table header and leaves a CSV header alone.
func namesColumn(page, column string) bool {
	return strings.Contains(strings.ToUpper(page), strings.ToUpper(column))
}

// runRankedPage drives `spotinfo recommend` against a stub registry, supplying
// the three constraints every recommendation requires, so a ranked page can be
// rendered without an acquisition client.
func runRankedPage(t *testing.T, registry providerRegistry, args ...string) (string, error) {
	t.Helper()

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), registry, &output)
		},
	)
	err := app.Run(recommendArgs(args...))

	return output.String(), err
}

// TestEachPlacementKindRendersUnderItsOwnName is the rendering half of the kind
// vocabulary: every format names the measurement it is showing, and prints it
// on that measurement's own scale.
func TestEachPlacementKindRendersUnderItsOwnName(t *testing.T) {
	for _, test := range []struct {
		name         string
		capabilities cloud.Capabilities
		candidate    cloud.Candidate
		wantColumn   string
		wantCell     string
		wantJSON     map[string]any
	}{
		{
			name:         "aws publishes an integer placement score",
			capabilities: awsCapabilities(),
			candidate: scoredCandidate(
				neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.041600000", 2, 8), 8),
			wantColumn: scoreHeaderRegional,
			wantCell:   "8",
			wantJSON:   map[string]any{"region_score": float64(8)},
		},
		{
			name:         "gcp publishes an obtainability probability",
			capabilities: obtainabilityCapabilities(),
			candidate: obtainableCandidate(
				neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-4", "0.011000000", 4, 16), 0.87),
			wantColumn: obtainabilityHeaderRegional,
			wantCell:   "0.87",
			wantJSON:   map[string]any{"region_obtainability": 0.87},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := stubOf(test.candidate.Provider, test.capabilities, test.candidate)
			args := []string{"--with-score"}

			table, err := renderListFrom(t, provider, append(args, "--output", outputTable)...)
			require.NoError(t, err)
			assert.True(t, namesColumn(table, test.wantColumn), "the column must name the measurement it shows")
			assert.Contains(t, table, test.wantCell)

			csv, err := renderListFrom(t, provider, append(args, "--output", outputCSV)...)
			require.NoError(t, err)
			assert.Contains(t, csv, test.wantColumn, "csv keeps the reviewed column spelling")
			assert.Contains(t, csv, ","+test.wantCell, "csv carries the undecorated figure")

			document, err := renderListFrom(t, provider, append(args, "--output", outputJSON)...)
			require.NoError(t, err)

			var report struct {
				Candidates []map[string]any `json:"candidates"`
			}
			require.NoError(t, json.Unmarshal([]byte(document), &report))
			require.Len(t, report.Candidates, 1)

			for field, want := range test.wantJSON {
				assert.InDelta(t, want, report.Candidates[0][field], 1e-9,
					"%s is the field this kind publishes", field)
			}
			// And the other kind's field is absent, not zero: a probability of
			// zero and an unmeasured one are different answers.
			assert.NotContains(t, report.Candidates[0], otherPlacementField(test.wantJSON))
		})
	}
}

// otherPlacementField names the regional field of the kind a case does not
// publish.
func otherPlacementField(published map[string]any) string {
	if _, scored := published["region_score"]; scored {
		return "region_obtainability"
	}

	return "region_score"
}

// TestTheRankedPageRendersEachPlacementKind is the same property on the other
// command. `recommend` publishes no CSV, so table and JSON are its two formats.
func TestTheRankedPageRendersEachPlacementKind(t *testing.T) {
	for _, test := range []struct {
		name         string
		capabilities cloud.Capabilities
		candidate    cloud.Candidate
		wantColumn   string
		wantCell     string
		wantField    string
	}{
		{
			name:         "aws placement score",
			capabilities: awsCapabilities(),
			candidate: scoredCandidate(
				neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.041600000", 2, 8), 8),
			wantColumn: "PLACEMENT SCORE",
			wantCell:   "8",
			wantField:  "region_score",
		},
		{
			name:         "gcp obtainability",
			capabilities: obtainabilityCapabilities(),
			candidate: obtainableCandidate(
				neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-4", "0.011000000", 4, 16), 0.87),
			wantColumn: "OBTAINABILITY",
			wantCell:   "0.87",
			wantField:  "region_obtainability",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := listRegistry(test.candidate.Provider, test.capabilities, test.candidate)
			args := []string{"--cloud", string(test.candidate.Provider), "--with-score"}

			table, err := runRankedPage(t, registry, append(args, "--output", outputTable)...)
			require.NoError(t, err)
			assert.Contains(t, table, test.wantColumn)
			assert.Contains(t, table, test.wantCell)

			document, err := runRankedPage(t, registry, append(args, "--output", outputJSON)...)
			require.NoError(t, err)

			var report struct {
				Recommendations []map[string]any `json:"recommendations"`
			}
			require.NoError(t, json.Unmarshal([]byte(document), &report))
			require.Len(t, report.Recommendations, 1)
			assert.Contains(t, report.Recommendations[0], test.wantField)
		})
	}
}

// A recommendation that asks for no placement figure renders exactly the table
// it always has. The column is conditional for the same reason `list`'s is: an
// empty column is a claim that a lookup was made.
func TestARankedPageWithoutScoresHasNoPlacementColumn(t *testing.T) {
	registry := listRegistry(cloud.ProviderAWS, awsCapabilities(),
		scoredCandidate(neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.041600000", 2, 8), 8))

	table, err := runRankedPage(t, registry)
	require.NoError(t, err)
	assert.NotContains(t, table, "PLACEMENT SCORE")
	assert.Contains(t, table, "RISK          WHY", "the WHY column follows RISK directly when nothing was scored")
}

// capturingProvider records the query it was asked, which is how a flag is
// proved to act rather than merely to parse.
type capturingProvider struct {
	stubProvider
	query *cloud.Query
}

func (p *capturingProvider) Query(ctx context.Context, query *cloud.Query) (cloud.Result, error) {
	p.query = query

	return p.stubProvider.Query(ctx, query)
}

func capturingRegistry(t *testing.T, provider *capturingProvider) providerRegistry {
	t.Helper()

	return mustRegistry(providers.Registration{
		ID:    provider.id,
		Build: func() (cloud.Provider, error) { return provider, nil },
	})
}

// TestTheScoreFlagsReachARecommendationQuery is the acceptance criterion the
// plan states for this task: declaring the four flags on `recommend` is not
// enough, because a flag that is parsed and never mapped is accepted and
// silently ignored.
func TestTheScoreFlagsReachARecommendationQuery(t *testing.T) {
	provider := &capturingProvider{stubProvider: stubProvider{
		id:           cloud.ProviderAWS,
		capabilities: awsCapabilities(),
		result: neutralResult(cloud.ProviderAWS,
			neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.041600000", 2, 8)),
	}}

	_, err := runRankedPage(t, capturingRegistry(t, provider),
		"--with-score", "--az", "--min-score", "6", "--score-timeout", "12")
	require.NoError(t, err)

	require.NotNil(t, provider.query, "the provider must have been queried")
	assert.Equal(t, cloud.PlacementRequest{
		Timeout: 12 * time.Second, MinScore: 6, SingleZone: true, Enabled: true,
	}, provider.query.Placement, "every score flag must reach the acquisition query")
}

// The same four flags on `list` map to the same request. One vocabulary means
// --az cannot mean one thing on one command and another on the other.
func TestTheScoreFlagsReachAListQuery(t *testing.T) {
	provider := &capturingProvider{stubProvider: stubProvider{
		id:           cloud.ProviderAWS,
		capabilities: awsCapabilities(),
		result: neutralResult(cloud.ProviderAWS,
			neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.041600000", 2, 8)),
	}}

	_, err := renderListFrom(t, provider,
		"--with-score", "--az", "--min-score", "6", "--score-timeout", "12")
	require.NoError(t, err)

	require.NotNil(t, provider.query)
	assert.Equal(t, cloud.PlacementRequest{
		Timeout: 12 * time.Second, MinScore: 6, SingleZone: true, Enabled: true,
	}, provider.query.Placement)
}

// An integer floor over a probability needs a stated mapping, and no vendor
// states one — an AWS 8 is a bucket whose boundaries AWS does not publish, not
// the probability 0.8. The refusal names the cloud so a reader can tell this
// apart from "the flag is broken".
func TestAnIntegerScoreFloorIsRefusedAgainstAnotherMeasurement(t *testing.T) {
	candidate := obtainableCandidate(
		neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-4", "0.011000000", 4, 16), 0.87)

	for _, test := range []struct {
		name string
		run  func(providerRegistry) (string, error)
	}{
		{
			name: listCommandName,
			run: func(registry providerRegistry) (string, error) {
				return runListWith(t, registry, nil, "--cloud", "gcp", "--with-score", "--min-score", "6")
			},
		},
		{
			name: recommendCommandName,
			run: func(registry providerRegistry) (string, error) {
				return runRankedPage(t, registry, "--cloud", "gcp", "--with-score", "--min-score", "6")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := test.run(listRegistry(cloud.ProviderGCP, obtainabilityCapabilities(), candidate))
			require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
			assert.Contains(t, err.Error(), string(cloud.ProviderGCP), "the refusal must name the cloud")
			assert.Contains(t, err.Error(), string(cloud.PlacementKindObtainability),
				"and the measurement the floor cannot be applied to")
			assert.Empty(t, output, "a refusal writes nothing to stdout")
		})
	}
}

// --with-score itself still answers on that cloud: only the integer filter is
// meaningless there, and refusing the whole lookup would withhold a figure the
// vendor does publish.
func TestObtainabilityIsStillFetchedWithoutAFloor(t *testing.T) {
	candidate := obtainableCandidate(
		neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-4", "0.011000000", 4, 16), 0.87)

	output, err := runListWith(t, listRegistry(cloud.ProviderGCP, obtainabilityCapabilities(), candidate), nil,
		"--cloud", "gcp", "--with-score")
	require.NoError(t, err)
	assert.Contains(t, output, "0.87")
}

// The three companion flags need --with-score on `recommend` as well as on
// `list`. Declaring them on a command that never checks them is exactly how
// three silently ignored flags would have landed.
func TestTheScoreCompanionsAreRefusedOnRecommendToo(t *testing.T) {
	registry := listRegistry(cloud.ProviderAWS, awsCapabilities(),
		neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.041600000", 2, 8))

	for _, args := range [][]string{
		{"--az"},
		{"--min-score", "6"},
		{"--score-timeout", "12"},
	} {
		t.Run(args[0], func(t *testing.T) {
			output, err := runRankedPage(t, registry, args...)
			require.ErrorIs(t, err, cloud.ErrInvalidArgument)
			assert.Contains(t, err.Error(), "--"+flagWithScore)
			assert.Empty(t, output)
		})
	}
}

// --score-timeout is bounded on the CLI the way internal/mcp bounds
// score_timeout. It used to be dropped by `if timeout > 0` with no error, so a
// negative or absurd budget was accepted and quietly replaced by the default.
func TestTheScoreTimeoutIsBoundedOnBothCommands(t *testing.T) {
	candidate := neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.041600000", 2, 8)
	provider := stubOf(cloud.ProviderAWS, awsCapabilities(), candidate)
	registry := listRegistry(cloud.ProviderAWS, awsCapabilities(), candidate)

	for _, seconds := range []string{"0", "-5", "99999"} {
		t.Run(seconds, func(t *testing.T) {
			_, listErr := renderListFrom(t, provider, "--with-score", "--score-timeout", seconds)
			require.ErrorIs(t, listErr, cloud.ErrInvalidArgument)

			_, recommendErr := runRankedPage(t, registry, "--with-score", "--score-timeout", seconds)
			require.ErrorIs(t, recommendErr, cloud.ErrInvalidArgument)
		})
	}
}

// The ranked page can be ordered by its placement figure now that it publishes
// one — but only under --with-score, because without it every row's figure is
// absent and the flag would exit 0 having reordered nothing.
func TestSortingARankedPageByItsPlacementFigure(t *testing.T) {
	// Each kind is ordered on its own scale. The pairs are chosen so the
	// placement order contradicts the price order, which is what the canonical
	// ranking policy would otherwise impose.
	for _, test := range []struct {
		name         string
		capabilities cloud.Capabilities
		cheap, dear  cloud.Candidate
	}{
		{
			name:         "placement score",
			capabilities: awsCapabilities(),
			cheap: scoredCandidate(
				neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.041600000", 2, 8), 3),
			dear: scoredCandidate(
				neutralCandidate(cloud.ProviderAWS, "us-east-1", "m5.large", "0.050000000", 2, 8), 9),
		},
		{
			name:         "obtainability",
			capabilities: obtainabilityCapabilities(),
			cheap: obtainableCandidate(
				neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-4", "0.041600000", 2, 8), 0.4),
			dear: obtainableCandidate(
				neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-8", "0.050000000", 2, 8), 0.9),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := listRegistry(test.cheap.Provider, test.capabilities, test.cheap, test.dear)

			ordered, err := runRankedPage(t, registry, "--cloud", string(test.cheap.Provider),
				"--with-score", "--sort", sortScore, "--order", orderDesc)
			require.NoError(t, err)
			assert.Less(t,
				indexOfMachine(t, ordered, string(test.dear.Machine.ID)),
				indexOfMachine(t, ordered, string(test.cheap.Machine.ID)),
				"the higher placement figure comes first under a descending sort, "+
					"even though the ranking policy puts the cheaper machine first")
		})
	}
}

func indexOfMachine(t *testing.T, page, machine string) int {
	t.Helper()

	index := strings.Index(page, machine)
	require.GreaterOrEqual(t, index, 0, "%s must appear in the page", machine)

	return index
}

// Without --with-score the sort has nothing to order by, so it is refused
// rather than accepted and ignored.
func TestSortingByPlacementNeedsTheFiguresFetched(t *testing.T) {
	registry := listRegistry(cloud.ProviderAWS, awsCapabilities(),
		neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.041600000", 2, 8))

	output, err := runRankedPage(t, registry, "--sort", sortScore)
	require.ErrorIs(t, err, cloud.ErrInvalidArgument)
	assert.Contains(t, err.Error(), "--"+flagWithScore)
	assert.Empty(t, output)
}

// zonedCandidate attaches the figures --az asks for: one observation per zone
// and no regional one at all.
func zonedCandidate(candidate cloud.Candidate, zone string, score int) cloud.Candidate {
	location := candidate.Location
	location.Zone = zone

	candidate.Placements = []cloud.PlacementObservation{{
		Location: location,
		Kind:     cloud.PlacementKindPlacementScore,
		Score:    score,
	}}
	candidate.PlacementStatus = cloud.PlacementStatusAvailable

	return candidate
}

// --az is the second way to ask for an ordering that would order nothing: only
// the regional figure sorts a page, and a zone-detailed one publishes none. It
// is refused for the same reason the missing --with-score is, rather than
// exiting 0 with the ranking policy's order and a score column that contradicts
// it.
func TestSortingByPlacementIsRefusedUnderZoneDetail(t *testing.T) {
	registry := listRegistry(cloud.ProviderAWS, awsCapabilities(),
		zonedCandidate(neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.010000000", 2, 8),
			"us-east-1a", 2),
		zonedCandidate(neutralCandidate(cloud.ProviderAWS, "us-east-1", "m5.large", "0.050000000", 2, 8),
			"us-east-1b", 9),
	)

	output, err := runRankedPage(t, registry, "--with-score", "--az", "--sort", sortScore, "--order", orderDesc)
	require.ErrorIs(t, err, cloud.ErrInvalidArgument)
	assert.Contains(t, err.Error(), "--"+flagAZ, "the refusal must name the flag that leaves nothing to order")
	assert.Empty(t, output)
}

// unmeasuredPlacement is a candidate whose placement lookup ran and produced
// nothing — the shape a partly failed region sweep leaves behind.
func unmeasuredPlacement(candidate cloud.Candidate) cloud.Candidate {
	candidate.PlacementStatus = cloud.PlacementStatusUnavailable

	return candidate
}

func withSavings(candidate cloud.Candidate, percent int) cloud.Candidate {
	candidate.SavingsPercent = &percent

	return candidate
}

// A figure nobody published sorts last under either order. Descending used to
// be built by flipping the ascending comparison, which inverted the absent
// handling with it and opened a "best placement first" page with every row that
// was never measured — silence ranked above measurement, which is the one
// comparison the placement status exists to prevent.
func TestAFigureNobodyPublishedSortsLastUnderEitherOrder(t *testing.T) {
	unmeasured := neutralCandidate(cloud.ProviderAWS, "us-east-1", "unmeasured", "0.010000000", 2, 8)
	lower := neutralCandidate(cloud.ProviderAWS, "us-east-1", "lower-figure", "0.020000000", 2, 8)
	higher := neutralCandidate(cloud.ProviderAWS, "us-east-1", "higher-figure", "0.030000000", 2, 8)

	for _, test := range []struct {
		name       string
		args       []string
		candidates []cloud.Candidate
	}{
		{
			name: sortScore,
			args: []string{"--with-score", "--sort", sortScore},
			candidates: []cloud.Candidate{
				unmeasuredPlacement(unmeasured), scoredCandidate(lower, 3), scoredCandidate(higher, 9),
			},
		},
		{
			// The same helper orders --sort savings and --sort risk, so the
			// property is held on a second key rather than on the one path that
			// happened to expose it.
			name: sortSavings,
			args: []string{"--sort", sortSavings},
			candidates: []cloud.Candidate{
				unmeasured, withSavings(lower, 20), withSavings(higher, 70),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := listRegistry(cloud.ProviderAWS, awsCapabilities(), test.candidates...)

			for _, order := range []string{orderAsc, orderDesc} {
				page, err := runRankedPage(t, registry, append([]string{"--order", order}, test.args...)...)
				require.NoError(t, err)

				unmeasuredAt := indexOfMachine(t, page, "unmeasured")
				assert.Greater(t, unmeasuredAt, indexOfMachine(t, page, "lower-figure"),
					"%s order must not rank an unmeasured row above a measured one", order)
				assert.Greater(t, unmeasuredAt, indexOfMachine(t, page, "higher-figure"),
					"%s order must not rank an unmeasured row above a measured one", order)
			}
		})
	}
}

// A lookup that came back empty is not the same answer as a lookup nobody
// asked for, and Placements is a slice, so both are the empty slice without an
// explicit status. Every surface has to be able to tell them apart.
func TestAnEmptyLookupIsDistinguishableFromNoLookup(t *testing.T) {
	// A published risk figure, so "unavailable" in the rendered page can only
	// have come from the placement column.
	base := neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.041600000", 2, 8)
	base.Risk = advisorRisk("<5%", 0, 5)

	unavailable := base
	unavailable.PlacementStatus = cloud.PlacementStatusUnavailable

	t.Run("asked, and nothing came back", func(t *testing.T) {
		provider := stubOf(cloud.ProviderAWS, awsCapabilities(), unavailable)

		table, err := renderListFrom(t, provider, "--with-score", "--output", outputTable)
		require.NoError(t, err)
		assert.True(t, namesColumn(table, scoreColumn), "a lookup that came back empty still opens the column")
		assert.Contains(t, table, string(cloud.PlacementStatusUnavailable))

		document, err := renderListFrom(t, provider, "--with-score", "--output", outputJSON)
		require.NoError(t, err)
		assert.Contains(t, document, `"placement_status": "unavailable"`)
	})

	t.Run("never asked", func(t *testing.T) {
		provider := stubOf(cloud.ProviderAWS, awsCapabilities(), base)

		table, err := renderListFrom(t, provider, "--output", outputTable)
		require.NoError(t, err)
		assert.False(t, namesColumn(table, scoreColumn), "an unasked question draws no column")
		assert.NotContains(t, table, string(cloud.PlacementStatusUnavailable))

		document, err := renderListFrom(t, provider, "--output", outputJSON)
		require.NoError(t, err)
		assert.NotContains(t, document, "placement_status",
			"an unasked question publishes no status at all")
	})

	t.Run("asked, and answered", func(t *testing.T) {
		provider := stubOf(cloud.ProviderAWS, awsCapabilities(), scoredCandidate(base, 8))

		document, err := renderListFrom(t, provider, "--with-score", "--output", outputJSON)
		require.NoError(t, err)
		assert.NotContains(t, document, "placement_status",
			"a published figure is available by construction; restating it says nothing new")
		assert.Contains(t, document, `"region_score": 8`)
	})
}
