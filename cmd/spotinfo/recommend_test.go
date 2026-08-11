package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	"spotinfo/internal/providers"
	awsprovider "spotinfo/internal/providers/aws"
	"spotinfo/internal/spot"
)

// recommendTestApp drives `spotinfo recommend` against the production AWS
// provider built over a mocked acquisition client.
//
// The command reaches AWS through the registry like every other cloud now: the
// v1 path that held the client itself, and picked itself by workload, is gone.
// A stub provider would prove nothing about the mapping, so the real adapter is
// registered over the mock.
func recommendTestApp(client spotClient, output io.Writer) *cli.App {
	registry := mustRegistry(providers.Registration{
		ID: cloud.ProviderAWS,
		Build: func() (cloud.Provider, error) {
			lookup, err := spot.LoadEmbeddedArchitectureLookup()
			if err != nil {
				return nil, err
			}

			return awsprovider.New(client, lookup)
		},
	})

	return newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), registry, output)
		},
	)
}

func recommendArgs(extra ...string) []string {
	args := make([]string, 0, 8+len(extra))
	args = append(args, appName, recommendCommandName,
		"--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8")
	return append(args, extra...)
}

func TestExecRecommendCmd_DefaultTableOutput(t *testing.T) {
	client := newQueryClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
		Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
	}}, nil).Once()
	var output bytes.Buffer

	err := recommendTestApp(client, &output).Run(recommendArgs("--workload", "ci"))
	require.NoError(t, err)
	assert.Contains(t, output.String(), "RANK")
	assert.Contains(t, output.String(), "m6i.large")
	assert.Contains(t, output.String(), "ARCHITECTURE_MATCH")
}

func TestExecRecommendCmd_ProducesVersionedJSONReport(t *testing.T) {
	client := newQueryClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
		Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
	}}, nil).Once()
	var output bytes.Buffer

	err := recommendTestApp(client, &output).Run([]string{
		appName, recommendCommandName, "--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
		"--os", "windows", "--region", "us-west-2", "--region", "us-east-1", "--workload", "ci",
		"--max-price", "0.04", "--top", "2", "--output", "json",
	})
	require.NoError(t, err)

	var report cloud.RecommendReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	assert.Equal(t, cloud.SchemaVersionRecommendV3, report.SchemaVersion)
	assert.Equal(t, []cloud.Region{"us-east-1", "us-west-2"}, report.Request.Regions)
	assert.Equal(t, cloud.OSWindows, report.Request.OS)
	assert.Equal(t, 2, report.Request.MinVCPU)
	assert.InDelta(t, 8.0, report.Request.MinMemoryGiB, 0)
	assert.Equal(t, cloud.WorkloadCI, report.Request.Workload)
	require.NotNil(t, report.Request.MaxPrice)
	assert.InDelta(t, 0.04, *report.Request.MaxPrice, 1e-9)
	assert.Equal(t, cloud.RankingPolicy(), report.RankingPolicy)
	require.Len(t, report.Recommendations, 1)
	assert.NotNil(t, report.Recommendations[0].RationaleCodes)
}

// The command deduplicates and orders the regions it was given; refusing an
// empty region, or "all" mixed with an explicit one, is the neutral request
// validator's job and is covered by the rejection table below.
func TestNeutralRegionsDeduplicatesAndOrders(t *testing.T) {
	for _, test := range []struct {
		name    string
		regions []string
		want    []cloud.Region
	}{
		{name: "duplicate explicit regions", regions: []string{"us-west-2", "us-east-1", "us-east-1"}, want: []cloud.Region{"us-east-1", "us-west-2"}},
		{name: "duplicate all", regions: []string{"all", "all"}, want: []cloud.Region{"all"}},
		{name: "trimmed all", regions: []string{" all "}, want: []cloud.Region{"all"}},
		{name: "trimmed duplicate explicit region", regions: []string{" us-east-1 ", "us-east-1"}, want: []cloud.Region{"us-east-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, neutralRegions(test.regions))
		})
	}
}

