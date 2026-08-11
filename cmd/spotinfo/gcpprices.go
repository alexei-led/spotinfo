package main

import (
	"os"
	"strings"

	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	gcpprovider "spotinfo/internal/providers/gcp"
)

// Opting into live GCP prices, and the regions they unlock.
//
// The committed GCP catalogue covers us-central1. Google's Cloud Billing
// Catalog API covers every region, needs an API key and no IAM permission, and
// states no redistribution terms — so it can answer one invocation but may
// never reach a snapshot. --gcp-billing-key is that opt-in: with a key, a
// caller can ask for any region; without one, the answer is exactly what it was
// before, which for another region is no candidates.

// gcpBillingKeyEnv is the variable the flag falls back to.
//
// It is spotinfo's own name rather than Google's generic GOOGLE_API_KEY on
// purpose. The key is the only opt-in this path has — there is no companion
// boolean the way --live-risk gates the preemption lookup — so an ambient
// variable exported for some other Google API would silently turn on a network
// call billed to a project the caller never mentioned here. A name only this
// tool reads cannot be lying around by accident.
const gcpBillingKeyEnv = "SPOTINFO_GCP_BILLING_KEY" //nolint:gosec // G101: an environment variable name, not a key

// gcpBillingKeyFlag names the key. Both commands carry it: `list` browses a
// region and `recommend` ranks within one, and a region either is reachable or
// is not — the same concept, so it has one name on both.
//
// Declared without urfave/cli EnvVars for the reason gcpProjectFlag records: an
// EnvVars-backed flag reports itself as *set*, and refuseUnsupportedFlags
// refuses this flag off GCP, so an exported variable would refuse every default
// AWS invocation on a machine that has one. The fallback is read explicitly
// below, which keeps "the caller named a key" distinguishable from "a key is in
// the environment".
func gcpBillingKeyFlag() *cli.StringFlag {
	return &cli.StringFlag{
		Name: flagGCPBillingKey,
		Usage: "Cloud Billing Catalog API key that prices GCP regions beyond the committed snapshot " +
			"for this invocation (or $" + gcpBillingKeyEnv + ")",
	}
}

// gcpBillingKeyFromEnv reads the key the environment carries, if any. It is the
// only way the MCP surface reaches this path: that surface takes no credential
// as a tool argument, the way it takes no GCP project.
func gcpBillingKeyFromEnv() string { return strings.TrimSpace(os.Getenv(gcpBillingKeyEnv)) }

// resolveGCPBillingKey reads the key this invocation should use: the flag
// first, then the environment. An absent key is not an error — it is the
// offline provider, which is the documented default.
func resolveGCPBillingKey(ctx *cli.Context) string {
	if key := strings.TrimSpace(lineageString(ctx, flagGCPBillingKey)); key != "" {
		return key
	}

	return gcpBillingKeyFromEnv()
}

// withGCPPrices returns the provider the request should run against, wired for
// live prices when the caller supplied a key.
//
// Any other cloud is returned unchanged: AWS refreshes through its acquisition
// client and Azure through its own anonymous sweep, and neither has anything to
// do with a Google API key. The data policy travels with the key so --offline
// makes no request at all and --refresh ignores the cached catalogue, which is
// what declaring CapabilityLiveEnrichment for GCP promises a caller.
func withGCPPrices(ctx *cli.Context, provider cloud.Provider) cloud.Provider {
	gcp, ok := provider.(*gcpprovider.Provider)
	if !ok {
		return provider
	}

	key := resolveGCPBillingKey(ctx)
	if key == "" {
		return provider
	}

	return gcp.WithLivePrices(gcpprovider.LivePriceConfig{APIKey: key, Policy: fetchPolicy(ctx)})
}
