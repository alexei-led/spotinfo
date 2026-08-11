package main

// End-to-end tests. Every other test in this package calls the command
// functions in process; these build the real binary and run it as a
// subprocess, so they are the only place that covers flag parsing, exit
// codes, the stdout/stderr split, and the embedded snapshots as shipped.
//
// Running out of process also sidesteps the reason the in-process CLI tests
// must stay serial: urfave/cli writes to its package-level HelpFlag during
// Apply, so two concurrent app.Run calls race inside the library. A
// subprocess has its own copy of that global, so these tests can be parallel.
//
// The suite never reaches the network and never needs credentials. AWS runs
// with --offline; GCP and Azure have no live path at all. One test proves it
// rather than asserting it, by pointing the proxy variables at a closed port.

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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
// PATH stays because the linker and the OS need it on some platforms. Every
// credential source is cleared, so a developer machine behaves like CI.
func e2eEnv(t *testing.T, extra ...string) []string {
	t.Helper()

	return append([]string{
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
	}, extra...)
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
// region, no cache, and no proxy unless the caller adds one. That makes a
// developer machine with credentials behave like CI.
func runSpotinfo(t *testing.T, extraEnv []string, args ...string) e2eResult {
	t.Helper()

	if testing.Short() {
		t.Skip("end-to-end test compiles the binary")
	}

	binary, err := buildSpotinfo()
	require.NoError(t, err, "building the binary under test")

	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, args...)

	cmd.Env = e2eEnv(t, extraEnv...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	require.False(t, errors.Is(ctx.Err(), context.DeadlineExceeded),
		"the command did not finish in %s: %v", e2eTimeout, args)

	result := e2eResult{stdout: stdout.String(), stderr: stderr.String()}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		result.exitCode = 0
	case errors.As(runErr, &exitErr):
		result.exitCode = exitErr.ExitCode()
	default:
		require.NoError(t, runErr, "running the binary under test")
	}

	return result
}

// offlineArgs is the flag set that keeps AWS from reaching the network. GCP
// and Azure ignore it because they have no live path, so passing it
// everywhere keeps the table rows uniform.
func offlineArgs(cloud string) []string {
	return []string{
		"recommend",
		"--cloud", cloud,
		"--architecture", "x86_64",
		"--cpu", "2",
		"--memory", "8",
		"--top", "3",
		"--offline",
	}
}

// dataRows returns the table rows below the header.
func dataRows(t *testing.T, table string) []string {
	t.Helper()

	var rows []string

	scanner := bufio.NewScanner(strings.NewReader(table))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "RANK") {
			continue
		}

		rows = append(rows, line)
	}

	require.NoError(t, scanner.Err())

	return rows
}

func TestEveryCloudAnswersFromTheShippedBinary(t *testing.T) {
	t.Parallel()

	// Column names are part of what a user reads, so they are asserted. The
	// values are not: the snapshots are refreshed weekly and a price moving
	// is not a regression.
	for _, tc := range []struct {
		cloud   string
		columns []string
	}{
		{cloud: "aws", columns: []string{"RANK", "REGION", "INSTANCE", "ARCHITECTURE", "vCPU", "MEMORY GiB", "USD/HOUR", "SAVINGS", "INTERRUPTION", "WHY"}},
		{cloud: "gcp", columns: []string{"RANK", "CLOUD", "REGION", "MACHINE", "ARCHITECTURE", "vCPU", "MEMORY GiB", "USD/HOUR", "SAVINGS", "RISK", "WHY"}},
		{cloud: "azure", columns: []string{"RANK", "CLOUD", "REGION", "MACHINE", "ARCHITECTURE", "vCPU", "MEMORY GiB", "USD/HOUR", "SAVINGS", "RISK", "WHY"}},
	} {
		t.Run(tc.cloud, func(t *testing.T) {
			t.Parallel()

			got := runSpotinfo(t, nil, offlineArgs(tc.cloud)...)

			require.Equal(t, 0, got.exitCode, "stderr: %s", got.stderr)

			header, _, found := strings.Cut(got.stdout, "\n")
			require.True(t, found, "no table in output: %q", got.stdout)

			for _, column := range tc.columns {
				assert.Contains(t, header, column)
			}

			assert.Len(t, dataRows(t, got.stdout), 3, "--top 3 must return three rows")
		})
	}
}

