package gcp

import (
	"os"
	"path/filepath"
	"strings"
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

// TestAnUnreadableCellFailsTheParse pins the fail-closed half of the row rule.
// A row that is not a machine is skipped, but a row that is one and cannot be
// read must stop the refresh: widening the parser to accept it would publish a
// specification or a price the source never stated.
func TestAnUnreadableCellFailsTheParse(t *testing.T) {
	t.Parallel()

	for name, cells := range map[string]string{
		"vcpu is not a number": `<td>c4-standard-2</td><td>two</td><td>7 GiB</td><td>$0.058121 / 1 hour</td>`,
		"vcpu is zero":         `<td>c4-standard-2</td><td>0</td><td>7 GiB</td><td>$0.058121 / 1 hour</td>`,
		// ParseFloat accepts both of these, and neither is negative. Without an
		// explicit finiteness check they reach the whole-core test and leave the
		// catalogue silently, as if the machine shared a core.
		"vcpu is NaN":          `<td>c4-standard-2</td><td>NaN</td><td>7 GiB</td><td>$0.058121 / 1 hour</td>`,
		"vcpu is infinite":     `<td>c4-standard-2</td><td>+Inf</td><td>7 GiB</td><td>$0.058121 / 1 hour</td>`,
		"memory has no unit":   `<td>c4-standard-2</td><td>2</td><td>lots</td><td>$0.058121 / 1 hour</td>`,
		"memory is zero":       `<td>c4-standard-2</td><td>2</td><td>0 GiB</td><td>$0.058121 / 1 hour</td>`,
		"price is monthly":     `<td>c4-standard-2</td><td>2</td><td>7 GiB</td><td>$42.43 / 1 month</td>`,
		"price is too precise": `<td>c4-standard-2</td><td>2</td><td>7 GiB</td><td>$0.0581210001 / 1 hour</td>`,
		"price is zero":        `<td>c4-standard-2</td><td>2</td><td>7 GiB</td><td>$0 / 1 hour</td>`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseSpotPage(strings.NewReader(spotPage(cells)), contractedRegion)
			require.ErrorIs(t, err, ErrSourceContract)
		})
	}
}

func TestTheSamePageShapeParsesWhenEveryCellIsReadable(t *testing.T) {
	t.Parallel()

	rows, err := ParseSpotPage(strings.NewReader(spotPage(readableCells)), contractedRegion)
	require.NoError(t, err)

	require.Len(t, rows, 1)
	assert.Equal(t, MachineRow{ID: "c4-standard-2", VCPU: 2, MemoryGiB: 7, Price: rows[0].Price}, rows[0])
	assert.Equal(t, "0.058121000", rows[0].Price.String())
}

// readableCells is the row every case above mutates one cell of.
const readableCells = `<td>c4-standard-2</td><td>2</td><td>7 GiB</td><td>$0.058121 / 1 hour</td>`

// spotPage renders a minimal Spot page in the shape the contracted source
// publishes. The region selector precedes the table because that is what
// attributes the table to a region.
func spotPage(cells string) string {
	return `<!doctype html><html><body>
<div role="option" aria-selected="true" data-value="Iowa (us-central1)"></div>
<table><thead><tr><th>` + headerMachineType + `</th><th>` + headerVCPU + `</th>` +
		`<th>` + headerMemory + `</th><th>` + headerSpotPrice + `</th></tr></thead>
<tbody><tr>` + cells + `</tr></tbody></table>
</body></html>`
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
