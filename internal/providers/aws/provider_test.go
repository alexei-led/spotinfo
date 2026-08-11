package aws

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/spot"
)

// stubClient stands in for the legacy AWS client. It records that acquisition
// happened so capability failures can be proven to short-circuit before I/O.
type stubClient struct {
	advices []spot.Advice
	err     error
	source  string
	calls   int
}

func (s *stubClient) GetSpotSavings(_ context.Context, _ ...spot.GetSpotSavingsOption) ([]spot.Advice, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}

	return s.advices, nil
}

func (s *stubClient) DataSource() string {
	if s.source == "" {
		return spot.DataSourceEmbedded
	}

	return s.source
}

// stubArchitectures classifies only the families it was given, exactly as the
// reviewed snapshot does.
type stubArchitectures map[string]spot.Architecture

func (s stubArchitectures) ArchitectureForInstance(instance string) (spot.Architecture, bool) {
	architecture, ok := s[instance]

	return architecture, ok
}

func testProvider(t *testing.T, client *stubClient) *Provider {
	t.Helper()

	provider, err := New(client, stubArchitectures{
		"m6i.large":  spot.ArchitectureX8664,
		"m6g.large":  spot.ArchitectureARM64,
		"t3.nano":    spot.ArchitectureX8664,
		"m5.xlarge":  spot.ArchitectureX8664,
		"c7flex.big": spot.ArchitectureX8664,
	})
	require.NoError(t, err)

	return provider
}

func linuxQuery() *cloud.Query {
	return &cloud.Query{Regions: []cloud.Region{"us-east-1"}, OS: cloud.OSLinux}
}

// Only the client is required. The architecture lookup is optional and degrades
// exactly one thing: without it the provider stops declaring architectures, so a
// request that filters on one is refused rather than answered from an
// unclassified catalogue. Requiring it made the v1 surfaces — which publish no
// architecture at all — fail on a file they never read.
func TestNewRequiresOnlyTheClient(t *testing.T) {
	t.Parallel()

	_, err := New(nil, stubArchitectures{})
	require.Error(t, err, "a provider without a client cannot acquire anything")

	provider, err := New(&stubClient{}, nil)
	require.NoError(t, err)
	assert.Empty(t, provider.Capabilities().Architectures,
		"an unavailable lookup must stop the provider declaring architectures")
	assert.False(t, provider.Capabilities().SupportsArchitecture(cloud.ArchitectureX8664))

	withLookup, err := New(&stubClient{}, stubArchitectures{})
	require.NoError(t, err)
	assert.True(t, withLookup.Capabilities().SupportsArchitecture(cloud.ArchitectureX8664))
}

// Without a lookup an architecture-filtered query is refused, not answered with
// an empty candidate list that looks like "no such machine exists".
func TestQueryWithoutAnArchitectureLookupRefusesArchitectureFilters(t *testing.T) {
	t.Parallel()

	provider, err := New(&stubClient{advices: []spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.04,
		Info: spot.TypeInfo{Cores: 2, RAM: 8},
	}}}, nil)
	require.NoError(t, err)

	query := linuxQuery()
	query.Architecture = cloud.ArchitectureX8664

	_, err = provider.Query(t.Context(), query)
	require.ErrorIs(t, err, cloud.ErrInvalidArgument)

	// A query that does not filter by architecture is still answered.
	result, err := provider.Query(t.Context(), linuxQuery())
	require.NoError(t, err)
	assert.Len(t, result.Candidates, 1)
}

