// Package feedcache stores fetched provider documents under the user cache
// directory, so a second invocation does not re-download what the first one
// already read.
//
// It is provider-neutral on purpose. Two clouds now fetch at runtime — the AWS
// advisor and price feeds, and the Azure Retail Prices API — and both want the
// same operator contract: one directory, one override variable, one off switch,
// and a cache that costs time rather than answers when it cannot be used. What
// differs between them is the time-to-live and whether the origin publishes a
// validator, and both of those are the caller's to decide.
//
// Every operation is best-effort. A cache that cannot be read, written, or
// created must never fail a command: the document is still fetchable and a
// committed snapshot is still compiled in.
package feedcache

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

const (
	// Format versions the on-disk layout. An entry written by a different
	// version is discarded rather than reinterpreted.
	Format = 1

	// DirEnv overrides the cache location; DisableEnv turns caching off.
	DirEnv     = "SPOTINFO_CACHE_DIR"
	DisableEnv = "SPOTINFO_CACHE"

	// DisableValue is what DisableEnv must be set to for caching to be off.
	DisableValue = "off"

	dirMode os.FileMode = 0o700

	// maxPayloadBytes bounds a decompressed entry. The largest document cached
	// today is an AWS feed at tens of megabytes; the ceiling keeps a corrupted
	// or hostile archive from expanding without limit.
	maxPayloadBytes = 64 << 20
)

// Entry is the metadata beside a cached payload. It is written after the
// payload and carries its digest, so a half-written pair reads as a miss rather
// than as data.
type Entry struct {
	FetchedAt    time.Time `json:"fetched_at"`
	URL          string    `json:"url"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	SHA256       string    `json:"sha256"`
	Format       int       `json:"format"`
}

// Fresh reports whether the entry may be served without asking the origin.
func (e *Entry) Fresh(url string, ttl time.Duration, now time.Time) bool {
	return e.Format == Format && e.URL == url && now.Sub(e.FetchedAt) < ttl
}

// Cache stores fetched documents under the user cache directory.
type Cache struct{ dir string }

// Open resolves the cache directory, or reports a disabled cache.
//
// Disabled is not an error. Read-only filesystems, minimal containers and
// SPOTINFO_CACHE=off all land here, and all of them should still get results.
func Open() *Cache {
	if os.Getenv(DisableEnv) == DisableValue {
		return &Cache{}
	}

	dir := os.Getenv(DirEnv)
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			slog.Debug("no user cache directory; feed cache disabled", slog.Any("error", err))

			return &Cache{}
		}

		dir = filepath.Join(base, "spotinfo")
	}

	// Cleaned, and only ever joined with the fixed file names below. The
	// directory is chosen by whoever runs the command — their own environment,
	// not untrusted input — so this is tidiness rather than a trust boundary.
	dir = filepath.Clean(dir)

	if err := os.MkdirAll(dir, dirMode); err != nil { //nolint:gosec // G703: operator-chosen path, cleaned above
		slog.Debug("cannot create the feed cache directory; caching disabled",
			slog.String("dir", dir), slog.Any("error", err))

		return &Cache{}
	}

	return &Cache{dir: dir}
}

// Enabled reports whether this cache has a directory to work in.
func (c *Cache) Enabled() bool { return c.dir != "" }

func (c *Cache) paths(name string) (payload, meta string) {
	return filepath.Join(c.dir, name+".json.gz"), filepath.Join(c.dir, name+".meta.json")
}

// Load returns a cached payload and its metadata.
//
// A payload whose digest disagrees with the metadata is treated as a miss: it
// means the pair was written by an interrupted run, and half a document parses
// into a plausible-looking result rather than an obvious failure.
func (c *Cache) Load(name string) ([]byte, *Entry, bool) {
	if !c.Enabled() {
		return nil, nil, false
	}

	payloadPath, metaPath := c.paths(name)

	// G304 on both reads: the base names are caller constants and the directory
	// is the operator's own, cleaned in Open.
	rawMeta, err := os.ReadFile(metaPath) //nolint:gosec // G304: see above
	if err != nil {
		return nil, nil, false
	}

	var entry Entry
	if unmarshalErr := json.Unmarshal(rawMeta, &entry); unmarshalErr != nil {
		return nil, nil, false
	}

	archive, err := os.ReadFile(payloadPath) //nolint:gosec // G304: see above
	if err != nil {
		return nil, nil, false
	}

	if Digest(archive) != entry.SHA256 {
		slog.Debug("cached feed does not match its digest; ignoring it", slog.String("feed", name))

		return nil, nil, false
	}

	payload, err := gunzip(archive)
	if err != nil {
		return nil, nil, false
	}

	return payload, &entry, true
}

// Save stores a payload. The payload is written first and the metadata second,
// so an interrupted save leaves metadata that describes nothing — which Load
// rejects — rather than metadata that describes the wrong bytes.
func (c *Cache) Save(name string, payload []byte, entry *Entry) {
	if !c.Enabled() {
		return
	}

	archive, err := gzipBytes(payload)
	if err != nil {
		slog.Debug("cannot compress a feed for the cache", slog.String("feed", name), slog.Any("error", err))

		return
	}

	payloadPath, metaPath := c.paths(name)

	entry.Format = Format
	entry.SHA256 = Digest(archive)

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

// Touch records that the origin confirmed a cached payload is still current,
// which restarts its time-to-live without transferring the document again.
func (c *Cache) Touch(name string, entry *Entry, now time.Time) {
	if !c.Enabled() {
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

// ApplyValidators asks the origin to answer only if the document changed. An
// origin that publishes neither validator leaves the request unconditional,
// which is the Azure Retail Prices API's case: it serves no ETag and no
// Last-Modified, so an expired entry there is re-downloaded rather than
// revalidated.
func ApplyValidators(req *http.Request, entry *Entry) {
	if entry.ETag != "" {
		req.Header.Set("If-None-Match", entry.ETag)
	}

	if entry.LastModified != "" {
		req.Header.Set("If-Modified-Since", entry.LastModified)
	}
}

// Digest is the hash encoding cache metadata records.
func Digest(data []byte) string {
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

	return io.ReadAll(io.LimitReader(reader, maxPayloadBytes)) //nolint:wrapcheck // as above
}
