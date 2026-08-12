package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFileReplacesTheTargetAndLeavesNoTemporaryFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")

	require.NoError(t, WriteFile(path, []byte("first")))
	require.NoError(t, WriteFile(path, []byte("second")))

	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "second", string(contents))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "a temporary file survived the write")
}

// An update that cannot be written must leave the previously reviewed snapshot
// exactly as it was. That is the difference between a failed refresh and a
// corrupted one.
func TestWriteFileLeavesTheReviewedSnapshotIntactOnFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	require.NoError(t, os.WriteFile(path, []byte("reviewed"), 0o644))

	// A read-only directory makes the temporary file impossible to create,
	// which is the failure the two-step write exists to survive.
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	require.Error(t, WriteFile(path, []byte("replacement")))

	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "reviewed", string(contents))
}

// A refresh changes one snapshot at a time, and the sidecars are rewritten as a
// set. Skipping the writes that would change nothing keeps the untouched files
// out of the run entirely, so a failure partway through the set cannot leave one
// of them rewritten against a payload the caller is about to roll back.
func TestWriteFileSkipsAnIdenticalRewrite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "manifest.json")
	require.NoError(t, os.WriteFile(path, []byte("reviewed"), 0o644))

	stale := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(path, stale, stale))

	require.NoError(t, WriteFile(path, []byte("reviewed")))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.ModTime().Equal(stale), "an identical rewrite touched the file")
}

func TestWriteFileKeepsTheExistingMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	require.NoError(t, os.WriteFile(path, []byte("first"), 0o640))

	require.NoError(t, WriteFile(path, []byte("second")))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

func TestWriteManifestRoundTrips(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "manifest.json")
	manifest := validManifest(t)

	require.NoError(t, WriteManifest(path, manifest))

	written, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, byte('\n'), written[len(written)-1], "a manifest ends with a newline so it diffs cleanly")

	reloaded, err := ParseManifest(written)
	require.NoError(t, err)
	assert.Equal(t, manifest, reloaded)
}

func TestWriteSnapshotRestoresPayloadWhenManifestWriteFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "catalog.json.gz")
	manifestPath := filepath.Join(dir, "manifest.json")
	require.NoError(t, os.WriteFile(payloadPath, []byte("reviewed"), 0o644))
	require.NoError(t, os.Mkdir(manifestPath, 0o755))

	err := WriteSnapshot(payloadPath, []byte("replacement"), manifestPath, validManifest(t))
	require.ErrorContains(t, err, "write snapshot manifest")

	payload, readErr := os.ReadFile(payloadPath)
	require.NoError(t, readErr)
	assert.Equal(t, "reviewed", string(payload))
}

func TestWriteSnapshotRemovesNewPayloadWhenManifestWriteFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "catalog.json.gz")
	manifestPath := filepath.Join(dir, "manifest.json")
	require.NoError(t, os.Mkdir(manifestPath, 0o755))

	err := WriteSnapshot(payloadPath, []byte("replacement"), manifestPath, validManifest(t))
	require.ErrorContains(t, err, "write snapshot manifest")
	assert.NoFileExists(t, payloadPath)
}

// A manifest that would fail its own gate must never reach disk: writing it
// would replace a good sidecar with one the next verification rejects.
func TestWriteManifestRefusesAnInvalidManifest(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "manifest.json")

	manifest := validManifest(t)
	manifest.Currency = "EUR"

	require.ErrorIs(t, WriteManifest(path, manifest), ErrInvalidManifest)
	assert.NoFileExists(t, path)
}
