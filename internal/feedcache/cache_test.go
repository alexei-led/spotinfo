package feedcache

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two document identities, standing in for any two feeds. The cache does not
// know which cloud a name belongs to, which is the point of the package.
const (
	firstURL  = "https://example.invalid/first.json"
	secondURL = "https://example.invalid/second.json"
)

func tempCache(t *testing.T) *Cache {
	t.Helper()
	t.Setenv(DirEnv, t.TempDir())

	cache := Open()
	require.True(t, cache.Enabled())

	return cache
}

func TestCacheRoundTrips(t *testing.T) {
	cache := tempCache(t)
	payload := []byte(`{"hello":"world"}`)

	cache.Save("advisor", payload, &Entry{
		FetchedAt: time.Now(), URL: firstURL, ETag: `"abc"`,
	})

	stored, entry, ok := cache.Load("advisor")
	require.True(t, ok)
	assert.Equal(t, payload, stored)
	assert.Equal(t, `"abc"`, entry.ETag)
	assert.Equal(t, firstURL, entry.URL)
}

// A cache that cannot be created must not fail anything: results still come
// from the origin or the committed snapshot.
func TestDisabledCacheIsNotAnError(t *testing.T) {
	t.Setenv(DisableEnv, DisableValue)

	cache := Open()
	assert.False(t, cache.Enabled())

	cache.Save("advisor", []byte("{}"), &Entry{})
	_, _, ok := cache.Load("advisor")
	assert.False(t, ok, "a disabled cache never reports a hit")
}

// A payload that does not match the digest its metadata records is a partial
// write, not data. Half a JSON document parses into a plausible-looking result
// rather than an obvious failure, so it must read as a miss.
func TestCorruptPayloadReadsAsAMiss(t *testing.T) {
	cache := tempCache(t)
	cache.Save("advisor", []byte(`{"a":1}`), &Entry{FetchedAt: time.Now(), URL: firstURL})

	payloadPath, _ := cache.paths("advisor")
	require.NoError(t, writeRaw(payloadPath, []byte("truncated")))

	_, _, ok := cache.Load("advisor")
	assert.False(t, ok)
}

// Freshness is bounded by the time-to-live, by the URL the entry was fetched
// from, and by the on-disk format version.
func TestEntryFreshness(t *testing.T) {
	t.Parallel()

	now := time.Now()
	entry := Entry{Format: Format, URL: firstURL, FetchedAt: now.Add(-time.Minute)}

	assert.True(t, (&entry).Fresh(firstURL, time.Hour, now))
	assert.False(t, (&entry).Fresh(firstURL, time.Second, now), "expired by ttl")
	assert.False(t, (&entry).Fresh(secondURL, time.Hour, now), "a different url is a different document")

	old := entry
	old.Format = Format + 1
	assert.False(t, (&old).Fresh(firstURL, time.Hour, now), "another format version must not be reinterpreted")
}

// Touch restarts the window without rewriting the payload, which is what makes
// a 304 cheap for an origin that publishes a validator.
func TestTouchRestartsTheWindow(t *testing.T) {
	cache := tempCache(t)
	stale := time.Now().Add(-48 * time.Hour)
	cache.Save("advisor", []byte(`{"a":1}`), &Entry{FetchedAt: stale, URL: firstURL})

	_, entry, ok := cache.Load("advisor")
	require.True(t, ok)
	assert.False(t, entry.Fresh(firstURL, 24*time.Hour, time.Now()))

	now := time.Now()
	cache.Touch("advisor", entry, now)

	payload, refreshed, ok := cache.Load("advisor")
	require.True(t, ok)
	assert.True(t, refreshed.Fresh(firstURL, 24*time.Hour, now))
	assert.Equal(t, []byte(`{"a":1}`), payload, "the payload must survive a touch untouched")
}

func writeRaw(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
