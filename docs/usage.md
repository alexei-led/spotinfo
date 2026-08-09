# Usage Guide

## Command Overview

`spotinfo` is a command-line tool for exploring AWS EC2 Spot instances with advanced filtering, sorting, and placement score analysis capabilities. The `recommend` subcommand supports multiple cloud providers; see [Cloud Capability Matrix](#cloud-capability-matrix) below.

## Basic Syntax

```bash
spotinfo [global options]
```

## Global Options

### Cloud Provider
| Flag | Description | Example |
|------|-------------|---------|
| `--cloud value` | Cloud provider: `aws`, `gcp`, `azure`. The root query command is AWS-only — it renders an interruption column no other cloud publishes — so `--cloud` selects the provider for `recommend` | `spotinfo --cloud gcp recommend ...` |

### Instance Selection
| Flag | Description | Example |
|------|-------------|---------|
| `--type value, --machine value` | EC2 instance type (supports RE2 regex) | `--type "m5.large"` or `--type "t3.*"` |
| `--os value` | Operating system filter | `--os linux` (default) or `--os windows` |

### Geographic Filtering
| Flag | Description | Example |
|------|-------------|---------|
| `--region value` | AWS regions (can be used multiple times) | `--region us-east-1 --region us-west-2` |
| | Use "all" for all regions | `--region all` |

### Resource Filtering
| Flag | Description | Example |
|------|-------------|---------|
| `--cpu value` | Minimum vCPU cores | `--cpu 4` |
| `--memory value` | Minimum memory in GiB | `--memory 16` |
| `--price value` | Maximum price per hour (USD) | `--price 0.50` |

### AWS Spot Placement Scores
| Flag | Description | Example |
|------|-------------|---------|
| `--with-score` | Enable placement score fetching | `--with-score` |
| `--az` | Request AZ-level scores (use with --with-score) | `--with-score --az` |
| `--min-score value` | Minimum placement score (1-10) | `--min-score 7` |
| `--score-timeout value` | Timeout for score API in seconds | `--score-timeout 30` |

### Sorting and Output
| Flag | Description | Example |
|------|-------------|---------|
| `--sort value` | Sort by: interruption, type, savings, price, region, score | `--sort score` |
| `--order value` | Sort order: asc or desc | `--order desc` |
| `--output value` | Output format: table, json, csv, text, number | `--output json` |

### System Options
| Flag | Description | Example |
|------|-------------|---------|
| `--mcp` | Run as MCP server instead of CLI | `--mcp` |
| `--debug` | Enable debug logging | `--debug` |
| `--quiet` | Quiet mode (errors only) | `--quiet` |
| `--json-log` | Output logs in JSON format | `--json-log` |
| `--help, -h` | Show help | `--help` |
| `--version, -v` | Print version | `--version` |

## Recommendations

`recommend` is an additive subcommand; root invocation and root `--type` behavior are unchanged. It recommends **individual instances**, not fleets or placement plans. Positive `--cpu` and `--memory` requests are required so results can be right-sized.

```bash
spotinfo recommend --architecture <x86_64|arm64> --cpu N --memory N [flags]
```

| Flag | Description | Default |
|------|-------------|---------|
| `--cloud aws|gcp|azure` | Cloud provider | `aws` |
| `--architecture x86_64|arm64` | Required processor architecture | none |
| `--instance REGEXP, --machine REGEXP` | RE2 machine-type filter; combined with architecture using AND semantics | none |
| `--region REGION` | One or more regions; `all` uses all regions the selected cloud publishes | `us-east-1` (AWS name; unset on non-AWS clouds expands to all their published regions) |
| `--cpu N`, `--vcpu N` | Required positive minimum vCPU count | none |
| `--memory N`, `--memory-gib N` | Required positive minimum GiB of memory | none |
| `--budget USD` | Positive maximum USD per candidate instance-hour | none |
| `--os linux|windows` | Operating-system price stream | `linux` |
| `--workload cost|web|ci|batch` | Ranking policy; `cost` ranks by price only and requires no risk data | `web` on clouds with risk data; `cost` otherwise |
| `--top N` | Maximum candidates returned; the v2 path (any non-AWS cloud, or AWS with `--workload cost`) caps it at 50 | `3` |
| `--output table|json` | Recommendation output format | `table` |

`cost` ranks purely by price with no interruption constraint and works on every cloud. The caps are applied to a risk bucket's *maximum* percentage: `web` admits a bucket whose maximum is at most 5%, `ci` at most 16%, `batch` at most 22%. On AWS the reachable Advisor buckets make those effective ceilings 5%, 15% and 20% — `web` accepts only `<5%`, `ci` accepts through `10-15%`, and `batch` accepts through `15-20%`. `web`, `ci`, and `batch` require a cloud that publishes interruption or preemption risk; requesting them on GCP or Azure returns `UNSUPPORTED_CAPABILITY`. `--cpu` and `--memory` are inclusive minima, as is `--budget`; a supplied budget must be greater than zero. A budget finer than nine decimal places — what dividing a monthly figure by 720 produces — is truncated to nine, which can only tighten the ceiling. Prices of zero, missing prices, and non-finite prices are never candidates; existing live-price enrichment can provide a usable positive price. `--os` selects Linux or Windows pricing before ranking. Repeated `--region` values are trimmed, deduplicated, and sorted before fetching and reporting; repeated `--region all` becomes one `all`, while `all` combined with an explicit region (or an empty region value) is rejected before fetching.

The shared root `--cloud`, `--os`, `--region`, `--output`, `--cpu`, `--memory`, and the machine filter (`--machine`, spelled `--type` on the root command and `--instance` on `recommend`) can be placed before `recommend` (for example, `spotinfo --cloud gcp --output json --cpu 2 --memory 8 recommend --architecture x86_64`). Explicit flags after `recommend` take precedence; if neither is set, the command defaults apply. `--architecture`, `--budget`, `--workload`, and `--top` remain command-local.

The default `table` output is concise and has rank, region, instance, architecture, vCPU, memory GiB, USD/hour, savings, interruption, and `WHY`. `WHY` is a comma-separated list of deterministic rationale codes such as `ARCHITECTURE_MATCH`, `KNOWN_POSITIVE_PRICE`, `VCPU_MINIMUM_MET`, `MEMORY_MINIMUM_MET`, `WORKLOAD_CI_CAP_MET`, and, when applicable, `BUDGET_CAP_MET`. These are facts and policy checks, not generated prose.

Candidates rank by lower USD/hour, lower interruption cap, lower excess vCPU, lower excess memory GiB, region, then instance type. Savings is displayed but is never a ranking input. Invalid input and a valid request with no candidates are errors.

```bash
spotinfo recommend --architecture arm64 --instance '^m[678]g\.' --vcpu 2 --memory-gib 8 \
  --budget 0.20 --workload ci --region us-east-1 --top 2
```

Use `--output json` for a deterministic versioned wrapper rather than a bare array. The normalized request has explicit units, sorted regions, OS, workload, and top; `recommendations` is always an array.

The following JSON example abridges each recommendation item to its identifying fields and rationale; actual items also include region, architecture, vCPU, memory GiB, price, savings, and interruption frequency.

```json
{
  "schema_version": "spotinfo.recommend/v1",
  "request": {
    "architecture": "arm64",
    "instance_regexp": "^m[678]g\\.",
    "regions": ["us-east-1"],
    "os": "linux",
    "minimum_vcpu": 2,
    "minimum_memory_gib": 8,
    "maximum_usd_per_instance_hour": 0.2,
    "workload": "ci",
    "top": 2
  },
  "ranking_policy": ["price_usd_per_hour_ascending", "interruption_frequency_ascending", "excess_vcpu_ascending", "excess_memory_gib_ascending", "region_ascending", "instance_ascending"],
  "recommendations": [{"instance": "m7g.large", "rationale_codes": ["ARCHITECTURE_MATCH", "BUDGET_CAP_MET", "KNOWN_POSITIVE_PRICE", "MEMORY_MINIMUM_MET", "VCPU_MINIMUM_MET", "WORKLOAD_CI_CAP_MET"]}]
}
```

v1 does not use placement scores, GPUs, ML, Markdown output, composite scores, or savings ranking. Architecture comes from a committed reviewed family snapshot; unknown families fail closed and no runtime AWS metadata call is made. Snapshot `provenance` must be non-empty and `reviewed_at` must be a valid `YYYY-MM-DD` date. See [Data Sources](data-sources.md) for the manual reviewed update procedure and freshness limitations.

Non-AWS clouds and AWS with `--workload cost` emit `spotinfo.recommend/v2` (see [API Reference](api-reference.md)). v2 prices are decimal strings with exactly nine fractional digits. AWS with `--workload web`, `ci`, or `batch` continues to emit v1.

The v2 `table` output is a different shape from the v1 table above: `RANK CLOUD REGION MACHINE ARCHITECTURE vCPU MEMORY GiB USD/HOUR RISK WHY`. There is no savings column, `CLOUD` is added, and `RISK` replaces `INTERRUPTION` — it prints the risk label when the cloud publishes one and the status (`unavailable`) when it does not, so a cost recommendation never reads as a safety claim. The v2 rationale vocabulary also differs: `ARCHITECTURE_MATCH`, `KNOWN_POSITIVE_PRICE`, `RESOURCE_MINIMUMS_MET` (one code for both minima, where v1 emits `VCPU_MINIMUM_MET` and `MEMORY_MINIMUM_MET`), then either `COST_POLICY` or `WORKLOAD_<POLICY>_CAP_MET`, plus `BUDGET_CAP_MET` and `MACHINE_PATTERN_MATCH` when those filters were applied.

## Cloud Capability Matrix

| Cloud | Entry point | Regions | OS | Architectures | Risk data | Workloads |
|-------|-------------|---------|-----|---------------|-----------|-----------|
| `aws` | root + `recommend` | all Advisor regions | linux, windows | x86_64, arm64 | interruption buckets | cost, web, ci, batch |
| `gcp` | `recommend` only | `us-central1` | linux | x86_64, arm64 | unavailable | cost |
| `azure` | `recommend` only | 8 regions | linux | x86_64, arm64 | unavailable | cost |

**GCP notes:**

- The root query command (`spotinfo --cloud gcp ...`) is not supported; it requires interruption data and returns `UNSUPPORTED_CAPABILITY`.
- Only `us-central1` is served. Naming any other region in `--region` returns `NO_CANDIDATES`. An unset `--region` expands to all GCP-published regions, which is currently only `us-central1`.
- `--os windows` returns `UNSUPPORTED_CAPABILITY`.
- Arm series: `c4a`, `n4a`, `t2a`. x86_64 series: `c2`, `c3`, `c3d`, `c4`, `c4d`, `e2`, `m1`, `m2`, `m3`, `n1`, `n2`, `n2d`, `n4`, `n4d`, `t2d`.
- Every machine carries a Spot price, a paired On-Demand price, and a derived savings percent. Risk is always `status: "unavailable"`.
- The embedded catalogue is refreshed by `make update-gcp-data` and the weekly `update-gcp-data` workflow.

**Azure notes:**

- The root query command (`spotinfo --cloud azure ...`) is not supported; it requires interruption data
  and returns `UNSUPPORTED_CAPABILITY`.
- Eight regions are served: `australiaeast`, `eastus`, `eastus2`, `northeurope`, `southeastasia`,
  `uksouth`, `westeurope`, `westus2`. Naming any other region returns `NO_CANDIDATES`. An unset
  `--region` expands to all eight.
- 224 VM sizes across 26 machine series (37 arm64 sizes in `bpsv2`, `dpsv5`, `dpdsv5`, `dpsv6`,
  `epsv5`). `--machine` accepts the full Azure size name, for example `Standard_D2s_v5`.
- Azure publishes eviction rates only through Resource Graph and Resource SKUs, both of which need a
  subscription, so every Azure candidate reports `risk.status = "unavailable"`.
- The embedded catalogue is refreshed by `make update-azure-data` and the weekly `update-azure-data`
  workflow.

## Output Formats

### Table Format (Default)
Human-readable table with visual indicators:
```bash
spotinfo --type "t3.micro" --with-score
```
```
┌───────────────┬──────┬────────────┬────────────────────────┬──────────┬────────────────────────────┐
│ INSTANCE INFO │ VCPU │ MEMORY GIB │ SAVINGS OVER ON-DEMAND │ USD/HOUR │ PLACEMENT SCORE (REGIONAL) │
├───────────────┼──────┼────────────┼────────────────────────┼──────────┼────────────────────────────┤
│ t3.micro      │    2 │          1 │                    68% │   0.0043 │ 🟢 9                       │
└───────────────┴──────┴────────────┴────────────────────────┴──────────┴────────────────────────────┘
```

### JSON Format
Structured data for automation:
```bash
spotinfo --type "t3.micro" --with-score --output json
```
```json
[
  {
    "region": "us-east-1",
    "instance": "t3.micro",
    "instance_type": "t3.micro",
    "range": {
      "label": "<5%",
      "min": 0,
      "max": 5
    },
    "savings": 68,
    "info": {
      "cores": 2,
      "emr": false,
      "ram_gb": 1
    },
    "price": 0.0043,
    "region_score": 9,
    "score_fetched_at": "2025-01-26T10:45:02.844335+03:00"
  }
]
```

> The `live_price` field is always present. It is `true` when the price was fetched from the EC2 `DescribeSpotPriceHistory` API (for newer instance types missing from the static pricing feed) and `false` when it came from the embedded static feed.

### CSV Format
Data-only format without visual indicators:
```bash
spotinfo --type "t3.micro" --with-score --output csv
```
```
Instance Info,vCPU,Memory GiB,Savings over On-Demand,Frequency of interruption,USD/Hour,Price Source,Placement Score (Regional)
t3.micro,2,1,68,<5%,0.0043,static,9
```

### Text Format
Plain text for scripting:
```bash
spotinfo --type "t3.micro" --with-score --output text
```
```
type=t3.micro, vCPU=2, memory=1GiB, saving=68%, interruption='<5%', price=0.0043, score=9
```

### Number Format
Single value for automation:
```bash
spotinfo --type "t3.micro" --output number
```
```
68
```

## Usage Patterns

### Quick Instance Assessment
```bash
# Basic instance information
spotinfo --type "m5.large"

# With placement scores
spotinfo --type "m5.large" --with-score
```

### Production Planning
```bash
# High-reliability instances
spotinfo --type "m5.*" --with-score --min-score 8 --region "us-east-1"

# Cross-region comparison
spotinfo --type "c5.xlarge" --with-score --region "us-east-1" --region "eu-west-1"
```

### Cost Optimization
```bash
# Cheapest instances with good reliability
spotinfo --type "t3.*" --with-score --min-score 6 --sort price --order asc

# Budget constraints
spotinfo --cpu 4 --memory 16 --price 0.20 --with-score
```

### Advanced Filtering
```bash
# Regex patterns
spotinfo --type "^(m5|c5)\.(large|xlarge)$" --with-score

# Combined filters
spotinfo --type "r5.*" --cpu 8 --memory 64 --with-score --min-score 7
```

### Availability Zone Analysis
```bash
# AZ-level placement scores
spotinfo --type "m5.large" --with-score --az --region "us-east-1"

# Compare AZ vs regional scores
spotinfo --type "c5.xlarge" --with-score --region "us-east-1"
spotinfo --type "c5.xlarge" --with-score --az --region "us-east-1"
```

## Automation Examples

### Shell Scripts
```bash
#!/bin/bash
# Find best instance for requirements
BEST_INSTANCE=$(spotinfo --cpu 4 --memory 16 --with-score --min-score 8 \
  --sort price --order asc --output json | jq -r '.[0].instance')
echo "Recommended instance: $BEST_INSTANCE"
```

### CI/CD Integration
```bash
# Cost validation in deployment pipeline
MAX_COST="0.50"
INSTANCE_COST=$(spotinfo --type "m5.xlarge" --region "us-east-1" --output number)
if (( $(echo "$INSTANCE_COST > $MAX_COST" | bc -l) )); then
  echo "Instance cost exceeds budget: $INSTANCE_COST > $MAX_COST"
  exit 1
fi
```

### Infrastructure as Code
```bash
# Generate Terraform variables
spotinfo --type "c5.*" --with-score --min-score 7 --output json > spot_instances.json
```

## Exit Codes

| Code | Description |
|------|-------------|
| 0 | Success |
| 1 | General error (invalid arguments, API failure, etc.) |

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `SPOTINFO_MODE` | Set to "mcp" to enable MCP server mode | CLI mode |
| `MCP_TRANSPORT` | MCP transport method | "stdio" |
| `MCP_PORT` | Port for SSE transport | "8080" |

## Performance Considerations

- **Caching**: Placement scores are cached for 10 minutes
- **Rate Limiting**: AWS API calls are rate-limited (10 requests/second)
- **Timeout**: Default score timeout is 30 seconds
- **Large Queries**: Use `--region` filtering for faster results with large instance type patterns

## Error Handling

Common error scenarios and solutions:

```bash
# Invalid instance type pattern
spotinfo --type "[invalid-regex"
# Error: error parsing regexp: missing closing ]

# Insufficient permissions
spotinfo --with-score --region "us-west-2"
# Error: You are not authorized to perform: ec2:GetSpotPlacementScores

# Network timeout
spotinfo --with-score --score-timeout 60
# Increases timeout for slow connections
```

## See Also

- [AWS Spot Placement Scores](aws-spot-placement-scores.md) - Detailed placement score documentation
- [Examples](examples.md) - Real-world usage examples
- [Troubleshooting](troubleshooting.md) - Common issues and solutions
- [MCP Server](mcp-server.md) - Model Context Protocol integration