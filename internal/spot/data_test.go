package spot

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRegionUSEast1   = "us-east-1"
	testInstanceT2Micro = "t2.micro"
)

func TestFetchAdvisorData_FallbackToEmbedded(t *testing.T) {
	tests := []struct {
		name        string
		ctx         context.Context
		description string
	}{
		{
			name: "timeout forces fallback",
			ctx: func() context.Context {
				ctx, _ := context.WithTimeout(context.Background(), 1*time.Millisecond)
				return ctx
			}(),
			description: "very short timeout should force fallback to embedded data",
		},
		{
			name:        "cancelled context forces fallback",
			ctx:         func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }(),
			description: "cancelled context should force fallback to embedded data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := fetchAdvisorData(tt.ctx)

			// Should successfully get data from embedded fallback
			require.NoError(t, err)
			assert.NotNil(t, data)
			assert.NotNil(t, data.Regions)
			assert.NotEmpty(t, data.Regions)

			// Verify we have expected regions in embedded data
			assert.Contains(t, data.Regions, testRegionUSEast1)

			// Verify data structure integrity
			usEast1 := data.Regions[testRegionUSEast1]
			assert.NotNil(t, usEast1.Linux)
			assert.NotEmpty(t, usEast1.Linux)
		})
	}
}

func TestFetchPricingData_FallbackToEmbedded(t *testing.T) {
	tests := []struct {
		name        string
		useEmbedded bool
		ctx         context.Context
		description string
	}{
		{
			name:        "explicit embedded mode",
			useEmbedded: true,
			ctx:         context.Background(),
			description: "useEmbedded=true should load embedded data directly",
		},
		{
			name:        "timeout forces fallback",
			useEmbedded: false,
			ctx: func() context.Context {
				ctx, _ := context.WithTimeout(context.Background(), 1*time.Millisecond)
				return ctx
			}(),
			description: "timeout should force fallback to embedded data",
		},
		{
			name:        "cancelled context forces fallback",
			useEmbedded: false,
			ctx:         func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }(),
			description: "cancelled context should force fallback to embedded data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := fetchPricingData(tt.ctx, tt.useEmbedded)

			// Should successfully get data from embedded fallback
			require.NoError(t, err)
			assert.NotNil(t, data)
			assert.NotNil(t, data.Config)
			assert.NotEmpty(t, data.Config.Regions)

			// Verify we have expected regions in embedded data
			regionFound := false
			for _, region := range data.Config.Regions {
				if region.Region == testRegionUSEast1 {
					regionFound = true
					assert.NotEmpty(t, region.InstanceTypes)
					break
				}
			}
			assert.True(t, regionFound, "us-east-1 region should be found in embedded pricing data")
		})
	}
}

func TestLoadEmbeddedAdvisorData(t *testing.T) {
	data, err := loadEmbeddedAdvisorData()

	require.NoError(t, err)
	assert.NotNil(t, data)
	assert.NotEmpty(t, data.Regions)

	// Test that we can access specific data
	usEast1, exists := data.Regions[testRegionUSEast1]
	assert.True(t, exists, "us-east-1 should exist in embedded data")
	assert.NotNil(t, usEast1.Linux)
	assert.NotEmpty(t, usEast1.Linux)

	// Test that embedded data has expected instance types
	found := false
	for instanceType := range usEast1.Linux {
		if instanceType == testInstanceT2Micro {
			found = true
			break
		}
	}
	assert.True(t, found, "t2.micro should be found in embedded advisor data")
}

func TestLoadEmbeddedPricingData(t *testing.T) {
	data, err := loadEmbeddedPricingData()

	require.NoError(t, err)
	assert.NotNil(t, data)
	assert.NotNil(t, data.Config)
	assert.NotEmpty(t, data.Config.Regions)

	// Test that we can access specific pricing data
	regionFound := false
	instanceFound := false

	for _, region := range data.Config.Regions {
		if region.Region == testRegionUSEast1 {
			regionFound = true
			for _, instanceType := range region.InstanceTypes {
				for _, size := range instanceType.Sizes {
					if size.Size == testInstanceT2Micro {
						instanceFound = true
						assert.NotEmpty(t, size.ValueColumns)
						break
					}
				}
				if instanceFound {
					break
				}
			}
			break
		}
	}

	assert.True(t, regionFound, "us-east-1 should be found in embedded pricing data")
	assert.True(t, instanceFound, "t2.micro should be found in embedded pricing data")
}

func TestFetchAdvisorData_WithValidContext(t *testing.T) {
	// Test with a reasonable timeout that might succeed if network is available
	// but will fallback gracefully if not
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	data, err := fetchAdvisorData(ctx)

	// Should always succeed (either from network or fallback)
	require.NoError(t, err)
	assert.NotNil(t, data)
	assert.NotEmpty(t, data.Regions)
}

func TestFetchPricingData_WithValidContext(t *testing.T) {
	// Test with a reasonable timeout that might succeed if network is available
	// but will fallback gracefully if not
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	data, err := fetchPricingData(ctx, false)

	// Should always succeed (either from network or fallback)
	require.NoError(t, err)
	assert.NotNil(t, data)
	assert.NotNil(t, data.Config)
	assert.NotEmpty(t, data.Config.Regions)
}

