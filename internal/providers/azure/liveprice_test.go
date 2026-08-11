package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/feedcache"
)

// The live price path, against a stub of the Retail Prices API.
//
// Every test here answers a query for one region and checks two things
// together: the candidates, and the mode the result claims. Mode without
// candidates would pass while the answer was empty, and candidates without mode
// would pass while the answer claimed a freshness nothing established.
//
// These tests are serial because they point the shared feed cache at a
// temporary directory with t.Setenv.

// liveRegion under test. Anything in the committed catalogue works; this one is
// the first region and prices 187 machines against a floor of 180.
const stubRegion = cloud.Region("australiacentral")

// spotDiscount is what the stub prices Spot at, against the on-demand amount it
// echoes from the snapshot. It is far below any committed row, so a candidate
// carrying it can only have come from the live sweep.
const spotDiscount = 10

// snapshotPrices returns the committed rows for one region.
func snapshotPrices(t *testing.T, provider *Provider) []CatalogPrice {
	t.Helper()

	for i := range provider.catalog.Regions {
		if provider.catalog.Regions[i].ID == stubRegion {
			return provider.catalog.Regions[i].Prices
		}
	}
	t.Fatalf("region %s is not in the committed catalogue", stubRegion)

	return nil
}

// retailItems renders committed rows back into the API's own shape, so the stub
// answers with a document the production parser reads exactly as it reads
// Azure's. Spot is priced at a tenth of the snapshot's amount, which is what
// makes a live answer distinguishable from a snapshot one.
func retailItems(prices []CatalogPrice) []RetailItem {
	items := make([]RetailItem, 0, len(prices)*pricesPerMachine)
	start := time.Now().UTC().AddDate(0, -1, 0)

	for i := range prices {
		price := &prices[i]

		product := "Virtual Machines " + string(price.Machine) + " Series"
		if price.OS == cloud.OSWindows {
			product += windowsMarker
		}

		for _, meter := range []struct {
			sku    string
			amount cloud.Money
		}{
			{sku: string(price.Machine) + suffixSpot, amount: dividedBy(price.Spot, spotDiscount)},
			{sku: string(price.Machine), amount: price.OnDemand},
		} {
			items = append(items, RetailItem{
				EffectiveStartDate: start,
				RetailPrice:        json.Number(meter.amount.String()),
				CurrencyCode:       string(cloud.CurrencyUSD),
				ArmRegionName:      string(stubRegion),
				ArmSkuName:         string(price.Machine),
				ProductName:        product,
				SkuName:            meter.sku,
				ServiceName:        serviceVirtualMachines,
				UnitOfMeasure:      unitOfMeasureHour,
				Type:               priceTypeConsumption,
			})
		}
	}

	return items
}

func dividedBy(amount cloud.Money, divisor int64) cloud.Money {
	divided, err := cloud.ParseMoney(fmt.Sprintf("%.9f", float64(amount.Nanos()/divisor)/1e9))
	if err != nil {
		panic(err)
	}

	return divided
}

// retailStub serves the paginated shape the real API serves, and counts every
// request so a test can assert that none was made.
type retailStub struct {
	server   *httptest.Server
	items    []RetailItem
	requests atomic.Int32
	pageSize int
	status   int
	body     string
	block    bool
}

func newRetailStub(t *testing.T, items []RetailItem) *retailStub {
	t.Helper()

	stub := &retailStub{items: items, pageSize: len(items), status: http.StatusOK}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)

	return stub
}

func (s *retailStub) serve(writer http.ResponseWriter, request *http.Request) {
	s.requests.Add(1)

	if s.block {
		<-request.Context().Done()

		return
	}

	if s.status != http.StatusOK {
		writer.WriteHeader(s.status)

		return
	}

	if s.body != "" {
		_, _ = writer.Write([]byte(s.body))

		return
	}

	skip, _ := strconv.Atoi(request.URL.Query().Get("$skip"))
	end := min(skip+s.pageSize, len(s.items))

	page := RetailPage{Items: s.items[skip:end], Count: end - skip}
	if end < len(s.items) {
		next := *request.URL
		values := next.Query()
		values.Set("$skip", strconv.Itoa(end))
		next.RawQuery = values.Encode()
		page.NextPageLink = s.server.URL + "?" + next.RawQuery
	}

	_ = json.NewEncoder(writer).Encode(page)
}

