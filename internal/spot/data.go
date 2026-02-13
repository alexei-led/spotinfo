package spot

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

//go:embed data/spot-advisor-data.json
var embeddedSpotData string

//go:embed data/spot-price-data.json
var embeddedPriceData string

const (
	spotAdvisorJSONURL = "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json"
	spotPriceJSURL     = "https://spot-price.s3.amazonaws.com/spot.js"
	responsePrefix     = "callback("
	responseSuffix     = ");"
	httpTimeout        = 5 * time.Second
)

// awsSpotPricingRegions maps non-standard region codes to AWS region codes.
var awsSpotPricingRegions = map[string]string{
	"us-east":    "us-east-1",
	"us-west":    "us-west-1",
	"eu-ireland": "eu-west-1",
	"apac-sin":   "ap-southeast-1",
	"apac-syd":   "ap-southeast-2",
	"apac-tokyo": "ap-northeast-1",
}

// minRange maps interruption range max values to min values
var minRange = map[int]int{5: 0, 11: 6, 16: 12, 22: 17, 100: 23} //nolint:mnd

// fetchAdvisorData retrieves spot advisor data from AWS or falls back to embedded data.
func fetchAdvisorData(ctx context.Context) (*advisorData, error) {
	client := &http.Client{Timeout: httpTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spotAdvisorJSONURL, http.NoBody)
	if err != nil {
		// If request creation fails, try embedded data
		return loadEmbeddedAdvisorData()
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("failed to fetch advisor data from AWS, using embedded data",
			slog.String("url", spotAdvisorJSONURL),
			slog.Any("error", err))
		return loadEmbeddedAdvisorData()
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("non-200 response from AWS advisor API, using embedded data",
			slog.Int("status_code", resp.StatusCode))
		return loadEmbeddedAdvisorData()
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("failed to read advisor response body, using embedded data",
			slog.Any("error", err))
		return loadEmbeddedAdvisorData()
	}

	var result advisorData
	err = json.Unmarshal(body, &result)
	if err != nil {
		slog.Warn("failed to parse advisor data from AWS, using embedded data",
			slog.Any("error", err))
		return loadEmbeddedAdvisorData()
	}

	slog.Debug("successfully fetched advisor data from AWS")
	return &result, nil
}

// loadEmbeddedAdvisorData loads embedded advisor data as fallback.
func loadEmbeddedAdvisorData() (*advisorData, error) {
	var result advisorData
	err := json.Unmarshal([]byte(embeddedSpotData), &result)
	if err != nil {
		return nil, fmt.Errorf("failed to parse embedded spot data: %w", err)
	}

	result.Embedded = true
	slog.Debug("using embedded advisor data")
	return &result, nil
}

// fetchPricingData retrieves spot pricing data from AWS or falls back to embedded data.
func fetchPricingData(ctx context.Context, useEmbedded bool) (*rawPriceData, error) {
	if useEmbedded {
		return loadEmbeddedPricingData()
	}

	client := &http.Client{Timeout: httpTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spotPriceJSURL, http.NoBody)
	if err != nil {
		// If request creation fails, try embedded data
		return loadEmbeddedPricingData()
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("failed to fetch pricing data from AWS, using embedded data",
			slog.String("url", spotPriceJSURL),
			slog.Any("error", err))
		return loadEmbeddedPricingData()
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("non-200 response from AWS pricing API, using embedded data",
			slog.Int("status_code", resp.StatusCode))
		return loadEmbeddedPricingData()
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("failed to read pricing response body, using embedded data",
			slog.Any("error", err))
		return loadEmbeddedPricingData()
	}

	// Process JSONP response
	bodyString := strings.TrimPrefix(string(bodyBytes), responsePrefix)
	bodyString = strings.TrimSuffix(bodyString, responseSuffix)

	var result rawPriceData
	err = json.Unmarshal([]byte(bodyString), &result)
	if err != nil {
		slog.Warn("failed to parse pricing data from AWS, using embedded data",
			slog.Any("error", err))
		return loadEmbeddedPricingData()
	}

	slog.Debug("successfully fetched pricing data from AWS")
	normalizeRegions(&result)
	return &result, nil
}

