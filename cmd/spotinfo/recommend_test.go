package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/spot"
)

func recommendTestApp(client spotClient, output *bytes.Buffer) *cli.App {
	command := recommendCommand()
	command.Action = func(ctx *cli.Context) error {
		return execRecommendCmd(ctx, context.Background(), client, output)
	}
	return &cli.App{Commands: []*cli.Command{command}}
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
	assert.Equal(t, recommendationRankingPolicy(), report.RankingPolicy)
	require.Len(t, report.Recommendations, 1)
	assert.NotNil(t, report.Recommendations[0].RationaleCodes)
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

func TestRootInvocationStillWorks(t *testing.T) {
	client := newMockspotClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.04, Savings: 72,
		Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
	}}, nil).Once()
	var output bytes.Buffer
	app := createTestApp(func(ctx *cli.Context) error {
		return execMainCmd(ctx, context.Background(), client, &output)
	})

	err := app.Run([]string{"spotinfo", "--type", "m6i.*", "--output", "json"})
	require.NoError(t, err)
	assert.Contains(t, output.String(), "m6i.large")
}
