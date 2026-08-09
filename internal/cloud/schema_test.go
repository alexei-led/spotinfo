package cloud

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The wire enum of risk kinds is frozen by
// docs/plans/contracts/recommend-spot-instances-v2-success.schema.json. This
// scans the package for every declared RiskKind constant, so a kind added for a
// new provider fails here instead of shipping a value the schema forbids.
func TestEveryDeclaredRiskKindHasAPublishedName(t *testing.T) {
	t.Parallel()

	declared := declaredRiskKinds(t)
	require.Contains(t, declared, string(RiskKindInterruptionFrequencyRange),
		"the source scan must find the kinds it is meant to police")

	for _, kind := range declared {
		wire, mapped := riskKindWireNames[RiskKind(kind)]
		assert.True(t, mapped, "risk kind %q has no published name", kind)
		assert.Contains(t, publishedRiskKindEnum(), wire, "published name %q is outside the frozen enum", wire)
	}
}

// publishedRiskKindEnum is the enum the success schema allows, transcribed from
// the contract file. A change here must be a deliberate schema change.
func publishedRiskKindEnum() []string {
	return []string{"interruption_bucket", "preemption_rate", "eviction_rate"}
}

// declaredRiskKinds returns the value of every `X RiskKind = "..."` constant in
// this package.
func declaredRiskKinds(t *testing.T) []string {
	t.Helper()

	sources, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, sources)

	var kinds []string
	for _, source := range sources {
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), source, nil, 0)
		require.NoError(t, parseErr)

		ast.Inspect(parsed, func(node ast.Node) bool {
			spec, ok := node.(*ast.ValueSpec)
			if !ok {
				return true
			}
			if name, isIdent := spec.Type.(*ast.Ident); !isIdent || name.Name != "RiskKind" {
				return true
			}
			for _, value := range spec.Values {
				literal, isLiteral := value.(*ast.BasicLit)
				if !isLiteral || literal.Kind != token.STRING {
					continue
				}
				unquoted, unquoteErr := strconv.Unquote(literal.Value)
				require.NoError(t, unquoteErr)
				kinds = append(kinds, unquoted)
			}

			return true
		})
	}

	return kinds
}

// An unmapped kind fails closed rather than publishing a Go constant.
func TestAnUnmappedRiskKindFailsClosed(t *testing.T) {
	t.Parallel()

	_, err := riskDTO(&RiskObservation{Status: RiskStatusAvailable, Kind: "coin_flip"})
	require.Error(t, err)
	assert.Equal(t, CodeInternal, CodeOf(err))
}

// Absent measurements serialise as null, never as zero.
func TestRiskDTOPublishesAbsenceAsNull(t *testing.T) {
	t.Parallel()

	published, err := riskDTO(&RiskObservation{Status: RiskStatusUnavailable})
	require.NoError(t, err)

	encoded, err := json.Marshal(published)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"status": "unavailable",
		"kind": null,
		"label": null,
		"min_percent": null,
		"max_percent": null,
		"window_days": null,
		"source_url": null,
		"observed_at": null
	}`, string(encoded))
}

// This is the last gate before a payload reaches a client, and the one place a
// cloud's silence could still be published as a number. An observation carrying
// a stale figure under a non-available status must publish the status alone.
func TestRiskDTOPublishesNoFigureWhenRiskIsNotAvailable(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.August, 1, 10, 30, 0, 0, time.UTC)
	low, high := 0.0, 5.0

	published, err := riskDTO(&RiskObservation{
		ObservedAt: &observedAt,
		MinPercent: &low,
		MaxPercent: &high,
		Window:     &HistoryWindow{Days: 30},
		Status:     RiskStatusUnavailable,
		Kind:       RiskKindInterruptionFrequencyRange,
		Label:      "<5%",
		SourceURL:  "https://example.invalid/advisor.json",
	})
	require.NoError(t, err)

	encoded, err := json.Marshal(published)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"status": "unavailable",
		"kind": null,
		"label": null,
		"min_percent": null,
		"max_percent": null,
		"window_days": null,
		"source_url": null,
		"observed_at": null
	}`, string(encoded))
}

