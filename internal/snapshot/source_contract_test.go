package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

func approvedContract(t *testing.T) *SourceContract {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "source-contract-approved.json"))
	require.NoError(t, err)

	contract, err := ParseSourceContract(raw)
	require.NoError(t, err)

	return contract
}

func reparseContract(t *testing.T, contract *SourceContract) error {
	t.Helper()

	raw, err := json.Marshal(contract)
	require.NoError(t, err)

	_, err = ParseSourceContract(raw)

	return err
}

func TestParseSourceContractAcceptsAnApprovedContract(t *testing.T) {
	t.Parallel()

	contract := approvedContract(t)

	assert.Equal(t, cloud.ProviderGCP, contract.Provider)
	assert.Equal(t, cloud.RiskStatusUnavailable, contract.Support.RiskStatus)
	assert.Equal(t, []cloud.OperatingSystem{cloud.OSLinux}, contract.Support.OperatingSystems)
	assert.Len(t, contract.Sources, 2)
	assert.Equal(t, SourceTypeOfficialServerRenderedHTML, contract.Sources[0].SourceType)
	assert.Equal(t, cloud.MoneyScale, contract.Thresholds.MaxFractionalDigits)
}

// The rejected fixture was never approved for redistribution. Nothing may be
// committed from it, and the loader has to say so rather than merely fail.
func TestParseSourceContractRejectsAnUnapprovedContract(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("testdata", "source-contract-rejected.json"))
	require.NoError(t, err)

	_, err = ParseSourceContract(raw)
	require.ErrorIs(t, err, ErrUnapprovedSource)
}

func TestParseSourceContractFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mutate  func(contract *SourceContract)
		wantErr error
		name    string
	}{
		{
			name:    "unknown contract schema version",
			mutate:  func(c *SourceContract) { c.SchemaVersion = "spotinfo.source-contract/v2" },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name: "aws, which predates the contract",
			// AWS embeds raw feeds under its own parser contract in internal/spot.
			mutate:  func(c *SourceContract) { c.Provider = cloud.ProviderAWS },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "review still pending",
			mutate:  func(c *SourceContract) { c.ReviewStatus = "pending" },
			wantErr: ErrUnapprovedSource,
		},
		{
			name:    "redistribution undecided",
			mutate:  func(c *SourceContract) { c.Terms.RedistributionDecision = "undecided" },
			wantErr: ErrUnapprovedSource,
		},
		{
			name:    "no reviewer",
			mutate:  func(c *SourceContract) { c.Reviewer = "  " },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "review date is not a date",
			mutate:  func(c *SourceContract) { c.ReviewedAt = "last tuesday" },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "no parser version",
			mutate:  func(c *SourceContract) { c.ParserVersion = "" },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "unsupported update cadence",
			mutate:  func(c *SourceContract) { c.UpdateCadence = "on demand" },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "no sources",
			mutate:  func(c *SourceContract) { c.Sources = nil },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "plain http source",
			mutate:  func(c *SourceContract) { c.Sources[0].URL = "http://cloud.google.com/spot-vms/pricing" },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name: "an undocumented endpoint",
			// Scraping a pricing calculator or a JavaScript endpoint is exactly
			// what the source_type enum exists to refuse.
			mutate:  func(c *SourceContract) { c.Sources[0].SourceType = "undocumented-json-endpoint" },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "unknown data kind",
			mutate:  func(c *SourceContract) { c.Sources[0].DataKinds = []DataKind{"eviction-history"} },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "repeated data kind",
			mutate:  func(c *SourceContract) { c.Sources[0].DataKinds = []DataKind{DataKindSpotPrice, DataKindSpotPrice} },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "no terms evidence",
			mutate:  func(c *SourceContract) { c.Terms.EvidenceURL = "" },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "no terms notes",
			mutate:  func(c *SourceContract) { c.Terms.Notes = " " },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name: "claiming risk data an offline provider cannot have",
			mutate: func(c *SourceContract) {
				c.Support.RiskStatus = cloud.RiskStatusAvailable
			},
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "unsupported operating system",
			mutate:  func(c *SourceContract) { c.Support.OperatingSystems = []cloud.OperatingSystem{"plan9"} },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "unsupported architecture",
			mutate:  func(c *SourceContract) { c.Support.Architectures = []cloud.Architecture{"riscv"} },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name: "spot prices with no on-demand baseline",
			// A savings percentage without a paired list price is a number with
			// no denominator.
			mutate:  func(c *SourceContract) { c.Support.PriceClasses = []cloud.PriceClass{cloud.PriceClassSpot} },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "no regions",
			mutate:  func(c *SourceContract) { c.Support.Regions = nil },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "repeated region",
			mutate:  func(c *SourceContract) { c.Support.Regions = []cloud.Region{"us-central1", "us-central1"} },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "empty machine series",
			mutate:  func(c *SourceContract) { c.Support.MachineSeries = []string{""} },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "no expected fields",
			mutate:  func(c *SourceContract) { c.ExpectedFields = nil },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "no no-go conditions",
			mutate:  func(c *SourceContract) { c.NoGoConditions = nil },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "no region threshold",
			mutate:  func(c *SourceContract) { c.Thresholds.MinRegions = 0 },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name:    "no size limit",
			mutate:  func(c *SourceContract) { c.Thresholds.MaxCompressedBytes = 0 },
			wantErr: ErrInvalidSourceContract,
		},
		{
			name: "precision beyond the fixed-point scale",
			// A source needing more digits must raise cloud.MoneyScale
			// deliberately, not round silently at the parser.
			mutate:  func(c *SourceContract) { c.Thresholds.MaxFractionalDigits = cloud.MoneyScale + 1 },
			wantErr: ErrInvalidSourceContract,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			contract := approvedContract(t)
			tc.mutate(contract)

			require.ErrorIs(t, reparseContract(t, contract), tc.wantErr)
		})
	}
}

