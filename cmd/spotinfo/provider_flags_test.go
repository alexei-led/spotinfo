package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	"spotinfo/internal/providers"
	awsprovider "spotinfo/internal/providers/aws"
	"spotinfo/internal/spot"
)

// Tests that run the CLI app are deliberately not parallel: urfave/cli appends
// its package-level HelpFlag to every command it parses and writes to that
// shared flag in Apply, so two concurrent app.Run calls race inside the
// library. Tests that touch no CLI app still run in parallel.

// stubProvider stands in for a compiled provider. The CLI only asks for an
// identifier and capabilities before acquisition, so nothing here reaches a
// cloud.
type stubProvider struct {
	id           cloud.ProviderID
	capabilities cloud.Capabilities
}

func (p stubProvider) ID() cloud.ProviderID             { return p.id }
func (p stubProvider) Capabilities() cloud.Capabilities { return p.capabilities }
func (p stubProvider) Query(context.Context, *cloud.Query) (cloud.Result, error) {
	return cloud.Result{Provider: p.id}, nil
}

// awsCapabilities mirrors the production AWS adapter.
// TestStubAWSCapabilitiesMatchTheAdapter fails if the two drift, which would
// leave every CLI test exercising a capability gate the binary does not have.
func awsCapabilities() cloud.Capabilities {
	return cloud.Capabilities{
		OperatingSystems: []cloud.OperatingSystem{cloud.OSLinux, cloud.OSWindows},
		Architectures:    []cloud.Architecture{cloud.ArchitectureX8664, cloud.ArchitectureARM64},
		SpotPrice:        true,
		OnDemandPrice:    false,
		MachineSpec:      true,
		Risk:             true,
		PlacementScore:   true,
		ZoneDetail:       true,
		LiveEnrichment:   true,
	}
}

// offlineLinuxCapabilities is a provider with committed Linux spot prices and
// no risk, score, or zone observations — the shape GCP and Azure will have.
func offlineLinuxCapabilities() cloud.Capabilities {
	return cloud.Capabilities{
		OperatingSystems: []cloud.OperatingSystem{cloud.OSLinux},
		Architectures:    []cloud.Architecture{cloud.ArchitectureX8664, cloud.ArchitectureARM64},
		SpotPrice:        true,
		MachineSpec:      true,
	}
}

func mustRegistry(registrations ...providers.Registration) *providers.Registry {
	registry, err := providers.New(registrations...)
	if err != nil {
		panic(err)
	}

	return registry
}

func registrationOf(provider stubProvider) providers.Registration {
	return providers.Registration{
		ID:    provider.id,
		Build: func() (cloud.Provider, error) { return provider, nil },
	}
}

// awsOnlyRegistry is the shipped wiring: AWS registered, GCP and Azure
// recognised but unregistered.
func awsOnlyRegistry() *providers.Registry {
	return mustRegistry(registrationOf(stubProvider{id: cloud.ProviderAWS, capabilities: awsCapabilities()}))
}

// stubSavingsClient and stubArchitectures let a real AWS provider be built
// without any AWS or embedded-data access.
type stubSavingsClient struct{}

func (stubSavingsClient) GetSpotSavings(context.Context, ...spot.GetSpotSavingsOption) ([]spot.Advice, error) {
	return nil, nil
}
func (stubSavingsClient) DataSource() string { return spot.DataSourceEmbedded }

type stubArchitectures struct{}

func (stubArchitectures) ArchitectureForInstance(string) (spot.Architecture, bool) { return "", false }

func TestStubAWSCapabilitiesMatchTheAdapter(t *testing.T) {
	t.Parallel()

	provider, err := awsprovider.New(stubSavingsClient{}, stubArchitectures{})
	require.NoError(t, err)
	assert.Equal(t, provider.Capabilities(), awsCapabilities(),
		"CLI tests must gate against the real AWS capabilities")
}

// runRoot and runRecommend drive the production app assembly. The mock client
// fails the test if it is called, so every assertion below also proves the
// failure happened before acquisition.
func runRoot(t *testing.T, registry providerRegistry, args ...string) error {
	t.Helper()

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(ctx *cli.Context) error {
			return execMainCmd(ctx, context.Background(), registry, newMockspotClient(t), &output)
		},
		func(*cli.Context) error { return nil },
	)

	return app.Run(append([]string{appName}, args...))
}

