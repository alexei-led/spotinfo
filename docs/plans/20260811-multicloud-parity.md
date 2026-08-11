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
is a neutral risk observation once the command is cloud-neutral. **Landed in Task 7**, which
renamed the two columns and the two `text` keys and re-recorded the goldens; all 17 tests pass
at that boundary.

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
test asserts, and deliberately so — the margin comes from trimming _both_ scopes. Region-only or
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
price at all, so _every_ AWS row already carries a discount without its denominator, and the
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
goldens** — and did: the flag-gated load stands and the enum keeps `""`, because a lookup leaves
an unclassified instance type empty as well, so the best-effort load would not have removed the
value. The reasoning is under Task 7; nothing empty reached a golden.

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
the flag reorders a _rendered_ page, which MCP does not produce. `live_risk` has no task in this
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

- [x] replace `contractAdvices()` with a fixed `cloud.Provider` stub, so both the `list` and
      the `recommend` goldens stay independent of the embedded feeds and a weekly data-refresh
      PR cannot rewrite a contract
- [x] delete the `aws-root-v1.*` goldens and record `list` goldens for all five formats
- [x] record `recommend` v3 goldens from the same stub
- [x] point `contract_v2_test.go` at the renamed v3 schema and keep it reading `MaxTop` from
      the schema, so raising one without the other still fails
- [x] run `UPDATE_GOLDEN=1 go test ./cmd/spotinfo/ ./internal/mcp/` once, then re-run without
      it and confirm a pass
- [x] review the full golden diff and confirm every change is intended
- [x] confirm the Task 2 e2e suite now passes end to end
- [x] run `make test lint verify-data verify-architecture` — the gate is the full suite from
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

➕ **The unpriced row prints `-`, in all four rendered formats and in the CSV `Price Source`
column with it.** `humanPrice` and `savingsDisplay` on the ranked page already print `-` for an
absent amount, so this is an in-repo convention rather than a new glyph. `priceSource` took a
`bool` and answered "static" for a row with no price at all, which names the feed a price was
read from for a price that was never read; it takes the candidate now and has three states. The
CSV price column is deliberately mixed — a float where there is a number, `-` where there is
not — because a parser reading `0` there would read a price of zero. `number` is unchanged: it
prints a savings percent, not a price, and the e2e suite requires it to stay a bare integer.

➕ **The goldens are recorded from an AWS-id stub through `answerList`, and that split is what
made a fixed provider reachable.** `resolveListProvider` sends AWS to `awsQueryProvider(client,
…)` rather than to the registry — Task 3's guard, and `TestListDoesNotBuildTheAWSProviderThroughTheRegistry`
pins it — so a registry stub cannot answer `spotinfo list --cloud aws`. Recording through the
production adapter instead is what this checkbox forbids: `awsprovider.New` reads
`spot.EmbeddedSourceRefs()`, so the JSON golden would carry the committed manifest hashes and
`fetched_at`, and every weekly data-refresh PR would rewrite it. `execListCmd` is therefore split
into "resolve the provider" and `answerList(ctx, execCtx, provider, sortKey, output)`, which the
golden test drives with the stub after the production app has parsed the flags. The stub keeps
the **aws** identifier: a GCP- or Azure-id fixture carrying zone prices and placement scores
would contradict two refusals this plan documents as permanent.

➕ **`list` renames two columns and two `text` keys, which is the Task 2 target the suite was
still red on.** `Instance Info` → `Machine` and `Frequency of interruption` → `Risk` in `table`
and `csv`; `type=` → `machine=` and `interruption=` → `risk=` in `text`. Both retired words name
concepts this release retired — `--type` became `--machine`, and the column is a neutral risk
observation on a cloud-neutral command.

➕ **The recorded fixture is five candidates and covers every rendering branch**: a static price,
a live one (the `*` suffix and the `live` price source), a regional placement score with no
timestamp, a pair of zonal scores with their own zone prices and a fixed 2026-08-06 `FetchedAt`
— fixed and in the past on purpose, because `addFreshnessInfo` marks anything older than thirty
minutes and a fixture stamped near "now" would make the goldens flap — and the unpriced
`me-south-1` row. The same stub answers `recommend`, where the ranked page drops two of the five:
`t3.nano` for the memory floor and the unpriced row because `accepts` refuses a candidate with no
price. That asymmetry is now visible in the recorded contract rather than only in prose.

⚠️ → **decided: AWS `list` candidates keep `architecture: ""` when `--architecture` is unset, and
`list-v1.schema.json` keeps admitting it.** Task 5 left this for Task 7. The alternative — loading
the lookup best-effort in `awsQueryProvider` — does **not** let `""` leave the enum, because
`provider.go:275` leaves an instance type the snapshot does not classify unclassified even with a
lookup; it would also make the default browse answer depend on the embedded architecture snapshot,
which is the feed dependence this task exists to remove, and it costs
`TestTheQueryProviderDeclaresNoArchitecture` for no schema gain. The recorded fixture states
`x86_64` explicitly, so nothing about the empty shape is frozen into a golden.

⚠️ → **superseded, and fixed: that note answered the schema question and left a surface
divergence behind it.** Whether `""` can leave the enum is a different question from whether both
surfaces publish the same value, and only the first was examined. Measured on the built binary:
`spotinfo list --cloud aws --machine '^m5.large$' --offline --output json` and the
`list_spot_machines` tool call with the same arguments returned the identical 29-row set with an
identical `request` echo, and `architecture` was `""` on every CLI row and `x86_64` on every MCP
row — because `list` loaded the lookup only when `--architecture` asked for it while the registry
the MCP surface resolves through always loads it. That is acceptance criterion 2, and
`internal/mcp/tools.go:121` claims the opposite in prose.

The fix is one constructor, `newAWSProvider(client)` in `cmd/spotinfo/main.go`, used by both the
registry factory and `list`. The schema decision above still stands — `""` stays in the enum,
because an instance type the snapshot does not classify is still unclassified — and the request
echo still reports an unset filter as `""`, which is the one place the empty string is right.
The capability gate for AWS `list` moved onto the built provider's own declaration with it: the
static package declaration let a request that filters by architecture past a broken snapshot, to
fail inside `Query` with `INVALID_ARGUMENT` after acquisition where MCP refused it with
`UNSUPPORTED_CAPABILITY` before it. `TestTheQueryProviderDeclaresNoArchitecture` was the
assertion that encoded the defect; it is replaced by its inverse rather than deleted.

