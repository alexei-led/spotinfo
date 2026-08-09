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
    "cloud": {
      "type": "string",
      "enum": ["aws", "gcp", "azure"],
      "default": "aws"
    },
    "regions": {
      "type": "array",
      "items": { "type": "string" },
      "default": ["all"]
    },
    "machine": { "type": "string", "default": "" },
    "architecture": { "type": "string", "enum": ["x86_64", "arm64"] },
    "os": {
      "type": "string",
      "enum": ["linux", "windows"],
      "default": "linux"
    },
    "min_vcpu": { "type": "integer", "minimum": 1 },
    "min_memory_gib": { "type": "number", "exclusiveMinimum": 0 },
    "max_price_per_hour": { "type": "number", "exclusiveMinimum": 0 },
    "workload": {
      "type": "string",
      "enum": ["cost", "web", "ci", "batch"],
      "default": "cost"
    },
    "top": { "type": "integer", "minimum": 1, "maximum": 50, "default": 3 }
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
  "ranking_policy": [
    "spot_price_ascending",
    "excess_vcpu_ascending",
    "excess_memory_gib_ascending",
    "region_ascending",
    "machine_ascending"
  ],
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
      "rationale_codes": [
        "COST_POLICY",
        "ARCHITECTURE_MATCH",
        "RESOURCE_MINIMUMS_MET"
      ]
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

- [x] Record AWS root and recommendation output hashes plus the normalized MCP v1 input and response fixtures.
- [x] Add neutral domain types with explicit units, currencies, source metadata, risk, and placement observations.
- [x] Add the AWS adapter without removing legacy AWS types or methods.
- [x] Add `internal/cloud/dependencies_test.go` and prove its forbidden-import case fails before the implementation passes.
- [x] Update `.archfit.yaml` module metadata and layer intent.
- [x] Run focused tests, race tests, vet, and the architecture gate.
- [x] Commit the #35 slice before starting Task 2.

Task 1 results:

- Fixed-point scale confirmed against the committed AWS price feed: 42,886 price
  strings, maximum 4 fractional digits, so nano-USD (9) stores every observed
  amount exactly. GCP and Azure precision is re-checked in Task 5.
- Goldens are the recorded contract: `cmd/spotinfo/testdata/aws-root-v1.json`,
  `aws-recommend-v1.json`, `internal/mcp/testdata/find-spot-instances-v1-input-schema.json`,
  and `find-spot-instances-v1-response.json`. All four come from fixed advice via
  mocked clients, never from the embedded feeds, so a weekly data refresh cannot
  rewrite them. Regenerate with `UPDATE_GOLDEN=1`.
- Archfit after the config change: `verdict pass`, 0 gate findings, `config-quality`
  warnings gone, `no-layer-back-edges` still `warn`. Critical finding
  `bac6b2e4f1019c672ac2eec8dc470b31` is unchanged (`model × cross_module_same_owner`,
  same id, still critical) — Task 4's target was not hidden by metadata. Two new
  medium advisories cover the expected `providers_aws -> cloud` and
  `providers_aws -> spot` edges.
- `owner` is uniform (`spotinfo`) across modules because the repository has one
  maintainer; inventing a team split would have altered Balanced-Coupling distance.

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

- [x] Define manifest, payload, parser, and source schema versions.
- [x] Add AWS sidecar manifests without changing AWS runtime output.
- [x] Add and validate the provider source-contract schema and generic approved/rejected fixtures.
- [x] Add atomic output writing and fail-closed validation.
- [x] Extend `make verify-data` and the AWS update workflow.
- [x] Run focused data tests, race tests, build, and verification gates.
- [x] Commit the #36 slice before starting Task 3.

Task 2 results:

- Hash model: `payload.sha256` is the hash of the **committed** file, so
  `make verify-data` can recompute it offline. `sources[].sha256` is upstream
  provenance. `payload.form` names the relationship: `raw-source` (AWS embeds
  the feed verbatim — the two hashes must be equal) or `parsed-catalog` (GCP and
  Azure commit a derived catalogue — the payload hash must differ from every
  source hash). No canonicalizer was written; a parse-derived hash would have
  forced a manifest rewrite on every weekly refresh for no extra signal.
