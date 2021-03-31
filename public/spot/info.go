package spot

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
)

var (
	doOnce sync.Once
	//go:embed data/spot-advisor-data.json
	jsonData string
	// parsed json raw data
	data *advisorData
	// min ranges
	minRange = map[int]int{5: 0, 11: 6, 16: 12, 22: 17, 100: 23}
)

const (
	SortByRange        = iota
	SortByInstance     = iota
	SortBySavings      = iota
	spotAdvisorJsonUrl = "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json"
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
	Ram   float32 `json:"ram_gb"`
}

type advice struct {
	Range   int `json:"r"`
	Savings int `json:"s"`
}

type osTypes struct {
	Windows map[string]advice `json:"Windows"`
	Linux   map[string]advice `json:"Linux"`
}

type advisorData struct {
	Ranges        []interruptionRange     `json:"ranges"`
	InstanceTypes map[string]instanceType `json:"instance_types"`
	Regions       map[string]osTypes      `json:"spot_advisor"`
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
	Instance string
	Range    Range
	Savings  int
	Info     TypeInfo
}

// ByRange implements sort.Interface based on the Range.Min field
type ByRange []Advice

func (a ByRange) Len() int           { return len(a) }
func (a ByRange) Less(i, j int) bool { return a[i].Range.Min < a[j].Range.Min }
func (a ByRange) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// ByInstance implements sort.Interface based on the Instance field
type ByInstance []Advice

func (a ByInstance) Len() int           { return len(a) }
func (a ByInstance) Less(i, j int) bool { return strings.Compare(a[i].Instance, a[j].Instance) <= 0 }
func (a ByInstance) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// By implements sort.Interface based on the Savings field
type BySavings []Advice

func (a BySavings) Len() int           { return len(a) }
func (a BySavings) Less(i, j int) bool { return a[i].Savings < a[j].Savings }
func (a BySavings) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

func lazyLoad(url string, timeout time.Duration, fallbackData string) (*advisorData, error) {
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

func GetSpotSavings(pattern, region, instanceOS string, cpu, memory, sortBy int) ([]Advice, error) {
	var err error
	doOnce.Do(func() {
		data, err = lazyLoad(spotAdvisorJsonUrl, 10*time.Second, jsonData)
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to load spot data")
	}
	r, ok := data.Regions[region]
	if !ok {
		return nil, fmt.Errorf("no spot price for region %s", region)
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
	var result []Advice
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
		if (cpu != 0 && info.Cores < cpu) || (memory != 0 && info.Ram < float32(memory)) {
			continue
		}
		// prepare record
		rng := Range{
			Label: data.Ranges[adv.Range].Label,
			Max:   data.Ranges[adv.Range].Max,
			Min:   minRange[data.Ranges[adv.Range].Max],
		}
		result = append(result, Advice{
			instance,
			rng,
			adv.Savings,
			TypeInfo(info),
		})
	}
	// sort by - range (default)
	switch sortBy {
	case SortByRange:
		sort.Sort(ByRange(result))
	case SortByInstance:
		sort.Sort(ByInstance(result))
	case SortBySavings:
		sort.Sort(BySavings(result))
	default:
		sort.Sort(ByRange(result))
	}
	return result, nil
}
