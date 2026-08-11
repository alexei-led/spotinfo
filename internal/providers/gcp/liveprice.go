package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"spotinfo/internal/cloud"
	"spotinfo/internal/feedcache"
	"spotinfo/internal/snapshot"
)

// Live GCP prices, and the regions they unlock.
//
// The committed catalogue covers us-central1 and nothing else: Google's
// server-rendered pricing pages publish one region at a time, and 55 of them
// would be 55 scrapes a week for a snapshot nobody reviewed. The Cloud Billing
// Catalog API publishes every region at once, needs only an API key, and needs
// no IAM permission — but Google states no redistribution terms for it, which is
// exactly why nothing it returns may ever be written into a snapshot. It arrives
// here instead, for one invocation, the way --live-risk arrives.
//
// **This path derives prices; the snapshot scrapes them.** The API prices
// *components* — a per-core rate and a per-GiB-of-memory rate for each machine
// series — and a machine price is their weighted sum, which is how Google
// documents predefined machine types being billed. That is a second derivation
// of the same figure, so the two are never mixed: an overlay covers every
// queried region or it is dropped whole, and within a region every published
// price comes from the same document. A caller who supplies a key therefore sees
// slightly different numbers for us-central1 than one who does not — same
// vendor, same instant, two derivations — and that is the honest reading of
// "live prices", not a defect to paper over by blending them.
//
// Everything below fails soft, the way the Azure sweep does. A missing key, a
// rejected key, a 500, a document that no longer parses, a region the API does
// not price: each discards the overlay and the answer falls back to the
// committed snapshot, which for a region outside it means no candidates —
// exactly what the same query answers today.

const (
	// computeEngineServiceID is Compute Engine's service identifier in the
	// billing catalogue. Service identifiers are stable and public; this one is
	// what scopes the SKU list to virtual machines.
	computeEngineServiceID = "6F81-5844-456A"

	// billingSKUEndpoint lists one service's SKUs. It carries no key: the key
	// travels in a header, so it never reaches a URL, a cache file name, a log
	// line or the provenance a published answer cites.
	billingSKUEndpoint = "https://cloudbilling.googleapis.com/v1/services/" +
		computeEngineServiceID + "/skus"

	// apiKeyHeader is Google's documented header form of the `key` query
	// parameter. Header rather than query so a secret cannot leak through any of
	// the places a URL is copied to.
	apiKeyHeader = "X-Goog-Api-Key" //nolint:gosec // G101: a header name, not a credential

	// LivePriceParserVersion identifies this derivation in the provenance a live
	// answer publishes. It is deliberately not ParserVersion: that one names the
	// HTML scrape the committed snapshot was built with, and reporting a composed
	// price under it would claim a reader could reproduce it from the pricing
	// pages.
	LivePriceParserVersion = "gcp-billing-catalog/1"

	// skuPageSize is the API's maximum page size, which is what keeps the SKU
	// list to a handful of round trips rather than hundreds.
	skuPageSize = 5000

	// maxSKUPages bounds pagination. It is what stops a nextPageToken loop, not
	// an expectation about the catalogue's depth.
	maxSKUPages = 40

	// maxSKUPageBytes bounds one downloaded page.
	maxSKUPageBytes = 32 << 20

	// livePriceCacheTTL is how long the SKU document is served from the cache
	// without asking again. The catalogue turns over when Google changes a
	// published rate, which is a monthly cadence at most, and the document is
	// large — the same shape, and the same window, as the Azure retail sweep.
	livePriceCacheTTL = 24 * time.Hour

	// livePriceBudget bounds the whole lookup, not one page.
	livePriceBudget = 30 * time.Second

	// livePriceCacheName namespaces this document beside the other cached feeds.
	// It carries no key and no region: the document is one catalogue for every
	// region, filtered here.
	livePriceCacheName = "gcp-billing-skus"

	// liveUserAgent identifies the tool to the API, as the snapshot updater does.
	liveUserAgent = "spotinfo/1.0 (+https://github.com/alexei-led/spotinfo)"

	// resourceFamilyCompute is the only SKU family that prices virtual machines.
	resourceFamilyCompute = "Compute"

	// usageTypeOnDemand and usageTypePreemptible are the two usage types this
	// tool publishes. The class is read from this field rather than from the
	// description, because it is the field Google documents as carrying it.
	usageTypeOnDemand    = "OnDemand"
	usageTypePreemptible = "Preemptible"

	// coreUsageUnit and ramUsageUnit are required exactly. A GB-per-hour memory
	// rate read as GiB-per-hour is a silent 7% price error that every other gate
	// here would pass, so the unit is a contract rather than an assumption.
	coreUsageUnit = "h"
	ramUsageUnit  = "GiBy.h"

	// componentCore and componentRAM are the two priced components, spelled the
	// way the description grammar capitalises them once folded.
	componentCore = "core"
	componentRAM  = "ram"

	// usdCurrency is the only currency this catalogue publishes in.
	usdCurrency = "USD"

	// minLiveMachines is the floor for a region the committed contract does not
	// cover. It is one, deliberately: a region outside the snapshot has no
	// reviewed machine count to be held to, and Google genuinely sells fewer
	// series in a small region than in us-central1 — holding it to us-central1's
	// floor would refuse most of the world to catch a defect the parse gates
	// above already catch. A region the snapshot *does* cover keeps the reviewed
	// floor, because thinning there is a regression against data a reviewer
	// accepted.
	minLiveMachines = 1
)

