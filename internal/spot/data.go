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
