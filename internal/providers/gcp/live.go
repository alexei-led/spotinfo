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
	"regexp"
	"sync"
	"time"

	"golang.org/x/oauth2/google"

	"spotinfo/internal/cloud"
)

// Live preemption risk.
//
// The committed snapshot cannot carry this figure and never will. Google
// publishes the Spot preemption rate only through compute
// advice.capacityHistory, which is authenticated and scoped to a project: the
// answer differs per caller, so it is neither redistributable nor a property of
// the catalogue. It arrives here instead, on request, for the ranked page only.
//
// Verified against the live API: 26 daily records over a 30-day window for
// n2-standard-4 in us-central1, and the rate differs by region for the same
// machine — 0.081 in us-central1 against 0.125 in europe-west1 — which is
// exactly why it cannot be baked in.

const (
	// capacityHistoryEndpoint is the compute beta advice API. Beta because that
	// is the only channel that serves capacityHistory; there is no v1 form.
	capacityHistoryEndpoint = "https://compute.googleapis.com/compute/beta/projects/%s/regions/%s/advice/capacityHistory"

	// historyTypePreemption is the only history this tool asks for; the
	// snapshot already carries prices.
	historyTypePreemption = "PREEMPTION"

	// riskSourceURL is what the published answer cites as its provenance.
	riskSourceURL = "https://cloud.google.com/compute/docs/instances/spot#preemption-rate"

	// credentialScope is the narrowest OAuth scope that serves the advice API.
	// It is a scope identifier, not a secret: the token is minted by
	// Application Default Credentials at call time.
	credentialScope = "https://www.googleapis.com/auth/compute" //nolint:gosec // G101: an OAuth scope URL, not a credential

	// liveRiskTimeout bounds the whole enrichment, not one call. Enrichment is
	// an optional extra on an answer that is already complete.
	liveRiskTimeout = 20 * time.Second

	// perCallTimeout bounds one machine-region lookup inside that budget.
	perCallTimeout = 5 * time.Second

	// ratioToPercent converts the API's 0.0-1.0 fraction to the percentage
	// every published risk figure uses.
	ratioToPercent = 100

	// maxErrorDetailBytes bounds how much of a failed response is quoted back.
	maxErrorDetailBytes = 512

	// hoursPerDay converts the observed history span to whole days.
	hoursPerDay = 24

	// maxConcurrentLookups caps parallelism. The page is at most cloud.MaxTop
	// rows, and this is an advisory API on the caller's own project quota.
	maxConcurrentLookups = 8
)

// ErrNoProject reports live risk requested without a project to bill it to.
var ErrNoProject = errors.New("gcp live risk needs a project id")

// ErrBadProject reports a project identifier that cannot be one.
var ErrBadProject = errors.New("not a Google Cloud project id")

// projectIDPattern is Google's documented project-id grammar: 6 to 30
// characters, starting with a lowercase letter, lowercase letters, digits and
// hyphens thereafter, not ending in a hyphen.
//
// It is enforced because the identifier is interpolated into the request path.
// An unchecked value containing "/" silently redirects the call to a different
// compute endpoint — still on the contracted host, so no credential escapes,
// but the answer is then a 404 for a resource nobody asked about rather than
// the preemption history. Refusing it names the mistake instead.
var projectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// ValidateProjectID rejects an identifier that is not a project id. The rule is
// quoted in the error so a caller can see what was expected.
func ValidateProjectID(project string) error {
	if project == "" {
		return ErrNoProject
	}
	if !projectIDPattern.MatchString(project) {
		return fmt.Errorf("%w: %q must match %s", ErrBadProject, project, projectIDPattern)
	}

	return nil
}

// LiveRiskConfig is what the CLI resolves and hands to the provider.
type LiveRiskConfig struct {
	// ProjectID is required. It is never guessed from gcloud's active config:
	// the call is billed to whatever project it names, and a tool that picks
	// one silently can bill a project its user never mentioned.
	// Endpoint overrides the advice API base, and Client overrides the
	// authenticated transport. Both are injection points for tests, which is
	// the only way the request shape and the response handling below get
	// exercised without credentials and without reaching Google; production
	// leaves them zero and gets the contracted endpoint over ADC. The AWS
	// live-price and score providers carry the same seam for the same reason.
	Client    *http.Client
	ProjectID string
	Endpoint  string
}