func TestDefaultAdvisorProvider_Integration(t *testing.T) {
	// Test the default advisor provider methods with real embedded data
	provider := newDefaultAdvisorProvider(100 * time.Millisecond)

	t.Run("getRegions", func(t *testing.T) {
		regions := provider.getRegions()

		assert.NotEmpty(t, regions)
		assert.Contains(t, regions, testRegionUSEast1)
		assert.Contains(t, regions, "us-west-2")
	})

	t.Run("getRegionAdvice", func(t *testing.T) {
		advice, err := provider.getRegionAdvice(testRegionUSEast1, "linux")

		require.NoError(t, err)
		assert.NotEmpty(t, advice)

		// Should have some common instance types
		assert.Contains(t, advice, testInstanceT2Micro)

		// Verify advice structure
		t2micro := advice[testInstanceT2Micro]
		assert.GreaterOrEqual(t, t2micro.Range, 0)
		assert.GreaterOrEqual(t, t2micro.Savings, 0)
	})

	t.Run("getRegionAdvice_InvalidOS", func(t *testing.T) {
		_, err := provider.getRegionAdvice(testRegionUSEast1, "invalid-os")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid instance OS")
	})

	t.Run("getRegionAdvice_InvalidRegion", func(t *testing.T) {
		_, err := provider.getRegionAdvice("invalid-region", "linux")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "region not found")
	})

	t.Run("getInstanceType", func(t *testing.T) {
		info, err := provider.getInstanceType(testInstanceT2Micro)

		require.NoError(t, err)
		assert.Greater(t, info.Cores, 0)
		assert.Greater(t, info.RAM, float32(0))
	})

	t.Run("getInstanceType_NotFound", func(t *testing.T) {
		_, err := provider.getInstanceType("invalid.instance")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "instance type not found")
	})

	t.Run("getRange", func(t *testing.T) {
		// Test different range indices
		tests := []struct {
			index    int
			hasError bool
		}{
			{0, false},  // Should be valid
			{1, false},  // Should be valid
			{2, false},  // Should be valid
			{-1, true},  // Should be invalid
			{100, true}, // Should be invalid
		}

		for _, tt := range tests {
			t.Run(fmt.Sprintf("index_%d", tt.index), func(t *testing.T) {
				rangeInfo, err := provider.getRange(tt.index)

				if tt.hasError {
					assert.Error(t, err)
				} else {
					require.NoError(t, err)
					assert.NotEmpty(t, rangeInfo.Label)
					assert.GreaterOrEqual(t, rangeInfo.Min, 0)
					assert.GreaterOrEqual(t, rangeInfo.Max, 0)
					assert.GreaterOrEqual(t, rangeInfo.Max, rangeInfo.Min)
				}
			})
		}
	})
}

func TestDefaultPricingProvider_Integration(t *testing.T) {
	// Test the default pricing provider methods with real embedded data
	provider := newDefaultPricingProvider(100*time.Millisecond, true) // Force embedded mode

	t.Run("getSpotPrice", func(t *testing.T) {
		price, err := provider.getSpotPrice(testInstanceT2Micro, testRegionUSEast1, "linux")

		require.NoError(t, err)
		assert.Greater(t, price, 0.0)
		assert.Less(t, price, 1.0) // Sanity check - t2.micro should be less than $1/hour
	})

	t.Run("getSpotPrice_NotFound", func(t *testing.T) {
		_, err := provider.getSpotPrice("invalid.instance", testRegionUSEast1, "linux")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no pricing data for instance")
	})

	t.Run("getSpotPrice_InvalidRegion", func(t *testing.T) {
		_, err := provider.getSpotPrice(testInstanceT2Micro, "invalid-region", "linux")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no pricing data for region")
	})

	t.Run("getSpotPrice_WindowsOS", func(t *testing.T) {
		price, err := provider.getSpotPrice(testInstanceT2Micro, testRegionUSEast1, "windows")

		// Should succeed and return Windows pricing
		require.NoError(t, err)
		assert.GreaterOrEqual(t, price, 0.0) // Windows pricing might be 0 or higher
	})

	t.Run("getSpotPrice_InvalidOS_DefaultsToLinux", func(t *testing.T) {
		price, err := provider.getSpotPrice(testInstanceT2Micro, testRegionUSEast1, "invalid-os")

		// Should succeed and default to Linux pricing
		require.NoError(t, err)
		assert.Greater(t, price, 0.0)

		// Should be same as Linux price
		linuxPrice, err := provider.getSpotPrice(testInstanceT2Micro, testRegionUSEast1, "linux")
		require.NoError(t, err)
		assert.Equal(t, linuxPrice, price)
	})
}

func TestDefaultPricingProvider_NetworkFallback(t *testing.T) {
	// Test pricing provider that tries network first but falls back to embedded
	provider := newDefaultPricingProvider(1*time.Millisecond, false) // Very short timeout

	price, err := provider.getSpotPrice(testInstanceT2Micro, testRegionUSEast1, "linux")

	// Should still succeed due to fallback
	require.NoError(t, err)
	assert.Greater(t, price, 0.0)
}