// The failures that discard an overlay. Each is a fallback to the committed
// snapshot, never a failed run.
var (
	// errThinRegion reports a region that priced fewer machines than its floor.
	errThinRegion = errors.New("live prices cover fewer machines than the floor for this region")
	// errNoSKUs reports a document that carries no SKU at all.
	errNoSKUs = errors.New("billing catalogue document carries no skus")
	// errSKUPagination reports pagination that did not terminate.
	errSKUPagination = errors.New("billing catalogue pagination did not terminate")
	// errSKUStatus reports a refused or failed request — a rejected key, a
	// disabled Billing API, an outage.
	errSKUStatus = errors.New("billing catalogue request was refused")
)

// skuDescriptionPattern reads the machine series and the priced component out
// of a SKU description.
//
// Exact rather than fuzzy, in both halves. The series token is matched against
// the contract's reviewed series list character for character, so a description
// this grammar does not recognise drops the machine rather than guessing which
// family it belongs to — "N2 Custom Instance Core", "Sole Tenancy Instance
// Core" and "Memory-optimized Instance Core" all fail to match, which is the
// intended outcome for all three. The optional affixes are the two Google
// actually publishes: a "Spot Preemptible" prefix on preemptible SKUs and a
// "Predefined" infix on N1.
var skuDescriptionPattern = regexp.MustCompile(
	`^(?:Spot )?(?:Preemptible )?([A-Za-z0-9]+)(?: Predefined)? Instance (Core|Ram) running in `)

// LivePriceConfig is what a composer hands the provider.
//
// APIKey is the opt-in: with none, this provider is exactly the offline one.
// Client and Endpoint are the injection points for tests — the only way the
// request shape and the response handling below are exercised without a key and
// without reaching Google — and production leaves both zero.
type LivePriceConfig struct {
	Client   *http.Client
	APIKey   string
	Endpoint string
	Budget   time.Duration
	Policy   cloud.FetchPolicy
}

// WithLivePrices returns a provider that answers the queried regions from the
// billing catalogue when a key was supplied. The receiver is unchanged, so a
// shared snapshot provider stays shareable — and, because the catalogue behind
// it is a pointer, nothing here ever writes a fetched price into it.
func (p *Provider) WithLivePrices(config LivePriceConfig) *Provider {
	enriched := *p
	enriched.live = config

	return &enriched
}

func (p *Provider) liveEndpoint() string { return endpointOr(p.live.Endpoint, billingSKUEndpoint) }

func (p *Provider) liveBudget() time.Duration {
	if p.live.Budget > 0 {
		return p.live.Budget
	}

	return livePriceBudget
}

// livePrice is one machine's composed pair.
type livePrice struct {
	spot     cloud.Money
	onDemand cloud.Money
}