// recommendReport is the part of spotinfo.recommend/v2 this suite reads. The
// full schema is pinned by cmd/spotinfo/contract_v2_test.go; here the point
// is that the shipped binary emits it, not that the shape is complete.
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
	DataSource struct {
		Provider string `json:"provider"`
		Mode     string `json:"mode"`
		Sources  []struct {
			URL           string `json:"url"`
			ContentSHA256 string `json:"content_sha256"`
		} `json:"sources"`
	} `json:"data_source"`
	Recommendations []struct {
		Rank         int     `json:"rank"`
		Cloud        string  `json:"cloud"`
		Region       string  `json:"region"`
		Machine      string  `json:"machine"`
		Architecture string  `json:"architecture"`
		VCPU         int     `json:"vcpu"`
		MemoryGiB    float64 `json:"memory_gib"`
		Risk         struct {
			Status string `json:"status"`
			Kind   string `json:"kind"`
		} `json:"risk"`
	} `json:"recommendations"`
}

// AWS is deliberately absent: with the CLI default workload it answers in
// spotinfo.recommend/v1, not v2. TestTheWorkloadChoosesThePayloadSchema pins
// that rule.
func TestJSONReportIsValidForEveryCloud(t *testing.T) {
	t.Parallel()

	for _, cloud := range []string{"gcp", "azure"} {
		t.Run(cloud, func(t *testing.T) {
			t.Parallel()

			args := append(offlineArgs(cloud), "--output", "json")
			got := runSpotinfo(t, nil, args...)

			require.Equal(t, 0, got.exitCode, "stderr: %s", got.stderr)

			var report recommendReport
			require.NoError(t, json.Unmarshal([]byte(got.stdout), &report),
				"output is not JSON: %q", got.stdout)

			assert.Equal(t, "spotinfo.recommend/v2", report.SchemaVersion)
			assert.Equal(t, "ok", report.Status)
			assert.Equal(t, cloud, report.Request.Cloud)
			assert.Equal(t, cloud, report.DataSource.Provider)
			assert.Equal(t, 2, report.Request.MinVCPU)
			assert.InDelta(t, 8.0, report.Request.MinMemoryGiB, 0.001)
			assert.Equal(t, 3, report.Request.Top)
			assert.NotEmpty(t, report.DataSource.Sources, "provenance must be published")
			assert.Len(t, report.Recommendations, 3)

			// A source the updater downloaded publishes the hash of what it
			// read, so a consumer can re-fetch and compare. A source recorded
			// as provenance only — the GCP machine-architecture reference is
			// read by a person, not by the parser — publishes no hash. An
			// empty string is therefore legal, but a malformed hash is not.
			for _, source := range report.DataSource.Sources {
				assert.NotEmpty(t, source.URL)

				if source.ContentSHA256 == "" {
					continue
				}

				assert.Len(t, source.ContentSHA256, 64, "content hash must be a sha256")
				assert.Regexp(t, "^[0-9a-f]{64}$", source.ContentSHA256)
			}

			for i, candidate := range report.Recommendations {
				assert.Equal(t, i+1, candidate.Rank, "ranks must be dense and ordered")
				assert.Equal(t, cloud, candidate.Cloud)
				assert.NotEmpty(t, candidate.Region)
				assert.NotEmpty(t, candidate.Machine)
				assert.Equal(t, "x86_64", candidate.Architecture)
				assert.GreaterOrEqual(t, candidate.VCPU, 2, "the vCPU floor must hold")
				assert.GreaterOrEqual(t, candidate.MemoryGiB, 8.0, "the memory floor must hold")
			}
		})
	}
}

