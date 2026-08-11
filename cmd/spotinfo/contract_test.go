package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
)

// The goldens in testdata record what `spotinfo list` and `spotinfo recommend`
// print. Both are produced from the one fixed cloud.Provider stub below, never
// from the embedded feeds and never through the production AWS adapter — that
// adapter reads its provenance from the committed sidecar manifests, so a
// golden recorded through it would carry manifest hashes and timestamps and
// every weekly data-refresh pull request would rewrite a contract nobody meant
// to change.
//
// Regenerate deliberately with UPDATE_GOLDEN=1 and review the diff.

// contractScoreFetchedAt is the instant the zonal scores were measured. It is
// fixed and in the past on purpose: addFreshnessInfo marks a score older than
// thirty minutes with an asterisk, so a fixture stamped near "now" would make
// the goldens flap. The regional score carries no timestamp, which records the
// other branch in the same page.
var contractScoreFetchedAt = time.Date(2026, time.August, 6, 8, 58, 27, 0, time.UTC)

// contractCandidates is the fixed answer both golden sets are rendered from.
//
// It covers every branch the four rendered formats have: a static price and a
// live one, a regional placement score and a pair of zonal ones with their own
// zone prices, and — the row the plan asks for by name — a machine the cloud
// published no price for. The AWS static price feed omits whole families and
// every me-* region, 600 rows of 19,353 under --region all, so that row is a
// real shape and not a hypothetical one.
func contractCandidates() []cloud.Candidate {
	return []cloud.Candidate{
		contractCandidate("us-east-1", "m6i.large", "0.041600000", 2, 8, 72, advisorRisk("<5%", 0, 5)),
		liveCandidate(
			contractCandidate("us-west-2", "m5.xlarge", "0.123400000", 4, 16, 65, advisorRisk("5-10%", 5, 11)),
			regionalScore("us-west-2", 8),
		),
		contractCandidate("eu-west-1", "t3.nano", "0.001600000", 2, 0.5, 60, advisorRisk("10-15%", 10, 16)),
		zonalCandidate(
			contractCandidate("us-east-1", "m5.large", "0.035000000", 2, 8, 68, advisorRisk("<5%", 0, 5)),
			map[string]int{"us-east-1a": 7, "us-east-1b": 9},
			map[string]string{"us-east-1a": "0.034000000", "us-east-1b": "0.036000000"},
		),
		unpricedCandidate(
			contractCandidate("me-south-1", "dl1.24xlarge", "", 96, 768, 41, advisorRisk("10-15%", 10, 16)),
		),
	}
}

// contractCandidate builds one priced Linux x86_64 machine. The architecture is
// stated rather than left empty: `spotinfo list --cloud aws` publishes an empty
// architecture unless --architecture asks for one, and freezing that shape into
// a recorded contract is exactly what this fixture exists to avoid.
func contractCandidate(region, machine, price string, vcpu int, memoryGiB float64,
	savings int, risk cloud.RiskObservation,
) cloud.Candidate {
	location := cloud.Location{Region: cloud.Region(region)}
	percent := savings

	candidate := cloud.Candidate{
		Provider: cloud.ProviderAWS,
		OS:       cloud.OSLinux,
		Location: location,
		Machine: cloud.MachineSpec{
			ID: cloud.MachineID(machine), Architecture: cloud.ArchitectureX8664,
			MemoryGiB: memoryGiB, VCPU: vcpu,
		},
		SavingsPercent: &percent,
		Risk:           risk,
	}
	if price != "" {
		observation := spotPrice(location, price, false)
		candidate.Spot = &observation
	}

	return candidate
}

// unpricedCandidate drops the price observation. An unknown price is the
// absence of an observation, never a zero: the JSON form publishes null and the
// rendered formats print "-".
func unpricedCandidate(candidate cloud.Candidate) cloud.Candidate {
	candidate.Spot = nil

	return candidate
}