- `min_records` is a **floor**, not a census. A refresh may grow the data; a
  shrink below the reviewed floor fails. That is what keeps a weekly refresh
  from rewriting review metadata, and it matches the `min_regions`/`min_machines`
  thresholds in the source-contract schema. AWS floors are ~75-80% of observed:
  price 30 regions / 1000 machines / 25000 priced cells (observed 40 / 1339 /
  36082), advisor 25 / 900 (observed 34 / 1192), architecture 140 families
  (observed 170).
- The coverage floor is also applied to the **live** AWS fetch. A feed that
  returns HTTP 200 with a truncated document now falls back to the embedded
  snapshot instead of replacing it. Embedded loads are not re-hashed at runtime:
  the bytes are compiled in, so that is a gate fact, not a runtime one.
- No JSON Schema library was added. `internal/snapshot/source_contract.go`
  mirrors `docs/plans/contracts/provider-source-contract.schema.json` in Go and
  names it as normative; the binary keeps its zero-dependency scratch-image
  runtime. Both fixtures are synthetic — the schema restricts `provider` to
  `gcp` and `azure`, and neither is approved here.
- Regeneration reuses the repo's `UPDATE_GOLDEN=1` convention:
  `make refresh-manifests`, invoked automatically by `update-data` and
  `update-price`. It rewrites hashes and, for raw feeds, the fetch time. Floors
  and reviewed provenance are never regenerated. The update workflow runs it a
  second time and fails on any diff, so a stale manifest cannot be merged.
- Archfit after the change: `verdict pass`, no new Critical. The four new medium
  advisories are the expected value-object edges (`snapshot -> cloud` ×2,
  `spot -> cloud`, `spot -> snapshot`). Critical
  `bac6b2e4f1019c672ac2eec8dc470b31` (`mcp -> spot`) is unchanged and remains
  Task 4's target.
- `internal/snapshot` is declared in the `domain` layer: both `internal/spot`
  (legacy) and future provider packages depend on it, so any other placement
  would create a back edge.
- Duplicate rule, corrected against real data: the committed AWS price feed
  lists 118 region/size pairs twice (8 GPU machine types) with **identical**
  prices. That is redundancy, not ambiguity, so `ValidatePrices` rejects only a
  key priced two _different_ ways and counts distinct keys toward the floor.
  Rejecting exact repeats would have made the gate unusable on real AWS data
  without catching anything the success criteria ask for.
  `TestEmbeddedPricesSatisfyTheNeutralRecordContract` runs the neutral record
  validator over the whole embedded feed, so `ValidatePrices` has a real
  consumer now rather than waiting for Task 5.
- `.golangci.yaml` depguard trap fixed. depguard prefix-checks only the nearest
  allow entry in sorted order, so `spotinfo/internal/spot` sitting alongside
  `spotinfo/internal` silently denied every package sorting between them —
  `internal/snapshot` here, and `internal/providers` in Task 3. The narrower
  entries are removed; a single `spotinfo/internal` now covers all of them.

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

- [x] Add the compiled registry and capability vocabulary.
- [x] Add provider selection with AWS as the default.
- [x] Preserve root `--type` and recommendation `--instance` behavior.
- [x] Add unsupported-capability and flag-order tests.
- [x] Pin archfit and add the provider-boundary gate to CI.
- [x] Run focused and full validation.
- [x] Commit the #37 slice before starting Task 4.

Task 3 results:

- `--cloud` is declared on both the root command and `recommend` with **no**
  default value, and the AWS default is applied once in `providerID`. Declaring
  `Value: "aws"` on both would let the subcommand's own default shadow an
  explicit `spotinfo --cloud gcp recommend …` and answer a GCP question with AWS
  prices. `TestCloudFlagResolvesAcrossContextLineage` pins both flag positions.
- `--machine` is the neutral machine-type filter, added as a `cli` **alias** of
  the AWS spellings (`--type` on root, `--instance` on recommend). Both names
  reach the same value, so every documented AWS invocation is byte-identical;
  only the help text gains the alias.