// loadEmbeddedPricingData loads embedded pricing data as fallback.
func loadEmbeddedPricingData() (*rawPriceData, error) {
	var result rawPriceData
	err := json.Unmarshal([]byte(embeddedPriceData), &result)
	if err != nil {
		return nil, fmt.Errorf("failed to parse embedded spot price data: %w", err)
	}

	result.Embedded = true
	slog.Debug("using embedded pricing data")
	normalizeRegions(&result)
	return &result, nil
}

// normalizeRegions normalizes region codes in the pricing data.
func normalizeRegions(result *rawPriceData) {
	for index, r := range result.Config.Regions {
		if awsRegion, ok := awsSpotPricingRegions[r.Region]; ok {
			result.Config.Regions[index].Region = awsRegion
		}
	}
}

// convertRawPriceData converts raw pricing data to a more usable format.
func convertRawPriceData(raw *rawPriceData) *spotPriceData {
	pricing := &spotPriceData{
		Region: make(map[string]regionPrice),
	}

	for _, region := range raw.Config.Regions {
		rp := regionPrice{
			Instance: make(map[string]instancePrice),
		}

		for _, it := range region.InstanceTypes {
			for _, size := range it.Sizes {
				var ip instancePrice

				for _, os := range size.ValueColumns {
					price, err := strconv.ParseFloat(os.Prices.USD, 64)
					if err != nil {
						price = 0
					}

					if os.Name == "mswin" {
						ip.Windows = price
					} else {
						ip.Linux = price
					}
				}

				rp.Instance[size.Size] = ip
			}
		}

		pricing.Region[region.Region] = rp
	}

	return pricing
}

// ec2SpotPriceFetcher abstracts the EC2 DescribeSpotPriceHistory API for testing.
type ec2SpotPriceFetcher interface {
	DescribeSpotPriceHistory(ctx context.Context, params *ec2.DescribeSpotPriceHistoryInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSpotPriceHistoryOutput, error)
}

// newEC2SpotPriceClient creates an EC2 client for spot price lookups in the given region.
//
//nolint:contextcheck // Initialization function appropriately uses caller-provided context for AWS config
func newEC2SpotPriceClient(ctx context.Context, region string) (ec2SpotPriceFetcher, error) { //nolint:unused // Will be used in Task 2 integration
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithRetryMode(aws.RetryModeStandard),
		awsconfig.WithRetryMaxAttempts(3), //nolint:mnd
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config for region %s: %w", region, err)
	}

	return ec2.NewFromConfig(cfg), nil
}

// fetchEC2SpotPrices calls EC2 DescribeSpotPriceHistory to get current Linux spot prices.
func fetchEC2SpotPrices(ctx context.Context, client ec2SpotPriceFetcher, instanceTypes []string) (map[string]float64, error) {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()

	ec2InstanceTypes := make([]ec2types.InstanceType, len(instanceTypes))
	for i, it := range instanceTypes {
		ec2InstanceTypes[i] = ec2types.InstanceType(it)
	}

	input := &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       ec2InstanceTypes,
		ProductDescriptions: []string{"Linux/UNIX"},
		StartTime:           aws.Time(time.Now()),
	}

	prices := make(map[string]float64)

	paginator := ec2.NewDescribeSpotPriceHistoryPaginator(client, input)
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("EC2 DescribeSpotPriceHistory failed: %w", err)
		}

		for _, sp := range output.SpotPriceHistory {
			it := string(sp.InstanceType)
			price, err := strconv.ParseFloat(aws.ToString(sp.SpotPrice), 64)
			if err != nil || price <= 0 {
				continue
			}
			// Keep the first (most recent) price per instance type
			if _, exists := prices[it]; !exists {
				prices[it] = price
			}
		}
	}

	slog.Debug("fetched EC2 spot prices",
		slog.Int("requested", len(instanceTypes)),
		slog.Int("found", len(prices)))

	return prices, nil
}

// getSpotInstancePrice retrieves the spot price for a specific instance.
func (s *spotPriceData) getSpotInstancePrice(instance, region, os string) (float64, error) {
	rp, ok := s.Region[region]
	if !ok {
		return 0, fmt.Errorf("no pricing data for region: %v", region)
	}

	price, ok := rp.Instance[instance]
	if !ok {
		return 0, fmt.Errorf("no pricing data for instance: %v", instance)
	}

	if os == "windows" {
		return price.Windows, nil
	}

	return price.Linux, nil
}
