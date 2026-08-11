// Package azure serves Azure Spot VM candidates from a committed snapshot built
// from two anonymous public sources: the documented Azure Retail Prices API for
// amounts, and Microsoft Learn VM-size pages for vCPU, memory and processor
// architecture. The Retail API publishes no specification and no architecture,
// so neither can be inferred from a size name here.
//
// The committed catalogue covers exactly the regions and machine series named in
// internal/providers/azure/data/source-contract.json. A region outside that list
// yields no candidates rather than a substituted answer.
//
// The same Retail Prices API is read at run time to refresh a named region's
// prices — see liveprice.go. It is anonymous there too: the live path needs no
// credential, cannot reach a source the contract does not name, and falls back
// to the committed catalogue on any failure.
//
// Risk is deliberately absent. Azure publishes eviction rates only through
// Resource Graph and Resource SKUs, both of which need a subscription, so this
// provider has nothing to report and must not let silence be ranked as low
// interruption.
package azure

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"spotinfo/internal/cloud"
)

const (
	// ParserVersion identifies the source contract below — the accepted field
	// values, the Spot rule, and the effective-date rule. Bump it whenever any of
	// those change, so a committed snapshot can never be read as the product of a
	// different parser.
	ParserVersion = "azure-retail-prices/2"
	// CatalogSchemaVersion versions the committed catalogue shape. v2 keys every
	// price row by operating system; a v1 archive has one OS for the whole
	// catalogue and must not be read as a v2 one.
	CatalogSchemaVersion = "spotinfo.azure-catalog/v2"
)

// The exact field values the approved API contract publishes. A row that
// disagrees means the request filter no longer selects what it used to, which is
// a contract failure rather than a row to skip.
const (
	serviceVirtualMachines = "Virtual Machines"
	priceTypeConsumption   = "Consumption"
	unitOfMeasureHour      = "1 Hour"
)

// The sku-name suffixes that classify a meter.
const (
	// suffixSpot marks an interruptible spare-capacity meter.
	suffixSpot = " Spot"
	// suffixLowPriority marks the legacy Batch "low priority" meter. It is a
	// different product with a different eviction model, and treating it as Spot
	// would publish a price for capacity a user cannot request that way.
	suffixLowPriority = " Low Priority"
)

// The productName suffixes that carry the operating system. Microsoft does not
// document the convention, so it is read as a suffix and never as a substring:
// " Windows" ends a licence-bundled meter, " Linux" ends the newer families that
// spell it out, and no suffix at all is Linux on every older family.
//
// The leading space is load-bearing. Matching "Windows" anywhere in the name
// would classify by coincidence, and the rule has to hold for the 4,449 Windows
// rows and the 1,012 " Linux" rows one region publishes.
const (
	windowsMarker = " Windows"
	linuxMarker   = " Linux"
)

// The products the Retail API publishes under serviceName "Virtual Machines"
// that are not a virtual-machine rate. Each is priced against a machine name a
// user recognises, at a rate they cannot buy that way.
//
// Cloud Services is the legacy PaaS product, published against the same
// armSkuName as the real VM meter at a different rate. Dedicated Host prices a
// whole physical host — "FX Series Dedicated Host" is sold as "FXmds Type1
// Spot", which reads as a Spot meter for a machine called FXmds Type1.
//
// Both markers are matched with spaces removed because both spellings are in
// use: "Basv2 Series Cloud Services" beside "Eadsv5 Series CloudServices", and
// "DCadsv6 Series Dedicated Host" beside "DCadsv5 series DedicatedHost".
const (
	cloudServicesMarker = "cloudservices"
	dedicatedHostMarker = "dedicatedhost"
)

// Errors a caller distinguishes. Everything else is an invalid catalogue.
var (
	// ErrSourceContract reports a response that no longer matches the approved
	// parser contract: a changed service, price type, unit, currency, or a field
	// that no longer decodes.
	ErrSourceContract = errors.New("azure retail price response does not match its parser contract")
	// ErrAmbiguousPrice reports one machine with two different prices in effect
	// at the same moment. There is no safe way to choose between them.
	ErrAmbiguousPrice = errors.New("azure publishes two different current prices for one machine")
	// ErrCatalog reports a committed catalogue that contradicts the contract that
	// approved it.
	ErrCatalog = errors.New("invalid azure catalogue")
)