- [x] one AWS constructor shared by the registry factory and `list`
- [x] `TestListPublishesTheAWSArchitectureWithoutBeingAsked` — in-gate, verified failing on the
      pre-fix tree
- [x] `TestE2ETheCLIAndMCPAnswerTheSameListQuestionIdentically` — the `list` sibling of the
      `recommend` parity test the finding named as missing, also verified failing pre-fix. It
      aligns rows by `(region, machine)`: without `--sort` the order is the provider's, and AWS
      advice comes out of a map, so the two surfaces enumerate the same rows in different orders.
      That is not a contract either surface publishes; the row set and every field on it is.

➕ **`internal/mcp/testdata/` came out of the regeneration byte-identical** — same three md5s,
empty `git diff`. That is the check on Tasks 5 and 6, which claim those goldens were hand-edited
rather than regenerated: had either edit been wrong, the one permitted `UPDATE_GOLDEN=1` run
would have shown it here.

⚠️ **Two documents are wrong as of this commit, and Task 16 owns both.** `docs/usage.md:235`
prints the retired CSV header row (`Instance Info,…,Frequency of interruption,…`) and
`docs/usage.md:248` the retired `text` keys (`type=…, interruption='…'`); both are now sample
output no build produces. `CLAUDE.md:459-460` names `cmd/spotinfo/testdata/aws-*-v1.*` as the
golden-pinned AWS contract, and that path no longer exists — the recorded set is
`cmd/spotinfo/testdata/{list-v1,recommend-v3}.*`, and its `internal/mcp` sibling went in Task 6.
Left alone rather than half-rewritten here. (`docs/reviews/spotinfo-multicloud-v2-architecture-review.md:156`
also names a deleted golden, but it records a past verification and is correct as history.)

---

## Phase 2 — Refuse what cannot act

### Task 8: Replace the surviving silent no-ops with capability refusals

Scoped to the refusals that **survive** the rest of the plan. `--offline` and `--refresh` are
deliberately excluded: Tasks 10 and 13 give them real behaviour on Azure and GCP, and writing
refusal tests here only to delete them there is churn.

**Files:**

- Modify: `cmd/spotinfo/provider_flags.go`
- Create: `cmd/spotinfo/refusals_test.go`

- [x] refuse `--az`, `--min-score` and `--score-timeout` when `--with-score` is absent
- [x] refuse `--with-score` on a cloud without `CapabilityPlacementScore`, with
      `UNSUPPORTED_CAPABILITY` — reuse the existing capability, do not add a new one
- [x] on Azure, make the `--with-score` and `--live-risk` refusals name the reason: both
      exist as vendor APIs but need an Azure subscription, which this build does not
      authenticate to. A reader must be able to tell "not built" from "not published"
- [x] refuse `--gcp-project` when the cloud is not GCP, matching how `--live-risk` is refused
- [x] refuse `--offline` and `--refresh` **only** on a cloud without
      `CapabilityLiveEnrichment` (`internal/cloud/provider.go:29`, already declared by AWS);
      Tasks 10 and 13 set it for Azure and GCP, which retires the refusal without a test edit
- [x] write the refusal test **driven by the registry, not by a hard-coded cloud list**: for
      each registered provider, assert `--offline` is refused **if and only if**
      `Capabilities().Has(CapabilityLiveEnrichment)` is false. A literal cloud table would have
      to be edited by Tasks 10 and 13, which is the churn this rescoping exists to avoid
- [x] write a table-driven test for the remaining refusals: exit code, empty stdout, message
      names both the flag and the cloud
- [x] run `make test` — must pass before Task 9

➕ **`--gcp-project` lost its urfave/cli `EnvVars`, and that is what makes the refusal safe.**
Measured before the change: with `GOOGLE_CLOUD_PROJECT` exported, `ctx.IsSet(--gcp-project)`
and `LocalFlagNames` both report the flag as **set** on a bare `spotinfo list` — urfave/cli
records an env-provided value as `HasBeenSet`. Refusing a set `--gcp-project` off GCP under
that declaration would refuse every default AWS invocation on any machine that has ever
touched GCP. The variable is still read, by the `os.Getenv` fallback `withLiveRisk` already
had, so the env path is unchanged and `TestLiveRiskRefusesABadProjectFromTheEnvironment` still
proves it reaches `ValidateProjectID`; the flag's `Usage` names the variable in place of the
`[$GOOGLE_CLOUD_PROJECT]` suffix urfave/cli printed. `TestAnExportedProjectVariableDoesNotRefuse
AnotherCloudsQuery` is the guard against re-adding it.

➕ **The capability gate runs before the companion check, deliberately.** On a cloud that
publishes no placement score, `--min-score 5` reports "gcp publishes no placement_score" rather
than "--min-score needs --with-score": adding `--with-score` there would not help, so the
capability is the answer that does. `refuseUnsupportedFlags` therefore sits in
`resolveListProvider` beside `Capabilities.Require`, and `requireWithScore` in
`validateListFlags`, which runs after it. Both are still before acquisition — Invariant 3.
The two existing rows of `TestUnsupportedCapabilitiesFailBeforeAcquisition` that drive
`--min-score` and `--az` on a score-free GCP pin that order.

➕ **The companion refusals are declared on `list` only, because `recommend` declares none of
the four score flags yet.** Task 11 lands them there; its ➕ line below records that it must
call `requireWithScore` too, or `--az` arrives on `recommend` silently ignored — the exact
defect this task exists to remove. No dead call was added here for it.

➕ **Two refusal classes, and the test separates them.** A capability refusal is about one
cloud and names it. A companion refusal is a flag-combination error refused identically
everywhere, so naming a cloud there would imply another cloud accepts it; `namesCloud` in
`cmd/spotinfo/refusals_test.go` carries the distinction as data. Three rows were also added to
the e2e table — `list --az`, `list --gcp-project`, `list --cloud azure --with-score` — which is
where the real process exit code and the empty stdout are observed. No `--offline` row: that
refusal is retired by Tasks 10 and 13.

⚠️ **`internal/mcp` still accepts `az` and `score_timeout` without `with_score`.**
`requestedPlacement` (`internal/mcp/tools.go:394`) refuses `min_score` without `with_score` and
ignores the other two: `az` sets `SingleZone` on a request with `Enabled: false`, and
`score_timeout` is dropped. That is the same silent no-op on the other surface, and this task's
file list is CLI-only, so it is recorded rather than fixed. **Task 15 must check it** when it
verifies acceptance criterion 2 — the CLI now refuses what MCP answers. The MCP `offline` and
`refresh` arguments diverge the same way on a snapshot-only cloud — `Query.CapabilityNeeds()`
never asks for `CapabilityLiveEnrichment` — but that half self-heals at Tasks 10 and 13, which
stop the CLI refusing them at all.