func TestExecRecommendCmd_NormalizesDuplicateRegionsAndAppliesInstancePattern(t *testing.T) {
	client := newQueryClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Run(func(_ context.Context, opts ...spot.GetSpotSavingsOption) {
		assert.Len(t, opts, 6, "machine filter must be included in candidate acquisition")
	}).Return([]spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
		Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
	}}, nil).Once()
	var output bytes.Buffer

	err := recommendTestApp(client, &output).Run(recommendArgs(
		"--machine", "^m6i\\.large$", "--region", " us-west-2 ", "--region", " us-east-1 ", "--region", "us-east-1",
		"--workload", "ci", "--output", "json",
	))
	require.NoError(t, err)

	var report cloud.RecommendReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	assert.Equal(t, []cloud.Region{"us-east-1", "us-west-2"}, report.Request.Regions)
	assert.Len(t, report.Recommendations, 1, "duplicate regions cannot duplicate recommendations")
}

// Every question flag now lives on the command that answers it, so the values
// the report echoes are read from one place. The lineage helpers stay because
// --cloud and --os are declared on two sibling commands.
func TestRecommendCommandEchoesTheFlagsItWasGiven(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want cloud.RequestDTO
	}{
		{
			name: "trimmed region",
			args: []string{
				appName, recommendCommandName, "--architecture", "x86_64", "--output", "json",
				"--region", " us-west-2 ", "--os", "windows", "--min-vcpu", "2", "--min-memory-gib", "8",
				"--workload", "ci",
			},
			want: cloud.RequestDTO{Regions: []cloud.Region{"us-west-2"}, OS: cloud.OSWindows, MinVCPU: 2, MinMemoryGiB: 8},
		},
		{
			name: "larger floors",
			args: []string{
				appName, recommendCommandName, "--architecture", "x86_64", "--output", "json",
				"--region", "us-west-2", "--os", "windows", "--min-vcpu", "4", "--min-memory-gib", "16",
				"--workload", "ci",
			},
			want: cloud.RequestDTO{Regions: []cloud.Region{"us-west-2"}, OS: cloud.OSWindows, MinVCPU: 4, MinMemoryGiB: 16},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newQueryClient(t)
			client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
				Region: "us-west-2", Instance: "m6i.xlarge", Price: 0.04, Savings: 72,
				Info: spot.TypeInfo{Cores: 4, RAM: 16}, Range: spot.Range{Label: "<5%", Max: 5},
			}}, nil).Once()
			var output bytes.Buffer

			err := recommendTestApp(client, &output).Run(test.args)
			require.NoError(t, err)

			var report cloud.RecommendReport
			require.NoError(t, json.Unmarshal(output.Bytes(), &report))
			assert.Equal(t, test.want.Regions, report.Request.Regions)
			assert.Equal(t, test.want.OS, report.Request.OS)
			assert.Equal(t, test.want.MinVCPU, report.Request.MinVCPU)
			assert.InDelta(t, test.want.MinMemoryGiB, report.Request.MinMemoryGiB, 0)
		})
	}
}

func TestRecommendCommandRejectsInvalidInputsBeforeFetching(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"missing architecture", []string{appName, recommendCommandName, "--min-vcpu", "2", "--min-memory-gib", "8"}},
		{"missing vcpu", []string{appName, recommendCommandName, "--architecture", "arm64", "--min-memory-gib", "8"}},
		{"missing memory", []string{appName, recommendCommandName, "--architecture", "arm64", "--min-vcpu", "2"}},
		{"zero vcpu", recommendArgs("--min-vcpu", "0")},
		{"zero memory", recommendArgs("--min-memory-gib", "0")},
		{"invalid architecture", recommendArgs("--architecture", "other")},
		{"invalid os", recommendArgs("--os", "darwin")},
		{"invalid output", recommendArgs("--output", "csv")},
		{"invalid sort", recommendArgs("--sort", "bogus")},
		{"invalid order", recommendArgs("--order", "sideways")},
		{"invalid max-price", recommendArgs("--max-price", "0")},
		{"negative max-price", recommendArgs("--max-price", "-1")},
		// NaN and +Inf both survive a bare `ceiling <= 0`. Without the explicit
		// terms they reached a downstream converter instead of this guard, and
		// +Inf would otherwise be stored raw as a ceiling nothing can exceed.
		{"nan max-price", recommendArgs("--max-price", "NaN")},
		{"inf max-price", recommendArgs("--max-price", "Inf")},
		{"negative inf max-price", recommendArgs("--max-price", "-Inf")},
		{"invalid top", recommendArgs("--top", "0")},
		{"mixed all and explicit regions", recommendArgs("--region", "all", "--region", "us-east-1")},
		{"empty region", recommendArgs("--region", "")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newQueryClient(t)
			var output bytes.Buffer
			err := recommendTestApp(client, &output).Run(test.args)
			require.Error(t, err)
			assert.Empty(t, output.String())
		})
	}
}