// RetailPage is one Azure Retail Prices API response page.
//
// Unknown fields are accepted: this is a live third-party API that adds fields
// (savings-plan terms, meter identifiers) without changing the ones this parser
// contracts for. The contract is enforced on the values below instead, so a
// renamed field arrives empty and fails rather than being silently ignored.
type RetailPage struct {
	NextPageLink string       `json:"NextPageLink"`
	Items        []RetailItem `json:"Items"`
	Count        int          `json:"Count"`
}

// RetailItem is one priced meter as the API publishes it.
type RetailItem struct {
	// EffectiveEndDate is absent on the row currently in effect.
	EffectiveEndDate   *time.Time `json:"effectiveEndDate"`
	EffectiveStartDate time.Time  `json:"effectiveStartDate"`
	// RetailPrice is decoded as a number literal rather than a float so the
	// published decimal reaches the fixed-point parser exactly as written.
	RetailPrice   json.Number `json:"retailPrice"`
	CurrencyCode  string      `json:"currencyCode"`
	ArmRegionName string      `json:"armRegionName"`
	ArmSkuName    string      `json:"armSkuName"`
	ProductName   string      `json:"productName"`
	SkuName       string      `json:"skuName"`
	ServiceName   string      `json:"serviceName"`
	UnitOfMeasure string      `json:"unitOfMeasure"`
	Type          string      `json:"type"`
}

// Observation is one accepted price row in the neutral vocabulary, still
// carrying the interval it applies to.
type Observation struct {
	End     *time.Time
	Start   time.Time
	Machine cloud.MachineID
	Region  cloud.Region
	OS      cloud.OperatingSystem
	Class   cloud.PriceClass
	Amount  cloud.Money
}

// PriceRow is one resolved price: a machine, in a region, for one operating
// system and purchase class, at the moment the snapshot was taken.
type PriceRow struct {
	Machine cloud.MachineID
	Region  cloud.Region
	OS      cloud.OperatingSystem
	Class   cloud.PriceClass
	Amount  cloud.Money
}

// priceKey identifies a price. The catalogue may publish each of these once.
// The operating system is part of the identity: Azure prices the same size
// twice, once bare and once with a Windows licence, and without it the two
// meters collide and the cheaper one wins by arrival order.
type priceKey struct {
	machine cloud.MachineID
	region  cloud.Region
	os      cloud.OperatingSystem
	class   cloud.PriceClass
}

// RetailAPIVersion is the documented API version this parser is written
// against. It is part of the contract: a different version may change field
// names or add price types the classification rules above do not cover.
const RetailAPIVersion = "2023-01-01-preview"

// RetailRequestURL builds the exact contracted request for one region.
//
// The filter lives beside the response contract because the two are one
// agreement: every value checkContract enforces is a value this filter asked
// for, and changing either alone would let the parser accept rows nobody
// reviewed.
func RetailRequestURL(base string, region cloud.Region) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("%w: %q is not a url: %w", ErrSourceContract, base, err)
	}

	query := url.Values{}
	query.Set("api-version", RetailAPIVersion)
	query.Set("currencyCode", string(cloud.CurrencyUSD))
	query.Set("$filter", fmt.Sprintf("serviceName eq '%s' and armRegionName eq '%s' and priceType eq '%s'",
		serviceVirtualMachines, odataLiteral(string(region)), priceTypeConsumption))
	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}

// odataLiteral escapes a value for an OData string literal. url.Values.Encode
// protects the HTTP layer but not the filter grammar, so a region carrying a
// quote would silently change which meters the sweep selects instead of failing.
func odataLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// DecodeRetailPage reads one API response page.
func DecodeRetailPage(document io.Reader) (*RetailPage, error) {
	var page RetailPage
	if err := json.NewDecoder(document).Decode(&page); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSourceContract, err)
	}

	return &page, nil
}

