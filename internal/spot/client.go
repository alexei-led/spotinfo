package spot

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTimeoutSeconds is the default timeout value in seconds.
	DefaultTimeoutSeconds = 5
	// allRegionsKeyword represents the special "all" regions value.
	allRegionsKeyword = "all"
	// osLinux and osWindows are the instance OS values accepted by this package.
	// Every comparison against them is case-insensitive; callers may pass any
	// casing and nothing normalises the value on the way in.
	osLinux   = "linux"
	osWindows = "windows"
	// priceColumnWindows is the price feed's own name for the Windows column,
	// distinct from the osWindows value users pass on the command line.
	priceColumnWindows = "mswin"

	// DataSourceAWS and DataSourceEmbedded report where the advice data came
	// from, so callers can tell a live answer from the embedded snapshot.
	DataSourceAWS      = "aws"
	DataSourceEmbedded = "embedded"
	// DataSourceCached is AWS data served from the local feed cache without
	// asking the origin this run. It is deliberately distinct from
	// DataSourceAWS: the data is AWS's, but reporting it as freshly fetched
	// would be a claim about recency that nothing checked.
	DataSourceCached = "cached"
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
	advisorProvider   advisorProvider
	pricingProvider   pricingProvider
	scoreProvider     scoreProvider
	livePriceProvider livePriceProvider
	timeout           time.Duration
	useEmbedded       bool
}

// advisorProvider provides access to spot advisor data (private interface close to consumer).
type advisorProvider interface {
	getRegions() []string
	usedEmbeddedData() bool
	getRegionAdvice(region, os string) (map[string]spotAdvice, error)
	getInstanceType(instance string) (TypeInfo, error)
	getRange(index int) (Range, error)
}

