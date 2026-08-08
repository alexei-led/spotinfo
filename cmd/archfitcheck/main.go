// Command archfitcheck reads an `archfit analyze --json` report on standard
// input and fails when it contains an open Critical or High finding.
//
// It exists because the archfit rule gate gives Balanced-Coupling advisories
// warning severity: a Critical advisory does not fail `archfit --gate`, so
// without this the architecture gate would pass while a critical edge stands.
//
// Exit codes: 0 when no Critical or High finding is open, 1 when one is, and 2
// when the input is not a readable archfit report.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

const (
	exitOK           = 0
	exitFindingsOpen = 1
	exitInvalidInput = 2

	severityCritical = "critical"
	severityHigh     = "high"

	// The archfit statuses that close a finding. Task 4's critical finding must
	// never reach statusWaived or statusBaselined.
	statusFixed     = "fixed"
	statusResolved  = "resolved"
	statusWaived    = "waived"
	statusBaselined = "baselined"
)

// resolvedStatuses are the finding statuses this gate treats as closed. Every
// other status — including one archfit adds later — counts as open, so a new
// status cannot silently hide a critical finding.
var resolvedStatuses = []string{statusFixed, statusResolved, statusWaived, statusBaselined}

// report is the slice of the archfit report this gate reads. Unknown fields are
// ignored: the gate must keep working across archfit releases that add them.
type report struct {
	Verdict  string    `json:"verdict"`
	Findings []finding `json:"findings"`
}

type finding struct {
	ID       string `json:"id"`
	RuleID   string `json:"rule_id"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
	Why      string `json:"why"`
	Edge     edge   `json:"edge"`
}

type edge struct {
	From node `json:"from"`
	To   node `json:"to"`
}

type node struct {
	Module string `json:"module"`
	Path   string `json:"path"`
}

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

func run(input io.Reader, output, errOutput io.Writer) int {
	contents, err := io.ReadAll(input)
	if err != nil {
		fmt.Fprintf(errOutput, "archfitcheck: read report: %v\n", err) //nolint:errcheck // diagnostics only

		return exitInvalidInput
	}

	var parsed report
	if err := json.Unmarshal(contents, &parsed); err != nil {
		fmt.Fprintf(errOutput, "archfitcheck: parse report: %v\n", err) //nolint:errcheck

		return exitInvalidInput
	}
	if parsed.Verdict == "" {
		fmt.Fprintln(errOutput, "archfitcheck: report has no verdict; is this an archfit analyze --json report?") //nolint:errcheck

		return exitInvalidInput
	}

	open := openBlockingFindings(parsed.Findings)
	if len(open) == 0 {
		fmt.Fprintf(output, "archfitcheck: no open critical or high findings (%d findings reviewed)\n", //nolint:errcheck
			len(parsed.Findings))

		return exitOK
	}

	fmt.Fprintf(errOutput, "archfitcheck: %d open critical or high finding(s):\n", len(open)) //nolint:errcheck
	for _, item := range open {
		fmt.Fprintf(errOutput, "  %s %s [%s] %s -> %s (%s)\n    %s\n", //nolint:errcheck
			item.Severity, item.ID, item.RuleID,
			item.Edge.From.Module, item.Edge.To.Module, item.Edge.From.Path, item.Why)
	}

	return exitFindingsOpen
}

// openBlockingFindings returns every Critical or High finding that is not
// recorded as resolved. Severity comparison is case-insensitive because it is
// report text, not an internal enum.
func openBlockingFindings(findings []finding) []finding {
	var open []finding

	for _, item := range findings {
		severity := strings.ToLower(strings.TrimSpace(item.Severity))
		if severity != severityCritical && severity != severityHigh {
			continue
		}
		if slices.Contains(resolvedStatuses, strings.ToLower(strings.TrimSpace(item.Status))) {
			continue
		}
		open = append(open, item)
	}

	return open
}