func TestRecommendCommandNoCandidatesReturnsSentinelWithoutOutput(t *testing.T) {
	client := newQueryClient(t)
	// Twice: the ranked query, then the widened one that diagnoses the empty
	// set. This client answers from the embedded snapshot, which is the state
	// where that second query costs nothing — see diagnoseNoCandidates.
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{}, nil).Twice()
	var output bytes.Buffer

	err := recommendTestApp(client, &output).Run(recommendArgs("--workload", "ci"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, cloud.ErrNoCandidates))
	assert.Empty(t, output.String())
}

func TestRecommendCommandClientFailureWrapsWithoutOutput(t *testing.T) {
	fetchErr := errors.New("candidate fetch failed")
	client := newQueryClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return(nil, fetchErr).Once()
	var output bytes.Buffer

	err := recommendTestApp(client, &output).Run(recommendArgs("--workload", "ci"))
	require.Error(t, err)
	assert.ErrorIs(t, err, fetchErr)
	assert.Contains(t, err.Error(), "aws candidate acquisition")
	assert.Empty(t, output.String())
}

type failingRecommendationWriter struct{ err error }

func (w failingRecommendationWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRecommendationOutputWriteFailuresAreReturned(t *testing.T) {
	writeErr := errors.New("write failed")

	t.Run("table", func(t *testing.T) {
		err := writeNeutralRecommendationTable(nil, failingRecommendationWriter{err: writeErr})
		require.Error(t, err)
		assert.ErrorIs(t, err, writeErr)
	})

	t.Run("json", func(t *testing.T) {
		client := newQueryClient(t)
		client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
			Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
			Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
		}}, nil).Once()

		err := recommendTestApp(client, failingRecommendationWriter{err: writeErr}).
			Run(recommendArgs("--workload", "ci", "--output", "json"))
		require.Error(t, err)
		assert.ErrorIs(t, err, writeErr)
	})
}

func TestSpotinfoAppAssemblyRegistersBothCommands(t *testing.T) {
	listClient := newQueryClient(t)
	listClient.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
		Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
	}}, nil).Once()
	var output bytes.Buffer
	app := newSpotinfoApp(
		func(ctx *cli.Context) error {
			return execListCmd(ctx, context.Background(), awsOnlyRegistry(), listClient, &output)
		},
		func(*cli.Context) error { return nil },
	)

	names := make([]string, 0, len(app.Commands))
	for _, command := range app.Commands {
		names = append(names, command.Name)
	}
	assert.Equal(t, []string{listCommandName, recommendCommandName}, names)

	err := app.Run([]string{appName, listCommandName, "--machine", "m6i.*", "--output", "json"})
	require.NoError(t, err)
	assert.Contains(t, output.String(), "m6i.large")
}

