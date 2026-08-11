package spot

import "time"

// How long each AWS feed may be served from the shared feed cache.
//
// Every invocation used to re-download both feeds, which is most of its
// runtime. The two are opposites, and the time-to-live values below follow that
// rather than one blanket number:
//
//   - The advisor document takes over a second to transfer and its
//     Last-Modified has been months in the past — AWS recomputes those
//     interruption buckets rarely. Caching it is where the time is.
//   - The price document transfers in about a tenth of a second and is
//     rewritten through the day. Caching it saves little and risks serving a
//     stale price, which is the number people act on, so it gets a short window.
//
// An expired entry is revalidated, not re-downloaded: both feeds serve ETag and
// Last-Modified, and a 304 costs one round trip and no payload. The mechanics
// live in internal/feedcache, which Azure's live price path shares.
const (
	// advisorCacheTTL is how long a cached advisor document is served without
	// asking the origin. Generous because the document barely changes.
	advisorCacheTTL = 24 * time.Hour
	// priceCacheTTL is deliberately short: prices are what a caller acts on.
	priceCacheTTL = time.Hour

	// advisorFeedName names the advisor feed in logs and on disk.
	advisorFeedName = "advisor"
)
