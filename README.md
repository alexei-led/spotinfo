[![CI](https://github.com/alexei-led/spotinfo/actions/workflows/ci.yaml/badge.svg)](https://github.com/alexei-led/spotinfo/actions/workflows/ci.yaml) [![Docker](https://github.com/alexei-led/spotinfo/actions/workflows/docker.yaml/badge.svg)](https://github.com/alexei-led/spotinfo/actions/workflows/docker.yaml) [![Go Report Card](https://goreportcard.com/badge/github.com/alexei-led/spotinfo)](https://goreportcard.com/report/github.com/alexei-led/spotinfo) [![MCP Compatible](https://img.shields.io/badge/MCP-Compatible-blue)](docs/mcp-server.md)

# spotinfo

**Pick a Spot machine on AWS, GCP or Azure from one command line. No credentials, no network, about a tenth of a second.**

## The problem

Spot capacity is far cheaper than On-Demand — between 40% and 85% off in the snapshots that
ship with this tool. Finding the right machine does not scale by hand:

- Each cloud publishes prices in a different place, in a different shape, on a different
  schedule. AWS has a JSON feed and an interruption Advisor. GCP has server-rendered HTML
  pages. Azure has a REST API and a separate documentation site for vCPU and memory.
- The consoles rank by price. They do not rank by "cheapest machine with at least 4 vCPU and
  16 GiB that my workload can survive losing".
- A price page has no memory of yesterday, so a script that reads one is a scraper you now
  own.

The result is that most teams pick one instance family, hard-code it, and stop looking.

## What spotinfo does

One command ranks real machines against a real requirement, and says where the number came
from.

```console
$ spotinfo recommend --architecture arm64 --cpu 4 --memory 16 --cloud azure
RANK  CLOUD  REGION        MACHINE            ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY
   1  azure  centralindia  Standard_D4ps_v6   arm64            4        16.0  0.017076        81%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   2  azure  centralindia  Standard_D4ps_v5   arm64            4        16.0  0.018665        81%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   3  azure  centralindia  Standard_D4pds_v5  arm64            4        16.0  0.022361        81%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
```

Four properties make that output worth trusting:

**It works with no credentials and no network.** Price snapshots ship inside the binary. A
weekly job refreshes them through a reviewed pull request. AWS additionally uses live feeds
when it can reach them, and falls back to the snapshot when it cannot.

**It says what it does not know.** GCP and Azure publish no redistributable interruption
data, so every candidate reports `RISK: unavailable`. It is never a zero and never a low
bucket. A cloud that measures nothing must not outrank a cloud that measures honestly.

**It refuses questions it cannot answer.** `--workload web` needs interruption data. Ask for
it on a cloud that has none and the command stops before it reads a price:

```console
$ spotinfo recommend --cloud gcp --workload web --architecture x86_64 --cpu 4 --memory 16
spotinfo: gcp: unsupported capability: risk
```

**Every answer carries its source.** The JSON report names each source URL and the SHA-256 of
the document that was read, so a reader can fetch it again and compare.

## Install

```bash
# macOS with Homebrew
brew install alexei-led/tap/spotinfo

# Linux and Windows
curl -L https://github.com/alexei-led/spotinfo/releases/latest/download/spotinfo_linux_amd64.tar.gz | tar xz

# Docker
docker pull ghcr.io/alexei-led/spotinfo:latest
```

macOS, Linux and Windows, on AMD64 and ARM64. Full instructions:
[Installation](docs/installation.md).

## Use it

```bash
# Cheapest Arm machine with 4 vCPU and 16 GiB, on each cloud
spotinfo recommend --cloud aws   --architecture arm64 --cpu 4 --memory 16
spotinfo recommend --cloud gcp   --architecture arm64 --cpu 4 --memory 16
spotinfo recommend --cloud azure --architecture arm64 --cpu 4 --memory 16

# A web tier that must survive interruption: AWS only, interruption capped at 5%
spotinfo recommend --architecture x86_64 --cpu 2 --memory 8 --workload web

# The cheapest AWS region for a machine, across every region
spotinfo recommend --architecture x86_64 --cpu 4 --memory 16 --region all --top 5

# A versioned JSON report for a pipeline
spotinfo recommend --cloud azure --architecture arm64 --cpu 4 --memory 16 --output json

# Explore AWS interruption rates and prices directly
spotinfo --type "m5.*" --region us-east-1

# Add placement scores. This one needs AWS credentials
spotinfo --type "m5.*" --region us-east-1 --with-score
```

Two commands, on purpose. `spotinfo recommend` ranks machines on any cloud. Bare `spotinfo`
explores the AWS Spot Advisor, renders an interruption column, and reads placement scores.
That second command is AWS-only. See [Quick start](docs/quick-start.md) and the
[Usage guide](docs/usage.md).

## Cloud coverage

|                          | AWS                  | GCP                           | Azure         |
| ------------------------ | -------------------- | ----------------------------- | ------------- |
| `spotinfo recommend`     | yes                  | yes                           | yes           |
| `spotinfo` query command | yes                  | no                            | no            |
| Interruption risk        | published            | `unavailable`, or opt-in live | `unavailable` |
| Workloads                | cost, web, ci, batch | cost                          | cost          |
| Operating systems        | linux, windows       | linux                         | linux         |
| Architectures            | x86_64, arm64        | x86_64, arm64                 | x86_64, arm64 |
| Credentials              | optional             | only for `--live-risk`        | never used    |

Region lists, machine counts and the reasoning behind each limit:
[Cloud coverage](docs/clouds.md).

## MCP server

spotinfo is a [Model Context Protocol](https://modelcontextprotocol.io/) server, so an
assistant can ask these questions directly.

```json
{
  "mcpServers": {
    "spotinfo": { "command": "spotinfo", "args": ["--mcp"] }
  }
}
```

Ask: _"Cheapest arm64 Azure Spot VM with 4 vCPUs and 16 GiB"_, or _"Compare m5.large spot
prices across US regions"_.

Setup: [MCP server](docs/mcp-server.md) and [Claude Desktop](docs/claude-desktop-setup.md).

## Documentation

| Document                                                  | What is in it                                |
| --------------------------------------------------------- | -------------------------------------------- |
| [Quick start](docs/quick-start.md)                        | The first five minutes                       |
| [Installation](docs/installation.md)                      | Every install method, and how to check one   |
| [Usage guide](docs/usage.md)                              | Every flag, every output format              |
| [Cloud coverage](docs/clouds.md)                          | What each cloud serves, and what it refuses  |
| [Examples](docs/examples.md)                              | Pipelines, Terraform, CI, cost monitors      |
| [MCP server](docs/mcp-server.md)                          | Tools, arguments, assistant setup            |
| [API reference](docs/api-reference.md)                    | The `spotinfo.recommend/v2` contract         |
| [AWS placement scores](docs/aws-spot-placement-scores.md) | What a score means, and does not             |
| [Data sources](docs/data-sources.md)                      | Every feed, snapshot, cache and refresh rule |
| [Troubleshooting](docs/troubleshooting.md)                | Errors, causes, fixes                        |
| [Multi-cloud parity](docs/reviews/multicloud-parity.md)   | What GCP and Azure cannot do yet, and why    |

## AWS credentials

Credentials are optional. Without them, spotinfo answers from the AWS feeds and the embedded
snapshot. With them, two more features work:

| Feature                                              | Permission                     |
| ---------------------------------------------------- | ------------------------------ |
| Live price for machines the static feed prices at $0 | `ec2:DescribeSpotPriceHistory` |
| Placement scores (`--with-score`)                    | `ec2:GetSpotPlacementScores`   |

Credentials load through the standard AWS SDK chain. The chain is probed once per run, so a
machine without credentials skips both calls instead of waiting for each to time out.

GCP credentials are needed only for `--live-risk`. See [Cloud coverage](docs/clouds.md#gcp).

## Development

Go 1.26+, make, golangci-lint.

```bash
make build            # hermetic: embeds the committed data, downloads nothing
make test             # unit and end-to-end tests, no credentials, no network
make lint
make verify-data      # manifests, source contracts, parser contracts, coverage floors
```

Contributions are welcome. Read [CLAUDE.md](CLAUDE.md) for the repository rules. Make sure
that every test passes before you open a pull request.

## License

Apache 2.0. See [LICENSE](LICENSE).