⚠️ **One documentation sentence is now wrong, and Task 16 owns it.**
`docs/quick-start.md:122` reads "It applies to AWS. GCP and Azure are always offline", which
describes `--offline` on those two clouds as redundant where it is now refused. Every other
`--offline` / `--refresh` mention is already AWS-scoped and stays true: `docs/clouds.md:57-58`
sits under `## AWS`, and `docs/data-sources.md:329` sits inside the AWS fetch-order list whose
own last bullet says GCP and Azure have no live path. `--gcp-project` needs no doc change —
`docs/installation.md:136`, `docs/clouds.md:113` and `docs/troubleshooting.md:465` all describe
`GOOGLE_CLOUD_PROJECT` as a source for the project, which `withLiveRisk` still reads. The
retired `spotinfo --type …` examples throughout `README.md`, `docs/usage.md`,
`docs/examples.md` and `docs/aws-spot-placement-scores.md` are a Task 4/5/7 debt, not this
task's: audited here, no new breakage found.

➕ **Files this task's list omits.** `cmd/spotinfo/list.go` (the refusal call site and the
companion check moved out of `validateScoreFloor`), `cmd/spotinfo/recommend.go` (the same call
site), `cmd/spotinfo/liverisk.go` (the Azure live-risk wording and the `EnvVars` removal) and
`cmd/spotinfo/e2e_test.go` (three refusal rows, where the process exit code is observed).

➕ **`--score-timeout` is unbounded on the CLI where MCP bounds it.** `internal/mcp` rejects a
value outside 1..`cloud.MaxScoreTimeoutSeconds`; `listQuery` applies `if timeout > 0` and drops
anything else without a word, so `--score-timeout -5` and `--score-timeout 99999` are both
accepted. Same no-op class, no checkbox here; worth folding into Task 11, which is already
changing how the score flags act.

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

- [x] change `windowsMarker` from `"Windows"` to `" Windows"` and test it with
      `strings.HasSuffix`, matching `classOf`'s `" Spot"` and `" Low Priority"` convention
- [x] add a `Dedicated Host` exclusion in `observe()` beside Low Priority and Cloud Services
- [x] add a fixture row reproducing the measured contaminant: `productName`
      `"FX Series Dedicated Host"`, `skuName` `"FXmds Type1 Spot"`, `armSkuName`
      `"FXmds Type1"`
- [x] write a test asserting that row produces no observation, and that a plain Dedicated Host
      row produces no on-demand observation either
- [x] write a test asserting a Linux row whose product name contains `Windows` other than as a
      suffix is kept

**Then, widen the key:**

- [x] read the OS from the `productName` suffix, handling all three states: `" Windows"`,
      `" Linux"`, and no suffix meaning Linux. The leading space is load-bearing — that is
      why the marker is matched with `HasSuffix`, not `Contains`
- [x] key `priceKey`, the catalogue rows and `verifyPrices` by (machine, region, **os**), so
      the "is priced twice" check at `catalog.go:383` still fires on a real duplicate
- [x] make the spec join at `catalog.go:104` carry OS through, and **report** a priced machine
      dropped for a missing spec rather than skipping it with a silent `continue`
- [x] write a test asserting one size in one region can hold both a Linux and a Windows price
- [x] write a test asserting two rows with the same (machine, region, os) still fail as a
      duplicate
- [x] write a test asserting a Windows price is never presented as a saving against a Linux
      price

**Then, declare and rebuild — all in this task:**

- [x] name Dedicated Host in the excluded-rows section of the contract
- [x] add `windows` to `support.operating_systems` and to the Azure `Capabilities()`
- [x] raise `parser_version` in the parser **and** the contract **and** the manifest — all
      three, or `verify-data` fails on `ErrContractMismatch`
- [x] raise `min_records.prices` to match the larger row count, and **raise
      `max_compressed_bytes` above 131072 if the rebuilt catalogue exceeds it** — the current
      archive is 88,272 bytes and Windows roughly doubles priced rows
- [x] rebuild with `make update-azure-data`, which regenerates the manifest and the archive
      together; if the environment has no network, record a `⚠️` and stop rather than
      hand-editing either
- [x] write a test asserting `--os windows --cloud azure` returns candidates and
      `--os windows --cloud gcp` still returns `UNSUPPORTED_CAPABILITY`
- [x] run `make verify-data` and `make test` — must pass before Task 10

➕ **`CatalogSchemaVersion` moved to `spotinfo.azure-catalog/v2` beside the parser bump.**
Every price row gained an `os` field and the catalogue's single top-level `os` became
`operating_systems`, so the committed shape changed. `DecodeCatalog` uses
`DisallowUnknownFields`, so without the bump a v1 archive fails with `unknown field "os"`
rather than a version mismatch — a worse error for the same condition. The manifest's
`data_schema_version` follows it, regenerated by the updater.

➕ **`Capabilities()` reads the catalogue instead of declaring an OS list.** `windows` is
in the support matrix, but the provider offers exactly what the committed rows price:
`publishedOperatingSystems` collects the OS of every published row, and `Capabilities()`
clones it. A refresh that stopped producing Windows rows would stop offering Windows in the
same run rather than refusing every query after acquisition.

➕ **`Catalog.Verify` now checks the operating systems both ways**, the way it already did
for series and architectures: every declared OS must be approved, and every approved OS must
appear. The second half is what makes the undocumented `" Windows"` suffix load-bearing —
a rename that emptied the Windows half fails the refresh instead of shipping a Linux-only
catalogue that still calls itself complete. The other defence is the widened key itself: a
Windows meter that lost its suffix lands on the Linux key at a different amount, and
`SelectCurrent` fails on `ErrAmbiguousPrice` rather than publishing either.

➕ **`BuildCatalog` returns a `BuildReport{Unpaired, Unspecified}` instead of one
`[]string`.** "Report a priced machine dropped for a missing spec" needed a second channel;
overloading `unpaired` would have made two different failures indistinguishable in the
updater's stderr.