// liveOverlay is a fetched answer for a set of regions. A nil overlay is the
// normal case — no key, every region at once, or anything that did not work —
// so every accessor below is nil-safe.
type liveOverlay struct {
	source  cloud.SourceRef
	prices  map[cloud.Region]map[cloud.MachineID]livePrice
	mode    cloud.DataMode
	regions []cloud.Region
}

// dataMode reports how fresh the answer is.
func (o *liveOverlay) dataMode() cloud.DataMode {
	if o == nil {
		return cloud.DataModeEmbeddedSnapshot
	}

	return o.mode
}

// sources publishes the billing catalogue *beside* the committed pages rather
// than instead of them.
//
// The amounts came from the API, but the vCPU count, the memory and the
// architecture of every candidate still come from the scraped snapshot, and
// Invariant 7 says provenance may never drop the source of a value a candidate
// carries. Dropping the pages would strip the specification's provenance;
// dropping the API would publish a price no listed source produced.
func (o *liveOverlay) sources(snapshotSources []cloud.SourceRef) []cloud.SourceRef {
	published := slices.Clone(snapshotSources)
	if o == nil {
		return published
	}

	return append(published, o.source)
}

// livePrices resolves fresh prices for the queried regions, or reports that the
// snapshot answers.
func (p *Provider) livePrices(ctx context.Context, query *cloud.Query) *liveOverlay {
	regions := p.liveRegions(query)
	if len(regions) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, p.liveBudget())
	defer cancel()

	payload, fetchedAt, fetched, err := p.fetchSKUs(ctx)
	if err != nil {
		slog.Debug("gcp billing catalogue unavailable; answering from the committed snapshot",
			slog.Any("error", err))

		return nil
	}

	items, err := decodeSKUStream(payload)
	if err != nil {
		slog.Debug("gcp billing catalogue is not readable; answering from the committed snapshot",
			slog.Any("error", err))

		return nil
	}

	prices := make(map[cloud.Region]map[cloud.MachineID]livePrice, len(regions))

	for _, region := range regions {
		priced, regionErr := p.regionPrices(items, region)
		if regionErr != nil {
			// One region short means the answer would mix a composed price with a
			// scraped one, so the whole overlay goes rather than half of it.
			slog.Debug("gcp live prices unavailable; answering from the committed snapshot",
				slog.String("region", string(region)), slog.Any("error", regionErr))

			return nil
		}

		prices[region] = priced
	}

	mode := cloud.DataModeLive
	if !fetched {
		// This API publishes no ETag and no Last-Modified, so an expired entry
		// cannot be revalidated: a cached document was not confirmed current this
		// run and must not be reported as live.
		mode = cloud.DataModeCached
	}

	return &liveOverlay{
		prices:  prices,
		regions: regions,
		mode:    mode,
		source: cloud.SourceRef{
			FetchedAt:     fetchedAt,
			URL:           p.liveEndpoint(),
			ContentSHA256: snapshot.SHA256Hex(payload),
			ParserVersion: LivePriceParserVersion,
			SchemaVersion: CatalogSchemaVersion,
		},
	}
}

// liveRegions is the region set a live lookup would cover: the queried regions,
// in the order they were asked for.
//
// "Every region" is not a live question — the default query asks for it, and
// answering that from a derived price would replace the reviewed snapshot on
// every invocation that named no region at all. An unset region list is the
// same case.
func (p *Provider) liveRegions(query *cloud.Query) []cloud.Region {
	if p.live.APIKey == "" || p.live.Policy.Offline || len(query.Regions) == 0 {
		return nil
	}
	if slices.Contains(query.Regions, cloud.RegionAll) {
		return nil
	}

	regions := make([]cloud.Region, 0, len(query.Regions))
	for _, region := range query.Regions {
		if region != "" && !slices.Contains(regions, region) {
			regions = append(regions, region)
		}
	}

	return regions
}