// neutralCandidate is one offline Linux machine with no published risk — the
// shape a committed GCP or Azure snapshot has.
func neutralCandidate(id cloud.ProviderID, region, machine, price string, vcpu int, memoryGiB float64) cloud.Candidate {
	amount, err := cloud.ParseMoney(price)
	if err != nil {
		panic(err)
	}
	location := cloud.Location{Region: cloud.Region(region)}

	return cloud.Candidate{
		Provider: id,
		OS:       cloud.OSLinux,
		Location: location,
		Machine: cloud.MachineSpec{
			ID: cloud.MachineID(machine), Architecture: cloud.ArchitectureX8664,
			MemoryGiB: memoryGiB, VCPU: vcpu,
		},
		Spot: &cloud.PriceObservation{
			Location: location, Class: cloud.PriceClassSpot, Currency: cloud.CurrencyUSD,
			Unit: cloud.BillingUnitInstanceHour, Amount: amount,
		},
		Risk: cloud.UnavailableRisk(),
	}
}

func neutralResult(id cloud.ProviderID, candidates ...cloud.Candidate) cloud.Result {
	return cloud.Result{
		Provider: id,
		Mode:     cloud.DataModeEmbeddedSnapshot,
		Sources: []cloud.SourceRef{{
			FetchedAt:     time.Date(2026, time.August, 6, 8, 58, 27, 0, time.UTC),
			URL:           "https://example.invalid/catalog.json",
			ContentSHA256: "f42df66cd52c9dc3ac28b6bb7e525627696eec60692d5cf56658c679f0012393",
			ParserVersion: "test-parser/1",
			SchemaVersion: "test-feed/v1",
		}},
		Candidates: candidates,
	}
}

// A cloud other than AWS is answered by the neutral engine and the v3 schema.
func TestRecommendServesANonAWSCloud(t *testing.T) {
	registry := listRegistry(cloud.ProviderGCP, offlineLinuxCapabilities(),
		neutralCandidate(cloud.ProviderGCP, "europe-west1", "n2-standard-2", "0.021", 2, 8),
		neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-4", "0.011", 4, 16),
	)

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), registry, &output)
		},
	)
	require.NoError(t, app.Run(recommendArgs("--cloud", "gcp", "--region", "all", "--output", "json")))

	var report cloud.RecommendReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))

	assert.Equal(t, cloud.SchemaVersionRecommendV3, report.SchemaVersion)
	assert.Equal(t, cloud.ProviderGCP, report.Request.Cloud)
	assert.Equal(t, cloud.WorkloadCost, report.Request.Workload,
		"a cloud without risk data defaults to the risk-free cost policy")
	require.Len(t, report.Recommendations, 2)
	assert.Equal(t, cloud.MachineID("n2-standard-4"), report.Recommendations[0].Machine, "cheapest first")
	assert.Equal(t, cloud.RiskStatusUnavailable, report.Recommendations[0].Risk.Status)
	assert.Equal(t, cloud.RankingPolicy(), report.RankingPolicy)
}

// The cost policy is served by the neutral engine on every cloud, including
// AWS, which the retired v1 schema never offered.
func TestRecommendServesTheCostPolicyOnAWS(t *testing.T) {
	registry := mustRegistry(registrationOf(stubProvider{
		id:           cloud.ProviderAWS,
		capabilities: awsCapabilities(),
		result: neutralResult(cloud.ProviderAWS,
			neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.0416", 2, 8)),
	}))

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), registry, &output)
		},
	)
	require.NoError(t, app.Run(recommendArgs("--workload", "cost", "--output", "json")))

	var report cloud.RecommendReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	assert.Equal(t, cloud.SchemaVersionRecommendV3, report.SchemaVersion)
	assert.Equal(t, cloud.ProviderAWS, report.Request.Cloud)
	assert.Equal(t, cloud.WorkloadCost, report.Request.Workload)
	require.Len(t, report.Recommendations, 1)
	require.NotNil(t, report.Recommendations[0].SpotUSDPerHour)
	assert.Equal(t, "0.041600000", *report.Recommendations[0].SpotUSDPerHour)
}

