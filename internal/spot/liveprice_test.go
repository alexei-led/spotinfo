package spot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestEnrichMissingPrices_FillsZeroPrices(t *testing.T) {
	t.Parallel()

	provider := newMocklivePriceProvider(t)
	provider.EXPECT().fetchLivePrices(
		mock.Anything, "us-east-1", mock.MatchedBy(func(types []string) bool {
			return len(types) == 1 && types[0] == "m8g.xlarge"
		}), "linux",
	).Return(map[string]float64{"m8g.xlarge": 0.1234}, nil).Once()

	advices := []Advice{
		{Region: "us-east-1", Instance: "t2.micro", Price: 0.0116},
		{Region: "us-east-1", Instance: "m8g.xlarge", Price: 0},
	}

	enrichMissingPrices(context.Background(), advices, provider, "linux", 5*time.Second)

	assert.Equal(t, 0.0116, advices[0].Price)
	assert.False(t, advices[0].LivePrice)
	assert.Equal(t, 0.1234, advices[1].Price)
	assert.True(t, advices[1].LivePrice)
}

func TestEnrichMissingPrices_MultipleRegions(t *testing.T) {
	t.Parallel()

	provider := newMocklivePriceProvider(t)
	provider.EXPECT().fetchLivePrices(
		mock.Anything, "us-east-1", mock.MatchedBy(func(types []string) bool {
			return len(types) == 1 && types[0] == "r8g.large"
		}), "linux",
	).Return(map[string]float64{"r8g.large": 0.05}, nil).Once()

	provider.EXPECT().fetchLivePrices(
		mock.Anything, "eu-west-1", mock.MatchedBy(func(types []string) bool {
			return len(types) == 1 && types[0] == "r8g.large"
		}), "linux",
	).Return(map[string]float64{"r8g.large": 0.06}, nil).Once()

	advices := []Advice{
		{Region: "us-east-1", Instance: "r8g.large", Price: 0},
		{Region: "eu-west-1", Instance: "r8g.large", Price: 0},
	}

	enrichMissingPrices(context.Background(), advices, provider, "linux", 5*time.Second)

	assert.Equal(t, 0.05, advices[0].Price)
	assert.True(t, advices[0].LivePrice)
	assert.Equal(t, 0.06, advices[1].Price)
	assert.True(t, advices[1].LivePrice)
}

func TestEnrichMissingPrices_NoMissing(t *testing.T) {
	t.Parallel()

	advices := []Advice{
		{Region: "us-east-1", Instance: "t2.micro", Price: 0.0116},
		{Region: "us-east-1", Instance: "t2.small", Price: 0.023},
	}

	// Provider should not be called at all
	enrichMissingPrices(context.Background(), advices, nil, "linux", 5*time.Second)

	assert.Equal(t, 0.0116, advices[0].Price)
	assert.Equal(t, 0.023, advices[1].Price)
}

func TestEnrichMissingPrices_NilProvider(t *testing.T) {
	t.Parallel()

	advices := []Advice{
		{Region: "us-east-1", Instance: "m8g.xlarge", Price: 0},
	}

	enrichMissingPrices(context.Background(), advices, nil, "linux", 5*time.Second)

	assert.Equal(t, 0.0, advices[0].Price)
	assert.False(t, advices[0].LivePrice)
}

func TestEnrichMissingPrices_APIFailure_GracefulDegradation(t *testing.T) {
	t.Parallel()

	provider := newMocklivePriceProvider(t)
	provider.EXPECT().fetchLivePrices(
		mock.Anything, "us-east-1", mock.Anything, "linux",
	).Return(nil, errors.New("access denied")).Once()

	advices := []Advice{
		{Region: "us-east-1", Instance: "m8g.xlarge", Price: 0},
	}

	enrichMissingPrices(context.Background(), advices, provider, "linux", 5*time.Second)

	assert.Equal(t, 0.0, advices[0].Price)
	assert.False(t, advices[0].LivePrice)
}

func TestEnrichMissingPrices_PartialResults(t *testing.T) {
	t.Parallel()

	provider := newMocklivePriceProvider(t)
	provider.EXPECT().fetchLivePrices(
		mock.Anything, "us-east-1", mock.MatchedBy(func(types []string) bool {
			return len(types) == 2
		}), "linux",
	).Return(map[string]float64{"m8g.xlarge": 0.15}, nil).Once()

	advices := []Advice{
		{Region: "us-east-1", Instance: "m8g.xlarge", Price: 0},
		{Region: "us-east-1", Instance: "r8gd.2xlarge", Price: 0},
	}

	enrichMissingPrices(context.Background(), advices, provider, "linux", 5*time.Second)

	assert.Equal(t, 0.15, advices[0].Price)
	assert.True(t, advices[0].LivePrice)
	assert.Equal(t, 0.0, advices[1].Price)
	assert.False(t, advices[1].LivePrice)
}

func TestOsToProductDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		os       string
		expected string
	}{
		{"linux", "Linux/UNIX"},
		{"Linux", "Linux/UNIX"},
		{"windows", "Windows"},
		{"Windows", "Windows"},
		{"", "Linux/UNIX"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, osToProductDescription(tt.os))
	}
}

