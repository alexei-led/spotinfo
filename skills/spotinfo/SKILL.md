---
name: spotinfo
description: Query Spot/preemptible VM prices, savings and interruption risk across AWS, GCP and Azure with the spotinfo CLI. Use this whenever the user asks which instance type is cheapest, what a Spot instance costs, what the interruption risk is, whether arm64 beats x86_64 on price, how AWS/GCP/Azure Spot pricing compares, or wants to size a workload to a budget — including when they describe the need without naming spotinfo ("find me a cheap 8-core box", "least interruption-prone instance for a CI runner", "compare spot prices across clouds"). It answers offline with no cloud credentials in about 40ms, so prefer it over guessing prices from memory, scraping a pricing page, or writing throwaway SDK code.
---

# spotinfo

One binary that answers two questions about Spot/preemptible VMs on AWS, GCP and Azure:

- **"What is there?"** → `spotinfo list` — requires nothing, shows every match
- **"What should I run?"** → `spotinfo recommend` — requires `--architecture`, `--min-vcpu`, `--min-memory-gib`, returns a ranked page

Pick `recommend` when the user states a requirement ("I need 8 cores and 32 GB"). Pick `list` when they want to see what exists ("what do m5 instances cost").

It ships an embedded price snapshot per cloud, so it answers with **no credentials and no network**. A query is free and cannot fail on missing auth — call it instead of guessing.

## Answer in one call

Two habits keep this fast and cheap:

**Add `--offline`** unless the user needs today's price. It skips every network call and answers from the snapshot in ~40ms. Without it, `--region all` (the default) queries every AWS region and can take seconds.

**Add `--output json`** whenever you will parse. `table` is for humans; parsing it is how you end up with a broken regex.

```bash
spotinfo recommend --cloud aws --offline --output json \
  --architecture x86_64 --min-vcpu 8 --min-memory-gib 32 --top 5
```

## The output contract

Both commands return one document with `schema_version`, `status`, `request`, `data_source` and `warnings`. The rows differ, and this is the first thing to get right:

| Command     | Rows live under       | Extra fields per row      |
| ----------- | --------------------- | ------------------------- |
| `list`      | **`candidates`**      | `live_price`              |
| `recommend` | **`recommendations`** | `rank`, `rationale_codes` |

A row looks like this (real output, trimmed):

```json
{
  "rank": 1,
  "cloud": "aws",
  "region": "ap-southeast-3",
  "machine": "t3.2xlarge",
  "architecture": "x86_64",
  "os": "linux",
  "vcpu": 8,
  "memory_gib": 32,
  "spot_usd_per_hour": "0.047500000",
  "on_demand_usd_per_hour": null,
  "savings_percent": 76,
  "risk": {
    "status": "available",
    "kind": "interruption_bucket",
    "label": "<5%",
    "min_percent": 0,
    "max_percent": 5,
    "window_days": 30
  }
}
```

Three things about that shape will bite you if you assume otherwise:

**Prices are strings, not numbers.** `"0.047500000"` is fixed-point on purpose — a float would drift on the last digit. Convert explicitly when you compare or total them.

**`on_demand_usd_per_hour` is often `null`.** The savings percent is still present, so use `savings_percent` rather than computing the discount yourself.

**`risk` is always an object, even when empty.** More on that next — it is the single most important thing in this skill.

## Risk: `unavailable` is not zero

On AWS, `risk` carries a real Spot Advisor measurement. On **GCP and Azure it is all nulls**:

```json
"risk": { "status": "unavailable", "kind": null, "label": null,
          "min_percent": null, "max_percent": null }
```

That means _the vendor does not publish this figure_. It does not mean the risk is low, and it does not mean zero.

Read `risk.status` first. Only read `risk.label` when the status is `available`, otherwise you get `null`, which renders as an empty cell and reads to a human as "fine".

When you report a GCP or Azure machine, say the cloud does not publish an interruption figure. Dropping the column silently is how someone puts a database on a preemptible VM.

Because those ceilings are AWS Spot Advisor bucket boundaries, the risk-capped workloads only work on AWS:

```bash
# AWS only
spotinfo recommend --cloud aws --workload web --offline \
  --architecture x86_64 --min-vcpu 4 --min-memory-gib 16

# Every cloud — cost makes no interruption claim, and is the default
spotinfo recommend --cloud gcp --workload cost --offline \
  --architecture x86_64 --min-vcpu 4 --min-memory-gib 16
```