// regionPrices composes one region's machine prices from the SKU document.
func (p *Provider) regionPrices(items []billingSKU, region cloud.Region) (map[cloud.MachineID]livePrice, error) {
	rates := p.componentRates(items, region)

	priced := make(map[cloud.MachineID]livePrice, len(p.catalog.Machines))

	for i := range p.catalog.Machines {
		machine := &p.catalog.Machines[i]

		price, composed := composeMachine(machine, rates)
		if !composed {
			continue
		}

		priced[machine.ID] = price
	}

	floor := minLiveMachines
	if region == p.catalog.Region {
		floor = p.minMachines
	}

	if len(priced) < floor {
		return nil, fmt.Errorf("%w: %d machines against a floor of %d", errThinRegion, len(priced), floor)
	}

	slog.Debug("gcp live prices composed",
		slog.String("region", string(region)),
		slog.Int("machines", len(priced)),
		slog.Int("catalogue", len(p.catalog.Machines)))

	return priced, nil
}

// componentKey identifies one priced component: a series, a price class, and
// whether it is the per-core or the per-GiB rate.
type componentKey struct {
	series    string
	class     cloud.PriceClass
	component string
}

// componentRates indexes the SKUs that price one region.
//
// Two SKUs that agree are harmless redundancy — the catalogue lists a rate
// under more than one geography for multi-region services — but two that
// disagree are ambiguity with no safe resolution, and both are dropped. That is
// the rule indexRows applies to the scraped pages, for the same reason: picking
// whichever arrived last publishes a price nobody chose.
func (p *Provider) componentRates(items []billingSKU, region cloud.Region) map[componentKey]cloud.Money {
	rates := make(map[componentKey]cloud.Money, len(p.series)*2*pricesPerMachine)
	conflicting := make(map[componentKey]struct{})

	for i := range items {
		item := &items[i]

		key, amount, usable := p.readSKU(item, region)
		if !usable {
			continue
		}

		previous, seen := rates[key]
		if seen && previous.Nanos() != amount.Nanos() {
			conflicting[key] = struct{}{}

			continue
		}

		rates[key] = amount
	}

	for key := range conflicting {
		slog.Debug("gcp billing catalogue prices one component twice at different rates",
			slog.String("series", key.series), slog.String("class", string(key.class)),
			slog.String("component", key.component), slog.String("region", string(region)))
		delete(rates, key)
	}

	return rates
}

// readSKU reads one SKU as a component rate, or reports that it is not one.
func (p *Provider) readSKU(item *billingSKU, region cloud.Region) (componentKey, cloud.Money, bool) {
	if item.Category.ResourceFamily != resourceFamilyCompute ||
		!slices.Contains(item.ServiceRegions, string(region)) {
		return componentKey{}, cloud.Money{}, false
	}

	class, priced := priceClassOf(item.Category.UsageType)
	if !priced {
		return componentKey{}, cloud.Money{}, false
	}

	match := skuDescriptionPattern.FindStringSubmatch(item.Description)
	if match == nil {
		return componentKey{}, cloud.Money{}, false
	}

	series := strings.ToLower(match[1])
	if _, approved := p.series[series]; !approved {
		return componentKey{}, cloud.Money{}, false
	}

	component := strings.ToLower(match[2])

	amount, readable := currentRate(item, expectedUsageUnit(component))
	if !readable {
		return componentKey{}, cloud.Money{}, false
	}

	return componentKey{series: series, class: class, component: component}, amount, true
}

// priceClassOf maps the SKU usage type onto a published price class. Every
// other usage type — committed use, custom, free tier — prices something this
// tool does not publish.
func priceClassOf(usageType string) (cloud.PriceClass, bool) {
	switch usageType {
	case usageTypeOnDemand:
		return cloud.PriceClassOnDemand, true
	case usageTypePreemptible:
		return cloud.PriceClassSpot, true
	default:
		return "", false
	}
}

func expectedUsageUnit(component string) string {
	if component == componentRAM {
		return ramUsageUnit
	}

	return coreUsageUnit
}

