# Multi-cloud parity, one vocabulary, and the capabilities the vendors actually publish

## Overview

spotinfo answers well for AWS and partially for GCP and Azure. The gap is not only missing
data. The same question asked two ways returns two different documents, ten of twenty-seven
flag names repeat a concept another name already expresses, and six flags are accepted and
silently ignored on some clouds.

This plan does four things, in this order:

1. **Collapses the surface to one vocabulary.** One name per concept, one schema family, two
   commands that both work on every cloud. Mechanical CLI-to-MCP name derivation, so the six
   naming drifts cannot recur.
2. **Fixes two latent defects in the Azure meter filter** before anything widens the
   catalogue key.
3. **Adds every capability reachable without a new credential type**: Windows on Azure, live
   Azure prices, GCP obtainability, and GCP regions beyond `us-central1`.
4. **Validates the shipped binary on all three clouds** (Phase 10). Tasks 1 to 16 prove the
   packages are right against mocks, stubs and goldens; nothing in them proves the binary a
   user downloads returns a _correct_ answer. Task 17 automates the command x cloud x format
   matrix, and Task 18 is the human judgement — vendor spot-checks, help text, error wording —
   that no assertion makes.

Three things stay refused, because no vendor publishes them: Windows on GCP, zone-level
_prices_ on either cloud, and risk-capped workloads (`web`/`ci`/`batch`) on either cloud.

**Two Azure capabilities are deferred, by decision.** The eviction rate (Azure Resource
Graph) and the Spot Placement Score both need an Azure **subscription**, and reaching them
needs `azidentity` for the credential chain. Measured cost of that dependency alone:

| Build                                | Bytes     |
| ------------------------------------ | --------- |
| `net/http` + `encoding/json`         | 3,615,410 |
| + `azidentity`                       | 8,443,266 |
| + `armresourcegraph`, client invoked | 8,579,298 |

`azidentity` is **+4.83 MB, or +11.7% of the shipped binary**; the ARM SDK on top of it is
+136 KB. The maintainer has no Azure subscription, so neither feature could be exercised or
tested even once. Both stay documented as buildable in
`docs/reviews/multicloud-parity.md` and out of this plan. Everything below needs **no Azure
credentials at all** — the Retail Prices API is anonymous, and GCP reuses the ADC machinery
`--live-risk` already ships.

Do not add `azidentity`, `armresourcegraph` or `armrecommender` while executing this plan.
Reopening that decision needs a subscription to test against.

**This is a major version.** Backward compatibility is explicitly waived by the maintainer.
`spotinfo.recommend/v1` is retired, the query command is renamed, and every golden file is
regenerated once, deliberately, as a design artifact.

## Source artifacts

- `docs/reviews/cli-and-mcp-surface-review.md` — the measured surface inconsistencies
- `docs/reviews/multicloud-parity.md` — per-capability verdicts, with vendor citations
- `docs/research/multicloud-source-contracts.md` — why each source was approved or excluded

## Context (from discovery)

- **Language and layout**: Go 1.26. `internal/cloud` is the provider-neutral domain,
  `internal/providers/{aws,gcp,azure}` adapt one cloud each, `cmd/spotinfo` composes,
  `internal/mcp` serves MCP, `internal/spot` is the legacy AWS path.
- **The query command does not use the neutral seam.** `cmd/spotinfo/provider_flags.go:116`
  records this deliberately: it holds the legacy client and never queries a `cloud.Provider`.
  Roughly 500 lines of `main.go` render `spot.Advice`, not `cloud.Candidate`. Moving it is a
  migration, not a rename — Tasks 3 and 4 are split for that reason.
- **`--mcp` is dispatched from the root Action**, `cmd/spotinfo/main.go:164`. Any change to
  what bare `spotinfo` does must preserve that branch.
- **Gates that stay binding**: `make test`, `make lint`, `make verify-data`,
  `make verify-architecture`. Backcompat is waived; the data gates are not.
- **Regression net**: `cmd/spotinfo/e2e_test.go` becomes the primary safety gate once the v1
  goldens are retired.

## Invariants

An executing agent must not change these. They are the reason the tool can be trusted across
clouds.

1. **`interruptionCappableKinds` stays exactly `[RiskKindInterruptionFrequencyRange]`.**
   This plan lands two placement kinds. **Neither joins that list**, and neither would
   `eviction_rate` if it is ever built. AWS measures the fraction of _running_ instances
   interrupted over 30 days. Azure publishes a per-hour eviction probability from 7 days of
   history. A placement score measures _provisioning success_, not interruption at all. The
   `web`/`ci`/`batch` ceilings are AWS Advisor bucket boundaries and transfer to none of them.
2. **A cloud that does not publish a figure reports its absence.** Never a zero, never a low
   bucket, never a substituted value from another cloud.
3. **A capability check happens before acquisition.** An unanswerable request is refused
   without reading a price.
4. **A task that changes a parser bumps `parser_version` in both the parser and the source
   contract, in the same task, and ends with `make verify-data`.** Never lower a threshold.
   The Azure contract carries `max_compressed_bytes` and `max_fractional_digits` beside the
   coverage floor; all of them bind.
5. **No source enters a snapshot unless the source contract names it.** Authenticated and
   unclear-licence sources stay on opt-in runtime paths, the way `--live-risk` already works.
6. **Builds stay hermetic.** No target that builds or tests may fetch.
7. **`content_sha256` provenance stays verifiable.** Trimming the published source list may
   never drop the provenance for a value a candidate actually carries.
8. **No Azure credential dependency.** Nothing in this plan authenticates to Azure. Adding
   `azidentity` costs +4.83 MB and unlocks only the two deferred features, neither of which
   can be tested without a subscription. A task reaching for it has left the plan.

## The vocabulary

This table is the single source. Every task below references it rather than re-deciding.

**Derivation rule, MCP argument from CLI flag:** strip the leading `--`, replace `-` with `_`.
One exception: a repeatable CLI flag becomes a plural JSON array (`--region` → `regions`).

| Concept             | CLI flag                       | MCP argument           | Commands  |
| ------------------- | ------------------------------ | ---------------------- | --------- |
| Cloud               | `--cloud`                      | `cloud`                | both      |
| Region              | `--region` (repeatable, `all`) | `regions`              | both      |
| Machine-name filter | `--machine` (RE2)              | `machine`              | both      |
| Architecture        | `--architecture`               | `architecture`         | both      |
| Operating system    | `--os`                         | `os`                   | both      |
| Minimum vCPU        | `--min-vcpu`                   | `min_vcpu`             | both      |
| Minimum memory      | `--min-memory-gib`             | `min_memory_gib`       | both      |
| Price ceiling       | `--max-price`                  | `max_price`            | both      |
| Workload policy     | `--workload`                   | `workload`             | recommend |
| Result count        | `--top`                        | `top`                  | recommend |
| Sort key            | `--sort`                       | `sort`                 | both      |
| Sort order          | `--order`                      | `order`                | both      |
| Output format       | `--output`                     | — (MCP is always JSON) | both      |
| Snapshot only       | `--offline`                    | `offline`              | both      |
| Ignore cache        | `--refresh`                    | `refresh`              | both      |
| Placement scores    | `--with-score`                 | `with_score`           | both      |
| Minimum score       | `--min-score`                  | `min_score`            | both      |
| Zone detail         | `--az`                         | `az`                   | both      |
| Score timeout       | `--score-timeout`              | `score_timeout`        | both      |
| Live risk           | `--live-risk`                  | `live_risk`            | recommend |
| GCP project         | `--gcp-project`                | — (env only on MCP)    | both      |
| GCP billing key     | `--gcp-billing-key`            | — (env only on MCP)    | both      |

**Removed names**, each replaced by exactly one above: `--type`, `--instance`, `--vcpu`,
`--memory`, `--memory-gib`, `--cpu`, `--price`, `--budget`. A removed name produces a rename
hint, not `flag provided but not defined`.

**Why `--cpu` and `--memory` are renamed, against the advice in the surface review §9.** That
review said the aliases already work and the churn buys nothing. It was written before the
derivation rule existed. With the rule, `--cpu` produces the MCP argument `cpu`, which loses
both the "minimum" and the unit, and `--memory` produces `memory`, which loses the unit
entirely. Renaming to `--min-vcpu` and `--min-memory-gib` is what makes acceptance criterion 3
mechanical instead of a table of exceptions. This override is deliberate; do not re-open it.

**Unified defaults.** The CLI and MCP defaults are the same value on both surfaces:

| Flag         | Commands       | Default, everywhere                                                                                   |
| ------------ | -------------- | ----------------------------------------------------------------------------------------------------- |
| `--workload` | recommend only | `cost` — the only value every cloud accepts, and it makes no interruption claim                       |
| `--region`   | both           | `all` — cross-region comparison is the tool's value, and it is already the GCP, Azure and MCP default |
| `--top`      | recommend only | 3                                                                                                     |
| `--os`       | both           | `linux`                                                                                               |
| `--output`   | both           | `table`                                                                                               |

The **Commands** column must agree with the vocabulary table above; the Task 1 command-tree
test reads both, so a disagreement fails a test rather than shipping.

**`--region all` on AWS has a cost, and it is accepted deliberately.** It changes the default
AWS invocation from one region to every region. On `list` that also means the live-price
fallback fires for unpriced instances in every region, and `CLAUDE.md` records that a real
client stalls for `livePriceTimeout` (10 s) per call — `us-east-1` is what bounds that today.
The default is what makes acceptance criterion 2 true, so it stays; say so in the help text
and in `docs/quick-start.md`, and point a person who wants speed at `--offline` or an explicit
`--region`. It also grows the Task 7 goldens.

AWS's old `us-east-1` and `web` defaults are retired. They were the only reason the same
question returned different documents on the two surfaces.

**Commands.** Two, both cloud-neutral:

- `spotinfo list` — filter and show every match, with a risk column. Replaces the bare query
  command.
- `spotinfo recommend` — rank the top N against a stated requirement.

**The discriminator between them**, so this is not re-litigated: `recommend` **requires**
`--architecture`, `--min-vcpu` and `--min-memory-gib` and answers "what should I run"; `list`
requires nothing and answers "what is there". Folding them would force three required flags
onto a browse command. They share a vocabulary, not a purpose.

Bare `spotinfo` with no subcommand prints help and exits non-zero — **except** when a mode
flag is present. `--mcp` and `--version` keep working on the root command.

**MCP tools.** Three, each mirroring the CLI, none with a cloud baked into its name:

| Tool                      | Mirrors              | Replaces                   |
| ------------------------- | -------------------- | -------------------------- |
| `list_spot_machines`      | `spotinfo list`      | `find_spot_instances`      |
| `recommend_spot_machines` | `spotinfo recommend` | `recommend_spot_instances` |
| `list_cloud_regions`      | —                    | `list_spot_regions`        |

**Schemas.** `spotinfo.list/v1` and `spotinfo.recommend/v3`, sharing the candidate, risk,
price and source DTOs. `spotinfo.recommend/v1` and the v1 MCP response shape are deleted.

## Development Approach

- **Testing approach**: regular (code first, then tests) — except Task 2, which writes the
  target e2e suite before the code exists.