func TestQueryMapsAdviceToNeutralCandidate(t *testing.T) {
	t.Parallel()

	fetchedAt := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	regionScore := 7
	client := &stubClient{advices: []spot.Advice{{
		Region:         "us-east-1",
		Instance:       "m6i.large",
		InstanceType:   "m6i.large",
		Range:          spot.Range{Label: "<5%", Min: 0, Max: 5},
		Savings:        72,
		Info:           spot.TypeInfo{Cores: 2, RAM: 8},
		Price:          0.0416,
		ZonePrice:      map[string]float64{"us-east-1c": 0.0402, "us-east-1a": 0.0418},
		RegionScore:    &regionScore,
		ZoneScores:     map[string]int{"us-east-1b": 9, "us-east-1a": 4},
		ScoreFetchedAt: &fetchedAt,
	}}}

	result, err := testProvider(t, client).Query(t.Context(), linuxQuery())
	require.NoError(t, err)
	require.Len(t, result.Candidates, 1)

	candidate := result.Candidates[0]
	assert.Equal(t, cloud.ProviderAWS, candidate.Provider)
	assert.Equal(t, cloud.Location{Region: "us-east-1"}, candidate.Location)
	assert.Equal(t, cloud.OSLinux, candidate.OS)
	assert.Equal(t, cloud.MachineSpec{
		ID: "m6i.large", Architecture: cloud.ArchitectureX8664, MemoryGiB: 8, VCPU: 2,
	}, candidate.Machine)

	require.NotNil(t, candidate.Spot)
	assert.Equal(t, "0.041600000", candidate.Spot.Amount.String())
	assert.Equal(t, cloud.PriceClassSpot, candidate.Spot.Class)
	assert.Equal(t, cloud.CurrencyUSD, candidate.Spot.Currency)
	assert.Equal(t, cloud.BillingUnitInstanceHour, candidate.Spot.Unit)
	assert.False(t, candidate.Spot.Live)
	assert.Nil(t, candidate.OnDemand, "the AWS feeds publish savings, not an on-demand price")

	require.NotNil(t, candidate.SavingsPercent)
	assert.Equal(t, 72, *candidate.SavingsPercent)

	assert.Equal(t, cloud.RiskStatusAvailable, candidate.Risk.Status)
	assert.Equal(t, cloud.RiskKindInterruptionFrequencyRange, candidate.Risk.Kind)
	assert.Equal(t, "<5%", candidate.Risk.Label)
	require.NotNil(t, candidate.Risk.MinPercent)
	require.NotNil(t, candidate.Risk.MaxPercent)
	assert.InDelta(t, 0.0, *candidate.Risk.MinPercent, 0)
	assert.InDelta(t, 5.0, *candidate.Risk.MaxPercent, 0)
	require.NotNil(t, candidate.Risk.Window)
	assert.Equal(t, advisorWindowDays, candidate.Risk.Window.Days)
	assert.Equal(t, spotAdvisorURL, candidate.Risk.SourceURL)

	require.Len(t, candidate.ZonePrices, 2)
	assert.Equal(t, "us-east-1a", candidate.ZonePrices[0].Location.Zone, "zones must be ordered deterministically")
	assert.Equal(t, "0.041800000", candidate.ZonePrices[0].Amount.String())
	assert.Equal(t, "us-east-1c", candidate.ZonePrices[1].Location.Zone)

	require.Len(t, candidate.Placements, 3)
	assert.Equal(t, cloud.PlacementObservation{
		FetchedAt: &fetchedAt, Location: cloud.Location{Region: "us-east-1"},
		Kind: cloud.PlacementKindPlacementScore, Score: 7,
	}, candidate.Placements[0])
	assert.Equal(t, "us-east-1a", candidate.Placements[1].Location.Zone)
	assert.Equal(t, 4, candidate.Placements[1].Score)
	assert.Equal(t, "us-east-1b", candidate.Placements[2].Location.Zone)

	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)
	assertCommittedProvenance(t, result.Sources)
}

