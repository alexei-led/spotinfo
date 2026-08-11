package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"

	"spotinfo/internal/cloud"
	"spotinfo/internal/feedcache"
	"spotinfo/internal/snapshot"
)

// Live Azure prices.
//
// The Retail Prices API is anonymous: no subscription, no tenant, no
// credential library. It is the same endpoint, the same OData filter and the
// same parser the committed snapshot was built from, so a live answer differs
// from a snapshot answer only in when the bytes were read. That is the whole
// reason this path is safe to run by default — it cannot widen the source
// contract, only refresh what the contract already approved.
//
// Everything below fails soft. A refused request, a 500, a body that no longer
// parses, a region that comes back too thin: each discards the live overlay and
// the answer falls back to the committed snapshot, reported as
// embedded-snapshot. A live path that can fail a run is worse than no live
// path.

const (
	// livePriceCacheTTL is how long a swept region is served from the cache
	// without asking the API again.
	//
	// Measured against the live endpoint on 2026-08-11, one region (westeurope)
	// returns 9,022 meters over 10 pages and 5.5 MB, in 6.9 s. Of those rows
	// 8,896 carry an effectiveStartDate on the first of a month and only 126 do
	// not: the API publishes price *intervals*, and their boundaries land on a
	// monthly cadence. It also serves `cache-control: no-cache` with neither an
	// ETag nor a Last-Modified, so an expired entry cannot be revalidated with a
	// conditional request the way both AWS feeds are — it is re-downloaded in
	// full.
	//
	// A document that turns over monthly, costs 5.5 MB and ten round trips to
	// read, and offers no cheap way to ask whether it moved, is the same shape
	// as the AWS advisor feed, and it gets the same window: one day of staleness
	// against a roughly thirty-day cadence.
	livePriceCacheTTL = 24 * time.Hour

	// liveSweepBudget bounds the whole enrichment, not one request. It matches
	// the budget the GCP live-risk path already spends on an optional extra to
	// an answer that is complete without it.
	liveSweepBudget = 20 * time.Second

	// maxLiveRegions is that budget divided by the measured cost of one region.
	// A sweep is ~7 s, so two fit with room for a slower link and a third does
	// not. A query naming more regions than this is answered from the snapshot
	// rather than half-fetched: a mixed answer could not report one honest mode.
	maxLiveRegions = 2

	// maxLivePages bounds pagination for one region. Ten pages is the observed
	// depth; the ceiling is what stops a NextPageLink loop.
	maxLivePages = 40

	// maxLivePageBytes bounds one downloaded page. Pages measure hundreds of
	// kilobytes.
	maxLivePageBytes = 8 << 20

	// cacheNamePrefix namespaces this provider's cache entries beside the AWS
	// feeds in the same directory.
	cacheNamePrefix = "azure-prices-"

	// liveUserAgent identifies the tool to the API, as the updater does.
	liveUserAgent = "spotinfo/1.0 (+https://github.com/alexei-led/spotinfo)"
)

// The two shapes a completed sweep is still refused for. Both mirror a check
// Catalog.Verify applies to the committed snapshot, because a live region
// replaces a committed one and must be held to the same shape.
var (
	// errThinRegion reports a sweep that priced fewer machines than the reviewed
	// per-region floor.
	errThinRegion = errors.New("live sweep prices fewer machines than the contract floor")
	// errUnpricedOS reports a sweep that priced no machine for an operating
	// system the catalogue publishes. Every one of the 55 committed regions
	// prices both, so this is the shape that would let Windows meters simply
	// disappear and leave a Linux-only region that still clears the floor.
	errUnpricedOS = errors.New("live sweep prices no machine for an operating system the catalogue publishes")
)

// LivePriceConfig is what a composer hands the provider. Client, Endpoint and
// Budget are injection points for tests — the only way the request shape and
// the response handling below are exercised without reaching Azure — and
// production leaves all three zero.
type LivePriceConfig struct {
	Client   *http.Client
	Endpoint string
	Budget   time.Duration
	Policy   cloud.FetchPolicy
}

// WithLivePrices returns a provider that answers the given data policy. The
// receiver is unchanged, so a shared snapshot provider stays shareable.
func (p *Provider) WithLivePrices(config LivePriceConfig) *Provider {
	enriched := *p
	enriched.live = config

	return &enriched
}

// liveEnabled reports whether this provider can reach the Retail Prices API at
// all. It is false only when the committed contract names no price source,
// which is the same shape as reading the operating systems out of the
// catalogue: the provider offers what its data supports, not what it wishes.
func (p *Provider) liveEnabled() bool { return p.liveBase() != "" }