- Fixed validation order, each step before acquisition: unparseable `--cloud` →
  `INVALID_ARGUMENT`; recognised but disabled provider → `DATA_UNAVAILABLE`
  carrying the registry reason code; declared-capability shortfall →
  `UNSUPPORTED_CAPABILITY`. In the shipped binary `--cloud gcp` therefore reports
  `DATA_UNAVAILABLE (PROVIDER_NOT_REGISTERED)` — resolution precedes the
  capability check — so the capability branches are proven against enabled stub
  providers instead. Both reject before any snapshot access.
- The root command requires `risk` because it renders an interruption column, so
  a risk-free provider fails the ordinary capability gate with no special-casing;
  `--with-score`/`--min-score` add `placement_score` and `--az` adds
  `zone_detail`. Recommend requires `risk` too: every v1 workload is an
  interruption cap. The risk-free `cost` policy arrives with the v2 schema.
- An OS outside the neutral vocabulary is deliberately **not** a capability
  question — the provider owns that error. That keeps the existing AWS
  `invalid instance OS` message and its mock-backed test intact; only a
  well-formed OS is checked against declared support.
- `resolveAWSProvider` rejects any non-AWS provider from the legacy acquisition
  path. Both commands still read candidates through the AWS client, so without it
  a hypothetical enabled provider would be answered with AWS data. Task 4 removes
  the second half of that function, not the first.
- Registry builds every factory eagerly, so `Status()` can report a broken
  snapshot before the first query. It returns all three recognised providers in
  stable lexical order; construction fails only on wiring bugs (unknown id,
  duplicate, nil factory, identifier mismatch), never on a provider's own data.
- `no-layer-back-edges` promoted `warn` → `fail`. Verified both ways: with a
  temporary `internal/providers` → `internal/mcp` import the gate returned
  `verdict fail, gate_findings 1, no-layer-back-edges providers -> mcp`; with the
  probe removed, `verdict pass, gate_findings 0`. The archfit module for the
  registry uses the `internal/providers/*.go` glob so it does not swallow
  `providers_aws`; both modules appear with their own edges. 15 medium
  Balanced-Coupling advisories, up from 10 — the new ones are the expected
  `cmd_spotinfo -> cloud|providers_aws`, `mcp -> cloud`, `providers -> cloud`
  edges. Critical `bac6b2e4f1019c672ac2eec8dc470b31` is unchanged and remains
  Task 4's target.
- Error vocabulary added to `internal/cloud`: `ErrUnsupportedCapability`,
  `ErrDataUnavailable`, and `CodeOf`. `NO_CANDIDATES` is deliberately not here —
  nothing in Task 3 returns it, and it belongs with the v2 contract Task 4
  freezes.
- Two test-harness fixes were needed, both pre-existing hazards this task
  exposed. `TestMainCmd_Integration` built its context with
  `cli.NewContext(app, nil, nil)`, which carries no parsed flag set: its
  `ctx.Set` calls silently failed and `LocalFlagNames` panics on it. It now runs
  through `app.Run`. CLI tests that run the app are not `t.Parallel()` —
  urfave/cli appends its package-level `HelpFlag` to every parsed command and
  writes to it in `Apply`, so concurrent `app.Run` calls race inside the library.
- `make lint` now installs pinned `golangci-lint v2.12.2` instead of `latest`,
  and CI installs pinned archfit `v1.6.0` before `make verify-architecture-rules`.
  GitNexus stays a review-only tool and is not installed in CI.
- `Registry.Status()` has a real consumer: `newProviderRegistry` logs every
  disabled provider and its reason code at debug level, so `spotinfo --debug`
  explains a missing cloud without a failing request. The recorded goldens are
  unchanged — none of them capture help text — so the new `--cloud` and
  `--machine` entries in `--help` are gated by nothing. Documenting both flags is
  deferred to Task 5, which already owns `README.md` and `docs/usage.md`.
- Accepted duplication until Task 4: `execRecommendCmd` parses the 3.9 KB
  architecture snapshot twice per run — once inside the registry factory, once
  for the v1 recommender. Threading the lookup through the command signature now
  would be undone when neutral acquisition replaces that call site.

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

