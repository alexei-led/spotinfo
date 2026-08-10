package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

const contractedRegion = cloud.Region("us-central1")

// An unreviewed series has no architecture. Defaulting it to x86_64 is how a new
// Arm series ships silently mislabelled, so the classification is total over the
// contracted series and anything else fails.
func TestAnUnreviewedSeriesHasNoArchitecture(t *testing.T) {
	t.Parallel()

	_, classified := ArchitectureOf("z9a-standard-2")
	assert.False(t, classified)
}

func TestEveryContractedSeriesIsClassified(t *testing.T) {
	t.Parallel()

	loaded, err := LoadEmbeddedSnapshot()
	require.NoError(t, err)

	contract := loaded.Contract
	require.NotEmpty(t, contract.Support.MachineSeries)

	for _, series := range contract.Support.MachineSeries {
		architecture, classified := ArchitectureOf(cloud.MachineID(series + "-standard-2"))
		assert.True(t, classified, "series %q is approved by the contract but has no reviewed architecture", series)
		assert.Contains(t, contract.Support.Architectures, architecture, series)
	}
}

func TestSeriesOfSplitsOnTheFirstSegment(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "c3d", SeriesOf("c3d-highmem-8"))
	assert.Equal(t, "e2", SeriesOf("e2-micro"))
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
		got, classified := ArchitectureOf(machine)
		assert.True(t, classified, machine)
		assert.Equal(t, want, got, machine)
	}
}
