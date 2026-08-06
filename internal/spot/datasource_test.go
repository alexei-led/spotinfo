package spot

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DataSource must never claim "aws" for data it did not positively fetch.
// The MCP metadata used to hard-code "embedded"/"current" regardless, which was
// wrong in both directions: it mislabelled live data and vouched for the
// freshness of a snapshot that can be months old.
func TestClientDataSource(t *testing.T) {
	t.Parallel()

	t.Run("embedded pricing forces the embedded verdict", func(t *testing.T) {
		t.Parallel()

		c := NewWithOptions(time.Second, true)
		// Force the lazy load so the verdict reflects real state.
		_, _ = c.pricingProvider.getSpotPrice(testInstanceT2Micro, testRegionUSEast1, osLinux)

		assert.Equal(t, DataSourceEmbedded, c.DataSource())
	})

	t.Run("nothing loaded yet is reported as embedded, never as live", func(t *testing.T) {
		t.Parallel()

		c := &Client{
			advisorProvider: &defaultAdvisorProvider{},
			pricingProvider: &defaultPricingProvider{},
		}

		assert.Equal(t, DataSourceEmbedded, c.DataSource(),
			"unknown provenance must not be reported as live AWS data")
	})

	t.Run("live on both feeds reports aws", func(t *testing.T) {
		t.Parallel()

		c := &Client{
			advisorProvider: &defaultAdvisorProvider{data: &advisorData{Embedded: false}},
			pricingProvider: &defaultPricingProvider{rawEmbedded: false},
		}

		assert.Equal(t, DataSourceAWS, c.DataSource())
	})

	t.Run("either feed falling back is enough to report embedded", func(t *testing.T) {
		t.Parallel()

		advisorFellBack := &Client{
			advisorProvider: &defaultAdvisorProvider{data: &advisorData{Embedded: true}},
			pricingProvider: &defaultPricingProvider{rawEmbedded: false},
		}
		pricingFellBack := &Client{
			advisorProvider: &defaultAdvisorProvider{data: &advisorData{Embedded: false}},
			pricingProvider: &defaultPricingProvider{rawEmbedded: true},
		}

		assert.Equal(t, DataSourceEmbedded, advisorFellBack.DataSource(),
			"result is only as fresh as its stalest input")
		assert.Equal(t, DataSourceEmbedded, pricingFellBack.DataSource())
	})
}

func TestPricingProviderRecordsFallback(t *testing.T) {
	t.Parallel()

	p := newDefaultPricingProvider(time.Second, true)
	require.NoError(t, p.loadData())

	assert.True(t, p.usedEmbeddedData(), "an embedded load must be recorded as embedded")
}
