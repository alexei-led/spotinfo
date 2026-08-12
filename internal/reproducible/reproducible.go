// Package reproducible holds the committed-payload encoding the snapshot
// updaters share.
//
// It is deliberately NOT in internal/snapshot. That package is linked into the
// shipped binary through each provider's embed.go, and nothing that belongs to
// refreshing data — least of all anything that reaches the network — may follow
// it there. Only cmd/update-gcp-data and cmd/update-azure-data import this.
package reproducible

import (
	"bytes"
	"compress/gzip"
	"fmt"
)

const (
	// gzipLevel is maximum compression: the payload is committed once a week and
	// read on every process start.
	gzipLevel = gzip.BestCompression
	// gzipOSUnknown is the only gzip OS byte that does not depend on the host
	// that produced the archive.
	gzipOSUnknown = 255
)

// Compress gzips a catalogue reproducibly.
//
// The header is zeroed deliberately. gzip records a modification time and an OS
// byte by default, so the same catalogue would compress to different bytes on
// different hosts and at different times: every no-op weekly refresh would
// change the payload hash and open a pull request that changes no data.
//
// This lives here because that reasoning was written out once per updater and
// had to stay true in both. It is the compression half only — each updater keeps
// its own HTTP client, because they genuinely differ: the GCP client pins
// redirects to the contracted host and the Azure one does not.
func Compress(data []byte) ([]byte, error) {
	var buffer bytes.Buffer

	writer, err := gzip.NewWriterLevel(&buffer, gzipLevel)
	if err != nil {
		return nil, fmt.Errorf("create gzip writer: %w", err)
	}

	writer.Header = gzip.Header{OS: gzipOSUnknown}

	if _, err := writer.Write(data); err != nil {
		return nil, fmt.Errorf("compress catalogue: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish catalogue: %w", err)
	}

	return buffer.Bytes(), nil
}
