# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`spotinfo` is a Go CLI tool for exploring Spot instance pricing across AWS, GCP and Azure. It
uses embedded data snapshots so every provider works offline; AWS additionally falls back to
live EC2 APIs when credentials are present.

AWS is the original surface and stays byte-compatible: the root query command, the
`spotinfo.recommend/v1` schema and the `find_spot_instances` MCP tool are unchanged. GCP and
Azure are served through a provider-neutral seam (`internal/cloud`) and are reachable from
`spotinfo recommend --cloud <id>` and the `recommend_spot_instances` MCP tool, which speak
`spotinfo.recommend/v2`. Neither publishes interruption risk, so both serve only the risk-free
`cost` workload; the root query command renders an interruption column and is AWS-only.

## Development Commands

### Building

- `make build` - Build binary for current OS/arch. Hermetic: embeds the committed data, never downloads.
- `make all` - Alias for `make build` (nothing more, despite the name)
- `make release` - Build binaries for multiple platforms

### Testing

- `make test` - `go test ./...`
- `make test-verbose` - `go test -v ./...`
- `make test-race` - Run tests with race detector
- `make test-coverage` - Run tests with coverage reporting

### Code Quality

- `make lint` - Run golangci-lint with config from `.golangci.yaml`
- `make fmt` - `go fmt ./...`

### Data Updates

- `make update-data` - Update embedded Spot Advisor data from AWS
- `make update-price` - Update embedded spot pricing data from AWS
- `make update-gcp-data` - Rebuild the embedded GCP catalogue from its contracted pages
- `make update-azure-data` - Rebuild the embedded Azure catalogue from the Retail Prices API
  and the contracted Microsoft Learn size pages
- `make refresh-manifests` - Rewrite AWS sidecar manifest hashes (run by the update targets)
- `make verify-data` - Data gate: manifests, source contracts, parser contracts, coverage floors

### Architecture Gates

- `make verify-architecture-rules` - archfit package-boundary and layer gate
- `make verify-architecture` - the above, then `cmd/archfitcheck` fails on any open Critical or
  High archfit finding

### Dependencies

- `make check-deps` - Verify system has required dependencies (wget)
- `make setup-tools` - Install golangci-lint and mockery at their pinned versions. archfit
  is not installed here; `make verify-architecture-rules` installs it on demand.

## Architecture

### Core Components

- `internal/cloud/` - The provider-neutral domain. `Query`, `Result`, `Candidate`,
  `Capabilities`, fixed-point `Money`, risk/price/source observations, the neutral recommender
  and the `spotinfo.recommend/v2` DTOs. It imports no provider SDK, no CLI and no MCP package;
  `dependencies_test.go` enforces that.
- `internal/snapshot/` - Manifest and source-contract contracts shared by every embedded
  snapshot, plus the fail-closed validators and the atomic writer the update commands use.
- `internal/providers/` - `registry.go` compiles the recognised providers. A provider whose
  snapshot fails a gate is _disabled_, never substituted by another cloud.
  - `aws/` - Adapter over the legacy `internal/spot` client
  - `gcp/` - Offline catalogue parsed from Google's server-rendered pricing pages
  - `azure/` - Offline catalogue from the Retail Prices API joined to Learn size pages
- `cmd/update-gcp-data/`, `cmd/update-azure-data/` - Anonymous snapshot updaters
- `cmd/archfitcheck/` - Fails the build on an open Critical or High archfit finding
- `cmd/spotinfo/main.go` - CLI entry point using urfave/cli/v2
- `internal/spot/` - Legacy AWS business logic. Still the AWS acquisition path
  - `client.go` - Spot client orchestration and option handling
  - `data.go` - Both `//go:embed` directives, feed fetching, and static price parsing
  - `liveprice.go` - Live price fallback via EC2 DescribeSpotPriceHistory API
  - `types.go` - Core data types (Advice, TypeInfo, Range, etc.)
  - `score.go` - Spot placement scores via EC2 API
  - `data/` - Embedded JSON data files from AWS feeds
- `internal/mcp/` - MCP server tools and handlers

There is no `price.go`; static pricing lives in `data.go` alongside the advisor parsing.

### Data Sources

AWS (three sources):

1. Spot Instance Advisor data: `https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json`
2. Spot pricing data: `https://website.spot.ec2.aws.a2z.com/spot.json` (the feed behind
   <https://aws.amazon.com/ec2/spot/pricing/> — replaces the legacy JSONP
   `spot-price.s3.amazonaws.com/spot.js`, frozen since 2024-05-13)
