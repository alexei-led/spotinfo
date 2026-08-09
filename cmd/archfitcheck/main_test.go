package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)

	return contents
}

func check(t *testing.T, input []byte) (code int, stdout, stderr string) {
	t.Helper()

	var out, errOut bytes.Buffer
	code = run(bytes.NewReader(input), &out, &errOut)

	return code, out.String(), errOut.String()
}

// The pre-change report is the one this gate exists for: archfit's own verdict
// is "pass" because Balanced-Coupling advisories are warnings, so a Critical
// finding would otherwise ship.
func TestCriticalFindingFailsDespiteAPassingArchfitVerdict(t *testing.T) {
	t.Parallel()

	code, _, stderr := check(t, fixture(t, "critical.json"))
	assert.Equal(t, exitFindingsOpen, code)
	assert.Contains(t, stderr, "bac6b2e4f1019c672ac2eec8dc470b31")
	assert.Contains(t, stderr, "mcp -> spot")
}

func TestOnlyMediumFindingsPass(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := check(t, fixture(t, "medium.json"))
	assert.Equal(t, exitOK, code)
	assert.Contains(t, stdout, "no open critical or high findings")
	assert.Empty(t, stderr)
}

func TestHighFindingsAlsoFail(t *testing.T) {
	t.Parallel()

	code, _, stderr := check(t, withSeverity(t, "critical.json", "HIGH"))
	assert.Equal(t, exitFindingsOpen, code)
	assert.Contains(t, strings.ToLower(stderr), "high")
}

// Only a finding archfit records as fixed or resolved is closed. Annotating one
// as waived or baselined does not close it, and neither does a status this gate
// has not seen — otherwise a one-word edit turns the architecture gate green on
// a critical finding.
func TestOnlyRecordedResolutionsCloseAFinding(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		status string
		want   int
	}{
		{status: "new", want: exitFindingsOpen},
		{status: "existing", want: exitFindingsOpen},
		{status: "reopened", want: exitFindingsOpen},
		{status: "something-archfit-adds-later", want: exitFindingsOpen},
		{status: "waived", want: exitFindingsOpen},
		{status: "baselined", want: exitFindingsOpen},
		{status: statusFixed, want: exitOK},
		{status: statusResolved, want: exitOK},
	} {
		t.Run(test.status, func(t *testing.T) {
			t.Parallel()

			code, _, _ := check(t, withStatus(t, "critical.json", test.status))
			assert.Equal(t, test.want, code)
		})
	}
}

func TestInvalidInputIsReportedSeparatelyFromAFailedGate(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		input string
	}{
		{name: "not json", input: "archfit: command not found"},
		{name: "empty", input: ""},
		{name: "json without a verdict", input: `{"findings": []}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			code, _, stderr := check(t, []byte(test.input))
			assert.Equal(t, exitInvalidInput, code)
			assert.Contains(t, stderr, "archfitcheck:")
		})
	}
}

func withSeverity(t *testing.T, name, severity string) []byte {
	t.Helper()

	return mutateFirstFinding(t, name, "severity", severity)
}

func withStatus(t *testing.T, name, status string) []byte {
	t.Helper()

	return mutateFirstFinding(t, name, "status", status)
}

func mutateFirstFinding(t *testing.T, name, field, value string) []byte {
	t.Helper()

	var document map[string]any
	require.NoError(t, json.Unmarshal(fixture(t, name), &document))

	findings, ok := document["findings"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, findings)

	first, ok := findings[0].(map[string]any)
	require.True(t, ok)
	first[field] = value

	encoded, err := json.Marshal(document)
	require.NoError(t, err)

	return encoded
}
