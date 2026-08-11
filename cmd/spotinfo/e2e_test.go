package main

// End-to-end tests. Every other test in this package calls the command
// functions in process; these build the real binary and run it as a
// subprocess, so they are the only place that covers flag parsing, exit
// codes, the stdout/stderr split, and the embedded snapshots as shipped.
//
// ---------------------------------------------------------------------------
// THIS SUITE IS EXPECTED TO FAIL UNTIL TASK 7 OF
// docs/plans/20260811-multicloud-parity.md.
//
// It was written in Task 2, before the surface it describes exists: two
// cloud-neutral commands, one schema family (spotinfo.list/v1 and
// spotinfo.recommend/v3), one flag vocabulary, and three renamed MCP tools.
// Tasks 3 to 7 make it pass. Never weaken an assertion here to reach green
// earlier — a red assertion is the specification working.
//
// Every test name carries `E2E` as an infix, so `go test -skip 'E2E'` — the
// per-task gate until Task 7 — excludes the whole suite and its subtests. The
// marker must stay an infix: a Go test function has to begin with `Test`, so
// `E2ETestFoo` is never collected and the suite would silently vanish.
//
// Two rules keep the red honest, and both are load-bearing:
//
//  1. This file imports the standard library and nothing else. It references no
//     production symbol, so a half-migrated package still compiles and the
//     failures stay assertions instead of build errors. That is also why the
//     flag names, tool names and schema versions below are written out rather
//     than read from the vocabulary tables they will match.
//  2. Any check that guards an index or a dereference is t.Fatalf, never
//     t.Errorf. A helper that kept going would report a panic where an
//     assertion belongs, and a panic is indistinguishable from a broken suite.
//
// ---------------------------------------------------------------------------
//
// Running out of process also sidesteps the reason the in-process CLI tests
// must stay serial: urfave/cli writes to its package-level HelpFlag during
// Apply, so two concurrent app.Run calls race inside the library. A
// subprocess has its own copy of that global, so these tests can be parallel.
//
// The suite never reaches the network and never needs credentials. Every
// subprocess runs with the proxy variables pointed at a closed port, so a
// request that escapes fails immediately instead of reaching a vendor;
// TestE2EOfflineAnswersWithEveryOutboundRequestBlocked is the test that reads
// that arrangement as its subject rather than relying on it in passing.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

const (
	// e2eTimeout bounds a single subprocess run. An offline answer takes about
	// a tenth of a second, so anything near this is a hang, not slow data.
	e2eTimeout = 60 * time.Second

	// deadProxy is a port nothing listens on. Pointing HTTP_PROXY and
	// HTTPS_PROXY at it turns any outbound HTTP call into an immediate
	// connection error, which is how the offline claim is tested instead of
	// assumed.
	deadProxy = "http://127.0.0.1:1"

	// The published schema family. One per command, shared DTOs, no workload
	// and no cloud selecting a different document.
	schemaListV1      = "spotinfo.list/v1"
	schemaRecommendV3 = "spotinfo.recommend/v3"

	// modeEmbedded is what data_source.mode reports for an answer built from
	// committed data. The other two states are `live` and `cached`; a snapshot
	// answer must claim neither.
	modeEmbedded = "embedded-snapshot"
)

// e2eBuildDir holds the compiled binary for the whole package run. TestMain
// removes it. It stays empty when no end-to-end test runs, so a short run
// pays nothing.
var (
	e2eBuildDir string

	buildSpotinfo = sync.OnceValues(func() (string, error) {
		dir, err := os.MkdirTemp("", "spotinfo-e2e-")
		if err != nil {
			return "", err
		}

		e2eBuildDir = dir

		name := "spotinfo"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}

		binary := filepath.Join(e2eBuildDir, name)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// -tags release matches how the shipped binary is built (Makefile
		// `build`). The tag selects no files today — the module has no build
		// constraints — so do not go hunting for the file it switches on. It
		// is kept because it is the cheap half of an invariant: the day a
		// //go:build release file appears, this suite keeps compiling what
		// ships instead of silently testing a different binary. Cost is one
		// extra build-cache entry.
		//
		// The ldflags the Makefile passes are deliberately omitted: they only
		// stamp version metadata, and leaving them out keeps the build
		// cacheable across runs.
		cmd := exec.CommandContext(ctx, "go", "build", "-tags", "release", "-o", binary, ".")

		out, buildErr := cmd.CombinedOutput()
		if buildErr != nil {
			return "", fmt.Errorf("go build: %w: %s", buildErr, out)
		}

		return binary, nil
	})
)

// TestMain runs whatever -skip says, so it must keep working while the rest of
// this file is red: a failure here is a build or environment problem, not an
// end-to-end assertion. It does no work of its own — the binary is built lazily
// by the first test that needs one, so `go test -skip 'E2E'` pays nothing.
func TestMain(m *testing.M) {
	code := m.Run()

	if e2eBuildDir != "" {
		_ = os.RemoveAll(e2eBuildDir)
	}

	os.Exit(code)
}

// e2eEnv is the environment every subprocess in this suite runs with. It is one
// function on purpose: the two call sites used to build it separately, they
// drifted, and the MCP path silently kept fetching the AWS feeds because it had
// no proxy pin. Anything that could change an answer belongs here, so a new
// caller cannot forget it.
//
// The blocked proxy is part of it rather than an opt-in: while this suite is
// red, a command that is not yet implemented falls through to whatever the old
// tree does, and some of those paths fetch. Making the block the default keeps
// the network-free claim true at every intermediate state of the migration.
//
// PATH stays because the linker and the OS need it on some platforms. Every
// credential source is cleared, so a developer machine behaves like CI.
func e2eEnv(t *testing.T, extra ...string) []string {
	t.Helper()

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"SPOTINFO_CACHE=off",
		"AWS_EC2_METADATA_DISABLED=true",
		"AWS_ACCESS_KEY_ID=",
		"AWS_SECRET_ACCESS_KEY=",
		"AWS_PROFILE=",
		"AWS_REGION=",
		"GOOGLE_CLOUD_PROJECT=",
		"GOOGLE_APPLICATION_CREDENTIALS=",
		"AZURE_SUBSCRIPTION_ID=",
	}

	env = append(env, blockNetwork()...)

	return append(env, extra...)
}