func TestParseSourceContractRejectsRenamedKeys(t *testing.T) {
	t.Parallel()

	renamed, err := json.Marshal(map[string]any{"schemaVersion": SourceContractSchemaVersion})
	require.NoError(t, err)

	_, err = ParseSourceContract(renamed)
	require.ErrorIs(t, err, ErrInvalidSourceContract)
}

func TestVerifySnapshotEnforcesTheContract(t *testing.T) {
	t.Parallel()

	contract := approvedContract(t)

	consistent := func(t *testing.T) *Manifest {
		t.Helper()

		manifest := validManifest(t)
		manifest.ParserVersion = contract.ParserVersion
		manifest.MinRecords = Coverage{
			Regions:  contract.Thresholds.MinRegions,
			Machines: contract.Thresholds.MinMachines,
			Prices:   1,
		}

		return manifest
	}

	require.NoError(t, contract.VerifySnapshot(consistent(t), contract.Thresholds.MaxCompressedBytes))

	tests := []struct {
		mutate func(manifest *Manifest)
		name   string
		size   int
	}{
		{
			name:   "another provider's snapshot",
			mutate: func(m *Manifest) { m.Provider = cloud.ProviderAzure },
		},
		{
			name:   "a parser the review never saw",
			mutate: func(m *Manifest) { m.ParserVersion = "gcp-spot-html/2" },
		},
		{
			name:   "a floor below the contracted region minimum",
			mutate: func(m *Manifest) { m.MinRecords.Regions-- },
		},
		{
			name:   "a floor below the contracted machine minimum",
			mutate: func(m *Manifest) { m.MinRecords.Machines-- },
		},
		{
			name:   "a snapshot larger than the reviewed limit",
			mutate: func(*Manifest) {},
			size:   contract.Thresholds.MaxCompressedBytes + 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			manifest := consistent(t)
			tc.mutate(manifest)

			size := tc.size
			if size == 0 {
				size = contract.Thresholds.MaxCompressedBytes
			}

			require.ErrorIs(t, contract.VerifySnapshot(manifest, size), ErrContractMismatch)
		})
	}
}