- Complete each task fully before starting the next.
- **Every task includes new or updated tests.** Tests are a deliverable, not a follow-up.
- **The per-task gate is `go test ./... -skip 'E2E'` until Task 7 completes.**
  `cmd/spotinfo/e2e_test.go` is written in Task 2 against the target surface and is expected
  to fail until Task 7. It is the **only** exemption from "all tests pass". Every other test,
  in every package, must pass at every task boundary. Never weaken an e2e assertion to reach
  green early — Task 2 renames its tests to `TestE2E…` so the skip pattern is exact.

  **The marker is an infix, `TestE2EFoo`, never a prefix.** A Go test function must begin with
  `Test`; `E2ETestFoo` is not collected, the suite silently disappears, and Task 7's "confirm
  the e2e suite now passes" passes vacuously against zero tests.

  The mechanism was verified against Go 1.26.5 in this repository: `-skip` matches each
  element of a test's name path with an unanchored regexp, so `-skip 'TestVersion'` excludes
  `TestVersionIsReportedWithoutTouchingAnyData`, and `-skip '/gcp'` excludes one subtest while
  its siblings run. `E2E` in the parent's name therefore excludes its subtests too. Do not
  substitute a build tag; the skip keeps the file compiling, which is what catches a rename
  that breaks the suite.

  Two consequences of keeping it compiled, both intended. `e2e_test.go` must reference only
  the standard library and its own subprocess helpers, never a production symbol, so a
  mid-refactor package still builds. And `TestMain` runs regardless of `-skip`, so a broken
  build surfaces as a package-level failure rather than a skipped test — read that failure as
  a compile error, not as an e2e assertion.

- From Task 7 onward the gate is the full `make test`, with no skip.
- Keep `t.Parallel()` where it is safe. Tests in `cmd/spotinfo` that build a `cli.App` stay
  serial — urfave/cli writes to a package-level `HelpFlag` during `Apply`. Subprocess e2e
  tests are unaffected and stay parallel.
- Update this plan when scope changes: `[x]` when done, `➕` for discovered tasks, `⚠️` for
  blockers.

## Testing Strategy

- **Unit tests**: required in every task. Table-driven for input matrices. Mock only system
  boundaries — network, filesystem, clock, credentials.
- **End-to-end tests**: `cmd/spotinfo/e2e_test.go` builds the binary and runs it as a
  subprocess. Credential-free and network-free, and one test must keep proving the offline
  claim with a dead proxy.
- **Golden files**: regenerate **once**, at the end of Task 7, never per-task. Both the `list`
  and `recommend` goldens must be produced from a **fixed stub provider**, never from the
  embedded feeds, so a weekly data-refresh PR cannot rewrite a contract. `contractAdvices()`
  in `cmd/spotinfo/contract_v1_test.go:24` does this today for `spot.Advice`; it becomes a
  fixed `cloud.Provider` stub.
- **Data gates**: `make verify-data` in the same task as any parser or contract change, with
  `UPDATE_GOLDEN` and `REFRESH_MANIFESTS` unset.
- **New live paths**: unit-test the client against a stub transport. Never call a real cloud
  API from a test.

## Progress Tracking

- Mark completed items `[x]` immediately.
- Add discovered tasks with a `➕` prefix.
- Record blockers with a `⚠️` prefix.

## Solution Overview

The design keeps the existing seam and widens it.

- `internal/cloud.PlacementObservation` already exists (`observations.go:123`) with a bare
  `Score int`. It is **extended** with a kind and two optional typed values, so three
  incomparable vendor scores stay distinguishable rather than being normalised into a shared
  1-10 that no vendor published. It is not replaced.
- `internal/cloud` gains `RiskKindEvictionRate`, deliberately outside
  `interruptionCappableKinds`.
- The AWS query path migrates onto `cloud.Candidate` before it is renamed, so the rename is a
  one-line change rather than a rewrite hidden inside one.
- Authenticated sources stay on runtime-only paths gated by an explicit flag, never in a
  snapshot — the pattern `--live-risk` already established for GCP.
- One flag vocabulary is defined once, and a test walks the built command tree to prove the
  declared flags equal it.

## Technical Details

**Azure OS marker.** Three states in `productName`: a `" Windows"` suffix, a `" Linux"` suffix on
newer families, and no suffix, which also means Linux on older families. The rule is a
suffix, not a substring, and Microsoft does not document it — treat it as a high-confidence
convention defended by a coverage floor.

**Azure snapshot thresholds.** `internal/providers/azure/data/source-contract.json` sets
`min_regions: 55`, `min_machines: 180`, `max_compressed_bytes: 131072` and
`max_fractional_digits: 6`. The committed `catalog.json.gz` is 88,272 bytes. Adding Windows
roughly doubles priced rows, so the size cap is the threshold most likely to bind.

**Azure provenance composition.** The manifest carries **81 sources: 55 region-scoped Retail
Prices URLs and 26 Microsoft Learn size pages.** The Learn pages are the provenance for vCPU,
memory and architecture on _every_ candidate. Trimming must be per-scope — region sources by
answer region, size-page sources by the machine series present in the answer — or Invariant 7
is broken. Trimming region sources alone leaves provenance the majority of the payload, which
is why the full list also moves to a separate interface.

**Azure eviction rate — deferred, not in this plan.** It is reachable only through Azure
Resource Graph, which needs a subscription. The query, the band values and the reason it
must never join `interruptionCappableKinds` are recorded in
`docs/reviews/multicloud-parity.md` §3, so the work is one read away if a subscription
appears.

**Placement interfaces.**

|           | AWS                      | Azure                           | GCP                       |
| --------- | ------------------------ | ------------------------------- | ------------------------- |
| Interface | `GetSpotPlacementScores` | `placementScores/spot/generate` | `compute.advice.capacity` |
| Value     | integer 1-10             | `High` / `Medium` / `Low`       | `obtainability` 0.0-1.0   |
| Stage     | GA                       | GA (`2025-06-05`)               | beta                      |
| Limits    | —                        | 8 regions x 5 sizes             | 5 machine types           |

**GCP prices beyond us-central1.** Cloud Billing Catalog API, needs an API key and no special
IAM permission. Runtime only — Google does not state redistribution terms, which is exactly
why it must never reach a snapshot.

## What Goes Where

- **Implementation Steps** (`[ ]`): everything achievable in this repository.
- **Post-Completion** (no checkboxes): release mechanics and manual verification against real
  cloud credentials, which cannot run in this test suite.

## Implementation Steps

---

## Phase 1 — One vocabulary, one schema family

### Task 1: Define the flag vocabulary and prove the command tree matches it

**Files:**

- Create: `cmd/spotinfo/vocabulary.go`
- Create: `cmd/spotinfo/vocabulary_test.go`
- Modify: `cmd/spotinfo/provider_flags.go`

- [x] declare every flag name from the vocabulary table as a single exported constant set,
      together with the unified default for each
- [x] add `mcpArgName(flag string) string` implementing the derivation rule, with the
      repeatable-flag plural exception held as data rather than as a branch
- [x] add a `renamedFlags` map from every removed name to its replacement
- [x] write a table-driven test asserting `mcpArgName` for every flag in the vocabulary
- [x] write a test asserting every removed name maps to a name that exists in the vocabulary
- [x] write a test that walks the command tree built by `newSpotinfoApp()` and asserts the
      declared `cli.Flag` set of each command equals the vocabulary entries marked for it —
      this test is what makes acceptance criterion 3 mechanical
- [x] run `go test ./... -skip 'E2E'` — must pass before Task 2

➕ **`vocabularyGaps` is how that test passes today, and it is a cross-task obligation.**
The tree is still the pre-migration one, so an unconditional equality assertion would be red
at this boundary. `cmd/spotinfo/vocabulary_test.go` therefore records every difference between
the built tree and the vocabulary as data — per command, per flag, each row naming the task
that closes it — and asserts the difference **equals** that table. Containment would rot; the
equality means a task that lands a flag and leaves its row fails exactly as loudly as one that
never lands it, and an empty table is plain equality between the tree and the vocabulary. A
row whose extra name is not in `renamedFlags` also fails, so the table cannot degrade into a
blessed snapshot of whatever the tree declares. Tasks 4, 11 and 13 each carry a `➕` line
below to delete their rows.

➕ `mcpArgName` lives in package `main` and cannot be imported by `internal/mcp`. Task 6's
"every MCP argument name equals `mcpArgName` of its CLI flag" test therefore belongs in
`cmd/spotinfo`, which already imports `internal/mcp`. Do not move the file to "fix" that
import — `internal/cloud` forbids CLI imports and `dependencies_test.go` enforces it.

➕ "Exported constant set" has no meaning in package `main`; the constants keep the existing
`flagX` spelling and are collected in `cmd/spotinfo/vocabulary.go`, which is the single source
the tests read. The vocabulary flag names moved there out of `main.go`, `provider_flags.go`
and `liverisk.go`; `main.go` keeps only the mode and logging flags, which describe how the
binary runs rather than what it is asked.

### Task 2: Write the target e2e suite, and let it fail

This task deliberately ends red. The suite describes the intended surface; Tasks 3 to 7 make
it pass. Do not weaken an assertion to make it green earlier.

**Files:**

- Modify: `cmd/spotinfo/e2e_test.go`

- [x] rename every test in the file to `TestE2E…` — an **infix**, because a Go test function
      must begin with `Test`; `E2ETestFoo` is not collected and the suite would vanish
- [x] keep the file's imports to the standard library and its own subprocess helpers only,
      never a production symbol, so the package still builds mid-refactor
- [x] replace `TestTheWorkloadChoosesThePayloadSchema` with a test asserting **one** schema,
      `spotinfo.recommend/v3`, for every cloud and every workload
- [x] delete `TestTheDefaultWorkloadSelectsADifferentSchemaOnEachSurface` and
      `TestTheCLIAndMCPDisagreeOnTheDefaultAWSAnswer`, whose premises this plan inverts;
      replace them with one test asserting the CLI and MCP return the **same** `request` echo,
      ranking policy and first result for the same question
- [x] rewrite `TestTheQueryCommandRendersEveryOutputFormat`,
      `TestTheNumberFormatPrintsOnlyASavingsPercent` and
      `TestTheQueryCommandAlwaysPublishesPriceProvenance` against `spotinfo list`
- [x] rewrite `TestRejectedInputExitsNonZeroWithAnEmptyStdout` against the new flag names and
      the new refusals
- [x] add a test asserting `spotinfo list --cloud <id>` answers on all three clouds
- [x] add a test asserting every removed flag name produces a rename hint naming its
      replacement, exits non-zero, and prints nothing to stdout
- [x] add a test asserting MCP tool names are exactly `list_spot_machines`,
      `recommend_spot_machines`, `list_cloud_regions`
- [x] add a test asserting `spotinfo --mcp` still starts and `spotinfo --version` still prints,
      while bare `spotinfo` prints help and exits non-zero
- [x] record in a file comment that the suite is expected to fail until Task 7
- [x] run `go test ./cmd/spotinfo/ -run E2E` — expected to fail; confirm every failure is an
      assertion, not a compile error

The suite is 17 tests, all collected by `-run E2E` and all excluded by `-skip 'E2E'`. As
written it fails 17 top-level and 50 subtest assertions, with no compile error, no panic and
no helper failure; `go test ./... -skip 'E2E'` passes.

➕ **`--offline` is passed on AWS only, and that is a Task 8 interaction.** Task 8 refuses
`--offline` on a cloud that declares no `CapabilityLiveEnrichment`, and Tasks 10 and 13 give
Azure and GCP one. A suite that passed `--offline` to every cloud would therefore be green at
Task 7, red at Task 8 and green again at Task 13 — churn that says nothing about the surface.
`e2eOfflineFor` adds the flag for AWS and nothing else; the other two clouds are proved
network-free by the dead proxy instead.

