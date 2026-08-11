package spot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/bluele/gcache"
	"golang.org/x/time/rate"
)

// Constants to replace magic numbers
const (
	// Cache configuration
	defaultCacheSize       = 1000
	defaultCacheExpiration = 10 * time.Minute
	defaultRateLimitBurst  = 10

	// Rate limiting configuration
	rateLimitInterval = 100 * time.Millisecond

	// AWS API configuration
	defaultTargetCapacity = 1
	defaultMaxResults     = 30
	maxRetryAttempts      = 5
	// defaultScoreTimeout bounds a lookup this client was given no timeout for.
	// It is the client's own fallback, not a surface default: the value the CLI
	// flag and the MCP argument advertise is cloud.DefaultScoreTimeoutSeconds,
	// which is where a caller-facing default belongs.
	defaultScoreTimeout = 30 * time.Second
)

// errScoreProviderUnavailable wraps every placement-score failure. Reported
// rather than substituted: a synthetic score is indistinguishable from a real
// one and would silently misinform capacity decisions.
//
// It is reported bare when the credential probe found nothing — the common
// case, and now the fast one — and wrapped around the cause when a request that
// did carry credentials was refused, which is where the permission half of the
// message earns its place.
var errScoreProviderUnavailable = errors.New(
	"spot placement scores unavailable: requires AWS credentials and the ec2:GetSpotPlacementScores permission")

// placementScore is one score AWS returned, together with the placement it
// applies to: a Region normally, or an AvailabilityZoneId when singleAZ was
// requested. Carrying the identity through avoids reconstructing it — the AZ
// name used to be synthesised as region+"a", which was wrong whenever AWS
// scored any zone other than the first.
//
// AWS scores the request as a whole, not each instance type, so one score
// applies to every instance type in that request.
type placementScore struct {
	placement string
	score     int
}

// awsAPIProvider provides spot placement scores with different implementations.
type awsAPIProvider interface {
	fetchScores(ctx context.Context, region string, instanceTypes []string, singleAZ bool) ([]placementScore, error)
}

// awsScoreProvider implements awsAPIProvider using real AWS API calls.
type awsScoreProvider struct {
	// newClient builds the region-scoped EC2 client. Injectable so the response
	// handling below can be tested without reaching AWS; production leaves it nil
	// and gets ec2.NewFromConfig over lazily resolved credentials.
	newClient func(region string) ec2.GetSpotPlacementScoresAPIClient
}

// scoresClient returns the injected client factory, or a real EC2 one built
// from credentials resolved on first use.
//
// Lazy for the same reason as the live-price client: resolving credentials in
// the constructor charged the probe to every invocation, including the ones
// that never ask for a score.
func (p *awsScoreProvider) scoresClient(region string) (ec2.GetSpotPlacementScoresAPIClient, error) {
	if p.newClient != nil {
		return p.newClient(region), nil
	}

	cfg, err := awsConfigWithCredentials()
	if err != nil {
		return nil, err
	}

	return ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		o.Region = region
	}), nil
}

// CachedScoreData wraps scores with timestamp for freshness tracking.
type CachedScoreData struct {
	FetchTime time.Time
	Scores    []placementScore
}

// FreshnessLevel indicates how fresh the cached data is.
type FreshnessLevel int

const (
	// Fresh data is less than 5 minutes old
	Fresh FreshnessLevel = iota
	// Recent data is between 5 and 30 minutes old
	Recent
	// Stale data is more than 30 minutes old
	Stale
)

// GetFreshness returns the freshness level based on the fetch time.
func (c *CachedScoreData) GetFreshness() FreshnessLevel {
	age := time.Since(c.FetchTime)
	switch {
	case age < 5*time.Minute:
		return Fresh
	case age < 30*time.Minute:
		return Recent
	default:
		return Stale
	}
}

// scoreCache implements the main score caching and rate limiting functionality.
type scoreCache struct {
	cache    gcache.Cache
	limiter  *rate.Limiter
	provider awsAPIProvider
}

// newScoreCache creates a new score cache with rate limiting and AWS provider.
//
//nolint:contextcheck // Initialization function appropriately uses context.Background() for AWS config
func newScoreCache() *scoreCache {
	cache := gcache.New(defaultCacheSize).
		LRU().
		Expiration(defaultCacheExpiration).
		Build()

	limiter := rate.NewLimiter(rate.Every(rateLimitInterval), defaultRateLimitBurst)

	// Try to create AWS provider, fallback to mock on error
	provider := createAPIProvider()

	return &scoreCache{
		cache:    cache,
		limiter:  limiter,
		provider: provider,
	}
}