- [x] Freeze the neutral cost-policy and `spotinfo.recommend/v2` contracts.
- [x] Move neutral recommendation filtering and ordering onto neutral candidates.
- [x] Preserve AWS recommendation v1 through an adapter and golden tests.
- [x] Add `recommend_spot_instances` and exact AWS/fake-provider MCP contracts.
- [x] Route existing AWS MCP response shaping through the neutral adapter without changing the existing tool contract.
- [x] Remove the direct `internal/mcp -> internal/spot` import.
- [x] Run focused, full, race, data, lint, build, and architecture gates.
- [x] Stop if any Critical or High archfit finding remains.
- [x] Commit the #38 slice before starting Task 5.

Task 4 results:

- Critical finding `bac6b2e4f1019c672ac2eec8dc470b31` is **fixed, not waived**: the
  `internal/mcp -> internal/spot` edge no longer exists in production code, so
  archfit reports neither the critical `mcp -> spot` advisory from `server.go`
  nor the medium one from `tools.go`. Findings dropped 15 → 13, all medium;
  coupling score 36 → 44; `critical-band edges: 0`.
- `find_spot_instances` was **not** routed through `cloud.Provider.Query`
  unchanged. `cloud.Query` carried none of the acquisition knobs that tool
  advertises, so routing through it as it stood would have silently made
  `with_score`, `min_score`, `az`, `score_timeout` and `sort_by` no-ops — and the
  recorded golden would still have passed, because it injects a `RegionScore`
  into fixed advice rather than asking for one. `cloud.Query` therefore gained
  `Sort SortOrder` and `Placement PlacementRequest`, both already part of the
  declared capability vocabulary, and
  `TestToolParametersReachTheProviderQuery` asserts every parameter lands on the
  query. Sorting still happens inside `internal/spot`; the adapter preserves the
  provider's order, which `Result` now documents.
- v1 compatibility is byte-identical against both goldens. The neutral domain
  models an unknown price, an unpublished savings figure and an unlabelled risk
  range as absences; `find_spot_instances` maps them back to the zeros v1
  published, because it is the compatibility surface. `data_source` keeps its own
  vocabulary (`aws`/`embedded`) mapped from `DataMode`.
- Risk kinds are mapped to the wire enum through a table in `internal/cloud/schema.go`,
  not by renaming the domain constants. The success schema freezes
  `interruption_bucket|preemption_rate|eviction_rate`;
  `TestEveryDeclaredRiskKindHasAPublishedName` scans the package AST for every
  `RiskKind` constant, so a kind added in Task 5 fails there instead of shipping
  a value outside the enum. An unmapped kind fails closed as `INTERNAL`.
- `data_source.sources` requires at least one complete entry, so the AWS provider
  now reads `spot.EmbeddedSourceRefs()` in `New` and a build that cannot describe
  its provenance is disabled by the registry. A documentation source may have no stable upstream hash, so
  `Manifest.SourceRefs()` preserves that absence. The derived payload hash is
  used only for manifest verification, never as upstream provenance. A live answer
  still reports the committed manifests: they are the provenance of the data the
  parser was written against.
- CLI schema routing: AWS under `web`, `ci` or `batch` keeps `spotinfo.recommend/v1`
  and its legacy acquisition path. Every other combination — another cloud, or
  the risk-free `cost` policy on any cloud including AWS — is answered by the
  neutral engine and `spotinfo.recommend/v2`. `--workload` lost its `Value:` for
  the same reason `--cloud` did in Task 3: the default depends on the provider
  (`web` where risk is published, `cost` where it is not), so "unset" has to stay
  distinguishable from "set to web".
- Task 3's `TestRecommendRejectsAProviderWithoutRisk` encoded the behaviour this
  task changes and was replaced: a risk-free provider now defaults to `cost` and
  is served, reporting `NO_CANDIDATES` for an empty result rather than
  `UNSUPPORTED_CAPABILITY`. Asking for a capped workload explicitly still fails
  before acquisition.
- The ranker re-applies every constraint over neutral candidates rather than
  trusting the provider, and `compareScored` is a total order over
  (price nanos, excess vCPU, excess memory, region, machine), so
  `TestRankingIsIndependentOfCandidateOrder` runs all 24 permutations of a
  four-candidate set and gets one answer. A risk-aware workload drops a candidate
  whose risk is unknown instead of ranking it as if it had cleared the cap.
