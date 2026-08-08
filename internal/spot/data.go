package spot

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
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
	// httpTimeout bounds a fetch only when the caller supplied no deadline of
	// its own. Providers pass their configured timeout via the context.
	httpTimeout = 5 * time.Second
)

// Sentinel errors for feed payloads that parse but carry no usable data.
var (
	errNoAdvisorRegions = errors.New("advisor data contained no regions")
	errNoPricingRegions = errors.New("pricing data contained no regions")
)

// withDefaultTimeout bounds a context that carries no deadline. A caller's own
// deadline wins, which is what makes the client's configured timeout effective —
// it used to be stored and never read, so every fetch waited the hardcoded 5s.
func withDefaultTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}

	return context.WithTimeout(ctx, httpTimeout)
}

// parsePrice reads a price from the feed, reporting whether it is usable.
//
// strconv.ParseFloat accepts "NaN", "Inf" and "-Inf" without error, so an
// err-only check lets a non-finite value through. It then reaches
// json.Marshal, which rejects non-finite floats — crashing `--output json` on
// a value chosen by an undocumented upstream feed. The feed already ships
// non-numeric cells ("N/A*"), so this is a live input, not a hypothetical.
func parsePrice(raw string) (float64, bool) {
	price, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
		return 0, false
	}

	return price, true
}

// fetchTimeout resolves a provider's configured timeout. A zero or negative
// value means "unset" and yields the default: context.WithTimeout(ctx, 0)
// produces an already-expired context, which would silently skip every fetch
// and serve embedded data forever instead of erroring.
func fetchTimeout(configured time.Duration) time.Duration {
	if configured <= 0 {
		return httpTimeout
	}

	return configured
}

// minRange maps interruption range max values to min values
var minRange = map[int]int{5: 0, 11: 6, 16: 12, 22: 17, 100: 23} //nolint:mnd

// fetchAdvisorData retrieves spot advisor data from AWS or falls back to embedded data.
func fetchAdvisorData(ctx context.Context) (*advisorData, error) {
	ctx, cancel := withDefaultTimeout(ctx)
	defer cancel()

	client := &http.Client{}

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

	result, err := parseAdvisorResponse(body)
	if err == nil {
		err = validateAdvisorCoverage(result)
	}

	if err != nil {
		slog.Warn("unusable advisor data from AWS, using embedded data",
			slog.Any("error", err))
		return loadEmbeddedAdvisorData()
	}

	slog.Debug("successfully fetched advisor data from AWS")
	return result, nil
}

// parseAdvisorResponse decodes an advisor feed body and rejects unusable payloads.
// A 200 carrying valid-but-unexpected JSON (an empty body, renamed keys) unmarshals
// cleanly into a zero-valued struct, so an empty region set must be an error rather
// than an advisor that silently knows about nothing.
func parseAdvisorResponse(body []byte) (*advisorData, error) {
	var result advisorData
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse advisor data: %w", err)
	}

	if len(result.Regions) == 0 {
		return nil, errNoAdvisorRegions
	}

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

	ctx, cancel := withDefaultTimeout(ctx)
	defer cancel()

	client := &http.Client{}

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

	result, err := parsePricingResponse(bodyBytes)
	if err == nil {
		err = validatePriceCoverage(result)
	}

	if err != nil {
		slog.Warn("unusable pricing data from AWS, using embedded data",
			slog.Any("error", err))
		return loadEmbeddedPricingData()
	}

	slog.Debug("successfully fetched pricing data from AWS")
	return result, nil
}

// parsePricingResponse decodes a pricing feed body and rejects unusable payloads.
// An empty region set would otherwise price every instance at $0 with no fallback,
// which matters because the feed is an undocumented endpoint that may change shape.
func parsePricingResponse(body []byte) (*rawPriceData, error) {
	// The current feed is plain JSON; these trims are no-ops unless it is re-wrapped.
	bodyString := strings.TrimPrefix(string(body), responsePrefix)
	bodyString = strings.TrimSuffix(bodyString, responseSuffix)

	var result rawPriceData
	if err := json.Unmarshal([]byte(bodyString), &result); err != nil {
		return nil, fmt.Errorf("failed to parse pricing data: %w", err)
	}

	if len(result.Config.Regions) == 0 {
		return nil, errNoPricingRegions
	}

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
					price, ok := parsePrice(os.Prices.USD)
					if !ok {
						price = 0
					}

					if os.Name == priceColumnWindows {
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

	// EqualFold, not ==: getRegionAdvice accepts "Windows" case-insensitively,
	// so an exact compare here returned Windows savings alongside the Linux
	// price for `--os Windows`.
	if strings.EqualFold(os, osWindows) {
		return price.Windows, nil
	}

	return price.Linux, nil
}
