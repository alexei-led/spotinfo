# Usage Guide

`spotinfo` explores Spot machine prices across AWS, GCP and Azure from two commands:

- `spotinfo list` — every machine matching a filter, with its price and its risk.
- `spotinfo recommend` — the best machines for a stated requirement, ranked.

Both commands serve all three clouds. Both answer from embedded snapshots when asked to, so
the tool works with no credentials and no network.

New to the tool? Start with the [Quick start](quick-start.md). This page is the complete
reference. Every command below was run against the shipped binary and its output pasted
verbatim; prices move, so your numbers will differ.

## Command tree

```bash
spotinfo [global options] <list|recommend> [command options]
```

There is no root query command. Running `spotinfo` with no command prints the help on stdout
and refuses on stderr:

```console
$ spotinfo
spotinfo: invalid argument: no command given; run "spotinfo list" to browse or "spotinfo recommend" to rank
```

(exit 1)

```console
$ spotinfo --help
NAME:
   spotinfo - explore Spot machine prices across AWS, GCP and Azure

USAGE:
   spotinfo [global options] command [command options]

VERSION:
   v2.5.0-86-g2f2b80b

COMMANDS:
   list       list every matching machine with its price and risk
   recommend  rank the best Spot machines for a stated requirement (requires --architecture, --min-vcpu and --min-memory-gib)
   help, h    Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --mcp          run as MCP server instead of CLI (default: false)
   --debug        enable debug logging (default: false)
   --quiet        quiet mode (errors only) (default: false)
   --json-log     output logs in JSON format (default: false)
   --help, -h     show help
   --version, -v  print the version
```

### Flag placement

Those six global flags select **how the binary runs**. Every flag that describes a **query**
belongs to `list` or `recommend` and must come after the command name:

```console
$ spotinfo --cloud gcp list --machine '^n2-standard-2$'
spotinfo: flag provided but not defined: -cloud

$ spotinfo --offline list --region us-east-1 --machine '^m5.large$'
spotinfo: flag provided but not defined: -offline
```

Both exit 1. This includes `--offline` in front of `--mcp`: `spotinfo --offline --mcp` is
refused the same way. To make an MCP call answer from the snapshot, pass the tool argument
`offline: true` — see [MCP server](mcp-server.md).

## `spotinfo list`

Lists every machine that matches the filter. It applies no policy and drops nothing: a
machine the cloud publishes with no price is still listed, with `-` in place of the price.

```console
$ spotinfo list --help
NAME:
   spotinfo list - list every matching machine with its price and risk

USAGE:
   spotinfo list [command options]

OPTIONS:
   --cloud value                      cloud provider: aws|gcp|azure (default: aws)
   --machine value                    filter: machine type RE2 regexp pattern
   --architecture value               filter: processor architecture x86_64|arm64
   --os value                         machine operating system: linux|windows (default: "linux")
   --region value [ --region value ]  one or more provider regions, or "all" for every published region; "all" on AWS queries every region, so pass --offline or an explicit --region when speed matters (default: "all")
   --output value                     format output: number|text|json|table|csv (default: "table")
   --min-vcpu value                   filter: minimum vCPU cores (default: no filter)
   --min-memory-gib value             filter: minimum memory GiB (default: no filter)
   --max-price value                  filter: positive maximum USD per machine-hour (default: no filter)
   --sort value                       sort results by machine|price|region|risk|savings|score (default: the order the cloud publishes)
   --order value                      sort order asc|desc (default: asc)
   --offline                          answer from the embedded snapshot instead of fetching the live feeds (default: false)
   --refresh                          ignore any cached provider feed and fetch it again (default: false)
   --gcp-project value                Google Cloud project to bill authenticated GCP calls to (or $GOOGLE_CLOUD_PROJECT)
   --gcp-billing-key value            Cloud Billing Catalog API key that prices GCP regions beyond the committed snapshot for this invocation (or $SPOTINFO_GCP_BILLING_KEY)
   --with-score                       include provider placement figures (experimental; on gcp the figure is obtainability from a beta Google API, fetched for a recommendation and needing --gcp-project) (default: false)
   --min-score value                  filter: minimum spot placement score (1-10, needs --with-score; refused on a cloud whose placement figure is not an integer score) (default: no filter)
   --az                               request zone-level figures instead of region-level (use with --with-score) (default: false)
   --score-timeout value              timeout for placement enrichment, 1-300 seconds (default: 30)
   --help, -h                         show help
```

### `list` examples

AWS, one region, from the embedded snapshot:

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$'
┌──────────┬──────┬────────────┬────────────────────────┬──────┬──────────┐
│ MACHINE  │ VCPU │ MEMORY GIB │ SAVINGS OVER ON-DEMAND │ RISK │ USD/HOUR │
├──────────┼──────┼────────────┼────────────────────────┼──────┼──────────┤
│ m5.large │    2 │          8 │                    59% │ >20% │ 0.0399   │
└──────────┴──────┴────────────┴────────────────────────┴──────┴──────────┘
```

Two regions. The `REGION` column is dropped when the **request** names exactly one `--region`,
and is present otherwise — that is about the request, not the result, so the default
`--region all` keeps the column even on GCP, where it resolves to a single region:

```console
$ spotinfo list --offline --region us-east-1 --region eu-west-1 --machine '^m5\.large$'
┌───────────┬──────────┬──────┬────────────┬────────────────────────┬──────┬──────────┐
│ REGION    │ MACHINE  │ VCPU │ MEMORY GIB │ SAVINGS OVER ON-DEMAND │ RISK │ USD/HOUR │
├───────────┼──────────┼──────┼────────────┼────────────────────────┼──────┼──────────┤
│ us-east-1 │ m5.large │    2 │          8 │                    59% │ >20% │ 0.0399   │
├───────────┼──────────┼──────┼────────────┼────────────────────────┼──────┼──────────┤
│ eu-west-1 │ m5.large │    2 │          8 │                    40% │ >20% │ 0.0533   │
└───────────┴──────────┴──────┴────────────┴────────────────────────┴──────┴──────────┘
```

GCP. The committed snapshot serves `us-central1`; `RISK` reads `unavailable` because Google
publishes no interruption figure this tool may redistribute:

```console
$ spotinfo list --cloud gcp --machine '^n2-standard-(2|4|8)$'
┌─────────────┬───────────────┬──────┬────────────┬────────────────────────┬─────────────┬──────────┐
│ REGION      │ MACHINE       │ VCPU │ MEMORY GIB │ SAVINGS OVER ON-DEMAND │ RISK        │ USD/HOUR │
├─────────────┼───────────────┼──────┼────────────┼────────────────────────┼─────────────┼──────────┤
│ us-central1 │ n2-standard-2 │    2 │          8 │                    47% │ unavailable │ 0.0507   │
├─────────────┼───────────────┼──────┼────────────┼────────────────────────┼─────────────┼──────────┤
│ us-central1 │ n2-standard-4 │    4 │         16 │                    47% │ unavailable │ 0.1013   │
├─────────────┼───────────────┼──────┼────────────┼────────────────────────┼─────────────┼──────────┤
│ us-central1 │ n2-standard-8 │    8 │         32 │                    47% │ unavailable │ 0.2027   │
└─────────────┴───────────────┴──────┴────────────┴────────────────────────┴─────────────┴──────────┘
```

Azure, Linux and then Windows. Azure prices the licence-bundled Windows meters, so the two
prices differ:

```console
$ spotinfo list --cloud azure --region westeurope --machine '^Standard_D2as_v5$' --output text
machine=Standard_D2as_v5, vCPU=2, memory=8GiB, saving=81%, risk='unavailable', price=0.0192

