package main

import (
	"fmt"
	"strings"
	"time"

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

// scoreFlags declares the four placement flags, once, for both commands.
//
// They used to be root-only and `recommend` declared none of them, so a ranked
// page could not ask for a placement figure at all — the one question a caller
// choosing between machines most wants answered. One declaration keeps the two
// commands from drifting into accepting different spellings or bounds.
func scoreFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name: flagWithScore,
			Usage: "include provider placement figures (experimental; on " + string(cloud.ProviderGCP) +
				" the figure is obtainability from a beta Google API, fetched for a recommendation " +
				"and needing --" + flagGCPProject + ")",
		},
		&cli.IntFlag{
			Name: flagMinScore,
			Usage: fmt.Sprintf("filter: minimum spot placement score (1-%d, needs --%s; refused on a cloud "+
				"whose placement figure is not an integer score)", cloud.MaxPlacementScore, flagWithScore),
			DefaultText: unfilteredDefaultText,
		},
		&cli.BoolFlag{
			Name:  flagAZ,
			Usage: "request zone-level figures instead of region-level (use with --" + flagWithScore + ")",
		},
		&cli.IntFlag{
			Name:  flagScoreTimeout,
			Usage: fmt.Sprintf("timeout for placement enrichment, 1-%d seconds", cloud.MaxScoreTimeoutSeconds),
			Value: cloud.DefaultScoreTimeoutSeconds,
		},
	}
}

// placementRequest maps the four score flags onto the neutral request. One
// mapping for both commands: the same flags on either must ask the provider for
// the same thing, or --az means one thing on `list` and another on `recommend`.
//
// Figures are fetched under --with-score; --min-score filters whatever is
// present. validateScoreFlags has already rejected the second without the first.
func placementRequest(ctx *cli.Context) cloud.PlacementRequest {
	request := cloud.PlacementRequest{}

	if lineageBool(ctx, flagWithScore) {
		request.Enabled = true
		request.SingleZone = lineageBool(ctx, flagAZ)
		if timeout := lineageInt(ctx, flagScoreTimeout); timeout > 0 {
			request.Timeout = time.Duration(timeout) * time.Second
		}
	}
	if minScore := lineageInt(ctx, flagMinScore); minScore > 0 {
		request.MinScore = minScore
	}

	return request
}

// validateScoreFlags applies every rule the four flags share, in the order that
// reports the most useful shortfall: the companion rule first, because a range
// check on a filter that cannot run is the wrong thing to say.
func validateScoreFlags(ctx *cli.Context) error {
	if err := requireWithScore(ctx); err != nil {
		return err
	}
	if err := validateScoreFloor(lineageInt(ctx, flagMinScore)); err != nil {
		return err
	}

	return validateScoreTimeout(ctx)
}

// validateScoreTimeout bounds the lookup budget the way internal/mcp bounds
// score_timeout.
//
// The CLI applied `if timeout > 0` and dropped anything else without a word, so
// `--score-timeout -5` and `--score-timeout 99999` were both accepted and
// silently replaced by the provider's own default — the same silent no-op the
// refusals above exist to remove, on the surface that did not have it.
//
// Only a value the caller actually passed is checked. An unset flag carries its
// declared default, and a context that never declared it reads as zero.
func validateScoreTimeout(ctx *cli.Context) error {
	if !lineageIsSet(ctx, flagScoreTimeout) {
		return nil
	}

	if seconds := lineageInt(ctx, flagScoreTimeout); seconds < 1 || seconds > cloud.MaxScoreTimeoutSeconds {
		return fmt.Errorf("%w: --%s must be between 1 and %d seconds",
			cloud.ErrInvalidArgument, flagScoreTimeout, cloud.MaxScoreTimeoutSeconds)
	}

	return nil
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

	// --min-score is an integer floor, and only one of the placement kinds is
	// an integer scale. Refused rather than mapped: reading 8 as an
	// obtainability of 0.8 would be this tool inventing a correspondence
	// between two vendors' measurements, which is the whole thing the kind
	// vocabulary exists to prevent. --with-score still answers there; only the
	// filter has no meaning.
	if lineageIsSet(ctx, flagMinScore) &&
		capabilities.Has(cloud.CapabilityPlacementScore) && !capabilities.SupportsScoreFloor() {
		return refusedFlag(flagMinScore, id, fmt.Sprintf(
			"%s publishes %s, and an integer 1-%d floor states no reviewed mapping onto it",
			id, capabilities.PlacementKind, cloud.MaxPlacementScore))
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

	// Refused the way --live-risk is: each flag names one cloud's mechanism, and
	// passing it to another is a question that cloud cannot be asked.
	if id != cloud.ProviderGCP {
		for _, credential := range gcpCredentials {
			if lineageIsSet(ctx, credential.flag) {
				return refusedFlag(credential.flag, id,
					fmt.Sprintf("the flag names %s, and %s makes none", credential.names, id))
			}
		}
	}

	return nil
}

// gcpCredential is a flag that carries something a Google Cloud call is
// authenticated or billed with.
type gcpCredential struct {
	flag  string
	names string
}

// gcpCredentials is ordered rather than a map, so a caller who passes both is
// told about the same one on every run.
var gcpCredentials = []gcpCredential{
	{flag: flagGCPProject, names: "the project an authenticated " + string(cloud.ProviderGCP) + " call is billed to"},
	{flag: flagGCPBillingKey, names: "the api key a " + string(cloud.ProviderGCP) + " price catalogue lookup is made with"},
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
