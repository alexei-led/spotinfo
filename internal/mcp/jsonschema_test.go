package mcp

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
)

// A deliberately small JSON Schema checker covering exactly the keywords the
// contracts in docs/plans/contracts/ use: type, required, properties,
// additionalProperties, items, enum, const, pattern, minLength, minItems,
// uniqueItems, minimum, maximum and exclusiveMinimum.
//
// Ceiling: it is not a general validator, and it ignores "format" and "default".
// Upgrade trigger: the first contract that uses $ref, allOf, or a conditional.
// At that point promote github.com/santhosh-tekuri/jsonschema/v6 — already in
// the module graph through mcp-go — from indirect to direct and delete this.
//
// TestValidatorRejectsEachWayAPayloadCanViolateAContract is what keeps it
// honest: a validator that silently accepts everything would make every
// contract test below vacuous.

func loadContractSchema(t *testing.T, name string) map[string]any {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "plans", "contracts", name))
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(contents, &schema))

	return schema
}

// assertValidAgainstSchema fails with the path of the first violation.
func assertValidAgainstSchema(t *testing.T, schema map[string]any, payload []byte) {
	t.Helper()

	var document any
	require.NoError(t, json.Unmarshal(payload, &document))
	require.NoError(t, validateAgainst(schema, document, "$"))
}

func validateAgainst(schema map[string]any, value any, path string) error { //nolint:cyclop // one branch per supported keyword
	if err := checkType(schema, value, path); err != nil {
		return err
	}
	if err := checkEnumAndConst(schema, value, path); err != nil {
		return err
	}

	switch typed := value.(type) {
	case map[string]any:
		return validateObject(schema, typed, path)
	case []any:
		return validateArray(schema, typed, path)
	case string:
		return validateString(schema, typed, path)
	case float64:
		return validateNumber(schema, typed, path)
	default:
		return nil
	}
}

func checkType(schema map[string]any, value any, path string) error {
	declared, ok := schema["type"]
	if !ok {
		return nil
	}

	names := make([]string, 0, 2) //nolint:mnd // a nullable type is at most two names
	switch typed := declared.(type) {
	case string:
		names = append(names, typed)
	case []any:
		for _, name := range typed {
			names = append(names, fmt.Sprint(name))
		}
	}

	// A whole number satisfies "number" as well as "integer": JSON has one
	// numeric type, and the schemas distinguish them only to constrain values.
	actual := jsonTypeOf(value)
	if actual == "integer" && slices.Contains(names, "number") {
		return nil
	}
	if !slices.Contains(names, actual) {
		return fmt.Errorf("%s: expected type %v, got %s", path, names, actual)
	}

	return nil
}

func jsonTypeOf(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return jsonTypeString
	case float64:
		if typed == math.Trunc(typed) {
			return "integer"
		}

		return "number"
	case []any:
		return "array"
	default:
		return "object"
	}
}

func checkEnumAndConst(schema map[string]any, value any, path string) error {
	if expected, ok := schema["const"]; ok && fmt.Sprint(expected) != fmt.Sprint(value) {
		return fmt.Errorf("%s: expected const %v, got %v", path, expected, value)
	}

	allowed, ok := schema["enum"].([]any)
	if !ok {
		return nil
	}
	for _, candidate := range allowed {
		if fmt.Sprint(candidate) == fmt.Sprint(value) {
			return nil
		}
	}

	return fmt.Errorf("%s: %v is outside enum %v", path, value, allowed)
}

func validateObject(schema map[string]any, object map[string]any, path string) error {
	properties, _ := schema["properties"].(map[string]any)

	if required, ok := schema["required"].([]any); ok {
		for _, name := range required {
			if _, present := object[fmt.Sprint(name)]; !present {
				return fmt.Errorf("%s: missing required property %v", path, name)
			}
		}
	}
	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		for name := range object {
			if _, declared := properties[name]; !declared {
				return fmt.Errorf("%s: unexpected property %q", path, name)
			}
		}
	}

	for name, value := range object {
		propertySchema, declared := properties[name].(map[string]any)
		if !declared {
			continue
		}
		if err := validateAgainst(propertySchema, value, path+"."+name); err != nil {
			return err
		}
	}

	return nil
}

