package providers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/providers"
)

// fakeProvider is a provider stand-in: the registry only ever asks for an
// identifier and capabilities, so nothing here touches a cloud.
type fakeProvider struct {
	id           cloud.ProviderID
	capabilities cloud.Capabilities
}

func (p fakeProvider) ID() cloud.ProviderID             { return p.id }
func (p fakeProvider) Capabilities() cloud.Capabilities { return p.capabilities }
func (p fakeProvider) Query(context.Context, *cloud.Query) (cloud.Result, error) {
	return cloud.Result{Provider: p.id}, nil
}

func registrationFor(id cloud.ProviderID) providers.Registration {
	return providers.Registration{ID: id, Build: func() (cloud.Provider, error) {
		return fakeProvider{id: id, capabilities: cloud.Capabilities{
			OperatingSystems: []cloud.OperatingSystem{cloud.OSLinux},
			SpotPrice:        true,
		}}, nil
	}}
}

func statusFor(t *testing.T, registry *providers.Registry, id cloud.ProviderID) providers.Status {
	t.Helper()

	for _, status := range registry.Status() {
		if status.ID == id {
			return status
		}
	}
	t.Fatalf("no status for provider %q", id)

	return providers.Status{}
}

func TestRegistryRecognisesOnlyTheNeutralVocabularyInStableOrder(t *testing.T) {
	t.Parallel()

	registry, err := providers.New(registrationFor(cloud.ProviderGCP), registrationFor(cloud.ProviderAWS))
	require.NoError(t, err)

	ids := make([]cloud.ProviderID, 0, len(registry.Status()))
	for _, status := range registry.Status() {
		ids = append(ids, status.ID)
	}
	// Registration order was gcp then aws; the report is lexical regardless.
	assert.Equal(t, []cloud.ProviderID{cloud.ProviderAWS, cloud.ProviderAzure, cloud.ProviderGCP}, ids)

	// Providers are built on first use, so nothing is available until asked for.
	assert.Empty(t, registry.Available(), "construction must not build anything")
	for _, id := range []cloud.ProviderID{cloud.ProviderAWS, cloud.ProviderGCP} {
		_, getErr := registry.Get(id)
		require.NoError(t, getErr)
	}

	available := make([]cloud.ProviderID, 0, len(registry.Available()))
	for _, provider := range registry.Available() {
		available = append(available, provider.ID())
	}
	assert.Equal(t, []cloud.ProviderID{cloud.ProviderAWS, cloud.ProviderGCP}, available)
}

func TestRegistryRejectsWiringBugs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		registrations []providers.Registration
	}{
		{
			name:          "unknown provider",
			registrations: []providers.Registration{{ID: "ibm", Build: registrationFor(cloud.ProviderAWS).Build}},
		},
		{
			name:          "missing factory",
			registrations: []providers.Registration{{ID: cloud.ProviderAWS}},
		},
		{
			name:          "duplicate registration",
			registrations: []providers.Registration{registrationFor(cloud.ProviderAWS), registrationFor(cloud.ProviderAWS)},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			registry, err := providers.New(test.registrations...)
			require.ErrorIs(t, err, cloud.ErrInvalidArgument)
			assert.Nil(t, registry)
		})
	}
}

// A provider that reports an identifier other than the one it was registered
// under is still a wiring bug, but it cannot be seen without building the
// provider. Deferring the build defers the detection to the first request for
// that cloud, where it stays ErrInvalidArgument rather than being reported as a
// missing snapshot.
func TestProviderReportingAnotherIdentifierFailsOnFirstUse(t *testing.T) {
	t.Parallel()

	registry, err := providers.New(providers.Registration{
		ID:    cloud.ProviderAWS,
		Build: func() (cloud.Provider, error) { return fakeProvider{id: cloud.ProviderGCP}, nil },
	})
	require.NoError(t, err, "the mismatch is invisible until the factory runs")

	provider, err := registry.Get(cloud.ProviderAWS)
	assert.Nil(t, provider)
	require.ErrorIs(t, err, cloud.ErrInvalidArgument)
	assert.Contains(t, err.Error(), string(cloud.ProviderGCP))
}

func TestUnregisteredProviderIsDisabledAndUnavailable(t *testing.T) {
	t.Parallel()

	registry, err := providers.New(registrationFor(cloud.ProviderAWS))
	require.NoError(t, err)

	status := statusFor(t, registry, cloud.ProviderGCP)
	assert.False(t, status.Enabled)
	assert.Equal(t, providers.ReasonNotRegistered, status.Reason)

	provider, err := registry.Get(cloud.ProviderGCP)
	assert.Nil(t, provider)
	require.ErrorIs(t, err, cloud.ErrDataUnavailable)
	assert.Equal(t, cloud.CodeDataUnavailable, cloud.CodeOf(err))
	assert.Contains(t, err.Error(), string(providers.ReasonNotRegistered))
}