// blockNetwork points every proxy variable at a closed port, so any outbound
// request fails immediately instead of reaching a vendor.
func blockNetwork() []string {
	return []string{
		"HTTP_PROXY=" + deadProxy,
		"HTTPS_PROXY=" + deadProxy,
		"http_proxy=" + deadProxy,
		"https_proxy=" + deadProxy,
		"NO_PROXY=",
		"no_proxy=",
	}
}

// e2eResult is what one subprocess run produced.
type e2eResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// runSpotinfo builds the binary once, then runs it with the given arguments.
// The environment is stripped to a known-empty one: no AWS credentials, no
// region, no cache, and no route to the network. That makes a developer machine
// with credentials behave like CI.
func runSpotinfo(t *testing.T, extraEnv []string, args ...string) e2eResult {
	t.Helper()

	if testing.Short() {
		t.Skip("end-to-end test compiles the binary")
	}

	binary, err := buildSpotinfo()
	if err != nil {
		t.Fatalf("building the binary under test: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, args...)

	cmd.Env = e2eEnv(t, extraEnv...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("the command did not finish in %s: %v", e2eTimeout, args)
	}

	result := e2eResult{stdout: stdout.String(), stderr: stderr.String()}

	var exitErr *exec.ExitError

	switch {
	case runErr == nil:
		result.exitCode = 0
	case errors.As(runErr, &exitErr):
		result.exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("running the binary under test: %v", runErr)
	}

	return result
}

// e2eRecommendArgs is the question every recommend test asks: the smallest request
// the command accepts, in vocabulary flag names.
//
// --offline is added for AWS only. AWS is the only cloud with a live path
// today, and the plan refuses --offline on a cloud that declares no live
// enrichment (Task 8) until Tasks 10 and 13 give Azure and GCP one. Passing it
// everywhere would make this suite red for a reason that has nothing to do with
// the surface it describes. GCP and Azure need no flag: the blocked proxy in
// e2eEnv proves they answer without a request.
func e2eRecommendArgs(cloud string) []string {
	return append([]string{
		"recommend",
		"--cloud", cloud,
		"--architecture", "x86_64",
		"--min-vcpu", "2",
		"--min-memory-gib", "8",
		"--top", "3",
	}, e2eOfflineFor(cloud)...)
}

// e2eListArgs is the browse question: no requirement at all, which is the whole
// difference between the two commands.
func e2eListArgs(cloud string) []string {
	return append([]string{"list", "--cloud", cloud}, e2eOfflineFor(cloud)...)
}

func e2eOfflineFor(cloud string) []string {
	if cloud == "aws" {
		return []string{"--offline"}
	}

	return nil
}

// dataRows returns a table's data rows: everything that is not blank, not a
// drawn border and not the header.
//
// Borders are filtered rather than assumed absent. The two renderers in this
// repository disagree today — the query command draws a box and the recommend
// table does not — and which one survives Task 7 is not something this suite
// should decide. The row count is the assertion; the style is not a contract.
func dataRows(t *testing.T, table string) []string {
	t.Helper()

	var rows []string

	scanner := bufio.NewScanner(strings.NewReader(table))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// A border carries no letter or digit, whatever it is drawn with.
		if line == "" || !strings.ContainsFunc(line, isAlphanumeric) {
			continue
		}

		if strings.Contains(strings.ToUpper(line), "RANK") {
			continue
		}

		rows = append(rows, line)
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("reading the table: %v", err)
	}

	return rows
}

func isAlphanumeric(char rune) bool {
	return unicode.IsLetter(char) || unicode.IsDigit(char)
}

// The assertion helpers below are hand-rolled because this file imports the
// standard library only. The split between them is the rule stated at the top:
// requireX stops the test, assertX records a failure and lets the rest of the
// checks run.

func requireZeroExit(t *testing.T, got e2eResult) {
	t.Helper()

	if got.exitCode != 0 {
		t.Fatalf("exit code %d, want 0\nstdout: %s\nstderr: %s", got.exitCode, got.stdout, got.stderr)
	}
}

func requireJSON(t *testing.T, payload string, into any) {
	t.Helper()

	if err := json.Unmarshal([]byte(payload), into); err != nil {
		t.Fatalf("output is not the expected JSON: %v\npayload: %s", err, payload)
	}
}

func requireNotEmpty[T any](t *testing.T, values []T, what string) {
	t.Helper()

	if len(values) == 0 {
		t.Fatalf("%s is empty", what)
	}
}

func assertEqual[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()

	if got != want {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

func assertSliceEqual[T comparable](t *testing.T, got, want []T, what string) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

func assertLen[T any](t *testing.T, values []T, want int, what string) {
	t.Helper()

	if len(values) != want {
		t.Errorf("%s: got %d, want %d", what, len(values), want)
	}
}

func assertContains(t *testing.T, haystack, needle, what string) {
	t.Helper()

	if !strings.Contains(haystack, needle) {
		t.Errorf("%s: %q does not contain %q", what, haystack, needle)
	}
}

// assertContainsFold is for table headers, where the renderer chooses the
// casing. The concept has to be there; whether it is upper-cased is not a
// contract.
func assertContainsFold(t *testing.T, haystack, needle, what string) {
	t.Helper()

	if !strings.Contains(strings.ToLower(haystack), strings.ToLower(needle)) {
		t.Errorf("%s: %q does not contain %q, in any casing", what, haystack, needle)
	}
}

func assertEmptyStdout(t *testing.T, got e2eResult) {
	t.Helper()

	if strings.TrimSpace(got.stdout) != "" {
		t.Errorf("a rejected request must not print a partial answer; stdout: %s", got.stdout)
	}
}

func assertNonZeroExit(t *testing.T, got e2eResult) {
	t.Helper()

	if got.exitCode == 0 {
		t.Errorf("exit code 0, want non-zero\nstdout: %s\nstderr: %s", got.stdout, got.stderr)
	}
}

// isSHA256 reports whether a published content hash is well formed. An empty
// hash is legal — see the comment at its call site — but a malformed one is
// not.
func isSHA256(value string) bool {
	const sha256HexLength = 64

	if len(value) != sha256HexLength {
		return false
	}

	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}

	return true
}

// dataSource is the provenance block both schemas publish, unchanged between
// them: they share the DTO.
type dataSource struct {
	Provider string `json:"provider"`
	Mode     string `json:"mode"`
	Sources  []struct {
		URL           string `json:"url"`
		ContentSHA256 string `json:"content_sha256"`
	} `json:"sources"`
}

// e2eCandidate is one machine, in the shape both schemas share.
type e2eCandidate struct {
	Cloud          string  `json:"cloud"`
	Region         string  `json:"region"`
	Machine        string  `json:"machine"`
	Architecture   string  `json:"architecture"`
	OS             string  `json:"os"`
	VCPU           int     `json:"vcpu"`
	MemoryGiB      float64 `json:"memory_gib"`
	SpotUSDPerHour string  `json:"spot_usd_per_hour"`
	Rank           int     `json:"rank"`
	Risk           struct {
		Status string `json:"status"`
		Kind   string `json:"kind"`
	} `json:"risk"`
}

// recommendReport is the part of spotinfo.recommend/v3 this suite reads. The
// full schema is pinned by cmd/spotinfo/contract_v2_test.go; here the point is
// that the shipped binary emits it, not that the shape is complete.
type recommendReport struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`
	Request       struct {
		Cloud        string   `json:"cloud"`
		Regions      []string `json:"regions"`
		Architecture string   `json:"architecture"`
		OS           string   `json:"os"`
		MinVCPU      int      `json:"min_vcpu"`
		MinMemoryGiB float64  `json:"min_memory_gib"`
		Workload     string   `json:"workload"`
		Top          int      `json:"top"`
	} `json:"request"`
	RankingPolicy   []string       `json:"ranking_policy"`
	DataSource      dataSource     `json:"data_source"`
	Recommendations []e2eCandidate `json:"recommendations"`
}

// listReport is the part of spotinfo.list/v1 this suite reads. It shares the
// candidate, risk, price and provenance DTOs with v3 and drops the ranking: a
// browse answer states what is there, not what to pick.
//
// `candidates` is the array key this suite defines as the contract. The plan
// names every other field; this one it leaves to the first surface that
// publishes it, and that is here.
type listReport struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`
	Request       struct {
		Cloud        string   `json:"cloud"`
		Regions      []string `json:"regions"`
		Machine      string   `json:"machine"`
		Architecture string   `json:"architecture"`
		OS           string   `json:"os"`
	} `json:"request"`
	DataSource dataSource     `json:"data_source"`
	Candidates []e2eCandidate `json:"candidates"`
}

// The neutral table is the same shape on every cloud, because the answer is the
// same document on every cloud. A per-cloud column set was the visible half of
// the two-schema surface this plan retires.
var recommendColumns = []string{
	"RANK", "CLOUD", "REGION", "MACHINE", "ARCHITECTURE",
	"vCPU", "MEMORY GiB", "USD/HOUR", "SAVINGS", "RISK", "WHY",
}

func TestE2EEveryCloudAnswersFromTheShippedBinary(t *testing.T) {
	t.Parallel()

	// Column names are part of what a user reads, so they are asserted. The
	// values are not: the snapshots are refreshed weekly and a price moving
	// is not a regression.
	for _, cloud := range []string{"aws", "gcp", "azure"} {
		t.Run(cloud, func(t *testing.T) {
			t.Parallel()

			got := runSpotinfo(t, nil, e2eRecommendArgs(cloud)...)
			requireZeroExit(t, got)

			// Searched across the whole page rather than on the first line: a
			// bordered renderer puts a rule there, and no column name can
			// collide with a value in a data row.
			for _, column := range recommendColumns {
				assertContainsFold(t, got.stdout, column, "table columns")
			}

			assertLen(t, dataRows(t, got.stdout), 3, "--top 3 must return three rows")
		})
	}
}

func TestE2EJSONReportIsValidForEveryCloud(t *testing.T) {
	t.Parallel()

	for _, cloud := range []string{"aws", "gcp", "azure"} {
		t.Run(cloud, func(t *testing.T) {
			t.Parallel()

			args := append(e2eRecommendArgs(cloud), "--output", "json")
			got := runSpotinfo(t, nil, args...)
			requireZeroExit(t, got)

			var report recommendReport

			requireJSON(t, got.stdout, &report)

			assertEqual(t, report.SchemaVersion, schemaRecommendV3, "schema_version")
			assertEqual(t, report.Status, "ok", "status")
			assertEqual(t, report.Request.Cloud, cloud, "request.cloud")
			assertEqual(t, report.DataSource.Provider, cloud, "data_source.provider")
			assertEqual(t, report.Request.MinVCPU, 2, "request.min_vcpu")
			assertEqual(t, report.Request.MinMemoryGiB, 8.0, "request.min_memory_gib")
			assertEqual(t, report.Request.Top, 3, "request.top")

			requireNotEmpty(t, report.DataSource.Sources, "data_source.sources: provenance must be published")
			requireNotEmpty(t, report.Recommendations, "recommendations")
			assertLen(t, report.Recommendations, 3, "recommendations")

			// A source the updater downloaded publishes the hash of what it
			// read, so a consumer can re-fetch and compare. A source recorded
			// as provenance only — the GCP machine-architecture reference is
			// read by a person, not by the parser — publishes no hash. An
			// empty string is therefore legal, but a malformed hash is not.
			for _, source := range report.DataSource.Sources {
				if source.URL == "" {
					t.Errorf("a published source has no URL")
				}

				if source.ContentSHA256 == "" {
					continue
				}

				if !isSHA256(source.ContentSHA256) {
					t.Errorf("%s: content_sha256 %q is not a sha256", source.URL, source.ContentSHA256)
				}
			}

			for i, machine := range report.Recommendations {
				assertEqual(t, machine.Rank, i+1, "ranks must be dense and ordered")
				assertEqual(t, machine.Cloud, cloud, "recommendation.cloud")
				assertEqual(t, machine.Architecture, "x86_64", "recommendation.architecture")

				if machine.Region == "" || machine.Machine == "" {
					t.Errorf("recommendation %d names no region or machine: %+v", i+1, machine)
				}

				if machine.VCPU < 2 {
					t.Errorf("%s: %d vCPU is below the requested floor", machine.Machine, machine.VCPU)
				}

				if machine.MemoryGiB < 8.0 {
					t.Errorf("%s: %g GiB is below the requested floor", machine.Machine, machine.MemoryGiB)
				}
			}
		})
	}
}

// One schema family, chosen by nothing. The workload used to select the payload
// version — AWS under web, ci or batch answered in spotinfo.recommend/v1 — so
// the document a caller received depended on a value they had not set. Every
// cloud and every workload that cloud accepts now answers in v3.
//
// The matrix is asymmetric on purpose: web, ci and batch cap an interruption
// frequency, and only AWS publishes one. GCP and Azure refuse those workloads,
// which TestE2ERejectedInputExitsNonZeroWithAnEmptyStdout asserts.
func TestE2EOneSchemaAnswersEveryCloudAndWorkload(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		cloud    string
		workload string
	}{
		{cloud: "aws", workload: "cost"},
		{cloud: "aws", workload: "web"},
		{cloud: "aws", workload: "ci"},
		{cloud: "aws", workload: "batch"},
		{cloud: "gcp", workload: "cost"},
		{cloud: "azure", workload: "cost"},
	} {
		t.Run(tc.cloud+"/"+tc.workload, func(t *testing.T) {
			t.Parallel()

			args := append(e2eRecommendArgs(tc.cloud), "--workload", tc.workload, "--output", "json")
			got := runSpotinfo(t, nil, args...)
			requireZeroExit(t, got)

			var envelope struct {
				SchemaVersion string `json:"schema_version"`
				Request       struct {
					Workload string `json:"workload"`
				} `json:"request"`
			}

			requireJSON(t, got.stdout, &envelope)

			assertEqual(t, envelope.SchemaVersion, schemaRecommendV3, "schema_version")
			assertEqual(t, envelope.Request.Workload, tc.workload, "request.workload")
		})
	}
}

