package spot

import (
	_ "embed" //nolint:gci
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
)

var (
	loadDataOnce sync.Once
	//go:embed data/spot-advisor-data.json
	embeddedSpotData string
	// parsed json raw data
	data *advisorData
	// min ranges
	minRange = map[int]int{5: 0, 11: 6, 16: 12, 22: 17, 100: 23} //nolint:gomnd
)

const (
	// SortByRange sort by frequency of interruption
	SortByRange = iota
	// SortByInstance sort by instance type (lexicographical)
	SortByInstance = iota
	// SortBySavings sort by savings percentage
	SortBySavings = iota
	// SortByPrice sort by spot price
	SortByPrice = iota
	// SortByRegion sort by AWS region name
	SortByRegion       = iota
	spotAdvisorJSONURL = "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json"
)

type interruptionRange struct {
	Label string `json:"label"`
	Index int    `json:"index"`
	Dots  int    `json:"dots"`
	Max   int    `json:"max"`
}

type instanceType struct {
	Cores int     `json:"cores"`
	Emr   bool    `json:"emr"`
	RAM   float32 `json:"ram_gb"` //nolint:tagliatelle
}

type advice struct {
	Range   int `json:"r"`
	Savings int `json:"s"`
}

type osTypes struct {
	Windows map[string]advice `json:"Windows"` //nolint:tagliatelle
	Linux   map[string]advice `json:"Linux"`   //nolint:tagliatelle
}

type advisorData struct {
	Ranges        []interruptionRange     `json:"ranges"`
	InstanceTypes map[string]instanceType `json:"instance_types"` //nolint:tagliatelle
	Regions       map[string]osTypes      `json:"spot_advisor"`   //nolint:tagliatelle
	Embedded      bool                    // true if loaded from embedded copy
}

//---- public types

// Range interruption range
type Range struct {
	Label string `json:"label"`
	Min   int    `json:"min"`
	Max   int    `json:"max"`
}

// TypeInfo instance type details: vCPU cores, memory, cam  run in EMR
type TypeInfo instanceType

// Advice - spot price advice: interruption range and savings
type Advice struct {
	Region    string
	Instance  string
	Range     Range
	Savings   int
	Info      TypeInfo
	Price     float64
	ZonePrice map[string]float64
}

// ByRange implements sort.Interface based on the Range.Min field
type ByRange []Advice

func (a ByRange) Len() int           { return len(a) }
func (a ByRange) Less(i, j int) bool { return a[i].Range.Min < a[j].Range.Min }
func (a ByRange) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// ByInstance implements sort.Interface based on the Instance field
type ByInstance []Advice

func (a ByInstance) Len() int           { return len(a) }
func (a ByInstance) Less(i, j int) bool { return strings.Compare(a[i].Instance, a[j].Instance) == -1 }
func (a ByInstance) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// BySavings implements sort.Interface based on the Savings field
type BySavings []Advice

func (a BySavings) Len() int           { return len(a) }
func (a BySavings) Less(i, j int) bool { return a[i].Savings < a[j].Savings }
func (a BySavings) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// ByPrice implements sort.Interface based on the Price field
type ByPrice []Advice

func (a ByPrice) Len() int           { return len(a) }
func (a ByPrice) Less(i, j int) bool { return a[i].Price < a[j].Price }
func (a ByPrice) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// ByRegion implements sort.Interface based on the Region field
type ByRegion []Advice

func (a ByRegion) Len() int           { return len(a) }
func (a ByRegion) Less(i, j int) bool { return strings.Compare(a[i].Region, a[j].Region) == -1 }
func (a ByRegion) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

func dataLazyLoad(url string, timeout time.Duration, fallbackData string) (*advisorData, error) {
	var result advisorData
	// try to load new data
	client := &http.Client{Timeout: timeout}

	resp, err := client.Get(url)
	if err != nil {
		goto fallback
	}

	defer func() {
		err = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		goto fallback
	}

	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		goto fallback
	}

	return &result, nil

	// fallback to embedded load
fallback:
	err = json.Unmarshal([]byte(fallbackData), &result)

	if err != nil {
		return nil, errors.Wrap(err, "failed to parse embedded spot data")
	}

	// set embedded loaded flag true
	result.Embedded = true

	return &result, nil
}

// GetSpotSavings get spot saving advices
//
//nolint:gocognit,gocyclo,cyclop
func GetSpotSavings(regions []string, pattern, instanceOS string, cpu, memory int, price float64, sortBy int, sortDesc bool) ([]Advice, error) {
	var err error

	loadDataOnce.Do(func() {
		const timeout = 10
		data, err = dataLazyLoad(spotAdvisorJSONURL, timeout*time.Second, embeddedSpotData)
	})

	if err != nil {
		return nil, errors.Wrap(err, "failed to load spot data")
	}

	// special case: "all" regions (slice with single element)
	if len(regions) == 1 && regions[0] == "all" {
		// replace regions with all available regions
		regions = make([]string, 0, len(data.Regions))
		for k := range data.Regions {
			regions = append(regions, k)
		}
	}

	// get advices for specified regions
	var result []Advice

	for _, region := range regions {
		r, ok := data.Regions[region]
		if !ok {
			return nil, errors.Errorf("no spot price for region %s", region)
		}

		var advices map[string]advice
		if strings.EqualFold("windows", instanceOS) {
			advices = r.Windows
		} else if strings.EqualFold("linux", instanceOS) {
			advices = r.Linux
		} else {
			return nil, errors.New("invalid instance OS, must be windows/linux")
		}

		// construct advices result
		for instance, adv := range advices {
			// match instance type name
			matched, err := regexp.MatchString(pattern, instance)
			if err != nil {
				return nil, errors.Wrap(err, "failed to match instance type")
			}

			if !matched { // skip not matched
				continue
			}
			// filter by min vCPU and memory
			info := data.InstanceTypes[instance]
			if (cpu != 0 && info.Cores < cpu) || (memory != 0 && info.RAM < float32(memory)) {
				continue
			}
			// get price details
			spotPrice, err := getSpotInstancePrice(instance, region, instanceOS, false)
			if err == nil {
				// filter by max price
				if price != 0 && spotPrice > price {
					continue
				}
			}

			// prepare record
			rng := Range{
				Label: data.Ranges[adv.Range].Label,
				Max:   data.Ranges[adv.Range].Max,
				Min:   minRange[data.Ranges[adv.Range].Max],
			}

			result = append(result, Advice{
				Region:   region,
				Instance: instance,
				Range:    rng,
				Savings:  adv.Savings,
				Info:     TypeInfo(info),
				Price:    spotPrice,
			})
		}
	}

	// sort results by - range (default)
	var data sort.Interface

	switch sortBy {
	case SortByRange:
		data = ByRange(result)
	case SortByInstance:
		data = ByInstance(result)
	case SortBySavings:
		data = BySavings(result)
	case SortByPrice:
		data = ByPrice(result)
	case SortByRegion:
		data = ByRegion(result)
	default:
		data = ByRange(result)
	}

	if sortDesc {
		data = sort.Reverse(data)
	}

	sort.Sort(data)

	return result, nil
}
