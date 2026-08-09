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

	"github.com/stretchr/testify/require"
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

	schema := loadContractSchema(t, "recommend-spot-instances-v2-success.schema.json")

	valid, err := os.ReadFile(filepath.Join("testdata", "recommend-spot-instances-v2-success.json"))
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
		"missing required key":     func(d map[string]any) { delete(d, "schema_version") },
		"unknown top-level key":    func(d map[string]any) { d["surprise"] = true },
		"wrong type":               func(d map[string]any) { d["warnings"] = "none" },
		"value outside an enum":    func(d map[string]any) { d["status"] = "partial" },
		"const contradicted":       func(d map[string]any) { d["schema_version"] = "spotinfo.recommend/v3" },
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