➕ **The dead proxy moved from one test into `e2eEnv`, so every subprocess runs with it.**
While the suite is red a command that does not exist yet falls through to the old tree, and
some of those paths fetch. Blocking by default keeps the network-free claim true at every
intermediate state rather than only at the end.
`TestE2EOfflineAnswersWithEveryOutboundRequestBlocked` still reads the arrangement as its
subject, which is the checkbox the plan asks for.

➕ **`candidates` is the array key this suite defines for `spotinfo.list/v1`.** The plan names
every other field in both schemas; the list array had no name yet, and Task 5 must publish it
under this one. Everything else the suite decodes — `schema_version`, `status`, `request`,
`ranking_policy`, `data_source`, `recommendations` and the candidate and risk fields — is
already spelled by `internal/cloud/schema.go`.

➕ **Two column assertions are targets Tasks 4 and 7 must land, not descriptions of today.**
The `recommend` table carries the same neutral columns on all three clouds, AWS included; and
`list` names a `risk` column rather than AWS's "Frequency of interruption", because the column
is a neutral risk observation once the command is cloud-neutral.

➕ **Nothing in the suite asserts a rendering style, and three places had to be written that
way.** The two renderers disagree today — the query command draws a box, the recommend table
does not — and which survives Task 7 is not this suite's decision. So column names are matched
case-insensitively and searched across the whole page rather than on line 1, which is a border
in one of the two; `dataRows` filters any line carrying no letter or digit, so the "`--top 3`
returns three rows" count holds under either; and `text`, which prints `key=value` per row and
no header at all, is asserted only to name the machine it priced. The `region` column is left
out of both lists: whether it is rendered is conditional on the query naming more than one
region, which is a separate decision from what the columns are called.

➕ **A recommend-only flag passed to `list` is deliberately not in the refusal table.**
`--workload` is undefined on `list`, so urfave/cli rejects it while parsing and writes its
usage banner to **stdout** — which the empty-stdout assertion would fail, for a reason no task
here owns. The command-tree test in `cmd/spotinfo/vocabulary_test.go` is what proves the flag
is not declared there.

### Task 3: Move the AWS query path onto `cloud.Candidate`

The command keeps its current name here. This is the migration; Task 4 is the rename. Splitting
them keeps a ~500-line rendering change out of a task that reads like a rename.

**Files:**

- Modify: `cmd/spotinfo/main.go`
- Modify: `cmd/spotinfo/provider_flags.go`
- Modify: `internal/providers/aws/provider.go`
- Modify: `cmd/spotinfo/main_test.go`
- Modify: `cmd/spotinfo/format_test.go`
- Modify: `cmd/spotinfo/contract_v1_test.go`
- Modify: `cmd/spotinfo/mocks_test.go`
- Modify: `cmd/spotinfo/provider_flags_test.go`

**The proof is the unchanged goldens, not a new test.** `contract_v1_test.go:63` drives the
whole app through `execMainCmd` with a mocked `spotClient`, and
`internal/providers/aws/provider.go:39` consumes that _same_ `GetSpotSavings` interface. Build
the AWS provider over the existing mock and keep `cmd/spotinfo/testdata/aws-root-v1.*` green,
unchanged, for all five formats. That is a stronger gate than any hand-written parity test.

- [x] make the AWS provider serve everything the query command renders today: interruption
      range, savings, price source, zone prices and placement scores
- [x] rewrite `printAdvicesText`, `printAdvicesNumber`, `buildTableRow`, `printAdvicesTable`,
      `expandAZ`, `getScoreDataValue`, `analyzeScoreTypes` and `sortByNames` against
      `cloud.Candidate` instead of `spot.Advice`
- [x] route the command through `cloud.Provider`, replacing the legacy-client path
- [x] **preserve the failure mode the legacy path exists to prevent.**
      `cmd/spotinfo/provider_flags.go:117-128` records it: building an AWS provider for this
      command made it inherit every input the provider needs, so an unreadable architecture
      manifest or sidecar — neither of which this command reads — failed
      `spotinfo --type t3.micro` with `SNAPSHOT_UNAVAILABLE` while the advisor and price data
      it _does_ read were intact. Keep construction cheap and let **acquisition** verify
      payloads, so a snapshot this command never reads cannot fail it
- [x] write a test that makes the architecture manifest unreadable and asserts
      `spotinfo list --machine "t3.micro"` still answers — this is the regression guard for the
      bullet above, and without it the bug re-ships silently
- [x] handle `Candidate.SavingsPercent` being `*int` where `spot.Advice.Savings` is a plain
      `int`: a zero savings prints `0` today and must keep printing `0`, not blank.
      `aws-root-v1.number.txt` is a savings percent, so this is load-bearing
- [x] handle the price type change: `spot.Advice.Price` is `float64` and
      `PriceObservation.Amount` is fixed-point `cloud.Money`; assert the last digit does not
      move for every golden price
- [x] keep `cmd/spotinfo/testdata/aws-root-v1.*` byte-identical and passing, with no
      `UPDATE_GOLDEN` run in this task
- [x] run `go test ./... -skip 'E2E'` — must pass before Task 4

➕ **The provider is built from the acquisition client, not resolved from the registry, and
that is what preserves the failure mode.** `awsQueryProvider` in `cmd/spotinfo/main.go` calls
`awsprovider.New(client, nil)`; `resolveAWSProvider` still answers the capability gate from the
package-level declaration and still routes every non-AWS cloud through the registry. The
architecture snapshot is the input the registry factory adds, so passing no lookup is what keeps
it out — and `TestTheQueryProviderDeclaresNoArchitecture` pins that the provider then declares
no architecture rather than classifying machines from their names.

➕ **The regression test drives the registry, not a corrupted embedded file.** The architecture
snapshot is `//go:embed`-ed and cannot be made unreadable at run time, so
`TestQueryAnswersWhenTheArchitectureSnapshotIsUnreadable` registers an AWS factory that fails
the way an unparsable manifest makes it fail, and asserts the query still answers. That is the
condition that re-introduces the bug: a future `registry.Get(aws)` in `execMainCmd` turns it
red. It drives the root query command with `--machine t3.micro`, because `spotinfo list` does
not exist until Task 4.

➕ **`spotClient` gained `DataSource() string` and `mocks_test.go` was regenerated.** It is now
exactly `internal/providers/aws`'s own input, so the command builds its provider over the same
client the golden test already mocks — which is what makes the unchanged goldens the proof. The
CLI tests take it through `newQueryClient`, which declares the expectation `.Maybe()`: a request
refused before acquisition never reads it.

➕ **`awsprovider.New` no longer warns about a nil architecture lookup.** Only the caller knows
whether it failed to load one or deliberately passed none, and `newProviderRegistry` already
logs the real failure. Left in place, every single query invocation printed "aws architecture
snapshot is unavailable" for a capability it never asked for.

➕ **An `--os` outside the neutral vocabulary is now refused before acquisition**, by the
provider's own capability check, rather than reported by the legacy client after a fetch.
`TestExecMainCmd_ErrorConditions` carries no client expectation for that case, so the test also
pins that no I/O happens — Invariant 3, checked. The wording moved from
`invalid instance OS: invalid-os` to `aws does not support os "invalid-os"`.

➕ **A _valid_ `--os` in any casing still answers, and that took a follow-up fix.** The
capability check is `slices.Contains` against the lowercase neutral constants, so casting the
raw flag into `cloud.Query` also refused `--os Linux` and `--os Windows` — spellings the legacy
client accepted with `strings.EqualFold` and the recommend path already folds through
`foldVocabulary`. `rootQuery` now folds the flag the same way, which restores both spellings and
leaves the invalid-OS wording above untouched (`foldVocabulary("invalid-os")` is the identity).
The fold is at the CLI boundary, not in `Capabilities.SupportsOS`: a case-folding comparator
would let a mixed-case `OS` survive the gate and reach the GCP and Azure lookups, which key on
the lowercase constants, turning a clear refusal into a silent empty answer.
`TestExecMainCmdAcceptsAnyOSSpelling` drives `execMainCmd` over seven spellings, so it covers
the gate and the provider's own `SupportsOS` rather than the mapping alone. Folding also trims,
so `--os " linux "` now answers where it previously errored — that one is new behaviour, not
restored behaviour, and it is deliberate: `ParseOperatingSystem` trims, so the recommend path
already accepted it and the whole point of the fold is that one spelling means one thing on
both commands. The MCP v1 tool needs no equivalent fix: `find_spot_instances` exposes no `os`
argument and pins `cloud.OSLinux` (`internal/mcp/tools.go:263`).

➕ **Verified against the shipped binary, not only against the goldens.** The goldens are three
fixed rows with no zone prices, no zone scores, no unpriced instance and no zero savings, so
they cannot see the two type changes this task is really about. Building the binary at this
commit and at its parent and diffing real embedded answers closes that: `json` record sets
(order-insensitive, because `--region all` was already nondeterministic before this change —
`sort.Sort` is unstable over map iteration) and sorted `text`, `csv`, `number` and `table`
output are identical for `us-east-1` and for `me-south-1`, across `--sort price|savings`,
`--cpu/--memory/--price` and an empty match. That covers **254 unpriced rows** in `me-south-1`,
which exercise the absent `PriceObservation`, and **6 zero-savings rows** in `us-east-1`, which
exercise the absent `SavingsPercent`. Every field matches except the one below.

The score flags are **not** covered by that comparison: `--offline` clears the live-price and
score providers, so `--with-score`, `--az`, `--min-score` and `--score-timeout` produce no
observations to differ over. `expandAZ` in particular changed from map order to the provider's
sorted zone order, and is covered only by `TestExpandAZSplitsZonalScoresAndPrices` and
`TestScoreValueReadsPlacementObservations`. Two other mappings are likewise unreachable in the
committed snapshot, and both are pre-existing provider behaviour the MCP path already had: a
savings figure above 100 now renders as `0` rather than raw, and a price needing more than nine
fractional digits drops the candidate rather than rendering it.

➕ **The `--region all` answer was already nondeterministic before this task, and Task 17's
checkbox names the wrong cause.** Running the pre-change binary twice over
`--offline --type m6g --region all` produces different output: `internal/spot/client.go:277`
iterates a map of instances and `sortAdvices` at `internal/spot/types.go:142` is `sort.Sort`,
which is not stable, so ties in the sort key keep whatever order the map gave. Task 17 asserts
byte-identical stdout across two identical `--offline` runs and attributes a failure to "a
non-deterministic map iteration in a renderer" — the renderers are deterministic; the
acquisition path is not. Fixing it means `sort.SliceStable` (or `slices.SortStableFunc`) in
`internal/spot`, not a renderer change. Out of scope here: it is unchanged behaviour, and this
task's own comparison worked around it by comparing record sets rather than order.

⚠️ **The v1 root JSON's `info.emr` is now always `false`.** `cloud.Candidate` carries no EMR
classification — it is a Spot Advisor flag with no meaning on another cloud — and 731 of the
1,192 instance types in the committed advisor snapshot publish it as `true`, which is **13,505
rows** of a full `--region all` answer. It is the only field that does not survive the seam, it
is not a golden change (`contractAdvices` sets no EMR flag), and the MCP v1 surface — answering
from `cloud.Candidate` since the seam was added — never published it either.
`cmd/spotinfo/v1json.go` says so at the field. **Task 5 must delete the field with the schema**,
which it already does by replacing the whole document with `spotinfo.list/v1`. Do not
reintroduce it into the neutral domain to close this.

