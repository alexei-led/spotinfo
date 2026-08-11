package main

// Live cloud validation. These tests reach real vendor endpoints, so they are
// **not** part of `make test`.
//
// `make validate-clouds` is the only thing that runs them: it exports
// SPOTINFO_VALIDATE_CLOUDS, and every test here skips with a stated reason
// without it. TestValidateCloudsIsNotAMergeGate asserts that separation against
// the Makefile and the workflows, in both directions, and it is deliberately
// *not* guarded — a change that folded the live suite into a merge gate must
// fail the ordinary suite.
//
// An env guard rather than a build tag, for the same reason `-skip 'E2E'` was
// chosen over one in Task 2: the file keeps compiling under `make test`, so a
// rename or a signature change breaks the build instead of silently deleting
// the suite.
//
// What is asserted here is what only a live run can show: that the two
// anonymous live paths report `live` or `cached` rather than answering from the
// committed snapshot, and that every one of them degrades to that snapshot
// instead of failing the run. Correctness of the numbers is Task 18's job — a
// person against each vendor's own page.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// validateCloudsEnv is the switch `make validate-clouds` sets.
const validateCloudsEnv = "SPOTINFO_VALIDATE_CLOUDS"

// The three data-source modes. `live` is a document the origin served or
// confirmed with a 304, `cached` a copy this machine already held, and
// `embedded-snapshot` the bytes compiled into the binary.
const (
	modeLive   = "live"
	modeCached = "cached"
)

// requireLiveRun skips unless the caller asked for a live run, naming what it
// would have done. A skipped cell must say why, so a green run is never mistaken
// for coverage it did not have.
func requireLiveRun(t *testing.T, what string) {
	t.Helper()

	if os.Getenv(validateCloudsEnv) == "" {
		t.Skipf("skipped: %s reaches a real vendor endpoint; run `make validate-clouds` to include it", what)
	}

	if testing.Short() {
		t.Skip("skipped: -short, and this test compiles the binary")
	}
}

// liveEnv is the environment a live run uses. It is deliberately not e2eEnv:
// that one pins the proxy variables at a closed port, which would make every
// assertion here fail for the wrong reason.
//
// The cache directory is a fresh one per test, so the first call to an origin
// reports `live` rather than inheriting a developer's warm cache — and the
// answer is still allowed to be `cached`, because a second call inside one test
// legitimately is.
func liveEnv(t *testing.T, extra ...string) []string {
	t.Helper()

	env := append(os.Environ(),
		"SPOTINFO_CACHE_DIR="+t.TempDir(),
		"SPOTINFO_CACHE=on",
		// The metadata endpoint is unreachable off EC2 and costs a retry budget
		// per unpriced instance. No test here needs credentials.
		"AWS_EC2_METADATA_DISABLED=true",
	)

	return append(env, extra...)
}

// runLive runs the built binary with the given environment and returns its
// result. It reuses the build the end-to-end suite makes, so a `make
// validate-clouds` run compiles the binary once.
func runLive(t *testing.T, env []string, args ...string) e2eResult {
	t.Helper()

	binary, err := buildSpotinfo()
	if err != nil {
		t.Fatalf("building the binary under test: %v", err)
	}

	// A live fetch of the AWS advisor document alone takes over a second, and
	// two feeds plus a vendor API is the worst case here.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env

	var stdout, stderr strings.Builder

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	result := e2eResult{stdout: stdout.String(), stderr: stderr.String()}
	if runErr != nil {
		var exitCode int
		if exitErr, ok := runErr.(interface{ ExitCode() int }); ok { //nolint:errorlint // the interface is the check
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("running the binary under test: %v; stderr: %s", runErr, result.stderr)
		}

		result.exitCode = exitCode
	}

	return result
}

// liveAnswer decodes the provenance block and the row count of a live answer.
// Both schemas publish data_source under the same name, so one shape reads a
// list and a recommendation alike.
func liveAnswer(t *testing.T, payload string) (mode string, rows int) {
	t.Helper()

	var report struct {
		DataSource struct {
			Mode string `json:"mode"`
		} `json:"data_source"`
		Candidates      []json.RawMessage `json:"candidates"`
		Recommendations []json.RawMessage `json:"recommendations"`
	}

	if err := json.Unmarshal([]byte(payload), &report); err != nil {
		t.Fatalf("the answer is not JSON: %v\npayload: %s", err, payload)
	}

	return report.DataSource.Mode, len(report.Candidates) + len(report.Recommendations)
}

// liveScopes are the questions the live checks ask: one region and one machine
// per cloud, which is the same acquisition path as a full sweep at a fraction of
// the traffic. They mirror e2eScopes deliberately — a live cell and its offline
// twin ask the same question, so a difference between them is the data source
// and nothing else.
var liveScopes = e2eScopes

