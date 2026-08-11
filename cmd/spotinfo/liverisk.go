package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	gcpprovider "spotinfo/internal/providers/gcp"
)

// Opting into authenticated risk.
//
// GCP publishes its Spot preemption rate only through an authenticated,
// per-project API, so it cannot live in the committed snapshot and cannot be
// the default: the offline path answers in about a tenth of a second and needs
// no credentials, which is the property that makes this tool worth running.
//
// --live-risk turns it on for one invocation. It costs one API call per
// recommendation on the ranked page, and it needs a project.

// gcpProjectEnv is the variable the Google SDKs read. Accepted so a shell that
// already exports it needs no flag. The flag names themselves are vocabulary
// entries and live in vocabulary.go.
const gcpProjectEnv = "GOOGLE_CLOUD_PROJECT"

// unsupportedLiveRisk is the one refusal, shared by both report paths.
//
// Worded as "not implemented for", not "publishes no risk": AWS does publish
// risk, from the Spot Advisor feed, and already reports it without this flag.
// The flag names one mechanism — the authenticated GCP preemption lookup — and
// refusing it is not a claim about what any other cloud measures.
//
// Azure gets its own sentence because the difference is real and a reader has
// to be able to see it. Azure publishes an eviction rate; this build simply
// cannot read it, for the same reason it serves no Spot Placement Score. That
// is a decision about the binary, not a fact about the vendor.
func unsupportedLiveRisk(id cloud.ProviderID) error {
	if id == cloud.ProviderAzure {
		return refusedFlag(flagLiveRisk, id,
			azureSubscriptionReason("an eviction rate through Azure Resource Graph"))
	}

	return refusedFlag(flagLiveRisk, id,
		fmt.Sprintf("the flag fetches %s preemption rates and is implemented for no other cloud", cloud.ProviderGCP))
}

// rejectLiveRiskOffGCP runs before the report path is chosen.
//
// The v1 AWS path never reaches withLiveRisk, so without this `--live-risk` was
// accepted and silently ignored there while the same flag errored on Azure —
// the same flag on the same command meaning two different things depending on
// which schema happened to answer.
func rejectLiveRiskOffGCP(ctx *cli.Context, id cloud.ProviderID) error {
	if !lineageBool(ctx, flagLiveRisk) || id == cloud.ProviderGCP {
		return nil
	}

	return unsupportedLiveRisk(id)
}

// liveRiskFlags are declared on recommend, where risk is ranked against. The
// project flag is declared on both commands, so it is separate.
func liveRiskFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name:  flagLiveRisk,
			Usage: "fetch live preemption risk from the provider's authenticated API (GCP only; needs credentials)",
		},
		gcpProjectFlag(),
	}
}

// gcpProjectFlag names the project an authenticated GCP call is billed to. Both
// commands carry it: the authenticated paths are per-command, but the project
// they bill is one concept and must have one name.
//
// The environment variable is read by withLiveRisk below, not declared here as
// urfave/cli EnvVars. Both mechanisms produce the same project, but EnvVars also
// makes the flag report itself as *set* — measured: with GOOGLE_CLOUD_PROJECT
// exported, ctx.IsSet and LocalFlagNames both name --gcp-project on a bare
// `spotinfo list`. refuseUnsupportedFlags refuses this flag off GCP, so under
// EnvVars an exported variable would refuse every default AWS invocation on a
// machine that has ever touched GCP. One explicit read keeps "the caller named a
// project" distinguishable from "a project is lying around in the environment".
func gcpProjectFlag() *cli.StringFlag {
	return &cli.StringFlag{
		Name:  flagGCPProject,
		Usage: "Google Cloud project to bill authenticated GCP calls to (or $" + gcpProjectEnv + ")",
	}
}

// withLiveRisk returns the provider the request should run against, wired for
// authenticated risk when the caller asked for it.
//
// The project is never taken from gcloud's active configuration. That value is
// ambient and frequently not what the caller means — the machine this was built
// on had `core/project` and `billing/quota_project` set to two different
// projects — and these calls are billed to whichever project they name. It has
// to be stated.
func withLiveRisk(ctx *cli.Context, provider cloud.Provider, request *cloud.RecommendRequest) (cloud.Provider, error) {
	if !lineageBool(ctx, flagLiveRisk) {
		return provider, nil
	}

	gcp, ok := provider.(*gcpprovider.Provider)
	if !ok {
		return nil, unsupportedLiveRisk(provider.ID())
	}

	project := strings.TrimSpace(lineageString(ctx, flagGCPProject))
	if project == "" {
		project = strings.TrimSpace(os.Getenv(gcpProjectEnv))
	}
	if project == "" {
		return nil, fmt.Errorf("%w: --%s needs a project; pass --%s or set %s",
			cloud.ErrInvalidArgument, flagLiveRisk, flagGCPProject, gcpProjectEnv)
	}
	// Refused here rather than at the request, because the identifier is
	// interpolated into the API path: an unchecked value redirects the call to a
	// different compute endpoint, whose 404 is then reported as "no preemption
	// history" for every machine on the page.
	if err := gcpprovider.ValidateProjectID(project); err != nil {
		return nil, fmt.Errorf("%w: %w", cloud.ErrInvalidArgument, err)
	}

	request.EnrichRisk = true

	return gcp.WithLiveRisk(gcpprovider.LiveRiskConfig{ProjectID: project}), nil
}
