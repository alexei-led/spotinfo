package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

// Every case here used to be accepted and ignored. A flag that cannot act must
// say so: an answer that silently dropped the filter exits 0 and is read as
// though the filter had been applied.
//
// Not parallel: these run a cli.App, and urfave/cli appends its package-level
// HelpFlag to every command it parses.

// TestOfflineAndRefreshAreRefusedExactlyWhereThereIsNoLiveFeed is driven by the
// registry rather than by a list of cloud names, and that is the point.
//
// --offline and --refresh describe what to do with a live feed. A cloud with no
// live path has none, so both are refused there — and Tasks 10 and 13 retire
// those refusals by declaring CapabilityLiveEnrichment for Azure and GCP. A
// literal cloud table here would have to be edited in those tasks; reading the
// capability off each registered provider means the refusal retires itself when
// the capability lands, and this test keeps asserting the same rule either way.
func TestOfflineAndRefreshAreRefusedExactlyWhereThereIsNoLiveFeed(t *testing.T) {
	// The production registry, so "registered" means what the binary ships.
	// Building it opens no connection: providers are constructed on first use.
	// What keeps the requests below off the network is narrower than it looks —
	// the AWS client is offline, and the Azure provider here is live-capable but
	// every request leaves --region at its default of all, which liveRegions
	// answers from the snapshot rather than sweeping. Name a covered region here
	// and this test fetches.
	registry, err := newProviderRegistry(newSpotClientFor(cloud.FetchPolicy{Offline: true}))
	require.NoError(t, err)

	registered := registry.Registered()
	require.NotEmpty(t, registered, "the binary must register at least one cloud")

	for _, id := range registered {
		for _, flag := range []string{flagOffline, flagRefresh} {
			t.Run(string(id)+"/"+flag, func(t *testing.T) {
				provider, getErr := registry.Get(id)
				require.NoError(t, getErr)

				// The AWS provider `list` builds from its acquisition client and
				// the one the registry hands back come from the same constructor,
				// newAWSProvider, so this capability is the capability the command
				// will gate on.
				live := provider.Capabilities().Has(cloud.CapabilityLiveEnrichment)

				output, runErr := runListWith(t, registry, stubSavingsClient{},
					"--cloud", string(id), "--"+flag)

				if live {
					require.NoError(t, runErr, "a cloud with a live feed must accept --%s", flag)

					return
				}

				require.ErrorIs(t, runErr, cloud.ErrUnsupportedCapability)
				assert.Equal(t, cloud.CodeUnsupportedCapability, cloud.CodeOf(runErr))
				assert.Contains(t, runErr.Error(), "--"+flag)
				assert.Contains(t, runErr.Error(), string(id))
				assert.Empty(t, output, "a refusal must not write a partial answer to stdout")
			})
		}
	}
}

// The remaining refusals, each of which survives the rest of the plan.
//
// The assertions are the three a caller depends on: the command fails, stdout
// carries nothing a parser could mistake for an answer, and the message names
// what was refused. The process exit code is main's translation of a non-nil
// error here; the subprocess suite in e2e_test.go asserts it directly.
//
// Two classes, and namesCloud is what separates them. A capability refusal is
// about one cloud and must name it — "not here" is the whole content of the
// message. A companion refusal is about a flag combination and is refused
// identically everywhere, so naming a cloud there would imply another one
// accepts it.
func TestAFlagTheCloudCannotActOnIsRefused(t *testing.T) {
	for name, test := range map[string]struct {
		id         cloud.ProviderID
		code       cloud.ErrorCode
		args       []string
		contains   []string
		namesCloud bool
	}{
		"placement scores on a cloud that publishes none": {
			id:         cloud.ProviderGCP,
			args:       []string{"--" + flagWithScore},
			code:       cloud.CodeUnsupportedCapability,
			contains:   []string{"--" + flagWithScore, string(cloud.CapabilityPlacementScore)},
			namesCloud: true,
		},
		"placement scores on a cloud this build cannot authenticate to": {
			id:         cloud.ProviderAzure,
			args:       []string{"--" + flagWithScore},
			code:       cloud.CodeUnsupportedCapability,
			contains:   []string{"--" + flagWithScore, "subscription"},
			namesCloud: true,
		},
		"a gcp project on another cloud": {
			id:         cloud.ProviderAzure,
			args:       []string{"--" + flagGCPProject, "some-project"},
			code:       cloud.CodeUnsupportedCapability,
			contains:   []string{"--" + flagGCPProject},
			namesCloud: true,
		},
		"zone detail without the lookup that produces it": {
			id:       cloud.ProviderAWS,
			args:     []string{"--" + flagAZ},
			code:     cloud.CodeInvalidArgument,
			contains: []string{"--" + flagAZ, "--" + flagWithScore},
		},
		"a score floor without the lookup that produces it": {
			id:       cloud.ProviderAWS,
			args:     []string{"--" + flagMinScore, "5"},
			code:     cloud.CodeInvalidArgument,
			contains: []string{"--" + flagMinScore, "--" + flagWithScore},
		},
		"a score timeout without the lookup it would bound": {
			id:       cloud.ProviderAWS,
			args:     []string{"--" + flagScoreTimeout, "60"},
			code:     cloud.CodeInvalidArgument,
			contains: []string{"--" + flagScoreTimeout, "--" + flagWithScore},
		},
	} {
		t.Run(name, func(t *testing.T) {
			registry := refusalRegistry(test.id)

			output, err := runListWith(t, registry, stubSavingsClient{},
				append([]string{"--cloud", string(test.id)}, test.args...)...)

			require.Error(t, err)
			assert.Equal(t, test.code, cloud.CodeOf(err))
			assert.Empty(t, output, "a refusal must not write a partial answer to stdout")

			for _, want := range test.contains {
				assert.Contains(t, err.Error(), want, "the refusal must name what it refused")
			}

			if test.namesCloud {
				assert.Contains(t, err.Error(), string(test.id), "the refusal must name the cloud that refused it")
			}
		})
	}
}

