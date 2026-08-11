package spot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const (
	livePriceTimeout  = 10 * time.Second
	maxPriceBatchSize = 50
	livePriceMaxPages = 5

	// EC2 DescribeSpotPriceHistory product descriptions. These are AWS API wire
	// values, unlike the osLinux/osWindows names they are derived from.
	productDescWindows = "Windows"
	productDescLinux   = "Linux/UNIX"
)

// livePriceProvider fetches current spot prices from the EC2 DescribeSpotPriceHistory API
// for instance types that have advisor data but missing/zero prices in the static feed.
type livePriceProvider interface {
	fetchLivePrices(ctx context.Context, region string, instanceTypes []string, os string) (map[string]float64, error)
}

// awsLivePriceProvider uses the real AWS EC2 API to fetch live spot prices.
type awsLivePriceProvider struct {
	// newClient builds the region-scoped EC2 client. Injectable so the response
	// handling below can be tested without reaching AWS; production leaves it nil
	// and gets ec2.NewFromConfig over lazily resolved credentials.
	newClient func(region string) ec2.DescribeSpotPriceHistoryAPIClient
}

// historyClient returns the injected client factory, or a real EC2 one built
// from credentials resolved on first use.
//
// Lazy on purpose. Resolving them in the constructor charged the credential
// probe to every invocation, including `--offline`, which then discards this
// provider entirely — 2 seconds added to a command that makes no request at
// all. Nothing here is needed until an instance is actually missing a price.
func (p *awsLivePriceProvider) historyClient(region string) (ec2.DescribeSpotPriceHistoryAPIClient, error) {
	if p.newClient != nil {
		return p.newClient(region), nil
	}

	cfg, err := awsConfigWithCredentials()
	if err != nil {
		return nil, fmt.Errorf("live pricing unavailable: %w", err)
	}

	return ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		o.Region = region
	}), nil
}

// newAWSLivePriceProvider creates a provider. Credentials are resolved on first
// use, in historyClient — see the note there on why not here.
func newAWSLivePriceProvider(_ context.Context) (*awsLivePriceProvider, error) {
	return &awsLivePriceProvider{}, nil
}

// fetchLivePrices calls DescribeSpotPriceHistory for the given instance types in a region.
// It returns the most recent price per instance type.
func (p *awsLivePriceProvider) fetchLivePrices(ctx context.Context, region string, instanceTypes []string, os string) (map[string]float64, error) {
	client, err := p.historyClient(region)
	if err != nil {
		return nil, err
	}

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
				// Same non-finite guard as the static feed: +Inf would satisfy
				// a bare `> 0` check and then break JSON output.
				if price, ok := parsePrice(*item.SpotPrice); ok && price > 0 {
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
	if strings.EqualFold(os, osWindows) {
		return productDescWindows
	}
	return productDescLinux
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

	failures := make(map[string]error, len(regionMissing))

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
				end := min(start+maxPriceBatchSize, len(types))
				batch := types[start:end]

				prices, err := provider.fetchLivePrices(enrichCtx, r, batch, os)
				if err != nil {
					mu.Lock()
					failures[r] = err
					mu.Unlock()

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
	reportLivePriceFailures(failures)
}

// reportLivePriceFailures logs what the live-price pass could not read.
//
// Missing credentials are reported once, not once per region. Every region
// resolves the same lazily cached credential chain, so a machine without AWS
// credentials logged the identical error for every region it queried: a default
// `spotinfo list` printed twelve copies of a 450-character IMDS failure to
// stderr on a command that answered correctly from the static feed, which reads
// as a broken tool. One line, naming what to do about it, is the whole
// information those twelve carried.
//
// Anything else stays per region: a throttled or unreachable region is a fact
// about that region, and collapsing those would hide which one failed.
func reportLivePriceFailures(failures map[string]error) {
	uncredentialed := make([]string, 0, len(failures))

	for region, err := range failures {
		if errors.Is(err, errNoAWSCredentials) {
			uncredentialed = append(uncredentialed, region)

			continue
		}

		slog.Warn("failed to fetch live prices", slog.String("region", region), slog.Any("error", err))
	}

	if len(uncredentialed) == 0 {
		return
	}

	slices.Sort(uncredentialed)
	slog.Warn("no AWS credentials, so machines missing from the static price feed keep no price; "+
		"configure AWS credentials to price them, or pass --offline to skip the lookup",
		slog.Any("regions", uncredentialed))
}