// assertCommittedProvenance checks that a result describes where its answer
// came from. Every field the published v2 payload requires must be present:
// provenance is read from the committed sidecar manifests, never invented.
func assertCommittedProvenance(t *testing.T, sources []cloud.SourceRef) {
	t.Helper()

	require.NotEmpty(t, sources, "a result must report the snapshots it was built from")

	for _, source := range sources {
		assert.True(t, strings.HasPrefix(source.URL, "https://"), "source url %q", source.URL)
		assert.False(t, source.FetchedAt.IsZero(), "source %q has no fetch time", source.URL)
		if source.ContentSHA256 != "" {
			assert.Regexp(t, "^[0-9a-f]{64}$", source.ContentSHA256, "source %q", source.URL)
		}
		assert.NotEmpty(t, source.ParserVersion, "source %q", source.URL)
		assert.NotEmpty(t, source.SchemaVersion, "source %q", source.URL)
	}
}

func TestQueryLeavesOptionalObservationsAbsent(t *testing.T) {
	t.Parallel()

	client := &stubClient{advices: []spot.Advice{{
		Region:   "us-east-1",
		Instance: "c7flex.big",
		Info:     spot.TypeInfo{Cores: 4, RAM: 8},
		Price:    0,
	}}}

	result, err := testProvider(t, client).Query(t.Context(), linuxQuery())
	require.NoError(t, err)
	require.Len(t, result.Candidates, 1)

	candidate := result.Candidates[0]
	assert.Nil(t, candidate.Spot, "an unpriced instance must not be published as costing zero")
	assert.Nil(t, candidate.SavingsPercent)
	assert.Nil(t, candidate.Placements)
	assert.Nil(t, candidate.ZonePrices)
	assert.Equal(t, cloud.RiskStatusUnavailable, candidate.Risk.Status)
	assert.Empty(t, candidate.Risk.Label)
}

func TestQueryMarksLivePricesAndLiveDataSource(t *testing.T) {
	t.Parallel()

	client := &stubClient{source: spot.DataSourceAWS, advices: []spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.05, LivePrice: true,
		Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
	}}}

	result, err := testProvider(t, client).Query(t.Context(), linuxQuery())
	require.NoError(t, err)
	require.Len(t, result.Candidates, 1)
	assert.True(t, result.Candidates[0].Spot.Live)
	assert.Equal(t, cloud.DataModeLive, result.Mode)
}

func TestQueryWidensAdvisorMemoryWithoutFloat32Noise(t *testing.T) {
	t.Parallel()

	client := &stubClient{advices: []spot.Advice{{
		Region: "us-east-1", Instance: "t3.nano", Price: 0.0016,
		Info: spot.TypeInfo{Cores: 2, RAM: 1.7}, Range: spot.Range{Label: "<5%", Max: 5},
	}}}

	result, err := testProvider(t, client).Query(t.Context(), linuxQuery())
	require.NoError(t, err)
	require.Len(t, result.Candidates, 1)
	assert.Equal(t, 1.7, result.Candidates[0].Machine.MemoryGiB)
}

