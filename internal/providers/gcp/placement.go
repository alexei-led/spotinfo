package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"spotinfo/internal/cloud"
)

// Live obtainability.
//
// Google publishes Spot availability through compute advice.capacity, which is
// authenticated, billed to the caller's own project and explicitly "not a
// guarantee": the answer is a real-time reading that is only valid when it is
// taken. None of that can live in a committed snapshot, so it arrives here on
// request, for the ranked page only — the same shape --live-risk established.
//
// The figure is an obtainability probability in 0.0-1.0 with an uptime
// estimate beside it, and it is neither an AWS placement score nor an
// interruption rate. It says how likely a request for the machine is to
// succeed, which is a different question from how often a running machine is
// taken away. cloud.PlacementKindObtainability keeps the two apart.

const (
	// capacityAdviceEndpoint is the compute beta advice API. Beta because that
	// is the only channel that serves capacity advice; there is no v1 form, and
	// Capabilities reports it as beta for the same reason.
	capacityAdviceEndpoint = "https://compute.googleapis.com/compute/beta/projects/%s/regions/%s/advice/capacity"

	// provisioningModelSpot is the only model this tool asks about.
	provisioningModelSpot = "SPOT"

	// anyZone asks for the best capacity anywhere in the region, which is what a
	// regional observation means. The provider declares no zone detail, so it
	// never asks for a single-zone shape.
	anyZone = "ANY"

	// requestedInstances is how many machines each question asks about.
	// Obtainability is the likelihood of creating the requested number, so the
	// number is part of the question: spotinfo asks "can I get one of these",
	// which is the figure a per-candidate column can honestly carry.
	requestedInstances = 1

	// maxMachineTypesPerRequest is Google's documented ceiling: "You can specify
	// up to five machine types" in one advice call.
	maxMachineTypesPerRequest = 5

	// placementTimeout bounds the whole enrichment when no caller names one.
	placementTimeout = 20 * time.Second

	// obtainabilityMin and obtainabilityMax bound the published probability.
	// Both published schemas declare the field with these bounds, so a reading
	// outside them would be a document that fails its own contract.
	obtainabilityMin = 0.0
	obtainabilityMax = 1.0
)

// ErrNoAdvice reports an answer carrying no recommendation, which is what the
// API returns for a machine it cannot advise on. It is never read as an
// obtainability of zero.
var ErrNoAdvice = errors.New("capacity advice returned no recommendation")

// ErrTooManyMachineTypes reports a request naming more machine types than
// Google accepts. Refused rather than truncated: a silently shortened request
// answers about a different set of machines than the caller asked about.
var ErrTooManyMachineTypes = errors.New("capacity advice accepts at most " +
	"five machine types per request")

// PlacementConfig is what the CLI resolves and hands to the provider for an
// obtainability lookup. It mirrors LiveRiskConfig: ProjectID is required and is
// never guessed from gcloud's active configuration, because the call is billed
// to whatever project it names, and Client and Endpoint are the test seams that
// let the request shape and the response handling be exercised without
// credentials and without reaching Google.
type PlacementConfig struct {
	Client    *http.Client
	ProjectID string
	Endpoint  string
	// Timeout bounds the whole enrichment, not one call. Zero takes
	// placementTimeout.
	Timeout time.Duration
}

func (c *PlacementConfig) endpoint() string { return endpointOr(c.Endpoint, capacityAdviceEndpoint) }

func (c *PlacementConfig) transport() (*http.Client, error) { return transportOr(c.Client) }

func (c *PlacementConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}

	return placementTimeout
}

// WithPlacement returns a provider that fetches obtainability for the ranked
// page. The receiver is unchanged, so the offline provider stays shareable.
func (p *Provider) WithPlacement(config PlacementConfig) *Provider {
	enriched := *p
	enriched.placement = &config

	return &enriched
}

