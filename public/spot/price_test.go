package spot

import (
	_ "embed"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	//go:embed test/spot-price-test-data.json
	embeddedPriceTestData string
)

func Test_pricingLazyLoad(t *testing.T) {
	t.Parallel()
	const minExpectedRegions = 20 // Use minimum expected instead of exact count
	type args struct {            //nolint:wsl
		url          string
		timeout      time.Duration
		fallbackData string
		embedded     bool
	}
	type want struct { //nolint:wsl
		embedded bool
	}
	var tests = []struct { //nolint:wsl
		name    string
		args    args
		want    want
		wantErr bool
	}{
		{
			name: "load embedded pricing on timeout",
			args: args{url: "https://www.google.com:81/", timeout: 1 * time.Second, fallbackData: embeddedPriceData},
			want: want{embedded: true},
		},
		{
			name: "load embedded pricing on not found",
			args: args{url: "https://notfound", timeout: 1 * time.Second, fallbackData: embeddedPriceData},
			want: want{embedded: true},
		},
		{
			name: "load embedded pricing on unexpected response",
			args: args{url: "https://www.example.com", timeout: 1 * time.Second, fallbackData: embeddedPriceData},
			want: want{embedded: true},
		},
		{
			name: "load embedded pricing if asked explicitly",
			args: args{fallbackData: embeddedPriceData, embedded: true},
			want: want{embedded: true},
		},
		{
			name: "load pricing from spot pricing S3 bucket",
			args: args{url: spotPriceJsURL, timeout: 10 * time.Second, fallbackData: embeddedPriceData},
			want: want{embedded: false},
		},
		{
			name:    "fail on empty embedded data",
			args:    args{timeout: 1 * time.Second},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := pricingLazyLoad(tt.args.url, tt.args.timeout, tt.args.fallbackData, tt.args.embedded)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, tt.want.embedded, got.Embedded)
			assert.GreaterOrEqual(t, len(got.Config.Regions), minExpectedRegions, "Should have at least minimum expected regions")

			// Validate no non-standard region codes are present
			regionCodes := make(map[string]bool)
			for _, r := range got.Config.Regions {
				regionCodes[r.Region] = true
			}

			for nonStandardCode := range awsSpotPricingRegions {
				assert.False(t, regionCodes[nonStandardCode], "Non-standard region code should be replaced: %s", nonStandardCode)
			}
		})
	}
}

func Test_convertRawData(t *testing.T) {
	t.Parallel()
	type args struct {
		priceData string
	}
	type want struct { //nolint:wsl
		regionsLen int
	}
	tests := []struct { //nolint:wsl
		name string
		args args
		want want
	}{
		{
			name: "convert embedded test data",
			args: args{embeddedPriceTestData},
			want: want{5}, // Exact count for test data
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var result rawPriceData
			err := json.Unmarshal([]byte(tt.args.priceData), &result)
			require.NoError(t, err, "Should parse test data successfully")

			got := convertRawData(&result)
			assert.Len(t, got.region, tt.want.regionsLen, "Should have expected number of regions")

			// Validate structure
			for regionName, regionData := range got.region {
				assert.NotEmpty(t, regionName, "Region name should not be empty")
				assert.NotEmpty(t, regionData, "Region data should not be empty")
			}
		})
	}
}

func Test_getSpotInstancePrice(t *testing.T) {
	t.Parallel()
	type args struct {
		instance string
		region   string
		os       string
		embedded bool
	}
	tests := []struct { //nolint:wsl
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "get price of us-east-1 t2.micro linux instance",
			args: args{
				region:   "us-east-1",
				os:       "linux",
				instance: "t2.micro",
				embedded: true,
			},
		},
		{
			name: "fail: get price for non-existing region",
			args: args{
				region:   "non-existing",
				os:       "linux",
				instance: "t2.micro",
				embedded: true,
			},
			wantErr: true,
		},
		{
			name: "fail: get price for non-existing instance",
			args: args{
				region:   "us-east-1",
				os:       "linux",
				instance: "non.existing",
				embedded: true,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := getSpotInstancePrice(tt.args.instance, tt.args.region, tt.args.os, tt.args.embedded)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Greater(t, got, 0.0, "Price should be positive for valid instances")
		})
	}
}
