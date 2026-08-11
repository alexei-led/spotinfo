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
	"spotinfo/internal/providers"
	"spotinfo/internal/spot"
)

// Not parallel: everything here runs a cli.App, and urfave/cli appends its
// package-level HelpFlag to every command it parses.

// listRegistry registers one enabled provider with the capabilities its cloud
// actually has.
func listRegistry(id cloud.ProviderID, capabilities cloud.Capabilities, candidates ...cloud.Candidate) *providers.Registry {
	return mustRegistry(registrationOf(stubProvider{
		id:           id,
		capabilities: capabilities,
		result:       neutralResult(id, candidates...),
	}))
}

// runListWith drives `spotinfo list` against a given registry and acquisition
// client. AWS is the one cloud whose provider is built from the client rather
// than resolved from the registry, so both have to be injectable.
func runListWith(t *testing.T, registry providerRegistry, client spotClient, args ...string) (string, error) {
	t.Helper()

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(ctx *cli.Context) error {
			return execListCmd(ctx, context.Background(), registry, client, &output)
		},
		func(*cli.Context) error { return nil },
	)
	err := app.Run(append([]string{appName, listCommandName}, args...))

	return output.String(), err
}

// awsListClient answers with one machine the Spot Advisor publishes an
// interruption bucket for.
func awsListClient(t *testing.T) spotClient {
	t.Helper()

	client := newQueryClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", InstanceType: "m6i.large",
		Range: spot.Range{Label: "<5%", Min: 0, Max: 5}, Savings: 72,
		Info: spot.TypeInfo{Cores: 2, RAM: 8}, Price: 0.0416,
	}}, nil).Once()

	return client
}

// list answers on every cloud. The command it replaced acquired AWS advice and
// told a caller who asked for another cloud to run a different one.
func TestListAnswersOnEveryCloud(t *testing.T) {
	for _, test := range []struct {
		build    func(*testing.T) (providerRegistry, spotClient)
		name     string
		id       cloud.ProviderID
		machine  string
		wantRisk string
	}{
		{
			name: "aws publishes an interruption bucket",
			id:   cloud.ProviderAWS, machine: "m6i.large", wantRisk: "<5%",
			build: func(t *testing.T) (providerRegistry, spotClient) {
				return awsOnlyRegistry(), awsListClient(t)
			},
		},
		{
			name: "gcp publishes none",
			id:   cloud.ProviderGCP, machine: "n2-standard-4",
			wantRisk: string(cloud.RiskStatusUnavailable),
			build: func(t *testing.T) (providerRegistry, spotClient) {
				return listRegistry(cloud.ProviderGCP, offlineLinuxCapabilities(),
						neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-4", "0.011", 4, 16)),
					newQueryClient(t)
			},
		},
		{
			name: "azure publishes none",
			id:   cloud.ProviderAzure, machine: "Standard_D4s_v5",
			wantRisk: string(cloud.RiskStatusUnavailable),
			build: func(t *testing.T) (providerRegistry, spotClient) {
				return listRegistry(cloud.ProviderAzure, offlineLinuxCapabilities(),
						neutralCandidate(cloud.ProviderAzure, "westeurope", "Standard_D4s_v5", "0.032", 4, 16)),
					newQueryClient(t)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, client := test.build(t)

			output, err := runListWith(t, registry, client, "--cloud", string(test.id), "--output", outputCSV)
			require.NoError(t, err)

			assert.Contains(t, output, test.machine)
			// The risk column carries the cloud's own answer: a published
			// figure, or the status saying there is none. Never a blank cell,
			// which reads as "no interruptions", and never a zero, which reads
			// as a measurement of none.
			assert.Contains(t, output, test.wantRisk)
		})
	}
}

// A cloud with no risk data must state its absence in every format a person or
// a script reads, not only in the one that happens to be tested.
func TestListRendersTheRiskStatusInEveryFormat(t *testing.T) {
	registry := listRegistry(cloud.ProviderGCP, offlineLinuxCapabilities(),
		neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-4", "0.011", 4, 16))

	for _, format := range []string{outputText, outputTable, outputCSV} {
		t.Run(format, func(t *testing.T) {
			output, err := runListCapturing(t, registry, "--cloud", "gcp", "--output", format)
			require.NoError(t, err)
			assert.Contains(t, output, string(cloud.RiskStatusUnavailable))
			assert.NotContains(t, output, "''", "an absent risk figure must not render as an empty label")
		})
	}
}

// candidateRisk is the one place the column is decided, so the three states it
// has to distinguish are pinned directly.
func TestCandidateRiskNeverRendersBlank(t *testing.T) {
	t.Parallel()

	published := cloud.Candidate{Risk: cloud.RiskObservation{Status: cloud.RiskStatusAvailable, Label: "5-10%"}}
	assert.Equal(t, "5-10%", candidateRisk(&published))

	unavailable := cloud.Candidate{Risk: cloud.UnavailableRisk()}
	assert.Equal(t, "unavailable", candidateRisk(&unavailable))

	// A provider that reports neither is still described rather than left
	// blank: an empty cell in a risk column reads as "no interruptions".
	assert.Equal(t, "unavailable", candidateRisk(&cloud.Candidate{}))
}

// number is list-only: one savings percent has no meaning for a ranked page, so
// recommend refuses the format rather than printing something else.
func TestTheNumberFormatIsListOnly(t *testing.T) {
	registry := listRegistry(cloud.ProviderGCP, offlineLinuxCapabilities(),
		neutralCandidate(cloud.ProviderGCP, "us-central1", "n2-standard-4", "0.011", 4, 16))

	output, err := runListCapturing(t, registry, "--cloud", "gcp", "--output", outputNumber)
	require.NoError(t, err)
	assert.Equal(t, "0\n", output, "an unpublished savings figure still prints a number")

	var recommendOutput bytes.Buffer
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), registry, &recommendOutput)
		},
	)
	err = app.Run(recommendArgs("--cloud", "gcp", "--region", "all", "--output", outputNumber))
	require.Error(t, err)
	assert.Empty(t, recommendOutput.String())
}

