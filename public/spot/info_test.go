package spot

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertSorted validates that the advice slice is sorted according to the specified criteria
func assertSorted(t *testing.T, advices []Advice, sortBy int, sortDesc bool) {
	t.Helper()

	if len(advices) <= 1 {
		return // trivially sorted
	}

	for i := 0; i < len(advices)-1; i++ {
		switch sortBy {
		case SortByRegion:
			comparison := strings.Compare(advices[i].Region, advices[i+1].Region)
			if sortDesc {
				assert.GreaterOrEqual(t, comparison, 0, "Regions should be sorted descending at index %d", i)
			} else {
				assert.LessOrEqual(t, comparison, 0, "Regions should be sorted ascending at index %d", i)
			}
		case SortBySavings:
			if sortDesc {
				assert.GreaterOrEqual(t, advices[i].Savings, advices[i+1].Savings, "Savings should be sorted descending at index %d", i)
			} else {
				assert.LessOrEqual(t, advices[i].Savings, advices[i+1].Savings, "Savings should be sorted ascending at index %d", i)
			}
		case SortByPrice:
			if sortDesc {
				assert.GreaterOrEqual(t, advices[i].Price, advices[i+1].Price, "Price should be sorted descending at index %d", i)
			} else {
				assert.LessOrEqual(t, advices[i].Price, advices[i+1].Price, "Price should be sorted ascending at index %d", i)
			}
		case SortByRange:
			if sortDesc {
				assert.GreaterOrEqual(t, advices[i].Range.Min, advices[i+1].Range.Min, "Range should be sorted descending at index %d", i)
			} else {
				assert.LessOrEqual(t, advices[i].Range.Min, advices[i+1].Range.Min, "Range should be sorted ascending at index %d", i)
			}
		case SortByInstance:
			comparison := strings.Compare(advices[i].Instance, advices[i+1].Instance)
			if sortDesc {
				assert.GreaterOrEqual(t, comparison, 0, "Instance types should be sorted descending at index %d", i)
			} else {
				assert.LessOrEqual(t, comparison, 0, "Instance types should be sorted ascending at index %d", i)
			}
		}
	}
}

func Test_dataLazyLoad(t *testing.T) {
	t.Parallel()
	type args struct {
		url      string
		timeout  time.Duration
		fallback string
	}
	type want struct { //nolint:wsl
		embedded  bool
		rangesLen int
	}
	tests := []struct { //nolint:wsl
		name    string
		args    args
		want    want
		wantErr bool
	}{
		{
			name: "load embedded on timeout",
			args: args{"https://www.google.com:81/", 1 * time.Second, embeddedSpotData},
			want: want{embedded: true, rangesLen: 1}, // At least 1 range expected
		},
		{
			name: "load embedded on not found",
			args: args{"https://notfound", 1 * time.Second, embeddedSpotData},
			want: want{embedded: true, rangesLen: 1},
		},
		{
			name: "load embedded on unexpected response",
			args: args{"https://www.example.com", 1 * time.Second, embeddedSpotData},
			want: want{embedded: true, rangesLen: 1},
		},
		{
			name: "load data from spot advisor",
			args: args{spotAdvisorJSONURL, 10 * time.Second, embeddedSpotData},
			want: want{embedded: false, rangesLen: 1},
		},
		{
			name:    "fail on empty embedded data",
			args:    args{"", 1 * time.Second, ""},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := dataLazyLoad(tt.args.url, tt.args.timeout, tt.args.fallback)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, tt.want.embedded, got.Embedded)
			assert.GreaterOrEqual(t, len(got.Ranges), tt.want.rangesLen, "Should have at least minimum expected ranges")

			// Validate ranges structure
			for _, rangeData := range got.Ranges {
				assert.NotEmpty(t, rangeData, "Range data should not be empty")
			}
		})
	}
}