// AcceptRows reduces one page to the rows this snapshot publishes.
//
// A row that contradicts the contracted request — another service, price type,
// unit, or currency — is an error, because it means the filter stopped selecting
// what it was reviewed to select. A row that is simply not a virtual-machine
// rate is skipped: the Cloud Services and Dedicated Host products, the legacy
// Low Priority meters, rows with no machine identifier, and rows priced at zero.
// Linux and Windows meters are both accepted, and each carries the operating
// system its product name declares.
func AcceptRows(items []RetailItem) ([]Observation, error) {
	accepted := make([]Observation, 0, len(items))

	for i := range items {
		item := &items[i]
		if err := item.checkContract(); err != nil {
			return nil, err
		}

		observation, usable, err := item.observe()
		if err != nil {
			return nil, err
		}
		if !usable {
			continue
		}

		accepted = append(accepted, observation)
	}

	return accepted, nil
}

// checkContract rejects a row the reviewed request should never have returned.
func (i *RetailItem) checkContract() error {
	for _, field := range []struct {
		name   string
		actual string
		want   string
	}{
		{name: "serviceName", actual: i.ServiceName, want: serviceVirtualMachines},
		{name: "type", actual: i.Type, want: priceTypeConsumption},
		{name: "unitOfMeasure", actual: i.UnitOfMeasure, want: unitOfMeasureHour},
		{name: "currencyCode", actual: i.CurrencyCode, want: string(cloud.CurrencyUSD)},
	} {
		if field.actual != field.want {
			return fmt.Errorf("%w: %s is %q, the contracted request returns only %q",
				ErrSourceContract, field.name, field.actual, field.want)
		}
	}

	if i.EffectiveStartDate.IsZero() {
		return fmt.Errorf("%w: %s has no effectiveStartDate", ErrSourceContract, i.SkuName)
	}

	return nil
}

// observe classifies one contract-conforming row. The second result is false for
// a row that is out of scope rather than malformed.
func (i *RetailItem) observe() (Observation, bool, error) {
	if i.ArmSkuName == "" || i.isExcludedProduct() {
		return Observation{}, false, nil
	}

	class, priced := classOf(i.SkuName)
	if !priced {
		return Observation{}, false, nil
	}

	amount, err := cloud.ParseMoney(i.RetailPrice.String())
	if err != nil {
		return Observation{}, false, fmt.Errorf("%w: %s in %s: %w",
			ErrSourceContract, i.ArmSkuName, i.ArmRegionName, err)
	}
	// A zero retail price is a promotional or placeholder meter, not a rate a
	// user can be quoted. It is dropped rather than published as free capacity.
	if amount.IsZero() {
		return Observation{}, false, nil
	}

	return Observation{
		Machine: cloud.MachineID(i.ArmSkuName),
		Region:  cloud.Region(i.ArmRegionName),
		OS:      osOf(i.ProductName),
		Class:   class,
		Amount:  amount,
		Start:   i.EffectiveStartDate,
		End:     i.EffectiveEndDate,
	}, true, nil
}

// isExcludedProduct reports whether a row prices something other than a virtual
// machine: the legacy Cloud Services product, or a dedicated host.
func (i *RetailItem) isExcludedProduct() bool {
	folded := strings.ToLower(strings.ReplaceAll(i.ProductName, " ", ""))

	return strings.Contains(folded, cloudServicesMarker) || strings.Contains(folded, dedicatedHostMarker)
}

// osOf reads the operating system a meter prices from its product name.
//
// The three states are a " Windows" suffix, a " Linux" suffix on the families
// that spell it out, and no suffix, which is Linux on every older family. The
// convention is undocumented, so the defence against it changing is elsewhere:
// a Windows meter that stopped ending in " Windows" would land on the Linux key
// beside the real Linux price, and SelectCurrent fails the refresh on two
// different amounts for one key rather than publishing either.
func osOf(productName string) cloud.OperatingSystem {
	switch {
	case strings.HasSuffix(productName, windowsMarker):
		return cloud.OSWindows
	case strings.HasSuffix(productName, linuxMarker):
		return cloud.OSLinux
	default:
		return cloud.OSLinux
	}
}

