package spot

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTimeoutSeconds is the default timeout value in seconds.
	DefaultTimeoutSeconds = 5
	// allRegionsKeyword represents the special "all" regions value.
	allRegionsKeyword = "all"
)

// getSpotSavingsConfig holds configuration options for GetSpotSavingsWithOptions.
//
//nolint:govet // fieldalignment: small config struct, 8-byte optimization not worth the code churn
type getSpotSavingsConfig struct {
	regions                []string
	pattern                string
	instanceOS             string
	scoreTimeout           time.Duration
	maxPrice               float64
	cpu                    int
	memory                 int
	minScore               int
	sortBy                 SortBy
	sortDesc               bool
	withScores             bool
	singleAvailabilityZone bool
}

// GetSpotSavingsOption is a functional option for GetSpotSavingsWithOptions.
type GetSpotSavingsOption func(*getSpotSavingsConfig)

// WithRegions sets the regions to query.
func WithRegions(regions []string) GetSpotSavingsOption {
	return func(cfg *getSpotSavingsConfig) {
		cfg.regions = regions
	}
}

// WithPattern sets the instance type pattern filter.
func WithPattern(pattern string) GetSpotSavingsOption {
	return func(cfg *getSpotSavingsConfig) {
		cfg.pattern = pattern
	}
}

// WithOS sets the operating system filter.
func WithOS(instanceOS string) GetSpotSavingsOption {
	return func(cfg *getSpotSavingsConfig) {
		cfg.instanceOS = instanceOS
	}
}

// WithCPU sets the minimum CPU requirement.
func WithCPU(cpu int) GetSpotSavingsOption {
	return func(cfg *getSpotSavingsConfig) {
		cfg.cpu = cpu
	}
}

// WithMemory sets the minimum memory requirement.
func WithMemory(memory int) GetSpotSavingsOption {
	return func(cfg *getSpotSavingsConfig) {
		cfg.memory = memory
	}
}

// WithMaxPrice sets the maximum price filter.
func WithMaxPrice(maxPrice float64) GetSpotSavingsOption {
	return func(cfg *getSpotSavingsConfig) {
		cfg.maxPrice = maxPrice
	}
}

// WithSort sets the sorting criteria.
func WithSort(sortBy SortBy, sortDesc bool) GetSpotSavingsOption {
	return func(cfg *getSpotSavingsConfig) {
		cfg.sortBy = sortBy
		cfg.sortDesc = sortDesc
	}
}

// WithScores enables spot placement score enrichment.
func WithScores(enable bool) GetSpotSavingsOption {
	return func(cfg *getSpotSavingsConfig) {
		cfg.withScores = enable
	}
}

// WithSingleAvailabilityZone enables AZ-level scoring instead of region-level.
func WithSingleAvailabilityZone(enable bool) GetSpotSavingsOption {
	return func(cfg *getSpotSavingsConfig) {
		cfg.singleAvailabilityZone = enable
	}
}

// WithMinScore sets the minimum score filter.
func WithMinScore(minScore int) GetSpotSavingsOption {
	return func(cfg *getSpotSavingsConfig) {
		cfg.minScore = minScore
	}
}

// WithScoreTimeout sets the timeout for score enrichment operations.
func WithScoreTimeout(timeout time.Duration) GetSpotSavingsOption {
	return func(cfg *getSpotSavingsConfig) {
		cfg.scoreTimeout = timeout
	}
}

// Client provides access to AWS EC2 Spot instance pricing and advice.
type Client struct {
	advisorProvider advisorProvider
	pricingProvider pricingProvider
	scoreProvider   scoreProvider
	timeout         time.Duration
	useEmbedded     bool
}

// advisorProvider provides access to spot advisor data (private interface close to consumer).
type advisorProvider interface {
	getRegions() []string
	getRegionAdvice(region, os string) (map[string]spotAdvice, error)
	getInstanceType(instance string) (TypeInfo, error)
	getRange(index int) (Range, error)
}