// liveBase is the endpoint a sweep reads: the contracted one, or a test's.
func (p *Provider) liveBase() string {
	if p.live.Endpoint != "" {
		return p.live.Endpoint
	}

	return p.retailBase
}

// liveRegion is one region's freshly resolved prices and the provenance of the
// document they came from.
type liveRegion struct {
	source  cloud.SourceRef
	prices  []CatalogPrice
	fetched bool
}

// liveOverlay replaces the snapshot's prices for the regions it covers.
//
// Every accessor below is nil-safe, because a nil overlay is the normal case
// and the query loop must read one shape whether or not anything was fetched.
type liveOverlay struct {
	regions map[cloud.Region]liveRegion
	mode    cloud.DataMode
}

// pricesFor returns the rows to answer one region from.
func (o *liveOverlay) pricesFor(region *CatalogRegion) []CatalogPrice {
	if o == nil {
		return region.Prices
	}

	if live, covered := o.regions[region.ID]; covered {
		return live.prices
	}

	return region.Prices
}

// dataMode reports how fresh the answer is.
func (o *liveOverlay) dataMode() cloud.DataMode {
	if o == nil {
		return cloud.DataModeEmbeddedSnapshot
	}

	return o.mode
}

// sources republishes the provenance of every live region.
//
// The URL is unchanged — a live sweep reads the exact request the manifest
// recorded — but the content hash and the fetch time are not, and publishing
// the snapshot's for a price that came from the API this run would be a claim
// nothing supports. A region the overlay does not cover keeps its snapshot
// source untouched.
func (o *liveOverlay) sources(snapshotSources []cloud.SourceRef) []cloud.SourceRef {
	published := slices.Clone(snapshotSources)
	if o == nil {
		return published
	}

	for i := range published {
		if live, covered := o.regions[published[i].Scope.Region]; covered {
			published[i] = live.source
		}
	}

	return published
}

// livePrices resolves fresh prices for the queried regions, or reports that the
// snapshot answers.
//
// A nil overlay is the normal outcome for most invocations — offline, every
// region at once, or anything that did not work — and it is never an error.
func (p *Provider) livePrices(ctx context.Context, query *cloud.Query) *liveOverlay {
	regions := p.liveRegions(query)
	if len(regions) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, p.liveBudget())
	defer cancel()

	cache := feedcache.Open()
	resolved := make(map[cloud.Region]liveRegion, len(regions))
	mode := cloud.DataModeLive

	for _, region := range regions {
		live, err := p.liveRegion(ctx, cache, region)
		if err != nil {
			// One region short means the answer cannot be one mode, so the whole
			// overlay goes rather than half of it.
			slog.Debug("azure live prices unavailable; answering from the committed snapshot",
				slog.String("region", string(region)), slog.Any("error", err))

			return nil
		}

		if !live.fetched {
			// An unrevalidated copy is the weaker claim, and one of them makes it
			// the claim for the whole answer. This API publishes no validator, so
			// there is no third state: a cached region was not confirmed current
			// this run and cannot be reported as live.
			mode = cloud.DataModeCached
		}

		resolved[region] = live
	}

	return &liveOverlay{regions: resolved, mode: mode}
}

// liveRegions is the region set a live sweep would cover: the queried regions,
// narrowed to the ones the approved contract already covers.
//
// "Every region" is not a live question. The default query asks for all 55,
// which is six minutes and 300 MB, so it is answered from the snapshot the
// contract built for exactly that purpose. A region the contract does not name
// is skipped rather than fetched: the live path refreshes approved coverage and
// must never widen it, and skipping also keeps the cache file name below out of
// a caller's hands.
func (p *Provider) liveRegions(query *cloud.Query) []cloud.Region {
	if !p.liveEnabled() || p.live.Policy.Offline || len(query.Regions) == 0 {
		return nil
	}
	if slices.Contains(query.Regions, cloud.RegionAll) {
		return nil
	}

	covered := make([]cloud.Region, 0, len(query.Regions))
	for _, region := range query.Regions {
		if _, known := p.regions[region]; known && !slices.Contains(covered, region) {
			covered = append(covered, region)
		}
	}

	if len(covered) > maxLiveRegions {
		slog.Debug("too many regions for a live azure sweep; answering from the committed snapshot",
			slog.Int("regions", len(covered)), slog.Int("max", maxLiveRegions))

		return nil
	}

	return covered
}

func (p *Provider) liveBudget() time.Duration {
	if p.live.Budget > 0 {
		return p.live.Budget
	}

	return liveSweepBudget
}

