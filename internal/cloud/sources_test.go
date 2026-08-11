package cloud

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// data_source.sources publishes the provenance of the rows an answer contains,
// not of every document the snapshot was built from. Azure reads 81, so a
// three-row answer that listed all of them would be mostly provenance for rows
// it does not have.
//
// The trimming may only ever narrow: Invariant 7 says a published value must
// keep the provenance a consumer can re-fetch and hash. The tests below pin
// both directions — nothing retained that backs no row, and no row published
// without a source that backs it.

func scopedSource(url string, scope SourceScope) SourceRef {
	return SourceRef{
		FetchedAt:     time.Date(2026, time.August, 6, 8, 58, 27, 0, time.UTC),
		URL:           url,
		ContentSHA256: "f42df66cd52c9dc3ac28b6bb7e525627696eec60692d5cf56658c679f0012393",
		ParserVersion: "test-parser/1",
		SchemaVersion: "test-feed/v1",
		Scope:         scope,
	}
}

// azureShapedResult is the composition the trimming exists for: one source per
// region, one per machine series, and one whose URL shape carries no scope.
func azureShapedResult() Result {
	answered := fixture{Region: "eastus", Machine: "Standard_D2s_v5", Price: "0.020", VCPU: 2, MemoryGiB: 8}.build()
	answered.Provider = ProviderAzure

	return Result{
		Provider: ProviderAzure,
		Mode:     DataModeEmbeddedSnapshot,
		Sources: []SourceRef{
			scopedSource("https://example.invalid/prices?region=eastus", SourceScope{Region: "eastus"}),
			scopedSource("https://example.invalid/prices?region=westeurope", SourceScope{Region: "westeurope"}),
			scopedSource("https://example.invalid/sizes/dsv5", SourceScope{Machines: []MachineID{"Standard_D2s_v5"}}),
			scopedSource("https://example.invalid/sizes/esv5", SourceScope{Machines: []MachineID{"Standard_E2s_v5"}}),
			scopedSource("https://example.invalid/whatever-this-is", SourceScope{}),
		},
		Candidates: []Candidate{answered},
	}
}

func listQueryFor(regions ...Region) *Query {
	return &Query{Regions: regions, OS: OSLinux}
}

func TestPublishedSourcesAreTrimmedPerScope(t *testing.T) {
	t.Parallel()

	result := azureShapedResult()

	report, err := NewListReport(listQueryFor("eastus"), &result)
	require.NoError(t, err)

	published := make([]string, 0, len(report.DataSource.Sources))
	for _, source := range report.DataSource.Sources {
		published = append(published, source.URL)
	}

	assert.Equal(t, []string{
		"https://example.invalid/prices?region=eastus",
		"https://example.invalid/sizes/dsv5",
		"https://example.invalid/whatever-this-is",
	}, published, "the answer's own region, its own machine series, and the unscoped source")
	assert.Equal(t, 2, report.DataSource.SourcesOmitted,
		"the other region and the other series were read and are not published")
}

// Invariant 7, the direction that matters: a published row must keep the
// provenance of the values it carries.
func TestEveryPublishedCandidateKeepsASourceThatBacksIt(t *testing.T) {
	t.Parallel()

	result := azureShapedResult()
	other := fixture{Region: "westeurope", Machine: "Standard_E2s_v5", Price: "0.030", VCPU: 2, MemoryGiB: 16}.build()
	other.Provider = ProviderAzure
	result.Candidates = append(result.Candidates, other)

	report, err := NewListReport(listQueryFor(RegionAll), &result)
	require.NoError(t, err)
	require.Len(t, report.Candidates, 2)

	for i := range result.Candidates {
		candidate := &result.Candidates[i]

		backed := false
		for _, published := range report.DataSource.Sources {
			if scopeOf(t, &result, published.URL).Covers(candidate) {
				backed = true

				break
			}
		}
		assert.Truef(t, backed, "%s in %s was published with no source that covers it",
			candidate.Machine.ID, candidate.Location.Region)
	}
}

// The other direction: nothing is published that backs no row, or the trimming
// has not happened at all.
func TestEveryRetainedSourceBacksAPublishedCandidate(t *testing.T) {
	t.Parallel()

	result := azureShapedResult()

	report, err := NewListReport(listQueryFor("eastus"), &result)
	require.NoError(t, err)

	for _, published := range report.DataSource.Sources {
		covered := false
		for i := range result.Candidates {
			if scopeOf(t, &result, published.URL).Covers(&result.Candidates[i]) {
				covered = true

				break
			}
		}
		assert.Truef(t, covered, "%s is published but backs no row in the answer", published.URL)
	}
}