// liveProvider wires a provider to the stub, with a fresh cache directory and a
// short budget so a blocked request fails in test time rather than in the
// twenty seconds production allows.
func liveProvider(t *testing.T, stub *retailStub, policy cloud.FetchPolicy) *Provider {
	t.Helper()
	t.Setenv(feedcache.DirEnv, t.TempDir())

	provider, err := New()
	require.NoError(t, err)

	return provider.WithLivePrices(LivePriceConfig{
		Endpoint: stub.server.URL,
		Budget:   2 * time.Second,
		Policy:   policy,
	})
}

// stubRegionQuery asks for the one region every stub here prices.
func stubRegionQuery() *cloud.Query {
	return &cloud.Query{OS: cloud.OSLinux, Regions: []cloud.Region{stubRegion}}
}

// A successful sweep answers from the API and says so. The prices are the
// stub's, not the snapshot's, which is the only way to tell the two apart.
func TestALiveSweepAnswersWithFetchedPricesAndReportsLive(t *testing.T) {
	provider := newTestProvider(t)
	stub := newRetailStub(t, retailItems(snapshotPrices(t, provider)))
	live := liveProvider(t, stub, cloud.FetchPolicy{})

	result, err := live.Query(context.Background(), stubRegionQuery())
	require.NoError(t, err)
	require.NotEmpty(t, result.Candidates)
	assert.Equal(t, cloud.DataModeLive, result.Mode)

	snapshot := priceOf(t, newTestProvider(t))
	fetched := priceOf(t, live)
	assert.Equal(t, snapshot.Nanos()/spotDiscount, fetched.Nanos(),
		"the published price must be the one the sweep fetched")
}

// priceOf reads one machine's spot price out of a region's first candidate, so
// a test can compare what the snapshot publishes against what a sweep does.
func priceOf(t *testing.T, provider *Provider) cloud.Money {
	t.Helper()

	result, err := provider.Query(context.Background(), stubRegionQuery())
	require.NoError(t, err)
	require.NotEmpty(t, result.Candidates)
	require.NotNil(t, result.Candidates[0].Spot)

	return result.Candidates[0].Spot.Amount
}

// Pagination: the sweep follows NextPageLink and the cached document is the
// concatenation of every page, which is what the published content hash covers.
func TestALiveSweepFollowsPagination(t *testing.T) {
	provider := newTestProvider(t)
	items := retailItems(snapshotPrices(t, provider))
	stub := newRetailStub(t, items)
	stub.pageSize = len(items)/3 + 1

	live := liveProvider(t, stub, cloud.FetchPolicy{})

	result, err := live.Query(context.Background(), stubRegionQuery())
	require.NoError(t, err)
	assert.Equal(t, cloud.DataModeLive, result.Mode)
	assert.Equal(t, int32(3), stub.requests.Load(), "three pages, three requests")
	assert.NotEmpty(t, result.Candidates)
}

// Every failure shape falls back to the committed snapshot. None of them is an
// error, and none of them answers empty: a live path that can fail a run, or
// empty one, is worse than no live path.
func TestEveryLiveFailureFallsBackToTheSnapshot(t *testing.T) {
	reference := newTestProvider(t)
	items := retailItems(snapshotPrices(t, reference))

	for _, testCase := range []struct {
		name   string
		break_ func(*retailStub)
	}{
		{name: "server error", break_: func(s *retailStub) { s.status = http.StatusInternalServerError }},
		{name: "timeout", break_: func(s *retailStub) { s.block = true }},
		{name: "malformed body", break_: func(s *retailStub) { s.body = `{"Items": [ {"retailPrice":` }},
		{name: "empty document", break_: func(s *retailStub) { s.body = `{"Items":[],"Count":0}` }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stub := newRetailStub(t, items)
			testCase.break_(stub)

			live := liveProvider(t, stub, cloud.FetchPolicy{})

			result, err := live.Query(context.Background(), stubRegionQuery())
			require.NoError(t, err, "a live failure must never become a query error")
			assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)
			assert.NotEmpty(t, result.Candidates, "the snapshot still answers")
			assert.Positive(t, stub.requests.Load(), "the failure has to be a real attempt")
		})
	}
}

