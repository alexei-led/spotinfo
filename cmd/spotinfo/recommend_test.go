package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/spot"
)

func recommendTestApp(client spotClient, output io.Writer) *cli.App {
	return newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error { return execRecommendCmd(ctx, context.Background(), client, output) },
	)
}

func recommendArgs(extra ...string) []string {
	args := make([]string, 0, 8+len(extra))
	args = append(args, "spotinfo", "recommend", "--architecture", "x86_64", "--cpu", "2", "--memory", "8")
	return append(args, extra...)
}

func TestExecRecommendCmd_DefaultTableOutput(t *testing.T) {
	client := newMockspotClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
		Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
	}}, nil).Once()
	var output bytes.Buffer

	err := recommendTestApp(client, &output).Run(recommendArgs())
	require.NoError(t, err)
	assert.Contains(t, output.String(), "RANK")
	assert.Contains(t, output.String(), "m6i.large")
	assert.Contains(t, output.String(), "ARCHITECTURE_MATCH")
}

func TestExecRecommendCmd_ProducesVersionedJSONReport(t *testing.T) {
	client := newMockspotClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
		Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
	}}, nil).Once()
	var output bytes.Buffer

	err := recommendTestApp(client, &output).Run([]string{
		"spotinfo", "recommend", "--architecture", "x86_64", "--vcpu", "2", "--memory-gib", "8",
		"--os", "windows", "--region", "us-west-2", "--region", "us-east-1", "--workload", "ci",
		"--budget", "0.04", "--top", "2", "--output", "json",
	})
	require.NoError(t, err)

	var report recommendationReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	assert.Equal(t, recommendationSchemaVersion, report.SchemaVersion)
	assert.Equal(t, []string{"us-east-1", "us-west-2"}, report.Request.Regions)
	assert.Equal(t, "windows", report.Request.OS)
	assert.Equal(t, 2, report.Request.MinimumVCPU)
	assert.Equal(t, 8, report.Request.MinimumMemoryGiB)
	assert.Equal(t, spot.WorkloadCI, report.Request.Workload)
	require.NotNil(t, report.Request.MaximumUSDPerInstanceHour)
	assert.Equal(t, 0.04, *report.Request.MaximumUSDPerInstanceHour)
	assert.Equal(t, spot.RecommendationRankingPolicy(), report.RankingPolicy)
	require.Len(t, report.Recommendations, 1)
	assert.NotNil(t, report.Recommendations[0].RationaleCodes)
}

func TestNormalizeRecommendationRegions(t *testing.T) {
	for _, test := range []struct {
		name    string
		regions []string
		want    []string
		wantErr bool
	}{
		{name: "duplicate explicit regions", regions: []string{"us-west-2", "us-east-1", "us-east-1"}, want: []string{"us-east-1", "us-west-2"}},
		{name: "duplicate all", regions: []string{"all", "all"}, want: []string{"all"}},
		{name: "trimmed all", regions: []string{" all "}, want: []string{"all"}},
		{name: "trimmed duplicate explicit region", regions: []string{" us-east-1 ", "us-east-1"}, want: []string{"us-east-1"}},
		{name: "trimmed all mixed with explicit region", regions: []string{" all ", "us-east-1"}, wantErr: true},
		{name: "all mixed with explicit region", regions: []string{"all", "us-east-1"}, wantErr: true},
		{name: "empty region", regions: []string{""}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeRecommendationRegions(test.regions)
			if test.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, spot.ErrInvalidRecommendationInput))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestExecRecommendCmd_NormalizesDuplicateRegionsAndAppliesInstancePattern(t *testing.T) {
	client := newMockspotClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Run(func(_ context.Context, opts ...spot.GetSpotSavingsOption) {
		assert.Len(t, opts, 5, "instance filter must be included in candidate acquisition")
	}).Return([]spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
		Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
	}}, nil).Once()
	var output bytes.Buffer

	err := recommendTestApp(client, &output).Run(recommendArgs(
		"--instance", "^m6i\\.large$", "--region", " us-west-2 ", "--region", " us-east-1 ", "--region", "us-east-1", "--output", "json",
	))
	require.NoError(t, err)

	var report recommendationReport
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	assert.Equal(t, []string{"us-east-1", "us-west-2"}, report.Request.Regions)
	assert.Len(t, report.Recommendations, 1, "duplicate regions cannot duplicate recommendations")
}

