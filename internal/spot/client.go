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
)

// Client provides access to AWS EC2 Spot instance pricing and advice.
type Client struct {
	advisorProvider advisorProvider
	pricingProvider pricingProvider
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

// New creates a new spot client with default options.
func New() *Client {
	return NewWithOptions(DefaultTimeoutSeconds*time.Second, false)
}

// NewWithOptions creates a new spot client with custom options.
func NewWithOptions(timeout time.Duration, useEmbedded bool) *Client {
	return &Client{
		advisorProvider: newDefaultAdvisorProvider(timeout),
		pricingProvider: newDefaultPricingProvider(timeout, useEmbedded),
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

// GetSpotSavings retrieves spot instance advice based on the given criteria.
//
//nolint:gocyclo,cyclop // Complex business logic that benefits from being in a single function
func (c *Client) GetSpotSavings(ctx context.Context, regions []string, pattern, instanceOS string, cpu, memory int, maxPrice float64, sortBy SortBy, sortDesc bool) ([]Advice, error) {
	// Handle "all" regions special case
	if len(regions) == 1 && regions[0] == "all" {
		regions = c.advisorProvider.getRegions()
	}

	result := make([]Advice, 0)

	for _, region := range regions {
		// Get advice for this region and OS
		advices, err := c.advisorProvider.getRegionAdvice(region, instanceOS)
		if err != nil {
			return nil, err
		}

		// Process each instance type
		for instance, adv := range advices {
			// Match instance type pattern
			if pattern != "" {
				matched, err := regexp.MatchString(pattern, instance)
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
			if (cpu != 0 && info.Cores < cpu) || (memory != 0 && info.RAM < float32(memory)) {
				continue
			}

			// Get spot price
			spotPrice, err := c.pricingProvider.getSpotPrice(instance, region, instanceOS)
			if err == nil {
				// Filter by max price
				if maxPrice != 0 && spotPrice > maxPrice {
					continue
				}
			}

			// Get range information
			rng, err := c.advisorProvider.getRange(adv.Range)
			if err != nil {
				continue // Skip if we can't get range info
			}

			result = append(result, Advice{
				Region:   region,
				Instance: instance,
				Range:    rng,
				Savings:  adv.Savings,
				Info:     info,
				Price:    spotPrice,
			})
		}
	}

	// Sort results
	sortAdvices(result, sortBy, sortDesc)

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

// GetSpotSavings provides backward compatibility with the old public function.
// Deprecated: Use Client.GetSpotSavings instead.
func GetSpotSavings(regions []string, pattern, instanceOS string, cpu, memory int, maxPrice float64, sortBy int, sortDesc bool) ([]Advice, error) {
	client := New()
	return client.GetSpotSavings(context.Background(), regions, pattern, instanceOS, cpu, memory, maxPrice, SortBy(sortBy), sortDesc)
}
