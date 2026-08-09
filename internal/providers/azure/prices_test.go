package azure

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

// snapshotTime is the instant every effective-date test resolves against. It is
// fixed so the fixtures describe intervals rather than "recently".
var snapshotTime = time.Date(2026, time.August, 9, 0, 0, 0, 0, time.UTC)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)

	return data
}

func fixtureItems(t *testing.T, names ...string) []RetailItem {
	t.Helper()

	pages := make([][]byte, 0, len(names))
	for _, name := range names {
		pages = append(pages, readFixture(t, name))
	}

	items, err := DecodePages(pages)
	require.NoError(t, err)

	return items
}

func TestPageDecodingFollowsPagination(t *testing.T) {
	t.Parallel()

	first, err := DecodeRetailPage(bytes.NewReader(readFixture(t, "retail-page-1.json")))
	require.NoError(t, err)
	assert.NotEmpty(t, first.NextPageLink, "the first page must advertise the next one")

	last, err := DecodeRetailPage(bytes.NewReader(readFixture(t, "retail-page-2.json")))
	require.NoError(t, err)
	assert.Empty(t, last.NextPageLink, "the last page ends the sweep")

	assert.Len(t, first.Items, first.Count)
	assert.Len(t, last.Items, last.Count)
}

// TestOnlyLinuxSpotAndListMetersAreAccepted pins the classification rules. Each
// excluded row is a real shape the API returns, and each would be wrong in a
// different way if it were published.
func TestOnlyLinuxSpotAndListMetersAreAccepted(t *testing.T) {
	t.Parallel()

	observations, err := AcceptRows(fixtureItems(t, "retail-page-1.json"))
	require.NoError(t, err)

	got := make(map[cloud.PriceClass]cloud.Money, len(observations))
	for _, observation := range observations {
		assert.Equal(t, cloud.MachineID("Standard_D2s_v5"), observation.Machine,
			"every other row on the page is out of scope")
		got[observation.Class] = observation.Amount
	}

	require.Len(t, got, 2, "one spot and one on-demand row survive")
	assert.Equal(t, "0.041200000", got[cloud.PriceClassSpot].String())
	assert.Equal(t, "0.096000000", got[cloud.PriceClassOnDemand].String(),
		"the Cloud Services rate for the same size must not win")
}

// TestLowPriorityIsNotSpot is called out on its own because it is the mistake a
// future reader is most likely to make: the meter looks interruptible and is
// priced like Spot, but it is the retired Batch product.
func TestLowPriorityIsNotSpot(t *testing.T) {
	t.Parallel()

	for name, sku := range map[string]string{
		"spot":         "D2s v5 Spot",
		"low priority": "D2s v5 Low Priority",
		"list":         "D2s v5",
	} {
		class, priced := classOf(sku)

		switch name {
		case "spot":
			assert.True(t, priced)
			assert.Equal(t, cloud.PriceClassSpot, class)
		case "low priority":
			assert.False(t, priced, "the legacy Batch meter is not a price this catalogue publishes")
		case "list":
			assert.True(t, priced)
			assert.Equal(t, cloud.PriceClassOnDemand, class)
		}
	}
}

func TestARowOutsideTheContractedRequestFails(t *testing.T) {
	t.Parallel()

	_, err := AcceptRows(fixtureItems(t, "schema-changed.json"))

	require.ErrorIs(t, err, ErrSourceContract)
	assert.Contains(t, err.Error(), "unitOfMeasure")
}

func TestContractFieldsAreCheckedIndividually(t *testing.T) {
	t.Parallel()

	valid := RetailItem{
		ServiceName:        "Virtual Machines",
		Type:               "Consumption",
		UnitOfMeasure:      "1 Hour",
		CurrencyCode:       "USD",
		EffectiveStartDate: snapshotTime,
	}

	for name, mutate := range map[string]func(*RetailItem){
		"another service":  func(i *RetailItem) { i.ServiceName = "Storage" },
		"another type":     func(i *RetailItem) { i.Type = "Reservation" },
		"another unit":     func(i *RetailItem) { i.UnitOfMeasure = "1 Month" },
		"another currency": func(i *RetailItem) { i.CurrencyCode = "EUR" },
		"no start date":    func(i *RetailItem) { i.EffectiveStartDate = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			item := valid
			mutate(&item)
			assert.ErrorIs(t, item.checkContract(), ErrSourceContract)
		})
	}

	require.NoError(t, valid.checkContract())
}