// liveCandidate marks the price as fetched from the EC2 API rather than read
// from the static feed, which every format publishes: a "*" suffix in text and
// table, a "Price Source" column in CSV, live_price in JSON.
func liveCandidate(candidate cloud.Candidate, placements ...cloud.PlacementObservation) cloud.Candidate {
	candidate.Spot.Live = true
	candidate.Placements = placements

	return candidate
}

func regionalScore(region string, score int) cloud.PlacementObservation {
	return cloud.PlacementObservation{
		Location: cloud.Location{Region: cloud.Region(region)},
		Kind:     cloud.PlacementKindPlacementScore,
		Score:    score,
	}
}

// zonalCandidate carries one score and one price per zone, which is the shape
// expandAZ turns into a row per zone.
func zonalCandidate(candidate cloud.Candidate, scores map[string]int, prices map[string]string) cloud.Candidate {
	for _, zone := range sortedKeys(scores) {
		candidate.Placements = append(candidate.Placements, cloud.PlacementObservation{
			FetchedAt: &contractScoreFetchedAt,
			Location:  cloud.Location{Region: candidate.Location.Region, Zone: zone},
			Kind:      cloud.PlacementKindPlacementScore,
			Score:     scores[zone],
		})
	}

	for _, zone := range sortedKeys(prices) {
		location := cloud.Location{Region: candidate.Location.Region, Zone: zone}
		candidate.ZonePrices = append(candidate.ZonePrices, spotPrice(location, prices[zone], false))
	}

	return candidate
}

func spotPrice(location cloud.Location, amount string, live bool) cloud.PriceObservation {
	money, err := cloud.ParseMoney(amount)
	if err != nil {
		panic(err)
	}

	return cloud.PriceObservation{
		Location: location, Class: cloud.PriceClassSpot, Currency: cloud.CurrencyUSD,
		Unit: cloud.BillingUnitInstanceHour, Amount: money, Live: live,
	}
}

func advisorRisk(label string, minPercent, maxPercent float64) cloud.RiskObservation {
	return cloud.RiskObservation{
		MinPercent: &minPercent,
		MaxPercent: &maxPercent,
		Window:     &cloud.HistoryWindow{Days: 30},
		Status:     cloud.RiskStatusAvailable,
		Kind:       cloud.RiskKindInterruptionFrequencyRange,
		Label:      label,
		SourceURL:  "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json",
	}
}

// contractSources is provenance shaped like the AWS sidecar manifests, with the
// instants and digests fixed here rather than read from them — they are fixture
// values, not the digest of anything this repository ships. Neither source is
// scoped, so each one describes every row and nothing is trimmed; per-scope
// trimming is Azure's shape and is covered in internal/cloud.
func contractSources() []cloud.SourceRef {
	fetchedAt := time.Date(2026, time.August, 6, 8, 58, 27, 0, time.UTC)

	return []cloud.SourceRef{
		{
			FetchedAt:     fetchedAt,
			URL:           "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json",
			ContentSHA256: "f42df66cd52c9dc3ac28b6bb7e525627696eec60692d5cf56658c679f0012393",
			ParserVersion: "aws-spot-advisor-json/1",
			SchemaVersion: "aws.spot-advisor-feed/v1",
		},
		{
			FetchedAt:     fetchedAt,
			URL:           "https://website.spot.ec2.aws.a2z.com/spot.json",
			ContentSHA256: "9d5a4d0b1a4a1f9e1f6f8f0b0d3a4c5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c",
			ParserVersion: "aws-spot-price-json/1",
			SchemaVersion: "aws.spot-price-feed/v1",
		},
	}
}

// contractProvider is the fixed provider both golden sets are recorded from.
func contractProvider() stubProvider {
	return stubProvider{
		id:           cloud.ProviderAWS,
		capabilities: awsCapabilities(),
		result: cloud.Result{
			Provider:   cloud.ProviderAWS,
			Mode:       cloud.DataModeEmbeddedSnapshot,
			Sources:    contractSources(),
			Candidates: contractCandidates(),
		},
	}
}

