package cloud

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProviderID(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		input string
		want  ProviderID
		valid bool
	}{
		{input: "aws", want: ProviderAWS, valid: true},
		{input: "GCP", want: ProviderGCP, valid: true},
		{input: " azure ", want: ProviderAzure, valid: true},
		{input: "oracle"},
		{input: ""},
		{input: "aws,gcp"},
	} {
		t.Run(test.input, func(t *testing.T) {
			t.Parallel()

			provider, err := ParseProviderID(test.input)
			if !test.valid {
				require.ErrorIs(t, err, ErrInvalidArgument)
				assert.Empty(t, provider, "an unknown provider must not fall back to another cloud")
				// A misspelled cloud is the one argument error whose reader may
				// not know the alternatives, so the refusal has to list them.
				for _, id := range ProviderIDs() {
					assert.Contains(t, err.Error(), string(id))
				}

				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.want, provider)
		})
	}
}

func TestProviderIDsAreStableAndLexical(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []ProviderID{ProviderAWS, ProviderAzure, ProviderGCP}, ProviderIDs())
}

func TestParseArchitectureAndOperatingSystem(t *testing.T) {
	t.Parallel()

	architecture, err := ParseArchitecture("arm64")
	require.NoError(t, err)
	assert.Equal(t, ArchitectureARM64, architecture)

	_, err = ParseArchitecture("i386")
	require.ErrorIs(t, err, ErrInvalidArgument)

	instanceOS, err := ParseOperatingSystem("Linux")
	require.NoError(t, err)
	assert.Equal(t, OSLinux, instanceOS)

	_, err = ParseOperatingSystem("freebsd")
	require.ErrorIs(t, err, ErrInvalidArgument)
}

// All three vocabulary parsers fold case. One request carries a cloud, an OS and
// an architecture, so accepting "AWS" and "LINUX" while rejecting "X86_64" is an
// asymmetry no caller can predict.
func TestVocabularyParsersFoldCaseConsistently(t *testing.T) {
	t.Parallel()

	provider, err := ParseProviderID("AWS")
	require.NoError(t, err)
	assert.Equal(t, ProviderAWS, provider)

	instanceOS, err := ParseOperatingSystem("LINUX")
	require.NoError(t, err)
	assert.Equal(t, OSLinux, instanceOS)

	for input, want := range map[string]Architecture{
		"X86_64": ArchitectureX8664,
		"x86_64": ArchitectureX8664,
		"ARM64":  ArchitectureARM64,
		"Arm64":  ArchitectureARM64,
		" arm64": ArchitectureARM64,
	} {
		architecture, err := ParseArchitecture(input)
		require.NoErrorf(t, err, "architecture %q must parse like its lowercase form", input)
		assert.Equal(t, want, architecture)
	}
}

func TestCapabilitiesGateUnsupportedRequests(t *testing.T) {
	t.Parallel()

	linuxOnly := Capabilities{
		OperatingSystems: []OperatingSystem{OSLinux},
		Architectures:    []Architecture{ArchitectureX8664},
	}

	assert.True(t, linuxOnly.SupportsOS(OSLinux))
	assert.False(t, linuxOnly.SupportsOS(OSWindows))
	assert.True(t, linuxOnly.SupportsArchitecture(ArchitectureX8664))
	assert.False(t, linuxOnly.SupportsArchitecture(ArchitectureARM64))
	assert.False(t, Capabilities{}.SupportsOS(OSLinux), "a provider declaring nothing supports nothing")
}

func TestUnavailableRiskIsExplicit(t *testing.T) {
	t.Parallel()

	risk := UnavailableRisk()
	assert.Equal(t, RiskStatusUnavailable, risk.Status)
	assert.Empty(t, risk.Kind)
	assert.Nil(t, risk.MinPercent)
	assert.Nil(t, risk.MaxPercent)
	assert.Nil(t, risk.Window)
}