⚠️ **Do not cut a release between Task 3 and Task 5.** A review confirmed the scale from the
shipped binaries: comparing `--offline --output json` record sets built at this commit and its
parent, 724 of 1,145 records differ in `us-east-1`, 655 of 929 in `eu-west-1` and 179 of 254 in
`me-south-1` — and in every differing record `info.emr` is the only changed field. `false` is a
wrong value, not an omission, so any tag in that window publishes a false EMR claim for the
majority of rows. The field cannot be dropped inside Task 3 (that moves the golden Task 3 must
keep byte-identical) and cannot be restored (see above), so the only remedy is to finish Task 5
before tagging.

### Task 4: Rename the command to `spotinfo list` and make it cloud-neutral

**Files:**

- Create: `cmd/spotinfo/list.go`
- Modify: `cmd/spotinfo/main.go`
- Create: `cmd/spotinfo/list_test.go`
- Modify: `cmd/spotinfo/multicloud_test.go`
- Modify: `cmd/spotinfo/validation_test.go`

- [x] move the migrated query command under a `list` subcommand carrying the vocabulary flags
- [x] render the risk column from the neutral risk observation, so a cloud without risk data
      prints its status rather than a blank or a zero
- [x] make bare `spotinfo` print help and exit non-zero **only when no mode flag is set** —
      `--mcp` is dispatched from the root Action at `main.go:164` and `--version` from the same
      command; both must keep working
- [x] move the five output formats onto `list`; `number` stays list-only, because one savings
      percent has no meaning for a ranked page
- [x] add `--sort` and `--order` to both commands, over neutral fields only
- [x] apply the unified defaults from the vocabulary section to both commands
- [x] write tests for `list` on a stub provider per cloud, including a cloud with no risk data
- [x] write tests for every sort key and both orders
- [x] write a test asserting `spotinfo --mcp` and `spotinfo --version` still work and bare
      `spotinfo` does not run a query
- ➕ point `commandScopes` in `cmd/spotinfo/vocabulary_test.go` at the new `list` command —
  it currently records the list vocabulary as living on the root — and delete every
  `vocabularyGaps` row marked "task 4". The command-tree test fails if a flag lands and
  its row stays — **done**; the root scope now carries the globals alone and only the task 11
  and task 13 rows remain
- [x] run `go test ./... -skip 'E2E'` — must pass before Task 5

➕ **`--sort` has no default, and that is what keeps the command cloud-neutral.** The old
default was `interruption`, which under the neutral vocabulary is `--sort risk` — and
`Query.CapabilityNeeds()` demands `CapabilityRisk` for that key, so a defaulted risk sort would
refuse `spotinfo list --cloud gcp` before acquisition for a column the command renders as a
status. An unset key leaves the order to the provider, which is also what keeps the AWS goldens
byte-identical: `legacySortBy("")` is `spot.SortByRange`, the same ordering the old default
produced. `--order` alone is accepted and means ascending.

➕ **The AWS golden pins the fixture order, not a sort, and always has.** Sorting happens inside
`internal/spot.Client`; `spot.WithSort` is a client option and `contract_v1_test.go` mocks the
client, so the row order of `cmd/spotinfo/testdata/aws-root-v1.*` is whatever `contractAdvices()`
returns. Verified by reordering the fixture: the goldens fail, which is the discriminator — the
recorded order is the input order. That is pre-existing (the retired `--sort interruption` default
was equally invisible to the mock), so dropping the default did not loosen the contract, and
adding `--sort risk` to the invocation would only imply a coverage the test does not have. **Task 7
should keep this in mind** when `contractAdvices()` becomes a fixed `cloud.Provider` stub: ordering
will still come from the stub, not from a sort.

➕ **The sort key names are the neutral field names.** `type` became `machine` and
`interruption` became `risk`, so the CLI word and the `cloud.SortKey` it maps to are spelled the
same — that is what "over neutral fields only" buys. `savings`, `price`, `region` and `score`
were already neutral.

➕ **On `recommend`, `--sort` reorders the ranked page and never re-selects it.** Selection stays
the canonical ranking policy published in `ranking_policy`, which acceptance criterion 2 requires
both surfaces to agree on, and `Rank` keeps naming each row's position in that policy — a row
labelled rank 3 printed first is honest, renumbering would not be. Threading the key into
`RecommendRequest.Query()` instead would be a silent no-op, because `rank()` re-sorts every
candidate unconditionally. `--sort score` is refused there: a recommendation publishes no
placement score until Task 11.

➕ **The surviving AWS `spotinfo.recommend/v1` path refuses `--sort` and `--order`.** That report
publishes a fixed ranking and cannot honour them, and accepting them there would make the same
flag on the same command mean two things depending on which schema answered. Task 5 deletes the
path and the refusal with it.

➕ **`--architecture` is a list flag now, so the AWS architecture snapshot is read on demand.**
`awsQueryProvider(client, withArchitecture)` loads the lookup only when the flag is set, which
keeps Task 3's guard intact — an unreadable architecture snapshot must not fail a query that
never mentions an architecture — while making the flag act rather than be refused.

➕ **A retired flag name is answered by an `OnUsageError` handler, not by a declaration.** The
names are gone from the tree, so urfave/cli would otherwise print "Incorrect Usage" plus the whole
help page **to stdout** and report `flag provided but not defined: -type`. `renameHint` in
`cmd/spotinfo/main.go` maps the name through `renamedFlags` and returns the error printing
nothing; it is set on the app **and** on both commands, because `Command.Run` consults
`c.OnUsageError` and never the app's. Anything outside `renamedFlags` is returned unchanged.

➕ **The root command lost `--cloud`, `--offline` and `--refresh` along with the query flags, and
two invocations stop parsing.** `spotinfo --cloud gcp recommend …` must now pass `--cloud` after
the subcommand, and `spotinfo --offline --mcp` is no longer accepted — `CLAUDE.md` states that
composition explicitly, so **Task 16 must correct that sentence**. The MCP surface gets an
`offline` tool argument in Task 6; until then the server answers from the live feeds with the
snapshot as fallback, which is what `cmd/spotinfo/e2e_test.go` already exercises through its
dead proxy.

➕ **`--workload` now defaults to `cost` on every cloud, which changes what AWS answers by
default.** `spotinfo recommend --cloud aws` with no workload is served by the neutral engine and
`spotinfo.recommend/v2`, where it used to default to `web` and be served by the v1 schema. That
is the unified default the plan asks for; the v1 report is still reachable with an explicit
`--workload web|ci|batch` until Task 5 deletes it.

⚠️ **`--gcp-project` is declared on `list` and nothing on that command reads it yet.** The
vocabulary marks it for both commands and the command-tree test enforces that, but the only
authenticated GCP path today is `--live-risk`, which is recommend-only. Task 8 refuses it off
GCP; Task 12 gives `list` a use for it through obtainability.

### Task 5: Retire `spotinfo.recommend/v1` and publish one schema family

**Files:**

- Modify: `internal/cloud/schema.go`
- Create: `docs/plans/contracts/list-v1.schema.json`
- Rename: `docs/plans/contracts/recommend-spot-instances-v2-{success,input,error}.schema.json`
  → `recommend-v3-{success,input,error}.schema.json`
- Delete: `cmd/spotinfo/testdata/aws-recommend-v1.json`
- Delete: `internal/spot/recommend.go`, `internal/spot/diagnose.go` and their tests
- Modify: `cmd/spotinfo/recommend.go`
- Modify: `cmd/spotinfo/contract_v2_test.go`
- Modify: `cmd/spotinfo/recommend_test.go`
- Modify: `internal/cloud/schema.go`, `internal/cloud/schema_test.go`
- Modify: `internal/cloud/observations.go`
- Modify: `internal/providers/azure/provider.go`, `internal/providers/gcp/provider.go`
- Modify: `internal/mcp/jsonschema_test.go`, `internal/mcp/recommend_test.go`,
  `internal/mcp/recommend.go`

- [x] delete the v1 recommendation adapter and every branch that selected a schema by workload
- [x] delete `internal/spot/recommend.go` and `internal/spot/diagnose.go`, now unreachable —
      this also resolves the misleading empty-result diagnostic in surface review §6.5, which
      is recorded here as **resolved by deletion**, not deferred
- [x] emit `spotinfo.recommend/v3` for every cloud and every workload
- [x] add `spotinfo.list/v1`, sharing the candidate, risk, price and source DTOs with v3
- [x] rename request fields to the vocabulary names in both schema files
- [x] **derive each source's scope from its URL, in the provider that owns the source.**
      `cloud.SourceRef` (`internal/cloud/observations.go:41`) carries no scope field, and is
      built in exactly one place, `internal/snapshot/manifest.go:236`. Adding a `scope` field
      to the manifest format is the wrong route: `ParseManifest` uses
      `DisallowUnknownFields` (`manifest.go:159`), so it would force every provider's manifest
      to be regenerated — dragging a network rebuild into Phase 1. Azure retail URLs carry
      `armRegionName+eq+%27<region>%27` and Learn URLs end `/sizes/<family>/<series>-series`,
      so both scopes are derivable
- [x] **fail closed: a source whose scope cannot be parsed is retained, never dropped.** That
      keeps Invariant 7 true by construction when a vendor changes a URL shape
- [x] trim `data_source.sources` **per scope**: region-scoped sources by the regions present in
      the answer, size-page sources by the machine series present in the answer
- [x] add `sources_omitted` counting what was read but not listed, and expose the full list
      through `list_cloud_regions` in Task 6, so the count is resolvable
- [x] write a test asserting a source with an unparseable URL is retained
- [x] update each renamed schema file's own `$id`, and every reference to the old filenames —
      `cmd/spotinfo/contract_v2_test.go`, `internal/mcp/jsonschema_test.go`,
      `internal/mcp/recommend_test.go`, `internal/mcp/recommend.go`, `internal/cloud/schema.go`,
      `internal/cloud/schema_test.go` and `docs/api-reference.md`, including doc comments
- [x] grep for `recommend-spot-instances-v2` and confirm zero hits outside `docs/plans/`
- [x] write a test asserting provenance is **less than half** the serialized payload for a
      three-row Azure answer — a ratio, not a byte count, so it survives a change in source
      count
- [x] write a test asserting every retained source is referenced by at least one candidate
- [x] write tests asserting both schemas validate against their contract files
- [x] run `go test ./... -skip 'E2E'` — must pass before Task 6

➕ **`SourceScope` names machine identifiers, not a series string.** The trimming happens in
`internal/cloud`, over published candidates, and `internal/providers/azure/catalog.go:38` records
why a neutral series field would not work: "an Azure size name does not carry its series in a form
that can be parsed back out". So `cloud.SourceScope` is `{Region, Machines []MachineID}`, the Azure
provider translates a Learn page's series into the machines it documents, and "series" stays an
Azure word. Each dimension is an independent filter and an empty dimension is unconstrained, which
is what makes the zero scope cover every candidate — fail-closed by construction rather than by a
branch that could be deleted.

