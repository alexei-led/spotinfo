package spot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/feedcache"
)

// advisorBody is the smallest document parseAdvisorResponse accepts.
const advisorBody = `{"spot_advisor":{"us-east-1":{"Linux":{}}},"instance_types":{},"ranges":[]}`

// feedServer counts requests and answers conditionally, like both real feeds do.
type feedServer struct {
	requests    atomic.Int32
	conditional atomic.Int32
}

func (s *feedServer) start(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)

		if r.Header.Get("If-None-Match") == `"v1"` {
			s.conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)

			return
		}

		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(advisorBody))
	}))
	t.Cleanup(server.Close)

	return server
}

func advisorFeed(url string, ttl time.Duration) feed[advisorData] {
	return feed[advisorData]{
		name:     "advisor",
		url:      url,
		embedded: loadEmbeddedAdvisorData,
		parse:    parseAdvisorResponse,
		validate: func(*advisorData) error { return nil },
		ttl:      ttl,
	}
}

// A cached document inside its window is served without touching the origin.
// That is the whole point: the advisor feed takes over a second to transfer.
func TestFreshCacheSkipsTheOriginEntirely(t *testing.T) {
	t.Setenv(feedcache.DirEnv, t.TempDir())

	server := &feedServer{}
	url := server.start(t).URL

	_, origin, err := fetchFeed(context.Background(), fetchOptions{}, advisorFeed(url, time.Hour))
	require.NoError(t, err)
	assert.Equal(t, originLive, origin)
	assert.Equal(t, int32(1), server.requests.Load())

	_, origin, err = fetchFeed(context.Background(), fetchOptions{}, advisorFeed(url, time.Hour))
	require.NoError(t, err)
	assert.Equal(t, originCached, origin, "a fresh entry must be served without asking")
	assert.Equal(t, int32(1), server.requests.Load(), "no second request")
}

// An expired entry is revalidated, not re-downloaded. A 304 costs one round trip
// and no payload, and restarts the window.
func TestExpiredCacheRevalidatesAndIsReportedLive(t *testing.T) {
	t.Setenv(feedcache.DirEnv, t.TempDir())

	server := &feedServer{}
	url := server.start(t).URL

	_, _, err := fetchFeed(context.Background(), fetchOptions{}, advisorFeed(url, time.Hour))
	require.NoError(t, err)

	// A zero window expires the entry immediately.
	_, origin, err := fetchFeed(context.Background(), fetchOptions{}, advisorFeed(url, 0))
	require.NoError(t, err)
	assert.Equal(t, originLive, origin, "a confirmed copy matches the origin, so it is live")
	assert.Equal(t, int32(1), server.conditional.Load(), "revalidation must be conditional")

	// The window restarted, so the next call needs nothing.
	before := server.requests.Load()
	_, origin, err = fetchFeed(context.Background(), fetchOptions{}, advisorFeed(url, time.Hour))
	require.NoError(t, err)
	assert.Equal(t, originCached, origin)
	assert.Equal(t, before, server.requests.Load())
}

// --refresh ignores a fresh entry and fetches unconditionally.
func TestRefreshIgnoresAFreshEntry(t *testing.T) {
	t.Setenv(feedcache.DirEnv, t.TempDir())

	server := &feedServer{}
	url := server.start(t).URL

	_, _, err := fetchFeed(context.Background(), fetchOptions{}, advisorFeed(url, time.Hour))
	require.NoError(t, err)

	_, origin, err := fetchFeed(context.Background(), fetchOptions{refresh: true}, advisorFeed(url, time.Hour))
	require.NoError(t, err)
	assert.Equal(t, originLive, origin)
	assert.Equal(t, int32(2), server.requests.Load(), "refresh must reach the origin")
	assert.Equal(t, int32(0), server.conditional.Load(), "refresh must not send validators")
}

// When the origin is unreachable, an expired cached copy still beats the
// snapshot compiled into the binary: it is AWS data that is merely old, rather
// than AWS data that is old and frozen at build time.
func TestUnreachableOriginPrefersAnExpiredEntryOverTheSnapshot(t *testing.T) {
	t.Setenv(feedcache.DirEnv, t.TempDir())

	server := &feedServer{}
	live := server.start(t)

	_, _, err := fetchFeed(context.Background(), fetchOptions{}, advisorFeed(live.URL, time.Hour))
	require.NoError(t, err)

	live.Close()

	data, origin, err := fetchFeed(context.Background(), fetchOptions{}, advisorFeed(live.URL, 0))
	require.NoError(t, err)
	assert.Equal(t, originCached, origin)
	require.NotNil(t, data)
	assert.False(t, data.Embedded, "the cached copy answered, not the committed snapshot")
}

// With nothing cached and no origin, the committed snapshot answers.
func TestNoCacheAndNoOriginFallsBackToTheSnapshot(t *testing.T) {
	t.Setenv(feedcache.DirEnv, t.TempDir())

	data, origin, err := fetchFeed(context.Background(), fetchOptions{},
		advisorFeed("http://127.0.0.1:1/nothing", time.Hour))
	require.NoError(t, err)
	assert.Equal(t, originEmbedded, origin)
	require.NotNil(t, data)
	assert.True(t, data.Embedded)
}

// --offline makes no request at all, even with an empty cache.
func TestOfflineNeverReachesTheOrigin(t *testing.T) {
	t.Setenv(feedcache.DirEnv, t.TempDir())

	server := &feedServer{}
	url := server.start(t).URL

	data, origin, err := fetchFeed(context.Background(),
		fetchOptions{useEmbedded: true}, advisorFeed(url, time.Hour))
	require.NoError(t, err)
	assert.Equal(t, originEmbedded, origin)
	assert.True(t, data.Embedded)
	assert.Equal(t, int32(0), server.requests.Load())
}
