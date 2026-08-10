package spot

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/reproducible"
	"spotinfo/internal/snapshot"
)

// embeddedSnapshot pairs a committed data file with the sidecar manifest that
// describes it and with the coverage its parser actually finds.
type embeddedSnapshot struct {
	load     func() (*snapshot.Manifest, error)
	coverage func(t *testing.T, payload []byte) snapshot.Coverage
	manifest string
	// file is the payload on disk, the file the manifest names and hashes.
	file string
	// source is the readable .json a compressed payload is built from, empty for
	// a payload committed raw. It is what a data-refresh pull request is reviewed
	// from, and what the archive is regenerated from.
	source string
}

// readPayload reads the file the manifest describes.
//
// Deliberately from disk rather than from the //go:embed variable: a refresh
// rewrites the archive during the run, and the embedded copy is whatever was
// compiled in before that write. Hashing the embedded bytes recorded a manifest
// for an archive that no longer existed. The embedded copy is checked against
// this file by TestEmbeddedArchivesMatchTheirJSON and by the loaders' own
// verification.
func (e embeddedSnapshot) readPayload(t *testing.T) []byte {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("data", e.file))
	require.NoError(t, err)

	return contents
}

func embeddedSnapshots() []embeddedSnapshot {
	return []embeddedSnapshot{
		{
			manifest: advisorManifestFile,
			load:     advisorManifest,
			source:   "spot-advisor-data.json",
			file:     "spot-advisor-data.json.gz",
			coverage: func(t *testing.T, payload []byte) snapshot.Coverage {
				t.Helper()

				contents, err := decompressEmbedded(payload)
				require.NoError(t, err)

				data, err := parseAdvisorResponse(contents)
				require.NoError(t, err)

				return advisorCoverage(data)
			},
		},
		{
			manifest: priceManifestFile,
			load:     priceManifest,
			source:   "spot-price-data.json",
			file:     "spot-price-data.json.gz",
			coverage: func(t *testing.T, payload []byte) snapshot.Coverage {
				t.Helper()

				contents, err := decompressEmbedded(payload)
				require.NoError(t, err)

				data, err := parsePricingResponse(contents)
				require.NoError(t, err)

				return priceCoverage(data)
			},
		},
		{
			manifest: architectureManifestFile,
			load:     architectureManifest,
			file:     "architecture-snapshot.json",
			coverage: func(t *testing.T, payload []byte) snapshot.Coverage {
				t.Helper()

				lookup, err := parseArchitectureSnapshot(payload)
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
// Coverage is computed by parsing the payload this gate was handed, not by
// calling the loaders. The loaders verify the payload against the on-disk
// manifest, which is the very file a refresh is in the middle of replacing — so
// routing coverage through them made `make refresh-manifests` fail on exactly
// the case it exists for, a feed whose data actually changed.
//
// Run with REFRESH_MANIFESTS=1 after refreshing a feed to rewrite the hashes and
// fetch times. Coverage floors stay hand-curated on purpose — regenerating them
// from whatever just downloaded would ratchet the gate to always pass.
//
// The variable is deliberately NOT UPDATE_GOLDEN, which the CLI and MCP contract
// goldens use. Those rewrite and then fail the run, so a regeneration can never
// be reported as a pass; this gate rewrites and passes, because rewriting is the
// point of `make refresh-manifests`. Sharing one name meant that regenerating a
// contract golden — or any ambient UPDATE_GOLDEN=1 — silently re-blessed whatever
// data files happened to be on disk and reported success.
func TestEmbeddedSnapshotManifests(t *testing.T) {
	t.Parallel()

	regenerate := os.Getenv("REFRESH_MANIFESTS") == "1"
	snapshots := embeddedSnapshots()
	manifests := make([]*snapshot.Manifest, len(snapshots))

	// Two phases, because the sidecars are refreshed as a set. Writing inside
	// each subtest lets one snapshot's manifest survive another's failure, and
	// the Makefile restores only the payload it downloaded — leaving a manifest
	// that hashes data no reviewer accepted. The group runs its parallel
	// children to completion before returning, so nothing is written until every
	// refreshed manifest describes a payload that still meets its floor.
	//
	// The write loop then runs file by file, which is safe because
	// snapshot.WriteFile skips a manifest whose bytes did not change and a
	// refresh swaps one payload: the run rewrites the one sidecar that payload
	// changed, atomically, and never opens the other two.
	if regenerate {
		for _, embedded := range snapshots {
			if embedded.source == "" {
				continue
			}
			require.NoError(t, writeArchive(embedded.source),
				"regenerate the archive %s ships from the .json a reviewer reads", embedded.source)
		}
	}

	validated := t.Run("validate", func(t *testing.T) {
		for i, embedded := range snapshots {
			t.Run(embedded.manifest, func(t *testing.T) {
				t.Parallel()

				payload := embedded.readPayload(t)

				manifest, err := embedded.load()
				require.NoError(t, err)

				if regenerate {
					manifest = refreshed(t, manifest, embedded, payload)
				}

				require.NoError(t, manifest.VerifyPayload(payload),
					"%s and its data file were not updated together; refresh with `make refresh-manifests`", embedded.manifest)
				require.NoError(t, snapshot.ValidateCoverage(embedded.coverage(t, payload), manifest.MinRecords))

				manifests[i] = manifest
			})
		}
	})

	if !regenerate || !validated {
		return
	}

	for i, embedded := range snapshots {
		require.NoError(t, snapshot.WriteManifest(filepath.Join("data", embedded.manifest), manifests[i]))
	}
}

// The shipped binary must refuse a payload that drifted from its manifest, not
// just `make verify-data`. GCP and Azure disable themselves on this; AWS used to
// serve the bytes anyway while publishing the manifest hash as their provenance.
func TestVerifyEmbeddedPayloadFailsClosedOnDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		load     func() (*snapshot.Manifest, error)
		file     string
		payload  []byte
		tampered bool
	}{
		{name: "advisor matches", load: advisorManifest, file: advisorManifestFile, payload: embeddedSpotData},
		{name: "price matches", load: priceManifest, file: priceManifestFile, payload: embeddedPriceData},
		{
			name: "advisor drifted", load: advisorManifest, file: advisorManifestFile,
			payload: append(embeddedSpotData, ' '), tampered: true,
		},
		{
			name: "price drifted", load: priceManifest, file: priceManifestFile,
			payload: append(embeddedPriceData, ' '), tampered: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := verifyEmbeddedPayload(tc.load, tc.file, tc.payload)
			if tc.tampered {
				require.Error(t, err, "a payload that does not hash to its manifest must be refused")
				assert.Contains(t, err.Error(), tc.file)

				return
			}

			require.NoError(t, err)
		})
	}
}

// Every committed AWS payload is verified on the path that loads it, so a drift
// cannot reach a caller.
func TestEmbeddedLoadersVerifyTheirPayloads(t *testing.T) {
	t.Parallel()

	require.NoError(t, advisorPayloadVerified())
	require.NoError(t, pricePayloadVerified())

	_, err := LoadEmbeddedArchitectureLookup()
	require.NoError(t, err)
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
func refreshed(t *testing.T, manifest *snapshot.Manifest, embedded embeddedSnapshot, payload []byte) *snapshot.Manifest {
	t.Helper()

	updated := *manifest
	updated.Payload.SHA256 = snapshot.SHA256Hex(payload)

	// The source hash is the upstream document's, never the payload's, unless
	// the payload literally is that document. For a compressed payload the two
	// differ on purpose: v2 publishes the source hash as provenance a consumer
	// can verify by re-fetching the URL, and an archive's hash would not match.
	switch updated.Payload.Form {
	case snapshot.PayloadFormRawSource:
		updated.Sources = []snapshot.Source{manifest.Sources[0]}
		updated.Sources[0].SHA256 = updated.Payload.SHA256

		if manifest.Payload.SHA256 != updated.Payload.SHA256 {
			updated.Sources[0].FetchedAt = time.Now().UTC().Truncate(time.Second)
		}
	case snapshot.PayloadFormCompressedSource:
		document, err := os.ReadFile(filepath.Join("data", embedded.source))
		require.NoError(t, err)

		updated.Sources = []snapshot.Source{manifest.Sources[0]}
		updated.Sources[0].SHA256 = snapshot.SHA256Hex(document)

		if manifest.Sources[0].SHA256 != updated.Sources[0].SHA256 {
			updated.Sources[0].FetchedAt = time.Now().UTC().Truncate(time.Second)
		}
	case snapshot.PayloadFormParsedCatalog:
	}

	return &updated
}

// writeArchive rebuilds a committed feed archive from the .json beside it.
//
// It uses the same deterministic encoder the GCP and Azure updaters use, so a
// refresh that changes no data produces byte-identical output and does not churn
// the manifest hash. internal/reproducible is build-time only and is reachable
// here because this is test code; the shipped package must never import it.
func writeArchive(source string) error {
	contents, err := os.ReadFile(filepath.Join("data", source))
	if err != nil {
		return err
	}

	archive, err := reproducible.Compress(contents)
	if err != nil {
		return err
	}

	return snapshot.WriteFile(filepath.Join("data", source+".gz"), archive)
}

// The archive the binary embeds must be exactly the .json committed next to it.
//
// Both files are committed on purpose: the .json is what a weekly data-refresh
// pull request is reviewed from, and the .gz is what ships, 3.4 MB smaller. That
// only works while they cannot drift, which is what this checks — a hand-edited
// .json, or an archive rebuilt from data nobody reviewed, fails here.
func TestEmbeddedArchivesMatchTheirJSON(t *testing.T) {
	t.Parallel()

	for _, embedded := range embeddedSnapshots() {
		if embedded.source == "" {
			continue
		}

		t.Run(embedded.source, func(t *testing.T) {
			t.Parallel()

			reviewed, err := os.ReadFile(filepath.Join("data", embedded.source))
			require.NoError(t, err)

			shipped, err := decompressEmbedded(embedded.readPayload(t))
			require.NoError(t, err)

			assert.Equal(t, reviewed, shipped,
				"%s.gz does not decompress to %s; run `make refresh-manifests`",
				embedded.source, embedded.source)
		})
	}
}