// The workload chooses the payload schema, not the cloud. AWS under web, ci
// or batch answers in spotinfo.recommend/v1, because that surface is
// byte-compatible with releases that predate the provider seam. Everything
// else — AWS under cost, and every other cloud, which serves cost only —
// answers in v2.
//
// The rule is not visible from the flag names, and --workload has a
// cloud-dependent default, so the schema a caller receives depends on a value
// they did not set. TestTheCLIAndMCPDisagreeOnTheDefaultAWSAnswer records what
// that costs in practice.
func TestTheWorkloadChoosesThePayloadSchema(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		cloud    string
		workload string
		version  string
	}{
		{cloud: "aws", workload: "cost", version: "spotinfo.recommend/v2"},
		{cloud: "aws", workload: "web", version: "spotinfo.recommend/v1"},
		{cloud: "aws", workload: "ci", version: "spotinfo.recommend/v1"},
		{cloud: "aws", workload: "batch", version: "spotinfo.recommend/v1"},
		{cloud: "gcp", workload: "cost", version: "spotinfo.recommend/v2"},
		{cloud: "azure", workload: "cost", version: "spotinfo.recommend/v2"},
	} {
		t.Run(tc.cloud+"/"+tc.workload, func(t *testing.T) {
			t.Parallel()

			args := append(offlineArgs(tc.cloud), "--workload", tc.workload, "--output", "json")
			got := runSpotinfo(t, nil, args...)

			require.Equal(t, 0, got.exitCode, "stderr: %s", got.stderr)

			var envelope struct {
				SchemaVersion string `json:"schema_version"`
			}

			require.NoError(t, json.Unmarshal([]byte(got.stdout), &envelope))
			assert.Equal(t, tc.version, envelope.SchemaVersion)
		})
	}
}

// The default workload is not the same on both surfaces, so the schema a
// caller gets without asking is not the same either. The CLI defaults to web
// on AWS and answers in v1; the MCP tool defaults to cost and answers in v2.
func TestTheDefaultWorkloadSelectsADifferentSchemaOnEachSurface(t *testing.T) {
	t.Parallel()

	cli := runSpotinfo(t, nil, offlineArgs("aws")...)
	require.Equal(t, 0, cli.exitCode, "stderr: %s", cli.stderr)

	args := append(offlineArgs("aws"), "--output", "json")
	cliJSON := runSpotinfo(t, nil, args...)

	var envelope struct {
		SchemaVersion string `json:"schema_version"`
	}

	require.NoError(t, json.Unmarshal([]byte(cliJSON.stdout), &envelope))
	assert.Equal(t, "spotinfo.recommend/v1", envelope.SchemaVersion,
		"the CLI default workload is web, which selects v1")

	fromMCP := callRecommendOverMCP(t,
		`{"cloud":"aws","architecture":"x86_64","min_vcpu":2,"min_memory_gib":8}`)
	assert.Equal(t, "spotinfo.recommend/v2", fromMCP.SchemaVersion,
		"the MCP default workload is cost, which selects v2")
}

// A cloud with no redistributable risk data must report the absence, never a
// zero or a low bucket. A neutral consumer that ranked silence against a
// measurement would prefer the cloud that publishes less.
func TestACloudWithoutRiskDataReportsUnavailableRatherThanZero(t *testing.T) {
	t.Parallel()

	for _, cloud := range []string{"gcp", "azure"} {
		t.Run(cloud, func(t *testing.T) {
			t.Parallel()

			args := append(offlineArgs(cloud), "--output", "json")
			got := runSpotinfo(t, nil, args...)

			require.Equal(t, 0, got.exitCode, "stderr: %s", got.stderr)

			var report recommendReport
			require.NoError(t, json.Unmarshal([]byte(got.stdout), &report))
			require.NotEmpty(t, report.Recommendations)

			for _, candidate := range report.Recommendations {
				assert.Equal(t, "unavailable", candidate.Risk.Status,
					"%s publishes no redistributable risk", cloud)
			}
		})
	}
}

