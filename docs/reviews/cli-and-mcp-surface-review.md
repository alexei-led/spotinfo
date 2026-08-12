# CLI and MCP surface review

Date: 2026-08-11. Branch: `feat/spotinfo-multicloud-v2`.

This review measures the command surface after GCP and Azure landed. It asks one question:
how much must a person learn before they can ask spotinfo a question and trust the answer?

Every number below comes from the compiled binary at `06220c6`, from the code, or from the
committed snapshots. No number comes from prose in `README.md` or `CLAUDE.md`.

Findings are in two tiers. Tier 1 changes nothing a client can observe in a payload or a
golden file. Tier 2 changes a published contract and needs an explicit decision.

---

## 1. Method

```bash
make build
.bin/spotinfo recommend --cloud <id> --architecture x86_64 --cpu 2 --memory 8 --output json
printf '...' | .bin/spotinfo --mcp --quiet     # JSON-RPC over stdio
```

New end-to-end tests in `cmd/spotinfo/e2e_test.go` run the compiled binary as a subprocess.
They pin the behavior this review describes, so a later change to any of it fails a test.

---

## 2. The headline: one question, two answers

`--workload` selects the payload schema. This is not visible from the flag name.

| Cloud      | Workload                         | Schema                  |
| ---------- | -------------------------------- | ----------------------- |
| aws        | `cost`                           | `spotinfo.recommend/v2` |
| aws        | `web`, `ci`, `batch`             | `spotinfo.recommend/v1` |
| gcp, azure | `cost` (the only accepted value) | `spotinfo.recommend/v2` |

The default value of `--workload` is not the same on the two surfaces:

|                                               | Default workload | Default regions | Resulting schema |
| --------------------------------------------- | ---------------- | --------------- | ---------------- |
| CLI `spotinfo recommend --cloud aws`          | `web`            | `us-east-1`     | v1               |
| MCP `recommend_spot_instances` (`cloud: aws`) | `cost`           | every region    | v2               |

The same request therefore returns two different documents with two different field names,
two different ranking policies, and a different first result. Measured:

```
CLI  → schema v1, regions ["us-east-1"], policy starts price, interruption
MCP  → schema v2, regions ["all"],       policy starts price, excess vCPU
```

The rule is defensible on its own: AWS under a risk-aware workload keeps a byte-compatible
v1 answer for clients that predate the provider seam. The cost is that a reader who does not
set `--workload` cannot predict which schema arrives.

`cmd/spotinfo/e2e_test.go` pins both halves of this in
`TestTheWorkloadChoosesThePayloadSchema` and
`TestTheDefaultWorkloadSelectsADifferentSchemaOnEachSurface`.

---

## 3. Flag count against concept count

27 distinct flag names across the two surfaces. They express 18 concepts.

| Concept             | Names for it                                             |
| ------------------- | -------------------------------------------------------- |
| Machine-type filter | `--type`, `--instance`, `--machine`                      |
| Minimum vCPU        | `--cpu`, `--vcpu`                                        |
| Minimum memory      | `--memory`, `--memory-gib`                               |
| Price ceiling       | `--price` (root), `--budget` (recommend)                 |
| Placement score     | `--with-score`, `--min-score`, `--az`, `--score-timeout` |
| Fetch policy        | `--offline`, `--refresh`                                 |
| Live GCP risk       | `--live-risk`, `--gcp-project`                           |

Ten of the 27 names repeat a concept that another name already expresses.

---

## 4. Flags whose meaning depends on something else

A flag that is accepted but does nothing is worse than a flag that is refused. The command
reports success, and the person believes the filter was applied.

| Flag              | Accepted when | Does nothing when                                                                               |
| ----------------- | ------------- | ----------------------------------------------------------------------------------------------- |
| `--offline`       | always        | `--cloud gcp`, `--cloud azure` — both clouds answer from a snapshot and never open a connection |
| `--refresh`       | always        | `--cloud gcp`, `--cloud azure` — same reason                                                    |
| `--az`            | always        | `--with-score` is absent                                                                        |
| `--min-score`     | always        | `--with-score` is absent                                                                        |
| `--score-timeout` | always        | `--with-score` is absent                                                                        |
| `--gcp-project`   | always        | the cloud is not GCP                                                                            |

Two more flags change meaning with the cloud:

- `--region` defaults to `us-east-1` on AWS and to every published region on GCP and Azure.
- `--output` accepts five formats on the query command and two on `recommend`.

`--live-risk` is the counter-example that shows the pattern is fixable: on AWS and Azure it
returns an error instead of being ignored.

