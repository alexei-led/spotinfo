# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`spotinfo` is a Go CLI tool that provides command-line access to AWS EC2 Spot Instance pricing and interruption data. It uses embedded AWS data feeds as fallback when network connectivity is unavailable.

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
- `make verify-data` - Parse gate: fails if the embedded files are not valid data

### Dependencies
- `make check-deps` - Verify system has required dependencies (wget)
- `make setup-tools` - Install golangci-lint (only that; mockery is not installed)

## Architecture

### Core Components
- `cmd/spotinfo/main.go` - CLI entry point using urfave/cli/v2
- `internal/spot/` - Core business logic package
  - `client.go` - Spot client orchestration and option handling
  - `data.go` - Both `//go:embed` directives, feed fetching, and static price parsing
  - `liveprice.go` - Live price fallback via EC2 DescribeSpotPriceHistory API
  - `types.go` - Core data types (Advice, TypeInfo, Range, etc.)
  - `score.go` - Spot placement scores via EC2 API
  - `data/` - Embedded JSON data files from AWS feeds
- `internal/mcp/` - MCP server tools and handlers

There is no `price.go`; static pricing lives in `data.go` alongside the advisor parsing.

### Data Sources
The tool uses three data sources:
1. Spot Instance Advisor data: `https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json`
2. Spot pricing data: `https://website.spot.ec2.aws.a2z.com/spot.json` (the feed behind
   https://aws.amazon.com/ec2/spot/pricing/ — replaces the legacy JSONP
   `spot-price.s3.amazonaws.com/spot.js`, frozen since 2024-05-13)
3. EC2 DescribeSpotPriceHistory API: Live fallback for newer instance types with $0 in the static feed

Sources 1-2 are embedded in the binary during build for offline capability.

### Key Libraries
- `github.com/urfave/cli/v2` - CLI framework
- `github.com/jedib0t/go-pretty/v6` - Table formatting
- `github.com/aws/aws-sdk-go-v2` - AWS SDK for live pricing and placement scores
- `github.com/mark3labs/mcp-go` - MCP server implementation (`internal/mcp/`)
- `github.com/bluele/gcache` - Placement score caching
- `github.com/spf13/cast` - Type coercion for MCP tool arguments
- `github.com/stretchr/testify` - Testing framework with assertions

## Build Requirements
- Go 1.26+
- wget (for data updates)
- golangci-lint (installed via make setup-tools)

## Output Formats
The CLI supports multiple output formats: number, text, json, table, csv

## CI/CD Pipeline

### GitHub Actions Workflows
- **ci.yaml**: Modern CI with Go 1.26, tests, linting, matrix builds for all platforms
- **release.yaml**: Tag-triggered releases with binary uploads using standard Go toolchain
- **docker.yaml**: Multi-arch Docker images published to GitHub Container Registry (ghcr.io)
- **auto-release.yaml**: Quarterly automated releases with smart change detection and semantic versioning
- **update-data.yaml**: Weekly refresh of the embedded AWS feeds; warns on stale feeds and opens a PR. This is the only workflow that touches `internal/spot/data/`.

### Docker
- **Build**: `docker build -t spotinfo .` (uses Go 1.26 and `make build`)
- **Multi-arch**: Supports linux/amd64 and linux/arm64 platforms
- **Registry**: Published to `ghcr.io/alexei-led/spotinfo`
- **Base**: Uses scratch image with ca-certificates for minimal attack surface

### Release Process
1. **Manual Release**: Create and push a tag starting with 'v' (e.g., `git tag v1.2.3 && git push origin v1.2.3`)
2. **Automated Release**: Runs quarterly (Jan/Apr/Jul/Oct) with automatic version bumping
3. **Artifacts**: Cross-platform binaries for Linux/macOS/Windows on AMD64/ARM64

## Testing
- **Framework**: Uses testify for assertions and test structure
- **Parallel Execution**: Tests run in parallel for better performance
- **Resilient Patterns**: Tests are resilient to data changes from external feeds
- **Coverage**: Comprehensive test coverage with `make test-coverage`

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

**Update flow:**
1. `make update-data` — fetches fresh `spot-advisor-data.json`
2. `make update-price` — fetches fresh `spot-price-data.json`
3. `make verify-data` — parse gate on the embedded files
4. Commit both files

Both targets download to a `.tmp` file and only replace the tracked file on success,
so a failed download cannot clobber good data.

**When to update:** Normally never by hand — the `update-data` workflow runs weekly and
opens a PR. Build, test and release workflows deliberately do NOT refresh the feeds, so
every binary embeds exactly what is committed. Run the targets manually only when you need
data fresher than the last PR.

**If prices show $0:** the instance type is missing from the static price feed. That is
expected for very new families and for regions AWS omits (currently all `me-*`); those fall
through to the live `DescribeSpotPriceHistory` path, which needs AWS credentials.

## Provider Interfaces (Key Pattern)

All data sources use interfaces for testability. Never call AWS directly in tests.

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
In production: use `NewWithOptions()` which wires up real AWS providers.
For injection: use `NewWithProviders(advisor, pricing)` + `SetLivePriceProvider()`.

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

- **Unit tests** use mock providers from `mocks_test.go` — no AWS credentials needed
- **Integration tests** require real AWS credentials — skip with `-short` flag
- **Pattern**: `if testing.Short() { t.Skip("requires AWS credentials") }`
- **Parallel**: all unit tests use `t.Parallel()` — keep it that way
- **Table-driven**: use `tc := tc` (loop variable capture) or Go 1.22+ range semantics

When adding a new feature:
1. Add mock support in `mocks_test.go` if new interface method needed
2. Write unit test with mock provider
3. Optionally add integration test guarded by `testing.Short()`

## Common Mistakes to Avoid

- **Never** call `enrichMissingPrices` with a nil provider — it's a no-op but check the guard
- **Never** forget `Advice.LivePrice = true` when price comes from EC2 API (not static feed)
- **Never** bypass the `maxPrice` re-filter after live price enrichment
- `allRegionsKeyword = "all"` is the special value for `--region all`, not an actual region
- `defaultScoreTimeout` and `livePriceTimeout` are separate — don't confuse them
- **Never** make `build` depend on `update-data`/`update-price`. It used to, which made every
  build and Docker image download fresh feeds and overwrite tracked data. Builds must be
  hermetic; refreshing data is a separate, explicit step.
- The static price feed is an **undocumented** endpoint (`website.spot.ec2.aws.a2z.com/spot.json`),
  not a published AWS API. Its predecessor froze silently for two years. If prices look wrong
  across the board, check the feed's `Last-Modified` before debugging the code.
