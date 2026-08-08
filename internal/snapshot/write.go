package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// defaultSnapshotMode is the mode a newly created snapshot file gets. Committed
// data files are world-readable, and an update must not silently tighten that.
const defaultSnapshotMode os.FileMode = 0o644

// WriteFile replaces path atomically: the bytes land in a temporary file in the
// same directory, are flushed, and only then rename over the target. A failed,
// partial, or interrupted update therefore leaves the previously reviewed
// snapshot untouched — the difference between a failed update and a corrupted
// one. Same directory, because rename is only atomic within a filesystem.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)

	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}

	tempName := temp.Name()
	// A no-op once the rename succeeded; the cleanup that matters is every
	// early return below.
	defer func() { _ = os.Remove(tempName) }()

	if err := writeAndSync(temp, data); err != nil {
		_ = temp.Close()

		return err
	}

	if err := temp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tempName, err)
	}

	//nolint:gosec // G302: snapshot data files are committed and world-readable by design.
	if err := os.Chmod(tempName, targetMode(path)); err != nil {
		return fmt.Errorf("set mode on %s: %w", tempName, err)
	}

	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}

	return nil
}

// WriteManifest validates a manifest and writes it as indented JSON with a
// trailing newline, so a regenerated manifest diffs cleanly in review. An
// invalid manifest is never written: a snapshot must not ship with a sidecar
// that would fail its own gate.
func WriteManifest(path string, manifest *Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest %s: %w", path, err)
	}

	return WriteFile(path, append(data, '\n'))
}

func writeAndSync(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", file.Name(), err)
	}

	if err := file.Sync(); err != nil {
		return fmt.Errorf("flush %s: %w", file.Name(), err)
	}

	return nil
}

// targetMode keeps an existing file's permissions across a replacement.
func targetMode(path string) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}

	return defaultSnapshotMode
}