// The table names the risk status rather than leaving a blank column that
// could read as "no interruptions".
func TestNeutralRecommendationTableNamesUnavailableRisk(t *testing.T) {
	registry := listRegistry(cloud.ProviderGCP, offlineLinuxCapabilities(),
		neutralCandidate(cloud.ProviderGCP, "europe-west1", "n2-standard-2", "0.021", 2, 8))

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), registry, &output)
		},
	)
	require.NoError(t, app.Run(recommendArgs("--cloud", "gcp", "--region", "all")))

	assert.Contains(t, output.String(), "RANK  CLOUD")
	assert.Contains(t, output.String(), "n2-standard-2")
	assert.Contains(t, output.String(), string(cloud.RiskStatusUnavailable))
	assert.Contains(t, output.String(), "COST_POLICY")
}

// AWS under a risk-capped workload was the one combination answered by
// spotinfo.recommend/v1. It is answered by v3 like everything else now, and the
// risk it filtered on is published in the same block every other cloud uses.
func TestRecommendServesEveryAWSWorkloadWithOneSchema(t *testing.T) {
	for _, workload := range []cloud.Workload{cloud.WorkloadCost, cloud.WorkloadWeb, cloud.WorkloadCI, cloud.WorkloadBatch} {
		t.Run(string(workload), func(t *testing.T) {
			client := newQueryClient(t)
			client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
				Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
				Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
			}}, nil).Once()

			var output bytes.Buffer
			require.NoError(t, recommendTestApp(client, &output).
				Run(recommendArgs("--workload", string(workload), "--output", "json")))

			var report cloud.RecommendReport
			require.NoError(t, json.Unmarshal(output.Bytes(), &report))
			assert.Equal(t, cloud.SchemaVersionRecommendV3, report.SchemaVersion)
			assert.Equal(t, workload, report.Request.Workload)
			require.Len(t, report.Recommendations, 1)
			assert.Equal(t, cloud.RiskStatusAvailable, report.Recommendations[0].Risk.Status)
		})
	}
}

// A risk-capped workload still needs a cloud that publishes risk, and that
// refusal happens before acquisition.
func TestRecommendRefusesARiskCappedWorkloadOnACloudWithoutRisk(t *testing.T) {
	registry := listRegistry(cloud.ProviderGCP, offlineLinuxCapabilities(),
		neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-4", "0.011", 4, 16))

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), registry, &output)
		},
	)

	err := app.Run(recommendArgs("--cloud", "gcp", "--region", "all", "--workload", "web"))
	require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
	assert.Empty(t, output.String())
}

// The default workload is cost on every cloud, including AWS. It used to be web
// wherever risk was published, which meant the same question returned a
// different document depending on which cloud answered it.
func TestRecommendDefaultsToTheCostPolicyOnAWS(t *testing.T) {
	registry := mustRegistry(registrationOf(stubProvider{
		id:           cloud.ProviderAWS,
		capabilities: awsCapabilities(),
		result: neutralResult(cloud.ProviderAWS,
			neutralCandidate(cloud.ProviderAWS, "us-east-1", "m6i.large", "0.0416", 2, 8)),
	}))

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), registry, &output)
		},
	)
	require.NoError(t, app.Run(recommendArgs("--output", "json")))

	var report cloud.RecommendReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	assert.Equal(t, cloud.SchemaVersionRecommendV3, report.SchemaVersion)
	assert.Equal(t, cloud.WorkloadCost, report.Request.Workload)
}