// currentRate reads the rate a SKU charges now.
//
// The last pricing entry is the current one: the API returns them in ascending
// effective order, and the tier that starts at zero usage is the rate a single
// machine-hour is billed at. The unit is required to match exactly, because a
// per-GB memory rate multiplied by a GiB figure is a wrong price that every
// other check here would accept.
func currentRate(item *billingSKU, unit string) (cloud.Money, bool) {
	if len(item.PricingInfo) == 0 {
		return cloud.Money{}, false
	}

	pricing := item.PricingInfo[len(item.PricingInfo)-1].PricingExpression
	if pricing.UsageUnit != unit {
		return cloud.Money{}, false
	}

	for i := range pricing.TieredRates {
		tier := &pricing.TieredRates[i]
		if tier.StartUsageAmount != 0 || tier.UnitPrice.CurrencyCode != usdCurrency {
			continue
		}

		units, err := strconv.ParseInt(tier.UnitPrice.Units, 10, 64)
		if err != nil && tier.UnitPrice.Units != "" {
			return cloud.Money{}, false
		}

		amount, err := cloud.MoneyFromNanos(units*nanosPerUnit + tier.UnitPrice.Nanos)
		if err != nil || amount.IsZero() {
			return cloud.Money{}, false
		}

		return amount, true
	}

	return cloud.Money{}, false
}

// nanosPerUnit is one US dollar in the nano units cloud.Money stores. The SKU
// wire format splits an amount into whole units and nanos, and this rejoins
// them.
const nanosPerUnit = 1_000_000_000

// composeMachine prices one machine from its series' component rates.
//
// The arithmetic is Google's own billing model for a predefined machine type:
// the vCPU count times the per-core rate, plus the memory times the per-GiB
// rate. It is done in nano units so the two terms are summed exactly, and only
// the memory product — the one term with a fractional multiplier, 3.75 GiB on
// n1 — is rounded.
//
// A machine missing any of its four rates is dropped rather than half-priced,
// and a composed Spot price at or above the composed list price means the rates
// were joined wrongly, which is the same check the committed catalogue is
// verified with.
func composeMachine(machine *CatalogMachine, rates map[componentKey]cloud.Money) (livePrice, bool) {
	series := SeriesOf(machine.ID)

	spot, ok := composeClass(machine, series, cloud.PriceClassSpot, rates)
	if !ok {
		return livePrice{}, false
	}

	onDemand, ok := composeClass(machine, series, cloud.PriceClassOnDemand, rates)
	if !ok {
		return livePrice{}, false
	}

	if spot.IsZero() || onDemand.IsZero() || spot.Nanos() >= onDemand.Nanos() {
		return livePrice{}, false
	}

	return livePrice{spot: spot, onDemand: onDemand}, true
}

func composeClass(machine *CatalogMachine, series string,
	class cloud.PriceClass, rates map[componentKey]cloud.Money,
) (cloud.Money, bool) {
	core, hasCore := rates[componentKey{series: series, class: class, component: componentCore}]
	ram, hasRAM := rates[componentKey{series: series, class: class, component: componentRAM}]

	if !hasCore || !hasRAM {
		return cloud.Money{}, false
	}

	nanos := int64(machine.VCPU)*core.Nanos() + int64(math.Round(machine.MemoryGiB*float64(ram.Nanos())))

	amount, err := cloud.MoneyFromNanos(nanos)
	if err != nil {
		return cloud.Money{}, false
	}

	return amount, true
}

// fetchSKUs returns the SKU document, from the cache or from the API, with the
// time it was read and whether this run read it.
func (p *Provider) fetchSKUs(ctx context.Context) (payload []byte, fetchedAt time.Time, fetched bool, err error) {
	cache := feedcache.Open()
	endpoint := p.liveEndpoint()
	now := time.Now().UTC()

	cached, entry, hit := cache.Load(livePriceCacheName)
	if hit && !p.live.Policy.Refresh && entry.Fresh(endpoint, livePriceCacheTTL, now) {
		return cached, entry.FetchedAt, false, nil
	}

	payload, err = p.sweepSKUs(ctx, endpoint)
	if err != nil {
		return nil, time.Time{}, false, err
	}

	cache.Save(livePriceCacheName, payload, &feedcache.Entry{FetchedAt: now, URL: endpoint})

	return payload, now, true, nil
}

