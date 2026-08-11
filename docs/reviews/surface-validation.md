# Surface validation — the built binary on three clouds

Task 17 of [`docs/plans/20260811-multicloud-parity.md`](../plans/20260811-multicloud-parity.md).

Tasks 1 to 16 proved the packages against mocks, stubs and goldens. Nothing in them proved that
the **binary a user downloads** answers on three clouds. This page records what was swept, what
passed, and every cell that was skipped and why.

Everything below was observed on the binary, as a subprocess. Nothing here is inferred from a
unit test.

## Where the assertions live, and why the split matters

| Half        | File                                  | Runs in     | Reaches a vendor |
| ----------- | ------------------------------------- | ----------- | ---------------- |
| **Offline** | `cmd/spotinfo/e2e_test.go`            | `make test` | never            |
| **Live**    | `cmd/spotinfo/validate_clouds_test.go` | `make validate-clouds` | yes   |

The live half is guarded by `SPOTINFO_VALIDATE_CLOUDS`, which only `make validate-clouds`
exports, and the file has **no build tag** — so `make test` compiles it (a rename breaks the
build rather than silently deleting the suite) and runs none of it.

`TestValidateCloudsIsNotAMergeGate` asserts that separation from **inside** the ordinary suite,
in both directions: the `validate-clouds` target exists and exports the variable, `make test`'s
recipe mentions neither, and no file under `.github/workflows/` mentions either. Absence alone
would pass vacuously against a deleted target, which is why existence is asserted first.

## The matrix: {list, recommend} × {aws, gcp, azure} × {number, text, json, table, csv}

Every cell runs `--offline`, scoped to one region and one machine per cloud — the same
acquisition path as a full sweep, at a tenth of a second instead of four seconds and 8 MB.

| Cloud | Region        | Machine              |
| ----- | ------------- | -------------------- |
| aws   | `us-east-1`   | `m5.large`           |
| gcp   | `us-central1` | `n2-standard-4`      |
| azure | `eastus`      | `Standard_B16as_v2`  |

Asserted per cell: **exit 0, non-empty stdout, empty stderr, at least one data row**. Empty
stderr is the load-bearing half — an empty match, an unreachable feed and a beta warning all
still exit 0 and still print something, and each is a cell that answered less than it claims.

Measured, 30 cells:

| Cell               | Exit | stderr  | Rows |
| ------------------ | ---- | ------- | ---- |
| list/aws/*         | 0    | empty   | 1    |
| list/gcp/*         | 0    | empty   | 1    |
| list/azure/*       | 0    | empty   | 1    |
| recommend/*/number | 1    | refusal | —    |
| recommend/aws/{text,json,table,csv}   | 0 | empty | 3 |
| recommend/gcp/{text,json,table,csv}   | 0 | empty | 3 |
| recommend/azure/{text,json,table,csv} | 0 | empty | 3 |

`number` is `list`-only, and the refusal says where it lives rather than only that it is wrong:

```console
$ spotinfo recommend --cloud aws --offline --architecture x86_64 --min-vcpu 2 --min-memory-gib 8 --output number
spotinfo: invalid argument: --output number belongs to `spotinfo list`: one savings percent cannot describe a ranked page
```

### `text` and `csv` were built here, and that is a scope decision

`recommend` answered in `table` and `json` only. Task 14 recorded that as **Open** with
"No task between here and Task 17 owns it", and the matrix above cannot be swept without it.

They landed rather than being recorded as a deviation, because the refusal had no principle
behind it: `number` is `list`-only because one savings percent cannot describe a ranked page,
while `text` and `csv` describe three ranked rows perfectly well. Leaving it made `--output`
the one flag whose accepted values depended on which command it was given on.

Both render the same columns as the table, and publish the amounts the JSON report publishes —
the full fixed-point price string and a bare percent — because a program reads them:

```console
$ spotinfo recommend --cloud gcp --offline --architecture x86_64 --min-vcpu 2 --min-memory-gib 8 --top 2 --output csv
Rank,Cloud,Region,Machine,Architecture,vCPU,Memory GiB,USD/Hour,Savings over On-Demand,Risk,Why
1,gcp,us-central1,n2d-standard-2,x86_64,2,8,0.026912000,68,unavailable,ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
```

The `text` keys are the `spotinfo.recommend/v3` field names, not the column headings
lower-cased: `USD/Hour` is a good column title and a poor shell variable, and a caller who
already reads the JSON document should not have to learn a second spelling.

## Contract validation

Every `json` cell is validated against the contract file it declares, read from
`docs/plans/contracts/` rather than restated in the test — so a schema edit that outruns the
code fails in `cmd/spotinfo` rather than in a consumer.

| Document                | Contract                            |
| ----------------------- | ----------------------------------- |
| `spotinfo.list/v1`      | `list-v1.schema.json`               |
| `spotinfo.recommend/v3` | `recommend-v3-success.schema.json`  |

Both pass, on all three clouds, on **both surfaces** — the CLI's `--output json` and the MCP
tools' payloads go through the same check.

