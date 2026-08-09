package azure

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

func TestSizePageYieldsSpecificationsAndArchitecture(t *testing.T) {
	t.Parallel()

	spec, err := ParseSeriesPage(bytes.NewReader(readFixture(t, "vm-sizes.html")), "dpsv5")
	require.NoError(t, err)

	assert.Equal(t, "dpsv5", spec.Series)
	assert.Equal(t, cloud.ArchitectureARM64, spec.Architecture,
		"the page writes [ARM-64]; the marker spelling varies and must be normalised")

	require.Len(t, spec.Sizes, 3, "only the specification table is read, not the storage table")
	assert.Equal(t, SizeSpec{ID: "Standard_D2ps_v5", VCPU: 2, MemoryGiB: 8}, spec.Sizes[0])
	assert.Equal(t, SizeSpec{ID: "Standard_D8ps_v5", VCPU: 8, MemoryGiB: 32}, spec.Sizes[2])
}

// TestArchitectureMarkerSpellingsAreNormalised pins every spelling Microsoft
// publishes today. Getting this wrong ships an Arm size labelled x86_64, which
// passes every other gate in this repository.
func TestArchitectureMarkerSpellingsAreNormalised(t *testing.T) {
	t.Parallel()

	for marker, want := range map[string]cloud.Architecture{
		"[x86-64]": cloud.ArchitectureX8664,
		"[x86_64]": cloud.ArchitectureX8664,
		"[Arm64]":  cloud.ArchitectureARM64,
		"[ARM-64]": cloud.ArchitectureARM64,
		"[arm64]":  cloud.ArchitectureARM64,
	} {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()

			spec, err := ParseSeriesPage(strings.NewReader(sizePage(marker)), "dsv5")
			require.NoError(t, err)
			assert.Equal(t, want, spec.Architecture)
		})
	}
}

func TestAPageWithoutAnArchitectureMarkerFails(t *testing.T) {
	t.Parallel()

	_, err := ParseSeriesPage(strings.NewReader(sizePage("")), "dsv5")

	require.ErrorIs(t, err, ErrSourceContract)
	assert.Contains(t, err.Error(), "no processor architecture marker",
		"an unmarked page must fail rather than default to x86_64")
}

func TestAPageThatMixesArchitecturesFails(t *testing.T) {
	t.Parallel()

	_, err := ParseSeriesPage(strings.NewReader(sizePage("[x86-64] and [Arm64]")), "dsv5")

	require.ErrorIs(t, err, ErrSourceContract)
	assert.Contains(t, err.Error(), "mixes")
}

// TestBothMemoryLabelsAreRead covers the split across the contracted pages: the
// memory-optimized pages write "Memory (GB)" for the same gibibyte figure every
// other page writes as "Memory (GiB)".
func TestBothMemoryLabelsAreRead(t *testing.T) {
	t.Parallel()

	for _, label := range []string{headerMemoryGiB, headerMemoryGB} {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			page := strings.Replace(sizePage("[x86-64]"), headerMemoryGiB, label, 1)

			spec, err := ParseSeriesPage(strings.NewReader(page), "esv5")
			require.NoError(t, err)
			require.Len(t, spec.Sizes, 1)
			assert.InDelta(t, 16.0, spec.Sizes[0].MemoryGiB, 0)
		})
	}
}

func TestARenamedHeaderEmptiesTheParse(t *testing.T) {
	t.Parallel()

	page := strings.Replace(sizePage("[x86-64]"), headerVCPU, "Cores (Qty.)", 1)

	_, err := ParseSeriesPage(strings.NewReader(page), "dsv5")

	require.ErrorIs(t, err, ErrSourceContract)
	assert.Contains(t, err.Error(), "no size table")
}

func TestUnreadableSpecificationCellsFail(t *testing.T) {
	t.Parallel()

	for name, replacement := range map[string]string{
		"vcpu":   "<td>Standard_E2s_v5</td><td>two</td><td>16</td>",
		"memory": "<td>Standard_E2s_v5</td><td>2</td><td>lots</td>",
		"zero":   "<td>Standard_E2s_v5</td><td>0</td><td>16</td>",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			page := strings.Replace(sizePage("[x86-64]"),
				"<td>Standard_E2s_v5</td><td>2</td><td>16</td>", replacement, 1)

			_, err := ParseSeriesPage(strings.NewReader(page), "esv5")
			require.ErrorIs(t, err, ErrSourceContract)
		})
	}
}

func TestSeriesFromURL(t *testing.T) {
	t.Parallel()

	for target, want := range map[string]string{
		"https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/general-purpose/dv5-series":    "dv5",
		"https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/memory-optimized/epsv5-series": "epsv5",
		"https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/general-purpose/dpsv6-series/": "dpsv6",
	} {
		series, err := SeriesFromURL(target)
		require.NoError(t, err)
		assert.Equal(t, want, series)
	}

	for _, target := range []string{
		"https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/overview",
		"https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/general-purpose/Dv5-Series",
	} {
		_, err := SeriesFromURL(target)
		assert.ErrorIs(t, err, ErrSourceContract, target)
	}
}

// sizePage renders a minimal page in the shape the contracted source publishes.
// marker is placed in the processor row; an empty marker omits it entirely.
func sizePage(marker string) string {
	return `<!doctype html><html><body>
<table><thead><tr><th>Part</th><th>Quantity Count Units</th><th>Specs</th></tr></thead>
<tbody><tr><td>Processor</td><td>2 - 96 vCPUs</td><td>Some Processor ` + marker + `</td></tr></tbody></table>
<table><thead><tr><th>` + headerSizeName + `</th><th>` + headerVCPU + `</th><th>` + headerMemoryGiB + `</th></tr></thead>
<tbody><tr><td>Standard_E2s_v5</td><td>2</td><td>16</td></tr></tbody></table>
</body></html>`
}