➕ **Measured, on the committed Azure snapshot: 4 sources published, 77 omitted, provenance
0.446 of the serialized payload.** Untrimmed it is over nine tenths. 0.446 is close to the half the
test asserts, and deliberately so — the margin comes from trimming *both* scopes. Region-only or
series-only trimming lands above half, which is the failure the plan's "Azure provenance
composition" note warns about, so the ratio test is what would catch a half-done trim.

➕ **An answer with no candidates publishes every source, and a trim that would publish none
fails closed.** Nothing can be trimmed against an empty row set — "which of these describes a row"
has no answer when there are no rows — and both schemas declare `data_source.sources` with
`minItems: 1`, so an empty list would be a document that fails its own contract.

➕ **`internal/spot/recommend.go` could not simply be deleted.** It also held the embedded
architecture snapshot — `Architecture`, `ArchitectureLookup`, `LoadEmbeddedArchitectureLookup` and
the `//go:embed` — which `internal/providers/aws`, `cmd/spotinfo/list.go` and the registry all
still use. That half moved to `internal/spot/architecture.go` and its two tests to
`internal/spot/architecture_test.go`; everything recommendation-shaped is gone, including
`Workload`, `RecommendationOptions`, `ErrInvalidRecommendationInput` and
`ErrNoRecommendationCandidates`.

➕ **Two goldens are deleted with the documents they recorded, and none was regenerated.**
`cmd/spotinfo/testdata/aws-recommend-v1.json` goes with the v1 report; `aws-root-v1.json` goes with
the v1 root array, and the `json` row of `TestAWSListOutputMatchesRecordedV1Contract` with it. The
four rendered goldens beside it stay **byte-identical and green** — `list` renders the same table,
text, csv and number output — and remain the value-level gate until Task 7 records the new set. The
JSON contract moved from recorded bytes to
`docs/plans/contracts/list-v1.schema.json`, which is a stronger check of the thing Task 5 changed.

➕ **The three `internal/mcp` recommend goldens were renamed and hand-edited, not
regenerated.** No `UPDATE_GOLDEN` run happened anywhere in this task. The whole diff is four lines
across two files: `spotinfo.recommend/v2` → `/v3`, `max_price_per_hour` → `max_price` (twice, and
alphabetical order puts it in the same position), and `"sources_omitted": 0` after the source list.
Each was reviewed against the schema change that caused it.

➕ **`execRecommendCmd` lost its `spotClient` parameter, and that changes the test wiring.**
The command reaches AWS through the registry like every other cloud now, so a stub provider with an
empty result no longer stands in for the acquisition client: `recommendTestApp` registers the
production `internal/providers/aws` adapter over the mocked client. That is a stronger arrangement
than the v1 path had — the mapping is exercised, not bypassed.

➕ **`--max-price` is the vocabulary name on the wire too.** `RequestDTO`'s tag, both schema
files and the recommend tool's argument are now `max_price`; the v1 `find_spot_instances` tool
keeps `max_price_per_hour` under its own constant until Task 6 retires it, so the golden-pinned v1
input schema is untouched.

➕ **`printAdvicesJSON` is deleted with the array it printed, and its `\u003c` unescaping with
it.** Both commands now render JSON through `writeJSONReport`, so a risk label reads
`"\u003c5%"` in `list` output exactly as it already did in `recommend` output. It decodes to
`<5%`; one JSON writer for one schema family is worth more than the cosmetic difference.

➕ **The list document carries the four flag-gated observations, not just the shared block.**
`live_price` is always emitted and `zone_prices`, `region_score`, `zone_scores` and
`score_fetched_at` are `omitempty` — the split `CLAUDE.md` records as a deliberate contract
decision. Dropping them would have made `--with-score --output json` lose what `--output table`
still shows.

➕ **Files this task's list omits.** `cmd/spotinfo/list.go` (the JSON branch), `v1json.go` and
`cmd/spotinfo/testdata/aws-root-v1.json` (deleted), `cmd/spotinfo/main.go`,
`cmd/spotinfo/provider_flags.go` (`recommendCapabilityRequest` is dead — `cloud.Recommend` derives
and checks the same needs), `internal/spot/architecture.go`, `internal/providers/azure/sources.go`,
`internal/cloud/sources_test.go`, `internal/providers/azure/sources_test.go`,
`cmd/spotinfo/{contract_v1,main,list,multicloud,provider_flags,validation}_test.go`, and the
schema-version sentences in `README.md`, `docs/clouds.md` and `docs/mcp-server.md`, which Task 5
makes false.

➕ **Six of the Task 2 e2e tests pass at this boundary, and that is the real check on the two
new documents.** `TestE2EJSONReportIsValidForEveryCloud`, `TestE2EListAnswersOnEveryCloud`,
`TestE2EOneSchemaAnswersEveryCloudAndWorkload`, `TestE2ETheListCommandAlwaysPublishesPriceProvenance`,
`TestE2EACloudWithoutRiskDataReportsUnavailableRatherThanZero` and
`TestE2EOfflineAnswersWithEveryOutboundRequestBlocked` all pass against the shipped binary — the
suite decodes `schema_version`, `status`, `request`, `data_source` and `candidates` by name, so a
field this task spelled differently would have shown up here rather than inside Task 7.
`TestE2ETheListCommandRendersEveryOutputFormat` still fails on its `machine` and `risk` column
names, which the plan already records as a Task 7 target.

➕ **The trim is O(sources x candidates) and measurably free.** `spotinfo list --cloud azure
--output json` over every region is 11,204 rows against 81 sources and runs in 0.12 s, against
0.12 s for the same query rendered as a table, which does no trimming; the worst shape found — 950
rows with 37 sources omitted, so 37 full scans — is 0.06 s either way. `coversAny` short-circuits
on the first row a source backs, and the comment on it names the ceiling and the upgrade trigger:
collect the published regions and machine IDs into two sets first, which makes it
O(sources + candidates).

➕ **`spot_usd_per_hour` is nullable in `spotinfo.list/v1`, and was `""` for one commit.**
Reported by review: `candidateDTO` set the amount only when the observation existed, so a machine
the AWS static price feed does not price published the Go zero string — which the contract this
same task created rejects, because `""` matches neither the amount pattern nor `null`. Measured on
the committed snapshot: 2 rows of 1,145 in `us-east-1`, **254 of 254** in `me-south-1`, and **600
of 19,353** under the unified `--region all` default, so `spotinfo list --output json` published a
document that failed its own schema. The fix is `*string` in the shared `CandidateDTO` and
`["string", "null"]` in `list-v1.schema.json` — the same shape `on_demand_usd_per_hour` already
had, and the one `internal/cloud/observations.go` states: an unknown price is the absence of an
observation, never a zero. All three answers now validate against the contract file, as do GCP and
Azure `list` and `recommend` on every cloud.

`recommend-v3-success.schema.json` **stays non-nullable**, because `accepts` drops a candidate
with no price before ranking. `TestRankerReappliesEveryConstraint` now asserts every published
recommendation carries an amount, so the asymmetry between the two schemas is enforced rather
than assumed. `savings_percent` stays published on an unpriced row: AWS publishes no on-demand
price at all, so *every* AWS row already carries a discount without its denominator, and the
figure is the Spot Advisor's own — read from a different feed than the price. That is not the
shape `internal/providers/azure/catalog.go` refuses, which is a savings figure Azure would have
had to compute itself.

It shipped green because `testCandidate.build()` attaches a price only under `if c.Price > 0`
and every fixture in `TestListPayloadValidatesAgainstTheContract` was priced, so the nil branch
was outside the fixture space. The case is now in the table with `Price: 0, Savings: 41` — the
`dl1.24xlarge` shape — and the test asserts the key is present **and null**, not merely present.
Reverting `internal/cloud/schema.go` makes it fail on the real error.

⚠️ **AWS `list` candidates publish `architecture: ""`, and Task 7 is about to freeze that into a
golden.** `awsQueryProvider` reads the architecture snapshot only when `--architecture` asks for it
— Task 4's ➕ above records why — so the default AWS browse answer carries an empty architecture
where GCP and Azure carry `x86_64`. `list-v1.schema.json` therefore admits `""` in that enum where
`recommend-v3-success.schema.json` does not, which is the one place the two schemas' shared
candidate block is not identical. It is not a cloud reporting silence for something it does not
publish — the classification is embedded in this binary — so Invariant 2 is not in play, and the
fix is a decision about `awsQueryProvider`, not about the schema: loading the lookup best-effort
when the flag is absent would populate the field and let `""` leave the enum, but it contradicts
`TestTheQueryProviderDeclaresNoArchitecture` and the Task 4 rationale behind it. Left alone here
rather than reversed inside a schema task. **Task 7 must decide before it records the `list`
goldens.**

⚠️ **`CLAUDE.md` still describes `spotinfo.recommend/v1` as a live contract** (lines 12,
15, 68 and 129). Task 16 owns that file and already carries a checkbox for it; it was left alone
rather than half-rewritten here, but it is wrong as of this commit.

### Task 6: Rebuild the MCP surface on the same vocabulary

**Files:**

- Modify: `internal/mcp/server.go`, `internal/mcp/tools.go`, `internal/mcp/recommend.go`
- Delete: `internal/mcp/testdata/find-spot-instances-v1-input-schema.json`
- Delete: `internal/mcp/testdata/find-spot-instances-v1-response.json`
- Delete: `internal/mcp/contract_v1_test.go`
- Modify: `internal/mcp/tools_test.go`, `internal/mcp/recommend_test.go`,
  `internal/mcp/jsonschema_test.go`, `internal/mcp/server_test.go`,
  `internal/mcp/helpers_test.go`, `internal/mcp/bench_test.go`, `internal/mcp/race_test.go`
- Modify: `internal/cloud/provider.go`

- [x] replace the three tools with `list_spot_machines`, `recommend_spot_machines` and
      `list_cloud_regions`, each taking `cloud` as an argument rather than implying AWS
- [x] add a **`RegionsOf(ctx, Provider)` helper**, not a `Regions()` interface method, so
      `list_cloud_regions` can enumerate a non-AWS cloud. `fetchRegions` at `tools.go:570`
      hardcodes an AWS query today. A `Query` with `Regions: [all]` already yields the answer,
      which is what that function does — the helper generalises it in one file. Adding a method
      to `cloud.Provider` would instead break all three providers and five test stubs for no
      extra capability
- [x] let `list_cloud_regions` also publish the full source list for a cloud, resolving
      `sources_omitted` from Task 5
- [x] derive every argument name through `mcpArgName`, and assert it in a test rather than
      hand-writing the names
- [x] make every tool return a structured `spotinfo.error/v1` body with a stable `code`;
      delete the bare-string error path. This also retires the v1 `min_memory_gb` unit
      mislabel, recorded here as **resolved by deletion**
- [x] add `readOnlyHint: true`, `idempotentHint: true`, `destructiveHint: false` and
      `openWorldHint: true` to all three tools, using the `mcp-go` v0.57.0 annotation builders
- [x] take every default from the shared default set, so no default can differ by surface
- [x] sweep the old tool names out of code and tests — they also appear in
      `internal/providers/aws/provider_test.go` and `internal/spot/data_test.go`
- [x] write a test asserting every MCP argument name equals `mcpArgName` of its CLI flag
- [x] write a test asserting every tool declares the read-only and idempotent annotations
- [x] write tests for the structured error body of each tool
- [x] write a test asserting `list_cloud_regions` answers for all three clouds
- [x] run `go test ./... -skip 'E2E'` — must pass before Task 7

