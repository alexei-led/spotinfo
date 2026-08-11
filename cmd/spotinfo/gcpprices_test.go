package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	gcpprovider "spotinfo/internal/providers/gcp"
)

// --gcp-billing-key on the CLI. What the key does once it reaches the provider
// is tested against a stub transport in internal/providers/gcp; these are the
// three things only the command layer can get wrong: refusing the flag on a
// cloud that cannot use it, resolving it from the flag or the environment, and
// wiring it onto the provider that was actually resolved.
//
// Serial: every test here builds a cli.App or calls t.Setenv.

// runWithGCPPrices resolves the key the way a command does and reports the
// provider the query would run against.
func runWithGCPPrices(t *testing.T, provider cloud.Provider, args ...string) cloud.Provider {
	t.Helper()

	var resolved cloud.Provider

	app := newSpotinfoApp(
		func(ctx *cli.Context) error {
			resolved = withGCPPrices(ctx, provider)

			return nil
		},
		func(*cli.Context) error { return nil },
	)
	require.NoError(t, app.Run(append([]string{appName, listCommandName}, args...)))

	return resolved
}

// The flag names a Google API key, so passing it to another cloud is a question
// that cloud cannot be asked — the same refusal --gcp-project already gets.
func TestABillingKeyOnAnotherCloudIsRefused(t *testing.T) {
	output, err := runListWith(t, refusalRegistry(cloud.ProviderAzure), stubSavingsClient{},
		"--cloud", string(cloud.ProviderAzure), "--"+flagGCPBillingKey, "some-key")

	require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
	assert.Equal(t, cloud.CodeUnsupportedCapability, cloud.CodeOf(err))
	assert.Contains(t, err.Error(), "--"+flagGCPBillingKey)
	assert.Contains(t, err.Error(), string(cloud.ProviderAzure))
	assert.Empty(t, output, "a refusal must not write a partial answer to stdout")
}

// The same refusal on `recommend`: the flag is declared on both commands, so it
// must mean the same thing on both.
func TestABillingKeyOnAnotherCloudIsRefusedOnRecommendToo(t *testing.T) {
	err := runRecommend(t, refusalRegistry(cloud.ProviderAzure),
		validRecommendArgs("--cloud", string(cloud.ProviderAzure), "--"+flagGCPBillingKey, "some-key")...)

	require.ErrorIs(t, err, cloud.ErrUnsupportedCapability)
	assert.Contains(t, err.Error(), "--"+flagGCPBillingKey)
}

// An exported key is ambient, not a request — but unlike GOOGLE_CLOUD_PROJECT
// it is this tool's own variable, so a caller who exported it did mean it. It
// must still not refuse a query about another cloud, which is what declaring it
// as urfave/cli EnvVars would have done.
func TestAnExportedBillingKeyDoesNotRefuseAnotherCloudsQuery(t *testing.T) {
	t.Setenv(gcpBillingKeyEnv, "ambient-key")

	_, err := runListWith(t, refusalRegistry(cloud.ProviderAzure), stubSavingsClient{},
		"--cloud", string(cloud.ProviderAzure))
	require.NoError(t, err, "an ambient key is not a flag the caller passed")
}

// The flag wins over the environment, and either one wires the provider.
func TestTheBillingKeyComesFromTheFlagOrTheEnvironment(t *testing.T) {
	t.Setenv(gcpBillingKeyEnv, "environment-key")

	app := newSpotinfoApp(
		func(ctx *cli.Context) error {
			assert.Equal(t, "flag-key", resolveGCPBillingKey(ctx))

			return nil
		},
		func(*cli.Context) error { return nil },
	)
	require.NoError(t, app.Run([]string{appName, listCommandName, "--" + flagGCPBillingKey, "flag-key"}))

	fallback := newSpotinfoApp(
		func(ctx *cli.Context) error {
			assert.Equal(t, "environment-key", resolveGCPBillingKey(ctx))

			return nil
		},
		func(*cli.Context) error { return nil },
	)
	require.NoError(t, fallback.Run([]string{appName, listCommandName}))
}

// A key wires the GCP provider and nothing else. Another cloud's provider is
// returned untouched: it has no Google catalogue to read.
func TestOnlyTheGCPProviderIsWiredForLivePrices(t *testing.T) {
	t.Setenv(gcpBillingKeyEnv, "")

	gcp, err := gcpprovider.New()
	require.NoError(t, err)

	withoutKey := runWithGCPPrices(t, gcp, "--cloud", string(cloud.ProviderGCP))
	assert.Same(t, gcp, withoutKey, "with no key there is nothing to wire")

	withKey := runWithGCPPrices(t, gcp,
		"--cloud", string(cloud.ProviderGCP), "--"+flagGCPBillingKey, "a-key")
	assert.NotSame(t, gcp, withKey, "a key must reach the provider the query runs against")
	assert.Equal(t, cloud.ProviderGCP, withKey.ID())

	other := stubProvider{id: cloud.ProviderAzure, capabilities: offlineLinuxCapabilities()}
	assert.Equal(t, other, runWithGCPPrices(t, other,
		"--cloud", string(cloud.ProviderAzure), "--"+flagGCPBillingKey, "a-key"))
}