// classOf reads the purchase class from a sku name. The legacy Low Priority
// meter reports false: it is neither Spot nor list price.
func classOf(skuName string) (cloud.PriceClass, bool) {
	switch {
	case strings.HasSuffix(skuName, suffixSpot):
		return cloud.PriceClassSpot, true
	case strings.HasSuffix(skuName, suffixLowPriority):
		return "", false
	default:
		return cloud.PriceClassOnDemand, true
	}
}

// SelectCurrent resolves each machine's price at a single instant.
//
// The API returns every interval it knows about, including intervals that have
// expired and intervals that have not started, so the caller must choose. `at` is
// an argument rather than the wall clock so one refresh resolves every region
// against the same moment and a rebuild is reproducible from its inputs.
//
// A key with no interval in effect is dropped and reported: an expired price is
// worse than a missing one. A key with two intervals in effect at different
// amounts is ambiguous and fails the refresh.
func SelectCurrent(observations []Observation, at time.Time) ([]PriceRow, []string, error) {
	current := make(map[priceKey]cloud.Money, len(observations))
	covered := make(map[priceKey]struct{}, len(observations))

	for i := range observations {
		observation := &observations[i]
		key := priceKey{
			machine: observation.Machine,
			region:  observation.Region,
			os:      observation.OS,
			class:   observation.Class,
		}
		covered[key] = struct{}{}

		if !observation.inEffect(at) {
			continue
		}

		if previous, repeated := current[key]; repeated && previous != observation.Amount {
			return nil, nil, fmt.Errorf("%w: %s/%s/%s/%s is priced %s and %s at %s",
				ErrAmbiguousPrice, observation.Region, observation.Machine, observation.OS, observation.Class,
				previous, observation.Amount, at.Format(time.RFC3339))
		}

		current[key] = observation.Amount
	}

	rows := make([]PriceRow, 0, len(current))
	for _, key := range sortedKeys(current) {
		rows = append(rows, PriceRow{
			Machine: key.machine,
			Region:  key.region,
			OS:      key.os,
			Class:   key.class,
			Amount:  current[key],
		})
	}

	return rows, expiredKeys(covered, current), nil
}

// inEffect reports whether an interval covers the instant. An absent end date is
// how the API marks the interval currently in force.
func (o *Observation) inEffect(at time.Time) bool {
	if o.Start.After(at) {
		return false
	}

	return o.End == nil || !o.End.Before(at)
}

// expiredKeys names the machines that had intervals but none in effect, so a
// refresh reports what it dropped instead of quietly shrinking.
func expiredKeys(covered map[priceKey]struct{}, current map[priceKey]cloud.Money) []string {
	var expired []string

	for key := range covered {
		if _, live := current[key]; live {
			continue
		}
		expired = append(expired, fmt.Sprintf("%s/%s/%s/%s", key.region, key.machine, key.os, key.class))
	}
	slices.Sort(expired)

	return expired
}

// sortedKeys orders price keys so a refresh that changes no price produces the
// same catalogue bytes.
func sortedKeys[V any](indexed map[priceKey]V) []priceKey {
	keys := slices.Collect(maps.Keys(indexed))
	slices.SortFunc(keys, func(left, right priceKey) int {
		if order := strings.Compare(string(left.region), string(right.region)); order != 0 {
			return order
		}
		if order := strings.Compare(string(left.machine), string(right.machine)); order != 0 {
			return order
		}
		if order := strings.Compare(string(left.os), string(right.os)); order != 0 {
			return order
		}

		return strings.Compare(string(left.class), string(right.class))
	})

	return keys
}

// decodePages reads a sequence of committed API pages, the shape the fixture
// tests use. The updater paginates itself and calls DecodeRetailPage per page.
func decodePages(pages [][]byte) ([]RetailItem, error) {
	var items []RetailItem

	for _, data := range pages {
		page, err := DecodeRetailPage(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
	}

	return items, nil
}