// liveList and liveRecommend are the offline matrix's questions with the
// --offline flag taken off: same command, same filters, same cloud, so a
// difference between a live cell and its offline twin is the data source and
// nothing else.
func liveList(scope e2eScope) []string {
	return []string{
		"list", "--cloud", scope.cloud, "--region", scope.region,
		"--machine", scope.machine, "--output", "json",
	}
}

func liveRecommend(scope e2eScope) []string {
	return []string{
		"recommend", "--cloud", scope.cloud, "--region", scope.region,
		"--architecture", "x86_64", "--min-vcpu", "2", "--min-memory-gib", "8",
		"--top", "3", "--output", "json",
	}
}

func liveScopeFor(cloud string) e2eScope {
	for _, scope := range liveScopes {
		if scope.cloud == cloud {
			return scope
		}
	}

	panic("no live scope for " + cloud)
}

// AWS answers from its live feeds without credentials of any kind. The check is
// the mode: an answer that fell back to the committed snapshot would look
// identical in every other respect, which is exactly why data_source.mode
// exists.
func TestValidateAWSAnswersFromLiveFeeds(t *testing.T) {
	requireLiveRun(t, "the AWS Spot Advisor and pricing feeds")

	scope := liveScopeFor("aws")

	for _, args := range [][]string{liveList(scope), liveRecommend(scope)} {
		got := runLive(t, liveEnv(t), args...)
		if got.exitCode != 0 {
			t.Fatalf("exit %d for %v\nstderr: %s", got.exitCode, args, got.stderr)
		}

		mode, rows := liveAnswer(t, got.stdout)

		if mode != modeLive && mode != modeCached {
			t.Errorf("%v answered in mode %q; a live run must report %q or %q, never the committed snapshot",
				args, mode, modeLive, modeCached)
		}

		if rows == 0 {
			t.Errorf("%v returned no row", args)
		}
	}
}

// Azure prices come from the anonymous Retail Prices API — no subscription, no
// credential, no Azure SDK. This is the check that the anonymous path is really
// anonymous: the environment carries no Azure variable at all.
func TestValidateAzureAnswersFromTheAnonymousRetailAPI(t *testing.T) {
	requireLiveRun(t, "the Azure Retail Prices API")

	scope := liveScopeFor("azure")

	for _, args := range [][]string{liveList(scope), liveRecommend(scope)} {
		got := runLive(t, liveEnv(t, "AZURE_SUBSCRIPTION_ID=", "AZURE_TENANT_ID=", "AZURE_CLIENT_ID="), args...)
		if got.exitCode != 0 {
			t.Fatalf("exit %d for %v\nstderr: %s", got.exitCode, args, got.stderr)
		}

		mode, rows := liveAnswer(t, got.stdout)

		if mode != modeLive && mode != modeCached {
			t.Errorf("%v answered in mode %q; the Retail Prices API is anonymous and must be reached",
				args, mode)
		}

		if rows == 0 {
			t.Errorf("%v returned no row", args)
		}
	}
}

// GCP answers from its committed snapshot. There is no anonymous live path:
// Google publishes no key-free price API, and the pricing pages the snapshot is
// built from are a build-time source the shipped binary never reads.
func TestValidateGCPAnswersFromTheCommittedSnapshot(t *testing.T) {
	requireLiveRun(t, "a GCP answer, which comes from the committed snapshot")

	scope := liveScopeFor("gcp")

	got := runLive(t, liveEnv(t), liveList(scope)...)
	if got.exitCode != 0 {
		t.Fatalf("exit %d\nstderr: %s", got.exitCode, got.stderr)
	}

	mode, rows := liveAnswer(t, got.stdout)

	if mode != modeEmbedded {
		t.Errorf("gcp answered in mode %q; without a billing key it must answer from %q", mode, modeEmbedded)
	}

	if rows == 0 {
		t.Errorf("gcp returned no row")
	}
}

// The one GCP live path, and it needs a key this repository does not hold. A
// missing key is a skip with a stated reason, never a failure: the maintainer
// runs this with a key, CI never can.
func TestValidateGCPBillingKeyPathWhenAKeyIsPresent(t *testing.T) {
	requireLiveRun(t, "the GCP Cloud Billing Catalog API")

	key := os.Getenv(gcpBillingKeyEnv)
	if key == "" {
		t.Skipf("skipped: no %s in the environment; the Cloud Billing Catalog API needs a key, "+
			"and there is no anonymous GCP price source to substitute", gcpBillingKeyEnv)
	}

	scope := liveScopeFor("gcp")

	got := runLive(t, liveEnv(t), append(liveList(scope), "--gcp-billing-key", key)...)

	// Whatever the key turns out to be worth, the run must answer: a live path
	// that fails the run is the failure mode the Safety notes forbid.
	if got.exitCode != 0 {
		t.Fatalf("a billing key must not fail the run; exit %d\nstderr: %s", got.exitCode, got.stderr)
	}

	_, rows := liveAnswer(t, got.stdout)
	if rows == 0 {
		t.Errorf("gcp returned no row with a billing key")
	}
}