// pricingProvider provides access to spot pricing data (private interface close to consumer).
type pricingProvider interface {
	getSpotPrice(instance, region, os string) (float64, error)
}

// scoreProvider provides access to spot placement scores (private interface close to consumer).
type scoreProvider interface {
	enrichWithScores(ctx context.Context, advices []Advice, singleAZ bool, timeout time.Duration) error
}

// New creates a new spot client with default options.
func New() *Client {
	return NewWithOptions(DefaultTimeoutSeconds*time.Second, false)
}

// NewWithOptions creates a new spot client with custom options.
func NewWithOptions(timeout time.Duration, useEmbedded bool) *Client {
	return &Client{
		advisorProvider: newDefaultAdvisorProvider(timeout),
		pricingProvider: newDefaultPricingProvider(timeout, useEmbedded),
		scoreProvider:   newScoreCache(),
		timeout:         timeout,
		useEmbedded:     useEmbedded,
	}
}

// NewWithProviders creates a new spot client with custom data providers (for testing).
func NewWithProviders(advisor advisorProvider, pricing pricingProvider) *Client {
	return &Client{
		advisorProvider: advisor,
		pricingProvider: pricing,
		timeout:         DefaultTimeoutSeconds * time.Second,
		useEmbedded:     false,
	}
}

// GetSpotSavings retrieves spot instance advice using functional options.
//
//nolint:gocyclo,cyclop // Complex business logic that benefits from being in a single function
func (c *Client) GetSpotSavings(ctx context.Context, opts ...GetSpotSavingsOption) ([]Advice, error) {
	// Default configuration
	cfg := &getSpotSavingsConfig{
		instanceOS:   "linux",
		sortBy:       SortByRange,
		scoreTimeout: defaultScoreTimeout,
	}

	// Apply options
	for _, opt := range opts {
		opt(cfg)
	}

	// Handle "all" regions special case
	regions := cfg.regions
	if len(regions) == 1 && regions[0] == allRegionsKeyword {
		regions = c.advisorProvider.getRegions()
	}

	result := make([]Advice, 0)

	for _, region := range regions {
		// Get advice for this region and OS
		advices, err := c.advisorProvider.getRegionAdvice(region, cfg.instanceOS)
		if err != nil {
			return nil, err
		}

		// Process each instance type
		for instance, adv := range advices {
			// Match instance type pattern
			if cfg.pattern != "" {
				matched, err := regexp.MatchString(cfg.pattern, instance)
				if err != nil {
					return nil, fmt.Errorf("failed to match instance type: %w", err)
				}
				if !matched {
					continue
				}
			}

			// Filter by CPU and memory requirements
			info, err := c.advisorProvider.getInstanceType(instance)
			if err != nil {
				continue // Skip instances we don't have type info for
			}
			if (cfg.cpu != 0 && info.Cores < cfg.cpu) || (cfg.memory != 0 && info.RAM < float32(cfg.memory)) {
				continue
			}

			// Get spot price
			spotPrice, err := c.pricingProvider.getSpotPrice(instance, region, cfg.instanceOS)
			if err == nil {
				// Filter by max price
				if cfg.maxPrice != 0 && spotPrice > cfg.maxPrice {
					continue
				}
			}

			// Get range information
			rng, err := c.advisorProvider.getRange(adv.Range)
			if err != nil {
				continue // Skip if we can't get range info
			}

			result = append(result, Advice{
				Region:       region,
				Instance:     instance,
				InstanceType: instance, // Set InstanceType field
				Range:        rng,
				Savings:      adv.Savings,
				Info:         info,
				Price:        spotPrice,
			})
		}
	}

	// Sort results
	sortAdvices(result, cfg.sortBy, cfg.sortDesc)

	// Add score enrichment if requested
	if cfg.withScores {
		err := c.enrichWithScores(ctx, result, cfg.singleAvailabilityZone, cfg.scoreTimeout)
		if err != nil {
			return nil, fmt.Errorf("score enrichment failed: %w", err)
		}
	}

	// Filter by minimum score if specified
	if cfg.minScore > 0 {
		result = filterByMinScore(result, cfg.minScore)
	}

	return result, nil
}

