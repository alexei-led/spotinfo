package main

// Contract validation for the end-to-end matrix.
//
// This lives beside cmd/spotinfo/e2e_test.go rather than inside it because that
// file imports the standard library and nothing else — a rule its own header
// states, and one that keeps it compiling against a half-migrated package. A
// JSON Schema validator is not the standard library, so the import lives here
// and e2e_test.go calls a same-package helper instead.
//
// The validator is github.com/santhosh-tekuri/jsonschema/v6, already in the
// module graph through mcp-go and used by no non-test file, so the shipped
// binary is unchanged. internal/mcp keeps its own small checker: its documented
// upgrade trigger — a contract using $ref, allOf or a conditional — has not
// fired, and swapping it is not this task's job.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The contract files the built binary's JSON output is validated against. They
// are read from docs/plans/contracts/ rather than restated here, so a schema
// edit that outruns the code fails in this package instead of in a consumer.
const (
	e2eListContract      = "list-v1.schema.json"
	e2eRecommendContract = "recommend-v3-success.schema.json"
)

// e2eSchemas compiles each contract once per package run. Compilation is the
// expensive half and the matrix validates thirty-odd documents against six
// files.
var e2eSchemas sync.Map

// e2eAssertValidAgainstContract fails when a document the binary printed
// violates the schema it declares.
//
// A full validator is used rather than the decode-into-a-struct check the rest
// of the suite does: encoding/json ignores every keyword a contract is written
// in — required, enum, pattern, additionalProperties, minItems — so a document
// missing half its fields decodes cleanly and satisfies nothing.
func e2eAssertValidAgainstContract(t *testing.T, contract, payload string) {
	t.Helper()

	schema := e2eContractSchema(t, contract)

	var document any
	if err := json.Unmarshal([]byte(payload), &document); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}

	if err := schema.Validate(document); err != nil {
		t.Errorf("the document does not satisfy %s:\n%v", contract, err)
	}
}

func e2eContractSchema(t *testing.T, contract string) *jsonschema.Schema {
	t.Helper()

	if cached, ok := e2eSchemas.Load(contract); ok {
		return cached.(*jsonschema.Schema) //nolint:forcetypeassert // only this function stores
	}

	path := filepath.Join("..", "..", "docs", "plans", "contracts", contract)

	file, err := os.Open(path) //nolint:gosec // G304: fixed repository path
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	defer file.Close() //nolint:errcheck // read-only

	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatalf("%s is not valid JSON: %v", contract, err)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(contract, document); err != nil {
		t.Fatalf("%s is not a usable schema resource: %v", contract, err)
	}

	schema, err := compiler.Compile(contract)
	if err != nil {
		t.Fatalf("%s does not compile as a JSON Schema: %v", contract, err)
	}

	e2eSchemas.Store(contract, schema)

	return schema
}

// A validator that accepted everything would make every contract assertion in
// the matrix vacuous, so it is checked against a document that must fail: the
// only difference from a real answer is a schema_version the contract pins with
// a const.
func TestE2EContractValidatorRejectsAWrongDocument(t *testing.T) {
	t.Parallel()

	err := e2eContractSchema(t, e2eListContract).Validate(map[string]any{
		"schema_version": "spotinfo.bogus/v9",
	})
	if err == nil {
		t.Fatalf("the contract validator accepted a document that declares the wrong schema")
	}
}