// The same question asked over the CLI and over MCP must return the same
// document. It used not to: the CLI defaulted to one region and the web
// workload, the MCP tool to every region and the cost workload, so the two
// surfaces ranked differently and answered in different schemas. The defaults
// are unified now, and this test is what holds them together.
func TestE2ETheCLIAndMCPAnswerTheSameQuestionIdentically(t *testing.T) {
	t.Parallel()

	cli := runSpotinfo(t, nil,
		"recommend", "--cloud", "aws", "--architecture", "x86_64",
		"--min-vcpu", "2", "--min-memory-gib", "8", "--offline", "--output", "json")
	requireZeroExit(t, cli)

	var fromCLI recommendReport

	requireJSON(t, cli.stdout, &fromCLI)

	fromMCP := callRecommendOverMCP(t,
		`{"cloud":"aws","architecture":"x86_64","min_vcpu":2,"min_memory_gib":8,"offline":true}`)

	// The resolved request, echoed. Neither surface may resolve a default the
	// other does not.
	assertSliceEqual(t, fromCLI.Request.Regions, []string{"all"}, "request.regions defaults to every region")
	assertSliceEqual(t, fromCLI.Request.Regions, fromMCP.Request.Regions, "request.regions")
	assertEqual(t, fromCLI.Request.Workload, "cost", "request.workload defaults to the risk-free policy")
	assertEqual(t, fromCLI.Request.Workload, fromMCP.Request.Workload, "request.workload")
	assertEqual(t, fromCLI.Request.OS, fromMCP.Request.OS, "request.os")
	assertEqual(t, fromCLI.Request.Top, fromMCP.Request.Top, "request.top")
	assertEqual(t, fromCLI.Request.MinVCPU, fromMCP.Request.MinVCPU, "request.min_vcpu")
	assertEqual(t, fromCLI.Request.MinMemoryGiB, fromMCP.Request.MinMemoryGiB, "request.min_memory_gib")

	// The ranking policy, which is what makes the order comparable at all.
	assertSliceEqual(t, fromCLI.RankingPolicy, fromMCP.RankingPolicy, "ranking_policy")

	requireNotEmpty(t, fromCLI.Recommendations, "the CLI returned no recommendation")
	requireNotEmpty(t, fromMCP.Recommendations, "MCP returned no recommendation")

	first, second := fromCLI.Recommendations[0], fromMCP.Recommendations[0]

	assertEqual(t, first.Machine, second.Machine, "the first result must be the same machine")
	assertEqual(t, first.Region, second.Region, "the first result must be the same region")
	assertEqual(t, first.Cloud, second.Cloud, "the first result must be on the same cloud")
	assertEqual(t, first.SpotUSDPerHour, second.SpotUSDPerHour, "the first result must carry the same price")
}