// defaultAdvisorProvider is the default implementation of advisorProvider.
type defaultAdvisorProvider struct {
	data    *advisorData
	err     error
	timeout time.Duration
	once    sync.Once
}

func newDefaultAdvisorProvider(timeout time.Duration) *defaultAdvisorProvider {
	return &defaultAdvisorProvider{timeout: timeout}
}

func (p *defaultAdvisorProvider) loadData() error {
	p.once.Do(func() {
		p.data, p.err = fetchAdvisorData(context.Background())
	})
	return p.err
}

func (p *defaultAdvisorProvider) getRegions() []string {
	if err := p.loadData(); err != nil {
		return nil
	}
	regions := make([]string, 0, len(p.data.Regions))
	for k := range p.data.Regions {
		regions = append(regions, k)
	}
	return regions
}

func (p *defaultAdvisorProvider) getRegionAdvice(region, os string) (map[string]spotAdvice, error) {
	// Validate OS first before loading data
	if !strings.EqualFold("windows", os) && !strings.EqualFold("linux", os) {
		return nil, fmt.Errorf("invalid instance OS, must be windows/linux")
	}

	if err := p.loadData(); err != nil {
		return nil, err
	}

	regionData, ok := p.data.Regions[region]
	if !ok {
		return nil, fmt.Errorf("region not found: %s", region)
	}

	var advices map[string]spotAdvice
	if strings.EqualFold("windows", os) {
		advices = regionData.Windows
	} else {
		advices = regionData.Linux
	}

	return advices, nil
}

func (p *defaultAdvisorProvider) getInstanceType(instance string) (TypeInfo, error) {
	if err := p.loadData(); err != nil {
		return TypeInfo{}, err
	}

	info, ok := p.data.InstanceTypes[instance]
	if !ok {
		return TypeInfo{}, fmt.Errorf("instance type not found: %s", instance)
	}

	return TypeInfo(info), nil
}

func (p *defaultAdvisorProvider) getRange(index int) (Range, error) {
	if err := p.loadData(); err != nil {
		return Range{}, err
	}

	if index < 0 || index >= len(p.data.Ranges) {
		return Range{}, fmt.Errorf("range index out of bounds: %d", index)
	}

	r := p.data.Ranges[index]
	return Range{
		Label: r.Label,
		Max:   r.Max,
		Min:   minRange[r.Max],
	}, nil
}

// defaultPricingProvider is the default implementation of pricingProvider.
type defaultPricingProvider struct {
	data        *spotPriceData
	err         error
	timeout     time.Duration
	useEmbedded bool
	once        sync.Once
}

func newDefaultPricingProvider(timeout time.Duration, useEmbedded bool) *defaultPricingProvider {
	return &defaultPricingProvider{
		timeout:     timeout,
		useEmbedded: useEmbedded,
	}
}

func (p *defaultPricingProvider) loadData() error {
	p.once.Do(func() {
		rawData, err := fetchPricingData(context.Background(), p.useEmbedded)
		if err != nil {
			p.err = err
			return
		}
		p.data = convertRawPriceData(rawData)
	})
	return p.err
}

func (p *defaultPricingProvider) getSpotPrice(instance, region, os string) (float64, error) {
	if err := p.loadData(); err != nil {
		return 0, err
	}
	return p.data.getSpotInstancePrice(instance, region, os)
}

// enrichWithScores delegates score enrichment to the scoreProvider.
func (c *Client) enrichWithScores(ctx context.Context, advices []Advice, singleAZ bool, timeout time.Duration) error {
	if c.scoreProvider == nil {
		c.scoreProvider = newScoreCache()
	}
	return c.scoreProvider.enrichWithScores(ctx, advices, singleAZ, timeout)
}
