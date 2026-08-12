package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

// validManifest returns the recorded good fixture, already parsed, so each test
// states only the field it breaks.
func validManifest(t *testing.T) *Manifest {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "manifest-valid.json"))
	require.NoError(t, err)

	manifest, err := ParseManifest(raw)
	require.NoError(t, err)

	return manifest
}

// reparse round-trips a manifest through JSON, which is how a gate actually
// reads one.
func reparse(t *testing.T, manifest *Manifest) error {
	t.Helper()

	raw, err := json.Marshal(manifest)
	require.NoError(t, err)

	_, err = ParseManifest(raw)

	return err
}

func TestParseManifestAcceptsTheRecordedFixture(t *testing.T) {
	t.Parallel()

	manifest := validManifest(t)

	assert.Equal(t, cloud.ProviderGCP, manifest.Provider)
	assert.Equal(t, KindSpotPrice, manifest.Kind)
	assert.Equal(t, cloud.CurrencyUSD, manifest.Currency)
	assert.Equal(t, cloud.BillingUnitInstanceHour, manifest.BillingUnit)
	assert.Equal(t, PayloadFormParsedCatalog, manifest.Payload.Form)
	assert.Equal(t, Coverage{Regions: 20, Machines: 40, Prices: 800}, manifest.MinRecords)

	refs := manifest.SourceRefs()
	require.Len(t, refs, 2)
	assert.Equal(t, "https://cloud.google.com/spot-vms/pricing", refs[0].URL)
	assert.Equal(t, manifest.ParserVersion, refs[0].ParserVersion)
	assert.Equal(t, manifest.DataSchemaVersion, refs[0].SchemaVersion)
	assert.Equal(t, manifest.Sources[0].SHA256, refs[0].ContentSHA256)
	assert.Equal(t, manifest.Sources[1].SHA256, refs[1].ContentSHA256)
	assert.Nil(t, refs[0].ObservedAt)
}

// The invalid fixture is a parsed catalogue whose payload hash equals its source
// hash: the shape of an updater that committed the downloaded page instead of
// the catalogue it was supposed to build from it.
func TestParseManifestRejectsTheRecordedInvalidFixture(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("testdata", "manifest-invalid.json"))
	require.NoError(t, err)

	_, err = ParseManifest(raw)
	require.ErrorIs(t, err, ErrInvalidManifest)
	assert.Contains(t, err.Error(), "parsed-catalog")
}

func TestParseManifestRejectsTrailingJSON(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("testdata", "manifest-valid.json"))
	require.NoError(t, err)

	_, err = ParseManifest(append(raw, []byte("{}")...))
	require.ErrorIs(t, err, ErrInvalidManifest)
}

func TestParseManifestFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mutate func(manifest *Manifest)
		name   string
	}{
		{
			name:   "unknown manifest schema version",
			mutate: func(m *Manifest) { m.SchemaVersion = "spotinfo.snapshot-manifest/v2" },
		},
		{
			name:   "unknown provider",
			mutate: func(m *Manifest) { m.Provider = "oracle" },
		},
		{
			name:   "unknown kind",
			mutate: func(m *Manifest) { m.Kind = "weather" },
		},
		{
			name:   "blank parser version",
			mutate: func(m *Manifest) { m.ParserVersion = " " },
		},
		{
			name:   "missing data schema version",
			mutate: func(m *Manifest) { m.DataSchemaVersion = "" },
		},
		{
			name:   "no sources",
			mutate: func(m *Manifest) { m.Sources = nil },
		},
		{
			name:   "plain http source",
			mutate: func(m *Manifest) { m.Sources[0].URL = "http://cloud.google.com/spot-vms/pricing" },
		},
		{
			name:   "relative source url",
			mutate: func(m *Manifest) { m.Sources[0].URL = "/spot-vms/pricing" },
		},
		{
			name:   "missing fetch time",
			mutate: func(m *Manifest) { m.Sources[0].FetchedAt = time.Time{} },
		},
		{
			name: "observed after it was fetched",
			mutate: func(m *Manifest) {
				observed := m.Sources[0].FetchedAt.Add(time.Hour)
				m.Sources[0].ObservedAt = &observed
			},
		},
		{
			name:   "truncated source hash",
			mutate: func(m *Manifest) { m.Sources[0].SHA256 = "3fdba35f" },
		},
		{
			name: "uppercase payload hash",
			mutate: func(m *Manifest) {
				m.Payload.SHA256 = "3FDBA35F04DC8C462986C992BCF875546257113072A909C162F7E470E581E278"
			},
		},
		{
			name:   "payload path escapes its directory",
			mutate: func(m *Manifest) { m.Payload.File = "../catalog.json.gz" },
		},
		{
			name:   "unknown payload form",
			mutate: func(m *Manifest) { m.Payload.Form = "compressed" },
		},
		{
			name:   "raw source payload built from several sources",
			mutate: func(m *Manifest) { m.Payload.Form = PayloadFormRawSource },
		},
		{
			name:   "price snapshot without a currency",
			mutate: func(m *Manifest) { m.Currency = "" },
		},
		{
			name:   "unsupported currency",
			mutate: func(m *Manifest) { m.Currency = "EUR" },
		},
		{
			name:   "unsupported billing unit",
			mutate: func(m *Manifest) { m.BillingUnit = "month" },
		},
		{
			name:   "non-price snapshot claiming a currency",
			mutate: func(m *Manifest) { m.Kind = KindMachineArchitecture },
		},
		{
			name:   "negative coverage floor",
			mutate: func(m *Manifest) { m.MinRecords.Machines = -1 },
		},
		{
			name:   "no machine floor",
			mutate: func(m *Manifest) { m.MinRecords.Machines = 0 },
		},
		{
			name:   "price snapshot with no price floor",
			mutate: func(m *Manifest) { m.MinRecords.Prices = 0 },
		},
		{
			name:   "price snapshot with no region floor",
			mutate: func(m *Manifest) { m.MinRecords.Regions = 0 },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			manifest := validManifest(t)
			tc.mutate(manifest)

			require.ErrorIs(t, reparse(t, manifest), ErrInvalidManifest)
		})
	}
}

// A renamed key must fail loudly. Decoding it into a zero value would let a
// source change shape while its manifest still claimed to describe it.
func TestParseManifestRejectsRenamedKeys(t *testing.T) {
	t.Parallel()

	renamed, err := json.Marshal(map[string]any{"schemaVersion": ManifestSchemaVersion})
	require.NoError(t, err)

	_, err = ParseManifest(renamed)
	require.ErrorIs(t, err, ErrInvalidManifest)
	assert.Contains(t, err.Error(), "schemaVersion")
}

// A raw-source snapshot is the AWS shape: the committed file is the feed itself,
// so its hash must equal the source hash rather than merely differ from it.
func TestParseManifestAcceptsRawSourceOnlyWhenHashesAgree(t *testing.T) {
	t.Parallel()

	manifest := validManifest(t)
	manifest.Payload.Form = PayloadFormRawSource
	manifest.Sources = manifest.Sources[:1]
	manifest.Sources[0].SHA256 = manifest.Payload.SHA256

	require.NoError(t, reparse(t, manifest))

	manifest.Sources[0].SHA256 = SHA256Hex([]byte("a different document"))
	require.ErrorIs(t, reparse(t, manifest), ErrInvalidManifest)
}

// A reviewed catalogue built by hand from documentation has no byte-level source
// to hash, so an empty source hash is allowed for a parsed catalogue.
func TestParseManifestPreservesAnUnhashedReviewedSource(t *testing.T) {
	t.Parallel()

	manifest := validManifest(t)
	manifest.Sources[0].SHA256 = ""

	require.NoError(t, reparse(t, manifest))
	require.Empty(t, manifest.SourceRefs()[0].ContentSHA256)
}

func TestVerifyPayloadDetectsAnUnpairedUpdate(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"machines":[]}`)

	manifest := validManifest(t)
	manifest.Payload.SHA256 = SHA256Hex(payload)

	require.NoError(t, manifest.VerifyPayload(payload))
	require.ErrorIs(t, manifest.VerifyPayload(append(payload, ' ')), ErrHashMismatch)
}

func TestKindIsPrice(t *testing.T) {
	t.Parallel()

	assert.True(t, KindSpotPrice.IsPrice())
	assert.True(t, KindOnDemandPrice.IsPrice())
	assert.False(t, KindInterruptionRisk.IsPrice())
	assert.False(t, KindMachineArchitecture.IsPrice())
}