// The browse half of the same rule, and it used to be broken. `spotinfo list`
// read the AWS architecture snapshot only when --architecture asked for one,
// while list_spot_machines resolves through the registry, which always loads it.
// The same question therefore returned the same 29 rows with `"architecture":
// ""` from the CLI and `"x86_64"` over MCP — one question, two answers, which is
// exactly what the neutral seam exists to prevent.
//
// The row set is compared in full rather than by its first entry: the defect was
// on every row, and a check of one would have missed a divergence further down.
func TestE2ETheCLIAndMCPAnswerTheSameListQuestionIdentically(t *testing.T) {
	t.Parallel()

	got := runSpotinfo(t, nil, "list", "--cloud", "aws",
		"--machine", "^m5\\.large$", "--offline", "--output", "json")
	requireZeroExit(t, got)

	var fromCLI listReport

	requireJSON(t, got.stdout, &fromCLI)

	fromMCP := callListOverMCP(t, `{"cloud":"aws","machine":"^m5\\.large$","offline":true}`)

	// The resolved request, echoed. Neither surface may resolve a default the
	// other does not.
	assertEqual(t, fromCLI.SchemaVersion, fromMCP.SchemaVersion, "schema_version")
	assertEqual(t, fromCLI.Request.Cloud, fromMCP.Request.Cloud, "request.cloud")
	assertEqual(t, fromCLI.Request.Machine, fromMCP.Request.Machine, "request.machine")
	assertEqual(t, fromCLI.Request.OS, fromMCP.Request.OS, "request.os")
	assertSliceEqual(t, fromCLI.Request.Regions, fromMCP.Request.Regions, "request.regions")
	// The one place an empty architecture is the right answer: it reports that
	// no architecture filter was applied. It must stay empty on both surfaces.
	assertEqual(t, fromCLI.Request.Architecture, "", "request.architecture echoes no filter")
	assertEqual(t, fromCLI.Request.Architecture, fromMCP.Request.Architecture, "request.architecture")

	// Provenance too: a snapshot answer on one surface and a live one on the
	// other would be the same rows established two different ways.
	assertEqual(t, fromCLI.DataSource.Provider, fromMCP.DataSource.Provider, "data_source.provider")
	assertEqual(t, fromCLI.DataSource.Mode, modeEmbedded, "data_source.mode under --offline")
	assertEqual(t, fromCLI.DataSource.Mode, fromMCP.DataSource.Mode, "data_source.mode")

	requireNotEmpty(t, fromCLI.Candidates, "the CLI returned no candidate")
	requireNotEmpty(t, fromMCP.Candidates, "MCP returned no candidate")
	assertLen(t, fromMCP.Candidates, len(fromCLI.Candidates), "candidates over MCP")

	// Aligned by (region, machine) rather than by position. With no --sort the
	// order is the provider's, and AWS advice comes out of a map, so the two
	// runs enumerate the same rows in different orders. That is not a contract
	// either surface publishes; the row set and every field on it is.
	overMCP := make(map[string]e2eCandidate, len(fromMCP.Candidates))
	for _, machine := range fromMCP.Candidates {
		overMCP[machine.Region+"/"+machine.Machine] = machine
	}

	for _, overCLI := range fromCLI.Candidates {
		where := overCLI.Region + "/" + overCLI.Machine

		fromTool, answered := overMCP[where]
		if !answered {
			t.Errorf("%s: the CLI returned it and MCP did not", where)

			continue
		}

		assertEqual(t, overCLI.SpotUSDPerHour, fromTool.SpotUSDPerHour, where+".spot_usd_per_hour")
		assertEqual(t, overCLI.VCPU, fromTool.VCPU, where+".vcpu")
		assertEqual(t, overCLI.MemoryGiB, fromTool.MemoryGiB, where+".memory_gib")
		assertEqual(t, overCLI.Risk.Status, fromTool.Risk.Status, where+".risk.status")
		assertEqual(t, overCLI.Architecture, fromTool.Architecture, where+".architecture")
		// Asserted against the value, not only against each other: two surfaces
		// that both published "" would agree and both be wrong.
		assertEqual(t, overCLI.Architecture, "x86_64", where+".architecture is the published one")
	}
}

