package gcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

const contractedRegion = cloud.Region("us-central1")

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()

	file, err := os.Open(filepath.Join("testdata", name))
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })

	return file
}

func rowsByID(rows []MachineRow) map[cloud.MachineID]MachineRow {
	indexed := make(map[cloud.MachineID]MachineRow, len(rows))
	for _, row := range rows {
		indexed[row.ID] = row
	}

	return indexed
}

func TestParseSpotPageReadsTheContractedColumns(t *testing.T) {
	t.Parallel()

	rows, err := ParseSpotPage(openFixture(t, "spot-pricing.html"), contractedRegion)
	require.NoError(t, err)

	indexed := rowsByID(rows)
	require.Contains(t, indexed, cloud.MachineID("c4-standard-2"))

	assert.Equal(t, 2, indexed["c4-standard-2"].VCPU)
	assert.InDelta(t, 7.0, indexed["c4-standard-2"].MemoryGiB, 0)
	assert.Equal(t, "0.058121000", indexed["c4-standard-2"].Price.String())
	assert.InDelta(t, 1.8, indexed["n1-highcpu-2"].MemoryGiB, 0)
}

func TestParseSpotPageSkipsRowsThatAreNotRequestableMachines(t *testing.T) {
	t.Parallel()

	rows, err := ParseSpotPage(openFixture(t, "spot-pricing.html"), contractedRegion)
	require.NoError(t, err)

	indexed := rowsByID(rows)
	assert.NotContains(t, indexed, cloud.MachineID("f1-micro"),
		"a fraction of a vCPU has no whole-core count")
	assert.NotContains(t, indexed, cloud.MachineID("n1-standard-96 Skylake Platform only"),
		"a platform annotation is not a machine type")
	assert.NotContains(t, indexed, cloud.MachineID("Local SSD"),
		"a table without the contracted header is not a machine table")
}

func TestParseSpotPageIgnoresTablesRenderedForAnotherRegion(t *testing.T) {
	t.Parallel()

	rows, err := ParseSpotPage(openFixture(t, "spot-pricing.html"), contractedRegion)
	require.NoError(t, err)

	assert.NotContains(t, rowsByID(rows), cloud.MachineID("c4-elsewhere-8"),
		"the africa-south1 table must not be relabelled as us-central1")

	elsewhere, err := ParseSpotPage(openFixture(t, "spot-pricing.html"), cloud.Region("africa-south1"))
	require.NoError(t, err)
	assert.Contains(t, rowsByID(elsewhere), cloud.MachineID("c4-elsewhere-8"),
		"the same table is readable when that region is the one asked for")
}

func TestParseOnDemandPageReadsTheDefaultColumnOnly(t *testing.T) {
	t.Parallel()

	rows, err := ParseOnDemandPage(openFixture(t, "on-demand-pricing.html"), contractedRegion)
	require.NoError(t, err)

	indexed := rowsByID(rows)
	assert.Equal(t, "0.117660000", indexed["c4-standard-2"].Price.String(),
		"committed-use columns price a contract, not an hour")
}

func TestParseRejectsAPageThatNoLongerMatchesItsContract(t *testing.T) {
	t.Parallel()

	for name, fixture := range map[string]string{
		"renamed price header": "schema-changed.html",
		"no region selector":   "no-region-selector.html",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseSpotPage(openFixture(t, fixture), contractedRegion)
			require.ErrorIs(t, err, ErrSourceContract)
		})
	}
}

func TestParseRejectsARegionThePageDidNotRender(t *testing.T) {
	t.Parallel()

	_, err := ParseSpotPage(openFixture(t, "spot-pricing.html"), cloud.Region("europe-west3"))
	require.ErrorIs(t, err, ErrSourceContract)
}

func TestArchitectureComesFromTheReviewedSeriesList(t *testing.T) {
	t.Parallel()

	for machine, want := range map[cloud.MachineID]cloud.Architecture{
		"c4a-standard-4":  cloud.ArchitectureARM64,
		"n4a-standard-2":  cloud.ArchitectureARM64,
		"t2a-standard-8":  cloud.ArchitectureARM64,
		"c4-standard-2":   cloud.ArchitectureX8664,
		"n2d-standard-16": cloud.ArchitectureX8664,
		"m3-ultramem-32":  cloud.ArchitectureX8664,
	} {
		assert.Equal(t, want, ArchitectureOf(machine), machine)
	}
}

func TestSeriesOfSplitsOnTheFirstSegment(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "c3d", SeriesOf("c3d-highmem-8"))
	assert.Equal(t, "e2", SeriesOf("e2-micro"))
}
