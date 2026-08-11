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
func TestListRejectsInputItUsedToAnswerSilently(t *testing.T) {
	for name, test := range map[string]struct {
		args []string
		want string
	}{
		"unknown sort": {
			args: []string{"--sort", "bogus", "--machine", "m5.large"},
			want: `unknown sort "bogus"`,
		},
		"unknown output format": {
			args: []string{"--output", "jsonn", "--machine", "m5.large"},
			want: `unknown output format "jsonn"`,
		},
		"unknown order": {
			args: []string{"--order", "sideways", "--machine", "m5.large"},
			want: `unknown order "sideways"`,
		},
		"negative vcpu floor": {
			args: []string{"--min-vcpu", "-5", "--machine", "m5.large"},
			want: "--min-vcpu must be zero or a positive number",
		},
		"negative memory floor": {
			args: []string{"--min-memory-gib", "-5", "--machine", "m5.large"},
			want: "--min-memory-gib must be zero or a positive number",
		},
		"unparsable architecture": {
			args: []string{"--architecture", "riscv64", "--machine", "m5.large"},
			want: "architecture must be x86_64 or arm64",
		},
		// The filter compares against scores that are only fetched under
		// --with-score, so on its own it dropped every row and printed nothing.
		"min-score without with-score": {
			args: []string{"--min-score", "5", "--machine", "m5.large"},
			want: "--min-score needs --with-score",
		},
		"min-score above the scale": {
			args: []string{"--min-score", "11", "--with-score", "--machine", "m5.large"},
			want: "--min-score must be between 1 and 10",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := runList(t, awsOnlyRegistry(), test.args...)
			require.Error(t, err)
			assert.ErrorIs(t, err, cloud.ErrInvalidArgument)
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

// The valid vocabulary must still be accepted, or the check above would be
// indistinguishable from rejecting everything.
func TestListAcceptsEveryDocumentedSortAndFormat(t *testing.T) {
	run := func(t *testing.T, args ...string) {
		t.Helper()

		client := newQueryClient(t)
		client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
			Region: "us-east-1", Instance: "m5.large", InstanceType: "m5.large",
			Range: spot.Range{Label: "<5%", Min: 0, Max: 5}, Savings: 59,
			Info: spot.TypeInfo{Cores: 2, RAM: 8}, Price: 0.0399,
		}}, nil).Once()

		var output bytes.Buffer
		app := newSpotinfoApp(
			func(ctx *cli.Context) error {
				return execListCmd(ctx, context.Background(), awsOnlyRegistry(), client, &output)
			},
			func(*cli.Context) error { return nil },
		)
		require.NoError(t, app.Run(append([]string{appName, listCommandName}, args...)))
		assert.NotEmpty(t, output.String())
	}

	// Every key in both orders: the order is what decides which end of the
	// column a caller reads first, and it was never covered.
	//
	// `score` carries --with-score because that is what makes the key usable at
	// all: the sort orders placement figures, and only --with-score fetches
	// them. Sorting by it without the fetch is refused, and
	// TestAScoreSortWithoutTheFetchThatFillsItIsRefusedOnList owns that half —
	// so this sweep still proves the whole vocabulary is accepted when asked
	// for properly, rather than quietly dropping the one key with a companion.
	for _, sortBy := range sortKeyNames() {
		for _, order := range []string{orderAsc, orderDesc} {
			t.Run("sort/"+sortBy+"/"+order, func(t *testing.T) {
				args := []string{"--sort", sortBy, "--order", order, "--machine", "m5.large"}
				if sortBy == sortScore {
					args = append(args, "--"+flagWithScore)
				}
				run(t, args...)
			})
		}
	}

	for _, format := range outputFormats {
		t.Run("output/"+format, func(t *testing.T) {
			run(t, "--output", format, "--machine", "m5.large")
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
		"no memory": {
			args: []string{"--architecture", "x86_64", "--min-vcpu", "4"},
			want: []string{"--min-memory-gib"},
		},
		"no vcpu": {
			args: []string{"--architecture", "x86_64", "--min-memory-gib", "8"},
			want: []string{"--min-vcpu"},
		},
		"no anything": {args: nil, want: []string{"--architecture", "--min-vcpu", "--min-memory-gib"}},
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

// list is cloud-neutral: the command that replaced the AWS-only query answers
// for a cloud that publishes no interruption figure, rather than sending the
// caller to a different command.
func TestListAnswersACloudWithNoRiskData(t *testing.T) {
	output, err := runListCapturing(t, shippedRegistry(t),
		"--cloud", "gcp", "--machine", "^n2-standard-4$", "--output", outputJSON)
	require.NoError(t, err)
	assert.Contains(t, output, "n2-standard-4")
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
				recommendations[i].SpotUSDPerHour = usdPerHour(amount)
			}

			decimals := priceDecimals(recommendations)
			for i, amount := range test.amounts {
				assert.Equal(t, test.want[i], humanPrice(usdPerHour(amount), decimals))
			}
		})
	}
}