// callListOverMCP asks the browse tool one question and returns the document it
// answered with.
func callListOverMCP(t *testing.T, arguments string) listReport {
	t.Helper()

	responses := mcpSession(t, fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_spot_machines","arguments":%s}}`,
		arguments))

	call, ok := responses[2]
	if !ok {
		t.Fatalf("the server returned no response for the tool call")
	}

	if len(call.Result.Content) == 0 {
		t.Fatalf("the tool returned no content")
	}

	var report listReport

	requireJSON(t, call.Result.Content[0].Text, &report)

	return report
}

// A cloud with no redistributable risk data must report the absence, never a
// zero or a low bucket. A neutral consumer that ranked silence against a
// measurement would prefer the cloud that publishes less.
func TestE2EACloudWithoutRiskDataReportsUnavailableRatherThanZero(t *testing.T) {
	t.Parallel()

	for _, cloud := range []string{"gcp", "azure"} {
		t.Run(cloud, func(t *testing.T) {
			t.Parallel()

			args := append(e2eRecommendArgs(cloud), "--output", "json")
			got := runSpotinfo(t, nil, args...)
			requireZeroExit(t, got)

			var report recommendReport

			requireJSON(t, got.stdout, &report)
			requireNotEmpty(t, report.Recommendations, "recommendations")

			for _, machine := range report.Recommendations {
				assertEqual(t, machine.Risk.Status, "unavailable",
					cloud+" publishes no redistributable risk")
			}
		})
	}
}