func TestQueryAppliesNeutralFiltersExactly(t *testing.T) {
	t.Parallel()

	ceiling, err := cloud.ParseMoney("0.05")
	require.NoError(t, err)

	advices := []spot.Advice{
		{Region: "us-east-1", Instance: "m6i.large", Price: 0.0416, Info: spot.TypeInfo{Cores: 2, RAM: 8}},
		{Region: "us-east-1", Instance: "m6g.large", Price: 0.0300, Info: spot.TypeInfo{Cores: 2, RAM: 8}},
		{Region: "us-east-1", Instance: "t3.nano", Price: 0.0016, Info: spot.TypeInfo{Cores: 2, RAM: 7}},
		{Region: "us-east-1", Instance: "m5.xlarge", Price: 0.9000, Info: spot.TypeInfo{Cores: 4, RAM: 16}},
		{Region: "us-east-1", Instance: "unknown.large", Price: 0.0100, Info: spot.TypeInfo{Cores: 2, RAM: 8}},
		{Region: "us-east-1", Instance: "c7flex.big", Price: 0, Info: spot.TypeInfo{Cores: 8, RAM: 32}},
	}

	for _, test := range []struct {
		name  string
		query *cloud.Query
		want  []cloud.MachineID
	}{
		{
			name:  "architecture filter drops other and unclassified machines",
			query: &cloud.Query{OS: cloud.OSLinux, Architecture: cloud.ArchitectureARM64},
			want:  []cloud.MachineID{"m6g.large"},
		},
		{
			name:  "fractional memory minimum is applied exactly",
			query: &cloud.Query{OS: cloud.OSLinux, MinMemoryGiB: 7.5},
			want:  []cloud.MachineID{"m6i.large", "m6g.large", "m5.xlarge", "unknown.large", "c7flex.big"},
		},
		{
			name:  "price ceiling excludes unknown prices",
			query: &cloud.Query{OS: cloud.OSLinux, MaxPrice: &ceiling},
			want:  []cloud.MachineID{"m6i.large", "m6g.large", "t3.nano", "unknown.large"},
		},
		{
			name:  "vcpu minimum",
			query: &cloud.Query{OS: cloud.OSLinux, MinVCPU: 4},
			want:  []cloud.MachineID{"m5.xlarge", "c7flex.big"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result, err := testProvider(t, &stubClient{advices: advices}).Query(t.Context(), test.query)
			require.NoError(t, err)

			machines := make([]cloud.MachineID, 0, len(result.Candidates))
			for _, candidate := range result.Candidates {
				machines = append(machines, candidate.Machine.ID)
			}
			assert.Equal(t, test.want, machines)
		})
	}
}

func TestQueryRejectsUnsupportedCapabilitiesBeforeAcquisition(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		query *cloud.Query
	}{
		{name: "nil query", query: nil},
		{name: "empty os", query: &cloud.Query{}},
		{name: "unsupported os", query: &cloud.Query{OS: "plan9"}},
		{name: "unsupported architecture", query: &cloud.Query{OS: cloud.OSLinux, Architecture: "riscv64"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &stubClient{}
			_, err := testProvider(t, client).Query(t.Context(), test.query)
			require.ErrorIs(t, err, cloud.ErrInvalidArgument)
			assert.Zero(t, client.calls, "capability checks must run before any data acquisition")
		})
	}
}

func TestQueryPropagatesAcquisitionFailure(t *testing.T) {
	t.Parallel()

	failure := errors.New("advisor feed unreachable")
	_, err := testProvider(t, &stubClient{err: failure}).Query(t.Context(), linuxQuery())
	require.ErrorIs(t, err, failure)
}

// A price outside the fixed-point scale is a defect in one feed row. It drops
// that candidate and leaves the rest of the answer intact; returning an error
// turned a single bad row into an empty response for the whole catalogue, on a
// surface whose v1 contract is golden-pinned and where v1 rendered whatever the
// feed published.
func TestQueryDropsOnlyThePriceFinerThanTheFixedPointScale(t *testing.T) {
	t.Parallel()

	client := &stubClient{advices: []spot.Advice{
		{
			Region: "us-east-1", Instance: "m6i.large", Price: 0.00000000012,
			Info: spot.TypeInfo{Cores: 2, RAM: 8},
		},
		{
			Region: "us-east-1", Instance: "m5.xlarge", Price: 0.1234,
			Info: spot.TypeInfo{Cores: 4, RAM: 16},
		},
	}}

	result, err := testProvider(t, client).Query(t.Context(), linuxQuery())
	require.NoError(t, err)
	require.Len(t, result.Candidates, 1, "the representable candidate must survive")
	assert.Equal(t, cloud.MachineID("m5.xlarge"), result.Candidates[0].Machine.ID)
}

// Every row being unrepresentable is still an empty answer, not an error: the
// query succeeded and found nothing it could publish.
func TestQueryReturnsNoCandidatesWhenEveryPriceIsUnrepresentable(t *testing.T) {
	t.Parallel()

	client := &stubClient{advices: []spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.00000000012,
		Info: spot.TypeInfo{Cores: 2, RAM: 8},
	}}}

	result, err := testProvider(t, client).Query(t.Context(), linuxQuery())
	require.NoError(t, err)
	assert.Empty(t, result.Candidates)
}