func TestRecommendCommandResolvesSharedFlagsAcrossContextLineage(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want recommendationRequest
	}{
		{
			name: "root flags before recommend",
			args: []string{
				"spotinfo", "--output", "json", "--region", " us-west-2 ", "--os", "windows", "--cpu", "2", "--memory", "8",
				"recommend", "--architecture", "x86_64",
			},
			want: recommendationRequest{Regions: []string{"us-west-2"}, OS: "windows", MinimumVCPU: 2, MinimumMemoryGiB: 8},
		},
		{
			name: "command flags after recommend override root flags",
			args: []string{
				"spotinfo", "--output", "table", "--region", "us-east-1", "--os", "linux", "--cpu", "2", "--memory", "8",
				"recommend", "--architecture", "x86_64", "--output", "json", "--region", "us-west-2", "--os", "windows", "--vcpu", "4", "--memory-gib", "16",
			},
			want: recommendationRequest{Regions: []string{"us-west-2"}, OS: "windows", MinimumVCPU: 4, MinimumMemoryGiB: 16},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newMockspotClient(t)
			client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
				Region: "us-west-2", Instance: "m6i.xlarge", Price: 0.04, Savings: 72,
				Info: spot.TypeInfo{Cores: 4, RAM: 16}, Range: spot.Range{Label: "<5%", Max: 5},
			}}, nil).Once()
			var output bytes.Buffer

			err := recommendTestApp(client, &output).Run(test.args)
			require.NoError(t, err)

			var report recommendationReport
			require.NoError(t, json.Unmarshal(output.Bytes(), &report))
			assert.Equal(t, test.want.Regions, report.Request.Regions)
			assert.Equal(t, test.want.OS, report.Request.OS)
			assert.Equal(t, test.want.MinimumVCPU, report.Request.MinimumVCPU)
			assert.Equal(t, test.want.MinimumMemoryGiB, report.Request.MinimumMemoryGiB)
		})
	}
}

func TestRecommendCommandRejectsInvalidInputsBeforeFetching(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"missing architecture", []string{"spotinfo", "recommend", "--cpu", "2", "--memory", "8"}},
		{"missing cpu", []string{"spotinfo", "recommend", "--architecture", "arm64", "--memory", "8"}},
		{"missing memory", []string{"spotinfo", "recommend", "--architecture", "arm64", "--cpu", "2"}},
		{"zero cpu", recommendArgs("--cpu", "0")},
		{"zero memory", recommendArgs("--memory", "0")},
		{"invalid architecture", recommendArgs("--architecture", "other")},
		{"invalid os", recommendArgs("--os", "darwin")},
		{"invalid output", recommendArgs("--output", "csv")},
		{"invalid budget", recommendArgs("--budget", "0")},
		{"invalid top", recommendArgs("--top", "0")},
		{"mixed all and explicit regions", recommendArgs("--region", "all", "--region", "us-east-1")},
		{"empty region", recommendArgs("--region", "")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newMockspotClient(t)
			var output bytes.Buffer
			err := recommendTestApp(client, &output).Run(test.args)
			require.Error(t, err)
			if test.name != "missing architecture" && test.name != "missing cpu" && test.name != "missing memory" {
				assert.True(t, errors.Is(err, spot.ErrInvalidRecommendationInput))
			}
			assert.Empty(t, output.String())
		})
	}
}

func TestRecommendCommandNoCandidatesReturnsSentinelWithoutOutput(t *testing.T) {
	client := newMockspotClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{}, nil).Once()
	var output bytes.Buffer

	err := recommendTestApp(client, &output).Run(recommendArgs())
	require.Error(t, err)
	assert.True(t, errors.Is(err, spot.ErrNoRecommendationCandidates))
	assert.Empty(t, output.String())
}

func TestRecommendCommandClientFailureWrapsWithoutOutput(t *testing.T) {
	fetchErr := errors.New("candidate fetch failed")
	client := newMockspotClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return(nil, fetchErr).Once()
	var output bytes.Buffer

	err := recommendTestApp(client, &output).Run(recommendArgs())
	require.Error(t, err)
	assert.ErrorIs(t, err, fetchErr)
	assert.Contains(t, err.Error(), "failed to get recommendation candidates")
	assert.Empty(t, output.String())
}

type failingRecommendationWriter struct{ err error }

func (w failingRecommendationWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRecommendationOutputWriteFailuresAreReturned(t *testing.T) {
	writeErr := errors.New("write failed")

	t.Run("table", func(t *testing.T) {
		err := writeRecommendationTable(nil, failingRecommendationWriter{err: writeErr})
		require.Error(t, err)
		assert.ErrorIs(t, err, writeErr)
	})

	t.Run("json", func(t *testing.T) {
		client := newMockspotClient(t)
		client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
			Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
			Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
		}}, nil).Once()

		err := recommendTestApp(client, failingRecommendationWriter{err: writeErr}).Run(recommendArgs("--output", "json"))
		require.Error(t, err)
		assert.ErrorIs(t, err, writeErr)
	})
}

func TestSpotinfoAppAssemblyPreservesRootInvocationAndRegistersRecommend(t *testing.T) {
	rootClient := newMockspotClient(t)
	rootClient.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
		Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
	}}, nil).Once()
	var output bytes.Buffer
	app := newSpotinfoApp(
		func(ctx *cli.Context) error { return execMainCmd(ctx, context.Background(), rootClient, &output) },
		func(*cli.Context) error { return nil },
	)

	require.Len(t, app.Commands, 1)
	assert.Equal(t, recommendCommandName, app.Commands[0].Name)
	err := app.Run([]string{"spotinfo", "--type", "m6i.*", "--output", "json"})
	require.NoError(t, err)
	assert.Contains(t, output.String(), "m6i.large")
}
