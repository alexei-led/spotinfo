package gcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/feedcache"
)

// The live price path, against a stub of the Cloud Billing Catalog API.
//
// Every test here checks the candidates and the mode together. Mode alone would
// pass while the answer was empty; candidates alone would pass while the answer
// claimed a freshness nothing established.
//
// None of these reach Google: the endpoint is an httptest server and the key is
// a literal. They are serial because they point the shared feed cache at a
// temporary directory with t.Setenv.

const (
	// widenedRegion is a region the committed snapshot does not cover. It is the
	// whole point of this path: without a key it answers nothing.
	widenedRegion = cloud.Region("europe-west1")

	// stubKey is what the stub demands. A request without it is answered the way
	// Google answers a bad key.
	stubKey = "test-billing-key"

	// coreRate and ramRate are the stub's per-core and per-GiB on-demand rates,
	// in nano-USD per hour. Round numbers, so a composed price is checkable by
	// hand: 4 vCPU and 16 GiB comes to 4*20m + 16*5m = 160m nano, or $0.16.
	coreRate = 20_000_000
	ramRate  = 5_000_000

	// spotDivisor is how much cheaper the stub prices Spot. A candidate carrying
	// a price divisible this way can only have come from the stub.
	spotDivisor = 4
)

// billingStub serves a SKU document, counting requests and recording how the
// key travelled.
type billingStub struct {
	server   *httptest.Server
	pages    [][]byte
	requests atomic.Int64
	keys     []string
	urls     []string
	status   int
}

// newBillingStub serves the given pages in order, chaining them with page
// tokens the way the catalogue does.
func newBillingStub(t *testing.T, pages [][]byte) *billingStub {
	t.Helper()

	stub := &billingStub{pages: pages, status: http.StatusOK}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)

	return stub
}

