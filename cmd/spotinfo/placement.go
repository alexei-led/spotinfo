package main

import (
	"log/slog"

	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	gcpprovider "spotinfo/internal/providers/gcp"
)

// Opting into authenticated placement figures.
//
// AWS attaches its placement scores while candidates are acquired: one API call
// carries a whole batch of instance types. GCP cannot — compute advice.capacity
// answers about one machine at a time and is billed to the caller's own project
// — so its figures are fetched for the ranked page, exactly the way --live-risk
// fetches preemption rates. `list` publishes none on GCP, and the provider says
// so rather than rendering an empty column.

// withPlacement returns the provider the request should run against, wired for
// authenticated placement figures when the caller asked for them.
//
// A provider that attaches its own scores during acquisition is returned
// unchanged: it needs no project and no second lookup.
func withPlacement(ctx *cli.Context, provider cloud.Provider, request *cloud.RecommendRequest) (cloud.Provider, error) {
	if !request.Placement.Enabled {
		return provider, nil
	}

	capabilities := provider.Capabilities()
	// Said once, on the surface that asked, because nothing in the published
	// answer distinguishes a GA interface from a preview one. The figure is
	// worth having; a caller building on it is entitled to know it can change.
	if capabilities.PlacementBeta {
		log.Warn("placement figures come from a provider beta API and can change without notice",
			slog.String("cloud", string(provider.ID())),
			slog.String("kind", string(capabilities.PlacementKind)))
	}

	gcp, ok := provider.(*gcpprovider.Provider)
	if !ok {
		return provider, nil
	}

	project, err := resolveGCPProject(ctx, flagWithScore)
	if err != nil {
		return nil, err
	}

	return gcp.WithPlacement(gcpprovider.PlacementConfig{
		ProjectID: project,
		// --score-timeout bounds the lookup here the way it bounds the AWS one,
		// so the flag means the same thing on both clouds.
		Timeout: request.Placement.Timeout,
	}), nil
}