//nolint:funlen,gocognit,gocyclo
func TestGetSpotSavings(t *testing.T) { //nolint:cyclop
	t.Parallel()
	type args struct {
		pattern    string
		regions    []string
		instanceOS string
		cpu        int
		memory     int
		price      float64
		sortBy     int
		sortDesc   bool
	}
	type want struct { //nolint:wsl
		minCPU    int
		minMemory float32
		maxPrice  float64
	}
	tests := []struct { //nolint:wsl
		name    string
		args    args
		want    want
		wantErr bool
	}{
		{
			name: "get advice by pattern",
			args: args{pattern: "^(m5)(\\S)*", regions: []string{"us-east-1"}, instanceOS: "linux"},
		},
		{
			name: "get advice by pattern multi-regional",
			args: args{pattern: "^(m5)(\\S)*", regions: []string{"us-east-1", "us-west-2", "eu-central-1"}, instanceOS: "linux"},
		},
		{
			name: "get advice by pattern sorted by price",
			args: args{pattern: "^(m5)(\\S)*", regions: []string{"us-east-1"}, instanceOS: "linux", sortBy: SortByPrice},
		},
		{
			name: "get advice by pattern sorted by interruption frequency (descending)",
			args: args{pattern: "^(m5)(\\S)*", regions: []string{"us-east-1"}, instanceOS: "linux", sortBy: SortByRange, sortDesc: true},
		},
		{
			name: "get advice by pattern sorted by instance type (descending)",
			args: args{pattern: "^(m5)(\\S)*", regions: []string{"us-east-1"}, instanceOS: "linux", sortBy: SortByInstance, sortDesc: true},
		},
		{
			name: "get advice by pattern min.cpu=64 and min.memory=128",
			args: args{pattern: "^(m5)(\\S)*", regions: []string{"us-east-1"}, instanceOS: "linux", cpu: 64, memory: 128},
			want: want{minCPU: 64, minMemory: 128},
		},
		{
			name: "get advice by pattern min.cpu=4, min.memory=16 and max.price $1.00/hour",
			args: args{pattern: "^(m5)(\\S)*", regions: []string{"us-east-1"}, instanceOS: "linux", cpu: 4, memory: 16, price: 1.0},
			want: want{minCPU: 4, minMemory: 16, maxPrice: 1.0},
		},
		{
			name:    "fail on bad regexp pattern",
			args:    args{pattern: "a(b", regions: []string{"us-east-1"}, instanceOS: "linux"},
			wantErr: true,
		},
		{
			name:    "fail on non-existing region",
			args:    args{pattern: "m3.medium", regions: []string{"non-existing"}, instanceOS: "linux"},
			wantErr: true,
		},
		{
			name:    "fail on non-existing os",
			args:    args{pattern: "m3.medium", regions: []string{"us-east-1"}, instanceOS: "reactos"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := GetSpotSavings(tt.args.regions, tt.args.pattern, tt.args.instanceOS, tt.args.cpu, tt.args.memory, tt.args.price, tt.args.sortBy, tt.args.sortDesc)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, got)

			// Validate pattern matching for all results
			for _, advice := range got {
				matched, err := regexp.MatchString(tt.args.pattern, advice.Instance)
				require.NoError(t, err, "Invalid regex pattern")
				assert.True(t, matched, "Instance %s should match pattern %s", advice.Instance, tt.args.pattern)

				// Validate CPU constraints
				if tt.want.minCPU > 0 {
					assert.GreaterOrEqual(t, advice.Info.Cores, tt.want.minCPU, "CPU cores should meet minimum requirement")
				}

				// Validate memory constraints
				if tt.want.minMemory > 0 {
					assert.GreaterOrEqual(t, advice.Info.RAM, tt.want.minMemory, "RAM should meet minimum requirement")
				}

				// Validate price constraints
				if tt.want.maxPrice > 0 {
					assert.LessOrEqual(t, advice.Price, tt.want.maxPrice, "Price should not exceed maximum")
				}

				// Validate required fields are populated
				assert.NotEmpty(t, advice.Instance, "Instance type should not be empty")
				assert.NotEmpty(t, advice.Region, "Region should not be empty")
				assert.Greater(t, advice.Price, 0.0, "Price should be positive")
			}

			// Validate sorting if specified
			if tt.args.sortBy != 0 {
				assertSorted(t, got, tt.args.sortBy, tt.args.sortDesc)
			}
		})
	}
}
