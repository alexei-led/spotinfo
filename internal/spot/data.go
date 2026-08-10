package spot

import (
	"bytes"
	"compress/gzip"
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

// The two large feeds are embedded gzipped, as the GCP and Azure catalogues
// already are. Raw, they are 3.8 MB of the binary; compressed they are 341 KB,
// because both repeat the same machine-size and OS strings in every region.
//
// The readable .json stays committed next to each archive and remains the file a
// data-refresh pull request is reviewed from; TestEmbeddedArchivesMatchTheirJSON
// proves the archive is exactly that file, so the two cannot drift. The manifest
// hashes the archive, because the archive is what ships.
//
// architecture-snapshot.json is left raw on purpose: it is 3.8 KB, so
// compressing it would save 3 KB and add a third decode path.

//go:embed data/spot-advisor-data.json.gz
var embeddedSpotData []byte

//go:embed data/spot-price-data.json.gz
var embeddedPriceData []byte

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

// feed describes one AWS document this package can fetch, and how to fall back
// when it cannot.
type feed[T any] struct {
	// embedded is the committed copy, used when the caller asked for it and
	// whenever a fetch cannot produce something usable.
	embedded func() (*T, error)
	parse    func([]byte) (*T, error)
	// validate rejects a document that parsed but lost coverage, so a truncated
	// live feed cannot replace a complete snapshot.
	validate func(*T) error
	// name appears in the log lines, so "advisor" and "pricing" read as before.
	// name appears in log lines and names the cache entry.
	name string
	url  string
	// ttl is how long a cached copy is served without asking the origin.
	ttl time.Duration
}

// feedOrigin records where an answer actually came from, so the CLI and the MCP
// tools can say so instead of implying every non-embedded answer is current.
type feedOrigin int

const (
	// originLive means the document was fetched, or the origin confirmed the
	// cached copy is still current. Either way it matches AWS right now.
	originLive feedOrigin = iota
	// originCached means a cached copy was served without asking the origin.
	originCached
	// originEmbedded means the committed snapshot answered.
	originEmbedded
)

// fetchOptions carries what the caller decided about freshness.
type fetchOptions struct {
	// useEmbedded answers from the committed snapshot and makes no request.
	useEmbedded bool
	// refresh ignores any cached copy for this run. The fetched document still
	// replaces it, so --refresh repairs a cache rather than bypassing it forever.
	refresh bool
}

// fetchFeed answers one AWS feed from the freshest source it can reach.
//
// The order is deliberate: a cached copy inside its time-to-live, then the
// origin, then a cached copy that has expired, then the committed snapshot. The
// expired copy sits above the snapshot because it is AWS data that is merely
// old, while the snapshot is AWS data that is old *and* frozen at build time.
//
// Every failure falls through rather than erroring: something usable always
// exists, so a transient upstream problem must not fail the command.
func fetchFeed[T any](ctx context.Context, options fetchOptions, source feed[T]) (*T, feedOrigin, error) {
	if options.useEmbedded {
		result, err := source.embedded()

		return result, originEmbedded, err
	}

	cache := openFeedCache()
	now := time.Now()

	cached, entry, hit := cache.load(source.name)
	if hit && !options.refresh && entry.fresh(source.url, source.ttl, now) {
		if result, err := source.parse(cached); err == nil && source.validate(result) == nil {
			slog.Debug("serving a cached feed",
				slog.String("feed", source.name),
				slog.Duration("age", now.Sub(entry.FetchedAt)))

			return result, originCached, nil
		}

		// A cached document that no longer parses is not worth keeping; fall
		// through and let the fetch below replace it.
		slog.Debug("cached feed is unusable; refetching", slog.String("feed", source.name))

		hit = false
	}

	result, err := fetchFromOrigin(ctx, cache, options, source, cached, entry, hit, now)
	if err == nil {
		return result, originLive, nil
	}

	// The origin could not be used. An expired cached copy still beats the
	// snapshot compiled into this binary.
	if hit {
		if stale, parseErr := source.parse(cached); parseErr == nil && source.validate(stale) == nil {
			slog.Warn("serving an expired cached feed; AWS is unreachable",
				slog.String("feed", source.name),
				slog.Duration("age", now.Sub(entry.FetchedAt)))

			return stale, originCached, nil
		}
	}

	fallback, fallbackErr := source.embedded()

	return fallback, originEmbedded, fallbackErr
}

// errFeedUnusable reports that the origin produced nothing this run.
var errFeedUnusable = errors.New("feed unusable")

// fetchFromOrigin performs the conditional request and updates the cache.
//
// A 304 is the point of the whole design: both feeds publish ETag and
// Last-Modified, so confirming an expired copy is still current costs one round
// trip and no payload, instead of re-transferring a document that has not moved
// in months.
//
//nolint:cyclop // one linear request path; each branch is a distinct HTTP outcome
func fetchFromOrigin[T any](ctx context.Context, cache *feedCache, options fetchOptions,
	source feed[T], cached []byte, entry *cacheEntry, hit bool, now time.Time,
) (*T, error) {
	ctx, cancel := withDefaultTimeout(ctx)
	defer cancel()

	client := &http.Client{}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.url, http.NoBody)
	if err != nil {
		return nil, errFeedUnusable
	}

	if hit && !options.refresh {
		applyValidators(req, entry)
	}

	resp, err := client.Do(req) //nolint:gosec // G704: every url is a package-level constant, not user input
	if err != nil {
		slog.Warn("failed to fetch "+source.name+" data from AWS",
			slog.String("url", source.url),
			slog.Any("error", err))

		return nil, errFeedUnusable
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified && hit {
		result, parseErr := source.parse(cached)
		if parseErr == nil && source.validate(result) == nil {
			cache.touch(source.name, entry, now)
			slog.Debug("origin confirmed the cached feed is current", slog.String("feed", source.name))

			return result, nil
		}

		return nil, errFeedUnusable
	}

	if resp.StatusCode != http.StatusOK {
		slog.Warn("non-200 response from AWS "+source.name+" API", //nolint:gosec // G706: status_code is an integer from the HTTP response, not user-controlled input
			slog.Int("status_code", resp.StatusCode))

		return nil, errFeedUnusable
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("failed to read "+source.name+" response body", slog.Any("error", err))

		return nil, errFeedUnusable
	}

	result, err := source.parse(body)
	if err == nil {
		err = source.validate(result)
	}

	if err != nil {
		slog.Warn("unusable "+source.name+" data from AWS", slog.Any("error", err))

		return nil, errFeedUnusable
	}

	cache.save(source.name, body, &cacheEntry{
		FetchedAt:    now,
		URL:          source.url,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	})
	slog.Debug("successfully fetched " + source.name + " data from AWS")

	return result, nil
}

// fetchAdvisorData retrieves spot advisor data from AWS or falls back to embedded data.
func fetchAdvisorData(ctx context.Context, options fetchOptions) (*advisorData, feedOrigin, error) {
	return fetchFeed(ctx, options, feed[advisorData]{
		name:     advisorFeedName,
		url:      spotAdvisorJSONURL,
		embedded: loadEmbeddedAdvisorData,
		parse:    parseAdvisorResponse,
		validate: validateAdvisorCoverage,
		ttl:      advisorCacheTTL,
	})
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

// maxEmbeddedFeedBytes bounds a decompressed feed. The two committed feeds are
// under 3 MB; the ceiling keeps a corrupted archive from expanding without limit.
const maxEmbeddedFeedBytes = 64 << 20

// decompressEmbedded expands one committed feed archive.
//
// The bytes it returns are heap-allocated, unlike a raw //go:embed string, which
// is demand-paged from the executable's read-only mapping. That is the trade the
// compression makes: ~3.4 MB off the binary for one transient copy of each feed
// during load. It is transient because only the parsed result is retained — the
// decompressed JSON is garbage after Unmarshal returns.
func decompressEmbedded(archive []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = reader.Close() }()

	contents, err := io.ReadAll(io.LimitReader(reader, maxEmbeddedFeedBytes))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	if len(contents) == maxEmbeddedFeedBytes {
		return nil, fmt.Errorf("archive hit the %d byte ceiling", maxEmbeddedFeedBytes) //nolint:err113
	}

	return contents, nil
}

// loadEmbeddedAdvisorData loads embedded advisor data as fallback.
func loadEmbeddedAdvisorData() (*advisorData, error) {
	if err := advisorPayloadVerified(); err != nil {
		return nil, err
	}

	contents, err := decompressEmbedded(embeddedSpotData)
	if err != nil {
		return nil, fmt.Errorf("embedded spot advisor archive: %w", err)
	}

	var result advisorData
	if err := json.Unmarshal(contents, &result); err != nil {
		return nil, fmt.Errorf("failed to parse embedded spot data: %w", err)
	}

	result.Embedded = true
	slog.Debug("using embedded advisor data")
	return &result, nil
}

// fetchPricingData retrieves spot pricing data from AWS or falls back to embedded data.
func fetchPricingData(ctx context.Context, options fetchOptions) (*rawPriceData, feedOrigin, error) {
	return fetchFeed(ctx, options, feed[rawPriceData]{
		name:     "pricing",
		url:      spotPriceJSURL,
		embedded: loadEmbeddedPricingData,
		parse:    parsePricingResponse,
		validate: validatePriceCoverage,
		ttl:      priceCacheTTL,
	})
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
	if err := pricePayloadVerified(); err != nil {
		return nil, err
	}

	contents, err := decompressEmbedded(embeddedPriceData)
	if err != nil {
		return nil, fmt.Errorf("embedded spot price archive: %w", err)
	}

	var result rawPriceData
	if err := json.Unmarshal(contents, &result); err != nil {
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
