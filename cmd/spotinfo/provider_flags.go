package main

import (
	"fmt"
	"strings"

	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
)

const (
	// flagCloud selects the provider. It is declared with no default value on
	// purpose: "not set" must stay distinguishable from "set to aws", or a
	// --cloud placed before the subcommand loses to the subcommand's own
	// default and a GCP question gets answered with AWS prices. The documented
	// default is applied once, in providerID.
	flagCloud = "cloud"
	// flagMachine is the neutral name for a machine-type filter. The AWS
	// spellings stay primary — --type on the root command, --instance on
	// recommend — so every documented AWS invocation keeps working; --machine
	// is declared as an alias of each.
	flagMachine = "machine"

	cloudFlagUsage = "cloud provider: aws|gcp|azure (default: aws)"
)

// providerRegistry is the slice of the compiled provider registry the CLI
// needs, defined next to its consumer.
type providerRegistry interface {
	Get(id cloud.ProviderID) (cloud.Provider, error)
}

// cloudFlag declares --cloud. Both the root command and recommend carry it so
// it can be given on either side of the subcommand.
func cloudFlag() *cli.StringFlag {
	return &cli.StringFlag{Name: flagCloud, Usage: cloudFlagUsage}
}

// providerID resolves --cloud from the nearest context that set it. An unset
// flag is the documented AWS default; anything else must name a recognised
// provider.
func providerID(ctx *cli.Context) (cloud.ProviderID, error) {
	value := strings.TrimSpace(lineageString(ctx, flagCloud))
	if value == "" {
		return cloud.ProviderAWS, nil
	}

	return cloud.ParseProviderID(value)
}

// resolveProvider turns --cloud into a usable provider. The order is fixed and
// every step runs before acquisition: an unrecognised value is
// ErrInvalidArgument, a recognised but disabled provider is ErrDataUnavailable
// carrying the registry's reason code, and the capability request is checked
// last so the reported shortfall names a provider that actually exists.
func resolveProvider(ctx *cli.Context, registry providerRegistry, request cloud.CapabilityRequest) (cloud.Provider, error) {
	id, err := providerID(ctx)
	if err != nil {
		return nil, err
	}

	provider, err := registry.Get(id)
	if err != nil {
		return nil, err
	}
	if err := provider.Capabilities().Require(request); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}

	return provider, nil
}

// requestedOS reports the operating system as a capability question. A value
// outside the neutral vocabulary is not one: the provider owns that error, and
// AWS already reports it with its own wording.
func requestedOS(value string) cloud.OperatingSystem {
	instanceOS, err := cloud.ParseOperatingSystem(value)
	if err != nil {
		return ""
	}

	return instanceOS
}

// requestedArchitecture mirrors requestedOS: an unreviewed architecture string
// is rejected by the recommendation validator, not by the capability gate.
func requestedArchitecture(value string) cloud.Architecture {
	architecture, err := cloud.ParseArchitecture(value)
	if err != nil {
		return ""
	}

	return architecture
}

// rootCapabilityRequest is what the root query command renders: spot prices,
// machine specifications and an interruption-frequency column. Placement and
// zone capabilities are demanded only when the matching flags ask for them.
func rootCapabilityRequest(ctx *cli.Context) cloud.CapabilityRequest {
	needed := []cloud.Capability{cloud.CapabilitySpotPrice, cloud.CapabilityMachineSpec, cloud.CapabilityRisk}
	if ctx.Bool(flagWithScore) || ctx.Int(flagMinScore) > 0 {
		needed = append(needed, cloud.CapabilityPlacementScore)
	}
	if ctx.Bool(flagAZ) {
		needed = append(needed, cloud.CapabilityZoneDetail)
	}

	return cloud.CapabilityRequest{OS: requestedOS(ctx.String(flagOS)), Needed: needed}
}

// recommendCapabilityRequest describes v1 recommendations: every workload
// (web, ci, batch) caps interruption frequency, so risk is required. The
// risk-free cost policy arrives with the v2 schema.
func recommendCapabilityRequest(ctx *cli.Context) cloud.CapabilityRequest {
	return cloud.CapabilityRequest{
		OS:           requestedOS(lineageString(ctx, flagOS)),
		Architecture: requestedArchitecture(ctx.String(flagArchitecture)),
		Needed:       []cloud.Capability{cloud.CapabilitySpotPrice, cloud.CapabilityMachineSpec, cloud.CapabilityRisk},
	}
}

// resolveAWSProvider is the provider gate for the root query command, which
// still acquires candidates through the legacy AWS client. Selecting any other
// provider fails here rather than being answered with AWS data.
//
// The identifier check is unreachable today — the root command requires the
// risk capability and only AWS declares it, so another cloud is already
// rejected by resolveProvider. It stays because it is the guard that survives
// that coincidence: the day a provider publishes risk, the capability gate
// would let it through to an acquisition path that only speaks AWS.
func resolveAWSProvider(ctx *cli.Context, registry providerRegistry, request cloud.CapabilityRequest) error {
	provider, err := resolveProvider(ctx, registry, request)
	if err != nil {
		return err
	}
	if provider.ID() != cloud.ProviderAWS {
		return fmt.Errorf("%w: %s candidates are not served by this command yet", cloud.ErrUnsupportedCapability, provider.ID())
	}

	return nil
}
