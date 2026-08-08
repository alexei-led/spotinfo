package aws

import (
	"context"
	"errors"
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

func TestNewRequiresCollaborators(t *testing.T) {
	t.Parallel()

	_, err := New(nil, stubArchitectures{})
	require.Error(t, err)

	_, err = New(&stubClient{}, nil)
	require.Error(t, err)
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
		FetchedAt: &fetchedAt, Location: cloud.Location{Region: "us-east-1"}, Score: 7,
	}, candidate.Placements[0])
	assert.Equal(t, "us-east-1a", candidate.Placements[1].Location.Zone)
	assert.Equal(t, 4, candidate.Placements[1].Score)
	assert.Equal(t, "us-east-1b", candidate.Placements[2].Location.Zone)

	assert.Equal(t, cloud.DataModeEmbeddedSnapshot, result.Mode)
	assert.Empty(t, result.Sources, "snapshot provenance arrives with manifests, and is never invented")
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

func TestQueryFailsOnAPriceFinerThanTheFixedPointScale(t *testing.T) {
	t.Parallel()

	client := &stubClient{advices: []spot.Advice{{
		Region: "us-east-1", Instance: "m6i.large", Price: 0.00000000012,
		Info: spot.TypeInfo{Cores: 2, RAM: 8},
	}}}

	_, err := testProvider(t, client).Query(t.Context(), linuxQuery())
	require.ErrorIs(t, err, cloud.ErrPrecisionLoss)
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
