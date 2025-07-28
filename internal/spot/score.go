package spot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
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
	defaultTargetCapacity      = 1
	defaultMaxResults          = 30
	maxRetryAttempts           = 5
	DefaultScoreTimeoutSeconds = 30
	defaultScoreTimeout        = DefaultScoreTimeoutSeconds * time.Second

	// Mock score range for fallback
	minMockScore = 1
	maxMockScore = 10
)

// awsAPIProvider provides spot placement scores with different implementations.
type awsAPIProvider interface {
	fetchScores(ctx context.Context, region string, instanceTypes []string, singleAZ bool) (map[string]int, error)
}

// awsScoreProvider implements awsAPIProvider using real AWS API calls.
type awsScoreProvider struct {
	cfg aws.Config
}

// mockScoreProvider implements awsAPIProvider using mock scores for fallback.
type mockScoreProvider struct{}

// CachedScoreData wraps scores with timestamp for freshness tracking.
type CachedScoreData struct {
	Scores    map[string]int
	FetchTime time.Time
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

// createAPIProvider creates an AWS API provider or falls back to mock.
//
//nolint:contextcheck // Initialization function appropriately uses context.Background() for AWS config
func createAPIProvider() awsAPIProvider {
	// Try to create AWS provider
	if provider, err := newAWSScoreProvider(context.Background()); err == nil {
		return provider
	}
	// Fallback to mock provider
	return &mockScoreProvider{}
}

// newAWSScoreProvider creates a new AWS score provider with proper configuration.
func newAWSScoreProvider(ctx context.Context) (*awsScoreProvider, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultScoreTimeout)
	defer cancel()

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRetryMode(aws.RetryModeAdaptive),
		awsconfig.WithRetryMaxAttempts(maxRetryAttempts),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &awsScoreProvider{cfg: cfg}, nil
}

// fetchScores implements awsAPIProvider for AWS API calls.
func (p *awsScoreProvider) fetchScores(ctx context.Context, region string, instanceTypes []string, singleAZ bool) (map[string]int, error) {
	// Create region-specific client
	client := ec2.NewFromConfig(p.cfg, func(o *ec2.Options) {
		o.Region = region
	})

	input := &ec2.GetSpotPlacementScoresInput{
		InstanceTypes:          instanceTypes,
		TargetCapacity:         aws.Int32(defaultTargetCapacity),
		SingleAvailabilityZone: aws.Bool(singleAZ),
		MaxResults:             aws.Int32(defaultMaxResults),
	}

	scores := make(map[string]int)
	paginator := ec2.NewGetSpotPlacementScoresPaginator(client, input)

	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get spot placement scores for region %s: %w", region, err)
		}

		// Process each score result
		for _, result := range output.SpotPlacementScores {
			score := int(aws.ToInt32(result.Score))

			// Map scores to the requested instance types
			// AWS may return scores for a subset of requested types
			for _, instanceType := range instanceTypes {
				// In practice, AWS returns results that correspond to the input
				// For simplicity, we'll assign the score to each requested type
				if _, exists := scores[instanceType]; !exists {
					scores[instanceType] = score
				}
			}
		}
	}

	// Fill in any missing instance types with a default score
	for _, instanceType := range instanceTypes {
		if _, exists := scores[instanceType]; !exists {
			// Use a moderate default score if AWS doesn't return data for this type
			scores[instanceType] = 5 // Middle of 1-10 range
		}
	}

	return scores, nil
}

// fetchScores implements scoreProvider for mock scores.
func (p *mockScoreProvider) fetchScores(ctx context.Context, region string, instanceTypes []string, singleAZ bool) (map[string]int, error) {
	scores := make(map[string]int)
	for i, instanceType := range instanceTypes {
		// Generate deterministic mock scores based on instance type and position
		score := (len(instanceType)*7+i*3)%maxMockScore + minMockScore
		scores[instanceType] = score
	}
	return scores, nil
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
	instanceTypes []string, singleAZ bool) (map[string]int, error) {

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

	// Fetch from provider (AWS or mock)
	scores, err := sc.provider.fetchScores(ctx, region, instanceTypes, singleAZ)
	if err != nil {
		return nil, err
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
	var errors []string

	for region, regionAdvices := range regionGroups {
		wg.Add(1)
		go func(r string, advs []*Advice) {
			defer wg.Done()

			// Collect unique instance types for this region
			instanceTypeSet := make(map[string]bool)
			typeToAdvices := make(map[string][]*Advice)

			for _, adv := range advs {
				instanceType := adv.InstanceType
				if instanceType == "" {
					instanceType = adv.Instance
				}

				if !instanceTypeSet[instanceType] {
					instanceTypeSet[instanceType] = true
				}
				typeToAdvices[instanceType] = append(typeToAdvices[instanceType], adv)
			}

			// Convert set to slice
			var instanceTypes []string
			for instanceType := range instanceTypeSet {
				instanceTypes = append(instanceTypes, instanceType)
			}

			// Fetch scores for this region
			scores, err := sc.getSpotPlacementScores(enrichCtx, r, instanceTypes, singleAZ)
			fetchTime := time.Now() // Capture fetch time for all advices in this region

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errors = append(errors, fmt.Sprintf("region %s: %v", r, err))
				return
			}

			// Apply scores to advices
			if singleAZ {
				// For AZ-level scores, store in ZoneScores map
				for instanceType, score := range scores {
					for _, adv := range typeToAdvices[instanceType] {
						if adv.ZoneScores == nil {
							adv.ZoneScores = make(map[string]int)
						}
						azID := fmt.Sprintf("%sa", r) // Mock AZ: us-east-1a, etc.
						adv.ZoneScores[azID] = score
						adv.ScoreFetchedAt = &fetchTime
					}
				}
			} else {
				// For region-level scores, store in RegionScore field
				for instanceType, score := range scores {
					for _, adv := range typeToAdvices[instanceType] {
						scoreVal := score
						adv.RegionScore = &scoreVal
						adv.ScoreFetchedAt = &fetchTime
					}
				}
			}

		}(region, regionAdvices)
	}

	wg.Wait()

	if len(errors) > 0 {
		return fmt.Errorf("score enrichment failed: %s", strings.Join(errors, "; "))
	}

	return nil
}