// sweepSKUs follows the catalogue's pagination and returns the concatenated
// response bodies — the bytes the published content_sha256 hashes, so a caller
// holding a key can verify the provenance by re-reading the same pages.
func (p *Provider) sweepSKUs(ctx context.Context, endpoint string) ([]byte, error) {
	var (
		bodies bytes.Buffer
		token  string
	)

	client := p.live.Client
	if client == nil {
		client = http.DefaultClient
	}

	for pages := 0; ; pages++ {
		if pages == maxSKUPages {
			return nil, fmt.Errorf("%w within %d pages", errSKUPagination, maxSKUPages)
		}

		target, err := skuPageURL(endpoint, token)
		if err != nil {
			return nil, err
		}

		body, err := p.fetchSKUPage(ctx, client, target)
		if err != nil {
			return nil, err
		}
		bodies.Write(body)

		var page billingSKUPage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode billing catalogue page: %w", err)
		}

		token = page.NextPageToken
		if token == "" {
			break
		}
	}

	return bodies.Bytes(), nil
}

// skuPageURL builds one page request. The page token is response-supplied, so
// it is escaped rather than concatenated.
func skuPageURL(endpoint, token string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("%w: parse billing catalogue url: %w", cloud.ErrInvalidArgument, err)
	}

	query := parsed.Query()
	query.Set("pageSize", strconv.Itoa(skuPageSize))

	if token != "" {
		query.Set("pageToken", token)
	}
	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}

func (p *Provider) fetchSKUPage(ctx context.Context, client *http.Client, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build billing catalogue request: %w", err)
	}
	// The key travels here and nowhere else. In the query string it would reach
	// the cache entry, every debug log that prints a URL, and the provenance a
	// published answer cites.
	request.Header.Set(apiKeyHeader, p.live.APIKey)
	request.Header.Set("User-Agent", liveUserAgent)

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("billing catalogue request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorDetailBytes)) //nolint:errcheck // best-effort detail

		return nil, fmt.Errorf("%w: %s: %s", errSKUStatus, response.Status, bytes.TrimSpace(detail))
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxSKUPageBytes))
	if err != nil {
		return nil, fmt.Errorf("read billing catalogue response: %w", err)
	}
	if len(body) == maxSKUPageBytes {
		return nil, fmt.Errorf("%w: a page hit the %d byte ceiling and may be truncated",
			errSKUStatus, maxSKUPageBytes)
	}

	return body, nil
}

// billingSKUPage is one page of the catalogue.
type billingSKUPage struct {
	NextPageToken string       `json:"nextPageToken"`
	SKUs          []billingSKU `json:"skus"`
}

// billingSKU is the part of a SKU this tool reads. Unknown fields are ignored
// rather than refused: the catalogue prices every Google Cloud resource, and a
// new field on a GPU SKU is not a reason to stop pricing machines.
type billingSKU struct {
	Description    string             `json:"description"`
	Category       billingSKUCategory `json:"category"`
	ServiceRegions []string           `json:"serviceRegions"`
	PricingInfo    []billingPricing   `json:"pricingInfo"`
}

type billingSKUCategory struct {
	ResourceFamily string `json:"resourceFamily"`
	ResourceGroup  string `json:"resourceGroup"`
	UsageType      string `json:"usageType"`
}

type billingPricing struct {
	PricingExpression billingPricingExpression `json:"pricingExpression"`
}

type billingPricingExpression struct {
	UsageUnit   string              `json:"usageUnit"`
	TieredRates []billingTieredRate `json:"tieredRates"`
}

type billingTieredRate struct {
	UnitPrice        billingUnitPrice `json:"unitPrice"`
	StartUsageAmount float64          `json:"startUsageAmount"`
}

type billingUnitPrice struct {
	CurrencyCode string `json:"currencyCode"`
	Units        string `json:"units"`
	Nanos        int64  `json:"nanos"`
}

// decodeSKUStream reads the concatenated pages a sweep cached. The pages are
// stored as they arrived rather than re-encoded, because the concatenation is
// what the published content_sha256 hashes.
func decodeSKUStream(payload []byte) ([]billingSKU, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))

	var items []billingSKU

	for {
		var page billingSKUPage

		err := decoder.Decode(&page)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode billing catalogue document: %w", err)
		}

		items = append(items, page.SKUs...)
	}

	if len(items) == 0 {
		return nil, errNoSKUs
	}

	return items, nil
}
