# Plan: spotinfo multi-cloud v2

## Overview

Add provider-neutral Spot and VM price exploration for AWS, GCP, and Azure.
Preserve the current AWS CLI, MCP tools, recommendation JSON, embedded-data fallback, and offline runtime.

The implementation is incremental. Each task is independently committable. Stop at every task gate when its acceptance criteria fail.

## Source artifact

Approved scope:

- GitHub issue [#35](https://github.com/alexei-led/spotinfo/issues/35): provider-neutral candidate query seam.
- GitHub issue [#36](https://github.com/alexei-led/spotinfo/issues/36): embedded snapshot manifests and parser gates.
- GitHub issue [#37](https://github.com/alexei-led/spotinfo/issues/37): provider registry and capability-aware CLI routing.
- GitHub issue [#38](https://github.com/alexei-led/spotinfo/issues/38): provider-neutral recommendations and MCP.
- GitHub issue [#28](https://github.com/alexei-led/spotinfo/issues/28): GCP public pricing tables.
- GitHub issue [#29](https://github.com/alexei-led/spotinfo/issues/29): Azure Retail Prices API.
- Umbrella issue [#27](https://github.com/alexei-led/spotinfo/issues/27).

Current evidence and decisions:

- `internal/spot/types.go:23-36` — `Advice` contains AWS interruption ranges, placement scores, AZ prices, and live-price fields.
- `internal/spot/client.go:209-325` — `Client.GetSpotSavings` combines AWS acquisition, filtering, live-price enrichment, and score behavior.
- `internal/spot/recommend.go:216-302` — `Recommend` is pure, but consumes AWS `Advice.Range` and AWS workload buckets.
- `cmd/spotinfo/main.go:382-474` — the CLI combines validation, AWS acquisition, recommendation, and rendering.
- `internal/mcp/tools.go:118-159,256-318` — MCP response shaping consumes AWS `spot.Advice` and AWS interruption semantics.
- GitNexus current index: `1,706 nodes`, `3,894 edges`, `83 flows`.
- GitNexus impact: changing `Advice` affects 22 symbols across `internal/spot`, `cmd/spotinfo`, and `internal/mcp`; risk is HIGH.
- Archfit current full gate returns success while reporting four active coupling advisories: one Critical and three Medium. Coupling score is 36/100. The `cmd_spotinfo`, `mcp`, and `spot` modules also lack declared subdomain and volatility.
- Archfit finding `bac6b2e4f1019c672ac2eec8dc470b31` is the Critical advisory: `internal/mcp/server.go -> internal/spot`. Task 4 must resolve it. Do not baseline or waive it.

External source contracts:

- AWS: preserve the existing embedded feeds. Treat the current static price feed as an undocumented source with a provider-specific parser contract.
- Azure prices: use the anonymous, documented [Azure Retail Prices API](https://learn.microsoft.com/en-us/rest/api/cost-management/retail-prices/azure-retail-prices). Use public Azure VM-size documentation for reviewed specifications and architecture.
- GCP prices: use official public server-rendered pages: [Spot VM pricing](https://cloud.google.com/spot-vms/pricing), [VM instance pricing](https://cloud.google.com/compute/vm-instance-pricing), and linked Compute pricing category pages. Treat them as public reference pages, not a supported pricing API.
- GCP preemption history: `advice.capacityHistory` is authenticated and beta. Defer it to optional live enrichment.
- Azure eviction history: Resource Graph `SpotResources` and Resource SKUs require subscription authorization. Defer them to optional live enrichment.
- Vantage and other aggregators: use for cross-checking only. Do not embed their data without explicit redistribution permission.

## Success criteria

- AWS legacy invocations produce the same documented output and behavior.
- AWS keeps `spotinfo.recommend/v1`; provider-neutral output uses `spotinfo.recommend/v2`.
- `--cloud aws|gcp|azure` defaults to AWS and rejects unsupported provider capabilities before acquisition.
- GCP and Azure support deterministic offline Linux results for the supported machine, region, architecture, price, and specification matrix.
- Unknown price, unknown architecture, stale schema, ambiguous price rows, and unavailable risk fail closed or remain explicitly marked unknown.
- No runtime provider requires an API key, OAuth token, cloud account, or subscription.
- No provider-specific numeric risk is compared as if it were an AWS interruption bucket.
- Every embedded data set has a manifest with source URL, fetch time, observation time, SHA-256, parser version, schema version, currency, and billing unit.
- Snapshot updates are explicit, hermetic, reviewable, and protected by parser and coverage gates.
- Existing AWS MCP contracts remain stable. The new `recommend_spot_instances` MCP tool has AWS, GCP, and Azure contract tests.
- Documentation explains provider coverage, source freshness, risk availability, limitations, and update procedures.
- The final archfit report contains no open Critical or High finding. The current `mcp -> spot` critical advisory is fixed, not waived.

## Validation Commands

Run the focused commands for each task before moving to the next task.
Run the full set after the final task.

- `make fmt`
- `make verify-data`
- `go test ./...`
- `make test-verbose`
- `make test-race`
- `go vet ./...`
- `make lint`
- `make build`
- `git diff --check`
- `archfit --gate --config .archfit.yaml --full --format json`
- `archfit --gate --config .archfit.yaml --base origin/master --format json`
- `gitnexus analyze --index-only`
- `gitnexus detect-changes --scope compare --base-ref origin/master --repo spotinfo`
- `gitnexus impact Advice --file internal/spot/types.go --kind Struct --direction upstream --depth 3 --include-tests --repo spotinfo`
- `gitnexus impact Recommend --file internal/spot/recommend.go --kind Function --direction upstream --depth 3 --include-tests --repo spotinfo`

Tool policy:

- Pin archfit `v1.6.0` in CI with `go install github.com/alexei-led/archfit/cmd/archfit@v1.6.0`.
- GitNexus `1.6.9` is review evidence, not a CI dependency. Run it locally or in the Ralphex review environment.
- Archfit currently passes because `no-layer-back-edges` is warning-only and Balanced-Coupling advisories do not fail the rule gate. Update module metadata and enforce both checks before provider code lands.
- `make lint` must install pinned `golangci-lint` `v2.12.2`, not `latest`.
- `make verify-architecture-rules` runs `archfit --gate --config .archfit.yaml --full --format json`.
- `make verify-architecture` first runs `verify-architecture-rules`; then it writes `archfit analyze --config .archfit.yaml --json` to a temporary file and runs `go run ./cmd/archfitcheck < temporary-file`. No shell pipeline is used. The helper exits `0` when no active Critical or High finding exists, `1` when one exists, and `2` for invalid input.

## MCP v2 contract

Keep the existing `find_spot_instances` tool name, input schema, defaults, error behavior, and response JSON as the AWS v1 compatibility contract. Store the exact current tool schema in `internal/mcp/testdata/find-spot-instances-v1-input-schema.json` and a normalized current response in `find-spot-instances-v1-response.json` before changing MCP code. The normalizer sets `metadata.query_time_ms` to `0` and sorts only arrays that the documented v1 contract treats as unordered. Runtime output is not changed.

The normative v2 contracts are machine-readable planning artifacts:

- `docs/plans/contracts/recommend-spot-instances-v2-input.schema.json`
- `docs/plans/contracts/recommend-spot-instances-v2-success.schema.json`
- `docs/plans/contracts/recommend-spot-instances-v2-error.schema.json`

The JSON below is illustrative. The schema files control types, required fields, defaults, nullability, formats, and `additionalProperties`.

Add a new `recommend_spot_instances` tool. Its input schema is:

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["architecture", "min_vcpu", "min_memory_gib"],
  "properties": {
    "cloud": {"type": "string", "enum": ["aws", "gcp", "azure"], "default": "aws"},
    "regions": {"type": "array", "items": {"type": "string"}, "default": ["all"]},
    "machine": {"type": "string", "default": ""},
    "architecture": {"type": "string", "enum": ["x86_64", "arm64"]},
    "os": {"type": "string", "enum": ["linux", "windows"], "default": "linux"},
    "min_vcpu": {"type": "integer", "minimum": 1},
    "min_memory_gib": {"type": "number", "exclusiveMinimum": 0},
    "max_price_per_hour": {"type": "number", "exclusiveMinimum": 0},
    "workload": {"type": "string", "enum": ["cost", "web", "ci", "batch"], "default": "cost"},
    "top": {"type": "integer", "minimum": 1, "maximum": 50, "default": 3}
  }
}
```

Omitted `machine` means no machine-name filter. Omitted `max_price_per_hour` means no price ceiling. `regions=["all"]` means all embedded regions for the selected provider and cannot be mixed with explicit regions. Offline GCP and Azure v1 support `linux` and `cost`; unsupported OS or risk-aware workload values return `UNSUPPORTED_CAPABILITY` before provider acquisition.

A successful result has `isError=false`. Its single text content item contains JSON that validates against `recommend-spot-instances-v2-success.schema.json`. The example below conforms to that schema. Required fields and nullability come from the schema file.

```json
{
  "schema_version": "spotinfo.recommend/v2",
  "status": "ok",
  "request": {
    "cloud": "aws",
    "regions": ["all"],
    "machine": "",
    "architecture": "x86_64",
    "os": "linux",
    "min_vcpu": 2,
    "min_memory_gib": 8,
    "max_price_per_hour": null,
    "workload": "cost",
    "top": 3
  },
  "ranking_policy": ["spot_price_ascending", "excess_vcpu_ascending", "excess_memory_gib_ascending", "region_ascending", "machine_ascending"],
  "data_source": {
    "provider": "aws",
    "mode": "embedded-snapshot",
    "sources": [
      {
        "url": "https://example.invalid/source",
        "fetched_at": "2026-08-07T00:00:00Z",
        "observed_at": null,
        "content_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
        "parser_version": "string",
        "schema_version": "string"
      }
    ]
  },
  "recommendations": [
    {
      "rank": 1,
      "cloud": "aws",
      "region": "us-east-1",
      "machine": "m7i.large",
      "architecture": "x86_64",
      "os": "linux",
      "vcpu": 2,
      "memory_gib": 8,
      "spot_usd_per_hour": "0.031000000",
      "on_demand_usd_per_hour": null,
      "savings_percent": null,
      "risk": {
        "status": "unavailable",
        "kind": null,
        "label": null,
        "min_percent": null,
        "max_percent": null,
        "window_days": null,
        "source_url": null,
        "observed_at": null
      },
      "rationale_codes": ["COST_POLICY", "ARCHITECTURE_MATCH", "RESOURCE_MINIMUMS_MET"]
    }
  ],
  "warnings": []
}
```

Canonical v2 prices are decimal strings with exactly nine fractional digits. Arrays are non-null. Empty results use `recommendations=[]` only for a successful query whose explicit contract permits an empty result; the recommendation tool normally returns `NO_CANDIDATES` instead.

An error result has `isError=true`. Its single text content item contains JSON that validates against `recommend-spot-instances-v2-error.schema.json`. The example below conforms to that schema:

```json
{
  "schema_version": "spotinfo.error/v1",
  "code": "INVALID_ARGUMENT",
  "message": "human-readable stable summary",
  "cloud": "aws"
}
```

Allowed error codes are `INVALID_ARGUMENT`, `UNSUPPORTED_CAPABILITY`, `DATA_UNAVAILABLE`, `NO_CANDIDATES`, and `INTERNAL`. The MCP host rejects a cloud value outside the input enum. If invalid arguments reach the handler, return `INVALID_ARGUMENT` and set `cloud` to the supplied string or `null` when it cannot be parsed. Validate input before provider acquisition. Do not return partial recommendations with an error. Keep provider or source internals out of `message`.

## Provider source approval contract

Task 2 creates `docs/research/multicloud-source-contracts.md`, `internal/snapshot/source_contract.go`, and `docs/plans/contracts/provider-source-contract.schema.json`. Task 5 creates and approves the GCP contract before GCP code, then creates and approves the Azure contract after the GCP commit and before Azure code.

The normative contract is `docs/plans/contracts/provider-source-contract.schema.json`. Each provider artifact contains exact source URLs, terms decision, expected fields, supported OS and architecture values, complete supported region and machine-series arrays, Spot and On-Demand classes, risk status, minimum counts, maximum compressed size, maximum decimal precision, parser version, weekly cadence, and no-go conditions.

`make verify-data` must fail when a provider contract is missing, not approved, outside its coverage thresholds, outside its size limit, or inconsistent with its manifest. If the maintainer cannot approve redistribution or stable extraction, stop the provider task and update the GitHub issue instead of committing data.

## Provider runtime failure contract

- The registry recognizes only `aws`, `gcp`, and `azure`, in stable lexical order.
- Invalid CLI values return `INVALID_ARGUMENT`. MCP values outside the schema enum are rejected by MCP schema validation.
- A recognized provider with a missing, unreadable, hash-mismatched, or invalid embedded snapshot is disabled. Selecting it returns `DATA_UNAVAILABLE`. The process can continue serving other valid providers.
- A disabled provider never falls back to AWS, another cloud, zero prices, stale live data, or a network request.
- AWS keeps its existing provider-internal live-to-embedded fallback. No other provider inherits that behavior.
- A valid provider that lacks the requested OS, risk, score, zone, or live capability returns `UNSUPPORTED_CAPABILITY` before candidate acquisition.
- A successful acquisition with no matching candidates returns `NO_CANDIDATES`.
- Update failure leaves the previous committed snapshot unchanged because writers use a temporary file and atomic replacement.
- `Registry.Available()` returns only enabled providers. `Registry.Status()` returns enabled and disabled providers with stable reason codes for CLI diagnostics and tests.

`internal/providers/registry_test.go`, CLI tests, and MCP tests must cover every branch above. No error returns partial recommendations.

## Implementation Steps

### Task 1: Add the provider-neutral domain seam and AWS compatibility adapter

Justification: #35. Changing `Advice` directly has HIGH impact across three modules. The current AWS query and recommendation paths must remain stable while new providers are added.

Files:

- `internal/cloud/types.go` — add `ProviderID`, `Region`, `MachineID`, `Architecture`, `OperatingSystem`, and `MachineSpec`.
- `internal/cloud/money.go` — add fixed-point USD-per-hour storage and canonical decimal formatting. Use integer nano-USD per hour unless source research proves that more than nine fractional digits are required; fail instead of silently rounding.
- `internal/cloud/observations.go` — add typed price, risk, placement, location, source, observation-time, and history-window records.
- `internal/cloud/provider.go` — add `Query`, `Result`, `Capabilities`, and the smallest consumer-owned provider interface.
- `internal/cloud/dependencies_test.go` — enforce that `internal/cloud` imports no provider SDK, `internal/spot`, CLI, or MCP package; enforce that provider packages import no CLI or MCP package.
- `internal/providers/aws/provider.go` — adapt existing `internal/spot` behavior to neutral candidates.
- `internal/providers/aws/provider_test.go` — prove AWS mapping and optional observations.
- `cmd/spotinfo/testdata/aws-root-v1.json`, `cmd/spotinfo/testdata/aws-recommend-v1.json` — record AWS characterization fixtures.
- `internal/mcp/testdata/find-spot-instances-v1-input-schema.json`, `find-spot-instances-v1-response.json` — record the existing AWS MCP input and response contracts.
- `.archfit.yaml` — declare `domain`, `legacy`, `provider`, `adapter`, and `cmd` layers; declare owner, role, subdomain, and volatility for current and planned modules; retain a one-way dependency from providers and adapters toward the neutral domain.

Preconditions: `make test-verbose`, `make test-race`, `go vet ./...`, `make lint`, and `make build` pass. Existing AWS root, recommendation, and MCP outputs are captured before structural edits.

Postconditions: Neutral consumers can query shared fields without importing AWS SDK types. The AWS adapter maps legacy data without removing `Advice`, `GetSpotSavings`, placement scores, or live price behavior. Provider-neutral money has explicit currency and billing unit. Risk and placement observations retain their source semantics.

Fitness gate: Before this task, archfit reports three under-specified modules and the dependency test does not exist. After this task, module metadata warnings are removed and `internal/cloud/dependencies_test.go` fails on a fixture containing a forbidden import. Keep `no-layer-back-edges` at warning until the migrated graph passes; do not baseline new violations.

Impact commands:

- `gitnexus impact Advice --file internal/spot/types.go --kind Struct --direction upstream --depth 3 --include-tests --repo spotinfo`
- `gitnexus detect-changes --scope compare --base-ref origin/master --repo spotinfo`
- Fallback: `go list -f '{{.ImportPath}}|{{join .Imports ","}}' ./...` and `git diff --name-only origin/master...HEAD`.

Verification commands:

- `go test ./internal/cloud/... ./internal/providers/aws/... ./internal/spot/... ./cmd/spotinfo/...`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `archfit --gate --config .archfit.yaml --base origin/master --format json`

Manual checks:

- Compare representative AWS root JSON, table, CSV, MCP, and recommendation outputs before and after the adapter.
- Confirm that no AWS SDK type appears in `internal/cloud`.
- Review fixed-point precision against the maximum fractional precision observed in AWS, GCP, and Azure fixtures.

- [ ] Record AWS root and recommendation output hashes plus the normalized MCP v1 input and response fixtures.
- [ ] Add neutral domain types with explicit units, currencies, source metadata, risk, and placement observations.
- [ ] Add the AWS adapter without removing legacy AWS types or methods.
- [ ] Add `internal/cloud/dependencies_test.go` and prove its forbidden-import case fails before the implementation passes.
- [ ] Update `.archfit.yaml` module metadata and layer intent.
- [ ] Run focused tests, race tests, vet, and the architecture gate.
- [ ] Commit the #35 slice before starting Task 2.

### Task 2: Add manifest-backed embedded snapshots and parser gates

Justification: #36. The project requires hermetic builds. GCP HTML pages and Azure price rows can change shape, duplicate records, or lose coverage without producing a parser error unless the update gate checks them.

Files:

- `internal/snapshot/manifest.go` — define manifest and schema versions, source URL, fetch time, observation time, raw-source SHA-256, parsed-payload SHA-256, parser version, provider, data kind, currency, billing unit, and record counts.
- `internal/snapshot/validate.go` — add finite positive price, currency, unit, duplicate, coverage, timestamp, and hash validation.
- `internal/snapshot/write.go` — add deterministic temporary-file and atomic-replace behavior for update commands.
- `internal/snapshot/manifest_test.go`, `internal/snapshot/testdata/manifest-valid.json`, `manifest-invalid.json` — test manifest contracts.
- `internal/snapshot/source_contract.go`, `source_contract_test.go`, `testdata/source-contract-approved.json`, `testdata/source-contract-rejected.json` — load and enforce the provider source approval contract.
- `docs/plans/contracts/provider-source-contract.schema.json` — keep the normative machine-readable contract schema.
- `docs/research/multicloud-source-contracts.md` — add the research template and record current source candidates, required decisions, and no-go criteria. Provider-specific approval occurs in Task 5.
- `internal/spot/data.go` — retain `parseAdvisorResponse`, `parsePricingResponse`, `loadEmbeddedAdvisorData`, and `loadEmbeddedPricingData`; add manifest validation around them.
- `internal/spot/feedguard_test.go` — extend the existing AWS parser contract.
- `internal/spot/data/spot-advisor-manifest.json`, `spot-price-manifest.json`, `architecture-manifest.json` — add AWS sidecar manifests.
- `Makefile` — extend `verify-data`; do not make `build` depend on network updates.
- `.github/workflows/update-data.yaml` — verify existing AWS manifests and parser contracts.

Preconditions: Task 1 passes. The provider source-contract JSON Schema is part of the approved plan. Task 2 creates only the generic contract loader, research template, snapshot framework, and AWS manifest migration. It does not create GCP or Azure parser or provider code.

Postconditions: Every existing AWS snapshot has a manifest. The generic manifest and source-contract validators fail closed on missing approval, missing fields, malformed values, empty output, duplicate records, unsupported currency, invalid architecture, or hash mismatch. Builds remain offline. GCP and Azure files remain owned by Task 5.

Fitness gate: `make verify-data` becomes the deterministic data gate. The malformed fixture fails before the parser fix and passes after the expected error is asserted. Archfit remains a package-boundary gate, not a data-correctness substitute.

Impact commands:

- `gitnexus impact parsePricingResponse --file internal/spot/data.go --kind Function --direction upstream --depth 3 --include-tests --repo spotinfo`
- `gitnexus impact parseAdvisorResponse --file internal/spot/data.go --kind Function --direction upstream --depth 3 --include-tests --repo spotinfo`
- `gitnexus detect-changes --scope compare --base-ref origin/master --repo spotinfo`
- Fallback: `rg -n "go:embed|parsePricingResponse|parseAdvisorResponse|verify-data|update-data|update-price" internal Makefile .github/workflows`.

Verification commands:

- `make verify-data`
- `go test ./internal/snapshot/... ./internal/spot/...`
- `go test -race ./internal/...`
- `go vet ./...`
- `make build`

Manual checks:

- Inspect one AWS manifest and one generic provider source-contract fixture.
- Confirm the update path cannot replace a valid snapshot with empty or partial output.
- Confirm that raw source hashes and parsed payload hashes describe different artifacts.

- [ ] Define manifest, payload, parser, and source schema versions.
- [ ] Add AWS sidecar manifests without changing AWS runtime output.
- [ ] Add and validate the provider source-contract schema and generic approved/rejected fixtures.
- [ ] Add atomic output writing and fail-closed validation.
- [ ] Extend `make verify-data` and the AWS update workflow.
- [ ] Run focused data tests, race tests, build, and verification gates.
- [ ] Commit the #36 slice before starting Task 3.

### Task 3: Add the provider registry and capability-aware CLI routing

Justification: #37. The current composition root wires `internal/spot` directly. Provider selection must be explicit and unsupported AWS features must fail before acquisition.

Files:

- `internal/providers/registry.go` — add a compiled provider registry and lookup.
- `internal/providers/registry_test.go` — test duplicate registration, unknown providers, stable provider ordering, and capabilities.
- `cmd/spotinfo/provider_flags.go` — add `--cloud aws|gcp|azure`, neutral `--machine`, AWS alias resolution, and provider-aware flag validation.
- `cmd/spotinfo/provider_flags_test.go` — test default AWS, explicit cloud, flag lineage, invalid cloud, and unsupported capability errors before acquisition.
- `cmd/spotinfo/main.go` — compose the registry and preserve root `--type` and `recommend --instance` behavior.
- `cmd/spotinfo/main_test.go`, `cmd/spotinfo/recommend_test.go` — preserve root and recommendation contracts.
- `internal/mcp/server.go` — accept the registry through a neutral interface without changing registered AWS tool names yet.
- `internal/mcp/server_test.go` — test registry construction and current AWS registration.
- `Makefile` — pin `ARCHFIT_VERSION := v1.6.0` and `GOLANGCI_LINT_VERSION := v2.12.2`; replace the current unpinned golangci-lint install; add the exact `verify-architecture-rules` command from Tool policy.
- `.github/workflows/ci.yaml` — run `go install github.com/alexei-led/archfit/cmd/archfit@v1.6.0`, then `make verify-architecture-rules`. Do not install GitNexus in CI.

Preconditions: Tasks 1 and 2 pass. The supported neutral capability vocabulary is fixed: Spot price, On-Demand price, machine specification, architecture, OS, risk, placement score, zone detail, and live enrichment. GCP and Azure are recognized identifiers but remain unavailable until Task 5.

Postconditions: AWS remains the default. Unknown or not-yet-built providers return clear errors. Unsupported flags fail before data acquisition. The registry has no dynamic plugins and no provider SDK dependency in `internal/cloud`.

Fitness gate: Pin and run archfit rules in CI. Configure layers in this order: `domain`, `legacy`, `provider`, `adapter`, `cmd`. Promote `no-layer-back-edges` from warning to fail only after the current graph and Task 3 graph pass. The gate must fail if a provider imports `cmd/spotinfo` or `internal/mcp`; `internal/cloud/dependencies_test.go` is mandatory. The advisory-severity assertion is intentionally activated in Task 4 after the current critical edge is removed.

Impact commands:

- `gitnexus impact execRecommendCmd --file cmd/spotinfo/main.go --kind Function --direction upstream --depth 3 --include-tests --repo spotinfo`
- `gitnexus impact spotClient --file internal/mcp/server.go --kind Interface --direction upstream --depth 3 --include-tests --repo spotinfo`
- `gitnexus detect-changes --scope compare --base-ref origin/master --repo spotinfo`
- Fallback: `go list -f '{{.ImportPath}}|{{join .Imports ","}}' ./...` and `rg -n "cloud|registry|GetSpotSavings|recommend" cmd internal`.

Verification commands:

- `go test ./cmd/spotinfo/... ./internal/cloud/... ./internal/providers/... ./internal/mcp/...`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `make lint`
- `make build`
- `make verify-architecture-rules`

Manual checks:

- Run the legacy root command with no `--cloud` flag.
- Run `recommend --cloud aws` with flags before and after the subcommand.
- Confirm GCP and Azure reject `--with-score`, `--az`, and `--min-score` before snapshot access.
- Confirm CI uses pinned archfit `v1.6.0`; GitNexus stays a review-only tool.

- [ ] Add the compiled registry and capability vocabulary.
- [ ] Add provider selection with AWS as the default.
- [ ] Preserve root `--type` and recommendation `--instance` behavior.
- [ ] Add unsupported-capability and flag-order tests.
- [ ] Pin archfit and add the provider-boundary gate to CI.
- [ ] Run focused and full validation.
- [ ] Commit the #37 slice before starting Task 4.

### Task 4: Move recommendations and MCP to provider-neutral candidates

Justification: #38. `Recommend` ranks AWS interruption buckets. MCP imports `internal/spot` directly and has critical archfit finding `bac6b2e4f1019c672ac2eec8dc470b31`. Provider code must not start until this coupling is removed.

Files:

- `internal/cloud/recommend.go` — add provider-neutral constraint filtering, cost policy, right-sizing, deterministic ordering, and risk-capability errors.
- `internal/cloud/recommend_test.go` — cover unknown price, exact resource bounds, budget, architecture, region, machine pattern, cost policy, unavailable risk, tie order, and permutation determinism.
- `internal/cloud/schema.go` — define `spotinfo.recommend/v2` request and result DTOs with cloud, machine, region, specification, Spot price, optional On-Demand price, optional savings, risk status, source, observation time, and rationale codes.
- `internal/spot/recommend.go` — keep the AWS `spotinfo.recommend/v1` adapter and existing `web`, `ci`, and `batch` behavior.
- `internal/spot/recommend_test.go` — keep AWS workload bucket and JSON compatibility tests.
- `cmd/spotinfo/recommend.go` — extract provider-neutral command flow from `main.go`; add `cost` workload. AWS default remains `web`; providers without risk default to `cost`; explicit `web`, `ci`, or `batch` fails when risk is unavailable.
- `cmd/spotinfo/recommend_test.go` — test v1 AWS and v2 neutral schemas, defaults, unavailable risk, and output failures.
- `internal/mcp/recommend.go` — add `recommend_spot_instances` with `cloud`, `machine`, `regions`, `architecture`, `os`, minimum vCPU, minimum memory GiB, budget, workload, and top parameters.
- `internal/mcp/recommend_test.go` — add exact request, default, validation-order, success, and error contract tests for AWS plus fake GCP and Azure providers.
- `internal/mcp/tools.go` — keep `find_spot_instances` name and response compatible while routing its AWS data through the neutral adapter.
- `internal/mcp/tools_test.go`, `internal/mcp/testdata/find-spot-instances-v1-input-schema.json`, `find-spot-instances-v1-response.json`, `recommend-spot-instances-v2-input-schema.json`, `recommend-spot-instances-v2-success.json`, `recommend-spot-instances-v2-error.json` — prove normalized v1 compatibility and validate v2 defaults, nullability, success, and errors against the schemas in `docs/plans/contracts/`.
- `internal/mcp/server.go` — remove the direct `internal/spot` import and register the neutral recommendation tool.
- `cmd/archfitcheck/main.go`, `main_test.go`, `testdata/critical.json`, `testdata/medium.json` — parse archfit JSON and fail on any active Critical or High finding.
- `Makefile` — add `verify-architecture` exactly as defined in Tool policy: run `verify-architecture-rules`, write `archfit analyze --json` to a temporary file, and invoke `go run ./cmd/archfitcheck < temporary-file` with shell redirection.
- `.github/workflows/ci.yaml` — switch from rule-only validation to `make verify-architecture` after the current critical edge is fixed.
- `.archfit.yaml` — make the neutral boundary explicit and remove the critical `mcp -> spot` edge.
- `docs/api-reference.md` — publish the exact v1 compatibility schema and v2 input, success, and error schemas from this plan.

Preconditions: Tasks 1–3 pass. No GCP or Azure provider implementation exists. Fake providers prove neutral behavior. AWS v1 CLI and MCP fixtures are unchanged.

Postconditions: Task 4 closes #38 without implementing #28 or #29. AWS v1 behavior remains compatible. The new v2 recommendation path and MCP tool operate against fake neutral providers. The direct `internal/mcp -> internal/spot` import is gone. The cost policy never claims risk awareness.

Fitness gate: Run `archfit explain bac6b2e4f1019c672ac2eec8dc470b31 --config .archfit.yaml` before the change. After the change, the finding must be fixed or absent. Do not baseline or waive it. `cmd/archfitcheck` must fail against the pre-change critical fixture and pass against a fixture with only Medium findings. `make verify-architecture` must fail on any active Critical or High finding and pass before Task 5 starts.

Impact commands:

- `gitnexus impact Recommend --file internal/spot/recommend.go --kind Function --direction upstream --depth 3 --include-tests --repo spotinfo`
- `gitnexus impact execRecommendCmd --file cmd/spotinfo/main.go --kind Function --direction upstream --depth 3 --include-tests --repo spotinfo`
- `gitnexus impact buildResponse --file internal/mcp/tools.go --kind Function --direction upstream --depth 3 --include-tests --repo spotinfo`
- `gitnexus detect-changes --scope compare --base-ref origin/master --repo spotinfo`
- Fallback: `rg -n "Advice|Range|calculateReliabilityScore|Recommend|spotClient|GetSpotSavings" internal cmd --glob '*.go'`.

Verification commands:

- `go test ./internal/cloud/... ./internal/spot/... ./internal/mcp/... ./cmd/spotinfo/...`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `make verify-data`
- `make lint`
- `make build`
- `make verify-architecture`
- `archfit explain bac6b2e4f1019c672ac2eec8dc470b31 --config .archfit.yaml`

Manual checks:

- Compare AWS legacy JSON and MCP output against Task 1 fixtures.
- Inspect the v2 schema and MCP tool description for provider-neutral terminology.
- Confirm that unavailable risk is explicit and that `cost` does not imply interruption safety.
- Confirm that the critical archfit finding is fixed, not hidden by configuration metadata.

- [ ] Freeze the neutral cost-policy and `spotinfo.recommend/v2` contracts.
- [ ] Move neutral recommendation filtering and ordering onto neutral candidates.
- [ ] Preserve AWS recommendation v1 through an adapter and golden tests.
- [ ] Add `recommend_spot_instances` and exact AWS/fake-provider MCP contracts.
- [ ] Route existing AWS MCP response shaping through the neutral adapter without changing the existing tool contract.
- [ ] Remove the direct `internal/mcp -> internal/spot` import.
- [ ] Run focused, full, race, data, lint, build, and architecture gates.
- [ ] Stop if any Critical or High archfit finding remains.
- [ ] Commit the #38 slice before starting Task 5.

### Task 5: Add GCP and Azure providers, then run final verification and documentation

Justification: #28 and #29 depend on completed #35–#38 foundations. Both providers must use anonymous update sources and deterministic embedded runtime data. Implement GCP first. Complete its review and commit before Azure implementation starts. Do not use parallel provider worktrees in this plan.

Files:

- `go.mod`, `go.sum` — add direct `golang.org/x/net v0.57.0` for `html` parsing after the GCP source contract is approved.
- `internal/providers/gcp/parser.go`, `parser_test.go`, `provider.go`, `provider_test.go`, `embed.go` — parse approved GCP tables and query the embedded snapshot.
- `internal/providers/gcp/testdata/spot-pricing.html`, `on-demand-pricing.html`, `machine-resource.html`, `schema-changed.html` — cover source variants and failure paths.
- `internal/providers/gcp/data/source-contract.json`, `catalog.json.gz`, `manifest.json` — add the approved machine-readable source contract and reviewed embedded GCP data.
- `cmd/update-gcp-data/main.go` — fetch declared official pages with context timeout, User-Agent, bounded retry, parser contracts, validation, and atomic output.
- `.github/workflows/update-gcp-data.yaml` — run the updater, verify data, run tests, and open one review PR without credentials.
- `internal/providers/azure/parser.go`, `parser_test.go`, `provider.go`, `provider_test.go`, `embed.go` — parse approved Azure prices and VM-size tables and query the embedded snapshot.
- `internal/providers/azure/testdata/retail-page-1.json`, `retail-page-2.json`, `duplicate-current.json`, `vm-sizes.html`, `schema-changed.json` — cover pagination, Spot detection, effective dates, duplicates, OS, region, and schema failure.
- `internal/providers/azure/data/source-contract.json`, `catalog.json.gz`, `manifest.json` — add the approved machine-readable source contract and reviewed embedded Azure data.
- `cmd/update-azure-data/main.go` — follow `NextPageLink`, select `priceType=Consumption`, match Spot rows, validate effective intervals, and write atomically.
- `.github/workflows/update-azure-data.yaml` — run the updater, verify data, run tests, and open one review PR without credentials.
- `cmd/spotinfo/multicloud_test.go` — add AWS, GCP, and Azure CLI user flows and deterministic JSON.
- `internal/mcp/recommend_test.go` — replace fake GCP/Azure cases with embedded-provider contract cases.
- `Makefile` — add `update-gcp-data`, `update-azure-data`, provider-specific verify targets, and full `verify-data` composition.
- `README.md`, `docs/usage.md`, `docs/api-reference.md`, `docs/mcp-server.md`, `docs/data-sources.md`, `docs/examples.md`, `docs/troubleshooting.md` — document sources, support matrix, commands, freshness, risk status, update procedures, and failure recovery.
- `.archfit.yaml`, `.github/workflows/ci.yaml` — keep provider modules and enforced boundaries current.
- `docs/research/multicloud-source-contracts.md` — record final GCP and Azure source approvals, exact support matrices, thresholds, and no-go outcomes.
- `docs/reviews/spotinfo-multicloud-v2-architecture-review.md` — store the independent final architecture review and finding disposition.
- `docs/plans/spotinfo-multicloud-v2.md` — record results, residual risks, deferred capabilities, and re-review evidence.

Preconditions: Task 4 is committed. `make verify-architecture` passes with no Critical or High finding. The source-contract schema and research record template exist. No provider implementation starts from an unapproved contract.

Task 5 internal gate order: first complete and approve `internal/providers/gcp/data/source-contract.json`; then implement, review, and commit GCP. Only after that commit, complete and approve `internal/providers/azure/data/source-contract.json`; then implement, review, and commit Azure. GCP approval identifies stable server-rendered headers. Azure approval identifies exact filters, pagination, effective-date rules, VM-size pages, and canonical USD-per-hour units.

Postconditions: GCP and Azure work offline for the documented Linux matrix. Both expose Spot and optional paired On-Demand prices, architecture, vCPU, memory, region, source metadata, and explicit unavailable risk. Provider-specific update workflows fail closed. Full documentation and review evidence exist.

Fitness gate: Run the boundary test and archfit delta after GCP, then commit the GCP slice. Repeat after Azure, then commit the Azure slice. A provider must not register when its manifest or coverage gate fails. The final archfit report must contain no open Critical or High finding.

Impact commands:

- `gitnexus analyze --index-only`
- `gitnexus impact Candidate --file internal/cloud/types.go --kind Struct --direction upstream --depth 3 --include-tests --repo spotinfo`
- `gitnexus impact Provider --file internal/providers/gcp/provider.go --kind Struct --direction upstream --depth 3 --include-tests --repo spotinfo`
- `gitnexus impact Provider --file internal/providers/azure/provider.go --kind Struct --direction upstream --depth 3 --include-tests --repo spotinfo`
- `gitnexus detect-changes --scope compare --base-ref origin/master --repo spotinfo`
- Fallback: `git diff --name-status origin/master...HEAD`, `go list -f '{{.ImportPath}}|{{join .Imports ","}}' ./...`, and `git diff --check`.

Verification commands:

- `make update-gcp-data`
- `make update-azure-data`
- `make fmt`
- `make verify-data`
- `go test ./internal/providers/gcp/... ./internal/providers/azure/... ./internal/cloud/... ./internal/mcp/... ./cmd/spotinfo/...`
- `go test ./...`
- `make test-verbose`
- `make test-race`
- `go vet ./...`
- `make lint`
- `make build`
- `git diff --check`
- `make verify-architecture`
- `archfit --gate --config .archfit.yaml --full --format json`
- `archfit --gate --config .archfit.yaml --base origin/master --format json`
- `gitnexus detect-changes --scope compare --base-ref origin/master --repo spotinfo`

Manual checks:

- Review source terms, redistribution, robots guidance, and attribution for each committed snapshot.
- Compare one Spot and one On-Demand price per provider with the current official source.
- Review the first GCP snapshot before Azure implementation continues.
- Review the first Azure snapshot before final verification continues.
- Run one offline CLI and MCP flow per provider.
- Confirm no API key, OAuth token, cloud account, subscription, or runtime network request is required.
- Review every changed public JSON field and schema version.
- Ask an independent reviewer to run a scoped architecture review against #35–#38, #28, and #29 and save it to `docs/reviews/spotinfo-multicloud-v2-architecture-review.md`.

- [ ] Write `docs/research/multicloud-source-contracts.md` and the exact GCP support matrix.
- [ ] Create and approve `internal/providers/gcp/data/source-contract.json`; run its schema and threshold gate; stop before GCP code if it fails.
- [ ] Record GCP source headers, page list, exact Linux machine/region/architecture matrix, terms decision, coverage thresholds, and binary-size delta.
- [ ] Implement and verify the GCP updater, parser, snapshot, provider, CLI flow, MCP flow, docs, and update workflow.
- [ ] Run all GCP-focused gates and commit the #28 slice.
- [ ] After the GCP slice is reviewed and committed, record the exact Azure support matrix.
- [ ] Create and approve `internal/providers/azure/data/source-contract.json`; run its schema and threshold gate; stop before Azure code if it fails.
- [ ] Record Azure filters, page selection, exact Linux machine/region/architecture matrix, terms decision, effective-date rules, coverage thresholds, and binary-size delta.
- [ ] Implement and verify the Azure updater, parser, snapshot, provider, CLI flow, MCP flow, docs, and update workflow.
- [ ] Run all Azure-focused gates and commit the #29 slice.
- [ ] Run the complete repository validation command set.
- [ ] Record GitNexus detect-changes and all HIGH-impact symbol reviews.
- [ ] Record archfit findings and prove that no Critical or High finding remains.
- [ ] Complete user, API, MCP, source, troubleshooting, and support-matrix documentation.
- [ ] Save the independent architecture review to `docs/reviews/spotinfo-multicloud-v2-architecture-review.md`, disposition every finding, and record the release recommendation.

## Acceptance criteria

- Issues #35, #36, #37, #38, #28, and #29 have an implementation result or an explicit deferred reason.
- AWS legacy output and behavior remain compatible.
- GCP and Azure support works offline for the documented Linux matrix.
- Every provider has source, schema, manifest, capability, parser, CLI, MCP, and output tests.
- No cross-provider risk comparison creates false precision.
- No runtime credential or network requirement is introduced for the offline path.
- Snapshot updates fail closed and are reviewed before merge.
- CI enforces unit tests, race tests, vet, lint, build, data verification, and provider-boundary checks.
- Documentation states the exact limits of GCP and Azure risk data.
- Archfit finding `bac6b2e4f1019c672ac2eec8dc470b31` is fixed or absent. It is not baselined or waived.
- `docs/research/multicloud-source-contracts.md` and both approved machine-readable source contracts contain exact supported matrices and thresholds.
- `make verify-architecture` fails on an active Critical or High archfit finding.
- `docs/reviews/spotinfo-multicloud-v2-architecture-review.md` reports no open Critical or High finding.

## Safety notes

- Do not rewrite `Advice` or remove `GetSpotSavings` in one change. Their current blast radius is HIGH.
- Do not start GCP or Azure provider implementation before Task 4 passes and the current critical `mcp -> spot` coupling is fixed.
- Do not add a provider until its source contract, manifest, parser fixtures, capability matrix, and offline behavior exist.
- Do not scrape pricing calculators, Azure Spot Advisor, undocumented JavaScript endpoints, or Vantage data for production snapshots.
- Do not add credentials to builds, update workflows, tests, or embedded snapshots.
- Do not treat GCP preemption history or Azure eviction history as available in offline v2.
- Do not publish a provider that returns zero for unknown price or risk.
- Keep each issue slice in a separate commit. Revert the slice if its gate fails.
- The engineer, mutator agent, or task runner executes this approved plan. The plan author does not modify source code during planning.

## Ralphex handoff

Commit this plan before using an isolated worktree. Then run:

```bash
ralphex --worktree --branch feat/spotinfo-multicloud-v2 --base-ref master docs/plans/spotinfo-multicloud-v2.md
```

Ralphex 1.6.1 has no validate-only command. The plan structure is validated separately before handoff.

## Re-review

After Task 5, run a fresh architecture review against the final diff and the GitHub issue contracts. Re-check the `internal/cloud` boundary, provider imports, snapshot update workflows, AWS compatibility, neutral recommendation policy, MCP schemas, source provenance, data size, and release strategy. Stop release work until all Critical and High findings are closed or explicitly accepted by the project owner.