$ spotinfo list --cloud azure --region westeurope --machine '^Standard_D2as_v5$' --os windows --output text
machine=Standard_D2as_v5, vCPU=2, memory=8GiB, saving=81%, risk='unavailable', price=0.0362
```

`--architecture` partitions a catalogue rather than searching it. In `us-east-1` the offline
snapshot lists 1145 Linux machines: 348 `arm64`, 797 `x86_64`, no overlap.

```console
$ spotinfo list --offline --region us-east-1 --architecture arm64 --output csv | head -4
Machine,vCPU,Memory GiB,Savings over On-Demand,Risk,USD/Hour,Price Source
a1.metal,16,32,0,<5%,0.408,static
c6g.metal,64,128,70,<5%,0.6574,static
c7g.4xlarge,16,32,54,<5%,0.1934,static
```

### Machines with no published price

`list` reports them rather than hiding them. The price renders as `-` in `table`, `text` and
`csv`, and as `null` in JSON:

```console
$ spotinfo list --offline --region us-east-1 --machine '^dl1\.24xlarge$' --output csv
Machine,vCPU,Memory GiB,Savings over On-Demand,Risk,USD/Hour,Price Source
dl1.24xlarge,96,768,41,<5%,-,-
```

Two consequences worth knowing before scripting against `list`:

- `--sort price --order asc` puts the unpriced rows **first**. Add `--max-price`, which drops
  them, or use `recommend`, which never proposes a machine without a positive price.
- On AWS this is usually a gap in the static price feed for a very new family. Without
  `--offline` and with AWS credentials, the live `DescribeSpotPriceHistory` fallback can fill
  it in; the row then reports `live_price: true` and a `Price Source` of `live`.

## `spotinfo recommend`

Ranks candidates for a requirement. `--architecture`, `--min-vcpu` and `--min-memory-gib` are
required — a recommendation with no size floor and no architecture is not a recommendation.

```console
$ spotinfo recommend --help
NAME:
   spotinfo recommend - rank the best Spot machines for a stated requirement (requires --architecture, --min-vcpu and --min-memory-gib)

USAGE:
   spotinfo recommend [command options]