3. EC2 DescribeSpotPriceHistory API: Live fallback for newer instance types with $0 in the static feed

Sources 1-2 are embedded in the binary during build for offline capability.

GCP: Google's public server-rendered Spot and Compute pricing pages, `us-central1` only.
Azure: the anonymous Azure Retail Prices API for amounts, joined to Microsoft Learn VM size
pages for vCPU, memory and processor architecture; eight reviewed regions, 26 machine series.

Every non-AWS snapshot is governed by an approved
`internal/providers/<cloud>/data/source-contract.json` that enumerates its exact source URLs,
support matrix and thresholds. No provider may read a source the contract does not name, and
`make verify-data` fails when a snapshot contradicts its contract. See
`docs/research/multicloud-source-contracts.md` for the reasoning behind each approval.

### Key Libraries

- `github.com/urfave/cli/v2` - CLI framework. **Stay on v2.** v3 is stable but the
  migration rewrites the entire CLI surface (`cli.Command` replaces `cli.App`, changed
  context and flag access) for no user-visible gain. Do not "upgrade" it as part of a
  routine dependency bump; revisit only if v2 stops getting security fixes.
- `github.com/jedib0t/go-pretty/v6` - Table formatting
- `github.com/aws/aws-sdk-go-v2` - AWS SDK for live pricing and placement scores
- `golang.org/x/net/html` - HTML parsing for the GCP and Azure snapshot updaters (build-time
  data refresh only; the shipped binary makes no request)
- `github.com/mark3labs/mcp-go` - MCP server implementation (`internal/mcp/`)
- `github.com/bluele/gcache` - Placement score caching
- `github.com/spf13/cast` - Type coercion for MCP tool arguments
- `github.com/stretchr/testify` - Testing framework with assertions

## Build Requirements

- Go 1.26+
- wget (for data updates)
- golangci-lint and mockery (installed via `make setup-tools`, pinned in the Makefile)
- archfit (pinned by `ARCHFIT_VERSION`; installed on demand by
  `make verify-architecture-rules`, not by `make setup-tools`)

## Output Formats

The CLI supports multiple output formats: number, text, json, table, csv

## CI/CD Pipeline

### GitHub Actions Workflows

- **ci.yaml**: Modern CI with Go 1.26, tests, linting, the pinned archfit architecture gate
  (`make verify-architecture`), then matrix builds for all platforms
- **release.yaml**: Tag-triggered releases with binary uploads using standard Go toolchain
- **docker.yaml**: Multi-arch Docker images published to GitHub Container Registry (ghcr.io)
- **auto-release.yaml**: Quarterly automated releases with smart change detection and semantic versioning
- **update-data.yaml**: Weekly refresh of the embedded AWS feeds; warns on stale feeds and opens a PR. This is the only workflow that touches `internal/spot/data/`.
- **update-gcp-data.yaml**: Weekly refresh of `internal/providers/gcp/data/`; anonymous, opens one PR
- **update-azure-data.yaml**: Weekly refresh of `internal/providers/azure/data/`; anonymous, opens one PR

Each data workflow owns exactly one provider's directory and fails closed rather than committing
partial data.

### Docker

- **Build**: `docker build -t spotinfo .` (uses Go 1.26 and `make build`)
- **Multi-arch**: Supports linux/amd64 and linux/arm64 platforms
- **Registry**: Published to `ghcr.io/alexei-led/spotinfo`
- **Base**: Uses scratch image with ca-certificates for minimal attack surface

### Release Process

1. **Manual Release**: Create and push a tag starting with 'v' (e.g., `git tag v1.2.3 && git push origin v1.2.3`)
2. **Automated Release**: Runs quarterly (Jan/Apr/Jul/Oct) with automatic version bumping
3. **Artifacts**: Cross-platform binaries for Linux/macOS/Windows on AMD64/ARM64

## Development Guidance

- Use `make` commands for all development tasks
- Run `make test-verbose` before committing changes
- Embedded data is refreshed by the weekly `update-data` workflow via PR — see Data Update
  Workflow below before refreshing it by hand
- Follow Go 1.26 best practices and modern testing patterns
- NEVER add Claude as co-author to git commit message

## Data Update Workflow

The embedded data files are critical — they provide offline capability:

- `internal/spot/data/spot-advisor-data.json` — Interruption rates, savings % (AWS advisor feed)
- `internal/spot/data/spot-price-data.json` — Static spot pricing (AWS pricing-page feed, plain JSON)
- `internal/spot/data/{spot-advisor,spot-price,architecture}-manifest.json` — the sidecar
  manifests that hash and describe those files