func TestClient_GetSpotSavings_WithLivePriceFallback(t *testing.T) {
	t.Parallel()

	mocks := &mockProviders{
		advisor: newMockadvisorProvider(t),
		pricing: newMockpricingProvider(t),
	}
	livePrice := newMocklivePriceProvider(t)

	mocks.advisor.EXPECT().getRegionAdvice("us-east-1", "linux").Return(map[string]spotAdvice{
		"t2.micro":   {Range: 0, Savings: 50},
		"m8g.xlarge": {Range: 1, Savings: 70},
	}, nil).Once()

	mocks.advisor.EXPECT().getInstanceType("t2.micro").Return(TypeInfo{Cores: 1, RAM: 1.0}, nil).Once()
	mocks.advisor.EXPECT().getInstanceType("m8g.xlarge").Return(TypeInfo{Cores: 4, RAM: 16.0}, nil).Once()

	mocks.advisor.EXPECT().getRange(0).Return(Range{Label: "<5%", Min: 0, Max: 5}, nil).Once()
	mocks.advisor.EXPECT().getRange(1).Return(Range{Label: "5-10%", Min: 5, Max: 10}, nil).Once()

	mocks.pricing.EXPECT().getSpotPrice("t2.micro", "us-east-1", "linux").Return(0.0116, nil).Once()
	mocks.pricing.EXPECT().getSpotPrice("m8g.xlarge", "us-east-1", "linux").Return(0.0, errors.New("no pricing data")).Once()

	livePrice.EXPECT().fetchLivePrices(
		mock.Anything, "us-east-1", mock.MatchedBy(func(types []string) bool {
			return len(types) == 1 && types[0] == "m8g.xlarge"
		}), "linux",
	).Return(map[string]float64{"m8g.xlarge": 0.1567}, nil).Once()

	client := NewWithProviders(mocks.advisor, mocks.pricing)
	client.SetLivePriceProvider(livePrice)

	result, err := client.GetSpotSavings(context.Background(),
		WithRegions([]string{"us-east-1"}),
		WithOS("linux"),
		WithSort(SortByInstance, false),
	)

	require.NoError(t, err)
	require.Len(t, result, 2)

	// m8g.xlarge should have live price
	assert.Equal(t, "m8g.xlarge", result[0].Instance)
	assert.Equal(t, 0.1567, result[0].Price)
	assert.True(t, result[0].LivePrice)

	// t2.micro should have static price
	assert.Equal(t, "t2.micro", result[1].Instance)
	assert.Equal(t, 0.0116, result[1].Price)
	assert.False(t, result[1].LivePrice)
}

func TestClient_GetSpotSavings_LivePriceWithMaxPriceFilter(t *testing.T) {
	t.Parallel()

	mocks := &mockProviders{
		advisor: newMockadvisorProvider(t),
		pricing: newMockpricingProvider(t),
	}
	livePrice := newMocklivePriceProvider(t)

	mocks.advisor.EXPECT().getRegionAdvice("us-east-1", "linux").Return(map[string]spotAdvice{
		"m8g.xlarge":  {Range: 0, Savings: 70},
		"m8g.2xlarge": {Range: 0, Savings: 65},
	}, nil).Once()

	mocks.advisor.EXPECT().getInstanceType("m8g.xlarge").Return(TypeInfo{Cores: 4, RAM: 16.0}, nil).Once()
	mocks.advisor.EXPECT().getInstanceType("m8g.2xlarge").Return(TypeInfo{Cores: 8, RAM: 32.0}, nil).Once()

	mocks.advisor.EXPECT().getRange(0).Return(Range{Label: "<5%", Min: 0, Max: 5}, nil).Times(2)

	// Both have zero price from static feed
	mocks.pricing.EXPECT().getSpotPrice("m8g.xlarge", "us-east-1", "linux").Return(0.0, errors.New("no pricing data")).Once()
	mocks.pricing.EXPECT().getSpotPrice("m8g.2xlarge", "us-east-1", "linux").Return(0.0, errors.New("no pricing data")).Once()

	// Live prices: one affordable, one expensive
	livePrice.EXPECT().fetchLivePrices(
		mock.Anything, "us-east-1", mock.MatchedBy(func(types []string) bool {
			return len(types) == 2
		}), "linux",
	).Return(map[string]float64{
		"m8g.xlarge":  0.10,
		"m8g.2xlarge": 0.25,
	}, nil).Once()

	client := NewWithProviders(mocks.advisor, mocks.pricing)
	client.SetLivePriceProvider(livePrice)

	// maxPrice filter is re-applied after live price enrichment,
	// so only instances within the price limit are returned.
	result, err := client.GetSpotSavings(context.Background(),
		WithRegions([]string{"us-east-1"}),
		WithOS("linux"),
		WithMaxPrice(0.15),
		WithSort(SortByPrice, false),
	)

	require.NoError(t, err)
	require.Len(t, result, 1, "Only the affordable instance should pass the maxPrice filter")
	assert.Equal(t, "m8g.xlarge", result[0].Instance)
	assert.Equal(t, 0.10, result[0].Price)
	assert.True(t, result[0].LivePrice)
}