➕ **The cap raise is measured, not anticipated.** The first rebuild ran to completion and
refused at `payload is 209979 bytes, contract allows 131072`, leaving the committed snapshot
untouched. `max_compressed_bytes` then moved 131072 → **262144**, the next power of two
above the observed payload; the rebuilt archive sits at 80% of it. `min_records.prices` moved
19,800 → **39,600**, which is the same formula the old floor was
(`min_regions` × `min_machines` × 2 classes) with the OS dimension added; the rebuild
carries 43,312 records. Raised before the rebuild, so `coverageFloor` reads it off the manifest
and one sweep did the job. No other threshold moved.

➕ **Rebuilt snapshot: 224 sizes, 26 series, 55 regions, 21,656 priced rows — 11,204 Linux
and 10,452 Windows — 209,979 compressed bytes.** The Linux row count is unchanged from the
previous snapshot, which is the check that the contaminant work cost no coverage. 196 of the
224 sizes carry a Windows meter; the 28 that do not are Arm. Four regions were swept by hand
before any code changed, and no `(machine, region, os)` key in them carried two different
prices — the failure that would have made this task a no-go.

➕ **The per-region floor now counts distinct machines, because keying by OS silently
weakened it.** `verifyRegions` compared `len(region.Prices)` against `min_machines`, and each
machine used to be exactly one row, so the two numbers were the same. Windows roughly doubled
the row count — the thinnest region, `mexicocentral`, went from 184 rows against a floor of
180 to 360 — which would have let a region lose half its Linux sizes and still pass a number
chosen against the old shape. `verifyPrices` now returns the distinct machine count and the
floor is applied to that, restoring the old margin exactly (184 against 180).
`TestTheRegionFloorCountsMachinesRatherThanRows` is the discriminator: a region whose row
count clears the floor while its machine count does not. No threshold moved, and the check
runs against the committed catalogue, so `make verify-data` re-proves it without a rebuild.

➕ **Four docs asserted the refusal this task removes, and were corrected here rather than
left for Task 16.** `docs/clouds.md` (matrix cell and the Azure section), `docs/usage.md`
(summary table and the refusal sentence), `docs/data-sources.md` (OS coverage, snapshot
figures, the Dedicated Host quirk) and `docs/troubleshooting.md` (a whole section for an error
that can no longer occur). `docs/research/multicloud-source-contracts.md` gained a §5b
recording the v2 decision with its measured numbers. Task 16 still owns the rewrite; this is
only the set of sentences the commit made false.

---

## Phase 5 — Live Azure prices

### Task 10: Add an anonymous live price path for Azure

**Files:**

- Create: `internal/providers/azure/liveprice.go`, `internal/providers/azure/liveprice_test.go`
- Modify: `internal/providers/azure/provider.go`

- [x] fetch current prices from the anonymous Retail Prices API for the queried regions only
- [x] reuse the existing feed cache, with a reviewed TTL recorded in `docs/data-sources.md`
- [x] report the result as `live`, `cached` or `embedded-snapshot` through the existing
      `data_source.mode`, never claiming a recency nothing established
- [x] set `CapabilityLiveEnrichment` true for Azure, which retires the Task 8 refusal for
      `--offline` and `--refresh` on Azure without editing that test
- [x] fail soft: any live failure falls back to the snapshot and never turns into an error
- [x] write tests against a stub transport covering success, a 500, a timeout and a malformed
      body
- [x] write a test asserting `--offline` makes no request on Azure
- [x] run `make test` — must pass before Task 11

➕ **"The queried regions only" needed a bound, and the bound is measured.** Sweeping one
region against the live API on 2026-08-11 is **9,022 meters over 10 pages and 5.5 MB, in 6.9
seconds**; `--region all`, which is the default, is 55 of those — roughly six minutes and 300
MB. The whole enrichment therefore gets the 20-second budget the GCP live-risk path already
spends on an optional extra, and at ~7 s a region that admits **two**. A query naming more, or
naming `all`, or naming none, is answered from the committed snapshot with one Debug line.
Regions are swept **sequentially**: the API advertises a rate-limit policy header
(`x-ms-ratelimit-retailprices-retry-after: 60`), and forty concurrent requests would make
fail-soft the normal case rather than the exception. A queried region the contract does not
cover is skipped rather than fetched — the live path refreshes approved coverage and must never
widen it, which also keeps the cache file name out of a caller's hands.

➕ **The overlay is all-or-nothing, and that is what keeps `mode` honest.** A region that
fails for any reason discards the whole overlay, not its own share of it: a half-live answer
has no single freshness to report. `mode` is `live` only when **every** queried region was
fetched this run and `cached` as soon as one came from an unrevalidated entry — verified
against the real API, where a second region fetched beside a cached one reports `cached`.

➕ **There is no 304 path here, and the TTL follows from that.** The Retail API serves
`cache-control: no-cache` with **neither an `ETag` nor a `Last-Modified`**, so
`CLAUDE.md`'s "a copy the origin confirmed with a 304 _is_ `live`" cannot apply — an expired
entry is re-downloaded in full, and a cached one was never confirmed. The window is **24 h**,
reasoned the way the AWS advisor feed's is: of the 9,022 rows in that sweep, **8,896 carry an
`effectiveStartDate` on the first of a month and 126 do not**, so the API publishes price
_intervals_ whose boundaries land on a monthly cadence. A document that turns over monthly,
costs 5.5 MB and ten round trips to read, and cannot be cheaply asked whether it moved, gets one
day of staleness against a roughly thirty-day cadence. Cached entries are ~580 KB per region.

➕ **The feed cache moved to `internal/feedcache`, which is what "reuse" had to mean.** It was
unexported inside `internal/spot`, and an Azure provider importing the legacy AWS package for a
generic on-disk cache is the wrong dependency even though the import policy permits it. The
mechanics moved verbatim with exported names; `internal/spot/cache.go` keeps only the two AWS
time-to-live values and the reasoning for why they differ, and the generic cache tests moved
with the code. The operator contract is unchanged and now shared: same
`os.UserCacheDir()/spotinfo`, same `SPOTINFO_CACHE_DIR`, same `SPOTINFO_CACHE=off`, every
failure still best-effort. `.archfit.yaml` gained the module at the `domain` layer;
`make verify-architecture` passes with no new Critical or High finding.

➕ **The live sweep reuses the snapshot's own parser and gates rather than a second copy.**
`RetailRequestURL`, `AcceptRows`, `SelectCurrent` and `buildRegions` are the build-time
functions; the rows are narrowed to the reviewed sizes **before** they are parsed, for the
reason `RetainSpecified`'s comment already gives — a region prices a few thousand sizes and an
anomaly in one this binary never publishes must not cost a caller the sizes it does. The one
threshold the sweep is held to is the contract's own per-region `min_machines`, reused, not
re-invented: a sweep that comes back thinner is refused whole. `cmd/update-azure-data`'s
`priceAPIBase` moved to `azure.RetailPriceBase` so the weekly refresh and the live path cannot
read two different "contracted" endpoints.

