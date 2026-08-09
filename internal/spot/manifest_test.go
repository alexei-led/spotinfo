package spot

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/snapshot"
)

// embeddedSnapshot pairs a committed data file with the sidecar manifest that
// describes it and with the coverage its parser actually finds.
type embeddedSnapshot struct {
	load     func() (*snapshot.Manifest, error)
	payload  func(t *testing.T) []byte
	coverage func(t *testing.T) snapshot.Coverage
	manifest string
}

func embeddedSnapshots() []embeddedSnapshot {
	return []embeddedSnapshot{
		{
			manifest: advisorManifestFile,
			load:     advisorManifest,
			payload:  func(*testing.T) []byte { return []byte(embeddedSpotData) },
			coverage: func(t *testing.T) snapshot.Coverage {
				t.Helper()

				data, err := loadEmbeddedAdvisorData()
				require.NoError(t, err)

				return advisorCoverage(data)
			},
		},
		{
			manifest: priceManifestFile,
			load:     priceManifest,
			payload:  func(*testing.T) []byte { return []byte(embeddedPriceData) },
			coverage: func(t *testing.T) snapshot.Coverage {
				t.Helper()

				data, err := loadEmbeddedPricingData()
				require.NoError(t, err)

				return priceCoverage(data)
			},
		},
		{
			manifest: architectureManifestFile,
			load:     architectureManifest,
			payload: func(t *testing.T) []byte {
				t.Helper()

				contents, err := architectureSnapshotFS.ReadFile("data/architecture-snapshot.json")
				require.NoError(t, err)

				return contents
			},
			coverage: func(t *testing.T) snapshot.Coverage {
				t.Helper()

				lookup, err := LoadEmbeddedArchitectureLookup()
				require.NoError(t, err)

				return snapshot.Coverage{Machines: len(lookup.families)}
			},
		},
	}
}

// TestEmbeddedSnapshotManifests is the data gate: every committed AWS snapshot
// must have a valid manifest, hash to what that manifest declares, and still
// cover its reviewed floor.
//
// Run with UPDATE_GOLDEN=1 after refreshing a feed to rewrite the hashes and
// fetch times. Coverage floors stay hand-curated on purpose — regenerating them
// from whatever just downloaded would ratchet the gate to always pass.
func TestEmbeddedSnapshotManifests(t *testing.T) {
	t.Parallel()

	regenerate := os.Getenv("UPDATE_GOLDEN") == "1"

	for _, embedded := range embeddedSnapshots() {
		t.Run(embedded.manifest, func(t *testing.T) {
			t.Parallel()

			payload := embedded.payload(t)

			manifest, err := embedded.load()
			require.NoError(t, err)

			if regenerate {
				manifest = refreshed(manifest, payload)
			}

			require.NoError(t, manifest.VerifyPayload(payload),
				"%s and its data file were not updated together; refresh with UPDATE_GOLDEN=1", embedded.manifest)
			require.NoError(t, snapshot.ValidateCoverage(embedded.coverage(t), manifest.MinRecords))

			// Written last, and only once the refreshed manifest describes a
			// payload that still meets its reviewed floor. Writing first would
			// leave a short feed hashed under its own manifest — the two agreeing
			// about data no reviewer accepted.
			if regenerate {
				require.NoError(t, snapshot.WriteManifest(filepath.Join("data", embedded.manifest), manifest))
			}
		})
	}
}

func TestEmbeddedManifestsDescribeTheirOwnFiles(t *testing.T) {
	t.Parallel()

	for _, embedded := range embeddedSnapshots() {
		manifest, err := embedded.load()
		require.NoError(t, err)

		assert.FileExists(t, filepath.Join("data", manifest.Payload.File))
	}
}

func TestEmbeddedSourceRefsCoverEverySnapshot(t *testing.T) {
	t.Parallel()

	refs, err := EmbeddedSourceRefs()
	require.NoError(t, err)
	require.Len(t, refs, len(embeddedSnapshots()))

	for _, ref := range refs {
		assert.NotEmpty(t, ref.URL)
		assert.NotEmpty(t, ref.ParserVersion)
		assert.NotEmpty(t, ref.SchemaVersion)
		assert.False(t, ref.FetchedAt.IsZero())
	}
}