// The offline claim is that no request is made at all. A dead proxy turns any
// attempt into a connection error, so a passing run is evidence and not a
// promise.
func TestE2EOfflineAnswersWithEveryOutboundRequestBlocked(t *testing.T) {
	t.Parallel()

	for _, cloud := range []string{"aws", "gcp", "azure"} {
		t.Run(cloud, func(t *testing.T) {
			t.Parallel()

			got := runSpotinfo(t, blockNetwork(), e2eRecommendArgs(cloud)...)

			if got.exitCode != 0 {
				t.Fatalf("a blocked network must not change the answer; exit %d, stderr: %s",
					got.exitCode, got.stderr)
			}

			assertLen(t, dataRows(t, got.stdout), 3, "rows with the network blocked")
		})
	}
}

// Rejections must go to stderr and leave stdout empty, so a caller that pipes
// stdout into a JSON parser gets nothing rather than a half report.
func TestE2ERejectedInputExitsNonZeroWithAnEmptyStdout(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		contains string
		args     []string
	}{
		{
			name:     "recommend without a size floor",
			args:     []string{"recommend", "--architecture", "x86_64", "--offline"},
			contains: "--min-vcpu",
		},
		{
			name:     "recommend without an architecture",
			args:     []string{"recommend", "--min-vcpu", "2", "--min-memory-gib", "8", "--offline"},
			contains: "architecture",
		},
		{
			name: "a risk-capped workload on a cloud with no interruption data",
			args: []string{
				"recommend", "--cloud", "gcp", "--architecture", "x86_64",
				"--min-vcpu", "2", "--min-memory-gib", "8", "--workload", "web",
			},
			contains: "unsupported capability",
		},
		{
			name: "windows on a cloud that prices linux only",
			args: []string{
				"recommend", "--cloud", "gcp", "--architecture", "x86_64",
				"--min-vcpu", "2", "--min-memory-gib", "8", "--os", "windows",
			},
			contains: "windows",
		},
		{
			name: "a region the cloud does not publish",
			args: []string{
				"recommend", "--cloud", "gcp", "--architecture", "x86_64",
				"--min-vcpu", "2", "--min-memory-gib", "8", "--region", "eu-west-1",
			},
			contains: "eu-west-1",
		},
		{
			name: "an unknown cloud on recommend",
			args: []string{
				"recommend", "--cloud", "oracle", "--architecture", "x86_64",
				"--min-vcpu", "2", "--min-memory-gib", "8",
			},
			contains: "oracle",
		},
		{
			name:     "an unknown cloud on list",
			args:     []string{"list", "--cloud", "oracle"},
			contains: "oracle",
		},
		// A recommend-only flag passed to `list` is deliberately absent from
		// this table. --workload is undefined there, so urfave/cli rejects it
		// while parsing and writes its usage banner to stdout — which the
		// empty-stdout assertion above would fail, for a reason no task in this
		// plan owns. That the flag is not declared on `list` is proved by the
		// command-tree test in cmd/spotinfo/vocabulary_test.go instead.
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := runSpotinfo(t, nil, tc.args...)

			assertNonZeroExit(t, got)
			assertEmptyStdout(t, got)
			assertContains(t, got.stderr, tc.contains, "the refusal must say why")
		})
	}
}

// Every retired name answers with the flag that replaced it. "flag provided but
// not defined" tells a caller their spelling is wrong; it does not tell them the
// concept still exists under one name, which is the whole point of collapsing
// the vocabulary.
//
// The table is written out rather than read from renamedFlags: this file
// references no production symbol, so a rename that forgets a hint fails here
// instead of agreeing with itself.
func TestE2EEveryRetiredFlagNameProducesARenameHint(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		retired     string
		value       string
		replacement string
	}{
		{retired: "type", value: "^m5\\.large$", replacement: "machine"},
		{retired: "instance", value: "^m5\\.large$", replacement: "machine"},
		{retired: "vcpu", value: "2", replacement: "min-vcpu"},
		{retired: "cpu", value: "2", replacement: "min-vcpu"},
		{retired: "memory", value: "8", replacement: "min-memory-gib"},
		{retired: "memory-gib", value: "8", replacement: "min-memory-gib"},
		{retired: "price", value: "1.0", replacement: "max-price"},
		{retired: "budget", value: "1.0", replacement: "max-price"},
	} {
		for _, command := range []string{"list", "recommend"} {
			t.Run(command+"/"+tc.retired, func(t *testing.T) {
				t.Parallel()

				got := runSpotinfo(t, nil, command, "--"+tc.retired, tc.value)

				assertNonZeroExit(t, got)
				assertEmptyStdout(t, got)
				assertContains(t, got.stderr, "--"+tc.retired, "the hint must name what was passed")
				assertContains(t, got.stderr, "--"+tc.replacement, "the hint must name the replacement")
			})
		}
	}
}

// --top is bounded on the CLI because the bound is written into request.top
// in the published schema. An unbounded flag emitted a document that failed
// its own contract.
func TestE2ETopAboveThePublishedBoundIsRejected(t *testing.T) {
	t.Parallel()

	got := runSpotinfo(t, nil,
		"recommend", "--cloud", "gcp", "--architecture", "x86_64",
		"--min-vcpu", "2", "--min-memory-gib", "8", "--top", "999", "--output", "json")

	assertNonZeroExit(t, got)
	assertEmptyStdout(t, got)
	assertContains(t, got.stderr, "top", "the refusal must name the flag")
}