// refusalRegistry serves one cloud with the capabilities it has today. AWS is
// registered too even though `list` builds its provider from the acquisition
// client: an unregistered cloud fails as DATA_UNAVAILABLE before any of these
// checks run.
func refusalRegistry(id cloud.ProviderID) providerRegistry {
	capabilities := offlineLinuxCapabilities()
	if id == cloud.ProviderAWS {
		capabilities = awsCapabilities()
	}

	return mustRegistry(registrationOf(stubProvider{id: id, capabilities: capabilities}))
}

// Azure is the one cloud where "not published" and "not built" differ, and the
// refusals have to say which. Both figures exist as vendor APIs and both need a
// subscription this build does not authenticate to; a message that read like
// GCP's would send a reader to Microsoft for something Microsoft already ships.
func TestTheAzureRefusalsNameTheSubscriptionThisBuildDoesNotAuthenticateTo(t *testing.T) {
	registry := refusalRegistry(cloud.ProviderAzure)

	_, scoreErr := runListWith(t, registry, stubSavingsClient{},
		"--cloud", string(cloud.ProviderAzure), "--"+flagWithScore)
	require.ErrorIs(t, scoreErr, cloud.ErrUnsupportedCapability)
	assert.Contains(t, scoreErr.Error(), "Spot Placement Score")
	assert.Contains(t, scoreErr.Error(), "does not authenticate to")

	riskErr := runRecommend(t, registry,
		validRecommendArgs("--cloud", string(cloud.ProviderAzure), "--"+flagLiveRisk)...)
	require.ErrorIs(t, riskErr, cloud.ErrUnsupportedCapability)
	assert.Contains(t, riskErr.Error(), "Azure Resource Graph")
	assert.Contains(t, riskErr.Error(), "does not authenticate to")
}

// A cloud that publishes nothing says so plainly, which is what makes the Azure
// wording above readable as a different fact rather than as the same refusal in
// more words.
func TestACloudThatPublishesNoPlacementScoreSaysSo(t *testing.T) {
	_, err := runListWith(t, refusalRegistry(cloud.ProviderGCP), stubSavingsClient{},
		"--cloud", string(cloud.ProviderGCP), "--"+flagWithScore)

	require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
	assert.Contains(t, err.Error(), "publishes no")
	assert.NotContains(t, err.Error(), "subscription")
}

// `list --sort score` without --with-score ordered nothing and exited 0 on AWS,
// the one cloud the capability check waves through: listCapabilityRequest asks
// for CapabilityPlacementScore, which AWS has, and having it is not the same as
// having fetched it. Measured before the fix at exit 0 over 31,009 rows in
// default order, with no placement column rendered.
//
// The clouds that publish no regional score are covered by the test above and
// refuse earlier, on the capability. This is the gap between the two.
func TestAScoreSortWithoutTheFetchThatFillsItIsRefusedOnList(t *testing.T) {
	_, err := runListWith(t, refusalRegistry(cloud.ProviderAWS), stubSavingsClient{},
		"--cloud", string(cloud.ProviderAWS), "--"+flagSort, sortScore)

	require.ErrorIs(t, err, cloud.ErrInvalidArgument)
	assert.Contains(t, err.Error(), "--"+flagWithScore)
}

// An exported GOOGLE_CLOUD_PROJECT is ambient: it is set on most machines that
// touch GCP at all, and it is not a request. Refusing --gcp-project off GCP must
// therefore refuse what the caller named, not what the shell happened to carry —
// otherwise one exported variable breaks every default AWS invocation.
//
// This is why the flag reads the variable in withLiveRisk instead of declaring
// it as urfave/cli EnvVars. Measured before the change: with the variable
// exported, ctx.IsSet(--gcp-project) was true on a bare `spotinfo list`.
//
// The variable still reaches the one path that reads it —
// TestLiveRiskRefusesABadProjectFromTheEnvironment in validation_test.go drives
// a bad project in through the environment and watches it be rejected — so this
// pair holds both halves: ambient is not a request, and the request still works.
func TestAnExportedProjectVariableDoesNotRefuseAnotherCloudsQuery(t *testing.T) {
	t.Setenv(gcpProjectEnv, "ambient-project")

	_, err := runListWith(t, refusalRegistry(cloud.ProviderAzure), stubSavingsClient{},
		"--cloud", string(cloud.ProviderAzure))
	require.NoError(t, err, "an ambient project variable is not a flag the caller passed")
}
