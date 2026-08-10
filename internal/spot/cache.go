package spot

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"spotinfo/internal/snapshot"
)

// Feed cache.
//
// Every invocation used to re-download both AWS feeds, which is most of its
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
// Last-Modified, and a 304 costs one round trip and no payload.
const (
	// advisorCacheTTL is how long a cached advisor document is served without
	// asking the origin. Generous because the document barely changes.
	advisorCacheTTL = 24 * time.Hour
	// priceCacheTTL is deliberately short: prices are what a caller acts on.
	priceCacheTTL = time.Hour

	// cacheFormat versions the on-disk layout. An entry written by a different
	// version is discarded rather than reinterpreted.
	cacheFormat = 1

	// cacheDirEnv overrides the cache location; cacheDisableEnv turns it off.
	cacheDirEnv     = "SPOTINFO_CACHE_DIR"
	cacheDisableEnv = "SPOTINFO_CACHE"

	cacheDirMode os.FileMode = 0o700

	// advisorFeedName names the advisor feed in logs and on disk.
	advisorFeedName = "advisor"
)

// cacheEntry is the metadata beside a cached payload. It is written after the
// payload and carries its digest, so a half-written pair reads as a miss rather
// than as data.
type cacheEntry struct {
	FetchedAt    time.Time `json:"fetched_at"`
	URL          string    `json:"url"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	SHA256       string    `json:"sha256"`
	Format       int       `json:"format"`
}

// fresh reports whether the entry may be served without asking the origin.
func (e *cacheEntry) fresh(url string, ttl time.Duration, now time.Time) bool {
	return e.Format == cacheFormat && e.URL == url && now.Sub(e.FetchedAt) < ttl
}

// feedCache stores fetched feeds under the user cache directory.
//
// Every operation is best-effort. A cache that cannot be read, written, or
// created must never fail the command: the feed is still fetchable and the
// committed snapshot is still there, so a broken cache costs time, not answers.
type feedCache struct{ dir string }

// openFeedCache resolves the cache directory, or reports a disabled cache.
//
// Disabled is not an error. Read-only filesystems, minimal containers and
// SPOTINFO_CACHE=off all land here, and all of them should still get results.
func openFeedCache() *feedCache {
	if os.Getenv(cacheDisableEnv) == "off" {
		return &feedCache{}
	}

	dir := os.Getenv(cacheDirEnv)
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			slog.Debug("no user cache directory; feed cache disabled", slog.Any("error", err))

			return &feedCache{}
		}

		dir = filepath.Join(base, "spotinfo")
	}

	// Cleaned, and only ever joined with the fixed file names below. The
	// directory is chosen by whoever runs the command — their own environment,
	// not untrusted input — so this is tidiness rather than a trust boundary.
	dir = filepath.Clean(dir)

	if err := os.MkdirAll(dir, cacheDirMode); err != nil { //nolint:gosec // G703: operator-chosen path, cleaned above
		slog.Debug("cannot create the feed cache directory; caching disabled",
			slog.String("dir", dir), slog.Any("error", err))

		return &feedCache{}
	}

	return &feedCache{dir: dir}
}

func (c *feedCache) enabled() bool { return c.dir != "" }

func (c *feedCache) paths(name string) (payload, meta string) {
	return filepath.Join(c.dir, name+".json.gz"), filepath.Join(c.dir, name+".meta.json")
}

// load returns a cached payload and its metadata.
//
// A payload whose digest disagrees with the metadata is treated as a miss: it
// means the pair was written by an interrupted run, and half a document parses
// into a plausible-looking result rather than an obvious failure.
func (c *feedCache) load(name string) ([]byte, *cacheEntry, bool) {
	if !c.enabled() {
		return nil, nil, false
	}

	payloadPath, metaPath := c.paths(name)

	// G304 on both reads: the base names are package constants and the directory
	// is the operator's own, cleaned in openFeedCache.
	rawMeta, err := os.ReadFile(metaPath) //nolint:gosec // G304: see above
	if err != nil {
		return nil, nil, false
	}

	var entry cacheEntry
	if unmarshalErr := json.Unmarshal(rawMeta, &entry); unmarshalErr != nil {
		return nil, nil, false
	}

	archive, err := os.ReadFile(payloadPath) //nolint:gosec // G304: see above
	if err != nil {
		return nil, nil, false
	}

	if digest(archive) != entry.SHA256 {
		slog.Debug("cached feed does not match its digest; ignoring it", slog.String("feed", name))

		return nil, nil, false
	}

	payload, err := gunzip(archive)
	if err != nil {
		return nil, nil, false
	}

	return payload, &entry, true
}

// save stores a payload. The payload is written first and the metadata second,
// so an interrupted save leaves metadata that describes nothing — which load
// rejects — rather than metadata that describes the wrong bytes.
func (c *feedCache) save(name string, payload []byte, entry *cacheEntry) {
	if !c.enabled() {
		return
	}

	archive, err := gzipBytes(payload)
	if err != nil {
		slog.Debug("cannot compress a feed for the cache", slog.String("feed", name), slog.Any("error", err))

		return
	}

	payloadPath, metaPath := c.paths(name)

	entry.Format = cacheFormat
	entry.SHA256 = digest(archive)

	rawMeta, err := json.Marshal(entry)
	if err != nil {
		return
	}

	// snapshot.WriteFile renames into place, so a reader never sees a partial
	// file and two concurrent invocations resolve to last-writer-wins.
	if err := snapshot.WriteFile(payloadPath, archive); err != nil {
		slog.Debug("cannot write a feed to the cache", slog.String("feed", name), slog.Any("error", err))

		return
	}

	if err := snapshot.WriteFile(metaPath, rawMeta); err != nil {
		slog.Debug("cannot write feed cache metadata", slog.String("feed", name), slog.Any("error", err))
	}
}

// touch records that the origin confirmed a cached payload is still current,
// which restarts its time-to-live without transferring the document again.
func (c *feedCache) touch(name string, entry *cacheEntry, now time.Time) {
	if !c.enabled() {
		return
	}

	entry.FetchedAt = now

	rawMeta, err := json.Marshal(entry)
	if err != nil {
		return
	}

	_, metaPath := c.paths(name)
	if err := snapshot.WriteFile(metaPath, rawMeta); err != nil {
		slog.Debug("cannot refresh feed cache metadata", slog.String("feed", name), slog.Any("error", err))
	}
}

// applyValidators asks the origin to answer only if the document changed.
func applyValidators(req *http.Request, entry *cacheEntry) {
	if entry.ETag != "" {
		req.Header.Set("If-None-Match", entry.ETag)
	}

	if entry.LastModified != "" {
		req.Header.Set("If-Modified-Since", entry.LastModified)
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func gzipBytes(data []byte) ([]byte, error) {
	var buffer bytes.Buffer

	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(data); err != nil {
		return nil, err //nolint:wrapcheck // best-effort cache path; the caller only logs
	}

	if err := writer.Close(); err != nil {
		return nil, err //nolint:wrapcheck // best-effort cache path; the caller only logs
	}

	return buffer.Bytes(), nil
}

func gunzip(archive []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err //nolint:wrapcheck // best-effort cache path; the caller only logs
	}
	defer func() { _ = reader.Close() }()

	return io.ReadAll(io.LimitReader(reader, maxEmbeddedFeedBytes)) //nolint:wrapcheck // as above
}
