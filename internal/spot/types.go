// Package spot provides functionality for retrieving AWS EC2 Spot instance pricing and advice.
package spot

import (
	"sort"
	"time"
)

// Range represents an interruption range for spot instances.
type Range struct {
	Label string `json:"label"`
	Min   int    `json:"min"`
	Max   int    `json:"max"`
}

// TypeInfo contains instance type details: vCPU cores, memory, and EMR compatibility.
type TypeInfo struct {
	Cores int     `json:"cores"`
	EMR   bool    `json:"emr"`
	RAM   float32 `json:"ram_gb"` //nolint:tagliatelle
}

// Advice represents spot price advice including interruption range and savings.
type Advice struct { //nolint:govet
	Region         string             `json:"region"`
	Instance       string             `json:"instance"`
	InstanceType   string             `json:"instance_type"`
	Range          Range              `json:"range"`
	Savings        int                `json:"savings"`
	Info           TypeInfo           `json:"info"`
	Price          float64            `json:"price"`
	ZonePrice      map[string]float64 `json:"zone_price,omitempty"`
	RegionScore    *int               `json:"region_score,omitempty"`
	ZoneScores     map[string]int     `json:"zone_scores,omitempty"`
	ScoreFetchedAt *time.Time         `json:"score_fetched_at,omitempty"`
}

// SortBy defines the sorting criteria for advice results.
type SortBy int

const (
	// SortByRange sorts by frequency of interruption.
	SortByRange SortBy = iota
	// SortByInstance sorts by instance type (lexicographical).
	SortByInstance
	// SortBySavings sorts by savings percentage.
	SortBySavings
	// SortByPrice sorts by spot price.
	SortByPrice
	// SortByRegion sorts by AWS region name.
	SortByRegion
	// SortByScore sorts by spot placement score.
	SortByScore
)

// ByRange implements sort.Interface based on the Range.Min field.
type ByRange []Advice

func (a ByRange) Len() int           { return len(a) }
func (a ByRange) Less(i, j int) bool { return a[i].Range.Min < a[j].Range.Min }
func (a ByRange) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// ByInstance implements sort.Interface based on the Instance field.
type ByInstance []Advice

func (a ByInstance) Len() int           { return len(a) }
func (a ByInstance) Less(i, j int) bool { return a[i].Instance < a[j].Instance }
func (a ByInstance) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// BySavings implements sort.Interface based on the Savings field.
type BySavings []Advice

func (a BySavings) Len() int           { return len(a) }
func (a BySavings) Less(i, j int) bool { return a[i].Savings < a[j].Savings }
func (a BySavings) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// ByPrice implements sort.Interface based on the Price field.
type ByPrice []Advice

func (a ByPrice) Len() int           { return len(a) }
func (a ByPrice) Less(i, j int) bool { return a[i].Price < a[j].Price }
func (a ByPrice) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// ByRegion implements sort.Interface based on the Region field.
type ByRegion []Advice

func (a ByRegion) Len() int           { return len(a) }
func (a ByRegion) Less(i, j int) bool { return a[i].Region < a[j].Region }
func (a ByRegion) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// ByScore implements sort.Interface based on the RegionScore field with nil-safe comparison.
type ByScore []Advice

func (a ByScore) Len() int { return len(a) }
func (a ByScore) Less(i, j int) bool {
	// Handle nil scores safely
	if a[i].RegionScore == nil && a[j].RegionScore == nil {
		return false // Both nil, maintain order
	}
	if a[i].RegionScore == nil {
		return false // nil scores go to end
	}
	if a[j].RegionScore == nil {
		return true // non-nil before nil
	}
	return *a[i].RegionScore > *a[j].RegionScore // Higher scores first
}
func (a ByScore) Swap(i, j int) { a[i], a[j] = a[j], a[i] }

// sortAdvices sorts the advice slice according to the specified criteria.
func sortAdvices(advices []Advice, sortBy SortBy, sortDesc bool) {
	var data sort.Interface

	switch sortBy {
	case SortByRange:
		data = ByRange(advices)
	case SortByInstance:
		data = ByInstance(advices)
	case SortBySavings:
		data = BySavings(advices)
	case SortByPrice:
		data = ByPrice(advices)
	case SortByRegion:
		data = ByRegion(advices)
	case SortByScore:
		data = ByScore(advices)
	default:
		data = ByRange(advices)
	}

	if sortDesc {
		data = sort.Reverse(data)
	}

	sort.Sort(data)
}

// filterByMinScore filters advices to only include those with a minimum region score.
func filterByMinScore(advices []Advice, minScore int) []Advice {
	var filtered []Advice
	for _, adv := range advices {
		if adv.RegionScore != nil && *adv.RegionScore >= minScore {
			filtered = append(filtered, adv)
		}
	}
	return filtered
}

// interruptionRange represents AWS spot instance interruption frequency ranges.
type interruptionRange struct {
	Label string `json:"label"`
	Index int    `json:"index"`
	Dots  int    `json:"dots"`
	Max   int    `json:"max"`
}

// instanceType represents AWS EC2 instance type specifications.
type instanceType struct {
	Cores int     `json:"cores"`
	EMR   bool    `json:"emr"`
	RAM   float32 `json:"ram_gb"` //nolint:tagliatelle
}

// spotAdvice represents spot pricing advice for a specific instance type.
type spotAdvice struct {
	Range   int `json:"r"`
	Savings int `json:"s"`
}

// osTypes represents spot pricing data by operating system.
type osTypes struct {
	Windows map[string]spotAdvice `json:"Windows"` //nolint:tagliatelle
	Linux   map[string]spotAdvice `json:"Linux"`   //nolint:tagliatelle
}

// advisorData represents the complete AWS spot advisor dataset.
type advisorData struct { //nolint:govet // Field alignment is less important than JSON tag clarity
	Embedded      bool                    // true if loaded from embedded copy
	Ranges        []interruptionRange     `json:"ranges"`
	Regions       map[string]osTypes      `json:"spot_advisor"`   //nolint:tagliatelle
	InstanceTypes map[string]instanceType `json:"instance_types"` //nolint:tagliatelle
}

// rawPriceData represents the raw AWS spot pricing data structure.
type rawPriceData struct { //nolint:govet
	Embedded bool   `json:"-"` // true if loaded from embedded copy
	Config   config `json:"config"`
}

type config struct {
	Rate         string         `json:"rate"`
	ValueColumns []string       `json:"valueColumns"`
	Currencies   []string       `json:"currencies"`
	Regions      []regionConfig `json:"regions"`
}

type regionConfig struct {
	Region        string               `json:"region"`
	InstanceTypes []instanceTypeConfig `json:"instanceTypes"`
}

type instanceTypeConfig struct {
	Type  string       `json:"type"`
	Sizes []sizeConfig `json:"sizes"`
}

type sizeConfig struct {
	Size         string              `json:"size"`
	ValueColumns []valueColumnConfig `json:"valueColumns"`
}

type valueColumnConfig struct {
	Name   string      `json:"name"`
	Prices priceConfig `json:"prices"`
}

type priceConfig struct {
	USD string `json:"USD"` //nolint:tagliatelle
}

// instancePrice represents pricing for an instance type by OS.
type instancePrice struct {
	Linux   float64
	Windows float64
}

// regionPrice represents pricing data for a region.
type regionPrice struct {
	Instance map[string]instancePrice
}

// spotPriceData represents processed spot pricing data.
type spotPriceData struct {
	Region map[string]regionPrice
}