// An empty answer is diagnosed by re-querying without the filters, and a sweep
// that failed in transport cached nothing — so that second query re-reads the
// region that failed. This is the ceiling the guard in
// internal/cloud/diagnose.go accepts and documents: one extra read of the one
// failed region, and only on a run that has already failed.
func TestTheDiagnosisRereadsOnlyTheRegionThatFailed(t *testing.T) {
	reference := newTestProvider(t)
	stub := newRetailStub(t, retailItems(snapshotPrices(t, reference)))
	stub.status = http.StatusInternalServerError

	live := liveProvider(t, stub, cloud.FetchPolicy{})
	failed := stub.requests.Load()

	const impossibleVCPU = 1 << 20

	_, err := cloud.Recommend(t.Context(), live, &cloud.RecommendRequest{
		Cloud:        cloud.ProviderAzure,
		Architecture: cloud.ArchitectureX8664,
		OS:           cloud.OSLinux,
		Workload:     cloud.WorkloadCost,
		Regions:      []cloud.Region{stubRegion},
		MinVCPU:      impossibleVCPU,
		MinMemoryGiB: 1,
		Top:          1,
	})
	require.ErrorIs(t, err, cloud.ErrNoCandidates)
	assert.Contains(t, err.Error(), "no machine has at least",
		"the second query is what turns the plain message into a named cause")
	assert.Equal(t, int32(2), stub.requests.Load()-failed,
		"the failed answer and the diagnosis, and nothing beyond either")
}

// A sweep that comes back thinner than the reviewed per-region floor is refused
// whole. Half a region is not a cheaper answer, it is a wrong one.
func TestAThinSweepIsRefusedRatherThanPublished(t *testing.T) {
	provider := newTestProvider(t)
	prices := snapshotPrices(t, provider)
	stub := newRetailStub(t, retailItems(prices[:20]))

	live := liveProvider(t, stub, cloud.FetchPolicy{})

	result, err := live.Query(context.Background(), stubRegionQuery())
	require.NoError(t, err)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)
	assert.Greater(t, len(result.Candidates), 20, "the snapshot answers, not the thin sweep")
}

// A sweep that prices a full region for Linux alone is refused too. The
// " Windows" suffix that classifies a meter is undocumented, so a sweep that
// stopped producing Windows rows would otherwise clear the machine floor and
// answer --os windows with nothing while the snapshot it replaced has rows.
func TestASweepMissingAnOperatingSystemIsRefused(t *testing.T) {
	provider := newTestProvider(t)

	linuxOnly := make([]CatalogPrice, 0)
	for _, price := range snapshotPrices(t, provider) {
		if price.OS == cloud.OSLinux {
			linuxOnly = append(linuxOnly, price)
		}
	}
	require.Greater(t, len(linuxOnly), provider.minMachines, "the sweep must clear the machine floor")

	stub := newRetailStub(t, retailItems(linuxOnly))
	live := liveProvider(t, stub, cloud.FetchPolicy{})

	result, err := live.Query(context.Background(), stubRegionQuery())
	require.NoError(t, err)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)

	windows, err := live.Query(context.Background(),
		&cloud.Query{OS: cloud.OSWindows, Regions: []cloud.Region{stubRegion}})
	require.NoError(t, err)
	assert.NotEmpty(t, windows.Candidates, "the snapshot's windows rows must survive the refusal")
}

// --offline is the one promise this path must keep exactly: no request at all.
func TestOfflineMakesNoRequest(t *testing.T) {
	provider := newTestProvider(t)
	stub := newRetailStub(t, retailItems(snapshotPrices(t, provider)))

	live := liveProvider(t, stub, cloud.FetchPolicy{Offline: true})

	result, err := live.Query(context.Background(), stubRegionQuery())
	require.NoError(t, err)
	assert.NotEmpty(t, result.Candidates)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)
	assert.Equal(t, int32(0), stub.requests.Load(), "--offline must not reach the api")
}