- No JSON Schema library was added, keeping the zero-dependency runtime. The
  contracts in `docs/plans/contracts/` are enforced by a ~130-line checker in
  `internal/mcp/jsonschema_test.go` covering exactly the keywords they use. Its
  ceiling is documented there: reach for a real validator if the contracts start
  using `$ref`, `allOf`, or conditionals.
- The advisor feed publishes an unvalidated savings integer, and the v2 schema
  bounds `savings_percent` to 0..100. The AWS adapter now treats a figure above
  100 as unpublished, exactly as it already treated `<= 0`, and
  `recommendationDTO` fails closed on an out-of-range value so a Task 5 provider
  that derives savings rather than reading it cannot publish a payload the
  schema rejects.
- `find_spot_instances` and `list_spot_regions` are AWS-only by contract:
  `queryAWS` resolves `cloud.ProviderAWS` and nothing else, which is why
  `Query.CapabilityNeeds()` demanding risk only for a risk-ordered query is safe
  here — the tools render an interruption column and AWS always publishes one.
  `queryAWS` is not a generic helper; a second cloud on those tools would need
  the capability request widened first.
- `make verify-architecture` was checked on both paths: it passes on the current
  tree, and a failing analyze step aborts the target instead of feeding an empty
  document to the checker (verified against a simulated failure, since archfit
  itself does not fail here).
- `internal/mcp/mocks_test.go` was deleted and the `spotinfo/internal/mcp` entry
  removed from `.mockery.yaml`: the mocked `spotClient` interface is gone, and the
  remaining seam is a two-method registry where a programmable stub reads better
  than a generated mock.
- `internal/mcp` test helpers still build the real embedded AWS client — that is
  the point of the race and bench tests, which exercise `spot.Client`'s
  `sync.Once` and shared providers. The new `mcp` import policy in
  `dependencies_test.go` is therefore `productionOnly`; archfit gates the shipped
  boundary, and the policy walker now keys imports by file rather than by
  directory so a policy can apply to production code without exempting a package.
- `make verify-architecture` runs the rule gate, then writes
  `archfit analyze --json` to a temporary file and feeds it to
  `go run ./cmd/archfitcheck`. A pipeline would report only the last command's
  status and hand the checker an empty document on an archfit failure. The
  checker treats only `fixed`, `resolved`, `waived` and `baselined` as closed —
  any status it has not seen counts as open, so a new archfit status cannot hide
  a Critical finding. CI runs `make verify-architecture` in place of the
  rule-only target.

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

- [x] Write `docs/research/multicloud-source-contracts.md` and the exact GCP support matrix.
- [x] Create and approve `internal/providers/gcp/data/source-contract.json`; run its schema and threshold gate; stop before GCP code if it fails.
- [x] Record GCP source headers, page list, exact Linux machine/region/architecture matrix, terms decision, coverage thresholds, and binary-size delta.
- [x] Implement and verify the GCP updater, parser, snapshot, provider, CLI flow, MCP flow, docs, and update workflow.
- [x] Run all GCP-focused gates and commit the #28 slice.