// createAPIProvider creates an AWS API provider, or nil when AWS is unavailable.
//
// It deliberately does NOT fall back to synthetic scores. Placement scores drive
// capacity decisions, so an invented number presented as an AWS score is worse
// than an explicit failure: the caller asked for scores with --with-score and is
// entitled to know they could not be obtained.
//
//nolint:contextcheck // Initialization function appropriately uses context.Background() for AWS config
func createAPIProvider() awsAPIProvider {
	provider, err := newAWSScoreProvider(context.Background())
	if err != nil {
		slog.Debug("placement score provider unavailable",
			slog.Any("error", err))

		return nil
	}

	return provider
}

// newAWSScoreProvider creates a new AWS score provider. Credentials are
// resolved on first use, in scoresClient — see the note there on why not here.
func newAWSScoreProvider(_ context.Context) (*awsScoreProvider, error) {
	return &awsScoreProvider{}, nil
}

// fetchScores implements awsAPIProvider for AWS API calls.
func (p *awsScoreProvider) fetchScores(ctx context.Context, region string, instanceTypes []string, singleAZ bool) ([]placementScore, error) {
	client, err := p.scoresClient(region)
	if err != nil {
		return nil, err
	}

	input := &ec2.GetSpotPlacementScoresInput{
		InstanceTypes:          instanceTypes,
		TargetCapacity:         aws.Int32(defaultTargetCapacity),
		SingleAvailabilityZone: aws.Bool(singleAZ),
		MaxResults:             aws.Int32(defaultMaxResults),
		// Narrow the scoring to the region actually asked about. A region-scoped
		// client only selects the endpoint; without this AWS scores every region
		// it considers viable, and with MaxResults the requested one can be
		// crowded out entirely.
		RegionNames: []string{region},
	}

	var scores []placementScore

	paginator := ec2.NewGetSpotPlacementScoresPaginator(client, input)

	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get spot placement scores for region %s: %w", region, err)
		}

		for _, result := range output.SpotPlacementScores {
			score := int(aws.ToInt32(result.Score))

			// Every result must belong to the requested region, in both modes.
			// AZ ids are opaque, so a zone from elsewhere is undetectable to the
			// user once it lands in ZoneScores.
			if aws.ToString(result.Region) != region {
				continue
			}

			if singleAZ {
				// AWS identifies the zone; keep only scored zones.
				if az := aws.ToString(result.AvailabilityZoneId); az != "" {
					scores = append(scores, placementScore{placement: az, score: score})
				}

				continue
			}

			scores = append(scores, placementScore{placement: region, score: score})
		}
	}

	// A placement AWS did not score is deliberately absent rather than filled
	// with a middle-of-the-range default: a synthesised score is
	// indistinguishable from a real one.
	return scores, nil
}

// dedupe drops repeated messages while preserving order. With --region all a
// single cause (missing credentials, say) otherwise produces the same sentence
// once per region.
func dedupe(msgs []string) []string {
	seen := make(map[string]bool, len(msgs))
	out := make([]string, 0, len(msgs))

	for _, m := range msgs {
		if seen[m] {
			continue
		}

		seen[m] = true

		out = append(out, m)
	}

	return out
}

// getCacheKey creates a consistent cache key for region and instance types.
func (sc *scoreCache) getCacheKey(region string, instanceTypes []string, singleAZ bool) string {
	sorted := make([]string, len(instanceTypes))
	copy(sorted, instanceTypes)
	sort.Strings(sorted)

	azFlag := "region"
	if singleAZ {
		azFlag = "az"
	}

	return fmt.Sprintf("%s:%s:%s", region, azFlag, strings.Join(sorted, ","))
}