// liveRegion resolves one region from the cache or the API and turns it into
// catalogue rows.
func (p *Provider) liveRegion(ctx context.Context, cache *feedcache.Cache,
	region cloud.Region,
) (liveRegion, error) {
	first, err := RetailRequestURL(p.liveBase(), region)
	if err != nil {
		return liveRegion{}, err
	}

	name := cacheNamePrefix + string(region)
	now := time.Now().UTC()

	payload, entry, hit := cache.Load(name)
	fetchedAt := now
	fetched := true

	if hit && !p.live.Policy.Refresh && entry.Fresh(first, livePriceCacheTTL, now) {
		fetchedAt, fetched = entry.FetchedAt, false
	} else {
		payload, err = p.sweepRegion(ctx, first)
		if err != nil {
			return liveRegion{}, err
		}
		cache.Save(name, payload, &feedcache.Entry{FetchedAt: now, URL: first})
	}

	prices, err := p.livePricesFrom(payload, now)
	if err != nil {
		return liveRegion{}, fmt.Errorf("%s: %w", region, err)
	}

	return liveRegion{
		prices:  prices,
		fetched: fetched,
		source: cloud.SourceRef{
			FetchedAt:     fetchedAt,
			URL:           first,
			ContentSHA256: snapshot.SHA256Hex(payload),
			ParserVersion: ParserVersion,
			SchemaVersion: CatalogSchemaVersion,
			Scope:         cloud.SourceScope{Region: region},
		},
	}, nil
}

// livePricesFrom turns one region's raw pages into catalogue rows.
//
// The rows are narrowed to the reviewed sizes *before* they are parsed and
// resolved, which is what RetainSpecified does for the weekly build and for the
// same reason: a region prices a few thousand sizes, the contract covers a
// couple of hundred, and an expired interval or a conflicting price in a size
// this binary never publishes must not cost a caller the live prices of the
// sizes it does.
func (p *Provider) livePricesFrom(payload []byte, at time.Time) ([]CatalogPrice, error) {
	items, err := decodeRetailStream(payload)
	if err != nil {
		return nil, err
	}

	specified := items[:0]
	for i := range items {
		if _, known := p.specs[cloud.MachineID(items[i].ArmSkuName)]; known {
			specified = append(specified, items[i])
		}
	}

	observations, err := AcceptRows(specified)
	if err != nil {
		return nil, err
	}

	rows, _, err := SelectCurrent(observations, at)
	if err != nil {
		return nil, err
	}

	prices := pairLivePrices(rows)
	// The reviewed per-region floor, reused rather than re-invented. It is what
	// verifyRegions holds the committed catalogue to, so a live sweep that comes
	// back thinner than the snapshot it would replace is refused for the same
	// reason a thin refresh is.
	if machines := distinctMachines(prices); machines < p.minMachines {
		return nil, fmt.Errorf("%w: %d machines against a floor of %d", errThinRegion, machines, p.minMachines)
	}
	// The other half of Catalog.verifyOperatingSystems, for the same reason it
	// exists: the " Windows" suffix that classifies a meter is undocumented, and
	// a sweep that stopped producing Windows rows would otherwise clear the floor
	// on Linux alone and answer --os windows with nothing while the snapshot it
	// replaced has rows. A renamed suffix is already caught upstream — the two
	// meters collide on one key at two amounts and SelectCurrent refuses — so
	// this covers the case where they simply stop arriving.
	if missing := unpricedOS(prices, p.catalog.OperatingSystems); missing != "" {
		return nil, fmt.Errorf("%w: %s", errUnpricedOS, missing)
	}

	return prices, nil
}

// unpricedOS names the first operating system the catalogue publishes that this
// sweep priced no machine for.
func unpricedOS(prices []CatalogPrice, published []cloud.OperatingSystem) cloud.OperatingSystem {
	priced := make(map[cloud.OperatingSystem]struct{}, len(published))
	for i := range prices {
		priced[prices[i].OS] = struct{}{}
	}

	for _, operatingSystem := range published {
		if _, found := priced[operatingSystem]; !found {
			return operatingSystem
		}
	}

	return ""
}

// pairLivePrices joins each size's Spot and On-Demand rows, through the same
// pairing the committed catalogue is built with, and then applies the one row
// check Verify would have applied at build time.
func pairLivePrices(rows []PriceRow) []CatalogPrice {
	byRegion := make(map[cloud.Region]map[machineOS]*pair, 1)

	for i := range rows {
		row := &rows[i]

		machines, seen := byRegion[row.Region]
		if !seen {
			machines = make(map[machineOS]*pair)
			byRegion[row.Region] = machines
		}

		key := machineOS{machine: row.Machine, os: row.OS}
		if machines[key] == nil {
			machines[key] = &pair{}
		}

		if row.Class == cloud.PriceClassSpot {
			machines[key].spot = row.Amount
		} else {
			machines[key].onDemand = row.Amount
		}
	}

	regions, _, _ := buildRegions(byRegion)

	var prices []CatalogPrice

	for i := range regions {
		for j := range regions[i].Prices {
			price := regions[i].Prices[j]
			// A Spot price at or above list price means the two meters were joined
			// wrongly, which verifyPrices fails a refresh on. A live sweep cannot
			// fail the run, so the row is dropped instead — and if that is
			// systemic, the floor below refuses the whole sweep.
			if price.Spot.Nanos() >= price.OnDemand.Nanos() {
				continue
			}
			prices = append(prices, price)
		}
	}

	return prices
}