func TestCapabilitiesDescribeTheAWSFeeds(t *testing.T) {
	t.Parallel()

	provider := testProvider(t, &stubClient{})
	assert.Equal(t, cloud.ProviderAWS, provider.ID())

	capabilities := provider.Capabilities()
	assert.True(t, capabilities.SupportsOS(cloud.OSLinux))
	assert.True(t, capabilities.SupportsOS(cloud.OSWindows))
	assert.True(t, capabilities.SpotPrice)
	assert.False(t, capabilities.OnDemandPrice)
	assert.True(t, capabilities.Risk)
	assert.True(t, capabilities.PlacementScore)
	assert.True(t, capabilities.ZoneDetail)
	assert.True(t, capabilities.LiveEnrichment)
}

// The adapter is additive: the legacy AWS surface it wraps must keep working.
func TestProviderSatisfiesTheNeutralInterface(t *testing.T) {
	t.Parallel()

	var provider cloud.Provider = testProvider(t, &stubClient{})
	assert.Equal(t, cloud.ProviderAWS, provider.ID())
}

// The advisor feed publishes an unvalidated savings integer. A figure outside
// the 1..100 range is not a percentage of an on-demand price, so it is absent
// rather than published — the same treatment an unpriced instance gets.
func TestSavingsOutsideTheValidRangeIsNotPublished(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		savings int
		want    *int
	}{
		{name: "no data", savings: 0},
		{name: "negative", savings: -5},
		{name: "above one hundred percent", savings: 150},
		{name: "lower bound", savings: 1, want: intPtr(1)},
		{name: "upper bound", savings: 100, want: intPtr(100)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &stubClient{advices: []spot.Advice{{
				Region: "us-east-1", Instance: "m6i.large", Price: 0.0416, Savings: test.savings,
				Info: spot.TypeInfo{Cores: 2, RAM: 8}, Range: spot.Range{Label: "<5%", Max: 5},
			}}}

			result, err := testProvider(t, client).Query(t.Context(), linuxQuery())
			require.NoError(t, err)
			require.Len(t, result.Candidates, 1)
			assert.Equal(t, test.want, result.Candidates[0].SavingsPercent)
		})
	}
}

func intPtr(value int) *int { return &value }

// appliedOptions applies mapped legacy options to a fresh legacy config and
// reads the result back by field name.
//
// getSpotSavingsConfig is unexported, so reflection is the only way to assert
// the mapping from outside internal/spot — and it is worth asserting: every
// list_spot_machines and `spotinfo list` call reaches the legacy client through
// legacyOptions, where a dropped placement flag or a swapped sort key changes
// the answer without failing anything.
func appliedOptions(t *testing.T, opts []spot.GetSpotSavingsOption) map[string]any {
	t.Helper()

	require.NotEmpty(t, opts)

	config := reflect.New(reflect.TypeOf(opts[0]).In(0).Elem())
	for _, opt := range opts {
		reflect.ValueOf(opt).Call([]reflect.Value{config})
	}

	fields := config.Elem()
	applied := make(map[string]any, fields.NumField())
	for i := range fields.NumField() {
		field := fields.Field(i)
		switch field.Kind() { //nolint:exhaustive // the config carries only these kinds.
		case reflect.String:
			applied[fields.Type().Field(i).Name] = field.String()
		case reflect.Bool:
			applied[fields.Type().Field(i).Name] = field.Bool()
		case reflect.Int, reflect.Int64:
			applied[fields.Type().Field(i).Name] = field.Int()
		case reflect.Float64:
			applied[fields.Type().Field(i).Name] = field.Float()
		case reflect.Slice:
			values := make([]string, 0, field.Len())
			for j := range field.Len() {
				values = append(values, field.Index(j).String())
			}
			applied[fields.Type().Field(i).Name] = values
		default:
			t.Fatalf("unhandled legacy config field kind %s", field.Kind())
		}
	}

	return applied
}