func runRecommend(t *testing.T, registry providerRegistry, args ...string) error {
	t.Helper()

	var output bytes.Buffer
	app := newSpotinfoApp(
		func(*cli.Context) error { return nil },
		func(ctx *cli.Context) error {
			return execRecommendCmd(ctx, context.Background(), registry, newMockspotClient(t), &output)
		},
	)

	return app.Run(append([]string{appName}, args...))
}

// validRecommendArgs carries every required recommendation input, so a failure
// below comes from the provider gate rather than from input validation — which
// deliberately runs first.
func validRecommendArgs(extra ...string) []string {
	args := make([]string, 0, 7+len(extra))
	args = append(args, recommendCommandName, "--architecture", "x86_64", "--cpu", "2", "--memory", "8")

	return append(args, extra...)
}

func TestUnknownCloudIsRejectedBeforeAcquisition(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T) error
	}{
		{
			name: "root",
			run:  func(t *testing.T) error { return runRoot(t, awsOnlyRegistry(), "--cloud", "ibm") },
		},
		{
			name: "recommend",
			run: func(t *testing.T) error {
				return runRecommend(t, awsOnlyRegistry(), validRecommendArgs("--cloud", "ibm")...)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.run(t)
			require.ErrorIs(t, err, cloud.ErrInvalidArgument)
			assert.Equal(t, cloud.CodeInvalidArgument, cloud.CodeOf(err))
		})
	}
}

// A recognised provider this binary was not built with is unavailable. It never
// silently answers with AWS data.
func TestUnregisteredCloudReportsDataUnavailableWithItsReasonCode(t *testing.T) {
	for _, id := range []cloud.ProviderID{cloud.ProviderGCP, cloud.ProviderAzure} {
		t.Run(string(id), func(t *testing.T) {
			err := runRoot(t, awsOnlyRegistry(), "--cloud", string(id))
			require.ErrorIs(t, err, cloud.ErrDataUnavailable)
			assert.Equal(t, cloud.CodeDataUnavailable, cloud.CodeOf(err))
			assert.Contains(t, err.Error(), string(providers.ReasonNotRegistered))
		})
	}
}

// The capability gate, proven against enabled providers that lack the
// observations the flags ask for. The risk-free provider covers the
// interruption column; the score-free one isolates the score and zone flags,
// which would otherwise be masked by the risk shortfall reported first.
func TestUnsupportedCapabilitiesFailBeforeAcquisition(t *testing.T) {
	scoreFree := offlineLinuxCapabilities()
	scoreFree.Risk = true

	for _, test := range []struct {
		name         string
		want         string
		args         []string
		capabilities cloud.Capabilities
	}{
		{
			name: "risk column", want: string(cloud.CapabilityRisk),
			args: []string{"--cloud", "gcp"}, capabilities: offlineLinuxCapabilities(),
		},
		{
			name: "placement score", want: string(cloud.CapabilityPlacementScore),
			args: []string{"--cloud", "gcp", "--with-score"}, capabilities: scoreFree,
		},
		{
			name: "minimum score", want: string(cloud.CapabilityPlacementScore),
			args: []string{"--cloud", "gcp", "--min-score", "5"}, capabilities: scoreFree,
		},
		{
			name: "availability zone detail", want: string(cloud.CapabilityZoneDetail),
			args: []string{"--cloud", "gcp", "--az"}, capabilities: withPlacementScore(scoreFree),
		},
		{
			name: "windows prices", want: "os windows",
			args: []string{"--cloud", "gcp", "--os", "windows"}, capabilities: offlineLinuxCapabilities(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := mustRegistry(registrationOf(stubProvider{id: cloud.ProviderGCP, capabilities: test.capabilities}))

			err := runRoot(t, registry, test.args...)
			require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
			assert.Equal(t, cloud.CodeUnsupportedCapability, cloud.CodeOf(err))
			assert.Contains(t, err.Error(), test.want)
			assert.Contains(t, err.Error(), string(cloud.ProviderGCP))
		})
	}
}

func withPlacementScore(capabilities cloud.Capabilities) cloud.Capabilities {
	capabilities.PlacementScore = true

	return capabilities
}

func TestRecommendRejectsAProviderWithoutRisk(t *testing.T) {
	registry := mustRegistry(registrationOf(stubProvider{id: cloud.ProviderGCP, capabilities: offlineLinuxCapabilities()}))

	err := runRecommend(t, registry, validRecommendArgs("--cloud", "gcp")...)
	require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
	assert.Contains(t, err.Error(), string(cloud.CapabilityRisk))
}

