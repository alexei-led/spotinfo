package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jedib0t/go-pretty/v6/text"
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

// Every rendering is part of the contract, not just JSON. The interruption
// column exists only in the human formats, so pinning JSON alone would let it
// be renamed or dropped without a single test failing.
func TestAWSRootOutputMatchesRecordedV1Contract(t *testing.T) {
	text.DisableColors()
	t.Cleanup(text.EnableColors)

	for format, golden := range map[string]string{
		"json":   "aws-root-v1.json",
		"table":  "aws-root-v1.table.txt",
		"text":   "aws-root-v1.text.txt",
		"csv":    "aws-root-v1.csv",
		"number": "aws-root-v1.number.txt",
	} {
		t.Run(format, func(t *testing.T) {
			client := newMockspotClient(t)
			client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return(contractAdvices(), nil).Once()

			var output bytes.Buffer
			app := newSpotinfoApp(
				func(ctx *cli.Context) error {
					return execMainCmd(ctx, context.Background(), awsOnlyRegistry(), client, &output)
				},
				func(*cli.Context) error { return nil },
			)

			require.NoError(t, app.Run([]string{"spotinfo", "--region", "all", "--output", format}))
			assertGolden(t, golden, output.Bytes())
		})
	}
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

// assertGolden compares against a recorded contract file. A regeneration always
// fails the run: rewriting the file and then comparing it to itself would let a
// job that happens to set UPDATE_GOLDEN report a client-visible contract change
// as a pass.
func assertGolden(t *testing.T, name string, actual []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(path, actual, 0o600))
		t.Fatalf("%s regenerated; review the diff and re-run without UPDATE_GOLDEN", name)
	}

	expected, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(expected), string(actual),
		"%s records the AWS v1 output contract; regenerate with UPDATE_GOLDEN=1 and review the diff", name)
}
