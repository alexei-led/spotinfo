package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	gcpprovider "spotinfo/internal/providers/gcp"
	"spotinfo/internal/spot"
)

// Each case below used to be accepted and answered. A rejected flag is visible;
// a silently ignored one produces a plausible answer to a question nobody asked,
// with an exit code of 0.
//
// Not parallel: these run a cli.App, and urfave/cli appends its package-level
// HelpFlag to every command it parses.
func TestRootRejectsInputItUsedToAnswerSilently(t *testing.T) {
	for name, test := range map[string]struct {
		args []string
		want string
	}{
		"unknown sort": {
			args: []string{"--sort", "bogus", "--type", "m5.large"},
			want: `unknown sort "bogus"`,
		},
		"unknown output format": {
			args: []string{"--output", "jsonn", "--type", "m5.large"},
			want: `unknown output format "jsonn"`,
		},
		"unknown order": {
			args: []string{"--order", "sideways", "--type", "m5.large"},
			want: `unknown order "sideways"`,
		},
		"negative cpu": {
			args: []string{"--cpu", "-5", "--type", "m5.large"},
			want: "cpu must be zero or a positive number",
		},
		"negative memory": {
			args: []string{"--memory", "-5", "--type", "m5.large"},
			want: "memory must be zero or a positive number",
		},
		// The filter compares against scores that are only fetched under
		// --with-score, so on its own it dropped every row and printed nothing.
		"min-score without with-score": {
			args: []string{"--min-score", "5", "--type", "m5.large"},
			want: "--min-score needs --with-score",
		},
		"min-score above the scale": {
			args: []string{"--min-score", "11", "--with-score", "--type", "m5.large"},
			want: "--min-score must be between 1 and 10",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := runRoot(t, awsOnlyRegistry(), test.args...)
			require.Error(t, err)
			assert.ErrorIs(t, err, cloud.ErrInvalidArgument)
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

// The valid vocabulary must still be accepted, or the check above would be
// indistinguishable from rejecting everything.
func TestRootAcceptsEveryDocumentedSortAndFormat(t *testing.T) {
	run := func(t *testing.T, args ...string) {
		t.Helper()

		client := newQueryClient(t)
		client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return(contractAdvices(), nil).Once()

		var output bytes.Buffer
		app := newSpotinfoApp(
			func(ctx *cli.Context) error {
				return execMainCmd(ctx, context.Background(), awsOnlyRegistry(), client, &output)
			},
			func(*cli.Context) error { return nil },
		)
		require.NoError(t, app.Run(append([]string{appName}, args...)))
		assert.NotEmpty(t, output.String())
	}

	for _, sortBy := range []string{sortType, sortInterruption, sortSavings, sortPrice, sortRegion, sortScore} {
		t.Run("sort/"+sortBy, func(t *testing.T) {
			run(t, "--sort", sortBy, "--type", "m5.large")
		})
	}

	for _, format := range outputFormats {
		t.Run("output/"+format, func(t *testing.T) {
			run(t, "--output", format, "--type", "m5.large")
		})
	}
}

// The recommend command needs three constraints, and used to report the two it
// did not have in the wire vocabulary of whichever schema would have answered —
// "min_memory_gib must be a positive number" for a caller who omitted --memory.
func TestRecommendNamesTheFlagsItRequires(t *testing.T) {
	for name, test := range map[string]struct {
		args []string
		want []string
	}{
		"no memory":   {args: []string{"--architecture", "x86_64", "--cpu", "4"}, want: []string{"--memory"}},
		"no cpu":      {args: []string{"--architecture", "x86_64", "--memory", "8"}, want: []string{"--cpu"}},
		"no anything": {args: nil, want: []string{"--architecture", "--cpu", "--memory"}},
	} {
		t.Run(name, func(t *testing.T) {
			err := runRecommend(t, awsOnlyRegistry(), append([]string{recommendCommandName}, test.args...)...)
			require.Error(t, err)
			assert.ErrorIs(t, err, cloud.ErrInvalidArgument)
			for _, flag := range test.want {
				assert.Contains(t, err.Error(), flag)
			}
		})
	}
}

// The root query command cannot serve a non-AWS cloud. Saying only that a
// capability is missing left the caller to conclude GCP publishes no spot
// prices, when the answer is one subcommand away.
func TestRootRefusalPointsAtTheRecommendCommand(t *testing.T) {
	err := runRoot(t, shippedRegistry(t), "--cloud", "gcp", "--type", "n2")
	require.Error(t, err)
	assert.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
	assert.Contains(t, err.Error(), "spotinfo recommend --cloud gcp")
}

// A price column that reads 0.042496000 beside AWS's 0.0502 is the wire format
// leaking into a table a person reads. The JSON string keeps all nine decimals.
func TestHumanPriceUsesOneWidthForTheWholeColumn(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		amounts []string
		want    []string
	}{
		"trailing zeros dropped to a common width": {
			amounts: []string{"0.027894000", "0.028620000"},
			want:    []string{"0.027894", "0.028620"},
		},
		"never fewer than four decimals": {
			amounts: []string{"0.100000000", "1.000000000"},
			want:    []string{"0.1000", "1.0000"},
		},
		"widest row sets the width": {
			amounts: []string{"0.100000000", "0.123456789"},
			want:    []string{"0.100000000", "0.123456789"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			recommendations := make([]cloud.RecommendationDTO, len(test.amounts))
			for i, amount := range test.amounts {
				recommendations[i].SpotUSDPerHour = amount
			}

			decimals := priceDecimals(recommendations)
			for i, amount := range test.amounts {
				assert.Equal(t, test.want[i], humanPrice(amount, decimals))
			}
		})
	}
}

// Savings against on-demand is the number clouds are compared on, and the v2
// table used to drop it: GCP and Azure showed an absolute hourly price where the
// AWS table showed price and savings, even though savings_percent was in the
// JSON all along.
func TestNeutralRecommendationTableShowsSavings(t *testing.T) {
	t.Parallel()

	savings := 76.0
	onDemand := "0.181596000"

	var rendered strings.Builder
	require.NoError(t, writeNeutralRecommendationTable([]cloud.RecommendationDTO{
		{
			Rank: 1, Cloud: cloud.ProviderGCP, Region: "us-central1", Machine: "c3d-standard-4",
			Architecture: cloud.ArchitectureX8664, VCPU: 4, MemoryGiB: 16,
			SpotUSDPerHour: "0.042496000", OnDemandUSDPerHour: &onDemand, SavingsPercent: &savings,
			Risk: cloud.RiskDTO{Status: cloud.RiskStatusUnavailable},
		},
		// A provider with no on-demand price has not measured a discount of
		// nothing, so its cell must not read 0%.
		{
			Rank: 2, Cloud: cloud.ProviderGCP, Region: "us-central1", Machine: "n2d-standard-4",
			Architecture: cloud.ArchitectureX8664, VCPU: 4, MemoryGiB: 16,
			SpotUSDPerHour: "0.053824000",
			Risk:           cloud.RiskDTO{Status: cloud.RiskStatusUnavailable},
		},
	}, &rendered))

	lines := strings.Split(strings.TrimRight(rendered.String(), "\n"), "\n")
	require.Len(t, lines, 3)
	assert.Contains(t, lines[0], "SAVINGS")
	assert.Contains(t, lines[1], "76%")
	assert.NotContains(t, lines[2], "0%")
	assert.Contains(t, lines[2], "-")
}

// Both recommend tables size their columns from the rows. The v1 renderer used
// fixed widths that were narrower than real values, so one long region name
// shifted every column to its right.
func TestRecommendTablesAlignRowsWiderThanTheirHeaders(t *testing.T) {
	t.Parallel()

	var rendered strings.Builder
	require.NoError(t, writeRecommendationTable([]spot.Recommendation{
		{Region: "ap-southeast-3", Instance: "m7i-flex.xlarge", Architecture: "x86_64", VCPU: 4, MemoryGiB: 16},
		{Region: "ca-west-1", Instance: "t3.xlarge", Architecture: "x86_64", VCPU: 4, MemoryGiB: 16},
	}, &rendered))

	lines := strings.Split(strings.TrimRight(rendered.String(), "\n"), "\n")
	require.Len(t, lines, 3)

	// Every column starts at the same offset on every line, which is the whole
	// point of a fixed-width table.
	header := strings.Index(lines[0], "ARCHITECTURE")
	require.Positive(t, header)
	for _, line := range lines[1:] {
		assert.Equal(t, header, strings.Index(line, "x86_64"), "row misaligned:\n%s", rendered.String())
	}
}

// --live-risk names one mechanism — the authenticated GCP preemption lookup —
// and must be refused identically on both report paths. It used to be checked
// only on the v2 path, so AWS under the default workload accepted it and
// silently ignored it while Azure errored on the same flag.
func TestLiveRiskIsRefusedOffGCPOnBothReportPaths(t *testing.T) {
	for name, args := range map[string][]string{
		"aws v1 path": {"--architecture", "x86_64", "--cpu", "2", "--memory", "8", "--workload", "web"},
		"aws v2 path": {"--architecture", "x86_64", "--cpu", "2", "--memory", "8", "--workload", "cost"},
	} {
		t.Run(name, func(t *testing.T) {
			err := runRecommend(t, awsOnlyRegistry(),
				append([]string{recommendCommandName, "--" + flagLiveRisk}, args...)...)
			require.Error(t, err)
			assert.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
			assert.Contains(t, err.Error(), "--"+flagLiveRisk)
		})
	}
}

// The project is never taken from ambient gcloud configuration, so asking for
// live risk without naming one has to fail rather than bill a guess.
func TestLiveRiskNeedsAnExplicitProject(t *testing.T) {
	t.Setenv(gcpProjectEnv, "")

	err := runRecommend(t, shippedRegistry(t), recommendCommandName,
		"--cloud", "gcp", "--architecture", "x86_64", "--cpu", "2", "--memory", "8",
		"--"+flagLiveRisk)
	require.Error(t, err)
	assert.ErrorIs(t, err, cloud.ErrInvalidArgument)
	assert.Contains(t, err.Error(), "--"+flagGCPProject)
}

// The project identifier is interpolated into the advice API path, so a value
// that is not one redirects the call. Refused before any request is made,
// because the alternative is a 404 reported as "no preemption history" for every
// machine on the page.
func TestLiveRiskRefusesAProjectThatIsNotOne(t *testing.T) {
	t.Setenv(gcpProjectEnv, "")

	for name, project := range map[string]string{
		"path traversal": "p/regions/us-central1/instances",
		"query string":   "proj?alt=media",
		"space":          "my project",
		"uppercase":      "MyProject",
	} {
		t.Run(name, func(t *testing.T) {
			err := runRecommend(t, shippedRegistry(t), recommendCommandName,
				"--cloud", "gcp", "--architecture", "x86_64", "--cpu", "2", "--memory", "8",
				"--"+flagLiveRisk, "--"+flagGCPProject, project)
			require.Error(t, err)
			assert.ErrorIs(t, err, cloud.ErrInvalidArgument)
			assert.ErrorIs(t, err, gcpprovider.ErrBadProject)
		})
	}
}

// The same rule has to apply to the environment variable, which is the path a
// shell that already exports GOOGLE_CLOUD_PROJECT takes.
func TestLiveRiskRefusesABadProjectFromTheEnvironment(t *testing.T) {
	t.Setenv(gcpProjectEnv, "p/regions/us-central1/instances")

	err := runRecommend(t, shippedRegistry(t), recommendCommandName,
		"--cloud", "gcp", "--architecture", "x86_64", "--cpu", "2", "--memory", "8",
		"--"+flagLiveRisk)
	require.Error(t, err)
	assert.ErrorIs(t, err, gcpprovider.ErrBadProject)
}