// --architecture is a declared list flag, so asking for one must read the AWS
// architecture snapshot rather than refuse the request for a capability the
// provider only declines when nobody asked.
func TestListFiltersAWSByArchitecture(t *testing.T) {
	client := newQueryClient(t)
	client.EXPECT().GetSpotSavings(mock.Anything, mock.Anything).Return([]spot.Advice{
		{
			Region: "us-east-1", Instance: "m6i.large", InstanceType: "m6i.large",
			Range: spot.Range{Label: "<5%", Min: 0, Max: 5}, Savings: 72,
			Info: spot.TypeInfo{Cores: 2, RAM: 8}, Price: 0.0416,
		},
		{
			Region: "us-east-1", Instance: "m6g.large", InstanceType: "m6g.large",
			Range: spot.Range{Label: "<5%", Min: 0, Max: 5}, Savings: 70,
			Info: spot.TypeInfo{Cores: 2, RAM: 8}, Price: 0.0308,
		},
	}, nil).Once()

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(ctx *cli.Context) error {
			return execListCmd(ctx, context.Background(), awsOnlyRegistry(), client, &output)
		},
		func(*cli.Context) error { return nil },
	)

	require.NoError(t, app.Run([]string{
		appName, listCommandName, "--architecture", "arm64", "--output", outputJSON,
	}))

	candidates := listedCandidates(t, output.Bytes())
	require.Len(t, candidates, 1, "only the Graviton machine is arm64")
	assert.Equal(t, cloud.MachineID("m6g.large"), candidates[0].Machine)
}

// The unified defaults are the same value on both surfaces, so they are read
// off the built tree rather than trusted to the flag declarations.
func TestListCarriesTheUnifiedDefaults(t *testing.T) {
	var captured *cli.Context
	app := newSpotinfoApp(
		func(ctx *cli.Context) error { captured = ctx; return nil },
		func(*cli.Context) error { return nil },
	)
	require.NoError(t, app.Run([]string{appName, listCommandName}))
	require.NotNil(t, captured)

	assert.Equal(t, []string{allRegions}, captured.StringSlice(flagRegion),
		"every published region is the default on both commands and both surfaces")
	assert.Equal(t, defaultOS, captured.String(flagOS))
	assert.Equal(t, defaultOutput, captured.String(flagOutput))
	assert.Empty(t, captured.String(flagSort), "an unset sort leaves the order to the cloud")
}

func TestRecommendCarriesTheUnifiedDefaults(t *testing.T) {
	var captured *cli.Context
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error { captured = ctx; return nil },
	)
	require.NoError(t, app.Run(recommendArgs()))
	require.NotNil(t, captured)

	assert.Equal(t, []string{allRegions}, captured.StringSlice(flagRegion))
	assert.Equal(t, defaultOS, captured.String(flagOS))
	assert.Equal(t, defaultOutput, captured.String(flagOutput))
	assert.Equal(t, defaultWorkload, captured.String(flagWorkload))
	assert.Equal(t, defaultTop, captured.Int(flagTop))
}