// --sort and --order reorder the ranked page without changing which candidates
// were selected: the rank each row carries is still its position under the
// published ranking policy.
func TestRecommendSortsTheRankedPageWithoutReselectingIt(t *testing.T) {
	registry := listRegistry(cloud.ProviderGCP, offlineLinuxCapabilities(),
		neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-4", "0.011", 4, 16),
		neutralCandidate(cloud.ProviderGCP, "europe-west1", "n2-standard-2", "0.021", 2, 8),
	)

	for _, test := range []struct {
		name    string
		args    []string
		machine cloud.MachineID
		rank    int
	}{
		{name: "policy order", args: nil, machine: "n2-standard-4", rank: 1},
		{name: "price ascending", args: []string{"--sort", sortPrice}, machine: "n2-standard-4", rank: 1},
		{name: "price descending", args: []string{"--sort", sortPrice, "--order", orderDesc},
			machine: "n2-standard-2", rank: 2},
		{name: "machine ascending", args: []string{"--sort", sortMachine}, machine: "n2-standard-2", rank: 2},
		{name: "region ascending", args: []string{"--sort", sortRegion}, machine: "n2-standard-2", rank: 2},
		{name: "risk keeps a stable order", args: []string{"--sort", sortRisk}, machine: "n2-standard-4", rank: 1},
		{name: "savings keeps a stable order", args: []string{"--sort", sortSavings}, machine: "n2-standard-4", rank: 1},
		{name: "order alone reverses the policy", args: []string{"--order", orderDesc},
			machine: "n2-standard-2", rank: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			app := newSpotinfoApp(
				func(*cli.Context) error { return nil },
				func(ctx *cli.Context) error {
					return execRecommendCmd(ctx, context.Background(), registry, &output)
				},
			)
			args := append([]string{"--cloud", "gcp", "--region", "all", "--output", "json"}, test.args...)
			require.NoError(t, app.Run(recommendArgs(args...)))

			var report cloud.RecommendReport
			require.NoError(t, json.Unmarshal(output.Bytes(), &report))
			require.Len(t, report.Recommendations, 2)
			assert.Equal(t, test.machine, report.Recommendations[0].Machine)
			assert.Equal(t, test.rank, report.Recommendations[0].Rank,
				"the rank is the ranking policy's position, not the print order")
		})
	}
}

// A placement score is not published on a recommendation, so sorting by one is
// refused rather than accepted and silently ignored.
func TestRecommendRefusesASortKeyItDoesNotPublish(t *testing.T) {
	registry := listRegistry(cloud.ProviderGCP, offlineLinuxCapabilities(),
		neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-4", "0.011", 4, 16))

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), registry, &output)
		},
	)

	err := app.Run(recommendArgs("--cloud", "gcp", "--region", "all", "--sort", sortScore))
	require.ErrorIs(t, err, cloud.ErrInvalidArgument)
	assert.Empty(t, output.String())
}

// --sort and --order used to be refused on AWS under a risk-capped workload,
// because the v1 report published a fixed ranking it could not honour. With one
// report the same flag on the same command means one thing on every cloud and
// every workload.
func TestRecommendSortsAnAWSRiskCappedPage(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		machine cloud.MachineID
		rank    int
	}{
		{name: "sort price ascending", args: []string{"--sort", sortPrice}, machine: "m6i.large", rank: 1},
		{name: "sort price descending", args: []string{"--sort", sortPrice, "--order", orderDesc},
			machine: "m6i.xlarge", rank: 2},
		{name: "order alone reverses the policy", args: []string{"--order", orderDesc},
			machine: "m6i.xlarge", rank: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newQueryClient(t)
			client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{
				{
					Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
					Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
				},
				{
					Region: "us-east-1", Instance: "m6i.xlarge", Price: 0.08, Savings: 70,
					Info: spot.TypeInfo{Cores: 4, RAM: 16}, Range: spot.Range{Label: "<5%", Max: 5},
				},
			}, nil).Once()

			var output bytes.Buffer
			args := append([]string{"--workload", "ci", "--output", "json"}, test.args...)
			require.NoError(t, recommendTestApp(client, &output).Run(recommendArgs(args...)))

			var report cloud.RecommendReport
			require.NoError(t, json.Unmarshal(output.Bytes(), &report))
			require.Len(t, report.Recommendations, 2)
			assert.Equal(t, test.machine, report.Recommendations[0].Machine)
			assert.Equal(t, test.rank, report.Recommendations[0].Rank,
				"the rank is the ranking policy's position, not the print order")
		})
	}
}

// An empty answer is reported as no candidates, never as an empty page.
func TestNeutralRecommendReportsNoCandidates(t *testing.T) {
	registry := listRegistry(cloud.ProviderGCP, offlineLinuxCapabilities())

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), registry, &output)
		},
	)

	err := app.Run(recommendArgs("--cloud", "gcp", "--region", "all", "--output", "json"))
	require.ErrorIs(t, err, cloud.ErrNoCandidates)
	assert.Empty(t, output.String())
}