// Every v1 workload caps interruption frequency, so a risk-free provider is
// rejected whichever one is chosen.
func TestRecommendRejectsARiskFreeProviderForEveryWorkload(t *testing.T) {
	registry := mustRegistry(registrationOf(stubProvider{id: cloud.ProviderGCP, capabilities: offlineLinuxCapabilities()}))

	for _, workload := range []string{"web", "ci", "batch"} {
		t.Run(workload, func(t *testing.T) {
			err := runRecommend(t, registry, validRecommendArgs("--cloud", "gcp", "--workload", workload)...)
			require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
		})
	}
}

// A provider that satisfies every capability but is not AWS still must not be
// served from the legacy AWS acquisition path.
func TestNonAWSProviderIsNotServedFromTheAWSPath(t *testing.T) {
	registry := mustRegistry(registrationOf(stubProvider{id: cloud.ProviderAzure, capabilities: awsCapabilities()}))

	err := runRoot(t, registry, "--cloud", "azure")
	require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
	assert.Contains(t, err.Error(), "not served by this command yet")
}

// --cloud may be given on either side of the subcommand. The flag that was
// actually set wins; the subcommand's own declaration must not shadow it.
func TestCloudFlagResolvesAcrossContextLineage(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "before the subcommand",
			args: append([]string{"--cloud", "gcp"}, validRecommendArgs()...),
		},
		{
			name: "after the subcommand",
			args: validRecommendArgs("--cloud", "gcp"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runRecommend(t, awsOnlyRegistry(), test.args...)
			require.ErrorIs(t, err, cloud.ErrDataUnavailable,
				"an explicit --cloud gcp must never resolve to the aws default")
			assert.Contains(t, err.Error(), string(cloud.ProviderGCP))
		})
	}
}

func TestProviderIDDefaultsToAWS(t *testing.T) {
	for _, test := range []struct {
		name string
		want cloud.ProviderID
		args []string
	}{
		{name: "unset", want: cloud.ProviderAWS, args: []string{}},
		{name: "explicit aws", want: cloud.ProviderAWS, args: []string{"--cloud", "aws"}},
		{name: "explicit gcp", want: cloud.ProviderGCP, args: []string{"--cloud", "gcp"}},
		{name: "surrounding whitespace", want: cloud.ProviderGCP, args: []string{"--cloud", " gcp "}},
		{name: "mixed case", want: cloud.ProviderAzure, args: []string{"--cloud", "AZURE"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var (
				got cloud.ProviderID
				err error
			)
			app := newSpotinfoApp(
				func(ctx *cli.Context) error {
					got, err = providerID(ctx)

					return nil
				},
				func(*cli.Context) error { return nil },
			)
			require.NoError(t, app.Run(append([]string{appName}, test.args...)))
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

// --machine is the neutral spelling of the machine-type filter. The AWS
// spellings stay primary, so every documented AWS invocation keeps working and
// both names reach the same value.
func TestMachineFlagIsAnAliasOfTheAWSSpellings(t *testing.T) {
	for _, test := range []struct {
		name string
		read string
		args []string
	}{
		{name: "root --type", read: flagType, args: []string{"--type", "m5.large"}},
		{name: "root --machine", read: flagType, args: []string{"--machine", "m5.large"}},
		{name: "recommend --instance", read: flagInstance, args: validRecommendArgs("--instance", "m5.large")},
		{name: "recommend --machine", read: flagInstance, args: validRecommendArgs("--machine", "m5.large")},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got string
			record := func(ctx *cli.Context) error {
				got = ctx.String(test.read)

				return nil
			}
			require.NoError(t, newSpotinfoApp(record, record).Run(append([]string{appName}, test.args...)))
			assert.Equal(t, "m5.large", got)
		})
	}
}

// An OS outside the neutral vocabulary is not a capability question: the
// provider owns that error, and AWS already reports it with its own wording.
func TestRequestedValuesIgnoreWhatIsNotInTheVocabulary(t *testing.T) {
	t.Parallel()

	assert.Equal(t, cloud.OSLinux, requestedOS("linux"))
	assert.Equal(t, cloud.OperatingSystem(""), requestedOS("plan9"))
	assert.Equal(t, cloud.OperatingSystem(""), requestedOS(""))

	assert.Equal(t, cloud.ArchitectureARM64, requestedArchitecture("arm64"))
	assert.Equal(t, cloud.Architecture(""), requestedArchitecture("riscv64"))
	assert.Equal(t, cloud.Architecture(""), requestedArchitecture(""))
}