The validator is `github.com/santhosh-tekuri/jsonschema/v6`, already in the module graph
through `mcp-go` and promoted from indirect to direct. **No non-test file imports it**, so the
shipped binary is unchanged. A decode-into-a-struct check would not do: `encoding/json` ignores
every keyword a contract is written in — `required`, `enum`, `pattern`,
`additionalProperties`, `minItems` — so a document missing half its fields decodes cleanly and
satisfies nothing. `TestE2EContractValidatorRejectsAWrongDocument` keeps the check honest by
feeding it a document that must fail.

`internal/mcp` keeps its own small checker. Its documented upgrade trigger — a contract using
`$ref`, `allOf` or a conditional — has not fired (`grep` finds those keywords only in
`provider-source-contract.schema.json`), and swapping it is not this task's job.

`go mod tidy` also demoted `github.com/spf13/cast` to indirect: no Go file imports it any more.
That is pre-existing staleness in `go.mod`, corrected as a side effect. `CLAUDE.md` still lists
it under Key Libraries — Task 16's documentation debt.

## Determinism — one real defect, found and fixed

Two identical `--offline` invocations must produce identical bytes. This is the only check in
the suite that sees an unstable ordering, and it found one:

```console
$ spotinfo list --cloud aws --offline --output csv > a; spotinfo list --cloud aws --offline --output csv > b; cmp a b
a b differ: char 764, line 4
```

Task 3 predicted the cause and named the fix: `internal/spot`, not a renderer. Confirmed —
`GetSpotSavings` ranged over a map of per-region advice, `getRegions` built its slice from a
map, and `sortAdvices` used `sort.Sort`. None of the comparators is a total order (every row in
the same interruption bucket ties), so the ties kept whatever order the maps produced.

The fix is three lines: sort the region keys, sort the machine keys, and sort **stably**. Each
was checked by reverting it alone. The map keys are what byte-identity depends on — reverting
them alone fails `TestGetSpotSavingsReturnsTheSameOrderEveryRun` with "run 21 returned a
different page" and fails
`TestE2ETheSameOfflineInvocationIsByteIdentical/aws_browses_every_region` on the binary.
Stability is what makes the surviving order *explainable*: reverting `sort.Stable` alone keeps
the run reproducible — pdqsort is deterministic for identical input — but permutes the ties, so
rows in one interruption bucket no longer read as "by region, then by machine name". That
revert fails the row-order assertion of the same unit test.

Now byte-identical, measured across `{list, recommend}` × three clouds, plus the two widest AWS
answers — every region, no sort key, and every region sorted by price.

## Invariant 2 on the rendered page

`TestE2ETheRiskColumnPrintsAStatusOnCloudsWithoutRiskData` reads the risk cell off `csv` by
column index and off `table` and `text` as a substring, for GCP and Azure:

- the cell is exactly `unavailable` — never blank, which reads as "no interruptions", and never
  a zero, which reads as a measurement of none;
- none of the five AWS Spot Advisor buckets (`<5%`, `5-10%`, `10-15%`, `15-20%`, `>20%`)
  appears anywhere on a GCP or Azure page.

## Refusals

**The refusal matrix** — 27 cells, each asserted to exit non-zero, print **nothing** to stdout,
and say what and where. Two classes, deliberately worded differently:

- a **capability** refusal is about one cloud and names it (`--with-score` on Azure,
  `--gcp-project` off GCP, `--live-risk` off GCP, `--workload web|ci` on GCP and Azure,
  `--os windows` on GCP);
- a **companion** refusal is a flag combination refused identically everywhere (`--az`,
  `--min-score`, `--score-timeout` without `--with-score`), so it names the two flags and no
  cloud — naming one would imply another cloud accepts the combination.

**Rename hints** — all eight retired names (`--type`, `--instance`, `--vcpu`, `--memory`,
`--memory-gib`, `--cpu`, `--price`, `--budget`) on **both** commands: exit non-zero, empty
stdout, and a message naming both what was passed and its replacement
(`TestE2EEveryRetiredFlagNameProducesARenameHint`, 16 cells).

## MCP, end to end

One stdio session per test — the server is one process for a client, and nine sessions would
mostly measure process start-up. `HTTP_PROXY`/`HTTPS_PROXY` are pinned at a closed port and
every tool call passes `offline: true`.

- the handshake completes;
- `tools/list` returns **exactly** `list_cloud_regions`, `list_spot_machines`,
  `recommend_spot_machines`, compared as a sorted set;
- all three tools answer for all three clouds — nine calls. `list_spot_machines` and
  `recommend_spot_machines` payloads are validated against their contract files;
  `list_cloud_regions` returns `spotinfo.regions/v1` with a non-empty region list and a
  non-empty source list, which is what makes a trimmed answer's `sources_omitted` resolvable.

