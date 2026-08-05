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
	// Feed behind https://aws.amazon.com/ec2/spot/pricing/. The legacy JSONP feed
	// (spot-price.s3.amazonaws.com/spot.js) has been frozen since 2024-05-13.
	spotPriceJSURL = "https://website.spot.ec2.aws.a2z.com/spot.json"
	// This feed serves plain JSON, so the trims below are no-ops. They are kept
	// because it is still served as application/x-javascript and may be re-wrapped.
	responsePrefix = "callback("
	responseSuffix = ");"
	httpTimeout    = 5 * time.Second
)

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

	resp, err := client.Do(req) //nolint:gosec // G704: spotAdvisorJSONURL is a package-level constant, not user input
	if err != nil {
		slog.Warn("failed to fetch advisor data from AWS, using embedded data",
			slog.String("url", spotAdvisorJSONURL),
			slog.Any("error", err))
		return loadEmbeddedAdvisorData()
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("non-200 response from AWS advisor API, using embedded data", //nolint:gosec // G706: status_code is integer from HTTP response, not user-controlled input
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

	resp, err := client.Do(req) //nolint:gosec // G704: spotPriceJSURL is a package-level constant, not user input
	if err != nil {
		slog.Warn("failed to fetch pricing data from AWS, using embedded data",
			slog.String("url", spotPriceJSURL),
			slog.Any("error", err))
		return loadEmbeddedPricingData()
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("non-200 response from AWS pricing API, using embedded data", //nolint:gosec // G706: status_code is integer from HTTP response, not user-controlled input
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
	return &result, nil
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

	if os == osWindows {
		return price.Windows, nil
	}

	return price.Linux, nil
}