➕ **A live region republishes its own provenance.** Same URL — a sweep issues the exact
request the manifest recorded — with the `content_sha256` and `fetched_at` of the document
actually read. Publishing the snapshot's hash beside a price fetched this run would have made
`content_sha256` unverifiable, which Invariant 7 forbids.

➕ **Verified against the shipped binary, not only against the stub.**
`spotinfo list --cloud azure --region westeurope --machine '^Standard_D2s_v5$' --output json`
answers `mode: live` in 4.4 s with a fresh hash and `sources_omitted: 79`; the same query again
is `cached`; `--offline` is `embedded-snapshot` with no request; `--refresh` is `live` again;
`--region all` and three regions are both `embedded-snapshot`.

➕ **Files this task's list omits.** `internal/feedcache/{cache,cache_test}.go` (new),
`internal/spot/{cache,cache_test,data,cache_flow_test}.go`, `.archfit.yaml`,
`internal/providers/azure/{prices,provider_test}.go`, `cmd/update-azure-data/{main,main_test}.go`,
`cmd/spotinfo/{main,list}.go` (`newProviderRegistryFor` and `fetchPolicy` — the delegating
overload meant Task 8's `refusals_test.go` needed no change for the capability; the fix pass
below touched only a comment in it, never an assertion),
`cmd/spotinfo/{multicloud_test,refusals_test}.go` (fix pass), and
`docs/{data-sources,clouds,quick-start}.md`.

➕ **One `internal/providers/azure/provider_test.go` assertion changed, and it is not the Task
8 test.** `TestCapabilitiesNeverClaimRisk` asserted `LiveEnrichment` was false, which is the
capability this task lands. Task 8's registry-driven
`TestOfflineAndRefreshAreRefusedExactlyWhereThereIsNoLiveFeed` was **not** edited and goes green
on its own, which is exactly the design it was written for.

➕ **A bare `azureprovider.New()` is live, and that made a test reach Azure for real.** The
zero `LivePriceConfig` has `Offline: false` and `liveBase()` falls back to the contracted
endpoint, so any test that queries one or two covered regions through a bare provider issues a
real HTTPS request — and, because the path fails soft, nothing goes red and nothing says a
request was made. It was caught by temporarily panicking in `sweepRegion` when
`live.Endpoint == ""` and running `go test ./...`: it fired inside this task's own
`TestALiveSweepAnswersWithFetchedPricesAndReportsLive`, which built the snapshot reference
provider that way. `newTestProvider` in `internal/providers/azure/provider_test.go` now returns
a provider with `Offline: true` and says why, which closes the trap for every current and future
test in the package; `liveProvider` builds its own from `New()` because it wires a stub endpoint.
The default itself stays live, because it matches `spot.Client` and because a bare provider that
silently ignored `--offline` would be the very silent no-op Phase 2 exists to remove.

⚠️➕ **That note first read "with that fix the probe fires nowhere in the module", and that was
false — the probe was run against a warm cache.** `cmd/spotinfo`'s `shippedRegistry` built
Azure the same bare way, and two tests that name a covered region —
`TestAzureRecommendationHonoursAnExplicitRegion` and `TestListAnswersWindowsOnAzure` — swept
`prices.azure.com` for real on every `make test`, 5.5 MB each. The `sweepRegion` panic never
fired there because `~/Library/Caches/spotinfo/azure-prices-westeurope.json.gz` was already
warm from the manual verification an hour earlier, so the fetch branch was never entered. The
generalizable rule: **a network-reach probe run against a warm cache proves nothing.** Run it
under a fresh `SPOTINFO_CACHE_DIR` and inspect the directory afterwards — a cache file that
appears is a request that happened, and it is evidence a passing fail-soft test cannot give
you. `shippedRegistry` now builds Azure through `WithLivePrices` with `Offline: true`, the way
`main.go` builds it, and both region-naming tests assert
`data_source.mode == embedded-snapshot`, which is the one observable that separates a snapshot
answer from a swept one. Verified both ways: with the pin, a fresh cache dir stays empty and
the package drops from 3.9 s to 1.4 s; with the pin dropped and a pre-warmed cache behind a
dead proxy, both assertions go red at `cached` — so the guard discriminates and needs no
network to prove it. `go test ./... -count=1` under one fresh cache dir now writes no
`azure-prices-*` entry anywhere in the module. `refusals_test.go`'s registry was left alone: it
is deliberately the production wiring, and it stays off the network only because every request
in it leaves `--region` at its default of `all`, which `liveRegions` refuses to sweep — its
comment claimed a structural safety it does not have and now says the real reason.

⚠️ **Discovered, pre-existing, not Azure: `internal/spot` fetches both AWS feeds during
`go test`.** The same fresh-cache-dir probe attributes `advisor.json.gz` and `pricing.json.gz`
to `TestNew_IntegrationWithEmbeddedData` and `TestNewWithOptions_EmbeddedDataMode`
(`internal/spot/client_test.go`, `datasource_test.go`), both of which build a live `New()`
client. The fetch itself predates this plan — `git log -S` puts both tests in `5eedf20`, and
`New()` has always been the live client — but the _cache write_ that makes it visible arrived
with `4e82714` on this branch, which is why nobody had seen it before. It is out of scope for a
Task 10 fix — the first test exists to prove the _live_ client falls back, so it cannot simply
be switched to an embedded one — but it violates CLAUDE.md's "never let a test reach the
network" and should get its own task.

⚠️ **CLAUDE.md is now stale on one point.** Its testing section still says "GCP and Azure have
no live path", which this task falsified for Azure. A later task should correct it.

➕ **The live sweep is held to `verifyOperatingSystems`' second half as well as the machine
floor.** A sweep that clears 180 machines on Linux alone is refused (`errUnpricedOS`): all 55
committed regions price both operating systems, so a Windows half that stops arriving would
otherwise answer `--os windows --region westeurope` with nothing while the snapshot it replaced
has rows. A _renamed_ suffix was already caught upstream — the two meters collide on one key at
two amounts and `SelectCurrent` refuses — so this covers only the disappearance case.