// A broken snapshot disables one provider. It must not fail construction, take
// another cloud down, or fall back to a provider the caller did not ask for.
func TestBrokenSnapshotDisablesOnlyItsOwnProvider(t *testing.T) {
	t.Parallel()

	failure := errors.New("catalog hash mismatch")
	registry, err := providers.New(
		registrationFor(cloud.ProviderAWS),
		providers.Registration{ID: cloud.ProviderGCP, Build: func() (cloud.Provider, error) {
			return nil, failure
		}},
	)
	require.NoError(t, err)

	// Nothing is built until it is asked for, so the failure surfaces from Get.
	assert.Equal(t, providers.ReasonNotLoaded, statusFor(t, registry, cloud.ProviderGCP).Reason)

	_, err = registry.Get(cloud.ProviderGCP)
	require.ErrorIs(t, err, cloud.ErrDataUnavailable)

	status := statusFor(t, registry, cloud.ProviderGCP)
	assert.False(t, status.Enabled)
	assert.Equal(t, providers.ReasonSnapshotUnavailable, status.Reason)
	assert.Equal(t, failure.Error(), status.Detail)

	served, err := registry.Get(cloud.ProviderAWS)
	require.NoError(t, err)
	assert.Equal(t, cloud.ProviderAWS, served.ID(), "a disabled provider must never be substituted")
	assert.Len(t, registry.Available(), 1)
}

func TestFactoryReturningNoProviderIsDisabledRatherThanServed(t *testing.T) {
	t.Parallel()

	registry, err := providers.New(providers.Registration{
		ID: cloud.ProviderAzure,
		//nolint:nilnil // a factory that misbehaves this way is exactly what this test pins down
		Build: func() (cloud.Provider, error) { return nil, nil },
	})
	require.NoError(t, err)

	_, err = registry.Get(cloud.ProviderAzure)
	require.ErrorIs(t, err, cloud.ErrDataUnavailable)

	status := statusFor(t, registry, cloud.ProviderAzure)
	assert.False(t, status.Enabled)
	assert.Equal(t, providers.ReasonSnapshotUnavailable, status.Reason)
	assert.Empty(t, registry.Available())
}

func TestGetRejectsAnIdentifierOutsideTheVocabulary(t *testing.T) {
	t.Parallel()

	registry, err := providers.New(registrationFor(cloud.ProviderAWS))
	require.NoError(t, err)

	_, err = registry.Get("ibm")
	require.ErrorIs(t, err, cloud.ErrInvalidArgument)
	assert.Equal(t, cloud.CodeInvalidArgument, cloud.CodeOf(err))
}

func TestEachProviderKeepsItsOwnCapabilities(t *testing.T) {
	t.Parallel()

	registry, err := providers.New(
		registrationFor(cloud.ProviderAWS),
		providers.Registration{ID: cloud.ProviderGCP, Build: func() (cloud.Provider, error) {
			return fakeProvider{id: cloud.ProviderGCP, capabilities: cloud.Capabilities{
				OperatingSystems: []cloud.OperatingSystem{cloud.OSLinux},
				SpotPrice:        true,
				Risk:             false,
			}}, nil
		}},
	)
	require.NoError(t, err)

	awsProvider, err := registry.Get(cloud.ProviderAWS)
	require.NoError(t, err)
	assert.True(t, awsProvider.Capabilities().Has(cloud.CapabilitySpotPrice))

	gcpProvider, err := registry.Get(cloud.ProviderGCP)
	require.NoError(t, err)
	require.ErrorIs(t,
		gcpProvider.Capabilities().Require(cloud.CapabilityRequest{Needed: []cloud.Capability{cloud.CapabilityRisk}}),
		cloud.ErrUnsupportedCapability)
}

// Status is a copy: a caller that mutates it must not change what the registry
// reports next time.
func TestStatusIsNotAliasedToRegistryState(t *testing.T) {
	t.Parallel()

	registry, err := providers.New(registrationFor(cloud.ProviderAWS))
	require.NoError(t, err)

	_, err = registry.Get(cloud.ProviderAWS)
	require.NoError(t, err)

	registry.Status()[0].Enabled = false
	assert.True(t, statusFor(t, registry, cloud.ProviderAWS).Enabled)
}