// Two identical offline invocations must produce identical bytes. Anything
// that varies — a map iteration order, a timestamp, an unstable sort — shows
// up here and nowhere else in the suite.
func TestE2EAnOfflineAnswerIsReproducible(t *testing.T) {
	t.Parallel()

	args := append(e2eRecommendArgs("azure"), "--output", "json")

	first := runSpotinfo(t, nil, args...)
	second := runSpotinfo(t, nil, args...)

	requireZeroExit(t, first)
	requireZeroExit(t, second)

	if first.stdout != second.stdout {
		t.Errorf("two identical invocations produced different documents:\nfirst: %s\nsecond: %s",
			first.stdout, second.stdout)
	}
}

// list answers on every cloud. The command it replaces was AWS-only and told a
// caller who asked for another cloud to run a different command.
func TestE2EListAnswersOnEveryCloud(t *testing.T) {
	t.Parallel()

	for _, cloud := range []string{"aws", "gcp", "azure"} {
		t.Run(cloud, func(t *testing.T) {
			t.Parallel()

			args := append(e2eListArgs(cloud), "--output", "json")
			got := runSpotinfo(t, nil, args...)
			requireZeroExit(t, got)

			var report listReport

			requireJSON(t, got.stdout, &report)

			assertEqual(t, report.SchemaVersion, schemaListV1, "schema_version")
			assertEqual(t, report.Status, "ok", "status")
			assertEqual(t, report.Request.Cloud, cloud, "request.cloud")
			assertEqual(t, report.DataSource.Provider, cloud, "data_source.provider")
			assertSliceEqual(t, report.Request.Regions, []string{"all"}, "request.regions defaults to every region")

			requireNotEmpty(t, report.Candidates, "candidates")

			for _, machine := range report.Candidates {
				assertEqual(t, machine.Cloud, cloud, "candidate.cloud")

				// A cloud that publishes no risk states its absence. Never a
				// blank, never a zero: a browse table that left the column
				// empty reads as "no interruptions".
				if machine.Risk.Status != "available" && machine.Risk.Status != "unavailable" {
					t.Errorf("%s: risk.status %q is neither available nor unavailable",
						machine.Machine, machine.Risk.Status)
				}
			}
		})
	}
}

// The five formats belong to list: it is the command that renders a page a
// person reads. `number` is list-only, because one savings percent has no
// meaning for a ranked page.
func TestE2ETheListCommandRendersEveryOutputFormat(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"table", "text", "csv", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()

			got := runSpotinfo(t, nil, "list",
				"--machine", "^m5\\.large$", "--region", "us-east-1",
				"--offline", "--output", format)
			requireZeroExit(t, got)

			assertContains(t, got.stdout, "m5.large", format+" must name the machine it priced")

			switch format {
			case "json":
				assertContains(t, got.stdout, schemaListV1, "the JSON form declares its schema")
			case "table", "csv":
				// The two columnar formats name the same concepts as the
				// vocabulary. `risk` in particular: the column this command
				// renders is a neutral risk observation now, not an AWS
				// interruption bucket.
				//
				// Searched across the whole page, not on a header line: `table`
				// draws a border above its header today, and whether it still
				// does after Task 7 is a rendering choice this suite does not
				// make. The region column is left out because it is conditional
				// on the query naming more than one region, which is a separate
				// decision from what the columns are called.
				for _, column := range []string{"machine", "vCPU", "memory", "USD/hour", "savings", "risk"} {
					assertContainsFold(t, got.stdout, column, format+" columns")
				}
			}
		})
	}
}

// number is the format a shell pipes into a comparison, so it must print one
// bare savings percent and nothing else. A stray label or unit breaks every
// caller that wraps it in an arithmetic test.
//
// One region is named explicitly: the default is every region now, and a single
// percent across all of them would answer a question nobody asked.
func TestE2ETheNumberFormatPrintsOnlyASavingsPercent(t *testing.T) {
	t.Parallel()

	got := runSpotinfo(t, nil, "list",
		"--machine", "^m5\\.large$", "--region", "us-east-1", "--offline", "--output", "number")
	requireZeroExit(t, got)

	savings, err := strconv.Atoi(strings.TrimSpace(got.stdout))
	if err != nil {
		t.Fatalf("output is not a bare number: %q", got.stdout)
	}

	if savings < 0 || savings > 100 {
		t.Errorf("savings %d is not a percentage", savings)
	}
}

// Price provenance is published on every answer, not only when a flag asked for
// it. The v1 JSON form carried it in `live_price`; the neutral document carries
// it in data_source — the mode that established the price, and the sources it
// was read from. Under --offline the mode is exactly `embedded-snapshot`: a
// snapshot answer that claimed `live` or `cached` would be claiming a recency
// nothing verified.
func TestE2ETheListCommandAlwaysPublishesPriceProvenance(t *testing.T) {
	t.Parallel()

	got := runSpotinfo(t, nil, "list",
		"--machine", "^m5\\.", "--region", "us-east-1", "--offline", "--output", "json")
	requireZeroExit(t, got)

	var report listReport

	requireJSON(t, got.stdout, &report)

	assertEqual(t, report.DataSource.Mode, modeEmbedded, "data_source.mode under --offline")
	requireNotEmpty(t, report.DataSource.Sources, "data_source.sources")
	requireNotEmpty(t, report.Candidates, "candidates")

	for _, machine := range report.Candidates {
		if machine.SpotUSDPerHour == "" {
			t.Errorf("%s published no price", machine.Machine)
		}
	}
}

