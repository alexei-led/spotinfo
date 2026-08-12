package reproducible_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/reproducible"
)

// The committed payload has to compress to the same bytes every time. gzip
// records a modification time and an OS byte by default, so without the zeroed
// header the same catalogue would hash differently on each host and every
// no-op weekly refresh would open a pull request that changes no data.
func TestCompressIsReproducible(t *testing.T) {
	t.Parallel()

	data := []byte(`{"schema_version":"spotinfo.gcp-catalog/v1","machines":[]}`)

	first, err := reproducible.Compress(data)
	require.NoError(t, err)

	second, err := reproducible.Compress(data)
	require.NoError(t, err)

	assert.Equal(t, first, second, "the same catalogue must compress to the same bytes")

	reader, err := gzip.NewReader(bytes.NewReader(first))
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	assert.True(t, reader.Header.ModTime.IsZero(), "a recorded mtime would change the hash on every run")
	assert.Equal(t, byte(255), reader.Header.OS, "the OS byte must not depend on the host")

	round, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, round)
}
