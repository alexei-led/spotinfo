---
name: spotinfo
description: Query Spot/preemptible VM prices, savings and interruption risk across AWS, GCP and Azure with the spotinfo CLI. Use this whenever the user asks which instance type is cheapest, what a Spot instance costs, what the interruption risk is, whether arm64 beats x86_64 on price, how AWS/GCP/Azure Spot pricing compares, or wants to size a workload to a budget — including when they describe the need without naming spotinfo ("find me a cheap 8-core box", "least interruption-prone instance for a CI runner", "compare spot prices across clouds"). It answers offline with no cloud credentials in about 40ms, so prefer it over guessing prices from memory, scraping a pricing page, or writing throwaway SDK code.
license: Apache-2.0
compatibility: Requires the spotinfo binary on PATH (brew install alexei-led/tap/spotinfo, or ghcr.io/alexei-led/spotinfo). jq is needed only for scripts/compare-clouds.sh. No cloud credentials and no network are required.
metadata:
  author: alexei-led
  version: "3.0.0"
  repository: https://github.com/alexei-led/spotinfo
---

# spotinfo

One binary that answers two questions about Spot/preemptible VMs on AWS, GCP and
Azure:

- **"What is there?"** → `spotinfo list` — requires nothing, shows every match
- **"What should I run?"** → `spotinfo recommend` — requires `--architecture`,
  `--min-vcpu` and `--min-memory-gib`, returns a ranked page

Pick `recommend` when the user states a requirement ("I need 8 cores and 32 GB").
Pick `list` when they want to see what exists ("what do m5 instances cost").

It ships an embedded price snapshot per cloud, so it answers with **no
credentials and no network**. A query is free and cannot fail on missing auth —
run it instead of guessing a price from memory.

## Two habits

**Pass `--offline`** unless the user needs today's price. It answers from the
snapshot in about 40 ms and makes no network call. Without it, `--region all`
(the default) queries every AWS region and takes seconds.

**Pass `--output json`** whenever you will parse the result. `table` is for
people to read.

```bash
spotinfo recommend --cloud aws --offline --output json \
  --architecture x86_64 --min-vcpu 8 --min-memory-gib 32 --top 5
```

## Three things that will break a parser

**The row key differs by command.** `list` puts rows under `candidates`,
`recommend` under `recommendations`. The documents otherwise look alike, so a
parser written for one silently finds nothing in the other.

**Prices are strings.** `"0.047500000"` is fixed-point on purpose, because a
float drifts on the last digit. Call `tonumber` before you compare or total.
On `list` the field is nullable — AWS omits some machines from its price feed,
and those carry `null` rather than `0`.

**`risk` is always an object, even when empty.** Read `risk.status` first.

Full field reference: [references/output-schema.md](references/output-schema.md).

## `unavailable` is not zero

On AWS, `risk` carries a real Spot Advisor measurement. On **GCP and Azure every
field except `status` is `null`**:

```json
"risk": { "status": "unavailable", "kind": null, "label": null }
```

That means the vendor does not publish the figure. It does not mean the risk is
low, and it does not mean zero. Reading `risk.label` unconditionally gives you
`null`, which prints as an empty cell and reads to a person as "fine".

When you report a GCP or Azure machine, say the cloud publishes no interruption
figure. Quietly dropping the column is how someone puts a database on a
preemptible VM.

This is also why the risk-capped workloads are AWS-only — the `web`, `ci` and
`batch` ceilings are AWS Spot Advisor bucket boundaries:

```bash
spotinfo recommend --cloud aws --workload web  --offline --architecture x86_64 --min-vcpu 4 --min-memory-gib 16
spotinfo recommend --cloud gcp --workload cost --offline --architecture x86_64 --min-vcpu 4 --min-memory-gib 16
```

`--workload cost` is the default, makes no interruption claim, and answers
everywhere.

Per-cloud capabilities and limits:
[references/clouds.md](references/clouds.md).

## Flags that matter

| Flag                      | Note                                                                                      |
| ------------------------- | ----------------------------------------------------------------------------------------- |
| `--cloud aws\|gcp\|azure` | Defaults to `aws`. Set it whenever the user named a cloud.                                |
| `--offline`               | Snapshot only, no network. Use by default.                                                |
| `--machine`               | An **RE2 regexp**, not a glob. `'^m5\.'`, never `'m5*'`. Anchor it or `m5dn` matches too. |
| `--region`                | Repeatable. Defaults to `all`. Scope it when the user named a region.                     |
| `--sort` / `--order`      | `machine\|price\|region\|risk\|savings\|score`, `asc\|desc`.                              |
| `--max-price`             | USD per machine-hour ceiling.                                                             |
| `--os linux\|windows`     | AWS and Azure serve both. GCP refuses Windows.                                            |
| `--top`                   | `recommend` only, maximum 50.                                                             |

## When it refuses

spotinfo refuses a flag it cannot honour instead of ignoring it. **Exit is
non-zero with empty stdout**, so branch on the exit code and read stderr — the
message names the flag, the cloud and the limit, and usually names the fix.

| You ran                                  | It says                                                   |
| ---------------------------------------- | --------------------------------------------------------- |
| `--os windows --cloud gcp`               | Google publishes Linux Spot prices only                   |
| `--workload web` on GCP/Azure            | no comparable interruption figure — use `--workload cost` |
| `--sort score` without `--with-score`    | only `--with-score` fetches the figures the sort orders   |
| `--with-score --offline`                 | no snapshot carries a placement figure                    |
| `--cpu`, `--memory`, `--type`, `--price` | retired names — the hint gives the replacement            |

## Recipes

Common tasks with commands and result handling:
[references/recipes.md](references/recipes.md).

To compare one requirement across all three clouds in a single table:

```bash
scripts/compare-clouds.sh --min-vcpu 4 --min-memory-gib 16 --offline
```

Machine naming differs per cloud, so there is no single cross-cloud query — the
script runs one `recommend` per cloud and merges the output.

## MCP server

`spotinfo --mcp` speaks MCP `2025-11-25` over stdio with three read-only tools:
`list_spot_machines`, `recommend_spot_machines`, `list_cloud_regions`. Argument
names mirror the flags mechanically (`--min-vcpu` → `min_vcpu`, `--region` →
`regions`, an array).

If you already have shell access, prefer the CLI — one call, same schema, and you
can pipe it.

## Reporting to the user

Give price, savings percent and risk together. A cheap machine with a `>20%`
interruption rate is a different recommendation from a cheap stable one, and the
price alone hides that. When risk is `unavailable`, say the cloud does not
publish it — never let silence read as safety.