func validateArray(schema map[string]any, items []any, path string) error {
	if minItems, ok := schema["minItems"].(float64); ok && float64(len(items)) < minItems {
		return fmt.Errorf("%s: expected at least %v items, got %d", path, minItems, len(items))
	}
	if unique, ok := schema["uniqueItems"].(bool); ok && unique {
		seen := make(map[string]struct{}, len(items))
		for _, item := range items {
			key := fmt.Sprint(item)
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("%s: duplicate item %v", path, item)
			}
			seen[key] = struct{}{}
		}
	}

	itemSchema, ok := schema["items"].(map[string]any)
	if !ok {
		return nil
	}
	for i, item := range items {
		if err := validateAgainst(itemSchema, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}

	return nil
}

func validateString(schema map[string]any, value, path string) error {
	if pattern, ok := schema["pattern"].(string); ok {
		matched, err := regexp.MatchString(pattern, value)
		if err != nil {
			return fmt.Errorf("%s: invalid pattern %q: %w", path, pattern, err)
		}
		if !matched {
			return fmt.Errorf("%s: %q does not match %q", path, value, pattern)
		}
	}
	if minLength, ok := schema["minLength"].(float64); ok && float64(len(value)) < minLength {
		return fmt.Errorf("%s: %q is shorter than %v", path, value, minLength)
	}

	return nil
}

func validateNumber(schema map[string]any, value float64, path string) error {
	if minimum, ok := schema["minimum"].(float64); ok && value < minimum {
		return fmt.Errorf("%s: %v is below minimum %v", path, value, minimum)
	}
	if maximum, ok := schema["maximum"].(float64); ok && value > maximum {
		return fmt.Errorf("%s: %v is above maximum %v", path, value, maximum)
	}
	if exclusive, ok := schema["exclusiveMinimum"].(float64); ok && value <= exclusive {
		return fmt.Errorf("%s: %v is not above %v", path, value, exclusive)
	}

	return nil
}

// The validator above is the only thing standing between the published
// contracts and a payload that violates them, and every other test using it
// feeds it a payload expected to pass. If a keyword check silently stopped
// firing, those tests would stay green while the shipped payload broke. This
// one breaks a known-good payload one way at a time and requires a rejection.
func TestValidatorRejectsEachWayAPayloadCanViolateAContract(t *testing.T) {
	t.Parallel()

	schema := loadContractSchema(t, "recommend-v3-success.schema.json")

	valid, err := os.ReadFile(filepath.Join("testdata", "recommend-v3-success.json"))
	require.NoError(t, err)

	var reference map[string]any
	require.NoError(t, json.Unmarshal(valid, &reference))
	require.NoError(t, validateAgainst(schema, reference, "$"), "the reference payload must be valid")

	firstRecommendation := func(document map[string]any) map[string]any {
		recommendations, ok := document["recommendations"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, recommendations)
		first, ok := recommendations[0].(map[string]any)
		require.True(t, ok)

		return first
	}

	for name, corrupt := range map[string]func(map[string]any){
		"missing required key":  func(d map[string]any) { delete(d, "schema_version") },
		"unknown top-level key": func(d map[string]any) { d["surprise"] = true },
		"wrong type":            func(d map[string]any) { d["warnings"] = "none" },
		"value outside an enum": func(d map[string]any) { d["status"] = "partial" },
		// The retired version, so this also pins that a v2 document no longer
		// validates against the published contract.
		"const contradicted":       func(d map[string]any) { d["schema_version"] = "spotinfo.recommend/v2" },
		"nested unknown key":       func(d map[string]any) { firstRecommendation(d)["surprise"] = true },
		"nested missing key":       func(d map[string]any) { delete(firstRecommendation(d), "spot_usd_per_hour") },
		"nested value below a min": func(d map[string]any) { firstRecommendation(d)["rank"] = 0.0 },
		"pattern violated": func(d map[string]any) {
			source, ok := d["data_source"].(map[string]any)
			require.True(t, ok)
			sources, ok := source["sources"].([]any)
			require.True(t, ok)
			first, ok := sources[0].(map[string]any)
			require.True(t, ok)
			first["content_sha256"] = "NOT-A-SHA256"
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var document map[string]any
			require.NoError(t, json.Unmarshal(valid, &document))
			corrupt(document)

			require.Error(t, validateAgainst(schema, document, "$"))
		})
	}
}

