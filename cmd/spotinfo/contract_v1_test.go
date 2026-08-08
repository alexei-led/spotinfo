package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/spot"
)

// The goldens in testdata record the AWS v1 CLI output contract before the
// provider-neutral seam exists. They are produced from fixed advice, never from
// the embedded feeds, so a weekly data refresh cannot rewrite the contract.
// Regenerate deliberately with UPDATE_GOLDEN=1 and review the diff.

func contractAdvices() []spot.Advice {
	regionScore := 8

	return []spot.Advice{
		{
			Region: "us-east-1", Instance: "m6i.large", InstanceType: "m6i.large",
			Range: spot.Range{Label: "<5%", Min: 0, Max: 5}, Savings: 72,
			Info: spot.TypeInfo{Cores: 2, RAM: 8}, Price: 0.0416,
		},
		{
			Region: "us-west-2", Instance: "m5.xlarge", InstanceType: "m5.xlarge",
			Range: spot.Range{Label: "5-10%", Min: 5, Max: 11}, Savings: 65,
			Info: spot.TypeInfo{Cores: 4, RAM: 16}, Price: 0.1234, LivePrice: true,
			RegionScore: &regionScore,
		},
		{
			Region: "eu-west-1", Instance: "t3.nano", InstanceType: "t3.nano",
			Range: spot.Range{Label: "10-15%", Min: 10, Max: 16}, Savings: 60,
			Info: spot.TypeInfo{Cores: 2, RAM: 0.5}, Price: 0.0016,
		},
	}
}

func TestAWSRootJSONMatchesRecordedV1Contract(t *testing.T) {
	client := newMockspotClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return(contractAdvices(), nil).Once()

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(ctx *cli.Context) error {
			return execMainCmd(ctx, context.Background(), awsOnlyRegistry(), client, &output)
		},
		func(*cli.Context) error { return nil },
	)

	require.NoError(t, app.Run([]string{"spotinfo", "--region", "all", "--output", "json"}))
	assertGolden(t, "aws-root-v1.json", output.Bytes())
}

func TestAWSRecommendJSONMatchesRecordedV1Contract(t *testing.T) {
	client := newMockspotClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return(contractAdvices(), nil).Once()

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), awsOnlyRegistry(), client, &output)
		},
	)

	require.NoError(t, app.Run([]string{
		"spotinfo", "recommend", "--architecture", "x86_64", "--cpu", "2", "--memory", "8",
		"--region", "us-east-1", "--region", "us-west-2", "--workload", "ci", "--output", "json",
	}))
	assertGolden(t, "aws-recommend-v1.json", output.Bytes())
}

func assertGolden(t *testing.T, name string, actual []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(path, actual, 0o600))
	}

	expected, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(expected), string(actual),
		"%s records the AWS v1 output contract; regenerate with UPDATE_GOLDEN=1 and review the diff", name)
}
