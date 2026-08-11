package main

import (
	"fmt"
	"strings"

	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	awsprovider "spotinfo/internal/providers/aws"
)

// cloudFlagUsage documents --cloud, which is declared with no default value on
// purpose: "not set" must stay distinguishable from "set to aws", or a --cloud
// placed before the subcommand loses to the subcommand's own default and a GCP
// question gets answered with AWS prices. The documented default is applied
// once, in providerID.
const cloudFlagUsage = "cloud provider: aws|gcp|azure (default: aws; GCP/Azure require recommend)"

// machineFilter resolves the machine-type filter from the nearest context that
// set it. --machine is an alias of two different primary names — --type on the
// root command, --instance on recommend — so `spotinfo --machine … recommend …`
// parses cleanly into a flag recommend never reads. Reading only the local
// --instance therefore answers without the caller's filter and reports success.
func machineFilter(ctx *cli.Context) string {
	resolved := flagLineageContext(ctx, flagInstance, flagType, flagMachine)
	if value := resolved.String(flagInstance); value != "" {
		return value
	}

	return resolved.String(flagType)
}

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
// The AWS path deliberately does not build a provider. This command never
// queries one — it holds the legacy client — so what AWS can answer is
// answerable from the static declaration. Building it made the command inherit
// every input the AWS provider needs, so an unreadable architecture manifest or
// sidecar — neither of which this command reads — failed
// `spotinfo --type t3.micro` with SNAPSHOT_UNAVAILABLE while the advisor and
// price data it does read were intact. A genuinely broken advisor or price
// snapshot still fails at acquisition, where the legacy client verifies its own
// payloads against their manifests.
//
// Every other cloud still resolves through the registry, so a disabled provider
// keeps reporting DATA_UNAVAILABLE with its reason code and a shortfall keeps
// naming the capability it lacks, before the identifier check below rejects it.
func resolveAWSProvider(ctx *cli.Context, registry providerRegistry, request cloud.CapabilityRequest) error {
	id, err := providerID(ctx)
	if err != nil {
		return err
	}

	if id == cloud.ProviderAWS {
		if capErr := awsprovider.Capabilities().Require(request); capErr != nil {
			return fmt.Errorf("%s: %w", id, capErr)
		}

		return nil
	}

	provider, err := registry.Get(id)
	if err != nil {
		return err
	}
	// Both remaining outcomes end the same way for the caller: this command
	// cannot answer for this cloud, and `recommend` can. Reporting only the
	// capability shortfall left the one actionable fact unsaid, so a caller read
	// "unsupported capability: risk" as "GCP publishes no spot prices".
	if err := provider.Capabilities().Require(request); err != nil {
		return fmt.Errorf("%s: %w; %s", id, err, recommendHint(id))
	}

	return fmt.Errorf("%w: %s candidates are not served by this command yet; %s",
		cloud.ErrUnsupportedCapability, provider.ID(), recommendHint(id))
}

// recommendHint names the command that does serve a non-AWS cloud. The root
// query command renders an interruption column, which only AWS publishes.
func recommendHint(id cloud.ProviderID) string {
	return fmt.Sprintf("the query command renders interruption risk and is AWS-only; use %q instead",
		appName+" "+recommendCommandName+" --"+flagCloud+" "+string(id))
}