// The offline claim is that no request is made at all. A dead proxy turns any
// attempt into a connection error, so a passing run is evidence and not a
// promise.
func TestOfflineAnswersWithEveryOutboundRequestBlocked(t *testing.T) {
	t.Parallel()

	blocked := blockNetwork()

	for _, cloud := range []string{"aws", "gcp", "azure"} {
		t.Run(cloud, func(t *testing.T) {
			t.Parallel()

			got := runSpotinfo(t, blocked, offlineArgs(cloud)...)

			require.Equal(t, 0, got.exitCode,
				"a blocked network must not change the answer; stderr: %s", got.stderr)
			assert.Len(t, dataRows(t, got.stdout), 3)
		})
	}
}

// Rejections must go to stderr and leave stdout empty, so a caller that pipes
// stdout into a JSON parser gets nothing rather than a half report.
func TestRejectedInputExitsNonZeroWithAnEmptyStdout(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		args     []string
		contains string
	}{
		{
			name:     "recommend without a size floor",
			args:     []string{"recommend", "--architecture", "x86_64"},
			contains: "--cpu, --memory are required",
		},
		{
			name:     "recommend without an architecture",
			args:     []string{"recommend", "--cpu", "2", "--memory", "8"},
			contains: "architecture",
		},
		{
			name:     "a risk-aware workload on a cloud with no risk data",
			args:     []string{"recommend", "--cloud", "gcp", "--architecture", "x86_64", "--cpu", "2", "--memory", "8", "--workload", "web"},
			contains: "unsupported capability: risk",
		},
		{
			name:     "the query command on a non-AWS cloud",
			args:     []string{"--cloud", "azure", "--type", "t3.*"},
			contains: "spotinfo recommend --cloud azure",
		},
		{
			name:     "windows on a cloud that prices linux only",
			args:     []string{"recommend", "--cloud", "gcp", "--architecture", "x86_64", "--cpu", "2", "--memory", "8", "--os", "windows"},
			contains: "windows",
		},
		{
			name:     "a region the cloud does not publish",
			args:     []string{"recommend", "--cloud", "gcp", "--architecture", "x86_64", "--cpu", "2", "--memory", "8", "--region", "eu-west-1"},
			contains: "eu-west-1",
		},
		{
			name:     "an unknown cloud",
			args:     []string{"recommend", "--cloud", "oracle", "--architecture", "x86_64", "--cpu", "2", "--memory", "8"},
			contains: "oracle",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := runSpotinfo(t, nil, tc.args...)

			assert.NotEqual(t, 0, got.exitCode, "stdout: %s", got.stdout)
			assert.Empty(t, strings.TrimSpace(got.stdout),
				"a rejected request must not print a partial answer")
			assert.Contains(t, got.stderr, tc.contains)
		})
	}
}

// --top is bounded on the CLI because the bound is written into request.top
// in the published v2 schema. An unbounded flag emitted a document that
// failed its own contract.
func TestTopAboveThePublishedBoundIsRejected(t *testing.T) {
	t.Parallel()

	got := runSpotinfo(t, nil,
		"recommend", "--cloud", "gcp", "--architecture", "x86_64",
		"--cpu", "2", "--memory", "8", "--top", "999", "--output", "json")

	assert.NotEqual(t, 0, got.exitCode)
	assert.Empty(t, strings.TrimSpace(got.stdout))
	assert.Contains(t, got.stderr, "top")
}

// Two identical offline invocations must produce identical bytes. Anything
// that varies — a map iteration order, a timestamp, an unstable sort — shows
// up here and nowhere else in the suite.
func TestAnOfflineAnswerIsReproducible(t *testing.T) {
	t.Parallel()

	args := append(offlineArgs("azure"), "--output", "json")

	first := runSpotinfo(t, nil, args...)
	second := runSpotinfo(t, nil, args...)

	require.Equal(t, 0, first.exitCode, "stderr: %s", first.stderr)
	require.Equal(t, 0, second.exitCode, "stderr: %s", second.stderr)
	assert.Equal(t, first.stdout, second.stdout)
}