➕ **`spotinfo.regions/v1` is a fourth schema, and the plan's Schemas paragraph does not name
it.** `list_cloud_regions` had to publish something, and a bare `{regions, total}` map — what the
retired `list_spot_regions` returned — could not carry the source list the checkbox above asks
for. The new payload reuses the `data_source` and source DTOs of the other two, so the omitted
entries are recovered in exactly the shape they were trimmed from. No contract file: no checkbox
asks for one, and `internal/cloud/sources_test.go` covers the document beside the trimming it
undoes — the test asserts published + omitted on a `list` answer equals what the regions answer
publishes, so the two cannot drift apart.

➕ **`RegionsOf` returns `([]Region, Result, error)`, not just the regions.** The caller needs the
provenance of the same acquisition, and a second query to fetch it would double the work. The
capability check lives inside the helper, so all three tools get Invariant 3 from one place. The
query pins `OS: linux` the way `fetchRegions` did: every provider declares it, and an empty OS is
an untested path through three catalogues to answer a question that is not about an operating
system.

➕ **Measured, on the shipped binary: `list_cloud_regions --cloud azure` publishes 81 sources
with `sources_omitted: 0`, against 2 published and 79 omitted for a one-row `list_spot_machines`
answer in the same session.** That is the resolvability the checkbox asks for, checked rather
than asserted.

➕ **"No default can differ by surface" needed the vocabulary itself to move, not just the
values.** `internal/mcp` cannot import `cmd/spotinfo`, so a shared default has to live in
`internal/cloud`. Moved there: the `--sort` word list (`SortKeyNames`, `ParseSortKey` — the CLI
words, so `score` still means `placement_score` on both surfaces), `OrderAsc`/`OrderDesc`,
`MaxPlacementScore` and `DefaultScoreTimeoutSeconds`. That last one **was** a real drift:
`internal/mcp` declared 30 with a comment justifying it by the v1 input-schema golden this task
deletes, while the CLI read `spot.DefaultScoreTimeoutSeconds`. `internal/spot` keeps a private
30-second fallback for a lookup it was given no timeout for — a client's own default, not a
surface's. `cmd/spotinfo` now spells `sortMachine`, `orderAsc` and `allRegions` from the neutral
constants, so one value is provably read by both.

➕ **`offline` and `refresh` are real arguments, and landing them was a composition change.**
The server held one acquisition client, so the two flags had no per-call meaning; advertising them
anyway would have been the defect the retired `include_names` parameter was. `providerRegistry.Get`
now takes a `cloud.FetchPolicy`, and `mcpProviders` in `cmd/spotinfo/main.go` builds at most one
registry per policy and reuses it — a refreshing call always builds fresh and **replaces** the
memoised entry, so the next caller answers from what refresh fetched rather than from the copy it
superseded. `runMCPServer` no longer reads a `cli.Context`: the data policy is a tool argument now,
not a property of the process. Verified against the shipped binary — the same
`list_spot_machines` call reports `embedded-snapshot` with `offline: true` and `cached` without it.
`stubRegistry` records the policy of every lookup, and `TestTheDataPolicyReachesAcquisition`
asserts it for both tools, because "accepted" and "honoured" are otherwise indistinguishable.

➕ **One CLI/MCP asymmetry is deliberate: an explicit empty `architecture`.** `--architecture ""`
is unset on the CLI, because `listQuery` trims and tests for empty; `architecture: ""` over MCP is
refused, because `ParseArchitecture` rejects it. That matches what the recommend tool already does
for `cloud`, `os` and `workload`: an argument a caller omits is absent, an argument they send as
`""` is a value none of the enums list, and folding the second into the first answers a question
they did not ask. Recorded rather than reconciled — changing either side would make one of those
two rules inconsistent.

➕ **`mcpProviders` is covered directly, not only through the tools.** Every MCP test drives a stub
registry, so the tool-side assertion proves the policy reaches `Get` and stops there.
`TestTheMCPProviderCacheHonoursTheDataPolicy` in `cmd/spotinfo` takes the other half: the same
policy reuses one client, a different policy gets a different one, and a refresh **replaces** the
memoised entry so the next caller answers from what it fetched. Verified by deleting the
assignment — the test goes red on exactly that line, which nothing else in the suite did.

➕ **`stubFor` copies its fixtures, found by `go test -race`.** It stamps each candidate with the
cloud it is serving, and parallel subtests routinely share one fixture slice — stamping in place is
a write the detector reports against whichever sibling reads it. It reproduced roughly one run in
six; six clean runs after the copy, and `-race` is green across every package.

➕ **Three contract artefacts moved together for those two arguments, and none was regenerated.**
`docs/plans/contracts/recommend-v3-input.schema.json` (normative), the `recommendArgs` allow-list
and `internal/mcp/testdata/recommend-v3-input-schema.json` (golden). No `UPDATE_GOLDEN` run
anywhere in this task: the golden diff is the tool rename, the four annotation values and those two
properties, each reviewed line by line.
`TestRegisteredInputSchemaAgreesWithTheNormativeContract` asserts all three agree, so dropping one
fails a test rather than shipping.

➕ **`sort`, `order` and `live_risk` are deliberately absent from `recommend_spot_machines`, and
`cmd/spotinfo/mcp_vocabulary_test.go` records the difference as data.** A ranked page publishes
each row's rank, so a client that wants another order sorts the array it was handed — on the CLI
the flag reorders a *rendered* page, which MCP does not produce. `live_risk` has no task in this
plan that puts it on the MCP surface. The four score arguments carry the same "task 11" rows the
CLI `vocabularyGaps` already has. As there, the test asserts the difference **equals** the table,
so an argument that lands while its row stays fails as loudly as one that never lands.

➕ **`mcp.Server.Tools()` is exported for that test.** `mcpArgName` lives in package `main`, so the
derivation has to be asserted from the side that owns the vocabulary — against what the server
actually registered, not against a second list that could drift from it. Task 1's ➕ predicted the
test would live in `cmd/spotinfo`; this is the accessor that lets it read the surface.

➕ **The concurrency tests were retargeted, not deleted with their helpers.** `race_test.go` exists
for the shared state inside `spot.Client`, which is unchanged; each test now drives the handler
that replaced the one it used to drive. The benchmarks whose subject is genuinely gone — the v1
response builders, the reliability score, the interruption filter — went with it, and
`BenchmarkParseListRequest` and `BenchmarkListReport` took their place.

➕ **The e2e suite went from 4 failing top-level tests to 1 at this boundary.**
`TestE2EMCPServerCompletesAHandshakeAndAnswers` and `TestE2EMCPAnswersEveryCloudInOneSchema` now
pass against the shipped binary, and so does
`TestE2ETheCLIAndMCPAnswerTheSameQuestionIdentically` — which is what forced `offline` to be a real
argument, since it passes `"offline":true` to the recommend tool. Only
`TestE2ETheListCommandRendersEveryOutputFormat` still fails, on the `machine` and `risk` column
names the plan already records as a Task 7 target.

⚠️ **Four documents still name the retired tools, so Task 16's scope grew.** `docs/mcp-server.md`,
`docs/api-reference.md`, `docs/troubleshooting.md` and `docs/claude-desktop-setup.md` describe
`find_spot_instances`, `list_spot_regions` and `recommend_spot_instances`, their v1 response shape
and their arguments. `CLAUDE.md` lines 12 and 14 are wrong for the same reason. They were left
alone rather than half-rewritten here.

### Task 7: Regenerate every golden once and review the diff

**Files:**

- Modify: `cmd/spotinfo/testdata/*`, `internal/mcp/testdata/*`
- Rename: `cmd/spotinfo/contract_v1_test.go` → `cmd/spotinfo/contract_test.go`
- Modify: `cmd/spotinfo/contract_v2_test.go`

- [ ] replace `contractAdvices()` with a fixed `cloud.Provider` stub, so both the `list` and
      the `recommend` goldens stay independent of the embedded feeds and a weekly data-refresh
      PR cannot rewrite a contract
- [ ] delete the `aws-root-v1.*` goldens and record `list` goldens for all five formats
- [ ] record `recommend` v3 goldens from the same stub
- [ ] point `contract_v2_test.go` at the renamed v3 schema and keep it reading `MaxTop` from
      the schema, so raising one without the other still fails
- [ ] run `UPDATE_GOLDEN=1 go test ./cmd/spotinfo/ ./internal/mcp/` once, then re-run without
      it and confirm a pass
- [ ] review the full golden diff and confirm every change is intended
- [ ] confirm the Task 2 e2e suite now passes end to end
- [ ] run `make test lint verify-data verify-architecture` — the gate is the full suite from
      here on, with no skip

➕ **The fixed `cloud.Provider` stub must include a candidate with no price observation.** Every
existing helper prices every candidate — `contractAdvices()`, `testCandidate.build()` under
`if c.Price > 0`, the `cmd/spotinfo` stubs — which is exactly how the `""` price defect in Task 5
reached a commit. A stub that prices every row records five goldens over a shape that omits the
600 unpriced rows a real `--region all` answer carries, and freezes whatever the four rendered
formats print for them without anyone reviewing it. Decide what `table`, `text`, `csv` and
`number` show for an unpriced row **before** recording, not after: the JSON form now publishes
`null`, and a renderer printing `0.0000` beside it would be the same silent zero in a different
font.

---

## Phase 2 — Refuse what cannot act

### Task 8: Replace the surviving silent no-ops with capability refusals

Scoped to the refusals that **survive** the rest of the plan. `--offline` and `--refresh` are
deliberately excluded: Tasks 10 and 13 give them real behaviour on Azure and GCP, and writing
refusal tests here only to delete them there is churn.

**Files:**

- Modify: `cmd/spotinfo/provider_flags.go`
- Create: `cmd/spotinfo/refusals_test.go`

- [ ] refuse `--az`, `--min-score` and `--score-timeout` when `--with-score` is absent
- [ ] refuse `--with-score` on a cloud without `CapabilityPlacementScore`, with
      `UNSUPPORTED_CAPABILITY` — reuse the existing capability, do not add a new one
- [ ] on Azure, make the `--with-score` and `--live-risk` refusals name the reason: both
      exist as vendor APIs but need an Azure subscription, which this build does not
      authenticate to. A reader must be able to tell "not built" from "not published"
- [ ] refuse `--gcp-project` when the cloud is not GCP, matching how `--live-risk` is refused
- [ ] refuse `--offline` and `--refresh` **only** on a cloud without
      `CapabilityLiveEnrichment` (`internal/cloud/provider.go:29`, already declared by AWS);
      Tasks 10 and 13 set it for Azure and GCP, which retires the refusal without a test edit
- [ ] write the refusal test **driven by the registry, not by a hard-coded cloud list**: for
      each registered provider, assert `--offline` is refused **if and only if**
      `Capabilities().Has(CapabilityLiveEnrichment)` is false. A literal cloud table would have
      to be edited by Tasks 10 and 13, which is the churn this rescoping exists to avoid
- [ ] write a table-driven test for the remaining refusals: exit code, empty stdout, message
      names both the flag and the cloud
- [ ] run `make test` — must pass before Task 9

---

## Phase 3 — Azure meter defects and Windows, as one parser change

### Task 9: Fix the meter filter, key the catalogue by OS, and rebuild the snapshot

These were two tasks. They are one, because `internal/snapshot/source_contract.go:153` fails
`verify-data` when the manifest's `parser_version` differs from the contract's. Raising the
contract in one task and regenerating the manifest in the next leaves the repository unable to
pass its own gate in between, and the only two exits are hand-editing a manifest — which
`CLAUDE.md` forbids — or rebuilding, which is the next task. Invariant 4 says parser, contract,
manifest and `verify-data` move together.