// Never let a live path fail a run. Each one is pointed at a closed port with no
// --offline flag, so the fetch is attempted and refused, and the answer must
// still arrive — from the committed snapshot, and saying so.
func TestValidateEveryLivePathDegradesToTheSnapshot(t *testing.T) {
	requireLiveRun(t, "the fallback from a blocked origin to the committed snapshot")

	for _, scope := range liveScopes {
		t.Run(scope.cloud, func(t *testing.T) {
			for _, args := range [][]string{liveList(scope), liveRecommend(scope)} {
				got := runLive(t, liveEnv(t, blockNetwork()...), args...)
				if got.exitCode != 0 {
					t.Fatalf("a blocked origin must not fail the run; exit %d for %v\nstderr: %s",
						got.exitCode, args, got.stderr)
				}

				mode, rows := liveAnswer(t, got.stdout)

				if mode != modeEmbedded {
					t.Errorf("%v answered in mode %q with every origin blocked; it must report %q",
						args, mode, modeEmbedded)
				}

				if rows == 0 {
					t.Errorf("%v returned no row with every origin blocked", args)
				}
			}
		})
	}
}

// The separation itself, asserted in both directions and deliberately **not**
// behind the env guard: this test runs inside `make test`, so a change that
// folded the live suite into a merge gate fails the ordinary suite rather than
// quietly adding a vendor dependency to every pull request.
//
// Absence alone would pass vacuously against a deleted target, which is why the
// target's existence is asserted first.
//
// The Makefile half checks every mention of the name rather than the recipes of
// the test targets, and that is not belt-and-braces. A recipe scan misses the
// way this would actually happen: `test: validate-clouds` is a **prerequisite**,
// it lives on the rule line rather than in the recipe, and nobody writes
// `@$(MAKE) validate-clouds` into a recipe by hand. Measured against the first
// version of this test, which read recipes only — the prerequisite passed it.
// Checking every line also closes a chain through a third target, which no
// fixed list of rules can.
func TestValidateCloudsIsNotAMergeGate(t *testing.T) {
	t.Parallel()

	const target = "validate-clouds"

	makefile := readRepoFile(t, "Makefile")

	from, to := makeRuleLines(makefile, target)
	if from < 0 {
		t.Fatalf("the Makefile declares no %s target; the live checks would have nothing to run them", target)
	}

	rule := strings.Join(strings.Split(makefile, "\n")[from:to], "\n")
	if !strings.Contains(rule, validateCloudsEnv) {
		t.Errorf("the %s rule must export %s, which is what un-skips this file:\n%s",
			target, validateCloudsEnv, rule)
	}

	for number, line := range strings.Split(makefile, "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case number >= from && number < to: // the rule itself
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
		case strings.HasPrefix(trimmed, ".PHONY"): // declaring it is not running it
		case strings.Contains(line, target) || strings.Contains(line, validateCloudsEnv):
			t.Errorf("Makefile line %d reaches the live cloud checks from another rule, "+
				"which is how `make test` would come to need a vendor:\n%s", number+1, line)
		}
	}

	workflows, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.y*ml"))
	if err != nil {
		t.Fatalf("listing the workflows: %v", err)
	}

	if len(workflows) == 0 {
		t.Fatalf("no workflows found; this assertion would pass against an empty directory")
	}

	for _, workflow := range workflows {
		contents, err := os.ReadFile(workflow) //nolint:gosec // G304: a path this test just globbed
		if err != nil {
			t.Fatalf("reading %s: %v", workflow, err)
		}

		if strings.Contains(string(contents), target) || strings.Contains(string(contents), validateCloudsEnv) {
			t.Errorf("%s runs the live cloud checks; no workflow that gates a merge may reach a vendor",
				filepath.Base(workflow))
		}
	}
}

func readRepoFile(t *testing.T, name string) string {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("..", "..", name)) //nolint:gosec // G304: fixed repository path
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}

	return string(contents)
}

// makeRuleLines returns the half-open line range of one rule: its header, which
// carries the prerequisites, and the indented recipe under it. It returns
// (-1, -1) when the Makefile declares no such rule.
func makeRuleLines(makefile, target string) (from, to int) {
	lines := strings.Split(makefile, "\n")

	from = slices.IndexFunc(lines, func(line string) bool {
		return strings.HasPrefix(line, target+":")
	})
	if from < 0 {
		return -1, -1
	}

	for to = from + 1; to < len(lines); to++ {
		if lines[to] == "" || (lines[to][0] != '\t' && lines[to][0] != ' ') {
			break
		}
	}

	return from, to
}