⚠️ **The MCP `refresh` argument is sticky, and this task made it expensive. Task 15 should
check it.** `mcpProviders.registry` memoises by `policy.Offline` alone, so a registry built for
a `refresh: true` call is stored under the same key as the default one and every later call
reuses it. The Azure provider inside it then carries `Policy.Refresh`, and re-sweeps 5.5 MB per
named region for the life of the process. The shape predates this task — the AWS client carried
the same sticky flag — but an AWS re-fetch is a conditional GET answered with a 304, where an
Azure re-sweep is ten full pages. Not a correctness bug: a re-fetched answer reports `live`, and
that is true.

⚠️ **`CLAUDE.md:433` is now wrong and Task 16 owns it.** It reads "AWS runs with `--offline`,
GCP and Azure have no live path" in the e2e testing note. Azure now has one. The suite is still
network-free — every Azure invocation in `cmd/spotinfo/e2e_test.go` uses the default
`--region all`, which never sweeps, and `e2eEnv` pins a dead proxy on top — but the sentence
must be corrected, and a future e2e test that names an explicit Azure region needs
`e2eOfflineFor` extended to Azure.

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

- [x] add `Kind` plus an optional `Obtainability *float64` to the existing
      `PlacementObservation`, keeping `Score int` for the AWS path
- [x] define **two** kinds, one per producer this plan builds: `placement_score` (AWS,
      integer 1-10) and `obtainability` (GCP, 0.0-1.0 with an optional uptime estimate).
      Azure's `High`/`Medium`/`Low` label is deferred with its fetcher — do not add a kind
      with no producer
- [x] do **not** normalise the kinds into a shared scale; a common 1-10 would invent
      precision no vendor published
- [x] decide and document how _absent_ differs from _unavailable_: `Placements` is a slice
      today, so both are the empty slice. Add an explicit status rather than overloading length
- [x] add `--with-score`, `--min-score`, `--az` and `--score-timeout` to `recommend` — they are
      root-only today (`main.go:1138`) and `recommendCommand` declares none of them, so Task 12
      would otherwise have no CLI entry point
- [x] make `--min-score` meaningful against both kinds, or refuse it against `obtainability`
      with a message naming the cloud — an integer floor over a 0.0-1.0 probability needs a
      stated mapping, not an implicit one
- [x] write tests for each kind's rendering in table, JSON and CSV
- [x] write a test asserting a placement kind is never accepted by `acceptsRisk`
- [x] write a test asserting the AWS path still produces the same score it does today
- ➕ delete the four `vocabularyGaps` rows marked "task 11" in
  `cmd/spotinfo/vocabulary_test.go` — **done**; only the two task 13 rows remain
- ➕ call `requireWithScore` (`cmd/spotinfo/provider_flags.go`, Task 8) from
  `execRecommendCmd` when the four flags land. Task 8 refuses `--az`, `--min-score` and
  `--score-timeout` without `--with-score` on `list` only, because `recommend` declares none
  of them today; declaring them without the check lands three silently ignored flags —
  **done**, through the shared `validateScoreFlags`
- ➕ bound `--score-timeout` on the CLI the way `internal/mcp` bounds `score_timeout`
  (1..`cloud.MaxScoreTimeoutSeconds`). `listQuery` drops anything outside `timeout > 0` with
  no error, so a negative or absurd value is accepted and ignored — **done**, on both commands
- [x] run `make test` — must pass before Task 12

➕ **`--min-score` is refused against `obtainability`, not mapped onto it.** The two options
the plan offers are not equally honest: an AWS 8 is a decile bucket whose boundaries AWS does
not publish, so reading `--min-score 8` as "obtainability ≥ 0.8" would be this tool inventing a
correspondence between two vendors' measurements — the exact thing the kind vocabulary exists to
prevent. `Capabilities` therefore gained `PlacementKind`, a provider that declares
`PlacementScore` must name its measurement, and `SupportsScoreFloor()` is the single rule both
surfaces read. `--with-score` still answers on such a cloud; only the integer filter is refused,
naming the cloud and the kind. The refusal is unreachable until Task 12 gives GCP the
capability, and is tested against a stub that declares it.

➕ **Three wire states, one per domain status, and `placement_status` is published only for
the one the values cannot already state.** A figure present means available; `"placement_status":
"unavailable"` means the lookup ran and produced nothing; neither means nobody asked. Emitting
`"available"` beside a score would say the same thing twice — and would have rewritten
`cmd/spotinfo/testdata/list-v1.json`, a golden this task must not touch. `list-v1.schema.json`
declares the field as a single-valued enum, which is honest about what can appear there today.
Reachable on AWS because `scoreOutcome` makes a per-region score failure non-fatal, so one
failed region among many really does yield rows with a requested-but-absent figure.

➕ **`region_score`, `zone_scores` and `score_fetched_at` keep their names, their order and
their meaning.** They moved into an embedded `cloud.PlacementDTO` — which `RecommendationDTO`
now embeds too, so a ranked page publishes the same block — and `encoding/json` flattens an
embedded struct at its declaration position, so the emitted key order is unchanged and both
recorded goldens stayed byte-identical with no `UPDATE_GOLDEN` run. The obtainability fields
and the status are appended after them.

➕ **`stubProvider.Query` now drops placements when the query asked for none**, which is what
kept `recommend-v3.table.txt` and `recommend-v3.json` unchanged. The fixture publishes scores
unconditionally and the recorded recommend invocation passes no `--with-score`, so once
`recommend` started publishing the block, a stub that ignored the request would have recorded a
column the shipped binary never draws.

➕ **`--sort score` works on `recommend` now, and needs `--with-score` _and_ a regional figure.**
Task 4 refused the key with "which recommend does not publish"; that sentence is false as of this
task, so the comparator was implemented — each kind ordered on its own scale, a mixed pair left
unordered, and only the _regional_ figure ordering a page, because inventing a maximum or a mean
across zones would be publishing a regional figure the provider declined to give. That leaves
**two** ways to ask for an ordering that would order nothing, and `requireScoresToSortByThem`
refuses both as companion rules: without `--with-score` every figure is absent, and under `--az`
every row carries one figure per zone and no regional one. The second refusal was missing on the
first pass — `spotinfo recommend --with-score --az --sort score` was accepted, exited 0 and left
the page in ranking-policy order with a zone-score column contradicting it — and landed in the fix
commit below, keyed off `--az` rather than off the kind because neither kind publishes a regional
figure once zone detail was asked for. `TestRecommendRefusesASortKeyItDoesNotPublish` still passes
unchanged; only its comment moved. `recommend`'s own `--sort` usage string now names both rules,
so `--help` states them rather than leaving them to be found by running it; `list` declares a
separate `--sort` whose rules differ, and it is not touched. **`list --sort score` is deliberately
untouched** — Task 8 scoped its companion set to the three flags, and re-opening it there is not
this task's to do.