// The neutral record validator run against the real feed. It is the only check
// that would catch the same machine being priced twice in one region and OS —
// the shape a feed takes when it starts listing a size under two families.
func TestEmbeddedPricesSatisfyTheNeutralRecordContract(t *testing.T) {
	t.Parallel()

	data, err := loadEmbeddedPricingData()
	require.NoError(t, err)

	manifest, err := priceManifest()
	require.NoError(t, err)

	require.NoError(t, snapshot.ValidatePrices(embeddedPriceRecords(data, manifest), manifest))
}

// embeddedPriceRecords flattens the feed into neutral records. Zero and
// unparseable cells are skipped: an unknown price is an absent record, and the
// coverage floor counts only the priced ones.
func embeddedPriceRecords(data *rawPriceData, manifest *snapshot.Manifest) []snapshot.PriceRecord {
	records := make([]snapshot.PriceRecord, 0, manifest.MinRecords.Prices)

	for _, region := range data.Config.Regions {
		for _, instanceType := range region.InstanceTypes {
			for _, size := range instanceType.Sizes {
				for _, column := range size.ValueColumns {
					price, ok := parsePrice(column.Prices.USD)
					if !ok || price == 0 {
						continue
					}

					amount, err := cloud.MoneyFromFloat(price)
					if err != nil {
						continue
					}

					instanceOS := cloud.OSLinux
					if column.Name == priceColumnWindows {
						instanceOS = cloud.OSWindows
					}

					records = append(records, snapshot.PriceRecord{
						Region:   cloud.Region(region.Region),
						Machine:  cloud.MachineID(size.Size),
						OS:       instanceOS,
						Class:    cloud.PriceClassSpot,
						Currency: manifest.Currency,
						Unit:     manifest.BillingUnit,
						Amount:   amount,
					})
				}
			}
		}
	}

	return records
}

// A live feed that parses but covers far less than the committed snapshot is a
// truncated document, not an update, and must not replace the embedded data.
func TestValidateCoverageRejectsATruncatedLiveFeed(t *testing.T) {
	t.Parallel()

	truncated, err := parsePricingResponse([]byte(validPricingPayload))
	require.NoError(t, err)

	require.ErrorIs(t, validatePriceCoverage(truncated), snapshot.ErrCoverage)

	thin, err := parseAdvisorResponse([]byte(validAdvisorPayload))
	require.NoError(t, err)

	require.ErrorIs(t, validateAdvisorCoverage(thin), snapshot.ErrCoverage)
}

// A repeated row is redundancy, not coverage. Counting raw cells would let a
// live feed that lost every region but one clear a floor meant to describe the
// whole matrix, so every dimension is counted distinct.
func TestPriceCoverageCountsDistinctRowsOnly(t *testing.T) {
	t.Parallel()

	region := regionConfig{
		Region: "us-east-1",
		InstanceTypes: []instanceTypeConfig{{
			Type: "generalCurrentGen",
			Sizes: []sizeConfig{{
				Size: "m5.large",
				ValueColumns: []valueColumnConfig{
					{Name: "linux", Prices: priceConfig{USD: "0.0416"}},
					{Name: "mswin", Prices: priceConfig{USD: "0.1234"}},
				},
			}},
		}},
	}

	want := snapshot.Coverage{Regions: 1, Machines: 1, Prices: 2}
	assert.Equal(t, want, priceCoverage(&rawPriceData{Config: config{Regions: []regionConfig{region}}}))
	assert.Equal(t, want,
		priceCoverage(&rawPriceData{Config: config{Regions: []regionConfig{region, region, region}}}),
		"repeating a region must not pad any dimension")
}

// refreshed rewrites only what a data refresh can change: the payload hash, and
// — for a raw feed, whose committed bytes are the source — the source hash and
// fetch time. A reviewed catalogue keeps its human provenance, and coverage
// floors are never touched.
func refreshed(manifest *snapshot.Manifest, payload []byte) *snapshot.Manifest {
	updated := *manifest
	updated.Payload.SHA256 = snapshot.SHA256Hex(payload)

	if updated.Payload.Form == snapshot.PayloadFormRawSource {
		updated.Sources = []snapshot.Source{manifest.Sources[0]}
		updated.Sources[0].SHA256 = updated.Payload.SHA256

		if manifest.Payload.SHA256 != updated.Payload.SHA256 {
			updated.Sources[0].FetchedAt = time.Now().UTC().Truncate(time.Second)
		}
	}

	return &updated
}