// capacityAdviceRequest is the documented request body.
type capacityAdviceRequest struct {
	InstanceProperties struct {
		Scheduling struct {
			ProvisioningModel string `json:"provisioningModel"`
		} `json:"scheduling"`
	} `json:"instanceProperties"`
	InstanceFlexibilityPolicy struct {
		InstanceSelections map[string]instanceSelection `json:"instanceSelections"`
	} `json:"instanceFlexibilityPolicy"`
	DistributionPolicy struct {
		TargetShape string `json:"targetShape"`
	} `json:"distributionPolicy"`
	Size int `json:"size"`
}

// instanceSelection is one named machine-type option in the flexibility policy.
type instanceSelection struct {
	MachineTypes []string `json:"machineTypes"`
}

type capacityAdviceResponse struct {
	Recommendations []struct {
		Scores struct {
			// Obtainability is a pointer because 0.0 is a real reading — "you are
			// very unlikely to get this" — and an absent one must not read as it.
			Obtainability *float64 `json:"obtainability"`
			// EstimatedUptime is a protobuf duration string, "600s".
			EstimatedUptime string `json:"estimatedUptime"`
		} `json:"scores"`
	} `json:"recommendations"`
}

// capacityAdviceBody builds the request for one machine-region question.
//
// It takes a slice because the wire format takes one: instanceSelections is a
// map of named options, and Google accepts up to maxMachineTypesPerRequest of
// them. spotinfo passes exactly one, and the reason is the response shape
// rather than the request's — see obtainability below. Over the ceiling it
// refuses, so a future caller that batches cannot silently truncate the set it
// asked about.
func capacityAdviceBody(machines []string) ([]byte, error) {
	if len(machines) == 0 {
		return nil, fmt.Errorf("%w: no machine types", cloud.ErrInvalidArgument)
	}
	if len(machines) > maxMachineTypesPerRequest {
		return nil, fmt.Errorf("%w: %d requested", ErrTooManyMachineTypes, len(machines))
	}

	var body capacityAdviceRequest
	body.InstanceProperties.Scheduling.ProvisioningModel = provisioningModelSpot
	body.DistributionPolicy.TargetShape = anyZone
	body.Size = requestedInstances
	body.InstanceFlexibilityPolicy.InstanceSelections = make(map[string]instanceSelection, len(machines))

	for i, machine := range machines {
		body.InstanceFlexibilityPolicy.InstanceSelections[fmt.Sprintf("selection-%d", i)] =
			instanceSelection{MachineTypes: []string{machine}}
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode capacity advice request: %w", err)
	}

	return encoded, nil
}

// EnrichPlacement implements cloud.PlacementEnricher.
//
// One lookup per distinct (machine, region) pair on the page, deduplicated the
// way live risk is. Every candidate ends with an explicit placement status: a
// figure it fetched, or the recorded absence of one. Neither is ever a zero.
func (p *Provider) EnrichPlacement(ctx context.Context, candidates []*cloud.Candidate) error {
	if p.placement == nil {
		return nil
	}

	// Recorded before anything can fail, so a lookup that never runs still
	// leaves every candidate reporting the absence rather than reporting
	// nothing — "asked, and none produced" is a different answer from "nobody
	// asked", and only the candidate can carry the difference.
	for _, candidate := range candidates {
		candidate.PlacementStatus = cloud.PlacementStatusUnavailable
	}

	if err := ValidateProjectID(p.placement.ProjectID); err != nil {
		return err
	}

	client, err := p.placement.transport()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, p.placement.timeout())
	defer cancel()

	attempted, failed, firstErr := perPair(ctx, candidates,
		func(ctx context.Context, machine, region string, group []*cloud.Candidate) error {
			placement, lookupErr := p.obtainability(ctx, client, machine, region)
			if lookupErr != nil {
				slog.Debug("no capacity advice",
					slog.String("machine", machine), slog.String("region", region),
					slog.Any("error", lookupErr))

				return lookupErr
			}

			for _, candidate := range group {
				candidate.Placements = []cloud.PlacementObservation{placement}
				candidate.PlacementStatus = cloud.PlacementStatusAvailable
			}

			return nil
		})

	// One machine Google cannot advise on is routine and stays at Debug. Every
	// lookup failing is one cause for the whole page — the Compute API disabled,
	// an unknown project, no compute.advice permission — and it is
	// indistinguishable in the rendered answer from a page nothing was advised
	// on, because both leave every candidate unavailable.
	if failed > 0 && failed == attempted {
		return fmt.Errorf("all %d capacity advice lookups failed, first: %w", failed, firstErr)
	}

	return nil
}