---

## 5. Names drift between the CLI and MCP

| Concept        | CLI flag                | MCP argument                                |
| -------------- | ----------------------- | ------------------------------------------- |
| Regions        | `--region`              | `regions`                                   |
| Machine filter | `--type` / `--instance` | `instance_types` (v1), `machine` (v2)       |
| Minimum vCPU   | `--cpu`                 | `min_vcpu`                                  |
| Minimum memory | `--memory`              | `min_memory_gb` (v1), `min_memory_gib` (v2) |
| Price ceiling  | `--price` / `--budget`  | `max_price_per_hour`                        |
| Sort key       | `--sort`                | `sort_by`                                   |

`min_memory_gb` in `find_spot_instances` is the one outright error in the list. The value is
GiB, and the v2 tool names the same value `min_memory_gib`.

---

## 6. MCP findings

### 6.1 Provenance is 94% of an Azure payload

An Azure recommendation for three machines returns 36,278 bytes. 34,159 of them — 94% — are
the `data_source.sources` array: 81 entries, one per contracted region query. The three
ranked machines occupy 1,601 bytes, or 4%.

```
total=36278  sources=34159 (94%)  recommendations=1601 (4%)
source entries: 81   regions in the answer: australiacentral2, centralindia
```

Over MCP the same call returns 37,911 bytes. For a model with a token budget, one Azure
recommendation costs roughly ten thousand tokens, and 94% of them describe regions that no
returned machine came from.

The array is correct: the updater read all 81 documents to rank across every region. The
question is whether a per-call answer is the right place to republish all of it.

### 6.2 v1 tools report errors as bare strings

`find_spot_instances` and `list_spot_regions` return `mcp.NewToolResultError(message)` with
plain text. `recommend_spot_instances` returns a `spotinfo.error/v1` JSON body with a stable
`code` field. A client can branch on a v2 error. It can only pattern-match a v1 error.

### 6.3 Tool annotations are available and unused

`github.com/mark3labs/mcp-go v0.57.0` exposes `WithReadOnlyHintAnnotation`,
`WithIdempotentHintAnnotation`, `WithDestructiveHintAnnotation` and
`WithOpenWorldHintAnnotation`. All three spotinfo tools read data and change nothing. None of
them declares a hint. Clients use these hints to decide whether to run a tool without asking
the person first.

### 6.4 `list_spot_regions` does not name its cloud

The description says "List all AWS regions where EC2 Spot Instances are available", which is
accurate. The name does not. A model that chooses a tool from a name list can call it for a
GCP question and receive AWS regions with no error.

### 6.5 The two paths diagnose an empty result differently

Both paths explain why nothing matched. The neutral v2 path names the screen and the price
floor it failed to reach. The v1 AWS path names only the screen, and the screen it names is
not always the one the reader changed.

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --cpu 4 --memory 16 --budget 0.001
spotinfo: no candidates: no machine costs 0.001000000 USD per hour or less; gcp publishes
nothing below 0.042496000 USD per hour, the price of c3d-standard-4 in us-central1

