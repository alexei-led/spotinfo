# Usage Guide

## Command Overview

`spotinfo` is a command-line tool for exploring AWS EC2 Spot instances with advanced filtering, sorting, and placement score analysis capabilities.

## Basic Syntax

```bash
spotinfo [global options]
```

## Global Options

### Instance Selection
| Flag | Description | Example |
|------|-------------|---------|
| `--type value` | EC2 instance type (supports RE2 regex) | `--type "m5.large"` or `--type "t3.*"` |
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

### CSV Format
Data-only format without visual indicators:
```bash
spotinfo --type "t3.micro" --with-score --output csv
```
```
Instance Info,vCPU,Memory GiB,Savings over On-Demand,Frequency of interruption,USD/Hour,Placement Score (Regional)
t3.micro,2,1,68,<5%,0.0043,9
```

### Text Format
Plain text for scripting:
```bash
spotinfo --type "t3.micro" --with-score --output text
```
```
type=t3.micro, vCPU=2, memory=1GiB, saving=68%, interruption='<5%', price=0.00, score=🟢 9
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