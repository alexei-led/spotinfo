# Quick start

Five minutes, from install to a ranked answer. For every install method, read
[installation.md](installation.md).

## 1. Install

```bash
brew install alexei-led/tap/spotinfo
```

## 2. Ask for a machine

Give `recommend` an architecture and a size floor. These three flags are required:

```bash
spotinfo recommend --architecture x86_64 --cpu 4 --memory 16
```

```
RANK  REGION     INSTANCE         ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR  SAVINGS  INTERRUPTION  WHY
   1  us-east-1  t3.xlarge        x86_64           4        16.0    0.0502      61%  <5%           ARCHITECTURE_MATCH,...
   2  us-east-1  m7i-flex.xlarge  x86_64           4        16.0    0.0888      51%  <5%           ARCHITECTURE_MATCH,...
   3  us-east-1  d3en.xlarge      x86_64           4        16.0    0.0929      71%  <5%           ARCHITECTURE_MATCH,...
```

The `WHY` column lists the rule each candidate satisfied. It is deterministic, so two runs on
the same data give the same codes.

## 3. Change the cloud

```bash
spotinfo recommend --cloud gcp   --architecture x86_64 --cpu 4 --memory 16
spotinfo recommend --cloud azure --architecture arm64  --cpu 4 --memory 16
```

GCP and Azure need no credentials and make no request. Both report `RISK: unavailable`,
because neither publishes an interruption figure that can ship in the binary. Read
[clouds.md](clouds.md) for what each cloud serves.

## 4. Change the workload

`--workload` sets the ranking policy and the interruption ceiling.

| Workload | Interruption ceiling | Clouds          |
| -------- | -------------------- | --------------- |
| `cost`   | none                 | aws, gcp, azure |
| `web`    | 5%                   | aws             |
| `ci`     | 16%                  | aws             |
| `batch`  | 22%                  | aws             |

```bash
spotinfo recommend --architecture x86_64 --cpu 2 --memory 8 --workload batch
```

The workload also selects the report schema on AWS. `cost` emits `spotinfo.recommend/v2`.
`web`, `ci` and `batch` emit `spotinfo.recommend/v1`.

## 5. Widen the search

```bash
# Every AWS region, five results
spotinfo recommend --architecture x86_64 --cpu 4 --memory 16 --region all --top 5

# Two named regions
spotinfo recommend --architecture arm64 --cpu 2 --memory 8 \
  --region us-east-1 --region eu-west-1

# A machine-name pattern, as an RE2 regexp
spotinfo recommend --architecture x86_64 --cpu 4 --memory 16 --instance "^c[0-9]"

# A price ceiling
spotinfo recommend --architecture x86_64 --cpu 4 --memory 16 --budget 0.06
```

If a ceiling admits nothing, the command names the screen that emptied the set:

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --cpu 4 --memory 16 --budget 0.001
spotinfo: no candidates: no machine costs 0.001000000 USD per hour or less; gcp publishes nothing below 0.042496000 USD per hour, the price of c3d-standard-4 in us-central1
```

On AWS, `--region` defaults to `us-east-1`. On GCP and Azure it defaults to every published
region.

## 6. Get JSON

```bash
spotinfo recommend --cloud azure --architecture arm64 --cpu 4 --memory 16 --output json
```

The report carries the request that produced it, the ranking policy, every source URL with
its SHA-256, and the ranked machines. Pipe it into `jq`:

```bash
spotinfo recommend --cloud gcp --architecture x86_64 --cpu 8 --memory 32 --output json \
  | jq -r '.recommendations[] | "\(.machine) \(.spot_usd_per_hour)"'
```

## 7. Explore AWS directly

The bare command is a different tool for a different question. It filters the AWS Spot
Advisor and shows an interruption column for every match. It is AWS-only.

```bash
# Every m5 machine in one region
spotinfo --type "m5.*" --region us-east-1

# With placement scores, cheapest first
spotinfo --type "m5.*" --with-score --sort price

# CSV for a spreadsheet
spotinfo --cpu 4 --memory 16 --region all --output csv
```

## 8. Work offline

```bash
spotinfo recommend --architecture x86_64 --cpu 2 --memory 8 --offline
```

`--offline` answers from the snapshot inside the binary and makes no request at all. It
applies to AWS, and to Azure when you name one or two regions — those refresh their prices
from Azure's anonymous Retail Prices API, and `--offline` skips that. GCP has no live price
path, so the flag changes nothing there.

## Next

- [Usage guide](usage.md) — every flag and every output format
- [Cloud coverage](clouds.md) — what each cloud serves and refuses
- [Examples](examples.md) — Terraform, CI pipelines, cost monitors
- [MCP server](mcp-server.md) — let an assistant ask these questions