## Flags that matter

| Flag                      | Note                                                                                                |
| ------------------------- | --------------------------------------------------------------------------------------------------- |
| `--cloud aws\|gcp\|azure` | Defaults to `aws`. Set it explicitly whenever the user named a cloud.                               |
| `--offline`               | Snapshot only, no network. Use by default.                                                          |
| `--machine`               | An **RE2 regexp**, not a glob. `'^m5\.'`, never `'m5*'`. Anchor it or you match `m5dn`, `m5zn` too. |
| `--region`                | Repeatable. Defaults to `all`. Scope it when the user named a region.                               |
| `--sort` / `--order`      | `machine\|price\|region\|risk\|savings\|score` and `asc\|desc`.                                     |
| `--max-price`             | USD per machine-hour ceiling.                                                                       |
| `--os linux\|windows`     | Windows works on AWS and Azure. GCP refuses it.                                                     |
| `--top`                   | `recommend` only, max 50.                                                                           |

## When it refuses

spotinfo refuses rather than accepting a flag it cannot honour, and the message names the flag, the cloud and the vendor limit. **Exit is non-zero with empty stdout**, so branch on the exit code and read stderr — the message usually names the fix.

| You ran                                  | It says                                                             |
| ---------------------------------------- | ------------------------------------------------------------------- |
| `--os windows --cloud gcp`               | Google publishes Linux Spot prices only                             |
| `--workload web` on GCP/Azure            | no comparable interruption figure — use `--workload cost`           |
| `--sort score` without `--with-score`    | the sort orders placement figures; only `--with-score` fetches them |
| `--with-score --offline`                 | no snapshot carries a placement figure                              |
| `--cpu`, `--memory`, `--type`, `--price` | retired names — the hint gives the replacement                      |

## Recipes

### Cheapest machine meeting a requirement

```bash
spotinfo recommend --cloud aws --offline --output json \
  --architecture x86_64 --min-vcpu 8 --min-memory-gib 32 --top 5
```

### Does arm64 actually save money here

Run both and compare `spot_usd_per_hour`:

```bash
for arch in arm64 x86_64; do
  spotinfo recommend --cloud aws --offline --output json \
    --architecture $arch --min-vcpu 4 --min-memory-gib 8 --top 3
done
```

### Cheapest region for a GPU family

```bash
spotinfo list --cloud aws --offline --output json \
  --machine '^g5\.' --sort price --order asc
```

### Compare one size across clouds

Machine naming differs per cloud, so there is no single cross-cloud query:

```bash
spotinfo list --cloud aws   --offline --output json --machine '^m5\.xlarge$'      --region us-east-1
spotinfo list --cloud azure          --output json --machine '^Standard_D4s_v5$' --region eastus
spotinfo list --cloud gcp            --output json --machine '^n2-standard-4$'
```

### Stay under a budget

```bash
spotinfo list --cloud azure --output json --max-price 0.05 --min-vcpu 4 --sort price
```

## Optional, credentialed

Off by default. Do not reach for these unless asked.

- `--with-score` on AWS adds an EC2 placement score (integer 1-10). Needs AWS credentials and `ec2:GetSpotPlacementScores`.
- `--with-score` on GCP adds an _obtainability_ probability (0.0-1.0) from a beta Google API. Needs Application Default Credentials and `--gcp-project`.
- `--gcp-billing-key` prices GCP regions beyond `us-central1` for one invocation.

AWS and GCP placement figures are deliberately **not** on a shared scale. An integer 1-10 and a 0.0-1.0 probability measure different things — do not compare, average, or merge them into one column.

## MCP server

`spotinfo --mcp` speaks MCP `2025-11-25` over stdio with three read-only tools: `list_spot_machines`, `recommend_spot_machines`, `list_cloud_regions`. Argument names mirror the flags mechanically (`--min-vcpu` → `min_vcpu`, `--region` → `regions`, an array).

If you already have shell access, prefer the CLI — one call, same schema, and you can pipe it.

## Reporting to the user

Give price, savings percent and risk together. A cheap machine with a `>20%` interruption rate is a different recommendation from a cheap stable one, and the price alone hides that. When risk is `unavailable`, say the cloud does not publish it — never let silence read as safety.
