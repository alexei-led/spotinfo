package spot

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tempCache(t *testing.T) *feedCache {
	t.Helper()
	t.Setenv(cacheDirEnv, t.TempDir())

	cache := openFeedCache()
	require.True(t, cache.enabled())

	return cache
}

func TestCacheRoundTrips(t *testing.T) {
	cache := tempCache(t)
	payload := []byte(`{"hello":"world"}`)

	cache.save("advisor", payload, &cacheEntry{
		FetchedAt: time.Now(), URL: spotAdvisorJSONURL, ETag: `"abc"`,
	})

	stored, entry, ok := cache.load("advisor")
	require.True(t, ok)
	assert.Equal(t, payload, stored)
	assert.Equal(t, `"abc"`, entry.ETag)
	assert.Equal(t, spotAdvisorJSONURL, entry.URL)
}

// A cache that cannot be created must not fail anything: results still come
// from the origin or the committed snapshot.
func TestDisabledCacheIsNotAnError(t *testing.T) {
	t.Setenv(cacheDisableEnv, "off")

	cache := openFeedCache()
	assert.False(t, cache.enabled())

	cache.save("advisor", []byte("{}"), &cacheEntry{})
	_, _, ok := cache.load("advisor")
	assert.False(t, ok, "a disabled cache never reports a hit")
}

// A payload that does not match the digest its metadata records is a partial
// write, not data. Half a JSON document parses into a plausible-looking result
// rather than an obvious failure, so it must read as a miss.
func TestCorruptPayloadReadsAsAMiss(t *testing.T) {
	cache := tempCache(t)
	cache.save("advisor", []byte(`{"a":1}`), &cacheEntry{FetchedAt: time.Now(), URL: spotAdvisorJSONURL})

	payloadPath, _ := cache.paths("advisor")
	require.NoError(t, writeRaw(payloadPath, []byte("truncated")))

	_, _, ok := cache.load("advisor")
	assert.False(t, ok)
}

// Freshness is bounded by the time-to-live, by the URL the entry was fetched
// from, and by the on-disk format version.
func TestEntryFreshness(t *testing.T) {
	now := time.Now()
	entry := cacheEntry{Format: cacheFormat, URL: spotAdvisorJSONURL, FetchedAt: now.Add(-time.Minute)}

	assert.True(t, (&entry).fresh(spotAdvisorJSONURL, time.Hour, now))
	assert.False(t, (&entry).fresh(spotAdvisorJSONURL, time.Second, now), "expired by ttl")
	assert.False(t, (&entry).fresh(spotPriceJSURL, time.Hour, now), "a different url is a different document")

	old := entry
	old.Format = cacheFormat + 1
	assert.False(t, (&old).fresh(spotAdvisorJSONURL, time.Hour, now), "another format version must not be reinterpreted")
}

// touch restarts the window without rewriting the payload, which is what makes a
// 304 cheap.
func TestTouchRestartsTheWindow(t *testing.T) {
	cache := tempCache(t)
	stale := time.Now().Add(-48 * time.Hour)
	cache.save("advisor", []byte(`{"a":1}`), &cacheEntry{FetchedAt: stale, URL: spotAdvisorJSONURL})

	_, entry, ok := cache.load("advisor")
	require.True(t, ok)
	assert.False(t, entry.fresh(spotAdvisorJSONURL, advisorCacheTTL, time.Now()))

	now := time.Now()
	cache.touch("advisor", entry, now)

	payload, refreshed, ok := cache.load("advisor")
	require.True(t, ok)
	assert.True(t, refreshed.fresh(spotAdvisorJSONURL, advisorCacheTTL, now))
	assert.Equal(t, []byte(`{"a":1}`), payload, "the payload must survive a touch untouched")
}

// The advisor window is long and the price window short, because the advisor
// document barely changes and takes a second to transfer, while prices are
// rewritten through the day and transfer in a tenth of that.
func TestPriceIsCachedFarMoreBrieflyThanAdvisor(t *testing.T) {
	assert.Less(t, priceCacheTTL, advisorCacheTTL)
	assert.LessOrEqual(t, priceCacheTTL, time.Hour)
}

func writeRaw(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
