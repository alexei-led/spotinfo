[![CI](https://github.com/alexei-led/spotinfo/actions/workflows/ci.yaml/badge.svg)](https://github.com/alexei-led/spotinfo/actions/workflows/ci.yaml) [![Docker](https://github.com/alexei-led/spotinfo/actions/workflows/docker.yaml/badge.svg)](https://github.com/alexei-led/spotinfo/actions/workflows/docker.yaml) [![Go Report Card](https://goreportcard.com/badge/github.com/alexei-led/spotinfo)](https://goreportcard.com/report/github.com/alexei-led/spotinfo) [![MCP Compatible](https://img.shields.io/badge/MCP-Compatible-blue)](docs/mcp-server.md)

# spotinfo

**Command-line tool for AWS EC2 Spot Instance exploration with placement score analysis**

`spotinfo` is a powerful CLI tool and [Model Context Protocol (MCP) server](#mcp-integration) that provides comprehensive AWS EC2 Spot Instance information, including real-time placement scores, pricing data, and interruption rates. Perfect for DevOps engineers optimizing cloud infrastructure costs.

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

### 📊 **Multiple Output Formats**
- **Visual formats**: Table with emoji indicators, plain text
- **Data formats**: JSON, CSV for automation and scripting
- **Clean separation**: Visual indicators only in human-readable formats

### 🌐 **Network Resilience**
- **Embedded data** for offline functionality
- **Graceful fallbacks** when AWS APIs are unavailable
- **Real-time API integration** with intelligent caching

## Quick Start

### Installation

```bash
# macOS with Homebrew
brew tap alexei-led/spotinfo
brew install spotinfo

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

`spotinfo` functions as a **[Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server**, enabling AI assistants to directly query AWS Spot Instance data through natural language.

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

**Ask Claude**: *"Find cheapest t3 instances with placement score >7"* or *"Compare m5.large prices across US regions"*

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
| **[Data Sources](docs/data-sources.md)** | AWS data feeds, caching strategy, and troubleshooting |

## Development

**Requirements**: Go 1.24+, make, golangci-lint

```bash
# Build and test
make all

# Update embedded data
make update-data update-price

# Docker build
docker buildx build --platform=linux/arm64,linux/amd64 -t spotinfo .
```

**CI/CD**: Automated testing, linting, releases, and multi-arch Docker builds via GitHub Actions

## Contributing

Contributions welcome! Please read the development commands in [CLAUDE.md](CLAUDE.md) and ensure all tests pass.

## License

Apache 2.0 License - see [LICENSE](LICENSE) for details.