// spotinfo.list/v1 is the browse payload. It shares the candidate, risk, price
// and source blocks with spotinfo.recommend/v3 and drops the ranking, so it is
// validated here, beside them, against its own contract file. Task 6 makes it
// the list_spot_machines payload; the document is already the one the CLI
// publishes.
func TestListPayloadValidatesAgainstTheContract(t *testing.T) {
	t.Parallel()

	schema := loadContractSchema(t, "list-v1.schema.json")
	regionScore := 8
	fetchedAt := time.Date(2026, time.August, 6, 8, 58, 27, 0, time.UTC)

	for _, test := range []struct {
		name       string
		provider   cloud.ProviderID
		candidates []cloud.Candidate
		// wantKeys are the optional per-candidate keys this case must actually
		// publish. Without them the schema check passes on a document that
		// carries none of them, which proves nothing about the ones a flag adds.
		wantKeys []string
		// wantNull are the keys this case must publish as JSON null, and
		// wantNonNull the ones it must publish with a value. Presence alone is
		// not enough for a figure a cloud may not have: an empty string in a
		// price field is neither a price nor a documented absence, and it is
		// what shipped before this pair existed.
		wantNull    []string
		wantNonNull []string
	}{
		{
			name:     "aws with published risk, zone prices and placement scores",
			provider: cloud.ProviderAWS,
			candidates: withZonePrice(buildCandidates(testCandidate{
				Region: "us-east-1", Machine: "m6i.large", Price: 0.0416, Savings: 72, Live: true,
				RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 2, MemoryGiB: 8,
				RegionScore: &regionScore, ZoneScores: map[string]int{"us-east-1a": 7},
				ScoreFetchedAt: &fetchedAt,
			}), "us-east-1a", "0.039"),
			wantKeys: []string{"live_price", "zone_prices", "region_score", "zone_scores", "score_fetched_at"},
		},
		{
			// The AWS static price feed omits some families outright and every
			// me-* region, so a browse answer really does carry rows nobody
			// published a spot price for — 600 of 19,353 on the committed
			// snapshot under --region all. The row stays browsable and the
			// absent price is null: an empty string is not a price, and it is
			// what the document published until this case existed. The Advisor
			// savings figure survives beside it because it comes from the
			// advisor feed, not from the price feed.
			name:     "aws machine the price feed does not price",
			provider: cloud.ProviderAWS,
			candidates: buildCandidates(testCandidate{
				Region: "us-east-1", Machine: "dl1.24xlarge", Savings: 41,
				RiskLabel: "10-15%", RiskMin: 10, RiskMax: 15, VCPU: 96, MemoryGiB: 768,
			}),
			wantKeys:    []string{"live_price"},
			wantNull:    []string{"spot_usd_per_hour", "on_demand_usd_per_hour"},
			wantNonNull: []string{"savings_percent"},
		},
		{
			name:        "gcp without risk",
			provider:    cloud.ProviderGCP,
			candidates:  gcpCandidates(),
			wantKeys:    []string{"live_price"},
			wantNonNull: []string{"spot_usd_per_hour"},
		},
		{
			name:     "an answer with no candidates",
			provider: cloud.ProviderAzure,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			for i := range test.candidates {
				test.candidates[i].Provider = test.provider
			}

			result := cloud.Result{
				Provider:   test.provider,
				Mode:       cloud.DataModeEmbeddedSnapshot,
				Sources:    testSources(),
				Candidates: test.candidates,
			}

			report, err := cloud.NewListReport(&cloud.Query{
				Regions: []cloud.Region{allRegions}, OS: cloud.OSLinux,
			}, &result)
			require.NoError(t, err)

			encoded, err := json.Marshal(report)
			require.NoError(t, err)
			assertValidAgainstSchema(t, schema, encoded)

			var document struct {
				Candidates []map[string]any `json:"candidates"`
			}
			require.NoError(t, json.Unmarshal(encoded, &document))
			require.Len(t, document.Candidates, len(test.candidates))

			for _, key := range test.wantKeys {
				require.Contains(t, document.Candidates[0], key)
			}
			for _, key := range test.wantNull {
				value, present := document.Candidates[0][key]
				require.True(t, present, "%s must be published", key)
				require.Nil(t, value, "%s must be null, not an empty string", key)
			}
			for _, key := range test.wantNonNull {
				require.NotNil(t, document.Candidates[0][key], "%s must carry a value", key)
			}
		})
	}
}

// withZonePrice attaches a per-zone price to the first candidate. testCandidate
// models none, and the zone-price block is one of the list-only fields the
// schema declares.
func withZonePrice(candidates []cloud.Candidate, zone, price string) []cloud.Candidate {
	amount, err := cloud.ParseMoney(price)
	if err != nil {
		panic(err)
	}
	candidates[0].ZonePrices = []cloud.PriceObservation{{
		Location: cloud.Location{Region: candidates[0].Location.Region, Zone: zone},
		Class:    cloud.PriceClassSpot,
		Currency: cloud.CurrencyUSD,
		Unit:     cloud.BillingUnitInstanceHour,
		Amount:   amount,
	}}

	return candidates
}