func TestTheQueryCommandRendersEveryOutputFormat(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		format   string
		contains string
	}{
		{format: "table", contains: "m5.large"},
		{format: "text", contains: "m5.large"},
		{format: "csv", contains: "Price Source"},
		{format: "json", contains: `"live_price"`},
	} {
		t.Run(tc.format, func(t *testing.T) {
			t.Parallel()

			got := runSpotinfo(t, nil,
				"--type", "^m5\\.large$", "--region", "us-east-1",
				"--offline", "--output", tc.format)

			require.Equal(t, 0, got.exitCode, "stderr: %s", got.stderr)
			assert.Contains(t, got.stdout, tc.contains)
		})
	}
}

// number is the format a shell pipes into a comparison, so it must print one
// bare savings percent and nothing else. A stray label or unit breaks every
// caller that wraps it in an arithmetic test.
func TestTheNumberFormatPrintsOnlyASavingsPercent(t *testing.T) {
	t.Parallel()

	got := runSpotinfo(t, nil,
		"--type", "^m5\\.large$", "--region", "us-east-1", "--offline", "--output", "number")

	require.Equal(t, 0, got.exitCode, "stderr: %s", got.stderr)

	savings, err := strconv.Atoi(strings.TrimSpace(got.stdout))
	require.NoError(t, err, "output is not a bare number: %q", got.stdout)
	assert.GreaterOrEqual(t, savings, 0)
	assert.LessOrEqual(t, savings, 100)
}

// live_price lost its omitempty on purpose: it is the only field that tells a
// JSON consumer whether a price came from the static feed or the EC2 API, and
// under omitempty "static feed" and "this build does not report provenance"
// were the same document.
func TestTheQueryCommandAlwaysPublishesPriceProvenance(t *testing.T) {
	t.Parallel()

	got := runSpotinfo(t, nil,
		"--type", "^m5\\.", "--region", "us-east-1", "--offline", "--output", "json")

	require.Equal(t, 0, got.exitCode, "stderr: %s", got.stderr)

	var advices []struct {
		Instance  string `json:"instance"`
		LivePrice *bool  `json:"live_price"`
	}

	require.NoError(t, json.Unmarshal([]byte(got.stdout), &advices))
	require.NotEmpty(t, advices)

	for _, advice := range advices {
		assert.NotNil(t, advice.LivePrice, "%s omitted live_price", advice.Instance)
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
	require.NoError(t, err)

	handshake := make([]string, 0, 2+len(requests))
	handshake = append(handshake,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "--mcp", "--quiet")
	// An AWS tool call would otherwise fetch both feeds: measured at 5.5s
	// against 49ms for a snapshot-only cloud. A dead proxy forces the snapshot
	// and makes the suite's network-free claim true for this path too.
	//
	// `--offline --mcp` also works — the root flag composes with the server,
	// and both land on the snapshot in the same time. The dead proxy is chosen
	// deliberately anyway: --offline takes the explicit embedded path, while a
	// blocked network takes the live-then-snapshot fallback, which nothing else
	// exercises over MCP. Switch to --offline only if that fallback gets its
	// own test.
	cmd.Env = e2eEnv(t, blockNetwork()...)
	cmd.Stdin = strings.NewReader(strings.Join(append(handshake, requests...), "\n") + "\n")

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	// Checked before the error, so a server that hangs reports the hang rather
	// than the generic kill that follows it.
	require.False(t, errors.Is(ctx.Err(), context.DeadlineExceeded),
		"the MCP server did not finish in %s; stderr: %s", e2eTimeout, stderr.String())
	require.NoError(t, runErr, "stderr: %s", stderr.String())

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

		require.Nil(t, response.Error, "the server reported an error: %+v", response.Error)

		responses[response.ID] = response
	}

	require.NoError(t, scanner.Err())

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

// callRecommendOverMCP runs one recommend_spot_instances call and decodes the
// v2 report it returns.
func callRecommendOverMCP(t *testing.T, arguments string) recommendReport {
	t.Helper()

	responses := mcpSession(t, fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recommend_spot_instances","arguments":%s}}`,
		arguments))

	call, ok := responses[2]
	require.True(t, ok, "the server returned no response for the tool call")
	require.NotEmpty(t, call.Result.Content)

	var report recommendReport
	require.NoError(t, json.Unmarshal([]byte(call.Result.Content[0].Text), &report),
		"tool result is not a v2 report: %q", call.Result.Content[0].Text)

	return report
}