// getSpotPlacementScores fetches spot placement scores with caching and rate limiting.
func (sc *scoreCache) getSpotPlacementScores(ctx context.Context, region string,
	instanceTypes []string, singleAZ bool) ([]placementScore, error) {

	if sc.provider == nil {
		return nil, errScoreProviderUnavailable
	}

	cacheKey := sc.getCacheKey(region, instanceTypes, singleAZ)

	// Check cache first
	if cached, err := sc.cache.Get(cacheKey); err == nil {
		if cachedData, ok := cached.(*CachedScoreData); ok {
			return cachedData.Scores, nil
		}
	}

	// Apply rate limiting
	if err := sc.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit wait failed: %w", err)
	}

	scores, err := sc.provider.fetchScores(ctx, region, instanceTypes, singleAZ)
	if err != nil {
		// Wrapped here, the one choke point every failure passes through, so the
		// guidance reaches the user exactly once regardless of the cause.
		return nil, fmt.Errorf("%w: %w", errScoreProviderUnavailable, err)
	}

	// Cache the result with timestamp (ignore error as it's not critical)
	cachedData := &CachedScoreData{
		Scores:    scores,
		FetchTime: time.Now(),
	}
	_ = sc.cache.Set(cacheKey, cachedData)

	return scores, nil
}

// enrichWithScores enriches advice slice with spot placement scores.
func (sc *scoreCache) enrichWithScores(ctx context.Context, advices []Advice,
	singleAZ bool, timeout time.Duration) error {

	enrichCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Group advices by region for batch processing
	regionGroups := make(map[string][]*Advice)
	for i := range advices {
		region := advices[i].Region
		regionGroups[region] = append(regionGroups[region], &advices[i])
	}

	// Process each region in parallel
	var wg sync.WaitGroup
	var mu sync.Mutex
	var fetchErrs []string

	for region, regionAdvices := range regionGroups {
		wg.Add(1)
		go func(r string, advs []*Advice) {
			defer wg.Done()

			// Collect the unique instance types to ask AWS about. A per-type
			// index is no longer kept: AWS scores the request as a whole, so
			// every returned score applies to every advice in this region.
			instanceTypeSet := make(map[string]bool)

			for _, adv := range advs {
				instanceType := adv.InstanceType
				if instanceType == "" {
					instanceType = adv.Instance
				}

				instanceTypeSet[instanceType] = true
			}

			// Convert set to slice
			instanceTypes := make([]string, 0, len(instanceTypeSet))
			for instanceType := range instanceTypeSet {
				instanceTypes = append(instanceTypes, instanceType)
			}

			// Fetch scores for this region
			scores, err := sc.getSpotPlacementScores(enrichCtx, r, instanceTypes, singleAZ)
			fetchTime := time.Now() // Capture fetch time for all advices in this region

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				fetchErrs = append(fetchErrs, fmt.Sprintf("region %s: %v", r, err))
				return
			}

			// No scores is not success. The caller asked for them explicitly with
			// --with-score; returning a normal table and exit 0 would look like
			// "these instances have no score" rather than "AWS scored nothing".
			if len(scores) == 0 {
				fetchErrs = append(fetchErrs,
					fmt.Sprintf("region %s: AWS returned no placement scores "+
						"(scores are only meaningful for a flexible request — try more instance types)", r))

				return
			}

			// AWS scores the request as a whole, so each returned placement score
			// applies to every instance type that was asked about in this region.
			for _, adv := range advs {
				for _, ps := range scores {
					if singleAZ {
						if adv.ZoneScores == nil {
							adv.ZoneScores = make(map[string]int)
						}
						// ps.placement is AWS's own AvailabilityZoneId.
						adv.ZoneScores[ps.placement] = ps.score
					} else {
						adv.RegionScore = &ps.score
					}

					adv.ScoreFetchedAt = &fetchTime
				}
			}

		}(region, regionAdvices)
	}

	wg.Wait()

	return scoreOutcome(advices, fetchErrs)
}

// scoreOutcome decides what per-region failures mean for the call as a whole.
//
// Only a total failure is fatal. With --region all a single region that scores
// nothing must not discard the other thirty-three: the caller asked for scores
// and got most of them, and returning an error here throws away every result,
// successful ones included.
func scoreOutcome(advices []Advice, fetchErrs []string) error {
	if len(fetchErrs) == 0 {
		return nil
	}

	scored := 0

	for i := range advices {
		if advices[i].RegionScore != nil || len(advices[i].ZoneScores) > 0 {
			scored++
		}
	}

	detail := strings.Join(dedupe(fetchErrs), "; ")

	if scored == 0 {
		// Caller (GetSpotSavings) already prefixes "score enrichment failed".
		return fmt.Errorf("%s", detail)
	}

	slog.Warn("some regions returned no placement scores",
		slog.Int("scored_advices", scored),
		slog.String("detail", detail))

	return nil
}
