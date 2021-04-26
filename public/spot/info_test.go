package spot

import (
	"regexp"
	"testing"
	"time"
)

func Test_dataLazyLoad(t *testing.T) {
	type args struct {
		url      string
		timeout  time.Duration
		fallback string
	}
	type want struct {
		embedded  bool
		rangesLen int
	}
	tests := []struct {
		name    string
		args    args
		want    want
		wantErr bool
	}{
		{
			name: "load embedded on timeout",
			args: args{"http://www.google.com:81/", 1 * time.Second, embeddedSpotData},
			want: want{embedded: true, rangesLen: 5},
		},
		{
			name: "load embedded on not found",
			args: args{"http://notfound", 1 * time.Second, embeddedSpotData},
			want: want{embedded: true, rangesLen: 5},
		},
		{
			name: "load embedded on unexpected response",
			args: args{"https://www.example.com", 1 * time.Second, embeddedSpotData},
			want: want{embedded: true, rangesLen: 5},
		},
		{
			name: "load data from spot advisor",
			args: args{spotAdvisorJsonUrl, 10 * time.Second, embeddedSpotData},
			want: want{embedded: false, rangesLen: 5},
		},
		{
			name:    "fail on empty embedded data",
			args:    args{"", 1 * time.Second, ""},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dataLazyLoad(tt.args.url, tt.args.timeout, tt.args.fallback)
			if (err != nil) != tt.wantErr {
				t.Errorf("dataLazyLoad() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != nil {
				if got.Embedded != tt.want.embedded {
					t.Errorf("dataLazyLoad() got.Embedded = %v, want %v", got.Embedded, tt.want.embedded)
				}
				if len(got.Ranges) != tt.want.rangesLen {
					t.Errorf("dataLazyLoad() len(got.Ranges) = %v, want %v", len(got.Ranges), tt.want.rangesLen)
				}
			}
		})
	}
}

func TestGetSpotSavings(t *testing.T) {
	type args struct {
		pattern    string
		region     string
		instanceOS string
		cpu        int
		memory     int
		price      float64
		sortBy     int
		sortDesc   bool
	}
	type want struct {
		minCpu    int
		minMemory float32
		maxPrice  float64
	}
	tests := []struct {
		name    string
		args    args
		want    want
		wantErr bool
	}{
		{
			name: "get advice by pattern",
			args: args{pattern: "^(m5)(\\S)*", region: "us-east-1", instanceOS: "linux"},
		},
		{
			name: "get advice by pattern min.cpu=64 and min.memory=128",
			args: args{pattern: "^(m5)(\\S)*", region: "us-east-1", instanceOS: "linux", cpu: 64, memory: 128},
			want: want{minCpu: 64, minMemory: 128},
		},
		{
			name: "get advice by pattern min.cpu=4, min.memory=16 and max.price $1.00/hour",
			args: args{pattern: "^(m5)(\\S)*", region: "us-east-1", instanceOS: "linux", cpu: 4, memory: 16, price: 1.0},
			want: want{minCpu: 4, minMemory: 16, maxPrice: 1.0},
		},
		{
			name:    "fail on bad regexp pattern",
			args:    args{pattern: "a(b", region: "us-east-1", instanceOS: "linux"},
			wantErr: true,
		},
		{
			name:    "fail on non-existing region",
			args:    args{pattern: "m3.medium", region: "non-existing", instanceOS: "linux"},
			wantErr: true,
		},
		{
			name:    "fail on non-existing os",
			args:    args{pattern: "m3.medium", region: "us-east-1", instanceOS: "reactos"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetSpotSavings(tt.args.pattern, tt.args.region, tt.args.instanceOS, tt.args.cpu, tt.args.memory, tt.args.price, tt.args.sortBy, tt.args.sortDesc)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetSpotSavings() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != nil {
				for _, advice := range got {
					matched, err := regexp.MatchString(tt.args.pattern, advice.Instance)
					if !matched || err != nil {
						t.Errorf("GetSpotSavings() advice.Instance does not match '%v'", &tt.args.pattern)
					}
					if advice.Info.Cores < tt.want.minCpu {
						t.Errorf("GetSpotSavings() advice.Cores = %v < min %v", advice.Info.Cores, tt.want.minCpu)
					}
					if advice.Info.Ram < tt.want.minMemory {
						t.Errorf("GetSpotSavings() advice.Ram = %v < min %v", advice.Info.Ram, tt.want.minMemory)
					}
					if tt.want.maxPrice != 0 && advice.Price > tt.want.maxPrice {
						t.Errorf("GetSpotSavings() advice.Price = %v > max %v", advice.Price, tt.want.maxPrice)
					}
				}
			}
		})
	}
}