**Update flow:**

1. `make update-data` — fetches fresh `spot-advisor-data.json`
2. `make update-price` — fetches fresh `spot-price-data.json`
3. `make verify-data` — parse gate on the embedded files
4. Commit the refreshed data files **and** their manifest sidecars — both update targets
   end by running `refresh-manifests`, and `verify-data` fails when a data file and its
   manifest hash disagree

Both targets download to a `.tmp` file and only replace the tracked file on success,
so a failed download cannot clobber good data.

**When to update:** Normally never by hand — the `update-data` workflow runs weekly and
opens a PR. Build, test and release workflows deliberately do NOT refresh the feeds, so
every binary embeds exactly what is committed. Run the targets manually only when you need
data fresher than the last PR.

**If prices show $0:** the instance type is missing from the static price feed. That is
expected for very new families and for regions AWS omits (currently all `me-*`); those fall
through to the live `DescribeSpotPriceHistory` path, which needs AWS credentials.

### Updating a GCP or Azure snapshot

`make update-gcp-data` / `make update-azure-data`, then `make verify-data`, then commit the
whole `internal/providers/<cloud>/data/` directory. Both updaters validate against the source
contract, the manifest hash and the reviewed coverage floor **before** writing, so a failed run
leaves the reviewed snapshot untouched. Both are normally run by their weekly workflow.

A refresh failure is usually the source changing shape, not a flake. `docs/data-sources.md`
tables every expected error and what it means. Two rules matter most:

- The coverage floor is a **floor**, not a census, and for Azure it is applied **per region**.
  Never lower it to make a short refresh pass.
- Never widen a parser to make a changed source fit. Review the source, then bump
  `parser_version` in both the parser and the source contract.

## Provider Interfaces (Key Pattern)

Two layers of interface. The neutral one is what new code should use:

```go
// cloud.Provider — the neutral seam every cloud implements (internal/cloud/provider.go)
type Provider interface {
    ID() ProviderID
    Capabilities() Capabilities
    Query(ctx context.Context, query *Query) (Result, error)
}
```

A provider declares what it can answer through `Capabilities`, and the CLI and MCP layers reject
an unsupported request with `UNSUPPORTED_CAPABILITY` _before_ acquisition. Never let a provider
answer a question it cannot: a cloud with no risk data must report
`RiskStatusUnavailable`, never a zero or a low bucket, or a neutral consumer will rank its
silence against another cloud's measurement.

A published risk figure is not automatically comparable either. The `web`/`ci`/`batch`
ceilings are AWS Spot Advisor bucket boundaries, so `acceptsRisk` admits only a kind listed
in `interruptionCappableKinds` (`internal/cloud/recommend.go`). Adding a provider that
publishes a different measurement means giving it a reviewed wire name in
`riskKindWireNames` and deciding, deliberately, whether it belongs in that list — not
letting a preemption rate be filtered as if it were an interruption frequency.

The legacy AWS interfaces below still back `internal/spot`. Never call AWS directly in tests.

```go
// advisorProvider — embedded/remote advisor data
type advisorProvider interface {
    getRegions() []string
    getRegionAdvice(region, os string) (map[string]spotAdvice, error)
    getInstanceType(instance string) (TypeInfo, error)
    getRange(index int) (Range, error)
}

// pricingProvider — static pricing data
type pricingProvider interface {
    getSpotPrice(instance, region, os string) (float64, error)
}

// livePriceProvider — EC2 API fallback for zero-price instances
type livePriceProvider interface {
    fetchLivePrices(ctx context.Context, region string, instanceTypes []string, os string) (map[string]float64, error)
}

// scoreProvider — EC2 placement scores
type scoreProvider interface {
    enrichWithScores(ctx context.Context, advices []Advice, singleAZ bool, timeout time.Duration) error
}
```

In tests: use `mocks_test.go` which implements all interfaces with controllable behavior.

> **`mocks_test.go` is generated** — it opens with `// Code generated by mockery; DO NOT EDIT.`
> Never hand-edit it. Regenerate with `make mocks` (mockery pinned via `MOCKERY_VERSION`
> in the Makefile, also installed by `make setup-tools`).
>
> **Every mocked interface must be listed in `.mockery.yaml`.** A mock that exists only by
> hand inside the generated file is silently deleted by the next run. `livePriceProvider`
> was in exactly that state and is now declared properly, alongside `advisorProvider`,
> `pricingProvider`, `scoreProvider`, `awsAPIProvider` and `spotClient`.
> In production: use `NewWithOptions()` which wires up real AWS providers.
> For injection: use `NewWithProviders(advisor, pricing)` + `SetLivePriceProvider()`.

