package spot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const (
	livePriceTimeout  = 10 * time.Second
	maxPriceBatchSize = 50
	livePriceMaxPages = 5
)

// livePriceProvider fetches current spot prices from the EC2 DescribeSpotPriceHistory API
// for instance types that have advisor data but missing/zero prices in the static feed.
type livePriceProvider interface {
	fetchLivePrices(ctx context.Context, region string, instanceTypes []string, os string) (map[string]float64, error)
}

// awsLivePriceProvider uses the real AWS EC2 API to fetch live spot prices.
type awsLivePriceProvider struct {
	cfg aws.Config
}

// newAWSLivePriceProvider creates a provider using the default AWS config.
func newAWSLivePriceProvider(ctx context.Context) (*awsLivePriceProvider, error) {
	ctx, cancel := context.WithTimeout(ctx, livePriceTimeout)
	defer cancel()

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRetryMode(aws.RetryModeAdaptive),
		awsconfig.WithRetryMaxAttempts(maxRetryAttempts),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config for live pricing: %w", err)
	}

	return &awsLivePriceProvider{cfg: cfg}, nil
}

// fetchLivePrices calls DescribeSpotPriceHistory for the given instance types in a region.
// It returns the most recent price per instance type.
func (p *awsLivePriceProvider) fetchLivePrices(ctx context.Context, region string, instanceTypes []string, os string) (map[string]float64, error) {
	client := ec2.NewFromConfig(p.cfg, func(o *ec2.Options) {
		o.Region = region
	})

	productDesc := osToProductDescription(os)

	// Convert instance type strings to EC2 types
	ec2Types := make([]ec2types.InstanceType, len(instanceTypes))
	for i, it := range instanceTypes {
		ec2Types[i] = ec2types.InstanceType(it)
	}

	input := &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       ec2Types,
		ProductDescriptions: []string{productDesc},
		StartTime:           aws.Time(time.Now().Add(-1 * time.Hour)),
	}

	prices := make(map[string]float64)
	paginator := ec2.NewDescribeSpotPriceHistoryPaginator(client, input)

	pages := 0
	for paginator.HasMorePages() && pages < livePriceMaxPages {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("DescribeSpotPriceHistory failed for region %s: %w", region, err)
		}
		pages++

		for _, item := range output.SpotPriceHistory {
			it := string(item.InstanceType)
			if _, exists := prices[it]; exists {
				continue // keep the first (most recent) price
			}
			if item.SpotPrice != nil {
				var price float64
				_, err := fmt.Sscanf(*item.SpotPrice, "%f", &price)
				if err == nil && price > 0 {
					prices[it] = price
				}
			}
		}

		// Stop early if we have prices for all requested types
		if len(prices) >= len(instanceTypes) {
			break
		}
	}

	return prices, nil
}

// createLivePriceProvider creates an AWS live price provider or returns nil on failure.
//
//nolint:contextcheck // Initialization function appropriately uses context.Background() for AWS config
func createLivePriceProvider() livePriceProvider {
	provider, err := newAWSLivePriceProvider(context.Background())
	if err != nil {
		slog.Debug("live price provider unavailable, zero-price instances will not be enriched",
			slog.Any("error", err))
		return nil
	}
	return provider
}

// osToProductDescription maps OS names to EC2 product description strings.
func osToProductDescription(os string) string {
	if strings.EqualFold(os, "windows") {
		return "Windows"
	}
	return "Linux/UNIX"
}

// enrichMissingPrices fills in zero-priced Advice entries using the live price API.
// It groups missing-price instances by region, fetches live prices in parallel, and
// updates the Advice slice in place. Errors are logged but do not fail the operation.
func enrichMissingPrices(ctx context.Context, advices []Advice, provider livePriceProvider, os string, timeout time.Duration) {
	if provider == nil {
		return
	}

	// Group advice indices by region where price is zero
	regionMissing := make(map[string][]int)
	for i := range advices {
		if advices[i].Price == 0 {
			regionMissing[advices[i].Region] = append(regionMissing[advices[i].Region], i)
		}
	}

	if len(regionMissing) == 0 {
		return
	}

	totalMissing := 0
	for _, indices := range regionMissing {
		totalMissing += len(indices)
	}
	slog.Info("fetching live prices for instances missing from static feed",
		slog.Int("count", totalMissing),
		slog.Int("regions", len(regionMissing)))

	enrichCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var wg sync.WaitGroup
	var mu sync.Mutex

	for region, indices := range regionMissing {
		wg.Add(1)
		go func(r string, idxs []int) {
			defer wg.Done()

			// Collect unique instance types for this region
			seen := make(map[string]bool)
			var types []string
			for _, idx := range idxs {
				it := advices[idx].Instance
				if !seen[it] {
					seen[it] = true
					types = append(types, it)
				}
			}

			// Batch if needed
			for start := 0; start < len(types); start += maxPriceBatchSize {
				end := start + maxPriceBatchSize
				if end > len(types) {
					end = len(types)
				}
				batch := types[start:end]

				prices, err := provider.fetchLivePrices(enrichCtx, r, batch, os)
				if err != nil {
					slog.Warn("failed to fetch live prices",
						slog.String("region", r),
						slog.Any("error", err))
					return
				}

				mu.Lock()
				for _, idx := range idxs {
					it := advices[idx].Instance
					if p, ok := prices[it]; ok && advices[idx].Price == 0 {
						advices[idx].Price = p
						advices[idx].LivePrice = true
					}
				}
				mu.Unlock()
			}
		}(region, indices)
	}

	wg.Wait()
}
