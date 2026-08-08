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
// It exists so the published contracts are enforced against real payloads
// without adding a schema library to a binary that ships with none. Reach for a
// real validator if the contracts start using $ref, allOf, or conditionals.

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