## Functional Options Pattern

`GetSpotSavings` uses functional options — add new filters without breaking existing callers:

```go
// Adding a new filter option:
func WithFoo(foo string) GetSpotSavingsOption {
    return func(cfg *getSpotSavingsConfig) {
        cfg.foo = foo
    }
}
// Add `foo string` field to getSpotSavingsConfig
// Apply in GetSpotSavings() after the options loop
```

## Testing Approach

- **Framework**: testify for assertions; `make test-coverage` for the coverage report
- **Unit tests** use mock providers from `mocks_test.go` (in `internal/spot` and
  `cmd/spotinfo`) or hand-written `cloud.Provider` stubs — no AWS credentials needed
- **Integration tests**: there are none today — every test runs without AWS credentials
  or network, so `-short` currently skips nothing. If you add one that needs real AWS,
  guard it with `if testing.Short() { t.Skip("requires AWS credentials") }` so
  `go test -short ./...` stays credential-free.
- **Parallel**: all unit tests use `t.Parallel()` — keep it that way
- **Table-driven**: use `tc := tc` (loop variable capture) or Go 1.22+ range semantics
- **Resilient**: assertions tolerate the embedded feeds changing under them
- **No network in unit tests**: build clients via the embedded path and a nil live-price
  provider. A real client stalls for `livePriceTimeout` (10s) per call on unpriced
  instances — that alone was 79s of the suite. See `newEmbeddedRegistry` and `stubProvider`
  in `internal/mcp/helpers_test.go`; the MCP tests answer from a `cloud.Provider` stub and
  never open an AWS client.
- **AWS v1 output is golden-pinned**: `cmd/spotinfo/testdata/aws-*-v1.*` and
  `internal/mcp/testdata/find-spot-instances-v1-*.json`. A diff there is a client-visible
  contract break, not a test to update. For those two contract tests `UPDATE_GOLDEN=1`
  rewrites the golden **and fails the run**, so a regeneration can never be reported as a
  pass; review the diff, then re-run without it. The same variable also drives
  `make refresh-manifests`, where rewriting is the point and the run passes normally.

When adding a new feature:

1. Add the interface to `.mockery.yaml` and run `make mocks` if a new mock is needed
2. Write unit test with mock provider
3. Only if it genuinely needs live AWS, add an integration test guarded by
   `testing.Short()` — and expect to be the first, see above

## Common Mistakes to Avoid

- **Never** call `enrichMissingPrices` with a nil provider — it's a no-op but check the guard
- **Never** forget `Advice.LivePrice = true` when price comes from EC2 API (not static feed)
- **Never** bypass the `maxPrice` re-filter after live price enrichment
- `allRegionsKeyword = "all"` is the special value for `--region all`, not an actual region
- **Never** guard a float CLI flag with `value <= 0` alone. `NaN` fails every comparison, so
  it slips past both the rejection and the "is it set" branch and silently drops the filter
- **Never** lower a snapshot's coverage floor on an unreadable manifest. The updaters seed
  from the contract minimum only when the manifest is genuinely absent; anything else is an
  error, or a reviewer's raised floor is discarded by a green PR
- `defaultScoreTimeout` and `livePriceTimeout` are separate — don't confuse them
- **Never** make `build` depend on `update-data`/`update-price`. It used to, which made every
  build and Docker image download fresh feeds and overwrite tracked data. Builds must be
  hermetic; refreshing data is a separate, explicit step.
- **Never** add a provider without an approved `source-contract.json`. The contract is the
  normative list of sources; reading anything it does not name is out of contract.
- **Never** infer processor architecture from a machine name when the source publishes it. The
  Azure parser reads each page's `[x86-64]`/`[Arm64]` marker and fails when it is absent; an Arm
  size shipped as `x86_64` passes every other gate and silently recommends a machine that
  cannot run the caller's binaries.
- **Never** resolve Azure effective dates against `time.Now()` inside the parser. The instant is
  an argument so one run resolves every region consistently and a rebuild is reproducible.
- **Never** treat an Azure `Low Priority` meter as Spot, or a `Cloud Services` meter as a VM
  price. Both sit under `serviceName = "Virtual Machines"` beside the real meters.
- The static price feed is an **undocumented** endpoint (`website.spot.ec2.aws.a2z.com/spot.json`),
  not a published AWS API. Its predecessor froze silently for two years. If prices look wrong
  across the board, check the feed's `Last-Modified` before debugging the code.
