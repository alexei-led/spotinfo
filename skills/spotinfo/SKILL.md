---
name: spotinfo
description: Query Spot/preemptible VM prices, savings and interruption risk across AWS, GCP and Azure with the spotinfo CLI. Use this whenever the user asks which instance type is cheapest, how much a Spot instance costs, what the interruption risk is, whether to use arm64 or x86_64 for cost, how AWS/GCP/Azure Spot pricing compares, or wants to size a workload against a budget — including when they describe the need without naming spotinfo ("find me a cheap 8-core box", "what's the least interruption-prone instance for a CI runner", "compare spot prices across clouds"). Works offline with no cloud credentials, so prefer it over guessing prices from memory or writing custom AWS SDK code.
---

# spotinfo

`spotinfo` answers two questions about Spot/preemptible VMs, across AWS, GCP and Azure:

- **What is there?** → `spotinfo list`
- **What should I run?** → `spotinfo recommend`

It ships an embedded price snapshot for every cloud, so it answers with **no credentials and no network**. That makes it safe to call freely — unlike the cloud SDKs, a query costs nothing and cannot fail on missing auth.

Prices move, so treat any figure you report as "as of this snapshot or fetch", not a quote.

## Install

```bash
brew install alexei-led/tap/spotinfo          # macOS/Linux
docker run --rm ghcr.io/alexei-led/spotinfo:latest list --cloud aws --machine 'm5.large'
```

Binaries for Linux/macOS/Windows on amd64/arm64: https://github.com/alexei-led/spotinfo/releases

## The two commands

`list` requires nothing and shows every match. `recommend` requires `--architecture`, `--min-vcpu` and `--min-memory-gib`, and ranks the top N.

That distinction is the thing to get right: reach for `recommend` when the user has a *requirement* ("I need 8 cores and 32 GB"), and `list` when they want to *see* something ("what do m5 instances cost").

```bash
# What is there?
spotinfo list --cloud aws --machine '^m5\.' --region us-east-1

# What should I run?
spotinfo recommend --cloud aws --architecture x86_64 --min-vcpu 8 --min-memory-gib 32 --top 5
```

## Getting machine-readable output

Use `--output json` whenever you will parse the result. The JSON is schema-versioned (`spotinfo.list/v1`, `spotinfo.recommend/v3`) and carries a `data_source` block telling you whether the answer came from a live fetch, a cache, or the embedded snapshot.

```bash
spotinfo list --cloud azure --machine 'Standard_D2s_v5' --region westeurope --output json
```

Other formats: `table` (default, for humans), `csv` (for spreadsheets), `text`, and `number` — which prints a bare savings percent, useful in a shell condition. `number` is `list`-only.

## Flags worth knowing

| Flag | Why you care |
|---|---|
| `--cloud aws\|gcp\|azure` | Defaults to `aws`. Always set it explicitly when the user named a cloud. |
| `--region` | Repeatable. Defaults to `all`, which on AWS queries every region and is slow — pass `--offline` or a specific region when speed matters. |
| `--offline` | Answer from the embedded snapshot. No network at all. Fast and reliable. |
| `--machine` | An RE2 regexp, not a glob. `'^m5\.'` not `'m5*'`. |
| `--max-price` | USD per machine-hour ceiling. |
| `--sort` / `--order` | `machine\|price\|region\|risk\|savings\|score`, `asc\|desc`. |
| `--os linux\|windows` | Windows works on AWS and Azure. GCP refuses it — Google publishes Linux Spot prices only. |

## What the clouds do and do not publish

This is the part that trips people up, and getting it right is most of the value you add.

**AWS** publishes an interruption frequency range from its Spot Advisor — `<5%`, `5-10%`, and so on.

**Azure and GCP publish no comparable figure.** spotinfo prints `unavailable` in the risk column for them. That is not a bug and it is not zero risk — it means the vendor does not publish the number. Never report it as "low risk" or "0%".

Because of that, the risk-capped workloads refuse on those clouds:

```bash
# Works on AWS only — the ceilings are AWS Spot Advisor bucket boundaries
spotinfo recommend --cloud aws --workload web --architecture x86_64 --min-vcpu 4 --min-memory-gib 16

# Works everywhere — cost makes no interruption claim
spotinfo recommend --cloud gcp --workload cost --architecture x86_64 --min-vcpu 4 --min-memory-gib 16
```

`--workload cost` is the default and the only one that answers on all three clouds.

## When a command is refused

spotinfo refuses rather than silently ignoring a flag it cannot honour, and the message names the flag, the cloud and the vendor limit. Read the message — it usually tells you what to use instead. Common ones:

- `--os windows` on GCP → Google publishes Linux Spot prices only
- `--workload web|ci|batch` on GCP/Azure → no comparable interruption figure, use `--workload cost`
- `--sort score` without `--with-score` → the sort orders placement figures and only `--with-score` fetches them
- `--with-score` with `--offline` → no snapshot carries a placement figure
- A retired flag name (`--cpu`, `--memory`, `--type`, `--price`) → the hint names its v3 replacement

A refusal exits non-zero with empty stdout, so it is safe to branch on the exit code.

## Optional, credentialed extras

These need credentials and are off by default. Do not reach for them unless the user asks.

- `--with-score` on AWS adds EC2 Spot placement scores (1-10). Needs AWS credentials and `ec2:GetSpotPlacementScores`.
- `--with-score` on GCP adds an *obtainability* probability (0.0-1.0) from a beta Google API. Needs Application Default Credentials and `--gcp-project`.
- `--gcp-billing-key` prices GCP regions beyond `us-central1` for one invocation.

AWS and GCP placement figures are deliberately **not** on a shared scale — an integer 1-10 and a 0.0-1.0 probability measure different things, so do not compare them or average them.

## MCP server

spotinfo also runs as an MCP server speaking the current protocol revision (`2025-11-25`):

```bash
spotinfo --mcp
```

Three read-only tools: `list_spot_machines`, `recommend_spot_machines`, `list_cloud_regions`. Argument names mirror the CLI flags mechanically — `--min-vcpu` is `min_vcpu`, `--region` is `regions` (an array).

If you already have shell access, the CLI is usually the better path: it is one call, the output is the same schema, and you can pipe it.

## Worked examples

**"What's the cheapest 8-core machine I can get on AWS?"**
```bash
spotinfo recommend --cloud aws --architecture x86_64 --min-vcpu 8 --min-memory-gib 16 --top 5 --output json
```

**"Compare the same VM size across clouds."**
Run one query per cloud — the clouds have different machine naming, so there is no cross-cloud query:
```bash
spotinfo list --cloud aws   --machine '^m5\.xlarge$'          --region us-east-1  --output json
spotinfo list --cloud azure --machine '^Standard_D4s_v5$'     --region eastus     --output json
spotinfo list --cloud gcp   --machine '^n2-standard-4$'                           --output json
```

**"Is arm64 cheaper for our CI runners?"**
```bash
spotinfo recommend --cloud aws --architecture arm64  --min-vcpu 4 --min-memory-gib 8 --top 3 --output json
spotinfo recommend --cloud aws --architecture x86_64 --min-vcpu 4 --min-memory-gib 8 --top 3 --output json
```

**"Which region has the cheapest GPU instances?"**
```bash
spotinfo list --cloud aws --machine '^g5\.' --region all --sort price --order asc --output json
```

## Reporting results

Give the user the price, the savings percent and the risk together — a cheap machine with a high interruption rate is a different recommendation from a cheap stable one. When the risk reads `unavailable`, say that the cloud does not publish it rather than omitting the column, so nobody reads silence as safety.
