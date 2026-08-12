# Examples

Every command on this page was run against the binary, and every output block is what it
printed. Nothing here needs a credential unless the section says so.

`spotinfo` has two commands. `list` browses a catalogue; `recommend` ranks it. Both take
`--cloud aws|gcp|azure`, and **every query flag follows the subcommand** — only `--mcp`,
`--debug`, `--quiet` and `--json-log` come before it. See
[troubleshooting.md](troubleshooting.md#a-flag-before-the-subcommand-is-not-defined) if that
bites.

## Browsing a catalogue

### 1. One family in one region

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.(large|xlarge)$'
┌───────────┬──────┬────────────┬────────────────────────┬──────┬──────────┐
│ MACHINE   │ VCPU │ MEMORY GIB │ SAVINGS OVER ON-DEMAND │ RISK │ USD/HOUR │
├───────────┼──────┼────────────┼────────────────────────┼──────┼──────────┤
│ m5.large  │    2 │          8 │                    59% │ >20% │ 0.0399   │
├───────────┼──────┼────────────┼────────────────────────┼──────┼──────────┤
│ m5.xlarge │    4 │         16 │                    68% │ >20% │ 0.0609   │
└───────────┴──────┴────────────┴────────────────────────┴──────┴──────────┘
```

`--machine` is an RE2 regexp, so anchor it with `^…$` when you mean one exact name. The
`REGION` column is dropped when the **request** names exactly one `--region`, and is present
otherwise. That is about the request, not the result: `--region all` keeps the column even on
GCP, where it resolves to a single region.

### 2. Filter by size and price, then sort

```console
$ spotinfo list --offline --region us-east-1 --machine '^t3\.' \
    --min-vcpu 2 --min-memory-gib 4 --max-price 0.05 --sort price --order asc
┌───────────┬──────┬────────────┬────────────────────────┬──────┬──────────┐
│ MACHINE   │ VCPU │ MEMORY GIB │ SAVINGS OVER ON-DEMAND │ RISK │ USD/HOUR │
├───────────┼──────┼────────────┼────────────────────────┼──────┼──────────┤
│ t3.medium │    2 │          4 │                    60% │ >20% │ 0.0175   │
├───────────┼──────┼────────────┼────────────────────────┼──────┼──────────┤
│ t3.large  │    2 │          8 │                    53% │ <5%  │ 0.0278   │
└───────────┴──────┴────────────┴────────────────────────┴──────┴──────────┘
```

`--sort` takes `machine|price|region|risk|savings|score`. `risk` needs a cloud that publishes
one, and `score` needs `--with-score`.

### 3. Compare one machine across regions

```console
$ spotinfo list --offline --region us-east-1 --region us-west-2 --machine '^c7g\.large$' --output csv
Region,Machine,vCPU,Memory GiB,Savings over On-Demand,Risk,USD/Hour,Price Source
us-east-1,c7g.large,2,4,57,5-10%,0.0222,static
us-west-2,c7g.large,2,4,51,10-15%,0.0276,static
```

`--region` repeats. Omitting it queries every published region, which on AWS is 34 of them.

### 4. Windows

AWS and Azure price Windows; GCP does not
([why](clouds.md#what-stays-refused-and-why)).

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --os windows --output text
machine=m5.large, vCPU=2, memory=8GiB, saving=44%, risk='>20%', price=0.1047

$ spotinfo list --cloud azure --region westeurope --machine '^Standard_D2as_v5$' --os windows --output text
machine=Standard_D2as_v5, vCPU=2, memory=8GiB, saving=81%, risk='unavailable', price=0.0362
```

The same size without `--os windows` is `price=0.0192` — a Windows meter is a separate row,
priced against the Windows list price.

## Ranking for a requirement

`recommend` requires `--architecture`, `--min-vcpu` and `--min-memory-gib`. There is no
default size floor, because a ranked page without one ranks the whole catalogue.

### 5. Cheapest machine that fits

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 --min-vcpu 2 --min-memory-gib 4
RANK  CLOUD  REGION     MACHINE     ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY
   1  aws    us-east-1  t2.medium   x86_64           2         4.0  0.0161          62%  <5%           ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   2  aws    us-east-1  t3.medium   x86_64           2         4.0  0.0175          60%  >20%          ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   3  aws    us-east-1  t3a.medium  x86_64           2         4.0  0.0194          53%  >20%          ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
```

The `WHY` column is the rationale for the row, not decoration. `COST_POLICY` means the
default `--workload cost` ordered it, and `KNOWN_POSITIVE_PRICE` means the price is a measured
number rather than a gap in the feed.

### 6. Cap the interruption rate for a web tier

`--workload web|ci|batch` caps interruption at 5%, 16% and 22%. Those are AWS Spot Advisor
bucket boundaries, so the three workloads answer on AWS only.

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --workload web
RANK  CLOUD  REGION     MACHINE     ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY
   1  aws    us-east-1  t2.medium   x86_64           2         4.0  0.0161          62%  <5%           ARCHITECTURE_MATCH,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET,WORKLOAD_WEB_CAP_MET
   2  aws    us-east-1  t3.large    x86_64           2         8.0  0.0278          53%  <5%           ARCHITECTURE_MATCH,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET,WORKLOAD_WEB_CAP_MET
   3  aws    us-east-1  c8id.large  x86_64           2         4.0  0.0336          61%  <5%           ARCHITECTURE_MATCH,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET,WORKLOAD_WEB_CAP_MET
```

`WORKLOAD_WEB_CAP_MET` replaces `COST_POLICY`, and every row is now in the `<5%` bucket.
`t3.medium` was rank 2 in the previous example and is gone from this one — its bucket is
`>20%`.

### 7. Arm

All three clouds publish Arm machines.

```console
$ spotinfo recommend --offline --region us-east-1 --architecture arm64 --min-vcpu 2 --min-memory-gib 4 --top 2
RANK  CLOUD  REGION     MACHINE     ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY
   1  aws    us-east-1  t4g.medium  arm64            2         4.0  0.0179          49%  15-20%        ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   2  aws    us-east-1  c7g.large   arm64            2         4.0  0.0222          57%  5-10%         ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET

$ spotinfo recommend --cloud gcp --architecture arm64 --min-vcpu 2 --min-memory-gib 8 --top 3
RANK  CLOUD  REGION       MACHINE         ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY
   1  gcp    us-central1  n4a-standard-2  arm64            2         8.0  0.032332        58%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   2  gcp    us-central1  t2a-standard-2  arm64            2         8.0  0.036064        53%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   3  gcp    us-central1  c4a-standard-2  arm64            2         8.0  0.040764        54%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET

$ spotinfo recommend --cloud azure --architecture arm64 --min-vcpu 4 --min-memory-gib 16 --top 3
RANK  CLOUD  REGION        MACHINE            ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY
   1  azure  centralindia  Standard_D4ps_v6   arm64            4        16.0  0.017076        81%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   2  azure  centralindia  Standard_D4ps_v5   arm64            4        16.0  0.018665        81%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   3  azure  centralindia  Standard_D4pds_v5  arm64            4        16.0  0.022361        81%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
```

`unavailable` in the `RISK` column is a real value, not a blank. GCP and Azure publish no
interruption figure that can ship in a snapshot, and the tool reports that absence rather than
guessing a zero. See [clouds.md](clouds.md#why-the-risk-column-differs).

## Multi-cloud

### 8. The same requirement on all three clouds

```console
$ for c in aws gcp azure; do
    spotinfo recommend --cloud "$c" --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 \
      --top 1 --offline --output csv | tail -n +2
  done
1,aws,ap-southeast-3,t3.xlarge,x86_64,4,16,0.024100000,73,<5%,ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
1,gcp,us-central1,c3d-standard-4,x86_64,4,16,0.042496000,76,unavailable,ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
1,azure,centralindia,Standard_D4as_v5,x86_64,4,16,0.020513000,81,unavailable,ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
```

One command per cloud, on purpose. A single ranked page mixing three clouds would order an
AWS row that carries a measured interruption bucket against two that carry `unavailable`, and
the cheapest cell would win by measuring least.

Note that GCP shows the highest price of the three and a higher savings percent than AWS (76%
against 73%). `SAVINGS` is measured against that cloud's own On-Demand list price, so it ranks
rows within a cloud and says nothing across them. Compare `USD/HOUR`.

## Output formats

`list` renders `number|text|json|table|csv`; `recommend` renders `text|json|table|csv`. `table`
is the default for both.

### 9. `number`, for one figure in a shell variable

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --output number
59
```

That is the savings percent, not a price. It is `list`-only —
`recommend --output number` is refused, because one savings percent cannot describe a ranked
page.

### 10. `text`, one line per row

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --output text
machine=m5.large, vCPU=2, memory=8GiB, saving=59%, risk='>20%', price=0.0399
```

`recommend --output text` uses the `spotinfo.recommend/v3` field names, so a log line and the
JSON document agree on what a value is called:

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 8 --top 1 --sort risk --output text
rank=1, cloud=gcp, region=us-central1, machine=n2d-standard-2, architecture=x86_64, vcpu=2, memory_gib=8, spot_usd_per_hour=0.026912000, savings_percent=68, risk=unavailable, rationale_codes=ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
```

### 11. `csv`, for a spreadsheet

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --top 2 --output csv
Rank,Cloud,Region,Machine,Architecture,vCPU,Memory GiB,USD/Hour,Savings over On-Demand,Risk,Why
1,aws,us-east-1,t2.medium,x86_64,2,4,0.016100000,62,<5%,ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
2,aws,us-east-1,t3.medium,x86_64,2,4,0.017500000,60,>20%,ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
```

`list --output csv` carries a `Price Source` column that the other formats do not:

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --output csv
Machine,vCPU,Memory GiB,Savings over On-Demand,Risk,USD/Hour,Price Source
m5.large,2,8,59,>20%,0.0399,static
```

### 12. `json`

`list` emits `spotinfo.list/v1` and `recommend` emits `spotinfo.recommend/v3`. Both are
objects, not bare arrays: rows live under `candidates` and `recommendations` respectively, and
every document carries a `data_source` block naming every document it was read from.

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --output json | jq '.candidates[0]'
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
```

**Prices are JSON strings, not numbers.** They are fixed-point decimals, so a consumer that
needs arithmetic must convert — `jq` calls that `tonumber`. `on_demand_usd_per_hour` is `null`
on AWS, which publishes a savings percent rather than the list price behind it, and populated
on GCP and Azure.

## Automation

### 13. Cheapest row from a family

```console
$ spotinfo list --offline --region us-east-1 --machine '^c7g\.' --output json \
    | jq -r '.candidates | sort_by(.spot_usd_per_hour | tonumber) | .[0]
             | "\(.machine) \(.spot_usd_per_hour) \(.risk.label)"'
c7g.medium 0.006500000 <5%
```

### 14. Instance-type list for an Auto Scaling group

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 \
    --min-vcpu 4 --min-memory-gib 16 --top 5 --output json \
    | jq -r '[.recommendations[].machine] | unique | join(",")'
m5.xlarge,m6a.xlarge,t2.xlarge,t3.xlarge,t3a.xlarge
```

### 15. Budget gate for a pipeline

```bash
#!/bin/bash
set -euo pipefail
MAX_PRICE=0.05
MACHINE='^m5\.large$'
REGION=us-east-1

PRICE=$(spotinfo list --region "$REGION" --machine "$MACHINE" --offline --output json \
  | jq -r '.candidates[0].spot_usd_per_hour')

if [ "$(echo "$PRICE > $MAX_PRICE" | bc -l)" -eq 1 ]; then
  echo "spot price $PRICE exceeds budget $MAX_PRICE"
  exit 1
fi
echo "spot price $PRICE is within budget $MAX_PRICE"
```

```console
$ ./gate.sh
spot price 0.039900000 is within budget 0.05
```

Drop `--offline` to price against the live feeds. Keeping it makes the gate answer from the
embedded snapshot in about a tenth of a second and never depend on a network the build agent
may not have.

### 16. Which sources answered

```console
$ spotinfo list --cloud gcp --machine '^n2-standard-4$' --output json | jq -r '.data_source.mode, (.data_source.sources | length)'
embedded-snapshot
5
```

`mode` is one of `embedded-snapshot`, `cached` or `live`. `cached` is a copy this machine
fetched earlier and has not revalidated; it is reported as itself rather than as `live`,
because "matches the vendor right now" is a claim only a confirmed fetch can make. A trimmed
answer also carries `sources_omitted` — `spotinfo --mcp`'s `list_cloud_regions` tool resolves
the full list.

## MCP

`spotinfo --mcp` speaks MCP over stdio. Three tools: `list_cloud_regions`,
`list_spot_machines`, `recommend_spot_machines`. Arguments are the CLI flag names with `--`
stripped and `-` replaced by `_`; the repeatable `--region` becomes the array `regions`.
There is no `--output` argument — MCP is always JSON.

### 17. Drive it by hand

```console
$ printf '%s\n' \
   '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}' \
   '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
   '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
   | spotinfo --mcp 2>/dev/null | jq -r 'select(.id==2) | [.result.tools[].name] | join(" ")'
list_cloud_regions list_spot_machines recommend_spot_machines
```

`--offline` is a **subcommand** flag and cannot be combined with `--mcp`. Pass the tool
argument `"offline": true` instead:

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "tools/call",
  "params": {
    "name": "recommend_spot_machines",
    "arguments": {
      "cloud": "azure",
      "architecture": "arm64",
      "min_vcpu": 4,
      "min_memory_gib": 16,
      "top": 1,
      "offline": true
    }
  }
}
```

A refused question comes back as a `spotinfo.error/v1` payload with `isError: true`, carrying
the same wording the CLI prints:

```json
{
  "schema_version": "spotinfo.error/v1",
  "code": "UNSUPPORTED_CAPABILITY",
  "message": "gcp: unsupported capability: os windows: this cloud publishes spot prices for linux only",
  "cloud": "gcp"
}
```

For a Claude Desktop configuration, see
[claude-desktop-setup.md](claude-desktop-setup.md).

## Paths that need a credential

The commands below are shown without output because this page's rule is that every output
block was observed, and these need credentials. Each states what it requires.

### 18. AWS placement scores

Needs AWS credentials and the `ec2:GetSpotPlacementScores` permission. A score rates the whole
request rather than one machine, and `--offline` does not suppress the call.

```bash
# regional scores, 8 or better
spotinfo list --region us-east-1 --machine '^m5\.' --with-score --min-score 8 --sort score --order desc

# zone-level instead of regional
spotinfo list --region us-east-1 --machine '^m5\.large$' --with-score --az
```

`--min-score`, `--az` and `--score-timeout` all require `--with-score` — it is what fetches
the figures they filter, split and wait for.

### 19. GCP live preemption risk

Needs Application Default Credentials and a project. The project is never inferred from
gcloud's ambient `core/project`, because the call is billed to whatever it names.

```bash
spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 \
  --live-risk --gcp-project my-project
```

A lookup that fails is a warning on stderr and `risk=unavailable` on the page, at exit 0 —
never a failed run. [troubleshooting.md](troubleshooting.md#--live-risk-returns-unavailable-for-every-machine)
tables the causes.

### 20. GCP regions beyond the snapshot

Needs a Cloud Billing Catalog API key, in `--gcp-billing-key` or `SPOTINFO_GCP_BILLING_KEY`.
It prices one invocation and enters no snapshot.

```bash
spotinfo list --cloud gcp --region europe-west1 --gcp-billing-key "$KEY" --output text
```

A key the API refuses is not an error: the run logs it at `--debug` and answers from the
committed snapshot.

## Azure live prices, without a credential

Naming one or two Azure regions refreshes their prices from the anonymous Retail Prices API
before answering. No subscription, no login.

```console
$ spotinfo list --cloud azure --region westeurope --refresh --machine '^Standard_D2as_v5$' --output json | jq -r .data_source.mode
live
```

`--region all` (the default), three or more regions, and `--offline` all answer from the
committed catalogue instead — the sweep is bounded by what it costs, at 10 pages and 5.5 MB
per region.

## See Also

- [Usage Guide](usage.md) — complete command reference
- [Cloud coverage](clouds.md) — what each cloud serves, and what it refuses
- [AWS Spot Placement Scores](aws-spot-placement-scores.md)
- [Troubleshooting](troubleshooting.md)