// mcpSession is one stdio conversation with the server: a handshake, then the
// caller's requests, then EOF. The server is a second entry point into the
// same data, and nothing else in this repository starts it as a subprocess.
//
// stdout is line-delimited JSON-RPC. Responses are returned keyed by request
// id, so a caller reads the one it asked for.
func mcpSession(t *testing.T, requests ...string) map[int]mcpResponse {
	t.Helper()

	if testing.Short() {
		t.Skip("end-to-end test compiles the binary")
	}

	binary, err := buildSpotinfo()
	if err != nil {
		t.Fatalf("building the binary under test: %v", err)
	}

	handshake := make([]string, 0, 2+len(requests))
	handshake = append(handshake,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "--mcp", "--quiet")
	// An AWS tool call would otherwise fetch both feeds: measured at 5.5s
	// against 49ms for a snapshot-only cloud. The dead proxy e2eEnv pins forces
	// the snapshot and makes the suite's network-free claim true for this path
	// too. It is also the only place that exercises the live-to-snapshot
	// fallback over MCP, which is why the tools here are not simply told to run
	// offline.
	cmd.Env = e2eEnv(t)
	cmd.Stdin = strings.NewReader(strings.Join(append(handshake, requests...), "\n") + "\n")

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	// Checked before the error, so a server that hangs reports the hang rather
	// than the generic kill that follows it.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("the MCP server did not finish in %s; stderr: %s", e2eTimeout, stderr.String())
	}

	if runErr != nil {
		t.Fatalf("running the MCP server: %v; stderr: %s", runErr, stderr.String())
	}

	responses := make(map[int]mcpResponse)

	scanner := bufio.NewScanner(strings.NewReader(stdout.String()))
	// An Azure recommendation carries one provenance entry per region, so a
	// single response line runs past the 64 KiB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		var response mcpResponse
		if json.Unmarshal(scanner.Bytes(), &response) != nil {
			continue
		}

		// Fatal, not recorded: every caller indexes into the result it asked
		// for, and a server error leaves that result empty.
		if response.Error != nil {
			t.Fatalf("the server reported an error: %+v", *response.Error)
		}

		responses[response.ID] = response
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("reading the server's output: %v", err)
	}

	return responses
}

type mcpResponse struct {
	ID     int `json:"id"`
	Result struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// callRecommendOverMCP runs one recommend_spot_machines call and decodes the
// v3 report it returns.
func callRecommendOverMCP(t *testing.T, arguments string) recommendReport {
	t.Helper()

	responses := mcpSession(t, fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recommend_spot_machines","arguments":%s}}`,
		arguments))

	call, ok := responses[2]
	if !ok {
		t.Fatalf("the server returned no response for the tool call")
	}

	if len(call.Result.Content) == 0 {
		t.Fatalf("the tool returned no content")
	}

	var report recommendReport

	requireJSON(t, call.Result.Content[0].Text, &report)

	return report
}

// The three tools mirror the CLI and none of them bakes a cloud into its name.
// find_spot_instances, recommend_spot_instances and list_spot_regions are
// retired with the v1 schema.
func TestE2EMCPServerCompletesAHandshakeAndAnswers(t *testing.T) {
	t.Parallel()

	responses := mcpSession(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)

	listing, ok := responses[2]
	if !ok {
		t.Fatalf("the server returned no tool listing")
	}

	toolNames := make([]string, 0, len(listing.Result.Tools))

	for _, tool := range listing.Result.Tools {
		toolNames = append(toolNames, tool.Name)

		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
	}

	slices.Sort(toolNames)
	assertSliceEqual(t, toolNames,
		[]string{"list_cloud_regions", "list_spot_machines", "recommend_spot_machines"},
		"the published tool names")
}

func TestE2EMCPAnswersEveryCloudInOneSchema(t *testing.T) {
	t.Parallel()

	for _, cloud := range []string{"aws", "gcp", "azure"} {
		t.Run(cloud, func(t *testing.T) {
			t.Parallel()

			report := callRecommendOverMCP(t, fmt.Sprintf(
				`{"cloud":%q,"architecture":"x86_64","min_vcpu":2,"min_memory_gib":8}`, cloud))

			assertEqual(t, report.SchemaVersion, schemaRecommendV3, "schema_version")
			assertEqual(t, report.Request.Cloud, cloud, "request.cloud")
			requireNotEmpty(t, report.Recommendations, "recommendations")
		})
	}
}

// Bare spotinfo runs no query: it has two subcommands and no default one. The
// mode flags are the exception, and they are dispatched from the root Action,
// so a change to what bare spotinfo does must leave both of them working.
func TestE2EBareInvocationPrintsHelpWhileTheModeFlagsStillWork(t *testing.T) {
	t.Parallel()

	t.Run("bare", func(t *testing.T) {
		t.Parallel()

		got := runSpotinfo(t, nil)

		assertNonZeroExit(t, got)

		// Help may go to either stream; naming both commands is what matters,
		// because that is what tells a caller where the query went.
		help := got.stdout + got.stderr
		assertContains(t, help, "list", "help must name the list command")
		assertContains(t, help, "recommend", "help must name the recommend command")
	})

	t.Run("version", func(t *testing.T) {
		t.Parallel()

		got := runSpotinfo(t, nil, "--version")
		requireZeroExit(t, got)
		assertContains(t, got.stdout, "spotinfo", "--version must print the binary name")
	})

	t.Run("mcp", func(t *testing.T) {
		t.Parallel()

		// A completed handshake is the proof: the server started, answered and
		// exited on EOF.
		responses := mcpSession(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		if _, ok := responses[2]; !ok {
			t.Fatalf("--mcp did not start a server that answers")
		}
	})
}
