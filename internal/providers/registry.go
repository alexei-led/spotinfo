// Package providers assembles the compiled set of cloud providers spotinfo can
// serve. Registration is static: there is no plugin loading, no dynamic
// discovery, and no provider is recognised beyond the neutral vocabulary in
// internal/cloud.
//
// A registration whose data is missing or invalid disables that provider
// instead of failing the process, so one broken snapshot cannot take the other
// clouds down with it. A disabled provider is never silently substituted.
package providers

import (
	"fmt"
	"slices"

	"spotinfo/internal/cloud"
)

// ReasonCode explains why a recognised provider is disabled. The values are
// stable: CLI diagnostics and tests match on them.
type ReasonCode string

const (
	// ReasonNotRegistered means the binary was built without this provider.
	ReasonNotRegistered ReasonCode = "PROVIDER_NOT_REGISTERED"
	// ReasonSnapshotUnavailable means the provider's embedded data is missing,
	// unreadable, hash-mismatched, or failed validation.
	ReasonSnapshotUnavailable ReasonCode = "SNAPSHOT_UNAVAILABLE"
)

// Factory builds one provider from committed data. It returns an error when
// that data cannot be served; the registry disables the provider and records
// the error as the disabled detail.
type Factory func() (cloud.Provider, error)

// Registration binds a recognised provider identifier to its factory.
type Registration struct {
	Build Factory
	ID    cloud.ProviderID
}

// Status reports one recognised provider's availability. Reason and Detail are
// empty when the provider is enabled.
type Status struct {
	ID      cloud.ProviderID
	Reason  ReasonCode
	Detail  string
	Enabled bool
}

// Registry holds the providers this binary can serve.
type Registry struct {
	enabled map[cloud.ProviderID]cloud.Provider
	status  []Status
}

// New builds the registry, running every factory once. A factory failure
// disables its provider; the returned error is reserved for wiring bugs an
// operator cannot fix at runtime: an unrecognised identifier, a duplicate
// registration, a missing factory, or a provider that reports an identifier
// other than the one it was registered under.
func New(registrations ...Registration) (*Registry, error) {
	byID := make(map[cloud.ProviderID]Factory, len(registrations))
	for _, registration := range registrations {
		if !slices.Contains(cloud.ProviderIDs(), registration.ID) {
			return nil, fmt.Errorf("%w: unknown cloud provider %q", cloud.ErrInvalidArgument, registration.ID)
		}
		if registration.Build == nil {
			return nil, fmt.Errorf("%w: provider %q has no factory", cloud.ErrInvalidArgument, registration.ID)
		}
		if _, duplicate := byID[registration.ID]; duplicate {
			return nil, fmt.Errorf("%w: provider %q registered twice", cloud.ErrInvalidArgument, registration.ID)
		}
		byID[registration.ID] = registration.Build
	}

	registry := &Registry{
		enabled: make(map[cloud.ProviderID]cloud.Provider, len(byID)),
		status:  make([]Status, 0, len(cloud.ProviderIDs())),
	}
	// Iterating the vocabulary, not the registrations, is what makes Status
	// report every recognised provider in stable lexical order.
	for _, id := range cloud.ProviderIDs() {
		build, registered := byID[id]
		if !registered {
			registry.status = append(registry.status, Status{ID: id, Reason: ReasonNotRegistered})

			continue
		}

		provider, err := build()
		switch {
		case err != nil:
			registry.status = append(registry.status, Status{
				ID: id, Reason: ReasonSnapshotUnavailable, Detail: err.Error(),
			})
		case provider == nil:
			registry.status = append(registry.status, Status{
				ID: id, Reason: ReasonSnapshotUnavailable, Detail: "factory returned no provider",
			})
		case provider.ID() != id:
			return nil, fmt.Errorf("%w: provider registered as %q reports %q", cloud.ErrInvalidArgument, id, provider.ID())
		default:
			registry.enabled[id] = provider
			registry.status = append(registry.status, Status{ID: id, Enabled: true})
		}
	}

	return registry, nil
}

// Get returns an enabled provider. An identifier outside the neutral
// vocabulary is ErrInvalidArgument; a recognised but disabled provider is
// ErrDataUnavailable, carrying the reason code. Nothing falls back to another
// cloud.
func (r *Registry) Get(id cloud.ProviderID) (cloud.Provider, error) {
	if provider, ok := r.enabled[id]; ok {
		return provider, nil
	}
	for _, status := range r.status {
		if status.ID == id {
			return nil, fmt.Errorf("%w: cloud provider %q is unavailable (%s)", cloud.ErrDataUnavailable, id, status.Reason)
		}
	}

	return nil, fmt.Errorf("%w: unknown cloud provider %q", cloud.ErrInvalidArgument, id)
}

// Available returns the enabled providers in stable lexical order.
func (r *Registry) Available() []cloud.Provider {
	available := make([]cloud.Provider, 0, len(r.enabled))
	for _, status := range r.status {
		if provider, ok := r.enabled[status.ID]; ok {
			available = append(available, provider)
		}
	}

	return available
}

// Status returns every recognised provider, enabled and disabled, in stable
// lexical order. The slice is a copy: a caller cannot mutate the registry.
func (r *Registry) Status() []Status {
	return slices.Clone(r.status)
}