func TestRiskDTOPublishesEveryAvailableField(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, time.August, 1, 10, 30, 0, 0, time.UTC)
	low, high := 5.0, 10.0

	published, err := riskDTO(&RiskObservation{
		ObservedAt: &observedAt,
		MinPercent: &low,
		MaxPercent: &high,
		Window:     &HistoryWindow{Days: 30},
		Status:     RiskStatusAvailable,
		Kind:       RiskKindInterruptionFrequencyRange,
		Label:      "5-10%",
		SourceURL:  "https://example.invalid/advisor.json",
	})
	require.NoError(t, err)

	encoded, err := json.Marshal(published)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"status": "available",
		"kind": "interruption_bucket",
		"label": "5-10%",
		"min_percent": 5,
		"max_percent": 10,
		"window_days": 30,
		"source_url": "https://example.invalid/advisor.json",
		"observed_at": "2026-08-01T10:30:00Z"
	}`, string(encoded))
}

// A provider that cannot say where its data came from cannot serve a v2 answer.
func TestIncompleteProvenanceFailsClosed(t *testing.T) {
	t.Parallel()

	complete := testSources()[0]

	for _, test := range []struct {
		name    string
		sources []SourceRef
	}{
		{name: "no sources", sources: nil},
		{name: "missing url", sources: []SourceRef{withoutURL(complete)}},
		{name: "missing hash", sources: []SourceRef{withoutHash(complete)}},
		{name: "missing fetch time", sources: []SourceRef{withoutFetchTime(complete)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := sourceDTOs(&Result{Provider: ProviderAWS, Sources: test.sources})
			require.ErrorIs(t, err, ErrDataUnavailable)
			assert.Equal(t, CodeDataUnavailable, CodeOf(err))
		})
	}
}

func withoutURL(source SourceRef) SourceRef       { source.URL = ""; return source }
func withoutHash(source SourceRef) SourceRef      { source.ContentSHA256 = ""; return source }
func withoutFetchTime(source SourceRef) SourceRef { source.FetchedAt = time.Time{}; return source }

// The error payload names the cloud the caller asked for, or null when none
// could be parsed.
func TestErrorReportPublishesTheRequestedCloudOrNull(t *testing.T) {
	t.Parallel()

	named, err := json.Marshal(NewErrorReport(CodeInvalidArgument, "bad input", "gcp"))
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"schema_version": "spotinfo.error/v1",
		"code": "INVALID_ARGUMENT",
		"message": "bad input",
		"cloud": "gcp"
	}`, string(named))

	anonymous, err := json.Marshal(NewErrorReport(CodeInternal, "boom", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"schema_version": "spotinfo.error/v1",
		"code": "INTERNAL",
		"message": "boom",
		"cloud": null
	}`, string(anonymous))
}

// The published schema bounds savings to 0..100. A provider that maps a figure
// outside it fails closed rather than handing a client a payload its own schema
// rejects.
func TestSavingsOutsideThePublishedRangeFailsClosed(t *testing.T) {
	t.Parallel()

	for _, savings := range []int{-1, 101, 1000} {
		t.Run(strconv.Itoa(savings), func(t *testing.T) {
			t.Parallel()

			candidate := fixture{Region: "us-east-1", Machine: "m5.large", Price: "0.020", VCPU: 2, MemoryGiB: 4}.build()
			candidate.SavingsPercent = &savings

			_, err := Recommend(t.Context(), riskFreeProvider(candidate), gcpCostRequest())
			require.Error(t, err)
			assert.Equal(t, CodeInternal, CodeOf(err))
			assert.Contains(t, err.Error(), "not a percentage of on-demand")
		})
	}
}

func TestSavingsOnThePublishedBoundsArePublished(t *testing.T) {
	t.Parallel()

	for _, savings := range []int{0, 100} {
		t.Run(strconv.Itoa(savings), func(t *testing.T) {
			t.Parallel()

			candidate := fixture{Region: "us-east-1", Machine: "m5.large", Price: "0.020", VCPU: 2, MemoryGiB: 4}.build()
			candidate.SavingsPercent = &savings

			report, err := Recommend(t.Context(), riskFreeProvider(candidate), gcpCostRequest())
			require.NoError(t, err)
			require.Len(t, report.Recommendations, 1)
			require.NotNil(t, report.Recommendations[0].SavingsPercent)
			assert.InDelta(t, float64(savings), *report.Recommendations[0].SavingsPercent, 0)
		})
	}
}

// gcpCostRequest is a valid cost-policy request against the risk-free provider.
func gcpCostRequest() *RecommendRequest {
	request := baseRequest()
	request.Cloud = ProviderGCP

	return request
}