// Every neutral sort key must reach the legacy client as its own ordering. A
// swapped entry returns the whole answer in the wrong order and nothing else
// notices; an unset key must keep the legacy default so existing AWS callers
// see the order they always have.
func TestNeutralSortKeysMapOntoTheLegacyOrdering(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		key  cloud.SortKey
		want spot.SortBy
	}{
		{key: "", want: spot.SortByRange},
		{key: cloud.SortByPrice, want: spot.SortByPrice},
		{key: cloud.SortBySavings, want: spot.SortBySavings},
		{key: cloud.SortByMachine, want: spot.SortByInstance},
		{key: cloud.SortByRegion, want: spot.SortByRegion},
		{key: cloud.SortByPlacementScore, want: spot.SortByScore},
		{key: cloud.SortByRisk, want: spot.SortByRange},
		{key: "not-a-sort-key", want: spot.SortByRange},
	} {
		t.Run(string(test.key), func(t *testing.T) {
			t.Parallel()

			query := linuxQuery()
			query.Sort = cloud.SortOrder{Key: test.key, Descending: true}

			applied := appliedOptions(t, legacyOptions(query))
			assert.Equal(t, int64(test.want), applied["sortBy"])
			assert.Equal(t, true, applied["sortDesc"])
		})
	}
}

// Placement is requested as one unit: enabling scores carries the zone choice
// and the timeout, while a minimum score filters whatever scores arrived. A
// disabled request must ask for no score work at all.
func TestPlacementFlagsReachTheLegacyClientTogether(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		placement cloud.PlacementRequest
		want      map[string]any
	}{
		{
			name:      "disabled",
			placement: cloud.PlacementRequest{},
			want: map[string]any{
				"withScores": false, "singleAvailabilityZone": false,
				"scoreTimeout": int64(0), "minScore": int64(0),
			},
		},
		{
			name:      "enabled in a single zone with a timeout",
			placement: cloud.PlacementRequest{Enabled: true, SingleZone: true, Timeout: 3 * time.Second},
			want: map[string]any{
				"withScores": true, "singleAvailabilityZone": true,
				"scoreTimeout": int64(3 * time.Second), "minScore": int64(0),
			},
		},
		{
			name:      "enabled across zones with no timeout",
			placement: cloud.PlacementRequest{Enabled: true},
			want: map[string]any{
				"withScores": true, "singleAvailabilityZone": false,
				"scoreTimeout": int64(0), "minScore": int64(0),
			},
		},
		{
			name:      "minimum score without enrichment",
			placement: cloud.PlacementRequest{MinScore: 7},
			want: map[string]any{
				"withScores": false, "singleAvailabilityZone": false,
				"scoreTimeout": int64(0), "minScore": int64(7),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			query := linuxQuery()
			query.Placement = test.placement

			applied := appliedOptions(t, legacyOptions(query))
			for field, want := range test.want {
				assert.Equal(t, want, applied[field], field)
			}
		})
	}
}

// The resource and price filters are a coarse pre-filter, but they must still
// arrive: an omitted one makes the legacy client return everything and leaves
// accepts() to carry a load it was never measured against. A zero value asks
// for no filter at all, which is what the legacy client treats as unset.
func TestResourceFiltersReachTheLegacyClientOnlyWhenRequested(t *testing.T) {
	t.Parallel()

	ceiling, err := cloud.MoneyFromFloat(0.25)
	require.NoError(t, err)

	query := linuxQuery()
	query.MachinePattern = "^m6i"
	query.MinVCPU = 4
	query.MinMemoryGiB = 15.5
	query.MaxPrice = &ceiling

	applied := appliedOptions(t, legacyOptions(query))
	assert.Equal(t, "^m6i", applied["pattern"])
	assert.Equal(t, int64(4), applied["cpu"])
	// Whole GiB: the legacy client compares integers, so 15.5 must floor rather
	// than round up and exclude a machine that satisfies the neutral minimum.
	assert.Equal(t, int64(15), applied["memory"])
	assert.InDelta(t, 0.25, applied["maxPrice"], 1e-9)
	assert.Equal(t, []string{"us-east-1"}, applied["regions"])
	assert.Equal(t, string(cloud.OSLinux), applied["instanceOS"])

	unfiltered := appliedOptions(t, legacyOptions(linuxQuery()))
	assert.Equal(t, "", unfiltered["pattern"])
	assert.Equal(t, int64(0), unfiltered["cpu"])
	assert.Equal(t, int64(0), unfiltered["memory"])
	assert.InDelta(t, 0.0, unfiltered["maxPrice"], 1e-9)
}