func TestMCPServerCompletesAHandshakeAndAnswers(t *testing.T) {
	t.Parallel()

	responses := mcpSession(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)

	listing, ok := responses[2]
	require.True(t, ok, "the server returned no tool listing")

	toolNames := make([]string, 0, len(listing.Result.Tools))
	for _, tool := range listing.Result.Tools {
		toolNames = append(toolNames, tool.Name)
		assert.NotEmpty(t, tool.Description, "%s has no description", tool.Name)
	}

	assert.ElementsMatch(t,
		[]string{"find_spot_instances", "list_spot_regions", "recommend_spot_instances"},
		toolNames)
}

// Every cloud answers in v2 over MCP, including AWS. The CLI does not — see
// TestTheWorkloadChoosesThePayloadSchema.
func TestMCPAnswersEveryCloudInV2(t *testing.T) {
	t.Parallel()

	for _, cloud := range []string{"aws", "gcp", "azure"} {
		t.Run(cloud, func(t *testing.T) {
			t.Parallel()

			report := callRecommendOverMCP(t, fmt.Sprintf(
				`{"cloud":%q,"architecture":"x86_64","min_vcpu":2,"min_memory_gib":8}`, cloud))

			assert.Equal(t, "spotinfo.recommend/v2", report.SchemaVersion)
			assert.Equal(t, cloud, report.Request.Cloud)
			assert.NotEmpty(t, report.Recommendations)
		})
	}
}

// The CLI and the MCP server answer the same AWS question differently, and
// the difference is not cosmetic: the CLI defaults to one region and the web
// workload, the MCP tool defaults to every region and the cost workload. The
// ranking policy differs as a result, so the top pick differs.
//
// This test records the divergence rather than approving it. If the defaults
// are ever aligned, delete the test — do not relax it.
func TestTheCLIAndMCPDisagreeOnTheDefaultAWSAnswer(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("end-to-end test compiles the binary")
	}

	cli := runSpotinfo(t, nil,
		"recommend", "--cloud", "aws", "--architecture", "x86_64",
		"--cpu", "2", "--memory", "8", "--offline", "--output", "json")
	require.Equal(t, 0, cli.exitCode, "stderr: %s", cli.stderr)

	var fromCLI struct {
		Request struct {
			Regions  []string `json:"regions"`
			Workload string   `json:"workload"`
		} `json:"request"`
	}

	require.NoError(t, json.Unmarshal([]byte(cli.stdout), &fromCLI))

	fromMCP := callRecommendOverMCP(t, `{"cloud":"aws","architecture":"x86_64","min_vcpu":2,"min_memory_gib":8}`)

	assert.Equal(t, []string{"us-east-1"}, fromCLI.Request.Regions,
		"the CLI defaults to a single region")
	assert.Equal(t, "web", fromCLI.Request.Workload,
		"the CLI defaults to the risk-aware web workload on AWS")

	assert.Equal(t, []string{"all"}, fromMCP.Request.Regions,
		"the MCP tool defaults to every region")
	assert.Equal(t, "cost", fromMCP.Request.Workload,
		"the MCP tool defaults to the risk-free cost workload")
}

func TestVersionIsReportedWithoutTouchingAnyData(t *testing.T) {
	t.Parallel()

	got := runSpotinfo(t, nil, "--version")

	require.Equal(t, 0, got.exitCode, "stderr: %s", got.stderr)
	assert.Contains(t, got.stdout, "spotinfo")
}