// An answer with no rows has nothing to trim against, so it publishes every
// source rather than none: "which of these describes a row" has no answer when
// there are no rows, and an empty list would fail the schema's minItems.
func TestAnEmptyAnswerPublishesEverySource(t *testing.T) {
	t.Parallel()

	result := azureShapedResult()
	result.Candidates = nil

	report, err := NewListReport(listQueryFor("eastus"), &result)
	require.NoError(t, err)
	assert.Len(t, report.DataSource.Sources, 5)
	assert.Zero(t, report.DataSource.SourcesOmitted)
	assert.NotNil(t, report.Candidates, "an empty answer is [], not null")
}

// A recommendation trims against the ranked page, not against everything the
// provider matched. A --region all query matches every region the snapshot
// covers and publishes three rows; provenance for the rest describes no value
// in the document.
func TestARecommendationTrimsAgainstTheRankedPage(t *testing.T) {
	t.Parallel()

	cheapest := fixture{Region: "eastus", Machine: "Standard_D2s_v5", Price: "0.020", VCPU: 2, MemoryGiB: 8}.build()
	dearer := fixture{Region: "westeurope", Machine: "Standard_E2s_v5", Price: "0.030", VCPU: 2, MemoryGiB: 16}.build()

	provider := &fakeProvider{
		id: ProviderAzure,
		capabilities: Capabilities{
			OperatingSystems: []OperatingSystem{OSLinux},
			Architectures:    []Architecture{ArchitectureX8664},
			SpotPrice:        true,
			MachineSpec:      true,
		},
		sources:    azureShapedResult().Sources,
		candidates: []Candidate{cheapest, dearer},
	}
	for i := range provider.candidates {
		provider.candidates[i].Provider = ProviderAzure
	}

	request := &RecommendRequest{
		Cloud: ProviderAzure, Architecture: ArchitectureX8664, OS: OSLinux, Workload: WorkloadCost,
		Regions: []Region{RegionAll}, MinVCPU: 2, MinMemoryGiB: 8, Top: 1,
	}

	report, err := Recommend(t.Context(), provider, request)
	require.NoError(t, err)
	require.Len(t, report.Recommendations, 1)
	assert.Equal(t, MachineID("Standard_D2s_v5"), report.Recommendations[0].Machine)
	assert.Equal(t, 2, report.DataSource.SourcesOmitted,
		"the dropped candidate's region and series are not this document's provenance")
}

// A trim that empties the list has lost the provenance of every row it kept,
// which both published schemas forbid with minItems 1.
func TestATrimThatWouldPublishNoSourceFailsClosed(t *testing.T) {
	t.Parallel()

	result := azureShapedResult()
	result.Sources = []SourceRef{scopedSource("https://example.invalid/prices?region=westeurope",
		SourceScope{Region: "westeurope"})}

	_, err := NewListReport(listQueryFor("eastus"), &result)
	require.ErrorIs(t, err, ErrDataUnavailable)
	assert.Equal(t, CodeDataUnavailable, CodeOf(err))
}

// The published document is the only thing a consumer sees, so the trimming has
// to survive serialisation with the rest of it.
func TestTheListDocumentSerialisesItsTrimmedProvenance(t *testing.T) {
	t.Parallel()

	result := azureShapedResult()

	report, err := NewListReport(listQueryFor("eastus"), &result)
	require.NoError(t, err)

	encoded, err := json.Marshal(report)
	require.NoError(t, err)

	var document struct {
		DataSource struct {
			Sources        []map[string]any `json:"sources"`
			SourcesOmitted int              `json:"sources_omitted"`
		} `json:"data_source"`
	}
	require.NoError(t, json.Unmarshal(encoded, &document))
	assert.Len(t, document.DataSource.Sources, 3)
	assert.Equal(t, 2, document.DataSource.SourcesOmitted)
}

// scopeOf finds the scope a result declared for one published URL. The scope is
// domain state and is deliberately not serialised: a consumer reads the source
// list, not the rule that produced it.
func scopeOf(t *testing.T, result *Result, url string) SourceScope {
	t.Helper()

	for i := range result.Sources {
		if result.Sources[i].URL == url {
			return result.Sources[i].Scope
		}
	}

	t.Fatalf("%s is published but is not one of the result's sources", url)

	return SourceScope{}
}