// Asking for every region is not a live question: 55 sweeps is minutes and
// hundreds of megabytes, which is what the committed snapshot exists for.
func TestEveryRegionAtOnceIsAnsweredFromTheSnapshot(t *testing.T) {
	provider := newTestProvider(t)
	stub := newRetailStub(t, retailItems(snapshotPrices(t, provider)))
	live := liveProvider(t, stub, cloud.FetchPolicy{})

	for _, regions := range [][]cloud.Region{
		{cloud.RegionAll},
		nil,
		{"australiacentral", "australiacentral2", "australiaeast"},
	} {
		result, err := live.Query(context.Background(), &cloud.Query{OS: cloud.OSLinux, Regions: regions})
		require.NoError(t, err)
		assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)
	}

	assert.Equal(t, int32(0), stub.requests.Load())
}

// A second query inside the window is served from the cache, and says cached
// rather than live: this API publishes no ETag and no Last-Modified, so nothing
// confirmed the copy is still current.
func TestASecondQueryIsServedFromTheCacheAndReportsCached(t *testing.T) {
	provider := newTestProvider(t)
	stub := newRetailStub(t, retailItems(snapshotPrices(t, provider)))
	live := liveProvider(t, stub, cloud.FetchPolicy{})

	first, err := live.Query(context.Background(), stubRegionQuery())
	require.NoError(t, err)
	require.Equal(t, cloud.DataModeLive, first.Mode)
	requests := stub.requests.Load()

	second, err := live.Query(context.Background(), stubRegionQuery())
	require.NoError(t, err)
	assert.Equal(t, cloud.DataModeCached, second.Mode)
	assert.Equal(t, requests, stub.requests.Load(), "a cache hit costs no request")
	assert.Equal(t, len(first.Candidates), len(second.Candidates))
}

// --refresh ignores the cached copy and fetches again.
func TestRefreshIgnoresTheCachedCopy(t *testing.T) {
	provider := newTestProvider(t)
	items := retailItems(snapshotPrices(t, provider))
	stub := newRetailStub(t, items)

	cached := liveProvider(t, stub, cloud.FetchPolicy{})
	_, err := cached.Query(context.Background(), stubRegionQuery())
	require.NoError(t, err)
	requests := stub.requests.Load()

	refreshing := cached.WithLivePrices(LivePriceConfig{
		Endpoint: stub.server.URL,
		Budget:   2 * time.Second,
		Policy:   cloud.FetchPolicy{Refresh: true},
	})

	result, err := refreshing.Query(context.Background(), stubRegionQuery())
	require.NoError(t, err)
	assert.Equal(t, cloud.DataModeLive, result.Mode)
	assert.Greater(t, stub.requests.Load(), requests)
}

// A disabled cache costs time, not answers: the sweep still runs and the answer
// is still live, it is just fetched every time.
func TestADisabledCacheStillAnswersLive(t *testing.T) {
	t.Setenv(feedcache.DisableEnv, feedcache.DisableValue)

	provider := newTestProvider(t)
	stub := newRetailStub(t, retailItems(snapshotPrices(t, provider)))
	live := provider.WithLivePrices(LivePriceConfig{Endpoint: stub.server.URL, Budget: 2 * time.Second})

	for range 2 {
		result, err := live.Query(context.Background(), stubRegionQuery())
		require.NoError(t, err)
		assert.Equal(t, cloud.DataModeLive, result.Mode)
	}

	assert.Equal(t, int32(2), stub.requests.Load(), "nothing is cached, so both queries fetch")
}

