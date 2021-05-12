package spot

import (
	_ "embed"
	"encoding/json"
	"testing"
	"time"
)

var (
	//go:embed test/spot-price-test-data.json
	embeddedPriceTestData string
)

func Test_pricingLazyLoad(t *testing.T) {
	const awsRegionsCount = 22
	type args struct { //nolint:wsl
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
			got, err := pricingLazyLoad(tt.args.url, tt.args.timeout, tt.args.fallbackData, tt.args.embedded)
			if (err != nil) != tt.wantErr {
				t.Errorf("pricingLazyLoad() error = %v, wantErr %v", err, tt.wantErr)
				return //nolint:nlreturn
			}
			if got != nil {
				if got.Embedded != tt.want.embedded {
					t.Errorf("pricingLazyLoad() got.Embedded = %v, want %v", got.Embedded, tt.want.embedded)
				}
				if len(got.Config.Regions) != awsRegionsCount {
					t.Errorf("pricingLazyLoad() len(got.Ranges) = %v, want %v", len(got.Config.Regions), awsRegionsCount)
				}

				// validate Spot pricing codes replaced
				for nonStandardCode := range awsSpotPricingRegions { //nolint:gofmt
					for _, r := range got.Config.Regions {
						if nonStandardCode == r.Region {
							t.Errorf("pricingLazyLoad() non-standard region code: %v", nonStandardCode)
						}
					}
				}
			}
		})
	}
}

func Test_convertRawData(t *testing.T) {
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
			want: want{5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result rawPriceData
			_ = json.Unmarshal([]byte(tt.args.priceData), &result)
			got := convertRawData(&result)
			if len(got.region) != tt.want.regionsLen {
				t.Errorf("convertRawData() regions = %v, want %v", len(got.region), tt.want.regionsLen)
			}
		})
	}
}

func Test_getSpotInstancePrice(t *testing.T) {
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
			got, err := getSpotInstancePrice(tt.args.instance, tt.args.region, tt.args.os, tt.args.embedded)
			if (err != nil) != tt.wantErr {
				t.Errorf("getSpotInstancePrice() error = %v, wantErr %v", err, tt.wantErr)
				return //nolint:nlreturn
			}
			if !tt.wantErr && got == 0 {
				t.Errorf("getSpotInstancePrice() got = %v, want > 0", got)
			}
		})
	}
}
