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
	"errors"
	"fmt"
	"slices"
	"sync"

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
	// ReasonNotLoaded is a registered provider nothing has asked for yet. It is
	// neither enabled nor broken: its snapshot has simply not been read, because
	// providers are built on first use.
	ReasonNotLoaded ReasonCode = "NOT_LOADED"
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
//
// A provider is built on first use, not at construction. Every factory here
// decodes, hashes and re-validates a committed snapshot, so building all of
// them up front charged an AWS-only invocation — the common case — for the GCP
// and Azure catalogues it never reads, and for the MCP server it retained both
// for the process lifetime. Resolution is memoized, so a provider is still
// built at most once and a failure stays sticky rather than being retried per
// call.
type Registry struct {
	resolve map[cloud.ProviderID]func() (cloud.Provider, error)
	loaded  map[cloud.ProviderID]*Status
	mu      sync.Mutex
}

// New validates the registrations and records how to build each provider; no
// factory runs here. A factory failure disables its provider when it is first
// asked for. The returned error is reserved for wiring bugs an operator cannot
// fix at runtime: an unrecognised identifier, a duplicate registration, or a
// missing factory.
//
// A provider that reports an identifier other than the one it was registered
// under is the one wiring bug that moved: it cannot be detected without building
// the provider, so it now surfaces from Get rather than from here.
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
		resolve: make(map[cloud.ProviderID]func() (cloud.Provider, error), len(byID)),
		loaded:  make(map[cloud.ProviderID]*Status, len(byID)),
	}

	for id, build := range byID {
		registry.resolve[id] = sync.OnceValues(func() (cloud.Provider, error) {
			provider, err := build()
			switch {
			case err != nil:
				return nil, err
			case provider == nil:
				return nil, errNoProvider
			case provider.ID() != id:
				// A wiring bug, not a data problem. It used to fail construction;
				// deferring the build defers the detection with it, so it surfaces
				// on the first request for that cloud instead.
				return nil, fmt.Errorf("%w: provider registered as %q reports %q",
					cloud.ErrInvalidArgument, id, provider.ID())
			}

			return provider, nil
		})
	}

	return registry, nil
}

// errNoProvider reports a factory that returned neither a provider nor an error.
var errNoProvider = errors.New("factory returned no provider")

// Get returns an enabled provider. An identifier outside the neutral
// vocabulary is ErrInvalidArgument; a recognised but disabled provider is
// ErrDataUnavailable, carrying the reason code. Nothing falls back to another
// cloud.
func (r *Registry) Get(id cloud.ProviderID) (cloud.Provider, error) {
	resolve, registered := r.resolve[id]
	if !registered {
		if slices.Contains(cloud.ProviderIDs(), id) {
			return nil, fmt.Errorf("%w: cloud provider %q is unavailable (%s)",
				cloud.ErrDataUnavailable, id, ReasonNotRegistered)
		}

		return nil, fmt.Errorf("%w: unknown cloud provider %q", cloud.ErrInvalidArgument, id)
	}

	provider, err := resolve()
	r.record(id, err)

	if err != nil {
		// A wiring bug keeps its own error; anything else is a snapshot this
		// binary cannot serve, and no other cloud is substituted for it.
		if errors.Is(err, cloud.ErrInvalidArgument) {
			return nil, err
		}

		return nil, fmt.Errorf("%w: cloud provider %q is unavailable (%s): %w",
			cloud.ErrDataUnavailable, id, ReasonSnapshotUnavailable, err)
	}

	return provider, nil
}

// record remembers what resolving a provider produced, so Status can report it
// without building anything itself.
func (r *Registry) record(id cloud.ProviderID, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	status := &Status{ID: id, Enabled: err == nil}
	if err != nil {
		status.Reason = ReasonSnapshotUnavailable
		status.Detail = err.Error()
	}

	r.loaded[id] = status
}

// Registered returns the providers this binary was built to serve, in stable
// lexical order, without building any of them.
//
// This is what "which clouds can this binary answer for" means once providers
// are built on first use: Available reports only what has already been resolved,
// which is empty at startup and would understate the binary's reach.
func (r *Registry) Registered() []cloud.ProviderID {
	ids := make([]cloud.ProviderID, 0, len(r.resolve))
	for _, id := range cloud.ProviderIDs() {
		if r.resolve[id] != nil {
			ids = append(ids, id)
		}
	}

	return ids
}

// Status returns every recognised provider in stable lexical order, reporting
// what is known without resolving anything. A registered provider that has not
// been asked for yet reports ReasonNotLoaded — neither enabled nor broken, just
// not built. Forcing a build here would undo the deferral.
func (r *Registry) Status() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()

	statuses := make([]Status, 0, len(cloud.ProviderIDs()))
	for _, id := range cloud.ProviderIDs() {
		switch {
		case r.loaded[id] != nil:
			statuses = append(statuses, *r.loaded[id])
		case r.resolve[id] != nil:
			statuses = append(statuses, Status{ID: id, Reason: ReasonNotLoaded})
		default:
			statuses = append(statuses, Status{ID: id, Reason: ReasonNotRegistered})
		}
	}

	return statuses
}