// Every rendering a caller reads is part of the contract, JSON included: the
// document `spotinfo list --output json` prints is validated field by field
// against docs/plans/contracts/list-v1.schema.json in internal/mcp, and
// recorded here so a change to what it *says* is visible as a diff.
func TestListOutputMatchesTheRecordedContract(t *testing.T) {
	text.DisableColors()
	t.Cleanup(text.EnableColors)

	for format, golden := range map[string]string{
		"table":  "list-v1.table.txt",
		"text":   "list-v1.text.txt",
		"csv":    "list-v1.csv",
		"number": "list-v1.number.txt",
		"json":   "list-v1.json",
	} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer

			app := newSpotinfoApp(
				func(ctx *cli.Context) error {
					// The sort key is parsed the way execListCmd parses it, so the
					// recorded page keeps tracking the production default.
					sortKey, err := parseSortBy(ctx.String(flagSort))
					require.NoError(t, err)

					return answerList(ctx, context.Background(), contractProvider(), sortKey, &output)
				},
				func(*cli.Context) error { return nil },
			)

			require.NoError(t, app.Run([]string{
				appName, listCommandName, "--region", "all", "--with-score", "--output", format,
			}))
			assertGolden(t, golden, output.Bytes())
		})
	}
}

// The ranked page is recorded from the same stub, which is what makes the two
// documents comparable: the same five machines, filtered by the same floors,
// and the reader can see which rows a recommendation drops and why. The unpriced
// machine is one of them — accepts() refuses a candidate with no price before
// ranking, so spot_usd_per_hour is nullable in spotinfo.list/v1 and not in
// spotinfo.recommend/v3.
func TestRecommendOutputMatchesTheRecordedContract(t *testing.T) {
	registry := mustRegistry(registrationOf(contractProvider()))

	for format, golden := range map[string]string{
		"table": "recommend-v3.table.txt",
		"json":  "recommend-v3.json",
	} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer

			app := newSpotinfoApp(
				func(*cli.Context) error { return nil },
				func(ctx *cli.Context) error {
					return execRecommendCmd(ctx, context.Background(), registry, &output)
				},
			)

			require.NoError(t, app.Run(recommendArgs("--region", "all", "--top", "3", "--output", format)))
			assertGolden(t, golden, output.Bytes())
		})
	}
}

// Every rendered price makes a round trip the recorded contract has to survive:
// the wire form is fixed-point Money and the four rendered formats print the
// float64 it converts to. A price that did not survive that conversion would
// render differently from the amount the JSON document publishes.
func TestGoldenPricesSurviveTheFixedPointRoundTrip(t *testing.T) {
	t.Parallel()

	shortest := func(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

	for _, candidate := range contractCandidates() {
		prices := candidate.ZonePrices
		if candidate.Spot != nil {
			prices = append(prices, *candidate.Spot)
		}

		for _, price := range prices {
			restored, err := cloud.MoneyFromFloat(price.Amount.Float64())
			require.NoError(t, err, "%s: the golden price must survive the float the renderers print",
				candidate.Machine.ID)
			assert.Equal(t, shortest(price.Amount.Float64()), shortest(restored.Float64()),
				"%s: the price moved crossing the fixed-point seam", candidate.Machine.ID)
		}
	}
}

// assertGolden compares against a recorded contract file. A regeneration always
// fails the run: rewriting the file and then comparing it to itself would let a
// job that happens to set UPDATE_GOLDEN report a client-visible contract change
// as a pass.
func assertGolden(t *testing.T, name string, actual []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(path, actual, 0o600))
		t.Fatalf("%s regenerated; review the diff and re-run without UPDATE_GOLDEN", name)
	}

	expected, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(expected), string(actual),
		"%s records the published CLI output contract; regenerate with UPDATE_GOLDEN=1 and review the diff", name)
}