// obtainability fetches and reads one machine-region advice answer.
func (p *Provider) obtainability(ctx context.Context, client *http.Client,
	machine, region string,
) (cloud.PlacementObservation, error) {
	// One machine type per request, and that is a reading of the response rather
	// than of the limit. A request naming several types answers with a single
	// scores block for the whole set — Google's own reference sends two
	// selections and returns one recommendation over two shards — so a batched
	// figure describes the set, not any member of it. Attaching it to each
	// machine would publish a number Google measured for none of them.
	//
	// The ceiling is maxMachineTypesPerRequest; raising the batch is only
	// correct if Google starts scoring each selection separately.
	encoded, err := capacityAdviceBody([]string{machine})
	if err != nil {
		return cloud.PlacementObservation{}, err
	}

	url := fmt.Sprintf(p.placement.endpoint(), p.placement.ProjectID, region)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return cloud.PlacementObservation{}, fmt.Errorf("build capacity advice request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return cloud.PlacementObservation{}, fmt.Errorf("capacity advice request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorDetailBytes)) //nolint:errcheck // best-effort detail

		return cloud.PlacementObservation{}, fmt.Errorf("capacity advice returned %s: %s", //nolint:err113 // transport detail, not a sentinel
			response.Status, bytes.TrimSpace(detail))
	}

	var decoded capacityAdviceResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return cloud.PlacementObservation{}, fmt.Errorf("decode capacity advice response: %w", err)
	}

	return readAdvice(&decoded, region)
}

// readAdvice turns one advice answer into a placement observation.
//
// A reading outside 0.0-1.0 is refused rather than clamped: both published
// schemas bound the field, so publishing it would emit a document that fails
// its own contract, and clamping would invent a probability Google did not
// give. An unreadable uptime estimate drops that field alone — the
// obtainability is still the figure the caller asked for.
func readAdvice(response *capacityAdviceResponse, region string) (cloud.PlacementObservation, error) {
	if len(response.Recommendations) == 0 {
		return cloud.PlacementObservation{}, ErrNoAdvice
	}

	scores := response.Recommendations[0].Scores
	if scores.Obtainability == nil {
		return cloud.PlacementObservation{}, ErrNoAdvice
	}

	value := *scores.Obtainability
	if value < obtainabilityMin || value > obtainabilityMax {
		return cloud.PlacementObservation{}, fmt.Errorf("%w: obtainability %v is outside %v-%v", //nolint:err113 // source detail, not a sentinel
			cloud.ErrDataUnavailable, value, obtainabilityMin, obtainabilityMax)
	}

	fetchedAt := time.Now().UTC()
	placement := cloud.PlacementObservation{
		FetchedAt:     &fetchedAt,
		Obtainability: &value,
		Location:      cloud.Location{Region: cloud.Region(region)},
		Kind:          cloud.PlacementKindObtainability,
	}

	if scores.EstimatedUptime != "" {
		uptime, err := time.ParseDuration(scores.EstimatedUptime)
		if err != nil {
			slog.Debug("unreadable capacity advice uptime estimate",
				slog.String("estimated_uptime", scores.EstimatedUptime), slog.Any("error", err))
		} else if uptime > 0 {
			placement.EstimatedUptime = &uptime
		}
	}

	return placement, nil
}
