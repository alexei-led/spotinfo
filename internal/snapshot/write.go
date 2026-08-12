package snapshot

import (
	"bytes"
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
//
// Writing bytes the target already holds is a no-op. Snapshot files are
// content-addressed by their manifests, so an identical rewrite changes nothing
// a reader can observe — and a file a refresh never opens is a file no failure
// elsewhere in that refresh can leave half-written.
func WriteFile(path string, data []byte) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) { //nolint:gosec // the caller owns the snapshot path.
		return nil
	}

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
	data, err := encodeManifest(manifest, path)
	if err != nil {
		return err
	}

	return WriteFile(path, data)
}

// WriteSnapshot writes a payload and its manifest as one update. The two files
// cannot be renamed as one filesystem object, so the payload is restored if the
// manifest commit fails. Callers therefore never return from a failed update
// with the reviewed payload replaced by an unpaired one.
func WriteSnapshot(payloadPath string, payload []byte, manifestPath string, manifest *Manifest) error {
	manifestData, err := encodeManifest(manifest, manifestPath)
	if err != nil {
		return err
	}

	previousPayload, payloadExists, err := previousFile(payloadPath)
	if err != nil {
		return err
	}
	if err := WriteFile(payloadPath, payload); err != nil {
		return fmt.Errorf("write snapshot payload: %w", err)
	}
	if err := WriteFile(manifestPath, manifestData); err != nil {
		if payloadExists {
			if restoreErr := WriteFile(payloadPath, previousPayload); restoreErr != nil {
				return fmt.Errorf("write snapshot manifest: %w; restore payload: %w", err, restoreErr)
			}
		} else if restoreErr := os.Remove(payloadPath); restoreErr != nil && !os.IsNotExist(restoreErr) {
			return fmt.Errorf("write snapshot manifest: %w; remove payload: %w", err, restoreErr)
		}

		return fmt.Errorf("write snapshot manifest: %w", err)
	}

	return nil
}

func encodeManifest(manifest *Manifest, path string) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode manifest %s: %w", path, err)
	}

	return append(data, '\n'), nil
}

func previousFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the caller owns the snapshot path.
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}

	return nil, false, fmt.Errorf("read previous snapshot payload: %w", err)
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
