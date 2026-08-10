[![CI](https://github.com/alexei-led/spotinfo/actions/workflows/ci.yaml/badge.svg)](https://github.com/alexei-led/spotinfo/actions/workflows/ci.yaml) [![Docker](https://github.com/alexei-led/spotinfo/actions/workflows/docker.yaml/badge.svg)](https://github.com/alexei-led/spotinfo/actions/workflows/docker.yaml) [![Go Report Card](https://goreportcard.com/badge/github.com/alexei-led/spotinfo)](https://goreportcard.com/report/github.com/alexei-led/spotinfo) [![MCP Compatible](https://img.shields.io/badge/MCP-Compatible-blue)](docs/mcp-server.md)

# spotinfo

**Command-line tool for Spot instance exploration across AWS, GCP and Azure, with AWS placement score analysis**

`spotinfo` is a powerful CLI tool and [Model Context Protocol (MCP) server](#mcp-integration) that provides comprehensive AWS EC2 Spot Instance information, including real-time placement scores, pricing data, and interruption rates. Perfect for DevOps engineers optimizing cloud infrastructure costs.

AWS is the full surface. GCP and Azure are served offline from committed price snapshots and are reachable through `spotinfo recommend --cloud <id>`; neither publishes interruption risk in a redistributable form, so both answer only the risk-free `cost` workload. GCP can additionally fetch its preemption rate from an authenticated per-project API with [`--live-risk`](#-live-preemption-risk-gcp-opt-in).

## Key Features

### 🎯 **AWS Spot Placement Scores**
- **Real-time placement scores** (1-10 scale) for launch success probability
- **Regional and AZ-level analysis** with visual indicators (🟢🟡🔴)
- **Smart contextual scoring** - scores reflect entire request success likelihood
- **Freshness tracking** with cache optimization

### 🔍 **Advanced Filtering & Analysis**
- **Regex-powered** instance type matching (`t3.*`, `^(m5|c5)\.(large|xlarge)$`)
- **Multi-dimensional filtering** by vCPU, memory, price, regions, and placement scores
- **Cross-region comparison** with `--region all` support
- **Flexible sorting** by price, reliability, savings, or placement scores

### 💡 **Single-Instance Recommendations**
- `recommend` requires an `x86_64` or `arm64` architecture plus positive vCPU and memory minima
- Workload caps apply to a risk bucket's maximum: web ≤5%, CI ≤16%, batch ≤22%. On AWS the reachable Advisor buckets make those effective ceilings `<5%`, `<=15%` and `<=20%`; `cost` applies no interruption constraint and works on every cloud
- The default concise table includes deterministic rationale codes; `--output json` emits a versioned request-and-results wrapper
- AWS v1 ranks by price, interruption, and right-sizing excess. v2 ranks by price and right-sizing excess, then region and machine; unavailable risk is never compared. Savings is displayed but never used to rank
- Repeated recommendation regions are deduplicated; `all` cannot be mixed with explicit regions
- **GCP support** (offline, no credentials): `spotinfo recommend --cloud gcp` serves `us-central1`, Linux, x86\_64 and arm64; risk is `unavailable` unless `--live-risk` fetches it, and only `--workload cost` applies either way. The root query command is AWS-only.
- **Azure support** (offline, no credentials): `spotinfo recommend --cloud azure` serves 55 regions,
  Linux, x86\_64 and arm64; risk is always `unavailable`, so only `--workload cost` applies. The root
  query command is AWS-only.

### 📊 **Multiple Output Formats**
- **Visual formats**: Table with emoji indicators, plain text
- **Data formats**: JSON, CSV for automation and scripting
- **Clean separation**: Visual indicators only in human-readable formats

### 💰 **Live Price Fallback**
- **Automatic enrichment** for newer instance types missing from the static pricing feed
- **EC2 DescribeSpotPriceHistory API** fetches current prices when static data shows $0
- **Price source indicators** in every format: a `*` suffix in `text` and `table`, a
  `Price Source` column in `csv`, and a `live_price` boolean in `json` (always present)
- **Graceful degradation** — works without AWS credentials, just shows $0 for missing types.
  The credential chain is probed once per run, so a machine without credentials skips the
  live-price and placement-score calls instead of waiting for each to time out

### 🔬 **Live Preemption Risk (GCP, opt-in)**

GCP publishes its Spot preemption rate only through the authenticated, per-project
`compute.advice.capacityHistory` API, so it cannot ship in the committed snapshot —
the answer differs per caller and is not redistributable. `--live-risk` fetches it
for one invocation:

```bash
spotinfo recommend --cloud gcp --architecture x86_64 --cpu 4 --memory 16 \
  --live-risk --gcp-project my-project
```

```
RANK  CLOUD  REGION       MACHINE         ... USD/HOUR  SAVINGS  RISK
   1  gcp    us-central1  c3d-standard-4  ... 0.042496      76%  6.3% avg
   2  gcp    us-central1  n2d-standard-4  ... 0.053824      68%  17.5% avg
```

- **Opt-in.** The default path stays offline and answers in about a tenth of a second.
- **Project is never guessed.** Pass `--gcp-project` or set `GOOGLE_CLOUD_PROJECT`;
  the call is billed to whichever project it names, so gcloud's ambient
  `core/project` is deliberately not used.
- **Credentials** come from Application Default Credentials (`gcloud auth
  application-default login`, a service account, or the GCE metadata server).
  Without them the command still answers, reporting `unavailable`.
- **One call per recommendation**, on the ranked page only — never the catalogue.
- **`kind` is `preemption_rate`, not `interruption_bucket`.** Google defines it as
  (preempted Spots) / (Spots that stopped running); AWS publishes the fraction of
  *running* instances interrupted. The numbers are not comparable, so
  `--workload web|ci|batch` **still refuses on GCP** — the ceilings are AWS Advisor
  bucket boundaries. Live risk makes the figure visible, not filterable.

### 🌐 **Network Resilience**
- **Embedded data** for offline functionality
- **Graceful fallbacks** when AWS APIs are unavailable
- **Real-time API integration** with intelligent caching

### ⚡ **Feed Cache and Offline Mode**

The two AWS feeds dominate an invocation, so fetched copies are cached on disk. A warm
cache answers in about a tenth of a second against roughly a second and a half cold.

| flag / variable | effect |
|---|---|
| `--offline` | answer from the committed snapshot; makes no request at all, including no `DescribeSpotPriceHistory` |
| `--refresh` | ignore any cached copy and fetch both feeds again |
| `SPOTINFO_CACHE_DIR` | override the cache location (default: `os.UserCacheDir()/spotinfo`) |
| `SPOTINFO_CACHE=off` | disable the cache entirely |

Cached entries expire on a per-feed schedule, because the feeds differ: the Spot Advisor
document is rewritten rarely and takes over a second to transfer, so it is held for **24
hours**; prices change through the day and transfer in a tenth of a second, so they are
held for **1 hour**. An expired entry is revalidated with `If-None-Match` /
`If-Modified-Since` rather than re-downloaded — both feeds serve ETags, so a `304` costs
one round trip and no payload.

Resolution order is: fresh cache → origin → *expired* cache → committed snapshot. An
expired entry outranks the snapshot because it is AWS data that is merely old, while the
snapshot is AWS data that is old *and* frozen at build time. Every cache failure is
non-fatal: a read-only filesystem costs time, not answers.

Because a cached answer is neither live nor embedded, it is reported as its own state —
`data_source.mode` is `cached` in `spotinfo.recommend/v2`, and `data_freshness` is
`cached` in the v1 MCP response (its `data_source` stays `aws`; provenance is unchanged,
only established recency differs). A copy the origin confirmed with a `304` is `live`: it
matches AWS right now.

## Quick Start

### Installation

```bash
# macOS with Homebrew
brew tap alexei-led/tap
brew install alexei-led/tap/spotinfo

# Linux/Windows: Download from releases
curl -L https://github.com/alexei-led/spotinfo/releases/latest/download/spotinfo_linux_amd64.tar.gz | tar xz

# Docker
docker pull ghcr.io/alexei-led/spotinfo:latest
```

**Supported platforms**: macOS, Linux, Windows on AMD64/ARM64

### Basic Usage

```bash
# Get placement scores for instances
spotinfo --type "m5.large" --with-score

# Find high-reliability instances with budget constraints
spotinfo --cpu 4 --memory 16 --with-score --min-score 8 --price 0.30

# Compare across regions with AZ-level details
spotinfo --type "t3.*" --with-score --az --region "us-east-1" --region "eu-west-1"

# Export data for automation
spotinfo --type "c5.*" --with-score --min-score 7 --output json

# Get low-interruption Arm single-instance candidates (default table output)
spotinfo recommend --architecture arm64 --workload web --vcpu 2 --memory-gib 8 --region us-east-1

# Emit the versioned JSON report, using Windows Spot prices
spotinfo recommend --architecture x86_64 --cpu 2 --memory 8 --os windows --output json

# GCP Spot VMs (offline, no credentials, us-central1, cost workload)
spotinfo recommend --cloud gcp --architecture x86_64 --cpu 2 --memory 8 --top 3

# Azure Spot VMs (offline, no credentials, 55 regions, cost workload)
spotinfo recommend --cloud azure --architecture arm64 --cpu 2 --memory 8 --top 3
```

### New Placement Score Flags

| Flag | Description |
|------|-------------|
| `--with-score` | Enable real-time placement score fetching |
| `--az` | Get AZ-level scores instead of regional |
| `--min-score N` | Filter instances with score ≥ N (1-10) |
| `--sort score` | Sort by placement score |

📖 **Complete reference**: [Usage Guide](docs/usage.md) | [Examples](docs/examples.md)

## MCP Integration

`spotinfo` functions as a **[Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server**, enabling AI assistants to directly query Spot instance data through natural language.

Three tools are served: `find_spot_instances` and `list_spot_regions` are AWS-only and byte-compatible with previous releases, while `recommend_spot_instances` answers for AWS, GCP and Azure and emits the `spotinfo.recommend/v2` payload.

### Quick Setup with Claude Desktop

```json
{
  "mcpServers": {
    "spotinfo": {
      "command": "spotinfo",
      "args": ["--mcp"]
    }
  }
}
```

**Ask Claude**: *"Find cheapest t3 instances with placement score >7"*, *"Compare m5.large prices across US regions"*, or *"Cheapest arm64 Azure Spot VMs with 4 vCPUs and 16 GiB"*

🤖 **Full setup guide**: [MCP Server Documentation](docs/mcp-server.md)

## Understanding AWS Spot Placement Scores

**🚨 Key Insight**: Placement scores are **contextual** - they evaluate success probability for your entire request, not individual instance types.

```bash
# Lower score (limited flexibility)
spotinfo --type "t3.micro" --with-score
# Score: 🔴 3

# Higher score (flexible options)
spotinfo --type "t3.*" --with-score  
# Score: 🟢 9
```

This is **expected AWS behavior** - providing multiple instance types gives AWS more fulfillment options.

📚 **Learn more**: [AWS Spot Placement Scores](docs/aws-spot-placement-scores.md)

## Documentation

| Document | Description |
|----------|-------------|
| **[Usage Guide](docs/usage.md)** | Complete CLI reference with all flags and examples |
| **[AWS Spot Placement Scores](docs/aws-spot-placement-scores.md)** | Deep dive into placement scores with visual guides |
| **[Examples & Use Cases](docs/examples.md)** | Real-world DevOps scenarios and automation patterns |
| **[MCP Server Setup](docs/mcp-server.md)** | Model Context Protocol integration guide |
| **[Data Sources](docs/data-sources.md)** | AWS, GCP, and Azure feeds, snapshots, caching, and troubleshooting |

## AWS Credentials

AWS credentials are **optional** but recommended for complete functionality:

| Feature | Without Credentials | With Credentials |
|---------|-------------------|------------------|
| Spot advisor data | Full access | Full access |
| Static pricing | Full access | Full access |
| Live price fallback | Unavailable (shows $0 for missing types) | Fetches current prices via EC2 API |
| Placement scores | Unavailable | Real-time scores (1-10) |

Required IAM permissions for full functionality:
- `ec2:DescribeSpotPriceHistory` — live price fallback for newer instance types
- `ec2:GetSpotPlacementScores` — placement score analysis

Credentials are loaded via the [AWS SDK default credential chain](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html) (environment variables, shared config, IAM roles, etc.).

## Development

**Requirements**: Go 1.26+, make, golangci-lint

```bash
# Build (alias for `make build`; run tests separately)
make build

# Update embedded data (usually unnecessary — a weekly workflow opens a PR for this)
# verify-data also checks reviewed architecture snapshot metadata and Advisor-family coverage.
make update-data update-price verify-data

# Update embedded GCP data (weekly update-gcp-data workflow normally handles this)
make update-gcp-data verify-data

# Update embedded Azure data (weekly update-azure-data workflow normally handles this)
make update-azure-data verify-data

# Docker build
docker buildx build --platform=linux/arm64,linux/amd64 -t spotinfo .
```

**CI/CD**: Automated testing, linting, releases, and multi-arch Docker builds via GitHub Actions

## Contributing

Contributions welcome! Please read the development commands in [CLAUDE.md](CLAUDE.md) and ensure all tests pass.

## License

Apache 2.0 License - see [LICENSE](LICENSE) for details.