// usdPerHour is the published nullable amount. `list` carries rows AWS has no
// spot price for, so the shared candidate block types the field as a pointer.
func usdPerHour(amount string) *string { return &amount }

// A machine the price feed omits has no price, not a price of zero. The column
// prints the same dash the savings column already prints for an unmeasured
// discount, and priceDecimals must not let an absent amount set the width.
func TestHumanPriceRendersAnAbsentAmountAsADash(t *testing.T) {
	t.Parallel()

	decimals := priceDecimals([]cloud.RecommendationDTO{
		{CandidateDTO: cloud.CandidateDTO{SpotUSDPerHour: nil}},
		{CandidateDTO: cloud.CandidateDTO{SpotUSDPerHour: usdPerHour("0.027894000")}},
	})

	assert.Equal(t, 6, decimals)
	assert.Equal(t, "-", humanPrice(nil, decimals))
	assert.Equal(t, "0.027894", humanPrice(usdPerHour("0.027894000"), decimals))
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
			Rank: 1,
			CandidateDTO: cloud.CandidateDTO{
				Cloud: cloud.ProviderGCP, Region: "us-central1", Machine: "c3d-standard-4",
				Architecture: cloud.ArchitectureX8664, VCPU: 4, MemoryGiB: 16,
				SpotUSDPerHour: usdPerHour("0.042496000"), OnDemandUSDPerHour: &onDemand, SavingsPercent: &savings,
				Risk: cloud.RiskDTO{Status: cloud.RiskStatusUnavailable},
			},
		},
		// A provider with no on-demand price has not measured a discount of
		// nothing, so its cell must not read 0%.
		{
			Rank: 2,
			CandidateDTO: cloud.CandidateDTO{
				Cloud: cloud.ProviderGCP, Region: "us-central1", Machine: "n2d-standard-4",
				Architecture: cloud.ArchitectureX8664, VCPU: 4, MemoryGiB: 16,
				SpotUSDPerHour: usdPerHour("0.053824000"),
				Risk:           cloud.RiskDTO{Status: cloud.RiskStatusUnavailable},
			},
		},
	}, &rendered))

	lines := strings.Split(strings.TrimRight(rendered.String(), "\n"), "\n")
	require.Len(t, lines, 3)
	assert.Contains(t, lines[0], "SAVINGS")
	assert.Contains(t, lines[1], "76%")
	assert.NotContains(t, lines[2], "0%")
	assert.Contains(t, lines[2], "-")
}

// The recommend table sizes its columns from the rows. The renderer it replaced
// used fixed widths that were narrower than real values, so one long region
// name shifted every column to its right.
func TestRecommendTableAlignsRowsWiderThanTheirHeaders(t *testing.T) {
	t.Parallel()

	var rendered strings.Builder
	require.NoError(t, writeNeutralRecommendationTable([]cloud.RecommendationDTO{
		{Rank: 1, CandidateDTO: cloud.CandidateDTO{
			Region: "ap-southeast-3", Machine: "m7i-flex.xlarge", Architecture: "x86_64",
			VCPU: 4, MemoryGiB: 16, SpotUSDPerHour: usdPerHour("0.100000000"),
		}},
		{Rank: 2, CandidateDTO: cloud.CandidateDTO{
			Region: "ca-west-1", Machine: "t3.xlarge", Architecture: "x86_64",
			VCPU: 4, MemoryGiB: 16, SpotUSDPerHour: usdPerHour("0.200000000"),
		}},
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
		"aws v1 path": {"--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8", "--workload", "web"},
		"aws v2 path": {"--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8", "--workload", "cost"},
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
		"--cloud", "gcp", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
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
				"--cloud", "gcp", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
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
		"--cloud", "gcp", "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
		"--"+flagLiveRisk)
	require.Error(t, err)
	assert.ErrorIs(t, err, gcpprovider.ErrBadProject)
}