**CLI/MCP parity on rendered output** (acceptance criterion 2, against the binary): for each
cloud, `spotinfo recommend --output json` and `recommend_spot_machines` asked the same question
return the same `request` echo, the same `ranking_policy`, the same `data_source.mode`, and a
first recommendation identical in machine, region, price and risk status.

## The binary itself

**No Azure credential library.** `go version -m` on the built binary names no `azidentity`, no
`armresourcegraph`, no `armrecommender`. This is the only check that catches a transitive pull —
such a dependency links without appearing in any import statement here, and `azidentity` alone
costs +4.83 MB for two features this build cannot exercise.

The assertion carries a **positive control**: the module list must name
`github.com/urfave/cli/v2` before the three absences are asserted. Without it, a `go version -m`
that failed quietly would make every absence true of an empty string.

**Size, recorded and not gated:**

| Build                                          | Bytes      |
| ---------------------------------------------- | ---------- |
| Pre-plan baseline, `afe3db6` (`master`)        | 43,961,634 |
| This commit, `make build`                      | 41,551,154 |
| Difference                                     | **−2,410,480, or −5.48%** |

Both built `CGO_ENABLED=0 go build -tags release`, the baseline from a `git archive` of
`afe3db6` with `-ldflags "-s -w"`. The version-stamping `-X` flags `make build` adds cost **0
bytes** — measured, the two byte counts were identical at an earlier commit in this session.
The plan's 15% growth threshold is not approached; the binary shrank. No test asserts a size
bound: that breaks on the next dependency bump for no signal.

## The live half

`make validate-clouds`, run on this tree. It reaches the AWS Spot feeds and the anonymous Azure
Retail Prices API; it needs no credential of any kind.

| Check                                        | Verdict | Observed                                     |
| -------------------------------------------- | ------- | -------------------------------------------- |
| AWS `list` and `recommend` from live feeds   | pass    | `data_source.mode` = `live`, rows returned   |
| Azure `list` and `recommend`, anonymous      | pass    | `data_source.mode` = `live`, rows returned   |
| GCP from the committed snapshot              | pass    | `data_source.mode` = `embedded-snapshot`     |
| GCP `--gcp-billing-key`                      | **skipped** | no `SPOTINFO_GCP_BILLING_KEY` in the environment. The Cloud Billing Catalog API needs a key and there is no anonymous GCP price source to substitute. The test skips with that reason and never fails |
| Every live path degrades to the snapshot     | pass    | all three clouds, both commands, every origin pointed at a closed port: exit 0, rows returned, `data_source.mode` = `embedded-snapshot` |

The Azure cell runs with `AZURE_SUBSCRIPTION_ID`, `AZURE_TENANT_ID` and `AZURE_CLIENT_ID`
cleared, which is what makes "anonymous" an observation rather than a claim.

The degradation check is the Safety note "never let a live path fail a run", checked rather
than assumed: the fetch is attempted — no `--offline` — refused by a closed port, and the answer
still arrives, saying where it came from.

## Recorded, not fixed

**Three capability refusals name the neutral capability rather than the flag the caller typed.**
`--az` on GCP prints `gcp: unsupported capability: zone_detail: …`, `--min-score` on Azure
prints `placement_score`, `--workload web` prints `risk`. The message is built in
`internal/cloud` and is shared with the MCP surface, whose argument names differ from the flag
names, so the flag cannot be named there without leaking CLI vocabulary into the neutral
domain. Mapping a capability back to a flag at the CLI boundary is ambiguous —
`CapabilityPlacementScore` is requested by `--with-score`, `--min-score` **or** `--sort score`.
Wording is Task 18's subject; the refusals themselves are correct, and every one of them exits
non-zero with an empty stdout.

**`--gcp-project` is accepted and ignored on GCP `list`.** Re-measured here and unchanged from
Task 15's ⚠️: `list --cloud gcp --gcp-project some-project` exits 0 with output byte-identical
to the same command without it. The vocabulary marks the flag for both commands and the
command-tree test enforces that; Task 4 raised it and Task 12 answered it the other way on
purpose. It is not in the refusal matrix above because it is not a refusal, and reversing two
earlier decisions is not a validation task's job.

## Gate

Run on this tree with `UPDATE_GOLDEN` and `REFRESH_MANIFESTS` cleared from the environment:

| Command                    | Exit | Note                                                        |
| -------------------------- | ---- | ----------------------------------------------------------- |
| `make build`               | 0    | 41,551,154 bytes                                            |
| `make test`                | 0    | all packages                                                |
| `make lint`                | 0    | 0 issues                                                    |
| `make verify-data`         | 0    | no parser, contract or manifest changed in this task        |
| `make verify-architecture` | 0    | `verdict: pass`, 0 cycles, 0 new high-risk unbalanced edges |
| `make validate-clouds`     | 0    | 4 passed, 1 skipped with a stated reason                    |

`go test ./cmd/spotinfo/ -run E2E -v` with no skip collects **27 top-level tests and 165
assertions in total, 0 failures** — non-zero, which is what the `TestE2E` infix rule exists to
guarantee.