// pricingProvider provides access to spot pricing data (private interface close to consumer).
type pricingProvider interface {
	getSpotPrice(instance, region, os string) (float64, error)
	usedEmbeddedData() bool
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
//
//nolint:contextcheck // Initialization function appropriately uses context.Background() for AWS config
func NewWithOptions(timeout time.Duration, useEmbedded bool) *Client {
	return NewWithFetchOptions(timeout, FetchPolicy{UseEmbedded: useEmbedded})
}

// FetchPolicy is how a caller wants feeds obtained.
type FetchPolicy struct {
	// UseEmbedded answers from the committed snapshot and makes no request.
	UseEmbedded bool
	// Refresh ignores any cached copy for this run.
	Refresh bool
}

// NewWithFetchOptions creates a client with an explicit freshness policy.
func NewWithFetchOptions(timeout time.Duration, policy FetchPolicy) *Client {
	options := fetchOptions{useEmbedded: policy.UseEmbedded, refresh: policy.Refresh}

	return &Client{
		advisorProvider:   newDefaultAdvisorProvider(timeout, options),
		pricingProvider:   newDefaultPricingProvider(timeout, options),
		scoreProvider:     newScoreCache(),
		livePriceProvider: createLivePriceProvider(),
		timeout:           timeout,
		useEmbedded:       policy.UseEmbedded,
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

// DataSource reports whether the returned advice was built from live AWS feeds
// or the embedded snapshot. Embedded wins if either feed fell back: the result
// is only as fresh as its stalest input. Reported conservatively — anything not
// positively known to be live counts as embedded.
func (c *Client) DataSource() string {
	if c.advisorProvider.usedEmbeddedData() || c.pricingProvider.usedEmbeddedData() {
		return DataSourceEmbedded
	}

	// The weakest input decides. An answer built from one freshly fetched feed
	// and one cached feed is not current, and saying so is the whole point of
	// having a third value.
	if originOf(c.advisorProvider) == originCached || originOf(c.pricingProvider) == originCached {
		return DataSourceCached
	}

	return DataSourceAWS
}

// originOf reads the origin from a provider that tracks one. A test double that
// does not is treated as live, matching how usedEmbeddedData already behaves for
// injected providers.
func originOf(provider any) feedOrigin {
	if tracked, ok := provider.(interface{ dataOrigin() feedOrigin }); ok {
		return tracked.dataOrigin()
	}

	return originLive
}

// SetLivePriceProvider sets the live price provider (for testing).
func (c *Client) SetLivePriceProvider(p livePriceProvider) {
	c.livePriceProvider = p
}

// GetSpotSavings retrieves spot instance advice using functional options.
//
//nolint:gocyclo,cyclop // Complex business logic that benefits from being in a single function
func (c *Client) GetSpotSavings(ctx context.Context, opts ...GetSpotSavingsOption) ([]Advice, error) {
	// Default configuration
	cfg := &getSpotSavingsConfig{
		instanceOS:   osLinux,
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

		// Process each instance type, in name order.
		//
		// Ranging over the map directly made the whole answer irreproducible:
		// the sort in sortAdvices is not a total order — every AWS row in the
		// same interruption bucket ties — so ties kept whatever order the map
		// enumerated, and two identical `--offline` invocations printed
		// different pages. Sorting the keys costs one allocation per region and
		// fixes the input order, which the stable sort then preserves.
		for _, instance := range slices.Sorted(maps.Keys(advices)) {
			adv := advices[instance]
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

	// Enrich instances with missing prices from live AWS API
	enrichMissingPrices(ctx, result, c.livePriceProvider, cfg.instanceOS, livePriceTimeout)

	// Re-apply maxPrice filter after live price enrichment.
	//
	// A zero price means "unknown" — the instance is absent from the static feed
	// and live enrichment could not fill it (no credentials, or AWS omits the
	// region entirely, as it does for me-*). Spot prices are never actually zero,
	// so an unknown price cannot be asserted to satisfy a price ceiling; drop it
	// rather than present it as the cheapest option.
	if cfg.maxPrice != 0 {
		filtered := result[:0]

		for _, adv := range result {
			if adv.Price > 0 && adv.Price <= cfg.maxPrice {
				filtered = append(filtered, adv)
			}
		}

		result = filtered
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
	// options carries the caller's freshness choice: the committed snapshot, or
	// a refresh that ignores any cached copy.
	options fetchOptions
	// origin records where the loaded document actually came from, so the client
	// can report a cached answer as cached instead of implying it is current.
	origin feedOrigin
}

func newDefaultAdvisorProvider(timeout time.Duration, options fetchOptions) *defaultAdvisorProvider {
	return &defaultAdvisorProvider{timeout: timeout, options: options}
}

func (p *defaultAdvisorProvider) loadData() error {
	p.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout(p.timeout))
		defer cancel()

		p.data, p.origin, p.err = fetchAdvisorData(ctx, p.options)
	})

	return p.err
}

// usedEmbeddedData reports whether the loaded advisor data came from the
// embedded copy. Unloaded counts as embedded: never claim live data we do not
// positively know we fetched.
func (p *defaultAdvisorProvider) usedEmbeddedData() bool {
	return p.data == nil || p.data.Embedded
}

// dataOrigin reports where the loaded document came from. Unloaded counts as
// embedded: never claim data we do not positively know we obtained.
func (p *defaultAdvisorProvider) dataOrigin() feedOrigin {
	if p.data == nil {
		return originEmbedded
	}

	return p.origin
}

// getRegions lists every region the advisor document describes, in name order.
//
// The order is part of the answer: `--region all` walks this slice and appends
// each region's rows in turn, so an unsorted list made the page a caller sees
// depend on Go's map iteration.
func (p *defaultAdvisorProvider) getRegions() []string {
	if err := p.loadData(); err != nil {
		return nil
	}

	return slices.Sorted(maps.Keys(p.data.Regions))
}

func (p *defaultAdvisorProvider) getRegionAdvice(region, os string) (map[string]spotAdvice, error) {
	// Validate OS first before loading data
	if !strings.EqualFold(osWindows, os) && !strings.EqualFold(osLinux, os) {
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
	if strings.EqualFold(osWindows, os) {
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
	data    *spotPriceData
	err     error
	timeout time.Duration
	origin  feedOrigin
	once    sync.Once
	// options carries the caller's freshness choice; rawEmbedded records what the
	// load actually used, which also covers falling back after a failed fetch.
	options     fetchOptions
	rawEmbedded bool
}

func newDefaultPricingProvider(timeout time.Duration, options fetchOptions) *defaultPricingProvider {
	return &defaultPricingProvider{
		timeout: timeout,
		options: options,
	}
}

func (p *defaultPricingProvider) loadData() error {
	p.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout(p.timeout))
		defer cancel()

		rawData, origin, err := fetchPricingData(ctx, p.options)
		if err != nil {
			p.err = err
			return
		}

		p.origin = origin
		p.rawEmbedded = rawData.Embedded
		p.data = convertRawPriceData(rawData)
	})
	return p.err
}

// usedEmbeddedData reports whether the loaded pricing data came from the
// embedded copy. Unloaded counts as embedded, as above.
func (p *defaultPricingProvider) usedEmbeddedData() bool {
	return p.rawEmbedded
}

// dataOrigin reports where the loaded document came from.
func (p *defaultPricingProvider) dataOrigin() feedOrigin {
	if p.data == nil {
		return originEmbedded
	}

	return p.origin
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