$ spotinfo recommend --architecture x86_64 --cpu 4 --memory 16 --budget 0.05 --offline
spotinfo: no recommendation candidates: no instance type is classified as x86_64
```

The second message is true. Acquisition applies the budget first, and the only AWS machines
in `us-east-1` with 4 vCPU, 16 GiB and a price at or below $0.05 are `m6g.xlarge` and
`c6gd.2xlarge`, both arm64. `internal/spot/diagnose.go` then reports the first screen that
empties the remaining set, which is architecture.

The reader set a budget and was told about architecture. The screens acquisition already
applied are not named, so the sentence reads as "this cloud has no x86_64 capacity".

**Tier 1 fix:** in `noRecommendationCandidates`, name the acquisition screens alongside the
failing stage when `opts.Budget`, `opts.CPU`, `opts.Memory` or `opts.Instance` is set.
`describeConstraints` already builds that string for the fully-empty case at
`internal/spot/diagnose.go:40`. This changes an error string only.

---

## 7. Tier 1 — no observable contract change

Each item below leaves every golden file and every published schema unchanged.

1. **Add read-only and idempotent annotations to all three MCP tools.** Annotations are
   metadata beside the tool definition. No payload changes.
2. **Refuse `--offline` and `--refresh` on GCP and Azure**, the way `--live-risk` is already
   refused off GCP. An error is a smaller surprise than a silent no-op. Alternative, if a
   refusal is too strict for a script that loops over clouds: write one line to stderr.
3. **Refuse `--az`, `--min-score` and `--score-timeout` without `--with-score`.**
4. **Rename `min_memory_gb` to `min_memory_gib` in `find_spot_instances`,** keeping the old
   name as an accepted alias. The unit label is wrong today.
5. **Name the cloud in `list_spot_regions`.** Add `aws` to the tool name, or add an optional
   `cloud` argument that refuses any value except `aws` with `UNSUPPORTED_CAPABILITY`.
6. **Say in `--workload` help text that the value selects the payload schema**, and say in
   the `recommend_spot_instances` description that its default is `cost`.
7. **Add `offline` as a tool argument on `recommend_spot_instances`.** Be precise about what
   is missing: the operator who launches the server *can* already force it, because
   `--offline` is a root flag that composes with `--mcp`. Measured on the shipped binary with
   an AWS tool call: `spotinfo --offline --mcp` answers in 203 ms with
   `data_source.mode: embedded-snapshot`, against 8,149 ms and `live` without it. What is
   missing is per-call control: the model calling the tool cannot ask for the fast path, and
   the operator's choice is fixed for the life of the process.
8. **Name the acquisition screens in the v1 empty-result message.** See section 6.5.

Items 2 and 3 turn a silent success into an error. That is a behavior change for a script
that passes the flag today and ignores the result, but it changes no payload field.

---

## 8. Tier 2 — breaking, needs a decision

Do not apply these without approval. Each one changes a document that a client parses, or a
file that a golden test pins.

1. **Align the default workload across the CLI and MCP.** Pick one value. This changes which
   schema an unqualified request receives, and `internal/mcp/testdata/` pins the current v2
   request echo.
2. **Trim `data_source.sources` to the regions that appear in the answer**, and publish the
   full list once, through an MCP resource or a separate tool. This removes 94% of an Azure
   payload. It changes the v2 success schema.
3. **Fold the query command into `recommend`.** The query command exists because it renders
   an interruption column and answers AWS only. Folding it removes a whole surface, the
   `--type`/`--instance` split, the `--price`/`--budget` split, and three output formats. It
   breaks every v1 golden in `cmd/spotinfo/testdata/`.
4. **Retire `spotinfo.recommend/v1`.** One schema for every cloud and both surfaces. This is
   the only change that removes the finding in section 2 completely.

Options 3 and 4 are the same decision at two sizes. Neither is worth doing for tidiness
alone. Both become worth doing when a fourth cloud lands, because each new provider must
otherwise answer the question "which of these two shapes do I emit, and why".

---

## 9. Not recommended

- **Do not rename `--cpu` to `--vcpu` or `--memory` to `--memory-gib` as the primary name.**
  The aliases already work, and the churn buys nothing.
- **Do not add the v1 output formats to `recommend`.** `number` prints one savings percent,
  which has no meaning for a multi-cloud ranking.
- **Do not make `--workload web` work on GCP with live risk.** Google measures preempted
  Spots against Spots that stopped running. AWS measures the fraction of running instances
  interrupted. The ceilings are AWS bucket boundaries and do not transfer.

---

## 10. What the new tests pin

`cmd/spotinfo/e2e_test.go` builds the binary once and runs it as a subprocess. It needs no
credentials and no network. One test proves the offline claim by pointing `HTTP_PROXY` and
`HTTPS_PROXY` at a closed port and requiring the same answer.

| Test                                                         | What it holds                                |
| ------------------------------------------------------------ | -------------------------------------------- |
| `TestEveryCloudAnswersFromTheShippedBinary`                  | Table columns per cloud, and `--top`         |
| `TestJSONReportIsValidForEveryCloud`                         | v2 shape, provenance hashes, resource floors |
| `TestTheWorkloadChoosesThePayloadSchema`                     | The rule in section 2                        |
| `TestTheDefaultWorkloadSelectsADifferentSchemaOnEachSurface` | The divergence in section 2                  |
| `TestACloudWithoutRiskDataReportsUnavailableRatherThanZero`  | Absent risk is never a zero                  |
| `TestOfflineAnswersWithEveryOutboundRequestBlocked`          | No request is made                           |
| `TestRejectedInputExitsNonZeroWithAnEmptyStdout`             | Seven refusal paths                          |
| `TestAnOfflineAnswerIsReproducible`                          | Two runs give identical bytes                |
| `TestMCPServerCompletesAHandshakeAndAnswers`                 | stdio handshake and tool list                |
| `TestMCPAnswersEveryCloudInV2`                               | Every cloud answers v2 over MCP              |