The ordering that mattered survives as bullet order: shrink the contaminant surface **before**
widening the catalogue key. It never needed a task boundary, only a code-change boundary.

**Files:**

- Modify: `internal/providers/azure/prices.go`, `internal/providers/azure/catalog.go`
- Modify: `internal/providers/azure/prices_test.go`,
  `internal/providers/azure/catalog_test.go`
- Modify: `internal/providers/azure/provider.go`
- Modify: `internal/providers/azure/testdata/retail-page-1.json`
- Modify: `internal/providers/azure/data/source-contract.json`
- Modify: `internal/providers/azure/data/manifest.json`
- Modify: `internal/providers/azure/data/catalog.json.gz`
- Modify: `cmd/update-azure-data/main.go`

**First, shrink the contaminant surface:**

- [ ] change `windowsMarker` from `"Windows"` to `" Windows"` and test it with
      `strings.HasSuffix`, matching `classOf`'s `" Spot"` and `" Low Priority"` convention
- [ ] add a `Dedicated Host` exclusion in `observe()` beside Low Priority and Cloud Services
- [ ] add a fixture row reproducing the measured contaminant: `productName`
      `"FX Series Dedicated Host"`, `skuName` `"FXmds Type1 Spot"`, `armSkuName`
      `"FXmds Type1"`
- [ ] write a test asserting that row produces no observation, and that a plain Dedicated Host
      row produces no on-demand observation either
- [ ] write a test asserting a Linux row whose product name contains `Windows` other than as a
      suffix is kept

**Then, widen the key:**

- [ ] read the OS from the `productName` suffix, handling all three states: `" Windows"`,
      `" Linux"`, and no suffix meaning Linux. The leading space is load-bearing — that is
      why the marker is matched with `HasSuffix`, not `Contains`
- [ ] key `priceKey`, the catalogue rows and `verifyPrices` by (machine, region, **os**), so
      the "is priced twice" check at `catalog.go:383` still fires on a real duplicate
- [ ] make the spec join at `catalog.go:104` carry OS through, and **report** a priced machine
      dropped for a missing spec rather than skipping it with a silent `continue`
- [ ] write a test asserting one size in one region can hold both a Linux and a Windows price
- [ ] write a test asserting two rows with the same (machine, region, os) still fail as a
      duplicate
- [ ] write a test asserting a Windows price is never presented as a saving against a Linux
      price

**Then, declare and rebuild — all in this task:**

- [ ] name Dedicated Host in the excluded-rows section of the contract
- [ ] add `windows` to `support.operating_systems` and to the Azure `Capabilities()`
- [ ] raise `parser_version` in the parser **and** the contract **and** the manifest — all
      three, or `verify-data` fails on `ErrContractMismatch`
- [ ] raise `min_records.prices` to match the larger row count, and **raise
      `max_compressed_bytes` above 131072 if the rebuilt catalogue exceeds it** — the current
      archive is 88,272 bytes and Windows roughly doubles priced rows
- [ ] rebuild with `make update-azure-data`, which regenerates the manifest and the archive
      together; if the environment has no network, record a `⚠️` and stop rather than
      hand-editing either
- [ ] write a test asserting `--os windows --cloud azure` returns candidates and
      `--os windows --cloud gcp` still returns `UNSUPPORTED_CAPABILITY`
- [ ] run `make verify-data` and `make test` — must pass before Task 10

---

## Phase 5 — Live Azure prices

### Task 10: Add an anonymous live price path for Azure

**Files:**

- Create: `internal/providers/azure/liveprice.go`, `internal/providers/azure/liveprice_test.go`
- Modify: `internal/providers/azure/provider.go`

- [ ] fetch current prices from the anonymous Retail Prices API for the queried regions only
- [ ] reuse the existing feed cache, with a reviewed TTL recorded in `docs/data-sources.md`
- [ ] report the result as `live`, `cached` or `embedded-snapshot` through the existing
      `data_source.mode`, never claiming a recency nothing established
- [ ] set `CapabilityLiveEnrichment` true for Azure, which retires the Task 8 refusal for
      `--offline` and `--refresh` on Azure without editing that test
- [ ] fail soft: any live failure falls back to the snapshot and never turns into an error
- [ ] write tests against a stub transport covering success, a 500, a timeout and a malformed
      body
- [ ] write a test asserting `--offline` makes no request on Azure
- [ ] run `make test` — must pass before Task 11

---

## Phase 7 — Placement scores

### Task 11: Extend the placement observation with a kind, and put the flags on both commands

`PlacementObservation` already exists at `internal/cloud/observations.go:123` with a bare
`Score int`, is populated at `internal/providers/aws/provider.go:370` and rendered at
`internal/mcp/tools.go:404`. This task **extends** it. Do not create a parallel type.

**Files:**

- Modify: `internal/cloud/observations.go`, `internal/cloud/schema.go`
- Modify: `internal/cloud/enrich.go`, `internal/cloud/recommend.go`
- Modify: `internal/mcp/tools.go`
- Modify: `internal/providers/aws/provider.go` — `placements()` at line 370 builds every
  `PlacementObservation` and must set the new `Kind`
- Modify: `cmd/spotinfo/main.go` — declares the four score flags
- Modify: `cmd/spotinfo/recommend.go` — must build a `PlacementRequest` in `execRecommendCmd`;
  declaring the flags is not enough to make them act
- Modify: `internal/cloud/schema_test.go`

- [ ] add `Kind` plus an optional `Obtainability *float64` to the existing
      `PlacementObservation`, keeping `Score int` for the AWS path
- [ ] define **two** kinds, one per producer this plan builds: `placement_score` (AWS,
      integer 1-10) and `obtainability` (GCP, 0.0-1.0 with an optional uptime estimate).
      Azure's `High`/`Medium`/`Low` label is deferred with its fetcher — do not add a kind
      with no producer
- [ ] do **not** normalise the kinds into a shared scale; a common 1-10 would invent
      precision no vendor published
- [ ] decide and document how _absent_ differs from _unavailable_: `Placements` is a slice
      today, so both are the empty slice. Add an explicit status rather than overloading length
- [ ] add `--with-score`, `--min-score`, `--az` and `--score-timeout` to `recommend` — they are
      root-only today (`main.go:1138`) and `recommendCommand` declares none of them, so Task 12
      would otherwise have no CLI entry point
- [ ] make `--min-score` meaningful against both kinds, or refuse it against `obtainability`
      with a message naming the cloud — an integer floor over a 0.0-1.0 probability needs a
      stated mapping, not an implicit one
- [ ] write tests for each kind's rendering in table, JSON and CSV
- [ ] write a test asserting a placement kind is never accepted by `acceptsRisk`
- [ ] write a test asserting the AWS path still produces the same score it does today
- ➕ delete the four `vocabularyGaps` rows marked "task 11" in
  `cmd/spotinfo/vocabulary_test.go`
- [ ] run `make test` — must pass before Task 12

### Task 12: Fetch GCP obtainability

**Files:**

- Create: `internal/providers/gcp/placement.go`, `internal/providers/gcp/placement_test.go`
- Modify: `internal/providers/gcp/provider.go`

- [ ] call `compute.advice.capacity` (beta) with the ADC machinery already built for
      `--live-risk`, reusing the same lazy credential resolution
- [ ] respect the documented limit of 5 machine types per request
- [ ] carry `estimatedUptime` alongside `obtainability`
- [ ] mark the capability as beta in `Capabilities()` and say so in the help text
- [ ] write tests against a stub transport: success, over-limit batching, no credentials
- [ ] run `make test` — must pass before Task 13

---

## Phase 8 — GCP beyond us-central1

### Task 13: Widen GCP regions and prices behind an API key

**Files:**

- Create: `internal/providers/gcp/liveprice.go`, `internal/providers/gcp/liveprice_test.go`
- Modify: `internal/providers/gcp/provider.go`
- Modify: `cmd/spotinfo/provider_flags.go`

- [ ] read the Cloud Billing Catalog API with a key from `--gcp-billing-key` or an environment
      variable, for the queried regions only
- [ ] let a successful call widen `--region` past `us-central1` for that invocation only
- [ ] never write a catalogue price into the snapshot: Google states no redistribution terms
- [ ] without a key, keep answering `us-central1` from the snapshot and report `NO_CANDIDATES`
      for any other explicit region, exactly as today
- [ ] set `CapabilityLiveEnrichment` true for GCP once the key path exists, retiring the Task 8
      refusal for `--offline` and `--refresh` on GCP
- [ ] write tests against a stub transport: success, no key, a rejected key, and a region the
      API does not price
- [ ] write a test asserting the snapshot is unchanged after a live call
- ➕ declare `--gcp-billing-key` on both commands and delete its two `vocabularyGaps` rows in
  `cmd/spotinfo/vocabulary_test.go`
- [ ] run `make verify-data` and `make test` — must pass before Task 14

---

## Phase 9 — Close out

### Task 14: Record the three refusals as answered

**Files:**

- Modify: `internal/cloud/provider.go`
- Modify: `docs/clouds.md`
- Modify: `docs/reviews/multicloud-parity.md`
- Create: `internal/cloud/refusals_test.go`

- [ ] keep Windows on GCP, zone-level prices on both, and `--workload web|ci|batch` on both
      refused, each with a message naming the vendor limit rather than implying a missing
      feature
- [ ] write a test asserting `interruptionCappableKinds` holds exactly one kind, so a future
      change to it fails a test rather than a consumer
- [ ] do not duplicate the per-cloud refusal tests already written in Task 9; assert
      only the message wording here
- [ ] update the verdict table in `docs/reviews/multicloud-parity.md` with what shipped
- [ ] run `make test` — must pass before Task 15

### Task 15: Verify acceptance criteria

- [ ] verify every requirement in the Overview is implemented
- [ ] verify every invariant still holds, especially Invariants 1, 4 and 7
- [ ] confirm the Task 1 command-tree test is what proves criterion 3, and that it is running
- [ ] run the full suite: `make test`
- [ ] run the race detector: `make test-race`
- [ ] run `make lint`
- [ ] run `make verify-data` with `UPDATE_GOLDEN` and `REFRESH_MANIFESTS` unset
- [ ] run `make verify-architecture`
- [ ] measure an Azure recommendation payload and confirm provenance is under half of it

### Task 16: [Final] Update documentation

- [ ] rewrite `docs/usage.md` against the new vocabulary and both commands
- [ ] update `README.md`, `docs/quick-start.md`, `docs/clouds.md`, `docs/installation.md`,
      `docs/mcp-server.md`, `docs/api-reference.md`, `docs/examples.md`,
      `docs/troubleshooting.md`, `docs/claude-desktop-setup.md` and `llms.txt`
- [ ] verify every documented command by running it, as was done for the current docs
- [ ] add a migration table to the release notes: every removed flag and its replacement, and
      every renamed MCP tool
- [ ] update `CLAUDE.md` — it names the old MCP tools and the v1 golden rule in several places,
      and must carry the vocabulary rule, the unified defaults and the new invariants
- [ ] leave this plan in place — Phase 10 still has to run against the documented surface, so
      archiving it here would file it as done before anyone has run the binary

---

## Phase 10 — Validate the shipped binary, not the packages

