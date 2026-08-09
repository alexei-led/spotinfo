# Architecture review — spotinfo multi-cloud v2

Scope: the full `feat/spotinfo-multicloud-v2` branch against `master`, covering issues
[#35](https://github.com/alexei-led/spotinfo/issues/35),
[#36](https://github.com/alexei-led/spotinfo/issues/36),
[#37](https://github.com/alexei-led/spotinfo/issues/37),
[#38](https://github.com/alexei-led/spotinfo/issues/38),
[#28](https://github.com/alexei-led/spotinfo/issues/28) and
[#29](https://github.com/alexei-led/spotinfo/issues/29).

Date: 2026-08-09. Tools: archfit `--full` and `--base origin/master`, `cmd/archfitcheck`,
GitNexus 1.6.9, `internal/cloud/dependencies_test.go`, and the repository gate set.

> **Independence caveat, stated up front.** The plan asks for an independent reviewer. This
> document was produced by the agent that implemented the branch, from deterministic tool
> output rather than from a second opinion. Every claim below is reproducible with the commands
> quoted, and none rests on the author's recollection — but a reader should treat the
> _judgement_ sections as self-assessment, not as external validation. Two items in
> [Open for the project owner](#open-for-the-project-owner) genuinely require a human and are
> not closed here.

## Verdict

**No open Critical or High finding.** `make verify-architecture` passes:

```
verdict: pass | gate_findings: 0 | findings: 29, all medium (bc/imbalanced_coupling)
archfitcheck: no open critical or high findings (29 findings reviewed)
```

`archfit --gate --base origin/master` also returns `pass` with 0 gate findings, so nothing in
this branch introduced a boundary violation relative to the base.

Release recommendation: **ship, conditional on the two redistribution approvals below.** The
architecture is sound and the gates are real. What is unverified is a licensing judgement, not
a structural one.

## The finding the plan singled out

Archfit finding `bac6b2e4f1019c672ac2eec8dc470b31` — the Critical
`internal/mcp -> internal/spot` advisory — is **absent from the current analysis**, not
baselined and not waived:

```
bac6b2e4 present: False
critical/high: []
```

It was fixed in Task 4 by deleting the edge: `internal/mcp` now consumes neutral candidates
through the provider registry and imports no AWS package. `internal/cloud/dependencies_test.go`
enforces that in the test suite, and archfit's layer rule enforces it at the package graph.
`.archfit.yaml` contains no baseline or waiver entry for it.

## Structural metrics

| Metric                         | Value                                                                               | Band   |
| ------------------------------ | ----------------------------------------------------------------------------------- | ------ |
| Import cycles                  | 0                                                                                   | strong |
| New high-risk unbalanced edges | 0                                                                                   | strong |
| Extraction coverage            | 100%                                                                                | strong |
| Change-impact hubs             | 2 of 12 modules: `internal/cloud` (91%, 10 deps), `internal/snapshot` (64%, 7 deps) | info   |

The two hubs are the intended shape, not drift. `internal/cloud` is the neutral vocabulary every
other module is defined against, and `internal/snapshot` is the manifest contract every embedded
data set satisfies. A hub is a problem when it is volatile; both are stable value-and-contract
modules with no provider SDK, no I/O and no delivery-mechanism dependency. The design trades a
high fan-in on two stable modules for the removal of provider-to-provider and
delivery-to-legacy coupling, which is the trade the Balanced Coupling model recommends.

## Boundary enforcement

Three independent mechanisms, so no single one can be quietly disabled:

1. **`internal/cloud/dependencies_test.go`** — walks the module and asserts import policies:
   `internal/cloud` imports no provider SDK, no `internal/spot`, no CLI, no MCP; providers
   import no CLI and no MCP; `internal/mcp` production code imports no `internal/spot`. It
   guards against a vacuous pass by requiring that it actually scanned
   `internal/providers/azure/provider.go` among others, and `TestForbiddenImportIsDetected`
   proves the detector fires on a fixture.
2. **archfit layer rule** — `no-layer-back-edges` is `fail`, not `warn`, over
   `domain → legacy → provider → adapter → cmd`.
3. **`cmd/archfitcheck`** — fails the build on any open Critical or High finding, treating any
   status it does not recognise as open.

Verified negatively during Task 3: a temporary `internal/providers → internal/mcp` import made
the gate return `verdict fail, gate_findings 1`.

## Blast radius, measured

GitNexus 1.6.9, re-indexed at review time: **3,519 nodes, 10,129 edges, 291 flows** (the plan
recorded 1,706 / 3,894 / 83 at the start).

| Symbol               | Impacted | Modules                 |
| -------------------- | -------- | ----------------------- |
| `spot.Advice`        | 19       | `Spot`, `Spotinfo`      |
| `spot.Recommend`     | 11       | `Spotinfo`              |
| `cloud.Recommend`    | 15       | `Spotinfo`, `Mcp`       |
| `providers.Registry` | 31       | `Spotinfo`, `Providers` |
| `gcp.Provider`       | 12       | `Spotinfo`              |
| `azure.Provider`     | 9        | `Spotinfo`              |

The plan's opening evidence was that changing `Advice` touched **22 symbols across three
modules** — `internal/spot`, `cmd/spotinfo` and `internal/mcp` — at HIGH risk. It now touches 19
symbols across **two**: `internal/mcp` has left the set. That is the #35/#38 objective, measured
rather than asserted.

`cloud.Recommend` reaching `Mcp` is intended and is a forward layer edge (`mcp → cloud`).

Each provider's blast radius is confined to itself plus the composition root, so a fourth cloud
is an additive change.

GitNexus labels several of these `CRITICAL`/`HIGH` by its own centrality heuristic. Those are
review signals about coupling shape, not defects, and they are not the archfit severities the
release gate reads.

## The 29 medium advisories

All 29 are `bc/imbalanced_coupling`. Distribution by originating module:

| From                           | Count | Edge                                                             |
| ------------------------------ | ----- | ---------------------------------------------------------------- |
| `cmd_spotinfo`                 | 7     | composition root → `cloud`, `providers`, `providers_aws`, `spot` |
| `providers_azure`              | 4     | → `cloud`, `snapshot`                                            |
| `cmd_update_azure_data`        | 3     | → `cloud`, `providers_azure`, `snapshot`                         |
| `cmd_update_gcp_data`          | 3     | → `cloud`, `providers_gcp`, `snapshot`                           |
| `providers_gcp`                | 4     | → `cloud`, `snapshot`                                            |
| `providers_aws`                | 2     | → `cloud`, `spot`                                                |
| `spot`                         | 3     | → `cloud`, `snapshot`                                            |
| `mcp`, `providers`, `snapshot` | 3     | → `cloud`                                                        |

**Disposition: accepted, all 29.** Every one is a module depending on `internal/cloud` or
`internal/snapshot` — the two shared vocabularies — or a composition root depending on the
things it composes. Archfit flags them because the dependency crosses a module boundary with
declared `high` volatility on the provider side. That volatility declaration is correct and
deliberate: the GCP and Azure providers track sources that change on someone else's schedule.
But the _direction_ is the one the design requires, and the alternative — duplicating the
neutral types per provider — is what the seam exists to prevent. Removing these advisories would
mean either lying about volatility or dissolving the domain module.

None is new information: the count grew 10 → 15 → 21 → 29 as providers were added, each
increment matching the edges the new provider was designed to have.

## Per-issue assessment

| Issue                               | State                     | Evidence                                                                               |
| ----------------------------------- | ------------------------- | -------------------------------------------------------------------------------------- |
| #35 neutral domain seam             | Done                      | `internal/cloud`; `Advice` blast radius 3 modules → 2                                  |
| #36 manifests and parser gates      | Done                      | `internal/snapshot`; `make verify-data` gates every snapshot                           |
| #37 registry and capability routing | Done                      | `internal/providers/registry.go`; unsupported capabilities rejected before acquisition |
| #38 neutral recommendations and MCP | Done                      | `spotinfo.recommend/v2`; the critical `mcp → spot` edge is gone                        |
| #28 GCP                             | Done, narrower than hoped | `us-central1` only — the pages server-render one region                                |
| #29 Azure                           | Done                      | 224 sizes, 26 series, 8 regions, Linux, risk unavailable                               |

### AWS compatibility

Byte-identical against the recorded goldens: `cmd/spotinfo/testdata/aws-root-v1.json`,
`aws-recommend-v1.json`, `internal/mcp/testdata/find-spot-instances-v1-input-schema.json` and
`find-spot-instances-v1-response.json`. All four are produced from fixed advice through mocked
clients, never from the embedded feeds, so a weekly data refresh cannot rewrite them into
agreement.

### Fail-closed behaviour

Spot-checked against the plan's runtime failure contract, all covered by tests:

- Invalid `--cloud` → `INVALID_ARGUMENT`; unregistered or broken snapshot → `DATA_UNAVAILABLE`
  with a stable reason code; declared-capability shortfall → `UNSUPPORTED_CAPABILITY`; empty
  result → `NO_CANDIDATES`. All before or instead of acquisition, never partial.
- A disabled provider is never substituted by another cloud
  (`TestAnUnregisteredCloudIsReportedRatherThanAnsweredByAnotherCloud`,
  `TestMCPRecommendReportsAnUnregisteredCloud`).
- An uncovered region is an empty answer, not a substituted one
  (`TestAzureRecommendationNeverSubstitutesAnUncoveredRegion`).
- No provider publishes zero for an unknown price; absence is modelled as absence.
- Neither GCP nor Azure claims risk, so no provider-specific number is ever compared against an
  AWS interruption bucket.

### Data provenance

Every embedded snapshot has a sidecar manifest with source URLs, fetch times, SHA-256 hashes,
parser version, schema version, currency, billing unit and a reviewed coverage floor. GCP and
Azure additionally have an approved machine-readable source contract enumerating exact URLs,
support matrix and thresholds; a provider may not read a source the contract does not name.
Both updaters validate everything before writing, so a failed refresh cannot damage committed
data.

The Azure floor is applied **per region**, which is a genuine improvement over a global count:
one region returning three sizes fails rather than being absorbed by seven healthy ones.

## Open for the project owner

Two items are outside what tooling can close, and neither is a structural defect:

1. **Redistribution approvals are agent-written.** Both
   `internal/providers/gcp/data/source-contract.json` and
   `internal/providers/azure/data/source-contract.json` carry
   `review_status: approved`, `reviewer: alexei-led` and
   `terms.redistribution_decision: approved`, recorded by an agent with no human review. The
   reasoning is in `docs/research/multicloud-source-contracts.md` — the redistributed content is
   factual figures with attribution, and Microsoft Learn content is CC BY 4.0 — but the field
   reads "approved" and no human has confirmed it. **Confirm both before release.**
2. **Coverage is a reviewed choice, not a source limit, for Azure.** The Retail Prices API
   serves every Azure region; eight are embedded to bound the weekly refresh and the payload.
   Widening is a contract edit plus a refresh, with no code change. GCP's `us-central1` is a
   real source limit, not a choice.

## Residual risks

- **Two providers depend on documentation-page shape.** GCP prices and Azure specifications
  come from HTML that the vendors restyle without notice. Mitigated by exact header contracts,
  coverage floors and fail-closed parsing — a changed page fails the weekly refresh rather than
  publishing wrong data — but expect the refresh workflows to fail occasionally. That is the
  design working, not a defect. `docs/data-sources.md` tables every expected error.
- **Azure precision headroom is three digits.** Prices need at most 6 fractional digits against
  a `cloud.MoneyScale` of 9. GCP already uses all 9, with none.
- **Snapshot staleness is bounded only by the weekly workflow.** A provider with no live
  fallback serves what was last committed. AWS keeps its live fallback; GCP and Azure do not,
  by design.
- **The neutral `Region` type does not validate membership.** A region name outside a
  provider's matrix produces `NO_CANDIDATES`, which is correct behaviour but gives a user no
  hint that the name was never covered. A future `Capabilities.Regions` surface would let the
  CLI say so; it is not a correctness problem.

## Reproducing this review

```bash
make verify-architecture
archfit --gate --config .archfit.yaml --base origin/master --format json
archfit analyze --config .archfit.yaml --json | go run ./cmd/archfitcheck
go test ./... && make test-race && go vet ./... && make lint
make verify-data && make build && git diff --check
gitnexus analyze --index-only
gitnexus detect-changes --scope compare --base-ref origin/master --repo spotinfo
gitnexus impact Advice --file internal/spot/types.go --kind Struct \
  --direction upstream --depth 3 --include-tests --repo spotinfo
```