➕ **A figure nobody published sorts last under either order, and the direction is now an argument
to the comparator rather than a flip of it.** `sortRecommendations` built a descending page with
`compare(&right, &left)`, which inverts the absent handling along with everything else: `--sort
score --order desc` opened with every row whose placement lookup produced nothing, ranked above
the best measured score — silence above measurement, which is the comparison `placement_status`
exists to prevent. Each comparator now takes `descending` and applies it only to two _present_
values, so a tie is still a tie in both directions and the stable sort still leaves the ranking
policy's own order among equals alone; `compareOptionalInt` and `compareOptionalFloat` collapsed
into one generic `compareOptional`. **This changes `--sort savings|risk --order desc` too**, which
carried the same defect from Task 4: a row with no published discount or no published risk no
longer sorts ahead of every row that has one.

➕ **The four score arguments are not added to the MCP `recommend_spot_machines` tool.** Its
argument set is pinned by `docs/plans/contracts/recommend-v3-input.schema.json` and recorded in
`internal/mcp/testdata/recommend-v3-input-schema.json`, both Task 6/7 artifacts; widening them
is a contract change no checkbox here owns, and it would rewrite a golden. The four
`mcpArgumentGaps` rows stay, with their reason corrected from "task 11 puts the score flags on
recommend" to the real one. The `list_spot_machines` tool _did_ gain the `min_score`-versus-kind
refusal, so the two surfaces agree wherever both declare the argument.

⚠️ **A ranked page's placement figures are reachable only from the CLI.** Acceptance criterion 2
asks that the same question return the same document on both surfaces; a question that cannot be
asked over MCP is not asked, and the gap table records it. **Task 15 should check this** when it
verifies criterion 2, alongside the Task 8 note about `az` and `score_timeout`.

⚠️ **`list --sort score` without `--with-score` is still a silent no-op, and this task did
not fix it.** On AWS it reaches `spot.SortByScore` over rows that carry no score and exits 0
having ordered nothing — the same defect `requireScoresToSortByThem` now removes on `recommend`.
It is left alone deliberately: Task 8 scoped its companion set to `--az`, `--min-score` and
`--score-timeout`, and widening that set on `list` is not this task's to do. **Task 15 should
check it** alongside the other companion-flag notes. The one-line fix is to add the sort key to
`requireWithScore`. **`list --with-score --az --sort score` is the same hole for the other
reason** — `spot.SortByScore` reads only `RegionScore` (`internal/spot/types.go:102`), which
`--az` never sets — and it is deferred with it. `recommend` refuses both combinations as of the
fix commit; `list` refuses neither.

➕ **Nothing re-applies `Placement.MinScore` in `internal/cloud`.** `rank()` and `accepts()`
never see it: the floor is honoured during acquisition by `spot.WithMinScore`, and every other
kind refuses the flag before a provider is queried. That is safe only because those two facts
hold together — **Task 12 must not assume a neutral re-filter covers it**. A provider that
publishes `placement_score` and does not apply the floor itself would silently return rows below
it.

⚠️ **`docs/api-reference.md` does not describe the three new response fields**
(`region_obtainability`, `zone_obtainability`, `placement_status`), and was not patched here.
The candidate schema it documents is the retired v1 shape — `instance_type`,
`spot_price_per_hour`, `reliability_score`, `interruption_rate`, none of which the binary
publishes since Task 5 — so adding three fields to a document describing a deleted schema would
make it more misleading, not less. Task 16 owns the rewrite. The same is true of the
`spotinfo --type …` examples in `README.md`, `docs/usage.md`, `docs/examples.md`,
`docs/quick-start.md` and `docs/aws-spot-placement-scores.md`, which Task 8 already recorded as
Task 4/5/7 debt; this task adds one item to that debt, which is that the four score flags are
documented as root-command flags and are now declared on `list` **and** `recommend`.
`docs/clouds.md:65` is unaffected — it says `--with-score` reads `GetSpotPlacementScores`, which
is still true and is now true on both commands.

➕ **`make verify-architecture` passes: verdict `pass`, `archfitcheck: no open critical or high
findings (31 findings reviewed)`, all 44 severities `medium`.** Run because this task adds about
a thousand lines across ten production files, which is the shape a size or complexity rule
notices.

➕ **Files this task's list omits.** `internal/cloud/provider.go` (`Candidate.PlacementStatus`,
`Capabilities.PlacementKind`, `SupportsScoreFloor`), `cmd/spotinfo/list.go` and
`cmd/spotinfo/provider_flags.go` (the shared `scoreFlags`, `placementRequest` and
`validateScoreFlags`), `docs/plans/contracts/{list-v1,recommend-v3-success}.schema.json`, the
new `cmd/spotinfo/placement_test.go`, and
`cmd/spotinfo/{contract,format,provider_flags,mcp_vocabulary}_test.go`,
`internal/cloud/recommend_test.go`, `internal/mcp/{helpers,jsonschema,tools}_test.go`,
`internal/providers/aws/provider_test.go` and one refusal row in `cmd/spotinfo/e2e_test.go`,
which is where a process exit code and an empty stdout are actually observed. `internal/cloud/enrich.go` and
`internal/cloud/schema_test.go` are in the list and were **not** touched: no checkbox asks for a
placement enricher — Task 12 owns the GCP fetcher — and the schema is covered by the contract
validation in `internal/mcp/jsonschema_test.go`, which is where the list payload is checked
against its schema file.

### Task 12: Fetch GCP obtainability

**Files:**

- Create: `internal/providers/gcp/placement.go`, `internal/providers/gcp/placement_test.go`
- Modify: `internal/providers/gcp/provider.go`

- [x] call `compute.advice.capacity` (beta) with the ADC machinery already built for
      `--live-risk`, reusing the same lazy credential resolution
- [x] respect the documented limit of 5 machine types per request
- [x] carry `estimatedUptime` alongside `obtainability`
- [x] mark the capability as beta in `Capabilities()` and say so in the help text
- [x] write tests against a stub transport: success, over-limit batching, no credentials
- [x] run `make test` — must pass before Task 13

