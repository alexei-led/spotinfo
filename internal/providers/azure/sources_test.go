package azure

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

const (
	retailPricesEastUS = "https://prices.azure.com/api/retail/prices?%24filter=serviceName+eq+%27Virtual+Machines" +
		"%27+and+armRegionName+eq+%27eastus%27+and+priceType+eq+%27Consumption%27" +
		"&api-version=2023-01-01-preview&currencyCode=USD"
	learnDSv5 = "https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/general-purpose/dsv5-series"
)

// westEuropeRetailURL is the same query shape as retailPricesEastUS, for a
// region no answer below contains.
func westEuropeRetailURL() string {
	return "https://prices.azure.com/api/retail/prices?%24filter=serviceName+eq+%27Virtual+Machines" +
		"%27+and+armRegionName+eq+%27westeurope%27+and+priceType+eq+%27Consumption%27" +
		"&api-version=2023-01-01-preview&currencyCode=USD"
}

func fixedInstant() time.Time {
	return time.Date(2026, time.August, 10, 11, 7, 24, 0, time.UTC)
}

// eastUSCandidate is one priced machine in the region retailPricesEastUS covers
// and the series learnDSv5 documents.
func eastUSCandidate() cloud.Candidate {
	amount, err := cloud.ParseMoney("0.020")
	if err != nil {
		panic(err)
	}
	location := cloud.Location{Region: "eastus"}

	return cloud.Candidate{
		Provider: cloud.ProviderAzure,
		OS:       cloud.OSLinux,
		Location: location,
		Machine: cloud.MachineSpec{
			ID: "Standard_D2s_v5", Architecture: cloud.ArchitectureX8664, MemoryGiB: 8, VCPU: 2,
		},
		Spot: &cloud.PriceObservation{
			Location: location, Class: cloud.PriceClassSpot, Currency: cloud.CurrencyUSD,
			Unit: cloud.BillingUnitInstanceHour, Amount: amount,
		},
		Risk: cloud.UnavailableRisk(),
	}
}

func scopeTestMachines() []CatalogMachine {
	return []CatalogMachine{
		{ID: "Standard_D2s_v5", Series: "dsv5", Architecture: cloud.ArchitectureX8664, VCPU: 2, MemoryGiB: 8},
		{ID: "Standard_D4s_v5", Series: "dsv5", Architecture: cloud.ArchitectureX8664, VCPU: 4, MemoryGiB: 16},
		{ID: "Standard_E2s_v5", Series: "esv5", Architecture: cloud.ArchitectureX8664, VCPU: 2, MemoryGiB: 16},
	}
}

func TestSourceScopeIsDerivedFromTheURL(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		url  string
		want cloud.SourceScope
	}{
		{
			name: "a retail query is scoped to the region it filters on",
			url:  retailPricesEastUS,
			want: cloud.SourceScope{Region: "eastus"},
		},
		{
			name: "a learn size page is scoped to the machines of its series",
			url:  learnDSv5,
			want: cloud.SourceScope{Machines: []cloud.MachineID{"Standard_D2s_v5", "Standard_D4s_v5"}},
		},
		// Every case below carries no scope, which covers every candidate and is
		// therefore always published. That is the direction this must fail in:
		// a URL shape nobody recognises costs a larger payload, never a row
		// published without the provenance of its own price.
		{
			name: "a learn page for a series this catalogue does not carry",
			url:  "https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/gpu-accelerated/ncadsh100v5-series",
		},
		{
			name: "a learn page that is not a size page",
			url:  "https://learn.microsoft.com/en-us/azure/virtual-machines/sizes-general",
		},
		{
			name: "a learn size page missing the sizes segment",
			url:  "https://learn.microsoft.com/en-us/azure/virtual-machines/general-purpose/dsv5-series",
		},
		{
			name: "a retail query with no region filter",
			url:  "https://prices.azure.com/api/retail/prices?%24filter=serviceName+eq+%27Virtual+Machines%27",
		},
		{
			name: "a retail query filtering on two regions",
			url: "https://prices.azure.com/api/retail/prices?%24filter=armRegionName+eq+%27eastus%27" +
				"+or+armRegionName+eq+%27westeurope%27",
		},
		{
			name: "a host neither source uses",
			url:  "https://example.invalid/prices?armRegionName=eastus",
		},
		{
			name: "a URL that does not parse",
			url:  "https://learn.microsoft.com/%zz/sizes/general-purpose/dsv5-series",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			scoped := scopedSources([]cloud.SourceRef{{URL: test.url}}, scopeTestMachines())
			require.Len(t, scoped, 1)
			assert.Equal(t, test.want, scoped[0].Scope)
		})
	}
}

// The fail-closed rule, proved on the published document rather than on the
// scope alone: a source whose shape this package cannot read survives a trim
// that drops a source it can.
func TestASourceWithAnUnreadableURLIsPublishedAnyway(t *testing.T) {
	t.Parallel()

	unreadable := "https://prices.azure.com/api/retail/prices?the-vendor-changed-this-shape"
	sources := scopedSources([]cloud.SourceRef{
		{URL: retailPricesEastUS, ParserVersion: "p/1", SchemaVersion: "s/1", FetchedAt: fixedInstant()},
		{URL: westEuropeRetailURL(), ParserVersion: "p/1", SchemaVersion: "s/1", FetchedAt: fixedInstant()},
		{URL: unreadable, ParserVersion: "p/1", SchemaVersion: "s/1", FetchedAt: fixedInstant()},
	}, scopeTestMachines())

	result := cloud.Result{
		Provider:   cloud.ProviderAzure,
		Mode:       cloud.DataModeEmbeddedSnapshot,
		Sources:    sources,
		Candidates: []cloud.Candidate{eastUSCandidate()},
	}

	report, err := cloud.NewListReport(&cloud.Query{Regions: []cloud.Region{"eastus"}, OS: cloud.OSLinux}, &result)
	require.NoError(t, err)

	published := make([]string, 0, len(report.DataSource.Sources))
	for _, source := range report.DataSource.Sources {
		published = append(published, source.URL)
	}

	assert.Contains(t, published, unreadable, "an unreadable URL must be retained, never dropped")
	assert.NotContains(t, published, westEuropeRetailURL(), "a region the answer does not contain is dropped")
	assert.Equal(t, 1, report.DataSource.SourcesOmitted)
}

// The committed snapshot is what makes this worth doing: 81 sources against a
// three-row answer. The assertion is a ratio, not a byte count, so it survives
// a refresh that changes how many regions or series the snapshot carries.
func TestProvenanceIsNotTheBulkOfAnAzureAnswer(t *testing.T) {
	t.Parallel()

	provider, err := New()
	require.NoError(t, err)

	report, err := cloud.Recommend(t.Context(), provider, &cloud.RecommendRequest{
		Cloud: cloud.ProviderAzure, Architecture: cloud.ArchitectureX8664, OS: cloud.OSLinux,
		Workload: cloud.WorkloadCost, Regions: []cloud.Region{cloud.RegionAll},
		MinVCPU: 2, MinMemoryGiB: 8, Top: 3,
	})
	require.NoError(t, err)
	require.Len(t, report.Recommendations, 3)

	document, err := json.Marshal(report)
	require.NoError(t, err)
	provenance, err := json.Marshal(report.DataSource.Sources)
	require.NoError(t, err)

	assert.Positive(t, report.DataSource.SourcesOmitted, "the snapshot reads far more than three rows need")
	assert.Less(t, float64(len(provenance)), float64(len(document))/2,
		"provenance is %d of %d bytes; untrimmed it is over nine tenths of the payload",
		len(provenance), len(document))
}