// Bare `spotinfo` no longer runs a query. It prints help and exits non-zero,
// which is what tells a script the command did nothing — but only when no mode
// flag was given, because --mcp is dispatched from this same action.
func TestBareInvocationPrintsHelpAndRunsNoQuery(t *testing.T) {
	var help bytes.Buffer

	app := newSpotinfoApp(
		func(*cli.Context) error {
			t.Fatal("bare spotinfo must not run a query")

			return nil
		},
		func(*cli.Context) error {
			t.Fatal("bare spotinfo must not run a recommendation")

			return nil
		},
	)
	app.Writer = &help

	err := app.Run([]string{appName})
	require.ErrorIs(t, err, cloud.ErrInvalidArgument)
	assert.Contains(t, err.Error(), listCommandName)
	assert.Contains(t, err.Error(), recommendCommandName)
	assert.Contains(t, help.String(), listCommandName, "the help must name the browse command")
	assert.Contains(t, help.String(), recommendCommandName, "the help must name the ranking command")
}

// --version is answered by urfave/cli before the root action runs, so moving
// the query out of that action must not have taken it with it.
func TestVersionFlagStillPrintsWithoutRunningACommand(t *testing.T) {
	var printed bytes.Buffer

	previous := cli.VersionPrinter
	cli.VersionPrinter = func(ctx *cli.Context) { printed.WriteString(ctx.App.Name + " " + ctx.App.Version) }
	t.Cleanup(func() { cli.VersionPrinter = previous })

	app := newSpotinfoApp(
		func(*cli.Context) error {
			t.Fatal("--version must not run a query")

			return nil
		},
		func(*cli.Context) error { return nil },
	)

	require.NoError(t, app.Run([]string{appName, "--version"}))
	assert.Contains(t, printed.String(), appName)
}

// --mcp is dispatched from the root action, and that branch has to survive the
// root losing its query flags. An unsupported transport is the cheapest proof
// the branch was taken: runMCPServer reaches its transport switch and returns,
// having opened no socket and made no request.
func TestMCPFlagStillReachesTheServerFromTheRootAction(t *testing.T) {
	t.Setenv(mcpTransportEnv, "websocket")

	app := newSpotinfoApp(
		func(*cli.Context) error {
			t.Fatal("--mcp must not run a query")

			return nil
		},
		func(*cli.Context) error { return nil },
	)

	err := app.Run([]string{appName, "--mcp", "--quiet"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported transport: websocket")
}

// Every retired name answers with the flag that replaced it, on both commands.
// "flag provided but not defined" tells a caller their spelling is wrong; it
// does not tell them the concept still exists under one name.
func TestRetiredFlagNamesProduceARenameHint(t *testing.T) {
	for retired, replacement := range renamedFlags {
		for _, command := range []string{listCommandName, recommendCommandName} {
			t.Run(command+"/"+retired, func(t *testing.T) {
				var stdout bytes.Buffer

				app := newSpotinfoApp(
					func(*cli.Context) error { return nil },
					func(*cli.Context) error { return nil },
				)
				app.Writer = &stdout

				err := app.Run([]string{appName, command, "--" + retired, "1"})
				require.ErrorIs(t, err, cloud.ErrInvalidArgument)
				assert.Contains(t, err.Error(), "--"+retired, "the hint must name what was passed")
				assert.Contains(t, err.Error(), "--"+replacement, "the hint must name the replacement")
				assert.Empty(t, stdout.String(), "a refusal must not write a help page into a piped answer")
			})
		}
	}
}

// A name the vocabulary never knew is still an unknown flag, not a rename.
func TestAnUnknownFlagIsNotReportedAsARename(t *testing.T) {
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(*cli.Context) error { return nil },
	)
	app.Writer = &bytes.Buffer{}

	err := app.Run([]string{appName, listCommandName, "--nonsense"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonsense")
	assert.NotContains(t, err.Error(), "renamed")
}

// An empty match names the filters that narrowed it. The note used to read the
// retired flag names, which meant it silently reported nothing at all.
func TestAnEmptyMatchNamesTheFiltersThatWereSet(t *testing.T) {
	var captured *cli.Context
	app := newSpotinfoApp(
		func(ctx *cli.Context) error { captured = ctx; return nil },
		func(*cli.Context) error { return nil },
	)
	require.NoError(t, app.Run([]string{
		appName, listCommandName, "--machine", "m5.large", "--min-vcpu", "4", "--max-price", "0.5",
	}))
	require.NotNil(t, captured)

	described := strings.Join(describeListFilters(captured), " ")
	assert.Contains(t, described, flagMachine+"=m5.large")
	assert.Contains(t, described, flagMinVCPU+"=4")
	assert.Contains(t, described, flagMaxPrice+"=0.5")
	assert.NotContains(t, described, flagOS, "an unset filter is not what narrowed the answer")
}