Tasks 1 to 16 prove the code is right against mocks, stubs and goldens. Nothing so far proves
that the **binary a user downloads** answers correctly on three clouds. These two tasks close
that gap: Task 17 automates what a machine can check, Task 18 is the judgement a machine
cannot make.

**Why this is not already covered.** `make test` runs `e2e_test.go`, which is deliberately
network-free and drives a handful of invocations. It does not sweep the command x cloud x
format matrix, it never exercises a live path, and it cannot tell a well-formed answer from a
_correct_ one. A price that parses, validates against its schema and renders in five formats
can still be the wrong price.

### Task 17: Build the binary and validate the full command x cloud x format matrix

**Files:**

- Modify: `cmd/spotinfo/e2e_test.go`
- Create: `cmd/spotinfo/validate_clouds_test.go`
- Modify: `Makefile`
- Create: `docs/reviews/surface-validation.md`

**The split is load-bearing.** The offline matrix extends the e2e suite and runs in
`make test`. The live checks reach real vendor endpoints, so per the Safety notes they must
**not** be part of `make test`; they go in `validate_clouds_test.go` behind a build tag or an
explicit env guard, driven by a separate `make validate-clouds` target. A test that reaches a
real cloud inside the default suite is a broken test; a target a person runs on purpose is
not.

**Offline matrix, in `e2e_test.go`, network-free and credential-free:**

- [ ] build once via `make build` and drive the **real binary** as a subprocess, as the file
      already does — never an in-process `cli.App`
- [ ] sweep `{list, recommend}` x `{aws, gcp, azure}` x `{number, text, json, table, csv}` and
      assert for each cell: exit code 0, non-empty stdout, empty stderr, at least one candidate
      row. `number` is `list`-only, so assert `recommend --output number` is **refused**, not
      that it is skipped
- [ ] assert every `json` cell validates against its contract schema file — `spotinfo.list/v1`
      or `spotinfo.recommend/v3` — by reading the schema from `docs/plans/contracts/`, so a
      schema edit that outruns the code fails here
- [ ] assert the risk column on GCP and Azure prints a **status**, never a blank, a zero or an
      AWS-shaped bucket. This is Invariant 2 checked on rendered output rather than on a struct
- [ ] assert determinism: the same `--offline` invocation run twice produces byte-identical
      stdout. A non-deterministic map iteration in a renderer surfaces here and nowhere else
- [ ] assert the refusal matrix: every flag Task 8 refuses, on every cloud that refuses it,
      exits non-zero with **empty stdout** and a message naming both the flag and the cloud
- [ ] assert every removed flag name (`--type`, `--instance`, `--vcpu`, `--memory`,
      `--memory-gib`, `--cpu`, `--price`, `--budget`) prints a rename hint naming its
      replacement, exits non-zero, and prints nothing to stdout
- [ ] drive the MCP stdio surface end to end: handshake, `tools/list` returns exactly the three
      tool names, then call each of the three tools for each of the three clouds and assert a
      structured result. Pin `HTTP_PROXY`/`HTTPS_PROXY` at a closed port and pass `--offline`,
      as `CLAUDE.md` requires, so the fallback path is what is exercised
- [ ] assert CLI/MCP parity on rendered output, not just in-process: the same question through
      `spotinfo recommend --output json` and through `recommend_spot_machines` yields the same
      `request` echo, the same ranking policy and the same first candidate. This is acceptance
      criterion 2, checked against the binary

**Binary-level assertions, same task:**

- [ ] assert the shipped binary links **no Azure credential library**: `go version -m` on the
      built binary must show no `azidentity`, `armresourcegraph` or `armrecommender`. This is
      Invariant 8 and acceptance criterion 8, and it is the only check that catches a
      transitive pull
- [ ] record the binary size and compare it against the pre-plan baseline; flag a growth over
      15% for review rather than failing on it

**Live checks, in `validate_clouds_test.go`, behind `make validate-clouds`:**

- [ ] AWS with live feeds (anonymous): assert `list` and `recommend` answer, and that
      `data_source.mode` reports `live` or `cached`, never `embedded-snapshot`
- [ ] Azure with the anonymous Retail Prices API (Task 10): same assertions
- [ ] GCP from the snapshot, and — only if a key is present in the environment — the
      `--gcp-billing-key` path; **skip with a stated reason when the key is absent**, never fail
- [ ] assert every live path **degrades to the snapshot** rather than failing the run: point
      each at a closed port and assert exit 0 with an answer and a `data_source.mode` of
      `embedded-snapshot`. This is the Safety note "never let a live path fail a run", checked
      rather than assumed
- [ ] `make validate-clouds` must be absent from `make test` and from every CI workflow that
      gates a merge — assert that by grepping the Makefile and `.github/workflows/`

**Close out:**

- [ ] run `make build && make test && make validate-clouds`
- [ ] run the e2e suite explicitly with no skip: `go test ./cmd/spotinfo/ -run E2E -v`, and
      confirm the test count is **non-zero** — a vacuous pass against zero collected tests is
      the exact failure the `TestE2E` infix rule exists to prevent
- [ ] write `docs/reviews/surface-validation.md` recording the matrix, what passed, and every
      cell that was skipped and why

### Task 18: Manual correctness and usability review

A person runs the binary and judges it. Findings go in a document; defects that are real get
fixed in this task, not deferred.

**Files:**

- Create: `docs/reviews/manual-validation.md`
- Modify: whatever the findings require

**Correctness — is the answer _right_, not merely well-formed:**

- [ ] spot-check three AWS instance types in three regions against the AWS Spot pricing page,
      and confirm the savings percent and interruption range agree with the Spot Advisor
- [ ] spot-check three Azure sizes in three regions against the Azure pricing calculator,
      **including one Windows price**, and confirm a Windows price is never presented as a
      saving against a Linux price
- [ ] spot-check three GCP machine types against Google's Spot pricing page
- [ ] confirm a cloud with no interruption data never renders a number in the risk column
- [ ] confirm `--workload web|ci|batch` still refuses GCP and Azure with a message naming the
      vendor limit, and that `--live-risk` on GCP makes the preemption rate **visible but not
      filterable** — Invariant 1 observed from the outside

**Usability — the part no test asserts:**

- [ ] run `spotinfo`, `spotinfo --help`, `spotinfo list --help`, `spotinfo recommend --help`
      and judge whether a first-time reader can tell the two commands apart without the plan.
      The discriminator is that `recommend` requires `--architecture`, `--min-vcpu` and
      `--min-memory-gib`; if the help text does not make that obvious, fix the help text
- [ ] confirm the `--region all` default is discoverable and its cost is stated, with the
      pointer to `--offline` or an explicit `--region` the plan requires
- [ ] trigger every error path by hand — unknown cloud, unknown region, unknown format, a
      removed flag, a refused capability, a filter matching nothing — and judge each message
      on whether it says **what to do next**. `NO_CANDIDATES` with no hint is a finding
- [ ] time a cold `spotinfo list --cloud aws` and the same call with `--offline`; if the
      default is slow enough to read as broken, that is a usability finding
- [ ] check the rename hints name the replacement flag, not just the removal
- [ ] drive the MCP surface from a real client (Claude Desktop or `mcp` CLI) and confirm the
      three tools are discoverable and their descriptions say which clouds each supports

**Close out:**

- [ ] record every finding in `docs/reviews/manual-validation.md` with a severity and a verdict
- [ ] fix every correctness finding and every high-severity usability finding **in this task**
- [ ] re-run `make test && make lint && make validate-clouds` after the fixes
- [ ] list anything deliberately not fixed, with the reason
- [ ] move this plan to `docs/plans/completed/` — this is the last step of the last task

## Acceptance criteria

1. One schema family. No response anywhere carries `spotinfo.recommend/v1`.
2. The same question asked over the CLI and over MCP returns the same `request` echo, the same
   ranking policy and the same first result.
3. Every concept has exactly one flag name, and every MCP argument name is derived from its
   CLI flag by the stated rule — proved by the Task 1 command-tree test, not by review.
4. No flag is accepted and ignored. Every unusable flag is refused, naming the flag and the
   cloud, or has been given real behaviour.
5. `spotinfo list` and `spotinfo recommend` both answer on AWS, GCP and Azure, and
   `spotinfo --mcp` and `spotinfo --version` still work.
6. Azure serves Linux and Windows. GCP still refuses Windows, with a message naming the reason.
7. Azure prices can be fetched live and anonymously; `--offline` and `--refresh` act on Azure.
8. GCP obtainability is available behind an explicit opt-in flag and never enters a
   snapshot. Azure eviction rate and Azure placement scores are **not** built, and the
   binary links no Azure credential library.
9. `interruptionCappableKinds` still holds exactly `[RiskKindInterruptionFrequencyRange]`.
10. Provenance is less than half of an Azure recommendation payload, and every retained source
    is referenced by at least one candidate.
11. `make test`, `make test-race`, `make lint`, `make verify-data` and
    `make verify-architecture` all pass.
12. The **built binary** answers on the full `{list, recommend}` x `{aws, gcp, azure}` x
    `{number, text, json, table, csv}` matrix, every `json` cell validates against its contract
    schema, the same `--offline` invocation twice is byte-identical, and `go version -m` on
    that binary shows no Azure credential library.
13. A person has run the binary against all three clouds, spot-checked prices against each
    vendor's own page, judged the help and error text, and recorded the result in
    `docs/reviews/manual-validation.md`. Every correctness finding is fixed.

## Safety notes

- **Never lower a threshold to make a refresh pass.** The Azure contract carries a coverage
  floor, a size cap and a precision cap. Raising `max_compressed_bytes` in Task 9 is a
  reviewed change to a cap that a larger catalogue legitimately outgrows — it is not the same
  as lowering a floor.
- **Never widen a parser to make a changed source fit.** Review the source, then raise
  `parser_version` in both the parser and the contract, in the same task.
- **Never add a new risk or placement kind to `interruptionCappableKinds`.**
- **Never pass a cancellable context to a credential helper that stores it in a token source.**
  A `defer cancel()` in a constructor makes the first real call fail with `context canceled`.
- **Never read an ambient project or subscription** from `gcloud` or `az`. The call is billed
  to whatever it names.
- **Never let a live path fail a run.** Every one of them degrades to the snapshot.
- Phases 5 to 8 add network paths. Keep every test on a stub transport; a test that reaches a
  real cloud is a broken test.
- Task 9 runs `make update-azure-data`, which needs network. In an offline environment,
  record a `⚠️` and stop; never hand-edit a snapshot.

## Post-Completion

_External work only. No checkboxes — these need a credential this repository does not hold._

**Task 18 now owns every manual check that needs no credential**, including the Azure Windows
price spot-check. What remains here is what a maintainer can only do with an account:

- `--with-score` on GCP with real Application Default Credentials, including the over-limit
  batching path from Task 12
- `--gcp-billing-key` against a real key, and against a key with the Billing API disabled
- Nothing on Azure requires a credential in this plan. The two deferred Azure features —
  eviction rate and placement score — are **not built**, so there is nothing to verify; if a
  subscription ever appears, `docs/reviews/multicloud-parity.md` §3 has the query and the
  reason neither may join `interruptionCappableKinds`

**Release mechanics:**

- Tag a new major version; the CLI surface, both schemas and all three MCP tool names change
- Publish the migration table from Task 16 in the release notes
- Update any published MCP client configuration that names the old tool names
- Confirm the weekly data workflows still pass against the raised thresholds