func (s *billingStub) serve(writer http.ResponseWriter, request *http.Request) {
	index := int(s.requests.Add(1)) - 1
	s.keys = append(s.keys, request.Header.Get(apiKeyHeader))
	s.urls = append(s.urls, request.URL.String())

	if s.status != http.StatusOK {
		writer.WriteHeader(s.status)
		_, _ = writer.Write([]byte(`{"error":{"code":403,"message":"API key not valid"}}`))

		return
	}

	if request.Header.Get(apiKeyHeader) != stubKey {
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"error":{"code":403,"message":"API key not valid"}}`))

		return
	}

	if index >= len(s.pages) {
		writer.WriteHeader(http.StatusInternalServerError)

		return
	}

	_, _ = writer.Write(s.pages[index])
}

// seriesOfCatalogue lists every machine series the committed catalogue prices.
func seriesOfCatalogue(t *testing.T, provider *Provider) []string {
	t.Helper()

	series := make([]string, 0, len(provider.catalog.Machines))
	for i := range provider.catalog.Machines {
		if name := SeriesOf(provider.catalog.Machines[i].ID); !slices.Contains(series, name) {
			series = append(series, name)
		}
	}

	return series
}

// skuPage renders one catalogue page pricing every named series in every named
// region, in the shape the API publishes.
func skuPage(t *testing.T, series []string, regions []cloud.Region, token string) []byte {
	t.Helper()

	names := make([]string, 0, len(regions))
	for _, region := range regions {
		names = append(names, string(region))
	}

	skus := make([]map[string]any, 0, len(series)*4) //nolint:mnd // four skus per series

	for _, name := range series {
		for _, class := range []struct {
			usageType string
			prefix    string
			divisor   int64
		}{
			{usageType: usageTypeOnDemand, divisor: 1},
			{usageType: usageTypePreemptible, prefix: "Spot Preemptible ", divisor: spotDivisor},
		} {
			skus = append(skus,
				sku(class.prefix+strings.ToUpper(name)+" Instance Core running in Americas",
					class.usageType, coreUsageUnit, coreRate/class.divisor, names),
				sku(class.prefix+strings.ToUpper(name)+" Instance Ram running in Americas",
					class.usageType, ramUsageUnit, ramRate/class.divisor, names))
		}
	}

	page := map[string]any{"skus": skus}
	if token != "" {
		page["nextPageToken"] = token
	}

	encoded, err := json.Marshal(page)
	require.NoError(t, err)

	return encoded
}

// sku renders one SKU. The amount is split into whole units and nanos the way
// the wire format does.
func sku(description, usageType, unit string, nanos int64, regions []string) map[string]any {
	return map[string]any{
		"description": description,
		"category": map[string]any{
			"resourceFamily": resourceFamilyCompute,
			"resourceGroup":  "CPU",
			"usageType":      usageType,
		},
		"serviceRegions": regions,
		"pricingInfo": []map[string]any{{
			"pricingExpression": map[string]any{
				"usageUnit": unit,
				"tieredRates": []map[string]any{{
					"startUsageAmount": 0,
					"unitPrice": map[string]any{
						"currencyCode": usdCurrency,
						"units":        "0",
						"nanos":        nanos,
					},
				}},
			},
		}},
	}
}

// liveProvider wires a provider to the stub, with a fresh cache directory and a
// short budget so a blocked request fails in test time.
func livePriceProvider(t *testing.T, stub *billingStub, key string, policy cloud.FetchPolicy) *Provider {
	t.Helper()
	t.Setenv(feedcache.DirEnv, t.TempDir())

	provider, err := New()
	require.NoError(t, err)

	return provider.WithLivePrices(LivePriceConfig{
		APIKey:   key,
		Endpoint: stub.server.URL,
		Budget:   5 * time.Second, //nolint:mnd // a test budget, not a production one
		Client:   stub.server.Client(),
		Policy:   policy,
	})
}

func regionQuery(regions ...cloud.Region) *cloud.Query {
	return &cloud.Query{OS: cloud.OSLinux, Regions: regions}
}

// candidateFor returns the answer's row for one machine.
func candidateFor(t *testing.T, result cloud.Result, id cloud.MachineID) *cloud.Candidate {
	t.Helper()

	for i := range result.Candidates {
		if result.Candidates[i].Machine.ID == id {
			return &result.Candidates[i]
		}
	}
	t.Fatalf("%s is not in the answer", id)

	return nil
}

// A key widens the answer past the committed region, and the prices are the
// ones the catalogue API published rather than the snapshot's.
func TestAKeyPricesARegionTheCommittedSnapshotDoesNotCover(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	stub := newBillingStub(t, [][]byte{
		skuPage(t, seriesOfCatalogue(t, provider), []cloud.Region{widenedRegion}, ""),
	})
	live := livePriceProvider(t, stub, stubKey, cloud.FetchPolicy{})

	result, err := live.Query(context.Background(), regionQuery(widenedRegion))
	require.NoError(t, err)
	require.NotEmpty(t, result.Candidates)
	assert.Equal(t, cloud.DataModeLive, result.Mode)

	for i := range result.Candidates {
		require.Equal(t, widenedRegion, result.Candidates[i].Location.Region,
			"a live answer may only carry the regions it was asked for")
	}

	// n2-standard-4 is 4 vCPU and 16 GiB, so the composed on-demand price is
	// 4 cores plus 16 GiB at the stub's rates, and Spot is a quarter of each.
	candidate := candidateFor(t, result, "n2-standard-4")
	assert.Equal(t, int64(4*coreRate+16*ramRate), candidate.OnDemand.Amount.Nanos())
	assert.Equal(t, int64(4*coreRate/spotDivisor+16*ramRate/spotDivisor), candidate.Spot.Amount.Nanos())
	require.NotNil(t, candidate.SavingsPercent)
	assert.Equal(t, 75, *candidate.SavingsPercent, "the savings must be derived from the published pair")
}

// The provenance names the catalogue API beside the committed pages, because
// the amounts came from one and the specification from the other.
func TestALiveAnswerPublishesTheCatalogueApiBesideTheCommittedPages(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	snapshotSources := len(provider.sources)

	stub := newBillingStub(t, [][]byte{
		skuPage(t, seriesOfCatalogue(t, provider), []cloud.Region{widenedRegion}, ""),
	})
	live := livePriceProvider(t, stub, stubKey, cloud.FetchPolicy{})

	query := regionQuery(widenedRegion)

	result, err := live.Query(context.Background(), query)
	require.NoError(t, err)
	require.Len(t, result.Sources, snapshotSources+1,
		"the scraped pages are the provenance of every vCPU and memory figure and may not be dropped")

	added := result.Sources[len(result.Sources)-1]
	assert.Equal(t, stub.server.URL, added.URL)
	assert.Equal(t, LivePriceParserVersion, added.ParserVersion)
	assert.Len(t, added.ContentSHA256, 64, "a live answer must publish a verifiable content hash")
	assert.False(t, added.FetchedAt.IsZero())

	// The published document, not only the internal result: a source missing a
	// parser or schema version fails report construction rather than shipping
	// invented provenance, and this is the one path that adds a source no
	// manifest declared.
	report, err := cloud.NewListReport(query, &result)
	require.NoError(t, err)
	assert.Len(t, report.DataSource.Sources, snapshotSources+1)
	assert.Zero(t, report.DataSource.SourcesOmitted)
	assert.Equal(t, cloud.DataModeLive, report.DataSource.Mode)
}

// The key travels in a header and nowhere else: not in a request URL, not in
// the published provenance, not in the cache entry's recorded URL.
func TestTheApiKeyNeverReachesAUrlOrThePublishedProvenance(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	stub := newBillingStub(t, [][]byte{
		skuPage(t, seriesOfCatalogue(t, provider), []cloud.Region{widenedRegion}, ""),
	})
	live := livePriceProvider(t, stub, stubKey, cloud.FetchPolicy{})

	result, err := live.Query(context.Background(), regionQuery(widenedRegion))
	require.NoError(t, err)
	require.NotEmpty(t, stub.urls)

	assert.Equal(t, []string{stubKey}, stub.keys, "the key must travel in the documented header")

	for _, target := range stub.urls {
		assert.NotContains(t, target, stubKey, "a key in a url reaches logs, caches and provenance")
	}

	for i := range result.Sources {
		assert.NotContains(t, result.Sources[i].URL, stubKey)
	}
}

// Without a key nothing is fetched and nothing widens: the committed region
// answers from the snapshot and any other region answers with no candidates,
// which is exactly what the same query answered before this path existed.
func TestWithoutAKeyTheAnswerIsTheCommittedSnapshot(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	stub := newBillingStub(t, [][]byte{
		skuPage(t, seriesOfCatalogue(t, provider), []cloud.Region{widenedRegion}, ""),
	})
	live := livePriceProvider(t, stub, "", cloud.FetchPolicy{})

	widened, err := live.Query(context.Background(), regionQuery(widenedRegion))
	require.NoError(t, err)
	assert.Empty(t, widened.Candidates, "no key, no region beyond the snapshot")
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, widened.Mode)

	committed, err := live.Query(context.Background(), regionQuery(provider.catalog.Region))
	require.NoError(t, err)
	require.NotEmpty(t, committed.Candidates)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, committed.Mode)
	assert.Equal(t, provider.catalog.Machines[0].Spot.Nanos(),
		candidateFor(t, committed, provider.catalog.Machines[0].ID).Spot.Amount.Nanos())

	assert.Zero(t, stub.requests.Load(), "with no key there is nothing to authenticate, so nothing is asked")
}

// A key the API rejects is a fallback, never a failed run.
func TestARejectedKeyFallsBackToTheCommittedSnapshot(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	stub := newBillingStub(t, nil)
	live := livePriceProvider(t, stub, "not-a-valid-key", cloud.FetchPolicy{})

	committed, err := live.Query(context.Background(), regionQuery(provider.catalog.Region))
	require.NoError(t, err, "a refused lookup may not fail the run")
	require.NotEmpty(t, committed.Candidates)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, committed.Mode)
	assert.Equal(t, provider.catalog.Machines[0].Spot.Nanos(),
		candidateFor(t, committed, provider.catalog.Machines[0].ID).Spot.Amount.Nanos())

	widened, err := live.Query(context.Background(), regionQuery(widenedRegion))
	require.NoError(t, err)
	assert.Empty(t, widened.Candidates, "a rejected key leaves the snapshot's coverage, which is one region")
	assert.Positive(t, stub.requests.Load(), "the key was supplied, so the lookup was attempted")
}

// An empty answer is diagnosed by re-querying the provider without the filters,
// and what that second query costs turns on *where* the live path gave up. The
// document is cached before it is read, so a failure after the read is free and
// only a failure in transport sweeps again. That is the ceiling the guard in
// internal/cloud/diagnose.go accepts, and it is measured here rather than
// reasoned about: the claim it replaces was a cost claim nothing verified.
func TestTheDiagnosisCostsOneExtraSweepAtMost(t *testing.T) {
	reference, err := New()
	require.NoError(t, err)

	for _, testCase := range []struct {
		name     string
		key      string
		pages    [][]byte
		requests int64
	}{
		{
			name: "a rejected key cached nothing, so the diagnosis sweeps again",
			key:  "not-a-valid-key", requests: 2,
		},
		{
			name:  "an unpriceable region was cached, so the diagnosis re-reads it for free",
			key:   stubKey,
			pages: [][]byte{skuPage(t, seriesOfCatalogue(t, reference), []cloud.Region{reference.catalog.Region}, "")},
			// The sweep succeeded and only composing europe-west1 failed.
			requests: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stub := newBillingStub(t, testCase.pages)
			live := livePriceProvider(t, stub, testCase.key, cloud.FetchPolicy{})

			_, err := cloud.Recommend(t.Context(), live, &cloud.RecommendRequest{
				Cloud:        cloud.ProviderGCP,
				Architecture: cloud.ArchitectureX8664,
				OS:           cloud.OSLinux,
				Workload:     cloud.WorkloadCost,
				Regions:      []cloud.Region{widenedRegion},
				MinVCPU:      1,
				MinMemoryGiB: 1,
				Top:          1,
			})
			require.ErrorIs(t, err, cloud.ErrNoCandidates)
			assert.Contains(t, err.Error(), "publishes no machines in "+string(widenedRegion),
				"the second query is what turns the plain message into a named cause")
			assert.Equal(t, testCase.requests, stub.requests.Load(),
				"the failed answer and the diagnosis, and nothing beyond either")
		})
	}
}

// A region the catalogue prices nothing for is answered from the snapshot,
// which for that region means no candidates rather than another region's rows.
func TestARegionTheCatalogueDoesNotPriceFallsBackToTheSnapshot(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	stub := newBillingStub(t, [][]byte{
		skuPage(t, seriesOfCatalogue(t, provider), []cloud.Region{provider.catalog.Region}, ""),
	})
	live := livePriceProvider(t, stub, stubKey, cloud.FetchPolicy{})

	result, err := live.Query(context.Background(), regionQuery(widenedRegion))
	require.NoError(t, err)
	assert.Empty(t, result.Candidates)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)
}

// One region short means the answer would mix a composed price with a scraped
// one, so the whole overlay goes rather than half of it.
func TestAnOverlayCoversEveryQueriedRegionOrNone(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	stub := newBillingStub(t, [][]byte{
		skuPage(t, seriesOfCatalogue(t, provider), []cloud.Region{provider.catalog.Region}, ""),
	})
	live := livePriceProvider(t, stub, stubKey, cloud.FetchPolicy{})

	result, err := live.Query(context.Background(), regionQuery(provider.catalog.Region, widenedRegion))
	require.NoError(t, err)
	require.NotEmpty(t, result.Candidates)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)
	assert.Equal(t, provider.catalog.Machines[0].Spot.Nanos(),
		candidateFor(t, result, provider.catalog.Machines[0].ID).Spot.Amount.Nanos(),
		"the committed region keeps its scraped prices when the other region cannot be priced")
}

// Every region at once is not a live question: it is what the committed
// snapshot exists to answer, and it is the default query.
func TestEveryRegionAtOnceIsAnsweredFromTheSnapshot(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	stub := newBillingStub(t, [][]byte{
		skuPage(t, seriesOfCatalogue(t, provider), []cloud.Region{provider.catalog.Region}, ""),
	})
	live := livePriceProvider(t, stub, stubKey, cloud.FetchPolicy{})

	result, err := live.Query(context.Background(), regionQuery(cloud.RegionAll))
	require.NoError(t, err)
	require.NotEmpty(t, result.Candidates)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)
	assert.Zero(t, stub.requests.Load())
}

// --offline is the promise that no request is made at all, and a key does not
// override it.
func TestOfflineMakesNoRequestEvenWithAKey(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	stub := newBillingStub(t, [][]byte{
		skuPage(t, seriesOfCatalogue(t, provider), []cloud.Region{widenedRegion}, ""),
	})
	live := livePriceProvider(t, stub, stubKey, cloud.FetchPolicy{Offline: true})

	result, err := live.Query(context.Background(), regionQuery(widenedRegion))
	require.NoError(t, err)
	assert.Empty(t, result.Candidates)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)
	assert.Zero(t, stub.requests.Load())
}

// The committed region keeps the reviewed coverage floor: a live document that
// prices two series would replace 333 scraped machines with a handful, which is
// a regression against data a reviewer accepted. A region the contract does not
// cover has no such baseline and is served with what the API prices.
func TestTheCommittedRegionKeepsItsReviewedFloorAndAWidenedRegionDoesNot(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	// The first two series the catalogue carries, so the thin document is data
	// rather than a hand-picked pair.
	thin := seriesOfCatalogue(t, provider)[:2]
	stub := newBillingStub(t, [][]byte{
		skuPage(t, thin, []cloud.Region{provider.catalog.Region, widenedRegion}, ""),
	})
	live := livePriceProvider(t, stub, stubKey, cloud.FetchPolicy{})

	// The widened region is asked first, so this is the run that fetches: the
	// same thin document is enough there, because no reviewed count exists to
	// thin against.
	widened, err := live.Query(context.Background(), regionQuery(widenedRegion))
	require.NoError(t, err)
	assert.Equal(t, cloud.DataModeLive, widened.Mode)
	assert.NotEmpty(t, widened.Candidates)
	assert.Less(t, len(widened.Candidates), len(provider.catalog.Machines),
		"a widened region carries exactly the machines the catalogue priced")

	committed, err := live.Query(context.Background(), regionQuery(provider.catalog.Region))
	require.NoError(t, err)
	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, committed.Mode,
		"a live document thinner than the reviewed floor may not replace the snapshot")
	assert.Len(t, committed.Candidates, len(provider.catalog.Machines))
}

// A thin overlay is the one failure of this path nothing downstream can catch:
// it answers, it clears the floor, and it looks like a real page. The counts
// therefore reach stderr, where the first run against a real key will show them.
func TestAThinlyPricedRegionWarnsWithBothCounts(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	var logged bytes.Buffer

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	stub := newBillingStub(t, [][]byte{
		skuPage(t, seriesOfCatalogue(t, provider)[:1], []cloud.Region{widenedRegion}, ""),
	})
	live := livePriceProvider(t, stub, stubKey, cloud.FetchPolicy{})

	result, err := live.Query(context.Background(), regionQuery(widenedRegion))
	require.NoError(t, err)
	require.Equal(t, cloud.DataModeLive, result.Mode)

	assert.Contains(t, logged.String(), "fraction of the catalogue")
	assert.Contains(t, logged.String(), string(widenedRegion))
	assert.Contains(t, logged.String(), "catalogue="+strconv.Itoa(len(provider.catalog.Machines)))
}

// The snapshot is data this binary ships, and a live call may never rewrite it.
// Google states no redistribution terms for the catalogue API, so a fetched
// price that reached the committed bytes would be republished by the next
// build.
func TestALiveCallLeavesTheCommittedSnapshotUnchanged(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	before := sha256.Sum256(embeddedCatalog)
	prices := make(map[cloud.MachineID]int64, len(provider.catalog.Machines))

	for i := range provider.catalog.Machines {
		prices[provider.catalog.Machines[i].ID] = provider.catalog.Machines[i].Spot.Nanos()
	}

	stub := newBillingStub(t, [][]byte{
		skuPage(t, seriesOfCatalogue(t, provider), []cloud.Region{provider.catalog.Region, widenedRegion}, ""),
	})
	live := livePriceProvider(t, stub, stubKey, cloud.FetchPolicy{})

	result, err := live.Query(context.Background(), regionQuery(provider.catalog.Region))
	require.NoError(t, err)
	require.Equal(t, cloud.DataModeLive, result.Mode, "this test is only meaningful once a live answer was served")

	assert.Equal(t, before, sha256.Sum256(embeddedCatalog), "the embedded snapshot must be byte-identical")

	// The stronger half: a second provider, and the one that served the live
	// answer, must both still hold the committed prices. The catalogue is behind
	// a pointer, so a live path that wrote through it would leak into every
	// later query in the process — and on the MCP surface, into every later tool
	// call.
	after, err := New()
	require.NoError(t, err)

	for _, holder := range []*Provider{live, after} {
		for i := range holder.catalog.Machines {
			machine := &holder.catalog.Machines[i]
			assert.Equal(t, prices[machine.ID], machine.Spot.Nanos(),
				"%s kept a fetched price in the committed catalogue", machine.ID)
		}
	}

	snapshot, err := after.Query(context.Background(), regionQuery(after.catalog.Region))
	require.NoError(t, err)
	assert.Equal(t, prices[after.catalog.Machines[0].ID],
		candidateFor(t, snapshot, after.catalog.Machines[0].ID).Spot.Amount.Nanos())
}

// Pagination is followed to the end, and every page carries the key.
func TestEveryCataloguePageIsRead(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	series := seriesOfCatalogue(t, provider)
	stub := newBillingStub(t, [][]byte{
		skuPage(t, series[:1], []cloud.Region{widenedRegion}, "second-page"),
		skuPage(t, series[1:], []cloud.Region{widenedRegion}, ""),
	})
	live := livePriceProvider(t, stub, stubKey, cloud.FetchPolicy{})

	result, err := live.Query(context.Background(), regionQuery(widenedRegion))
	require.NoError(t, err)
	assert.Equal(t, cloud.DataModeLive, result.Mode)
	assert.Equal(t, int64(2), stub.requests.Load())
	assert.Equal(t, []string{stubKey, stubKey}, stub.keys)
	assert.Contains(t, stub.urls[1], "pageToken=second-page")

	// Both pages contributed, so a machine from each series is priced.
	candidateFor(t, result, machineOfSeries(t, provider, series[0]))
	candidateFor(t, result, machineOfSeries(t, provider, series[len(series)-1]))
}

func machineOfSeries(t *testing.T, provider *Provider, series string) cloud.MachineID {
	t.Helper()

	for i := range provider.catalog.Machines {
		if SeriesOf(provider.catalog.Machines[i].ID) == series {
			return provider.catalog.Machines[i].ID
		}
	}
	t.Fatalf("no machine in series %s", series)

	return ""
}

// A cached document is answered from, and reported as cached rather than live:
// this API publishes no validator, so nothing confirmed the copy is current.
func TestACachedDocumentIsReportedAsCachedAndRefreshIgnoresIt(t *testing.T) {
	provider, err := New()
	require.NoError(t, err)

	pages := [][]byte{
		skuPage(t, seriesOfCatalogue(t, provider), []cloud.Region{widenedRegion}, ""),
		skuPage(t, seriesOfCatalogue(t, provider), []cloud.Region{widenedRegion}, ""),
	}
	stub := newBillingStub(t, pages)

	t.Setenv(feedcache.DirEnv, t.TempDir())

	base, err := New()
	require.NoError(t, err)

	config := LivePriceConfig{
		APIKey:   stubKey,
		Endpoint: stub.server.URL,
		Client:   stub.server.Client(),
	}

	first, err := base.WithLivePrices(config).Query(context.Background(), regionQuery(widenedRegion))
	require.NoError(t, err)
	require.Equal(t, cloud.DataModeLive, first.Mode)

	second, err := base.WithLivePrices(config).Query(context.Background(), regionQuery(widenedRegion))
	require.NoError(t, err)
	assert.Equal(t, cloud.DataModeCached, second.Mode)
	assert.Equal(t, int64(1), stub.requests.Load(), "a fresh cache entry answers without asking again")

	refreshing := config
	refreshing.Policy = cloud.FetchPolicy{Refresh: true}

	third, err := base.WithLivePrices(refreshing).Query(context.Background(), regionQuery(widenedRegion))
	require.NoError(t, err)
	assert.Equal(t, cloud.DataModeLive, third.Mode)
	assert.Equal(t, int64(2), stub.requests.Load(), "--refresh must ignore the cached copy")
}

// A machine price is the vCPU count times the per-core rate plus the memory
// times the per-GiB rate, in exact nano units.
func TestAMachinePriceIsComposedFromItsSeriesRates(t *testing.T) {
	t.Parallel()

	rates := ratesFor(t, "n1-standard-1", seriesRates{
		coreSpot: 20_000_000, ramSpot: 4_000_000,
		coreOnDemand: 80_000_000, ramOnDemand: 16_000_000,
	})

	for name, test := range map[string]struct {
		machine  CatalogMachine
		spot     int64
		onDemand int64
		composed bool
	}{
		"whole memory": {
			machine:  CatalogMachine{ID: "n1-standard-4", VCPU: 4, MemoryGiB: 15},
			spot:     4*20_000_000 + 15*4_000_000,
			onDemand: 4*80_000_000 + 15*16_000_000,
			composed: true,
		},
		"fractional memory rounds to the nearest nano": {
			machine:  CatalogMachine{ID: "n1-standard-1", VCPU: 1, MemoryGiB: 3.75},
			spot:     20_000_000 + 15_000_000,
			onDemand: 80_000_000 + 60_000_000,
			composed: true,
		},
		"a series the catalogue does not price is dropped": {
			machine: CatalogMachine{ID: "t2a-standard-4", VCPU: 4, MemoryGiB: 16},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			price, composed := composeMachine(&test.machine, rates)
			require.Equal(t, test.composed, composed)

			if !composed {
				return
			}

			assert.Equal(t, test.spot, price.spot.Nanos())
			assert.Equal(t, test.onDemand, price.onDemand.Nanos())
		})
	}
}

// A machine with only half its rates is dropped rather than half-priced.
func TestAMachineMissingOneRateIsDropped(t *testing.T) {
	t.Parallel()

	machine := CatalogMachine{ID: "n2-standard-4", VCPU: 4, MemoryGiB: 16}

	rates := ratesFor(t, machine.ID, seriesRates{
		coreSpot: 1_000_000, ramSpot: 1_000_000, coreOnDemand: 4_000_000,
	})
	delete(rates, componentKey{
		series: SeriesOf(machine.ID), class: cloud.PriceClassOnDemand, component: componentRAM,
	})

	_, composed := composeMachine(&machine, rates)
	assert.False(t, composed, "an on-demand price with no memory rate is not a price")
}

// A Spot price at or above list price means the rates were joined wrongly.
func TestAComposedSpotPriceAtOrAboveListPriceIsDropped(t *testing.T) {
	t.Parallel()

	machine := CatalogMachine{ID: "n2-standard-4", VCPU: 4, MemoryGiB: 16}

	rates := ratesFor(t, machine.ID, seriesRates{
		coreSpot: 4_000_000, ramSpot: 4_000_000,
		coreOnDemand: 4_000_000, ramOnDemand: 4_000_000,
	})

	_, composed := composeMachine(&machine, rates)
	assert.False(t, composed)
}

// The description grammar is exact: a SKU it does not recognise is dropped
// rather than attributed to whichever series its name resembles.
func TestOnlyADescriptionTheGrammarRecognisesIsRead(t *testing.T) {
	t.Parallel()

	provider, err := New()
	require.NoError(t, err)

	for name, test := range map[string]struct {
		description string
		series      string
		component   string
		read        bool
	}{
		"a predefined core sku": {
			description: "N1 Predefined Instance Core running in Americas",
			series:      SeriesOf("n1-standard-1"), component: componentCore, read: true,
		},
		"a spot preemptible memory sku": {
			description: "Spot Preemptible N2D Instance Ram running in Americas",
			series:      SeriesOf("n2d-standard-4"), component: componentRAM, read: true,
		},
		"a custom machine type": {description: "N2 Custom Instance Core running in Americas"},
		"sole tenancy":          {description: "Sole Tenancy Instance Core running in Americas"},
		"extended memory":       {description: "N2 Custom Extended Instance Ram running in Americas"},
		"a memory-optimized series with no series token": {
			description: "Memory-optimized Instance Core running in Americas",
		},
		"a series outside the contract": {description: "Z9 Instance Core running in Americas"},
		"a gpu":                         {description: "Nvidia Tesla T4 GPU attached to Spot Preemptible VMs running in Americas"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			unit := coreUsageUnit
			if test.component == componentRAM {
				unit = ramUsageUnit
			}

			usage := usageTypeOnDemand
			if strings.Contains(test.description, "Preemptible") {
				usage = usageTypePreemptible
			}

			item := decodeSKU(t, sku(test.description, usage, unit, coreRate, []string{string(widenedRegion)}))

			key, _, read := provider.readSKU(&item, widenedRegion)
			require.Equal(t, test.read, read, "description %q", test.description)

			if read {
				assert.Equal(t, test.series, key.series)
				assert.Equal(t, test.component, key.component)
			}
		})
	}
}

// A rate published in a unit this parser does not know is refused, not
// converted. A per-GB memory rate read as per-GiB is a silent 7% error.
func TestARateInAnUnexpectedUnitIsRefused(t *testing.T) {
	t.Parallel()

	provider, err := New()
	require.NoError(t, err)

	item := decodeSKU(t, sku("N2 Instance Ram running in Americas",
		usageTypeOnDemand, "GBy.h", ramRate, []string{string(widenedRegion)}))

	_, _, read := provider.readSKU(&item, widenedRegion)
	assert.False(t, read, "a gigabyte rate is not a gibibyte rate")
}

// A currency this catalogue does not publish in is refused rather than
// published as dollars.
func TestARateInAnotherCurrencyIsRefused(t *testing.T) {
	t.Parallel()

	provider, err := New()
	require.NoError(t, err)

	raw := sku("N2 Instance Core running in Americas",
		usageTypeOnDemand, coreUsageUnit, coreRate, []string{string(widenedRegion)})
	pricing, _ := raw["pricingInfo"].([]map[string]any)
	expression, _ := pricing[0]["pricingExpression"].(map[string]any)
	tiers, _ := expression["tieredRates"].([]map[string]any)
	price, _ := tiers[0]["unitPrice"].(map[string]any)
	price["currencyCode"] = "EUR"

	item := decodeSKU(t, raw)

	_, _, read := provider.readSKU(&item, widenedRegion)
	assert.False(t, read)
}

// Two SKUs that disagree about the same rate are ambiguity with no safe
// resolution, so both are dropped — the rule the scraped pages already follow.
func TestTwoSkusThatDisagreeAboutARateDropIt(t *testing.T) {
	t.Parallel()

	provider, err := New()
	require.NoError(t, err)

	regions := []string{string(widenedRegion)}
	items := []billingSKU{
		decodeSKU(t, sku("N2 Instance Core running in Americas", usageTypeOnDemand, coreUsageUnit, coreRate, regions)),
		decodeSKU(t, sku("N2 Instance Core running in Americas", usageTypeOnDemand, coreUsageUnit, coreRate*2, regions)),
		decodeSKU(t, sku("N2 Instance Ram running in Americas", usageTypeOnDemand, ramUsageUnit, ramRate, regions)),
		decodeSKU(t, sku("N2 Instance Ram running in Americas", usageTypeOnDemand, ramUsageUnit, ramRate, regions)),
	}

	rates := provider.componentRates(items, widenedRegion)

	series := SeriesOf("n2-standard-4")

	_, core := rates[componentKey{series: series, class: cloud.PriceClassOnDemand, component: componentCore}]
	assert.False(t, core, "two rates for one component is not a rate")

	_, ram := rates[componentKey{series: series, class: cloud.PriceClassOnDemand, component: componentRAM}]
	assert.True(t, ram, "an exact repeat is redundancy, not ambiguity")
}

// A SKU that prices another region is not this region's rate.
func TestASkuForAnotherRegionIsNotRead(t *testing.T) {
	t.Parallel()

	provider, err := New()
	require.NoError(t, err)

	item := decodeSKU(t, sku("N2 Instance Core running in Americas",
		usageTypeOnDemand, coreUsageUnit, coreRate, []string{"asia-east1"}))

	_, _, read := provider.readSKU(&item, widenedRegion)
	assert.False(t, read)
}

// A usage type this tool does not publish — committed use, free tier — prices
// something a Spot answer must not carry.
func TestOnlyOnDemandAndPreemptibleUsageTypesAreRead(t *testing.T) {
	t.Parallel()

	provider, err := New()
	require.NoError(t, err)

	item := decodeSKU(t, sku("N2 Instance Core running in Americas",
		"Commit1Yr", coreUsageUnit, coreRate, []string{string(widenedRegion)}))

	_, _, read := provider.readSKU(&item, widenedRegion)
	assert.False(t, read)
}

// decodeSKU turns a rendered SKU into the shape the parser reads, so a test
// exercises the production decoding rather than a hand-built struct.
func decodeSKU(t *testing.T, raw map[string]any) billingSKU {
	t.Helper()

	encoded, err := json.Marshal(raw)
	require.NoError(t, err)

	var item billingSKU
	require.NoError(t, json.Unmarshal(encoded, &item))

	return item
}

// seriesRates is one series' four component rates, in nano-USD.
type seriesRates struct {
	coreSpot     int64
	ramSpot      int64
	coreOnDemand int64
	ramOnDemand  int64
}

// ratesFor indexes those rates under the series a machine belongs to, so a test
// names a machine rather than repeating a series token the classifier already
// owns.
func ratesFor(t *testing.T, id cloud.MachineID, rates seriesRates) map[componentKey]cloud.Money {
	t.Helper()

	series := SeriesOf(id)

	return map[componentKey]cloud.Money{
		{series: series, class: cloud.PriceClassSpot, component: componentCore}:     nanoMoney(t, rates.coreSpot),
		{series: series, class: cloud.PriceClassSpot, component: componentRAM}:      nanoMoney(t, rates.ramSpot),
		{series: series, class: cloud.PriceClassOnDemand, component: componentCore}: nanoMoney(t, rates.coreOnDemand),
		{series: series, class: cloud.PriceClassOnDemand, component: componentRAM}:  nanoMoney(t, rates.ramOnDemand),
	}
}

func nanoMoney(t *testing.T, nanos int64) cloud.Money {
	t.Helper()

	amount, err := cloud.MoneyFromNanos(nanos)
	require.NoError(t, err)

	return amount
}
