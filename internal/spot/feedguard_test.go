package spot

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A minimal but structurally valid pricing payload.
const validPricingPayload = `{"config":{"rate":"perhr","valueColumns":["linux"],"currencies":["USD"],
"regions":[{"region":"us-east-1","instanceTypes":[{"type":"generalCurrentGen",
"sizes":[{"size":"m5.large","valueColumns":[{"name":"linux","prices":{"USD":"0.0434"}}]}]}]}]}}`

const validAdvisorPayload = `{"ranges":[{"index":0,"label":"<5%","dots":0,"max":5}],
"instance_types":{"m5.large":{"cores":2,"ram_gb":8,"emr":true}},
"spot_advisor":{"us-east-1":{"Linux":{"m5.large":{"s":59,"r":0}}}}}`

// A feed that returns HTTP 200 with well-formed JSON of the wrong shape must be
// rejected, not accepted as "zero regions". Accepting it would price every
// instance at $0 with no fallback — the failure mode the guard exists to stop.
func TestParsePricingResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "valid payload", body: validPricingPayload},
		{name: "jsonp wrapped payload", body: "callback(" + validPricingPayload + ");"},
		{name: "empty object", body: `{}`, wantErr: true},
		{name: "empty region list", body: `{"config":{"regions":[]}}`, wantErr: true},
		{name: "renamed keys", body: `{"configuration":{"areas":[{"area":"us-east-1"}]}}`, wantErr: true},
		{name: "html error page", body: `<!doctype html><h1>403</h1>`, wantErr: true},
		{name: "truncated json", body: `{"config":{"regions":[`, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parsePricingResponse([]byte(tc.body))
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, got)

				return
			}

			require.NoError(t, err)
			require.NotEmpty(t, got.Config.Regions)
			assert.Equal(t, "us-east-1", got.Config.Regions[0].Region)
		})
	}
}

func TestParseAdvisorResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "valid payload", body: validAdvisorPayload},
		{name: "empty object", body: `{}`, wantErr: true},
		{name: "no regions", body: `{"ranges":[],"instance_types":{},"spot_advisor":{}}`, wantErr: true},
		{name: "renamed keys", body: `{"advisor":{"us-east-1":{}}}`, wantErr: true},
		{name: "invalid json", body: `not json`, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseAdvisorResponse([]byte(tc.body))
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, got)

				return
			}

			require.NoError(t, err)
			assert.Contains(t, got.Regions, "us-east-1")
		})
	}
}

// Regression guard for the frozen-feed defect. The retired spot.js feed stopped
// updating on 2024-05-13, so every instance family released after that date
// priced at $0. If the embedded data is ever regenerated from a stale source,
// this fails loudly instead of shipping silent zeros.
func TestEmbeddedPricingCoversPost2024Families(t *testing.T) {
	t.Parallel()

	data, err := loadEmbeddedPricingData()
	require.NoError(t, err)

	pricing := convertRawPriceData(data)

	region, ok := pricing.Region[testRegionUSEast1]
	require.True(t, ok, "embedded pricing must cover %s", testRegionUSEast1)

	// Families released after the old feed froze. At least one must be priced;
	// naming several keeps the test alive as individual types are retired.
	post2024 := []string{"m8g.xlarge", "c8g.xlarge", "r8g.xlarge", "c8id.large", "m8i.large"}

	priced := make([]string, 0, len(post2024))

	for _, instance := range post2024 {
		if p, found := region.Instance[instance]; found && p.Linux > 0 {
			priced = append(priced, instance)
		}
	}

	assert.NotEmpty(t, priced,
		"no post-2024 instance family has a non-zero price in %s — the embedded price feed is stale (checked %v)",
		testRegionUSEast1, post2024)
}

// A zero timeout means "unset", not "expire immediately". context.WithTimeout
// with a zero duration yields an already-expired context, which would skip every
// fetch and silently serve embedded data forever instead of erroring.
func TestFetchTimeoutTreatsZeroAsUnset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured time.Duration
		want       time.Duration
	}{
		{name: "zero falls back to the default", configured: 0, want: httpTimeout},
		{name: "negative falls back to the default", configured: -time.Second, want: httpTimeout},
		{name: "an explicit short timeout is honoured", configured: time.Millisecond, want: time.Millisecond},
		{name: "an explicit long timeout is honoured", configured: time.Minute, want: time.Minute},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, fetchTimeout(tc.configured))
		})
	}
}

// strconv.ParseFloat accepts "NaN"/"Inf" without error, so an err-only check
// admits a non-finite price. It then reaches json.Marshal, which rejects
// non-finite floats — crashing `--output json` on a value the upstream feed
// chose. That feed already ships non-numeric cells ("N/A*").
func TestParsePriceRejectsNonFinite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		raw    string
		want   float64
		wantOK bool
	}{
		{name: "ordinary price", raw: "0.0434", want: 0.0434, wantOK: true},
		{name: "zero is valid but unusable upstream", raw: "0", want: 0, wantOK: true},
		{name: "NaN parses without error and must be rejected", raw: "NaN"},
		{name: "positive infinity", raw: "Inf"},
		{name: "explicit positive infinity", raw: "+Inf"},
		{name: "negative infinity", raw: "-Inf"},
		{name: "negative price", raw: "-1.5"},
		{name: "the feed's own not-available marker", raw: "N/A*"},
		{name: "empty", raw: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parsePrice(tc.raw)

			assert.Equal(t, tc.wantOK, ok)
			assert.InDelta(t, tc.want, got, 1e-9)
		})
	}
}

// End-to-end guard: a feed carrying NaN must not produce a price that breaks
// JSON rendering.
func TestNonFinitePriceNeverReachesJSON(t *testing.T) {
	t.Parallel()

	raw, err := parsePricingResponse([]byte(`{"config":{"regions":[{"region":"us-east-1",
"instanceTypes":[{"type":"g","sizes":[{"size":"m5.large","valueColumns":[
{"name":"linux","prices":{"USD":"NaN"}}]}]}]}]}}`))
	require.NoError(t, err)

	priced := convertRawPriceData(raw)
	got := priced.Region["us-east-1"].Instance["m5.large"].Linux

	assert.False(t, math.IsNaN(got), "a NaN feed value must not become a NaN price")
	assert.Zero(t, got)

	_, err = json.Marshal(struct{ Price float64 }{got})
	require.NoError(t, err, "the resulting price must be JSON-encodable")
}

// getSpotPrice compared the OS exactly while getRegionAdvice used EqualFold, so
// `--os Windows` returned Windows savings alongside the Linux price.
func TestGetSpotInstancePriceIsCaseInsensitiveOnOS(t *testing.T) {
	t.Parallel()

	data, err := loadEmbeddedPricingData()
	require.NoError(t, err)

	priced := convertRawPriceData(data)

	linux, err := priced.getSpotInstancePrice("m5.large", testRegionUSEast1, osLinux)
	require.NoError(t, err)

	for _, spelling := range []string{"windows", "Windows", "WINDOWS", "WiNdOwS"} {
		got, err := priced.getSpotInstancePrice("m5.large", testRegionUSEast1, spelling)
		require.NoError(t, err)
		assert.NotEqual(t, linux, got,
			"%q must return the Windows price, not the Linux one", spelling)
		assert.Positive(t, got, "%q must resolve to a real price", spelling)
	}
}