// A live price is published under the URL the manifest records, but with the
// hash and the fetch time of the document that was actually read. Publishing
// the snapshot's hash beside a price fetched this run would make the
// provenance unverifiable.
func TestALiveRegionRepublishesItsOwnProvenance(t *testing.T) {
	provider := newTestProvider(t)
	stub := newRetailStub(t, retailItems(snapshotPrices(t, provider)))
	live := liveProvider(t, stub, cloud.FetchPolicy{})

	before, err := provider.Query(context.Background(), stubRegionQuery())
	require.NoError(t, err)

	after, err := live.Query(context.Background(), stubRegionQuery())
	require.NoError(t, err)

	committed := sourceForRegion(t, before.Sources, stubRegion)
	fetched := sourceForRegion(t, after.Sources, stubRegion)

	// Same request, and in production the same URL: only the host differs here,
	// because the test points the endpoint at its own stub.
	assert.Equal(t, queryOf(t, committed.URL), queryOf(t, fetched.URL),
		"the sweep reads the request the manifest recorded")
	assert.NotEqual(t, committed.ContentSHA256, fetched.ContentSHA256)
	assert.True(t, fetched.FetchedAt.After(committed.FetchedAt))
	assert.Equal(t, ParserVersion, fetched.ParserVersion)
}

func queryOf(t *testing.T, rawURL string) string {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)

	return parsed.RawQuery
}

func sourceForRegion(t *testing.T, sources []cloud.SourceRef, region cloud.Region) cloud.SourceRef {
	t.Helper()

	for i := range sources {
		if sources[i].Scope.Region == region {
			return sources[i]
		}
	}
	t.Fatalf("no source scoped to %s", region)

	return cloud.SourceRef{}
}

// A NextPageLink pointing off the contracted host is refused. It is
// response-supplied, so following one anywhere would let a single reply move
// the sweep to a source nobody approved — and the bytes would then be published
// under the contracted URL.
func TestAPageLinkOffTheContractedHostIsRefused(t *testing.T) {
	t.Parallel()

	require.NoError(t, sameLiveHost("https://prices.azure.com/api", "https://prices.azure.com/api?$skip=1000"))
	require.Error(t, sameLiveHost("https://prices.azure.com/api", "https://evil.example/api"))
	require.Error(t, sameLiveHost("https://prices.azure.com/api", "http://prices.azure.com/api"))
}

// The live path declares the capability, which is what retires the CLI's
// --offline and --refresh refusals for this cloud.
func TestAzureDeclaresLiveEnrichment(t *testing.T) {
	t.Parallel()

	provider, err := New()
	require.NoError(t, err)
	assert.True(t, provider.Capabilities().Has(cloud.CapabilityLiveEnrichment))
}

// A contract that names no price endpoint disables the live path instead of
// failing the provider: the committed catalogue still answers everything it
// answered before, and the capability stops being declared in the same run.
func TestNoContractedEndpointDisablesLiveEnrichment(t *testing.T) {
	t.Parallel()

	provider := &Provider{catalog: &Catalog{}}
	assert.False(t, provider.Capabilities().Has(cloud.CapabilityLiveEnrichment))
	assert.Nil(t, provider.livePrices(context.Background(), stubRegionQuery()))
}

// A concatenation of pages decodes to every item in them, which is what the
// cache stores and the published hash covers.
func TestDecodeRetailStreamReadsEveryPage(t *testing.T) {
	t.Parallel()

	var payload []byte

	for _, sku := range []string{"Standard_D2s_v5 Spot", "Standard_D4s_v5 Spot"} {
		page, err := json.Marshal(RetailPage{Items: []RetailItem{{SkuName: sku}}, Count: 1})
		require.NoError(t, err)
		payload = append(payload, page...)
	}

	items, err := decodeRetailStream(payload)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "Standard_D4s_v5 Spot", items[1].SkuName)

	_, err = decodeRetailStream([]byte(`{"Items":[]}`))
	require.ErrorIs(t, err, ErrSourceContract)
}

// The request the live path builds is the one the contract approved, filter and
// all — the same function the weekly updater calls.
func TestTheLiveRequestIsTheContractedOne(t *testing.T) {
	t.Parallel()

	target, err := RetailRequestURL("https://prices.azure.com/api/retail/prices", stubRegion)
	require.NoError(t, err)

	parsed, err := url.Parse(target)
	require.NoError(t, err)
	values := parsed.Query()

	assert.Equal(t, RetailAPIVersion, values.Get("api-version"))
	assert.Equal(t, string(cloud.CurrencyUSD), values.Get("currencyCode"))
	assert.Contains(t, values.Get("$filter"), "armRegionName eq '"+string(stubRegion)+"'")
}
