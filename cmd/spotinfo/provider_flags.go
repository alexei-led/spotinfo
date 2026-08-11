package main

import (
	"fmt"
	"strings"

	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
)

// cloudFlagUsage documents --cloud, which is declared with no default value on
// purpose: "not set" must stay distinguishable from "set to aws". The documented
// default is applied once, in providerID.
const cloudFlagUsage = "cloud provider: aws|gcp|azure (default: aws)"

// machineFilter resolves the machine-type filter from the nearest context that
// set it. One name, one concept: both commands declare --machine, so this reads
// the same flag wherever it was given.
func machineFilter(ctx *cli.Context) string {
	return lineageString(ctx, flagMachine)
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

// The recommend command declares no capability request of its own any more.
// cloud.Recommend derives one from the request — the workload decides whether
// published risk is needed — and checks it before acquisition, so a second
// hand-written copy here could only drift from it.

// Refusing a flag that cannot act.
//
// A silently ignored flag is worse than a rejected one: it answers a question
// nobody asked and exits 0, so a caller reads the result as though the filter
// had been applied. Everything below runs after the provider is resolved and
// before it is queried — Invariant 3, a refusal costs no I/O.
//
// The wording separates two facts on purpose. "publishes no X" is a vendor
// fact and there is nothing a caller can do about it; "publishes X, and this
// build does not authenticate to it" is a decision this repository made, and it
// is recorded in docs/reviews/multicloud-parity.md. A reader who cannot tell
// them apart cannot tell whether to wait for a vendor or to ask for a feature.

// refusedFlag is the one shape every flag refusal takes: the flag, the cloud,
// and why.
func refusedFlag(flag string, id cloud.ProviderID, reason string) error {
	return fmt.Errorf("%w: --%s is refused on %s: %s", cloud.ErrUnsupportedCapability, flag, id, reason)
}

// azureSubscriptionReason is the wording this build owes a reader twice, for
// --with-score and for --live-risk. Azure publishes both figures; neither is
// served because reading them needs a credential chain that costs +4.83 MB of
// binary and that nothing here could exercise even once.
func azureSubscriptionReason(published string) string {
	return "azure publishes " + published +
		", but reading it needs an Azure subscription this build does not authenticate to"
}

// refuseUnsupportedFlags reports the first flag the chosen cloud cannot act on.
//
// Each check reads the provider's own declaration rather than its identifier,
// so a cloud that gains the capability retires its refusal by declaring it —
// no edit here, and no edit in the tests.
func refuseUnsupportedFlags(ctx *cli.Context, provider cloud.Provider) error {
	id := provider.ID()
	capabilities := provider.Capabilities()

	if lineageBool(ctx, flagWithScore) && !capabilities.Has(cloud.CapabilityPlacementScore) {
		return refusedFlag(flagWithScore, id, placementScoreReason(id))
	}

	// --offline and --refresh describe what to do with a live feed. A cloud
	// answered entirely from its committed snapshot has none, so both were
	// accepted and ignored there: --offline promised a snapshot answer that was
	// already the only kind available, and --refresh promised a fetch that never
	// happened.
	if !capabilities.Has(cloud.CapabilityLiveEnrichment) {
		if lineageBool(ctx, flagOffline) {
			return refusedFlag(flagOffline, id,
				"it answers from its committed snapshot, so there is no live feed to skip")
		}

		if lineageBool(ctx, flagRefresh) {
			return refusedFlag(flagRefresh, id,
				"it answers from its committed snapshot, so there is no fetched feed to ignore")
		}
	}

	// Refused the way --live-risk is: the flag names one cloud's mechanism, and
	// passing it to another is a question that cloud cannot be asked.
	if id != cloud.ProviderGCP && lineageIsSet(ctx, flagGCPProject) {
		return refusedFlag(flagGCPProject, id,
			fmt.Sprintf("the flag names the project an authenticated %s call is billed to, and %s makes none",
				cloud.ProviderGCP, id))
	}

	return nil
}

// placementScoreReason says why a cloud serves no placement score.
func placementScoreReason(id cloud.ProviderID) string {
	if id == cloud.ProviderAzure {
		return azureSubscriptionReason("a Spot Placement Score")
	}

	return fmt.Sprintf("it publishes no %s", cloud.CapabilityPlacementScore)
}

// scoreCompanion is a flag that only means something once --with-score has
// fetched the placement scores it describes.
type scoreCompanion struct {
	flag   string
	acting string
}

// scoreCompanions is ordered rather than a map, so a caller who passes two of
// them is told about the same one on every run.
var scoreCompanions = []scoreCompanion{
	{flag: flagAZ, acting: "splits by zone"},
	{flag: flagMinScore, acting: "filters on"},
	{flag: flagScoreTimeout, acting: "waits for"},
}

// requireWithScore refuses a companion flag given on its own.
//
// None of the three does anything without --with-score: --min-score compared
// every candidate against an absent score and dropped all of them, and --az and
// --score-timeout described a lookup that was never made. All three exited 0.
//
// This is a malformed flag combination, not a capability shortfall, so it is
// ErrInvalidArgument and it runs after the capability gate: on a cloud that
// publishes no placement score at all, "this cloud has none" is the answer that
// helps, because adding --with-score there would not.
func requireWithScore(ctx *cli.Context) error {
	if lineageBool(ctx, flagWithScore) {
		return nil
	}

	for _, companion := range scoreCompanions {
		if lineageIsSet(ctx, companion.flag) {
			return fmt.Errorf("%w: --%s needs --%s, which is what fetches the placement scores it %s",
				cloud.ErrInvalidArgument, companion.flag, flagWithScore, companion.acting)
		}
	}

	return nil
}