// TestCurrentIntervalWins covers the three outcomes of the effective-date rule
// against the shapes the API actually returns.
func TestCurrentIntervalWins(t *testing.T) {
	t.Parallel()

	observations, err := AcceptRows(fixtureItems(t, "retail-page-2.json"))
	require.NoError(t, err)

	rows, expired, err := SelectCurrent(observations, snapshotTime)
	require.NoError(t, err)

	prices := make(map[string]string, len(rows))
	for _, row := range rows {
		prices[string(row.Machine)+"/"+string(row.Class)] = row.Amount.String()
	}

	assert.Equal(t, "0.082400000", prices["Standard_D4s_v5/spot"],
		"the interval in force replaces the one that ended in June")
	assert.Equal(t, "0.192000000", prices["Standard_D4s_v5/on-demand"])
	assert.NotContains(t, prices, "Standard_D8s_v5/spot",
		"an expired price with no replacement is dropped, not carried forward")
	assert.Equal(t, []string{"eastus/Standard_D8s_v5/spot"}, expired,
		"what was dropped is reported so a shrinking snapshot is visible")
}

func TestTwoPricesInEffectAtOnceFailClosed(t *testing.T) {
	t.Parallel()

	observations, err := AcceptRows(fixtureItems(t, "duplicate-current.json"))
	require.NoError(t, err)

	_, _, err = SelectCurrent(observations, snapshotTime)

	require.ErrorIs(t, err, ErrAmbiguousPrice)
	assert.Contains(t, err.Error(), "Standard_D2s_v5")
}

// TestRepeatedIdenticalPriceIsNotAmbiguous separates redundancy from conflict:
// the same amount twice is harmless, which is why the fixture carries both.
func TestRepeatedIdenticalPriceIsNotAmbiguous(t *testing.T) {
	t.Parallel()

	observations, err := AcceptRows(fixtureItems(t, "duplicate-current.json"))
	require.NoError(t, err)

	repeated := make([]Observation, 0, 2)
	for _, observation := range observations {
		if observation.Amount.String() == "0.041200000" {
			repeated = append(repeated, observation)
		}
	}
	require.Len(t, repeated, 2)

	rows, _, err := SelectCurrent(repeated, snapshotTime)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "0.041200000", rows[0].Amount.String())
}

// TestSelectionIsRelativeToTheGivenInstant proves `at` is a real input: the same
// rows resolve to the June price when asked about June.
func TestSelectionIsRelativeToTheGivenInstant(t *testing.T) {
	t.Parallel()

	observations, err := AcceptRows(fixtureItems(t, "retail-page-2.json"))
	require.NoError(t, err)

	june := time.Date(2026, time.June, 15, 0, 0, 0, 0, time.UTC)

	rows, _, err := SelectCurrent(observations, june)
	require.NoError(t, err)

	var spot string
	for _, row := range rows {
		if row.Machine == "Standard_D4s_v5" && row.Class == cloud.PriceClassSpot {
			spot = row.Amount.String()
		}
	}

	assert.Equal(t, "0.077700000", spot)
}

func TestRequestURLCarriesTheContractedFilter(t *testing.T) {
	t.Parallel()

	target, err := RetailRequestURL("https://prices.azure.com/api/retail/prices", "westeurope")
	require.NoError(t, err)

	for _, fragment := range []string{
		"api-version=" + RetailAPIVersion,
		"currencyCode=USD",
		"serviceName+eq+%27Virtual+Machines%27",
		"armRegionName+eq+%27westeurope%27",
		"priceType+eq+%27Consumption%27",
	} {
		assert.Contains(t, target, fragment)
	}
}

func TestMalformedResponseIsRejected(t *testing.T) {
	t.Parallel()

	_, err := DecodeRetailPage(bytes.NewReader([]byte("<html>service unavailable</html>")))

	require.ErrorIs(t, err, ErrSourceContract)
}
