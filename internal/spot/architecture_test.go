package spot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadEmbeddedArchitectureLookup_ClassifiesAdvisorFamiliesAndReviewedRegressions(t *testing.T) {
	lookup, err := LoadEmbeddedArchitectureLookup()
	require.NoError(t, err)

	advisor, err := loadEmbeddedAdvisorData()
	require.NoError(t, err)
	for instance := range advisor.InstanceTypes {
		_, ok := lookup.ArchitectureForInstance(instance)
		assert.Truef(t, ok, "instance %q must have a reviewed family architecture", instance)
	}

	for _, family := range []string{"g6", "g6e", "g6f", "hpc8a", "m6i"} {
		architecture, ok := lookup.ArchitectureForInstance(family + ".large")
		assert.True(t, ok)
		assert.Equalf(t, ArchitectureX8664, architecture, "%s is x86_64", family)
	}
	for _, family := range []string{"a1", "m6g", "c7g", "g5g", "hpc7g"} {
		architecture, ok := lookup.ArchitectureForInstance(family + ".large")
		assert.True(t, ok)
		assert.Equalf(t, ArchitectureARM64, architecture, "%s is arm64", family)
	}
	_, ok := lookup.ArchitectureForInstance("not-reviewed.large")
	assert.False(t, ok)
}

func TestParseArchitectureSnapshotRejectsInvalidData(t *testing.T) {
	validMetadata := `"provenance":"reviewed manually","reviewed_at":"2026-08-06",`

	_, err := parseArchitectureSnapshot([]byte(`{` + validMetadata + `"families":{"m5":"riscv"}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid architecture")

	_, err = parseArchitectureSnapshot([]byte(`{` + validMetadata + `"families":{}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no families")

	for _, contents := range [][]byte{
		[]byte(`{"reviewed_at":"2026-08-06","families":{"m5":"x86_64"}}`),
		[]byte(`{"provenance":"reviewed manually","families":{"m5":"x86_64"}}`),
		[]byte(`{"provenance":"reviewed manually","reviewed_at":"not-a-date","families":{"m5":"x86_64"}}`),
	} {
		_, err = parseArchitectureSnapshot(contents)
		require.Error(t, err)
	}
}