// endpoint returns the advice API URL template for this configuration.
func (c *LiveRiskConfig) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}

	return capacityHistoryEndpoint
}

// transport returns the injected client, or the ADC one resolved on first use.
func (c *LiveRiskConfig) transport() (*http.Client, error) {
	if c.Client != nil {
		return c.Client, nil
	}

	return httpClient()
}

// WithLiveRisk returns a provider that fetches preemption rates for the ranked
// page. The receiver is unchanged, so the offline provider stays shareable.
func (p *Provider) WithLiveRisk(config LiveRiskConfig) *Provider {
	enriched := *p
	enriched.liveRisk = &config

	return &enriched
}

// httpClient resolves Application Default Credentials once.
//
// Lazy and cached, including the failure, for the same reason the AWS
// credential probe is: a machine without ADC must pay the discovery once, not
// once per candidate, and must not pay it at all when nobody asked for live
// risk.
// The context here must outlive this function. google.DefaultClient stores it
// in the token source, which uses it again on every refresh — so a context
// cancelled by a defer in this constructor makes the first real call fail with
// "oauth2: Post .../token: context canceled", before any request is sent. The
// client's own Timeout bounds the exchange instead, and each API call carries
// its own deadline on top.
var httpClient = sync.OnceValues(func() (*http.Client, error) {
	client, err := google.DefaultClient(context.Background(), credentialScope)
	if err != nil {
		return nil, fmt.Errorf("no Google Cloud credentials: %w", err)
	}
	client.Timeout = perCallTimeout

	return client, nil
})

// capacityHistoryRequest is the documented request body. Every field below is
// required: the API rejects the call with a 400 naming the next missing one,
// which is how this shape was established.
type capacityHistoryRequest struct {
	InstanceProperties struct {
		MachineType string `json:"machineType"`
		Scheduling  struct {
			ProvisioningModel string `json:"provisioningModel"`
		} `json:"scheduling"`
	} `json:"instanceProperties"`
	Types []string `json:"types"`
}

type capacityHistoryResponse struct {
	MachineType       string `json:"machineType"`
	PreemptionHistory []struct {
		Interval struct {
			StartTime time.Time `json:"startTime"`
			EndTime   time.Time `json:"endTime"`
		} `json:"interval"`
		PreemptionRate float64 `json:"preemptionRate"`
	} `json:"preemptionHistory"`
}

// EnrichRisk implements cloud.RiskEnricher.
//
// One lookup per distinct (machine, region) pair on the page, deduplicated
// because a page usually repeats a machine across regions or a region across
// machines. A pair that fails keeps the risk it already had.
func (p *Provider) EnrichRisk(ctx context.Context, candidates []*cloud.Candidate) error {
	if p.liveRisk == nil {
		return nil
	}
	if err := ValidateProjectID(p.liveRisk.ProjectID); err != nil {
		return err
	}

	client, err := p.liveRisk.transport()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, liveRiskTimeout)
	defer cancel()

	type key struct{ machine, region string }

	pending := make(map[key][]*cloud.Candidate)
	for _, candidate := range candidates {
		k := key{string(candidate.Machine.ID), string(candidate.Location.Region)}
		pending[k] = append(pending[k], candidate)
	}

	var (
		waitGroup sync.WaitGroup
		mutex     sync.Mutex
		slots     = make(chan struct{}, maxConcurrentLookups)
		failed    int
		firstErr  error
	)

	for k, group := range pending {
		waitGroup.Add(1)

		go func(k key, group []*cloud.Candidate) {
			defer waitGroup.Done()

			slots <- struct{}{}
			defer func() { <-slots }()

			risk, lookupErr := p.preemptionRisk(ctx, client, k.machine, k.region)

			mutex.Lock()
			defer mutex.Unlock()

			if lookupErr != nil {
				slog.Debug("no preemption history",
					slog.String("machine", k.machine), slog.String("region", k.region),
					slog.Any("error", lookupErr))

				failed++
				if firstErr == nil {
					firstErr = lookupErr
				}

				return
			}

			for _, candidate := range group {
				candidate.Risk = risk
			}
		}(k, group)
	}

	waitGroup.Wait()

	// One machine with no history is routine and stays at Debug. Every lookup
	// failing is not: it is one cause for the whole page — the Compute API
	// disabled, an unknown project, no compute.advice permission — and it is
	// indistinguishable from success in the rendered answer, because both leave
	// every candidate at RiskStatusUnavailable. The caller asked for this data
	// explicitly and paid an API call per pair for it, so the batch reports.
	if failed > 0 && failed == len(pending) {
		return fmt.Errorf("all %d preemption lookups failed, first: %w", failed, firstErr)
	}

	return nil
}

