package cloud

import (
	"context"
	"log/slog"
)

// Risk enrichment.
//
// Some risk figures are not in a committed snapshot and cannot be: GCP publishes
// its Spot preemption rate only through an authenticated, per-project API, so it
// is neither redistributable nor the same for two callers. A provider that can
// fetch such a figure implements RiskEnricher, and the recommender calls it on
// the ranked result — never on the catalogue.
//
// Ranked, not raw, for two reasons. One call per (machine, region) against a
// 333-machine catalogue across every region is tens of thousands of requests;
// against the ranked page it is at most Top. And running after ranking means
// enrichment cannot reorder the answer, so a live call that fails leaves the
// same recommendations in the same order, carrying the snapshot's own risk
// status.
//
// It is opt-in at the CLI. The default path stays offline and answers in about a
// tenth of a second, which is the property that makes this tool worth running.

// RiskEnricher is implemented by a provider that can attach a risk observation
// its snapshot does not carry.
//
// Implementations must fail soft: a candidate that cannot be enriched keeps the
// risk it already had. A provider that returns an error for the whole batch
// leaves every candidate untouched, and the caller reports the recommendations
// it already ranked.
type RiskEnricher interface {
	// EnrichRisk attaches risk to the candidates it can, in place.
	EnrichRisk(ctx context.Context, candidates []*Candidate) error
}

// enrichRankedRisk attaches live risk to a ranked page, if the provider can and
// the request asked for it.
//
// Every failure is logged and swallowed. The recommendation is already complete
// without it: risk that could not be fetched stays RiskStatusUnavailable, which
// is the honest answer and the one every consumer already handles.
func enrichRankedRisk(ctx context.Context, provider Provider, request *RecommendRequest, ranked []scored) {
	if !request.EnrichRisk {
		return
	}

	enricher, ok := provider.(RiskEnricher)
	if !ok {
		slog.Debug("provider cannot fetch live risk", slog.String("provider", string(provider.ID())))

		return
	}

	candidates := make([]*Candidate, 0, len(ranked))
	for i := range ranked {
		candidates = append(candidates, ranked[i].candidate)
	}

	if err := enricher.EnrichRisk(ctx, candidates); err != nil {
		slog.Warn("live risk unavailable; reporting the snapshot's risk status",
			slog.String("provider", string(provider.ID())), slog.Any("error", err))
	}
}

// PlacementEnricher is implemented by a provider whose placement figures come
// from an authenticated API rather than from acquisition.
//
// AWS does not implement it: GetSpotPlacementScores takes a whole batch of
// instance types, so its scores are attached while candidates are acquired. GCP
// does, because compute advice.capacity is billed to the caller's own project
// and answers about one machine at a time — asking it for a 333-machine
// catalogue is hundreds of requests for an answer the caller will read three
// rows of.
//
// The same fail-soft rule applies as for risk: a candidate that cannot be
// enriched keeps PlacementStatusUnavailable, which is the honest report of a
// figure nobody could fetch.
type PlacementEnricher interface {
	// EnrichPlacement attaches placement figures to the candidates it can, in
	// place, and records the absence on the ones it cannot.
	EnrichPlacement(ctx context.Context, candidates []*Candidate) error
}

// enrichRankedPlacement attaches placement figures to a ranked page, if the
// provider fetches them separately and the request asked for them.
//
// Ranked, not raw, for the reason above: it is the difference between at most
// Top requests and one per catalogue entry. A provider that attaches its own
// scores during acquisition does not implement the interface and is skipped
// here, so a page never gets two sets of figures.
func enrichRankedPlacement(ctx context.Context, provider Provider, request *RecommendRequest, ranked []scored) {
	if !request.Placement.Enabled {
		return
	}

	enricher, ok := provider.(PlacementEnricher)
	if !ok {
		return
	}

	candidates := make([]*Candidate, 0, len(ranked))
	for i := range ranked {
		candidates = append(candidates, ranked[i].candidate)
	}

	if err := enricher.EnrichPlacement(ctx, candidates); err != nil {
		slog.Warn("live placement figures unavailable; reporting them as unavailable",
			slog.String("provider", string(provider.ID())), slog.Any("error", err))
	}
}