GCP slice result (#28), recorded for the Azure slice and the final review:

- **Region coverage is `us-central1` only.** The official pages server-render one region and
  switch the rest in with JavaScript. The plan assumed a broader matrix; it is a narrower
  claim, not a no-go. The parser attributes every table to the region selector rendered above
  it, so a table Google renders for another region — the Spot page has one, for
  `africa-south1` — is skipped rather than relabelled.
- **Source list changed.** `https://cloud.google.com/compute/vm-instance-pricing` now
  redirects to a landing page with no tables. On-Demand prices come from the
  `products/compute/pricing/{general-purpose,compute-optimized,memory-optimized}` category
  pages. `storage-optimized` is not read: no machine it prices is offered as Spot.
- **`machine-resource.html` fixture not created.** Architecture is a reviewed series list in
  the parser (`c4a`, `n4a`, `t2a` are Arm), with the documentation page recorded as its source
  in the manifest — the same posture as the AWS architecture snapshot. There is no page to
  parse, so there is no fixture for one.
- **`internal/mcp/recommend_test.go` was left alone.** Its fake providers cover the error
  matrix without pinning MCP tests to committed prices, and importing a concrete provider into
  `internal/mcp` would add a layer edge the boundary gate exists to prevent. The embedded-GCP
  MCP contract test lives in `cmd/spotinfo/multicloud_test.go`, which already composes the
  real registry.
- **The root query command stays AWS-only.** It renders an interruption column, so it requires
  the risk capability, which GCP does not have; `spotinfo --cloud gcp --type …` returns
  `UNSUPPORTED_CAPABILITY`. GCP is served by `recommend` and by `recommend_spot_instances`.
- **`--region` default.** The declared default `us-east-1` is an AWS region name, so on a
  non-AWS cloud an unset `--region` now means every region that cloud publishes. An explicit
  `--region` is always honoured.
- **Numbers.** 333 machines, 18 series, 666 prices, 5,815 compressed bytes, maximum 9
  fractional digits (exactly `cloud.MoneyScale` — no headroom). Binary-size delta +127,056
  bytes.
- **The coverage floor already earned its keep.** One refresh received a partially rendered
  page missing every `n4d` table; the run stopped at 295 machines and wrote nothing.
- **Open maintainer item.** `terms.redistribution_decision` is recorded as `approved` with
  evidence `https://developers.google.com/site-policies`. The pricing pages carry no Creative
  Commons footer, so the decision rests on the figures being facts. The project owner must
  confirm it before release.
- [x] After the GCP slice is reviewed and committed, record the exact Azure support matrix.
- [x] Create and approve `internal/providers/azure/data/source-contract.json`; run its schema and threshold gate; stop before Azure code if it fails.
- [x] Record Azure filters, page selection, exact Linux machine/region/architecture matrix, terms decision, effective-date rules, coverage thresholds, and binary-size delta.
- [x] Implement and verify the Azure updater, parser, snapshot, provider, CLI flow, MCP flow, docs, and update workflow.
- [x] Run all Azure-focused gates and commit the #29 slice.
- [x] Run the complete repository validation command set.
- [x] Record GitNexus detect-changes and all HIGH-impact symbol reviews.
- [x] Record archfit findings and prove that no Critical or High finding remains.
- [x] Complete user, API, MCP, source, troubleshooting, and support-matrix documentation.
- [x] Save the independent architecture review to `docs/reviews/spotinfo-multicloud-v2-architecture-review.md`, disposition every finding, and record the release recommendation.

Azure slice result (#29), and final verification:

- **Two sources, because neither answers the whole question.** The Retail Prices API publishes
  no vCPU count, memory figure or architecture, so those come from one Microsoft Learn size
  page per approved series — 27 sources in the contract. Architecture is read from each page's
  own `[x86-64]`/`[Arm64]` marker and never inferred from a size name.
- **Three source quirks, each found by a gate rather than by reading docs.**
  1. `Low Priority` is the retired Batch meter, priced beside Spot under the same size. Excluded.
  2. Legacy **Cloud Services** meters are published under `serviceName = "Virtual Machines"`
     against the same `armSkuName` at a different rate. They made ~40 sizes per region
     ambiguous and failed the refresh. The second spelling — `Eadsv5 Series CloudServices`,
     no space — survived a first fix that matched only `Cloud Services`, so the marker is
     matched with spaces removed.
  3. Memory is labelled `Memory (GiB)` on 18 pages and `Memory (GB)` on the 8 memory-optimized
     pages, for identical figures (`Standard_E2_v5` reads 16 on one, `Standard_E2s_v5` reads 16
     on the other). Both accepted as gibibytes; a third label fails.
- **Effective dates are resolved against an instant passed in, not `time.Now()`.** The API
  returns expired and future intervals — 289 meters in `eastus` carry more than one row — so a
  parser that took the first or last row would publish an expired price and pass every other
  gate. All eight regions resolve against one instant per run. No interval in effect drops the
  machine and reports it; two intervals in effect at different prices fails the refresh.
  Resolution runs *after* the reviewed-matrix filter, so a conflict in a size this catalogue
  never publishes is not a reason to fail a weekly refresh.
- **The coverage floor is per region**, not global: 180 machines minimum against 224 observed
  in every region. A global count would let one region return three sizes and be absorbed by
  seven healthy ones.
- **Numbers.** 224 sizes, 26 series (37 arm64 sizes across `bpsv2`, `dpdsv5`, `dpsv5`, `dpsv6`,
  `epsv5`), 8 regions, 3,584 prices, 16,515 compressed bytes, max 6 fractional digits against a
  `MoneyScale` of 9 — three digits of headroom, unlike GCP's zero. Binary-size delta
  **+119,536 bytes** (0.20%); `golang.org/x/net/html` was already a dependency.
- **Region coverage is a reviewed choice, not a source limit** — unlike GCP. The API serves
  every Azure region; eight bound the weekly refresh to ~100 requests. Widening is a contract
  edit plus a refresh, no code change.
- **Cross-checked against the live source at commit time**: `uksouth/Standard_D2as_v6` spot
  `0.014013` and on-demand `0.106`, `westeurope/Standard_D2ps_v5` spot `0.017002`. Exact matches.
- Two changes outside Azure. `Money.FractionalDigits` replaced a copy that would have existed in
  each provider — it is a property of the value object, not of a catalogue. And the neutral
  recommendation table now sizes its region and machine columns from the rows: fixed widths were
  fine for `n2d-standard-2` and broke on `Standard_D2pds_v5`.
- Two gaps closed after a review pass. `sizeRows` skipped an unparseable size name with a
  silent `continue`, so a constrained-vCPU row such as `Standard_E32-8as_v5` would have shrunk
  the catalogue without a word — the one behaviour the rest of this design rules out. A row
  starting with `Standard_` is now a size row, and a name the parser cannot read fails. No
  contracted page lists one today (0 of 225 rows), and re-running the updater with the stricter
  parser produced a byte-identical payload. `make verify-data` also now runs both updater test
  packages, which is where the contract checks live — notably that every approved series has a
  source page.
- The three HTML helpers (`walk`, `cellText`, `tableRows`) are deliberately duplicated between
  the GCP and Azure parsers, with the trigger recorded in `sizes.go`: extract them when a third
  provider parses HTML. A package created to hold forty lines would add a dependency edge for
  less than it removes.

Final verification:

- Gates: `make fmt`, `go build ./...`, `go test ./...`, `make test-verbose`, `make test-race`,
  `go vet ./...`, `make lint` (0 issues), `make verify-data`, `make build`, `git diff --check`,
  `make verify-architecture`, and `archfit --gate --base origin/master` all pass.
- **Archfit: `verdict pass`, 0 gate findings, 29 findings, all medium `bc/imbalanced_coupling`.**
  Critical `bac6b2e4f1019c672ac2eec8dc470b31` is **absent** — fixed in Task 4, not baselined or
  waived. All 29 advisories are modules depending on `internal/cloud` or `internal/snapshot`, or
  a composition root depending on what it composes; dispositioned as accepted in the review.
  Metrics: 0 cycles, 0 new high-risk unbalanced edges, 100% coverage, 2 change-impact hubs
  (`internal/cloud` 91%, `internal/snapshot` 64%) — both stable contract modules.
- **GitNexus re-index: 3,519 nodes, 10,129 edges, 291 flows** (from 1,706 / 3,894 / 83). The
  plan's headline risk was that changing `Advice` touched 22 symbols across three modules
  including `internal/mcp`; it now touches 19 across two, and `internal/mcp` has left the set.
  `spot.Recommend` 11, `cloud.Recommend` 15 (`Spotinfo` + `Mcp`, a forward edge),
  `providers.Registry` 31, `gcp.Provider` 12, `azure.Provider` 9 — each provider's radius is
  itself plus the composition root, so a fourth cloud is additive.
- `CLAUDE.md` was rewritten for the post-Task-1 architecture: neutral seam, snapshot contracts,
  provider registry, the two new updaters and workflows, the neutral `cloud.Provider` interface,
  and four new "never" rules drawn from what actually went wrong in this task.
- Review saved to `docs/reviews/spotinfo-multicloud-v2-architecture-review.md`: no open Critical
  or High, ship recommendation conditional on the redistribution approvals below.

**Project owner approval recorded.** The project owner confirmed both redistribution decisions
for `internal/providers/{gcp,azure}/data/source-contract.json`. The reasoning is recorded in
`docs/research/multicloud-source-contracts.md`: factual figures with attribution, and Microsoft
Learn content under CC BY 4.0. Reconfirm each decision before a schema change or source expansion.

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