func distinctMachines(prices []CatalogPrice) int {
	seen := make(map[cloud.MachineID]struct{}, len(prices))
	for i := range prices {
		seen[prices[i].Machine] = struct{}{}
	}

	return len(seen)
}

// sweepRegion follows one region's pagination and returns the concatenated
// response bodies — the same bytes the updater hashes into the manifest, so the
// content_sha256 a live answer publishes is verifiable the same way.
func (p *Provider) sweepRegion(ctx context.Context, first string) ([]byte, error) {
	var (
		bodies bytes.Buffer
		next   = first
	)

	client := p.liveClient()

	for pages := 0; next != ""; pages++ {
		if pages == maxLivePages {
			return nil, fmt.Errorf("%w: pagination did not terminate within %d pages",
				ErrSourceContract, maxLivePages)
		}
		// A NextPageLink is response-supplied. Following one off the contracted
		// host would let a single reply redirect the sweep somewhere nobody
		// approved, and the bytes would then be published under the contracted
		// URL.
		if err := sameLiveHost(first, next); err != nil {
			return nil, err
		}

		body, err := fetchLivePage(ctx, client, next)
		if err != nil {
			return nil, err
		}
		bodies.Write(body)

		page, err := DecodeRetailPage(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		next = page.NextPageLink
	}

	return bodies.Bytes(), nil
}

// liveClient returns the injected client, or one that refuses a redirect off
// the contracted host for the same reason sameLiveHost exists.
func (p *Provider) liveClient() *http.Client {
	if p.live.Client != nil {
		return p.live.Client
	}

	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return sameLiveHost(via[0].URL.String(), req.URL.String())
		},
	}
}

func fetchLivePage(ctx context.Context, client *http.Client, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build retail price request: %w", err)
	}
	request.Header.Set("User-Agent", liveUserAgent)
	request.Header.Set("Accept-Language", "en")

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("retail price request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: retail prices returned %s", ErrSourceContract, response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxLivePageBytes))
	if err != nil {
		return nil, fmt.Errorf("read retail price response: %w", err)
	}
	if len(body) == maxLivePageBytes {
		return nil, fmt.Errorf("%w: a page hit the %d byte ceiling and may be truncated",
			ErrSourceContract, maxLivePageBytes)
	}

	return body, nil
}

// sameLiveHost keeps a follow-on request inside the contracted service.
func sameLiveHost(first, next string) error {
	left, err := url.Parse(first)
	if err != nil {
		return fmt.Errorf("%w: parse request url: %w", ErrSourceContract, err)
	}

	right, err := url.Parse(next)
	if err != nil {
		return fmt.Errorf("%w: parse follow-on url: %w", ErrSourceContract, err)
	}

	if right.Scheme != left.Scheme || right.Hostname() != left.Hostname() {
		return fmt.Errorf("%w: %s points at %s://%s, not the contracted %s://%s",
			ErrSourceContract, next, right.Scheme, right.Hostname(), left.Scheme, left.Hostname())
	}

	return nil
}

// decodeRetailStream reads the concatenated pages a sweep cached. The pages are
// stored as they arrived rather than re-encoded, because the concatenation is
// what the published content_sha256 hashes.
func decodeRetailStream(payload []byte) ([]RetailItem, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))

	var items []RetailItem

	for {
		var page RetailPage

		err := decoder.Decode(&page)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrSourceContract, err)
		}

		items = append(items, page.Items...)
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("%w: retail price document carries no items", ErrSourceContract)
	}

	return items, nil
}

// RetailPriceBase is the one contracted source that supplies amounts. It lives
// here rather than in the updater because both the weekly refresh and the live
// path must read the same approved endpoint.
func RetailPriceBase(contract *snapshot.SourceContract) (string, error) {
	for i := range contract.Sources {
		source := &contract.Sources[i]
		if source.SourceType == snapshot.SourceTypeDocumentedRESTAPI &&
			slices.Contains(source.DataKinds, snapshot.DataKindSpotPrice) {
			return source.URL, nil
		}
	}

	return "", fmt.Errorf("%w: no contracted rest api supplies spot prices", snapshot.ErrInvalidSourceContract)
}