➕ **One machine type per request, and that is a reading of the _response_ rather than of the
limit.** Google's REST reference for `advice.capacity` sends two `instanceSelections` and
answers with **one** `recommendations[]` entry carrying a single `scores` block over two
`shards`, and the availability guide states the score "applies to the entire configuration
rather than per individual machine type". A five-type request therefore scores the _set_:
attaching that figure to each member would publish a number Google measured for none of them,
which is precisely what `PlacementKind` exists to prevent. So `maxMachineTypesPerRequest` is
the documented ceiling and `capacityAdviceBody` refuses above it — never truncates, which is
the failure `docs/reviews/multicloud-parity.md` §5 asks to avoid — while `obtainability` asks
one type at a time. `TestCapacityAdviceBodyHonoursGooglesMachineTypeCeiling` drives 0, 1, 5 and
6 types, and `TestEnrichPlacementAsksAboutEveryMachineOverTheRequestCeiling` drives a
seven-machine page and asserts seven requests with no machine dropped. Raising the batch is
only correct if Google starts scoring each selection separately.

➕ **`--with-score` on `list --cloud gcp` is refused, so Task 4's ⚠️ note is answered the other
way: `list` still has no use for `--gcp-project`.** The figure is fetched, not read from the
catalogue, and it is fetched for the ranked page — one authenticated request per row against a
333-machine catalogue is hundreds of calls billed to the caller's own project for an answer
nobody browses. The refusal lives in `gcp.Provider.Query`, which is the one place the CLI and
the MCP `list_spot_machines` tool both pass through; a CLI-only check would have left the MCP
browse tool answering an empty column and exiting 0. The CLI half is tested twice — a unit test
and an `e2e_test.go` refusal row, which is where the exit code and the empty stdout are
observed. The MCP half follows from the chokepoint plus the mapping: `ListSpotMachinesTool.Handle`
wraps every error out of `list` — `provider.Query` included — in `cloud.CodeOf`, which turns
`ErrUnsupportedCapability` into `UNSUPPORTED_CAPABILITY`. It is not tested directly because
`internal/mcp` answers from stubs and registers no GCP provider.
`Capabilities().PlacementScore` is
declared **unconditionally** and not from whether a fetcher is wired: both surfaces check
capabilities on the provider they resolved, before `withPlacement` attaches one, so a
conditional declaration would refuse the single request that can be served.

➕ **Two more refusals landed in the same place, for different reasons.** `--sort score` on a
browsed GCP catalogue can never be honoured — there is no placement column at acquisition at
all — and an integer `--min-score` states no reviewed mapping onto a probability. Both surfaces
already refuse the second (`SupportsScoreFloor`); the provider refuses it too, which is where a
surface that forgot is caught. This makes GCP stricter than AWS `list`, whose `--sort score`
silent no-op Task 11 recorded as deferred: that one is a key AWS _can_ serve under
`--with-score`, so it is a different defect.

➕ **`estimatedUptime` is published as `region_estimated_uptime_seconds`.**
`PlacementObservation` gained `EstimatedUptime *time.Duration` and `PlacementDTO` a nullable
seconds field, added to both `docs/plans/contracts/{list-v1,recommend-v3-success}.schema.json`.
Seconds rather than Google's `"600s"` duration string so a consumer compares without parsing,
and named for its unit the way `window_days` and `memory_gib` are. It is `omitempty` and only
GCP sets it, so every recorded golden stayed byte-identical with no `UPDATE_GOLDEN` run. It is
not rendered in the table or CSV: those pages carry one placement column, and a second column
empty on two of three clouds buys less than the JSON field does.

➕ **`Capabilities.PlacementBeta` has exactly one reader, and it is not GCP-specific.**
`withPlacement` logs one warning on stderr when the resolved provider declares it, naming the
cloud and the kind. Nothing in the published answer distinguishes a GA interface from a preview
one, and the advice API has no v1 form. The `--with-score` help text says the same thing
statically.

➕ **`perPair` is now shared by both authenticated lookups.** `EnrichRisk` and
`EnrichPlacement` deduplicate the page by (machine, region) and fan out under the same
`maxConcurrentLookups` bound; the loop was written twice and is now written once, with the
failure accounting under a mutex and the candidate writes outside it — the groups partition the
page, so no two callbacks can touch the same candidate. `endpointOr` and `transportOr` are the
matching extraction for the endpoint override and the lazy ADC client, so there is one
`sync.OnceValues` credential resolution however many APIs are asked. `go test -race` is clean
on `internal/providers/gcp` and `internal/cloud`.

➕ **No test resolves real credentials or reaches Google.** Every success path injects a
`httptest` server through `PlacementConfig.Client`; the no-credentials test swaps the
package-level `httpClient` for a failing resolver and is deliberately serial. The two CLI tests
that could have wired a real lookup clear `GOOGLE_CLOUD_PROJECT` with `t.Setenv` and assert a
refusal, so neither ever reaches `google.DefaultClient` — a developer machine with Application
Default Credentials would otherwise have made this suite call the live API.

➕ **Files this task's list omits.** `internal/cloud/{enrich,recommend,observations,provider,
schema}.go` (the `PlacementEnricher` seam, `enrichRankedPlacement`, the uptime field and
`PlacementBeta`), `internal/providers/gcp/live.go` (the three extractions),
`cmd/spotinfo/placement.go` (new, `withPlacement`), `cmd/spotinfo/liverisk.go`
(`resolveGCPProject`, shared with `--live-risk`), `cmd/spotinfo/{recommend,provider_flags}.go`,
both contract schema files, and `cmd/spotinfo/{placement,e2e}_test.go` plus
`internal/providers/gcp/provider_test.go`.

➕ **`make verify-architecture` passes: verdict `pass`, `archfitcheck: no open critical or high
findings (31 findings reviewed)`, all 44 severities `medium`** — unchanged from Task 11. Run
because this task adds two production files and about 400 lines across nine, which is the shape
a size or complexity rule notices.

⚠️ **A ranked page's obtainability is reachable only from the CLI**, for the same reason Task 11
recorded: `recommend_spot_machines` declares none of the four score arguments, and the MCP
surface has no `gcp_project` argument at all. **Task 15 should check this** alongside the
existing criterion-2 notes.

⚠️ **Two lines of Task 16 doc debt, added to Task 11's.** `docs/clouds.md:65` says `--with-score`
reads `GetSpotPlacementScores`, which is now true of AWS only — on GCP it reads
`advice.capacity` and returns a probability. And Task 11's ⚠️ about `docs/api-reference.md`
missing three placement fields now misses four: `region_estimated_uptime_seconds` joins them.

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