OPTIONS:
   --cloud value                      cloud provider: aws|gcp|azure (default: aws)
   --architecture value               required machine architecture: x86_64|arm64
   --machine value                    machine type RE2 regexp (combined with architecture)
   --region value [ --region value ]  one or more provider regions, or "all" for every published region; "all" on AWS queries every region, so pass --offline or an explicit --region when speed matters (default: "all")
   --min-vcpu value                   required minimum vCPU cores (default: none, this flag is required)
   --min-memory-gib value             required minimum memory GiB (default: none, this flag is required)
   --max-price value                  positive maximum USD per candidate machine-hour (default: no filter)
   --os value                         machine operating system: linux|windows (default: "linux")
   --workload value                   ranking policy: cost|web|ci|batch (web, ci and batch cap interruption and need a cloud that publishes it) (default: "cost")
   --top value                        maximum recommendations to return (default: 3)
   --sort value                       print the ranked page ordered by machine|price|region|risk|savings|score (score needs --with-score, and not --az) (default: the ranking policy's own order)
   --order value                      sort order asc|desc (default: asc)
   --offline                          answer from the embedded snapshot instead of fetching the live feeds (default: false)
   --refresh                          ignore any cached provider feed and fetch it again (default: false)
   --output value                     format output: text|json|table|csv (default: "table")
   --gcp-billing-key value            Cloud Billing Catalog API key that prices GCP regions beyond the committed snapshot for this invocation (or $SPOTINFO_GCP_BILLING_KEY)
   --live-risk                        fetch live preemption risk from the provider's authenticated API (GCP only; needs credentials) (default: false)
   --gcp-project value                Google Cloud project to bill authenticated GCP calls to (or $GOOGLE_CLOUD_PROJECT)
   --with-score                       include provider placement figures (experimental; on gcp the figure is obtainability from a beta Google API, fetched for a recommendation and needing --gcp-project) (default: false)
   --min-score value                  filter: minimum spot placement score (1-10, needs --with-score; refused on a cloud whose placement figure is not an integer score) (default: no filter)
   --az                               request zone-level figures instead of region-level (use with --with-score) (default: false)
   --score-timeout value              timeout for placement enrichment, 1-300 seconds (default: 30)
   --help, -h                         show help
```

The three required flags are checked before anything is fetched:

```console
$ spotinfo recommend --offline
spotinfo: invalid argument: --architecture, --min-vcpu, --min-memory-gib are required; every recommendation needs an architecture and a size floor

$ spotinfo recommend --offline --min-vcpu 2 --min-memory-gib 4
spotinfo: invalid argument: --architecture is required; every recommendation needs an architecture and a size floor
```

### `recommend` examples

AWS:

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --top 3
RANK  CLOUD  REGION     MACHINE     ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY
   1  aws    us-east-1  t2.medium   x86_64           2         4.0  0.0161          62%  <5%           ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   2  aws    us-east-1  t3.medium   x86_64           2         4.0  0.0175          60%  >20%          ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   3  aws    us-east-1  t3a.medium  x86_64           2         4.0  0.0194          53%  >20%          ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
```

GCP, Arm64:

```console
$ spotinfo recommend --cloud gcp --architecture arm64 --min-vcpu 2 --min-memory-gib 4 --top 2
RANK  CLOUD  REGION       MACHINE         ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY
   1  gcp    us-central1  n4a-highcpu-2   arm64            2         4.0  0.027276        58%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   2  gcp    us-central1  n4a-standard-2  arm64            2         8.0  0.032332        58%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
```

Azure, every published region — the ranking crosses regions, so the cheapest region wins:

```console
$ spotinfo recommend --cloud azure --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --top 3
RANK  CLOUD  REGION             MACHINE            ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY
   1  azure  mexicocentral      Standard_B2als_v2  x86_64           2         4.0  0.009108        78%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   2  azure  centralindia       Standard_D2als_v6  x86_64           2         4.0  0.009610        81%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   3  azure  australiacentral2  Standard_D2als_v6  x86_64           2         4.0  0.010100        90%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
```

### Ranking policy

Every workload ranks the same way. `ranking_policy` in the JSON answer is the same list for
`cost`, `web`, `ci` and `batch`:

```json
[
  "spot_price_ascending",
  "excess_vcpu_ascending",
  "excess_memory_gib_ascending",
  "region_ascending",
  "machine_ascending"
]
```

Savings is displayed and never ranked — a large discount off an expensive on-demand rate is
still an expensive machine. Risk is never a tie-break either; the workload uses it as a
**ceiling**, not as an ordering.

### Workloads

`--workload` chooses the interruption ceiling a candidate must be under:

| Workload | Ceiling                  | Clouds          | Rationale code           |
| -------- | ------------------------ | --------------- | ------------------------ |
| `cost`   | none                     | aws, gcp, azure | `COST_POLICY`            |
| `web`    | interruption at most 5%  | aws             | `WORKLOAD_WEB_CAP_MET`   |
| `ci`     | interruption at most 16% | aws             | `WORKLOAD_CI_CAP_MET`    |
| `batch`  | interruption at most 22% | aws             | `WORKLOAD_BATCH_CAP_MET` |

The ceilings are AWS Spot Advisor bucket boundaries, applied to the bucket's **maximum**
percentage. On the Advisor's five buckets that makes `web` accept only `<5%`, `ci` accept
through `10-15%`, and `batch` accept through `15-20%`.

Because the ceilings are boundaries of an AWS measurement, a cloud that publishes no figure
measured that way is refused rather than ranked as if it were safe:

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --workload web
spotinfo: gcp: unsupported capability: risk: the web workload caps interruption frequency at 5%, an AWS Spot Advisor bucket boundary, and gcp publishes no figure measured that way; workload cost applies no ceiling and answers on every cloud
```

What the ceiling buys, on AWS. The same requirement under `cost` and under `web`:

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 --min-vcpu 4 --min-memory-gib 8 --machine '^r5' --workload cost --top 1 --output text
rank=1, cloud=aws, region=us-east-1, machine=r5.xlarge, architecture=x86_64, vcpu=4, memory_gib=32, spot_usd_per_hour=0.078400000, savings_percent=70, risk=>20%, rationale_codes=ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE MACHINE_PATTERN_MATCH RESOURCE_MINIMUMS_MET

$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 --min-vcpu 4 --min-memory-gib 8 --machine '^r5' --workload web --top 1 --output text
rank=1, cloud=aws, region=us-east-1, machine=r5b.xlarge, architecture=x86_64, vcpu=4, memory_gib=32, spot_usd_per_hour=0.129400000, savings_percent=48, risk=<5%, rationale_codes=ARCHITECTURE_MATCH KNOWN_POSITIVE_PRICE MACHINE_PATTERN_MATCH RESOURCE_MINIMUMS_MET WORKLOAD_WEB_CAP_MET
```

A ceiling nothing meets is an error, not an empty page:

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 --min-vcpu 4 --min-memory-gib 8 --machine '^m5\.' --workload web --top 1
spotinfo: no candidates: no machine publishes an interruption rate within the web workload ceiling of 5%
```

### Rationale codes

`WHY` is a fixed vocabulary of facts and policy checks, never generated prose. Codes observed
from the binary:

| Code                     | Meaning                                                       |
| ------------------------ | ------------------------------------------------------------- |
| `ARCHITECTURE_MATCH`     | the machine publishes the requested processor architecture    |
| `RESOURCE_MINIMUMS_MET`  | vCPU and memory both meet the requested floors                |
| `KNOWN_POSITIVE_PRICE`   | a positive Spot price was published; no candidate lacks one   |
| `MACHINE_PATTERN_MATCH`  | `--machine` was given and the name matched it                 |
| `BUDGET_CAP_MET`         | `--max-price` was given and the price is under it             |
| `COST_POLICY`            | ranked under `--workload cost`, which applies no risk ceiling |
| `WORKLOAD_WEB_CAP_MET`   | risk is within the `web` ceiling                              |
| `WORKLOAD_CI_CAP_MET`    | risk is within the `ci` ceiling                               |
| `WORKLOAD_BATCH_CAP_MET` | risk is within the `batch` ceiling                            |

`COST_POLICY` and the three `WORKLOAD_*_CAP_MET` codes are mutually exclusive: the workload
either applied a ceiling or did not.

### `--sort` on a ranked page

On `recommend`, `--sort` reorders the **printed page**; it does not re-rank. The `rank` field
keeps the ranking policy's answer, so a sorted page can start at rank 3:

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 --top 3 --sort machine --output text
rank=3, cloud=aws, region=us-east-1, machine=t2.xlarge, architecture=x86_64, vcpu=4, memory_gib=16, spot_usd_per_hour=0.058300000, savings_percent=61, risk=10-15%, rationale_codes=ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
rank=1, cloud=aws, region=us-east-1, machine=t3.xlarge, architecture=x86_64, vcpu=4, memory_gib=16, spot_usd_per_hour=0.050200000, savings_percent=61, risk=<5%, rationale_codes=ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
rank=2, cloud=aws, region=us-east-1, machine=t3a.xlarge, architecture=x86_64, vcpu=4, memory_gib=16, spot_usd_per_hour=0.055300000, savings_percent=61, risk=>20%, rationale_codes=ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
```

`--sort risk` is the one key that means different things on the two commands. On `list` it is
refused for a cloud with no risk figure, because there is nothing to order by:

```console
$ spotinfo list --cloud gcp --sort risk
spotinfo: gcp: unsupported capability: risk: this cloud's catalogue carries no risk figure
```

On `recommend` the same request is accepted and the page prints — the ranked page is already
complete, and the sort only shuffles a page whose risk column reads `unavailable` throughout:

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --sort risk --top 2 --output text
rank=1, cloud=gcp, region=us-central1, machine=n2d-standard-2, architecture=x86_64, vcpu=2, memory_gib=8, spot_usd_per_hour=0.026912000, savings_percent=68, risk=unavailable, rationale_codes=ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
rank=2, cloud=gcp, region=us-central1, machine=c4d-standard-2, architecture=x86_64, vcpu=2, memory_gib=7, spot_usd_per_hour=0.032182369, savings_percent=64, risk=unavailable, rationale_codes=ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
```

## Flag reference

Defaults and scope, as the binary applies them. Both commands take the same flags except
where the last column says otherwise.

| Flag                | Default                       | Notes                                                                                  |
| ------------------- | ----------------------------- | -------------------------------------------------------------------------------------- |
| `--cloud`           | `aws`                         | `aws`, `gcp` or `azure`                                                                |
| `--machine`         | no filter                     | RE2 regexp over the machine name                                                       |
| `--architecture`    | no filter on `list`           | **required** on `recommend`; `x86_64` or `arm64`                                       |
| `--min-vcpu`        | no filter on `list`           | **required** on `recommend`; inclusive minimum                                         |
| `--min-memory-gib`  | no filter on `list`           | **required** on `recommend`; inclusive minimum                                         |
| `--max-price`       | no filter                     | inclusive positive ceiling in USD per machine-hour                                     |
| `--os`              | `linux`                       | `windows` is served on AWS and Azure, refused on GCP                                   |
| `--region`          | `all`                         | repeatable; `all` is a keyword, not a region name                                      |
| `--sort`            | the order the cloud publishes | `machine`, `price`, `region`, `risk`, `savings`, `score`                               |
| `--order`           | `asc`                         | `asc` or `desc`                                                                        |
| `--output`          | `table`                       | `number` is `list`-only                                                                |
| `--offline`         | off                           | answer from the embedded snapshot; no price or risk request                            |
| `--refresh`         | off                           | ignore any cached feed and fetch again                                                 |
| `--workload`        | `cost`                        | `recommend` only                                                                       |
| `--top`             | `3`                           | `recommend` only; 1-50                                                                 |
| `--live-risk`       | off                           | `recommend` only; GCP only, needs a project                                            |
| `--with-score`      | off                           | placement figures; see [Placement figures](#placement-figures)                         |
| `--min-score`       | no filter                     | 1-10, needs `--with-score`; refused where the placement figure is not an integer score |
| `--az`              | off                           | zone-level figures, needs `--with-score`                                               |
| `--score-timeout`   | `30`                          | 1-300 seconds, needs `--with-score`                                                    |
| `--gcp-project`     | `$GOOGLE_CLOUD_PROJECT`       | GCP only                                                                               |
| `--gcp-billing-key` | `$SPOTINFO_GCP_BILLING_KEY`   | GCP only                                                                               |

`--region` values are trimmed of surrounding whitespace. `recommend` also deduplicates and
sorts them — `--region us-west-2 --region us-east-1 --region us-east-1` echoes
`["us-east-1","us-west-2"]` — while `list` keeps what you passed, so a repeated region repeats
its rows. `all` combined with a named region is refused:

```console
$ spotinfo list --offline --region all --region us-east-1
spotinfo: failed to get spot savings: aws candidate acquisition: region not found: all
```

An unknown region behaves differently per cloud, because AWS knows its region list and the
other two match against a catalogue. On AWS it is an error; on GCP and Azure the answer is an
empty result and a warning on stderr:

```console
$ spotinfo list --offline --region atlantis
spotinfo: failed to get spot savings: aws candidate acquisition: region not found: atlantis

$ spotinfo list --cloud gcp --region atlantis
time=... level=WARN msg="no machines matched the query" filters="[region=atlantis]"
```

(the second exits 0 with an empty table)

## Output formats

`list` serves `table`, `text`, `json`, `csv` and `number`. `recommend` serves the first four;
`number` is refused there:

```console
$ spotinfo recommend --offline --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --output number
spotinfo: invalid argument: --output number belongs to `spotinfo list`: one savings percent cannot describe a ranked page
```

### `list` formats

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --output text
machine=m5.large, vCPU=2, memory=8GiB, saving=59%, risk='>20%', price=0.0399
```

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --output csv
Machine,vCPU,Memory GiB,Savings over On-Demand,Risk,USD/Hour,Price Source
m5.large,2,8,59,>20%,0.0399,static
```

The CSV header gains a leading `Region` column under the same rule as the table: it is there
unless the request names exactly one `--region`. The `Price Source` column is `static` for a
snapshot or feed price, `live` when the AWS `DescribeSpotPriceHistory` fallback supplied it,
and `-` for a machine with no published price.

`number` prints one bare integer: the savings percent of the first match. It is not a price.

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --output number
59
```

`json` is a versioned document, not a bare array:

```bash
spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --output json
```

```json
{
  "schema_version": "spotinfo.list/v1",
  "status": "ok",
  "request": {
    "cloud": "aws",
    "regions": ["us-east-1"],
    "machine": "^m5\\.large$",
    "architecture": "",
    "os": "linux",
    "min_vcpu": 0,
    "min_memory_gib": 0,
    "max_price": null,
    "sort": "",
    "order": "asc"
  },
  "data_source": {
    "provider": "aws",
    "mode": "embedded-snapshot",
    "sources": [
      {
        "url": "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json",
        "fetched_at": "2026-08-10T06:54:02Z",
        "observed_at": null,
        "content_sha256": "158f404cca4a5167a2a035cbd952de8f1886bb14c8cdbc2a55f4871c33735f1a",
        "parser_version": "aws-spot-advisor-json/1",
        "schema_version": "aws.spot-advisor-feed/v1"
      }
    ],
    "sources_omitted": 0
  },
  "candidates": [
    {
      "cloud": "aws",
      "region": "us-east-1",
      "machine": "m5.large",
      "architecture": "x86_64",
      "os": "linux",
      "vcpu": 2,
      "memory_gib": 8,
      "spot_usd_per_hour": "0.039900000",
      "on_demand_usd_per_hour": null,
      "savings_percent": 59,
      "risk": {
        "status": "available",
        "kind": "interruption_bucket",
        "label": ">20%",
        "min_percent": 23,
        "max_percent": 100,
        "window_days": 30,
        "source_url": "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json",
        "observed_at": null
      },
      "live_price": false
    }
  ],
  "warnings": []
}
```

Two edits for length above: the real answer carries all three AWS sources in
`data_source.sources`, and the binary prints one array element per line.

Prices are decimal strings with nine fractional digits, so no consumer has to trust a float
round-trip; a machine with no published price carries `null` instead.
`on_demand_usd_per_hour` is `null` on AWS, which publishes a savings percent rather than a
paired on-demand rate; GCP and Azure populate it.

### `recommend` formats

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --top 1 --output text
rank=1, cloud=aws, region=us-east-1, machine=t2.medium, architecture=x86_64, vcpu=2, memory_gib=4, spot_usd_per_hour=0.016100000, savings_percent=62, risk=<5%, rationale_codes=ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
```

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --top 3 --output csv
Rank,Cloud,Region,Machine,Architecture,vCPU,Memory GiB,USD/Hour,Savings over On-Demand,Risk,Why
1,aws,us-east-1,t2.medium,x86_64,2,4,0.016100000,62,<5%,ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
2,aws,us-east-1,t3.medium,x86_64,2,4,0.017500000,60,>20%,ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
3,aws,us-east-1,t3a.medium,x86_64,2,4,0.019400000,53,>20%,ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
```

```bash
spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --top 1 --output json
```

```json
{
  "schema_version": "spotinfo.recommend/v3",
  "status": "ok",
  "request": {
    "cloud": "gcp",
    "regions": ["all"],
    "machine": "",
    "architecture": "x86_64",
    "os": "linux",
    "min_vcpu": 2,
    "min_memory_gib": 4,
    "max_price": null,
    "workload": "cost",
    "top": 1
  },
  "ranking_policy": [
    "spot_price_ascending",
    "excess_vcpu_ascending",
    "excess_memory_gib_ascending",
    "region_ascending",
    "machine_ascending"
  ],
  "data_source": {
    "provider": "gcp",
    "mode": "embedded-snapshot",
    "sources": [
      {
        "url": "https://cloud.google.com/spot-vms/pricing",
        "fetched_at": "2026-08-09T05:26:02Z",
        "observed_at": null,
        "content_sha256": "145c737738229a71fa6cb31ee34fa147c08fff363e70761c53eca03302ec8d80",
        "parser_version": "gcp-pricing-html/1",
        "schema_version": "spotinfo.gcp-catalog/v1"
      }
    ],
    "sources_omitted": 0
  },
  "recommendations": [
    {
      "rank": 1,
      "cloud": "gcp",
      "region": "us-central1",
      "machine": "n2d-standard-2",
      "architecture": "x86_64",
      "os": "linux",
      "vcpu": 2,
      "memory_gib": 8,
      "spot_usd_per_hour": "0.026912000",
      "on_demand_usd_per_hour": "0.084492000",
      "savings_percent": 68,
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
        "ARCHITECTURE_MATCH",
        "COST_POLICY",
        "KNOWN_POSITIVE_PRICE",
        "RESOURCE_MINIMUMS_MET"
      ]
    }
  ],
  "warnings": []
}
```

Same two edits: the real answer lists all five GCP sources, and prints one array element per
line. Field-by-field semantics are in the [API reference](api-reference.md).

## Risk

`RISK` reports what the cloud publishes, or reports that it publishes nothing. It is never a
zero and never a value borrowed from another cloud.

- **AWS** publishes an interruption bucket over a 30-day window. The five labels are `<5%`,
  `5-10%`, `10-15%`, `15-20%` and `>20%`; JSON carries `min_percent`/`max_percent` beside the
  label, and `kind: "interruption_bucket"`.
- **GCP** and **Azure** publish nothing redistributable. Every candidate reports
  `unavailable`, and the JSON risk block is `status: "unavailable"` with every other member
  `null`.

That absence is why `--workload web|ci|batch` is refused on those two clouds, and why
`--sort risk` is refused on `list` there.

### Live GCP preemption risk

`spotinfo recommend --cloud gcp --live-risk` fetches a per-project preemption rate from
Google's beta capacity-history API for the ranked page only — one request per recommendation,
never per catalogue entry. It needs Application Default Credentials and a project, from
`--gcp-project` or `GOOGLE_CLOUD_PROJECT`; the ambient `gcloud` project is deliberately not
read, because the call is billed to whatever it names.

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --live-risk
spotinfo: invalid argument: --live-risk needs a project; pass --gcp-project or set GOOGLE_CLOUD_PROJECT
```

The figure is **visible but not filterable**. Google measures preempted Spot VMs as a share
of VMs that stopped running; AWS measures the share of _running_ instances interrupted. They
are different measurements, so `--workload web|ci|batch` still refuses GCP even when the
number is in hand.

A failed live lookup degrades rather than fails. The ranked page prints with
`risk=unavailable`, the run exits 0, and the reason arrives on stderr in a warning that begins
`live risk unavailable; reporting the snapshot's risk status`:

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 \
    --top 1 --live-risk --gcp-project fake-project-123 --output text
time=... level=WARN msg="live risk unavailable; reporting the snapshot's risk status" provider=gcp error="all 1 preemption lookups failed, first: capacity history returned 404 Not Found: ..."
rank=1, cloud=gcp, region=us-central1, machine=n2d-standard-2, architecture=x86_64, vcpu=2, memory_gib=8, spot_usd_per_hour=0.026912000, savings_percent=68, risk=unavailable, rationale_codes=ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
```

The flag is refused on the other two clouds:

```console
$ spotinfo recommend --offline --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --live-risk
spotinfo: unsupported capability: --live-risk is refused on aws: the flag fetches gcp preemption rates and is implemented for no other cloud

$ spotinfo recommend --cloud azure --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --live-risk
spotinfo: unsupported capability: --live-risk is refused on azure: azure publishes an eviction rate through Azure Resource Graph, but reading it needs an Azure subscription this build does not authenticate to
```

## Placement figures

`--with-score` asks the cloud how likely a request is to be **fulfilled**. That is a different
question from risk, and each cloud answers it in its own units, so the flag is not portable.

| Cloud   | Figure                                       | Where               | Needs                                                     |
| ------- | -------------------------------------------- | ------------------- | --------------------------------------------------------- |
| `aws`   | Spot placement score, integer 1-10           | `list`, `recommend` | AWS credentials with `ec2:GetSpotPlacementScores`         |
| `gcp`   | obtainability, a probability from 0.0 to 1.0 | `recommend` only    | Application Default Credentials and `--gcp-project`       |
| `azure` | published, but not read by this build        | refused             | an Azure subscription this build does not authenticate to |

Because GCP's figure is a probability, `--min-score`'s integer 1-10 floor has no reviewed
meaning there and is refused instead of being approximated:

```console
$ spotinfo list --cloud gcp --with-score --min-score 5
spotinfo: unsupported capability: --min-score is refused on gcp: gcp publishes obtainability, and an integer 1-10 floor states no reviewed mapping onto it

$ spotinfo list --cloud gcp --with-score
spotinfo: failed to get spot savings: unsupported capability: gcp obtainability is fetched for a ranked recommendation only, and needs a Google Cloud project

$ spotinfo list --cloud azure --with-score
spotinfo: unsupported capability: --with-score is refused on azure: azure publishes a Spot Placement Score, but reading it needs an Azure subscription this build does not authenticate to
```

`--min-score`, `--az` and `--score-timeout` all need `--with-score`, which is what fetches the
figures they filter, split or wait for. Each is refused on its own terms:

```console
$ spotinfo list --offline --az
spotinfo: invalid argument: --az needs --with-score, which is what fetches the placement scores it splits by zone

$ spotinfo list --offline --min-score 5
spotinfo: invalid argument: --min-score needs --with-score, which is what fetches the placement scores it filters on

$ spotinfo list --offline --score-timeout 10
spotinfo: invalid argument: --score-timeout needs --with-score, which is what fetches the placement scores it waits for
```

`--sort score` needs it too, and cannot be combined with `--az`, because ordering a page needs
one figure per row and `--az` asks for one per zone:

```console
$ spotinfo list --offline --sort score
spotinfo: invalid argument: --sort score needs --with-score, which is what fetches the placement figures it orders by

$ spotinfo recommend --offline --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --sort score --with-score --az
spotinfo: invalid argument: --sort score cannot be combined with --az: only a regional placement figure orders a page, and --az asks for one figure per zone instead
```

`--offline` does **not** suppress placement acquisition — the snapshot has no placement
figures, so `--with-score` still calls the cloud. Without credentials the run fails rather
than degrading, and it reports one failure per region queried.

## Data sources, offline and caching

Every provider answers from a committed snapshot when asked to, and every provider has a live
path:

| Cloud   | Live path                                                                                                 |
| ------- | --------------------------------------------------------------------------------------------------------- |
| `aws`   | the Spot Advisor and Spot pricing feeds, plus `DescribeSpotPriceHistory` for prices the static feed omits |
| `gcp`   | the Cloud Billing Catalog API behind `--gcp-billing-key`                                                  |
| `azure` | the anonymous Azure Retail Prices API                                                                     |

`--offline` answers from the embedded snapshot and makes no price or risk request. `--refresh`
ignores any cached feed and fetches again. Both are accepted on all three clouds. A cloud with
nothing to fetch answers from the snapshot anyway rather than failing — `spotinfo list --cloud
gcp --refresh` with no billing key reports `mode: "embedded-snapshot"` and exits 0.

Neither flag governs placement. `--with-score` has no snapshot to read from, so it still calls
the provider's placement API under `--offline`:

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --with-score --score-timeout 3
spotinfo: failed to get spot savings: aws candidate acquisition: score enrichment failed: region us-east-1: spot placement scores unavailable: requires AWS credentials and the ec2:GetSpotPlacementScores permission: …
```

`data_source.mode` in a JSON answer reports which of the three it was:

| Mode                | Meaning                                                              |
| ------------------- | -------------------------------------------------------------------- |
| `live`              | fetched from the source now, or confirmed unchanged by a `304`       |
| `cached`            | served from a local cache entry that was not revalidated in this run |
| `embedded-snapshot` | served from the data compiled into the binary                        |

A cached answer is never reported as `live`: provenance is the same, established recency is
not.

Fetched feeds are cached under `os.UserCacheDir()/spotinfo`. The AWS advisor document is
cached for 24 hours and prices for 1 hour, because the advisor document is large and rarely
changes while prices are rewritten through the day. `SPOTINFO_CACHE_DIR` moves the cache;
`SPOTINFO_CACHE=off` disables it. Cache failures are never fatal — a read-only filesystem
costs time, not answers.

`--region all` on AWS queries every published region, which is the slow path. `--offline` or
an explicit `--region` is what makes it quick; the flag's own help text says so.

See [Data sources](data-sources.md) for each feed, its contract and its refresh procedure, and
[Cloud coverage](clouds.md) for the region list, machine counts and machine series per cloud.

## Cloud coverage summary

[clouds.md](clouds.md) is the single source for region lists, machine counts, machine series
and the reason behind each limit. The short version:

| Cloud   | Commands            | Regions       | OS             | Architectures | Risk                 | Workloads            |
| ------- | ------------------- | ------------- | -------------- | ------------- | -------------------- | -------------------- |
| `aws`   | `list`, `recommend` | 34            | linux, windows | x86_64, arm64 | interruption buckets | cost, web, ci, batch |
| `gcp`   | `list`, `recommend` | `us-central1` | linux          | x86_64, arm64 | unavailable          | cost                 |
| `azure` | `list`, `recommend` | 55            | linux, windows | x86_64, arm64 | unavailable          | cost                 |

GCP's snapshot carries `us-central1`; `--gcp-billing-key` prices further regions for a single
invocation without ever writing them into a snapshot. `--os windows` is refused on GCP, which
publishes no Windows Spot price:

```console
$ spotinfo list --cloud gcp --os windows
spotinfo: gcp: unsupported capability: os windows: this cloud publishes spot prices for linux only
```

## MCP server

`spotinfo --mcp` speaks the Model Context Protocol over stdio and exposes three tools:

| Tool                      | Answers                                                 | Schema                  |
| ------------------------- | ------------------------------------------------------- | ----------------------- |
| `list_cloud_regions`      | every region a cloud publishes, with its sources        | `spotinfo.regions/v1`   |
| `list_spot_machines`      | the same document as `spotinfo list --output json`      | `spotinfo.list/v1`      |
| `recommend_spot_machines` | the same document as `spotinfo recommend --output json` | `spotinfo.recommend/v3` |

Tool arguments are the CLI flag names with `--` stripped and `-` replaced by `_`; the one
exception is the repeatable `--region`, which becomes the array argument `regions`. There is
no `output` argument — MCP is always JSON. There is no `gcp_billing_key` argument either: the
server reads `SPOTINFO_GCP_BILLING_KEY` from its environment. No `gcp_project` argument
exists because the tools expose neither placement figures nor live risk.

`recommend_spot_machines` requires `architecture`, `min_vcpu` and `min_memory_gib`, exactly as
the command does. It declares no placement or live-risk arguments: those are CLI-only.

A refused call returns a `spotinfo.error/v1` payload with `isError: true` and the same wording
the CLI prints:

```json
{
  "schema_version": "spotinfo.error/v1",
  "code": "UNSUPPORTED_CAPABILITY",
  "message": "azure: unsupported capability: risk: the batch workload caps interruption frequency at 22%, an AWS Spot Advisor bucket boundary, and azure publishes no figure measured that way; workload cost applies no ceiling and answers on every cloud",
  "cloud": "azure"
}
```

`code` is one of `INVALID_ARGUMENT`, `UNSUPPORTED_CAPABILITY`, `DATA_UNAVAILABLE`,
`NO_CANDIDATES` or `INTERNAL`. Naming a tool that does not exist is a JSON-RPC error instead:

```json
{
  "code": -32602,
  "message": "tool 'find_spot_instances' not found: tool not found"
}
```

Full argument schemas and client configuration are in [MCP server](mcp-server.md).

## Renamed and removed flags

The eight older flag names are refused with the name that replaced them, on both commands:

| Old flag                   | Use instead        |
| -------------------------- | ------------------ |
| `--type`, `--instance`     | `--machine`        |
| `--vcpu`, `--cpu`          | `--min-vcpu`       |
| `--memory`, `--memory-gib` | `--min-memory-gib` |
| `--price`, `--budget`      | `--max-price`      |

```console
$ spotinfo list --offline --type m5
spotinfo: invalid argument: --type was renamed to --machine

$ spotinfo list --offline --cpu 4
spotinfo: invalid argument: --cpu was renamed to --min-vcpu
```

The MCP tools were renamed with them: `find_spot_instances` is now `list_spot_machines`,
`recommend_spot_instances` is now `recommend_spot_machines`, and `list_spot_regions` is now
`list_cloud_regions`. The retired names are not served.

`spotinfo.recommend/v1` and `spotinfo.recommend/v2` are retired. The binary publishes
`spotinfo.list/v1`, `spotinfo.recommend/v3`, `spotinfo.regions/v1` and `spotinfo.error/v1`.

## Errors and refusals

Every CLI failure prints one line to stderr, prefixed `spotinfo:`, and exits 1. stdout stays
empty, in every output format including `json` — a failed run never emits a partial document.
The one exception is running `spotinfo` with no command, which prints the help to stdout
before refusing.

| Code | Meaning                                    |
| ---- | ------------------------------------------ |
| 0    | success (including an empty `list` result) |
| 1    | any refusal or failure                     |

### Vocabulary

```console
$ spotinfo list --cloud oracle
spotinfo: invalid argument: unknown cloud provider "oracle"

$ spotinfo list --offline --architecture s390x
spotinfo: invalid argument: architecture must be x86_64 or arm64

$ spotinfo list --offline --sort colour
spotinfo: invalid argument: unknown sort "colour", want one of machine|price|region|risk|savings|score

$ spotinfo list --offline --order sideways
spotinfo: invalid argument: unknown order "sideways", want asc or desc

$ spotinfo list --offline --output yaml
spotinfo: invalid argument: unknown output format "yaml", want one of number|text|json|table|csv

$ spotinfo recommend --offline --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --workload gpu
spotinfo: invalid argument: workload must be cost, web, ci, or batch
```

### Bounds

```console
$ spotinfo recommend --offline --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --top 51
spotinfo: invalid argument: top must be between 1 and 50

$ spotinfo list --offline --max-price 0
spotinfo: invalid argument: --max-price must be a positive USD machine-hour price

$ spotinfo list --offline --min-vcpu -1
spotinfo: invalid argument: --min-vcpu must be zero or a positive number of vCPU cores

$ spotinfo list --offline --with-score --min-score 11
spotinfo: invalid argument: --min-score must be between 1 and 10

$ spotinfo list --offline --with-score --score-timeout 0
spotinfo: invalid argument: --score-timeout must be between 1 and 300 seconds
```

`--max-price NaN` is rejected by the same message as `--max-price 0`. A non-finite number
fails every comparison, so it would otherwise slip past both the rejection and the "was it
set" check and silently drop the filter.

### Nothing matched

`list` reports an empty result and exits 0. `recommend` cannot rank an empty set, so it
refuses and names what the cloud does publish:

```console
$ spotinfo list --offline --region us-east-1 --machine '^zzzz$'
time=... level=WARN msg="no machines matched the query" filters="[machine=^zzzz$ region=us-east-1]"
┌─────────┬──────┬────────────┬────────────────────────┬──────┬──────────┐
│ MACHINE │ VCPU │ MEMORY GIB │ SAVINGS OVER ON-DEMAND │ RISK │ USD/HOUR │
├─────────┼──────┼────────────┼────────────────────────┼──────┼──────────┤
└─────────┴──────┴────────────┴────────────────────────┴──────┴──────────┘

$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --machine '^zzzz$'
spotinfo: no candidates: no machine name matches "^zzzz$"; aws publishes c3.2xlarge, c3.xlarge, c5.9xlarge, c5.metal, c5a.2xlarge, c5a.xlarge, c5n.metal, c6a.metal, and more
```

### Bad regexp

```console
$ spotinfo list --offline --machine '['
spotinfo: failed to get spot savings: aws candidate acquisition: failed to match instance type: error parsing regexp: missing closing ]: `[`
```

### Wrong cloud for a credential flag

```console
$ spotinfo list --offline --gcp-project foo
spotinfo: unsupported capability: --gcp-project is refused on aws: the flag names the project an authenticated gcp call is billed to, and aws makes none
```

## Automation examples

Both commands emit a JSON object, not a bare array: `list` puts its rows under `.candidates`,
`recommend` puts its ranked page under `.recommendations`.

Cheapest machine meeting a requirement, from a ranked page:

```bash
#!/usr/bin/env bash
set -euo pipefail
BEST=$(spotinfo recommend --offline --region us-east-1 \
  --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 --top 1 --output json \
  | jq -r '.recommendations[0].machine')
echo "Recommended machine: $BEST"
```

Gate a pipeline on a price. Read it from JSON — `--output number` reports savings, not price:

```bash
MAX_COST=0.50
COST=$(spotinfo list --offline --region us-east-1 --machine '^m5\.xlarge$' --output json \
  | jq -r '.candidates[0].spot_usd_per_hour')
if (( $(echo "$COST > $MAX_COST" | bc -l) )); then
  echo "Spot price exceeds budget: $COST > $MAX_COST"
  exit 1
fi
```

Gate on savings instead, which is what `--output number` prints:

```bash
MIN_SAVINGS=60
SAVINGS=$(spotinfo list --offline --region us-east-1 --machine '^m5\.xlarge$' --output number)
if [ "$SAVINGS" -lt "$MIN_SAVINGS" ]; then
  echo "Savings $SAVINGS% is below the $MIN_SAVINGS% floor"
  exit 1
fi
```

Compare the same requirement across three clouds:

```console
$ for cloud in aws gcp azure; do
    spotinfo recommend --cloud "$cloud" --offline --architecture x86_64 \
      --min-vcpu 2 --min-memory-gib 4 --top 1 --output csv | tail -n +2
  done
1,aws,ap-south-1,t3a.medium,x86_64,2,4,0.008800000,54,10-15%,ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
1,gcp,us-central1,n2d-standard-2,x86_64,2,8,0.026912000,68,unavailable,ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
1,azure,mexicocentral,Standard_B2als_v2,x86_64,2,4,0.009108000,78,unavailable,ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
```

Drop `--offline` to price against the live feeds. Without AWS credentials the AWS leg then
logs a `failed to fetch live prices` warning per region and answers from the snapshot anyway.

Feed a ranked page into infrastructure code:

```bash
spotinfo recommend --cloud azure --region westeurope --architecture arm64 \
  --min-vcpu 2 --min-memory-gib 4 --top 5 --output json > spot_candidates.json
```

## Environment variables

| Variable                   | Meaning                                                          | Default                      |
| -------------------------- | ---------------------------------------------------------------- | ---------------------------- |
| `SPOTINFO_MODE`            | set to `mcp` to start the MCP server without `--mcp`             | CLI mode                     |
| `MCP_TRANSPORT`            | MCP transport, `stdio` or `sse`                                  | `stdio`                      |
| `MCP_PORT`                 | port for the `sse` transport                                     | `8080`                       |
| `SPOTINFO_CACHE_DIR`       | override the feed cache location                                 | `os.UserCacheDir()/spotinfo` |
| `SPOTINFO_CACHE`           | set to `off` to disable feed caching                             | caching on                   |
| `GOOGLE_CLOUD_PROJECT`     | project for authenticated GCP calls, if `--gcp-project` is unset | unset                        |
| `SPOTINFO_GCP_BILLING_KEY` | Cloud Billing Catalog API key, if `--gcp-billing-key` is unset   | unset                        |

## Performance

- The AWS placement-score cache holds results for 10 minutes; its API calls are rate-limited
  to 10 per second.
- The default `--score-timeout` is 30 seconds, bounded at 300.
- `--region all` on AWS queries every published region. Pass `--offline` or an explicit
  `--region` when speed matters.
- `--offline` answers from embedded data and makes no price or risk request — but it does not
  suppress `--with-score`, which has no snapshot to answer from. Measured on one machine, the
  same single-region AWS query took about 0.1 s offline, about 0.1 s from a warm feed cache,
  and 2.4 s with `--refresh`, which re-downloads both feeds.

## See Also

- [Quick start](quick-start.md) — the short path to a first answer
- [Cloud coverage](clouds.md) — what each cloud serves, and why it refuses the rest
- [Data sources](data-sources.md) — every feed, its contract and its refresh procedure
- [API reference](api-reference.md) — field-by-field schema documentation
- [MCP server](mcp-server.md) — tool schemas and client configuration
- [AWS Spot placement scores](aws-spot-placement-scores.md) — the AWS placement figure in detail
- [Examples](examples.md) — real-world recipes
- [Troubleshooting](troubleshooting.md) — common failures and their fixes