// The AWS placement scores are unchanged by the kind vocabulary: the same
// integers reach the same locations, and each observation now names the
// measurement it is. This is the "still produces the same score" guard — a
// mapping that started publishing a probability, or that dropped the kind and
// so became unpublishable, fails here.
func TestPlacementScoresKeepTheirValuesAndNameTheirKind(t *testing.T) {
	t.Parallel()

	fetchedAt := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	regionScore := 7
	client := &stubClient{advices: []spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.0416,
		Info:           spot.TypeInfo{Cores: 2, RAM: 8},
		RegionScore:    &regionScore,
		ZoneScores:     map[string]int{"us-east-1b": 9, "us-east-1a": 4},
		ScoreFetchedAt: &fetchedAt,
	}}}

	query := linuxQuery()
	query.Placement = cloud.PlacementRequest{Enabled: true}

	result, err := testProvider(t, client).Query(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, result.Candidates, 1)

	assert.Equal(t, []cloud.PlacementObservation{
		{
			FetchedAt: &fetchedAt, Location: cloud.Location{Region: "us-east-1"},
			Kind: cloud.PlacementKindPlacementScore, Score: 7,
		},
		{
			FetchedAt: &fetchedAt, Location: cloud.Location{Region: "us-east-1", Zone: "us-east-1a"},
			Kind: cloud.PlacementKindPlacementScore, Score: 4,
		},
		{
			FetchedAt: &fetchedAt, Location: cloud.Location{Region: "us-east-1", Zone: "us-east-1b"},
			Kind: cloud.PlacementKindPlacementScore, Score: 9,
		},
	}, result.Candidates[0].Placements)
	assert.Equal(t, cloud.PlacementStatusAvailable, result.Candidates[0].PlacementStatus)

	// And the declaration a surface reads before acquisition to decide whether
	// an integer --min-score floor means anything here.
	assert.Equal(t, cloud.PlacementKindPlacementScore, Capabilities().PlacementKind)
	assert.True(t, Capabilities().SupportsScoreFloor())
}

// A lookup that came back empty is a different answer from one nobody asked
// for, and the status is what carries the difference: Placements is a slice, so
// both would otherwise be the empty slice. Scores are fetched through an EC2
// API that fails soft, so the empty case is the one a caller without
// credentials actually sees.
func TestAnEmptyScoreLookupIsReportedAsUnavailable(t *testing.T) {
	t.Parallel()

	client := &stubClient{advices: []spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.0416,
		Info: spot.TypeInfo{Cores: 2, RAM: 8},
	}}}

	asked := linuxQuery()
	asked.Placement = cloud.PlacementRequest{Enabled: true}

	scored, err := testProvider(t, client).Query(t.Context(), asked)
	require.NoError(t, err)
	require.Len(t, scored.Candidates, 1)
	assert.Empty(t, scored.Candidates[0].Placements)
	assert.Equal(t, cloud.PlacementStatusUnavailable, scored.Candidates[0].PlacementStatus)

	unscored, err := testProvider(t, client).Query(t.Context(), linuxQuery())
	require.NoError(t, err)
	require.Len(t, unscored.Candidates, 1)
	assert.Equal(t, cloud.PlacementStatusNotRequested, unscored.Candidates[0].PlacementStatus)
}
