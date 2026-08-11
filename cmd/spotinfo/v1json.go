package main

import (
	"time"

	"spotinfo/internal/cloud"
)

// The AWS v1 root JSON, rendered from neutral candidates.
//
// The query command acquires through cloud.Provider, which models absence: an
// instance the feed does not price carries no price observation, and an advisor
// row with no usable savings figure carries no percentage. v1 published a zero
// for both, and the recorded contract in testdata/aws-root-v1.json must not
// move, so the mapping back to zero lives here rather than in the domain.
//
// Field order is part of the contract: encoding/json emits struct fields in
// declaration order, so these mirror spot.Advice exactly.

// v1Range is the interruption bucket the Spot Advisor published.
type v1Range struct {
	Label string `json:"label"`
	Min   int    `json:"min"`
	Max   int    `json:"max"`
}

// v1Info is the machine block.
//
// emr is the one field no neutral candidate carries: it is a Spot Advisor
// classification with no meaning on another cloud, so the provider seam does not
// map it and it is emitted at its zero value. The MCP v1 surface, which has been
// answering from cloud.Candidate since the seam was added, never published it
// either. The field is retired with this schema.
type v1Info struct {
	Cores int     `json:"cores"`
	EMR   bool    `json:"emr"`
	RAM   float64 `json:"ram_gb"` //nolint:tagliatelle
}

// v1Advice is one row of the AWS v1 root JSON.
type v1Advice struct { //nolint:govet // JSON field order is the contract, not the memory layout.
	Region       string  `json:"region"`
	Instance     string  `json:"instance"`
	InstanceType string  `json:"instance_type"`
	Range        v1Range `json:"range"`
	Savings      int     `json:"savings"`
	Info         v1Info  `json:"info"`
	Price        float64 `json:"price"`
	// Always emitted, unlike the optional fields below it. Price provenance is
	// carried by every other output format — a "*" suffix in text and table, a
	// "Price Source" column in CSV — and under omitempty the JSON form was the
	// only one where "this price came from the static feed" and "this build does
	// not report provenance" looked identical.
	LivePrice      bool               `json:"live_price"`
	ZonePrice      map[string]float64 `json:"zone_price,omitempty"`
	RegionScore    *int               `json:"region_score,omitempty"`
	ZoneScores     map[string]int     `json:"zone_scores,omitempty"`
	ScoreFetchedAt *time.Time         `json:"score_fetched_at,omitempty"`
}

// v1Advices renders a whole answer. The slice is allocated even when empty, so
// an unmatched query stays a parseable "[]" rather than becoming "null".
func v1Advices(candidates []cloud.Candidate) []v1Advice {
	advices := make([]v1Advice, 0, len(candidates))
	for i := range candidates {
		advices = append(advices, v1AdviceOf(&candidates[i]))
	}

	return advices
}

func v1AdviceOf(candidate *cloud.Candidate) v1Advice {
	machine := string(candidate.Machine.ID)

	advice := v1Advice{
		Region:       string(candidate.Location.Region),
		Instance:     machine,
		InstanceType: machine,
		Range:        v1RangeOf(&candidate.Risk),
		Savings:      candidateSavings(candidate),
		Info: v1Info{
			Cores: candidate.Machine.VCPU,
			RAM:   candidate.Machine.MemoryGiB,
		},
		Price:          candidatePrice(candidate),
		LivePrice:      candidateLivePrice(candidate),
		ZonePrice:      v1ZonePrices(candidate),
		ScoreFetchedAt: v1ScoreFetchedAt(candidate),
	}

	if regional := regionalPlacement(candidate); regional != nil {
		score := regional.Score
		advice.RegionScore = &score
	}
	if zonal := zonalPlacements(candidate); len(zonal) > 0 {
		advice.ZoneScores = make(map[string]int, len(zonal))
		for _, placement := range zonal {
			advice.ZoneScores[placement.Location.Zone] = placement.Score
		}
	}

	return advice
}

// v1RangeOf maps the neutral risk observation back to the bucket v1 published.
// An unavailable risk becomes the zero range, which is what an advisor row with
// no bucket rendered as.
func v1RangeOf(risk *cloud.RiskObservation) v1Range {
	bucket := v1Range{Label: risk.Label}
	if risk.MinPercent != nil {
		bucket.Min = int(*risk.MinPercent)
	}
	if risk.MaxPercent != nil {
		bucket.Max = int(*risk.MaxPercent)
	}

	return bucket
}

func v1ZonePrices(candidate *cloud.Candidate) map[string]float64 {
	if len(candidate.ZonePrices) == 0 {
		return nil
	}

	prices := make(map[string]float64, len(candidate.ZonePrices))
	for i := range candidate.ZonePrices {
		prices[candidate.ZonePrices[i].Location.Zone] = candidate.ZonePrices[i].Amount.Float64()
	}

	return prices
}

// v1ScoreFetchedAt reports when the placement scores were fetched. Every
// observation on one candidate comes from the same call, so the first timestamp
// present is the answer.
func v1ScoreFetchedAt(candidate *cloud.Candidate) *time.Time {
	for i := range candidate.Placements {
		if candidate.Placements[i].FetchedAt != nil {
			return candidate.Placements[i].FetchedAt
		}
	}

	return nil
}