// preemptionRisk fetches and summarises one machine-region history.
func (p *Provider) preemptionRisk(ctx context.Context, client *http.Client,
	machine, region string,
) (cloud.RiskObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, perCallTimeout)
	defer cancel()

	var body capacityHistoryRequest
	body.InstanceProperties.MachineType = machine
	body.InstanceProperties.Scheduling.ProvisioningModel = "SPOT"
	body.Types = []string{historyTypePreemption}

	encoded, err := json.Marshal(body)
	if err != nil {
		return cloud.RiskObservation{}, fmt.Errorf("encode capacity history request: %w", err)
	}

	url := fmt.Sprintf(p.liveRisk.endpoint(), p.liveRisk.ProjectID, region)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return cloud.RiskObservation{}, fmt.Errorf("build capacity history request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return cloud.RiskObservation{}, fmt.Errorf("capacity history request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorDetailBytes)) //nolint:errcheck // best-effort detail

		return cloud.RiskObservation{}, fmt.Errorf("capacity history returned %s: %s", //nolint:err113 // transport detail, not a sentinel
			response.Status, bytes.TrimSpace(detail))
	}

	var decoded capacityHistoryResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return cloud.RiskObservation{}, fmt.Errorf("decode capacity history response: %w", err)
	}

	return summarisePreemption(&decoded)
}

// errNoHistory reports an empty series, which the API returns for a machine it
// has not observed rather than as an error.
var errNoHistory = errors.New("capacity history returned no preemption records")

// summarisePreemption reduces the daily series to one published figure.
//
// The range is the observed minimum and maximum, and the label is the mean, so
// a reader sees both the typical rate and how far it moved. The window is
// measured from the series itself: the API returned 16 to 30 records depending
// on the machine, so a hardcoded 30 days would have published a span the data
// does not cover.
func summarisePreemption(response *capacityHistoryResponse) (cloud.RiskObservation, error) {
	records := response.PreemptionHistory
	if len(records) == 0 {
		return cloud.RiskObservation{}, errNoHistory
	}

	lowest, highest, total := records[0].PreemptionRate, records[0].PreemptionRate, 0.0
	earliest, latest := records[0].Interval.StartTime, records[0].Interval.EndTime

	for i := range records {
		rate := records[i].PreemptionRate
		lowest = min(lowest, rate)
		highest = max(highest, rate)
		total += rate

		if records[i].Interval.StartTime.Before(earliest) {
			earliest = records[i].Interval.StartTime
		}
		if records[i].Interval.EndTime.After(latest) {
			latest = records[i].Interval.EndTime
		}
	}

	mean := total / float64(len(records))
	minPercent, maxPercent := lowest*ratioToPercent, highest*ratioToPercent
	observedAt := latest

	risk := cloud.RiskObservation{
		Status:     cloud.RiskStatusAvailable,
		Kind:       cloud.RiskKindPreemptionRate,
		Label:      fmt.Sprintf("%.1f%% avg", mean*ratioToPercent),
		MinPercent: &minPercent,
		MaxPercent: &maxPercent,
		SourceURL:  riskSourceURL,
		ObservedAt: &observedAt,
	}

	if days := int(latest.Sub(earliest).Hours() / hoursPerDay); days > 0 {
		risk.Window = &cloud.HistoryWindow{Days: days}
	}

	return risk, nil
}
