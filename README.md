[![CI](https://github.com/alexei-led/spotinfo/actions/workflows/ci.yaml/badge.svg)](https://github.com/alexei-led/spotinfo/actions/workflows/ci.yaml) [![Docker](https://github.com/alexei-led/spotinfo/actions/workflows/docker.yaml/badge.svg)](https://github.com/alexei-led/spotinfo/actions/workflows/docker.yaml) [![Go Report Card](https://goreportcard.com/badge/github.com/alexei-led/spotinfo)](https://goreportcard.com/report/github.com/alexei-led/spotinfo) 

# spotinfo

Using [Amazon EC2 Spot](https://aws.amazon.com/ec2/spot/) instances is an excellent way to reduce EC2 on-demand instance cost, up to 90%. Whenever you have a workload that can survive VM interruption or be suspended and resumed later on without impacting business use cases, choosing the Spot pricing model is a no-brainer choice.

You should weigh your application’s tolerance for interruption and your cost saving goals when selecting a Spot instance. The lower your interruption rate, the longer your Spot instances are likely to run.

Amazon provides an excellent web interface [AWS Spot Instance Advisor](https://aws.amazon.com/ec2/spot/instance-advisor/) to explore available Spot instances and determine spot instance pools with the least chance of interruption. You can also check the savings you get over on-demand rates. You can also check the savings you get over on-demand rates. And then, you are supposed to use these metrics for selecting appropriate Spot instances.

While the **AWS Spot Instance Advisor** is a valuable tool, it is not easy to use its data for scripting and automation, and some use cases require too many clicks.

That's why I created the `spotinfo` tool. It's an easy-to-use command-line tool (open source under Apache 2.0 License) that allows you to explore AWS Spot instances in a terminal and use the spot data it provides for scripting and automation.

Under the hood, the `spotinfo` is using two public data sources available from AWS:

1. **AWS Spot Instance Advisor** [data feed](https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json)
1. AWS Spot Pricing [data feed](http://spot-price.s3.amazonaws.com/spot.js)

## Features

The `spotinfo` allows you to access the same information you can see in the **AWS Spot Instance Advisor**, but from a command line and can be used for scripting and automation use cases. In addition, the tool provides some useful features that are not available for **AWS Spot Instance Advisor** web interface.

### Advanced Filtering

The first feature is _advanced filtering_. You can filter spot instances by:

- vCPU - minimum number of CPU cores
- Memory GiB - minimum memory size
- Operating system - Linux or Windows
- Region - one or more AWS regions (or `all` AWS regions)
- Savings (compared to on-demand)
- Frequency of interruption
- Hourly rate (in `USD/hour`)

When filtering by instance type, [regular expressions](https://github.com/google/re2/wiki/Syntax) are supported. And this can help you create advanced queries.

### Spot Price Visibility

With **AWS Spot Instance Advisor**, you can see a discount comparing to the on-demand EC2 instance rate. But to find out, what is the actual price, you are going to pay, you must visit a different [AWS Spot pricing](https://aws.amazon.com/ec2/spot/pricing/) web page and search it again for the specific instance type.

The `spotinfo` saves your time and can display the spot price alongside other information. You can also filter and sort by spot price if you like.

### Flexible Output Formats

Working with data in a command line and accessing data from scripts and automation requires flexibility of output format. The `spotinfo` can return results in multiple formats: human-friendly formats, like `table` and plain `text`, and automation-friendly: `json`, `csv`, or just a saving number. Choose whatever format you need for any concrete use case.

### Compare Spots across multiple AWS Regions

One annoying thing about the **AWS Spot Instance Advisor**, is the inability to compare EC2 spot instances across multiple AWS regions. Only a single region view is available, or you need to open multiple browser tabs and constantly switch between them to compare spot instances across multiple AWS regions.

The `spotinfo` can help you to compare spot instances across multiple AWS regions. All you need to do is pass a `--region` command-line flag, and you can use this flag more than once.

Another option is to pass a special `all` value (with `--region=all` flag) to see spot instances across all available AWS regions.

### Network Resilience

While the `spotinfo` uses public AWS data feeds, it also embeds the same data within the tool. So, if data feed is not available, for any reason (no connectivity, service not available or other), the `spotinfo` still will be able to return the same result.

Data snapshot from both AWS data feeds is [embedded](https://golang.org/pkg/embed) into the `spotinfo` binary during the build.

## Install

### macOS with Homebrew

```sh
brew tap alexei-led/spotinfo
brew install spotinfo
```

### Download platform binary

Download OS/platform specific binary from the [Releases](https://github.com/alexei-led/spotinfo/releases) page and add it to the `PATH`.

- Supported OS: macOS, Windows, Linux
- Supported Platforms: Intel (`amd64`) and ARM (`arm64`)

## Usage

With `spotinfo` command you can get a filtered and sorted list of Spot instance types as a plain text, `JSON`, pretty table or `CSV` format.

```shell
spotinfo --help
NAME:
   spotinfo - spotinfo CLI

USAGE:
   spotinfo [global options] command [command options] [arguments...]

VERSION:
   1.0.0

COMMANDS:
   help, h  Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --type value    EC2 instance type (can be RE2 regexp patten)
   --os value      instance operating system (windows/linux) (default: "linux")
   --region value  set one or more AWS regions, use "all" for all AWS regions (default: "us-east-1")
   --output value  format output: number|text|json|table|csv (default: "table")
   --cpu value     filter: minimal vCPU cores (default: 0)
   --memory value  filter: minimal memory GiB (default: 0)
   --price value   filter: maximum price per hour (default: 0)
   --sort value    sort results by interruption|type|savings|price|region (default: "interruption")
   --order value   sort order asc|desc (default: "asc")
   --help, -h      show help (default: false)
   --version, -v   print the version (default: false)
```

## Model Context Protocol (MCP) Server

The `spotinfo` tool also functions as a **Model Context Protocol (MCP) server**, enabling AI assistants like Claude to directly query AWS EC2 Spot Instance data. This provides a seamless way for AI agents to access real-time spot pricing and interruption data for infrastructure recommendations.

### What is MCP?

The [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) is an open standard that allows AI assistants to securely connect to external data sources and tools. By running `spotinfo` in MCP mode, you can:

- **Ask Claude for spot recommendations**: "Find me the cheapest t3 instances with <10% interruption rate"
- **Get real-time pricing**: "What's the current m5.large spot price in us-east-1?"  
- **Compare across regions**: "Show me r5.xlarge prices across all US regions"
- **Infrastructure planning**: Use AI to analyze and recommend optimal spot instance configurations

### Quick Start with Claude Desktop

1. **Install spotinfo** (if not already installed):
   ```bash
   # macOS with Homebrew
   brew tap alexei-led/spotinfo
   brew install spotinfo
   
   # Or download from releases page
   curl -L https://github.com/alexei-led/spotinfo/releases/latest/download/spotinfo_linux_amd64.tar.gz | tar xz
   ```

2. **Add to Claude Desktop configuration**:
   
   Open Claude Desktop settings and add to your `claude_desktop_config.json`:
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

3. **Restart Claude Desktop** and start asking about AWS Spot Instances!

### Available MCP Tools

#### `find_spot_instances`
Search for AWS EC2 Spot Instance options based on requirements.

**Parameters:**
- `regions` (optional): AWS regions to search (e.g., `["us-east-1", "eu-west-1"]`). Use `["all"]` for all regions
- `instance_types` (optional): Instance type pattern (e.g., `"m5.large"`, `"t3.*"`)  
- `min_vcpu` (optional): Minimum vCPUs required
- `min_memory_gb` (optional): Minimum memory in gigabytes
- `max_price_per_hour` (optional): Maximum spot price per hour in USD
- `max_interruption_rate` (optional): Maximum interruption rate percentage (0-100)
- `sort_by` (optional): Sort by `"price"`, `"reliability"`, or `"savings"` (default: `"reliability"`)
- `limit` (optional): Maximum results to return (default: 10, max: 50)

**Response:** Array of spot instances with pricing, savings, interruption data, and specs.

#### `list_spot_regions`
List all AWS regions where EC2 Spot Instances are available.

**Parameters:**
- `include_names` (optional): Include human-readable region names (default: true)

**Response:** Array of available region codes and total count.

### Configuration Options

#### Environment Variables
- `SPOTINFO_MODE=mcp` - Enable MCP server mode
- `MCP_TRANSPORT=stdio` - Transport method (currently only stdio supported)
- `MCP_PORT=8080` - Port for future SSE transport

#### Command Line Flags
```bash
# Start MCP server with stdio transport (for Claude Desktop)
spotinfo --mcp

# Or using environment variable
SPOTINFO_MODE=mcp spotinfo
```

### Example Usage

Once configured with Claude Desktop, you can ask natural language questions:

**Example 1: Finding cost-effective instances**
```
Human: Find me the 5 cheapest t3 instances globally with less than 10% interruption rate

Claude: I'll search for t3 instances with low interruption rates and sort by price.

[Claude calls find_spot_instances with parameters:
{
  "instance_types": "t3.*",
  "max_interruption_rate": 10,
  "sort_by": "price", 
  "limit": 5
}]

Results: Found 5 t3 instances under $0.05/hour with <10% interruption rates:
- t3.nano in ap-south-1: $0.0017/hour (5-10% interruption)
- t3.micro in ap-south-1: $0.0033/hour (<5% interruption)
- ...
```

**Example 2: Regional comparison**
```
Human: Compare m5.large spot prices across US East regions

Claude: I'll check m5.large pricing in US East regions for you.

[Claude calls find_spot_instances with:
{
  "regions": ["us-east-1", "us-east-2"],
  "instance_types": "m5.large"
}]

Results: m5.large spot prices in US East:
- us-east-1: $0.0928/hour (70% savings, <5% interruption)
- us-east-2: $0.1024/hour (68% savings, <5% interruption)
```

**Example 3: Infrastructure planning**
```
Human: I need instances with at least 16 vCPUs and 64GB RAM for machine learning workloads. What are my most reliable options under $1/hour?

Claude: I'll find high-spec instances optimized for reliability within your budget.

[Claude calls find_spot_instances with:
{
  "min_vcpu": 16,
  "min_memory_gb": 64,
  "max_price_per_hour": 1.0,
  "sort_by": "reliability",
  "limit": 10
}]

Results: Found 8 instances meeting your criteria, with r5.4xlarge and m5.4xlarge offering the best reliability...
```

### Advanced Configuration

#### Claude Desktop Configuration (macOS)
Configuration file location: `~/Library/Application Support/Claude/claude_desktop_config.json`

```json
{
  "mcpServers": {
    "spotinfo": {
      "command": "/opt/homebrew/bin/spotinfo",
      "args": ["--mcp"],
      "env": {
        "AWS_REGION": "us-east-1"
      }
    }
  }
}
```

#### Claude Desktop Configuration (Windows)
Configuration file location: `%APPDATA%\Claude\claude_desktop_config.json`

```json
{
  "mcpServers": {
    "spotinfo": {
      "command": "C:\\Program Files\\spotinfo\\spotinfo.exe", 
      "args": ["--mcp"]
    }
  }
}
```

### Troubleshooting

#### Common Issues

**Claude can't find spotinfo tools:**
- Verify `spotinfo --mcp` runs without errors
- Check the binary path in your configuration
- Restart Claude Desktop after configuration changes

**Permission denied errors:**
- Ensure the spotinfo binary is executable: `chmod +x /path/to/spotinfo`
- Check file paths in configuration are correct

**No data returned:**
- The tool uses embedded data and works offline
- Check if specific regions/instance types exist with CLI: `spotinfo --type=m5.large --region=us-east-1`

#### Debug Mode
```bash
# Test MCP server manually
spotinfo --mcp
# Should start server and wait for input

# Test with MCP Inspector
npx @modelcontextprotocol/inspector spotinfo --mcp
```

### Benefits of MCP Integration

1. **Natural Language Interface**: Ask questions about spot instances in plain English
2. **Intelligent Recommendations**: Claude can analyze your requirements and suggest optimal configurations  
3. **Real-time Data**: Access current spot pricing and interruption data
4. **Cross-region Analysis**: Easily compare options across multiple AWS regions
5. **Automated Decision Making**: Use Claude's reasoning to optimize cost vs. reliability trade-offs

The MCP integration transforms `spotinfo` from a CLI tool into an intelligent infrastructure advisor, making AWS Spot Instance selection more accessible and efficient.

## Data Sources

The `spotinfo` uses the following data sources to get updated information about AWS EC2 Spot instances:

1. AWS Spot Advisor [JSON file](https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json), maintained/updated by AWS team
2. AWS Spot Pricing [`callback` JS file](http://spot-price.s3.amazonaws.com/spot.js), maintained/updated by AWS team

The `spotinfo` also includes **embedded** (during the build) copies of the above files, and thus can continue to work, even if there is no network connectivity, or these files are not available, for any reason.

### Examples

#### Use Case 1

Get all Graviton2 Linux Spot instances in the AWS Oregon (`us-west-2`) region, with CPU cores > 8 and memory > 64gb, sorted by type, and output the result in a table format.

```shell
# run binary
spotinfo --type="^.(6g)(\S)*" --cpu=8 --memory=64 --region=us-west-2 --os=linux --output=table --sort=type

# OR run Docker image
docker run -it --rm ghcr.io/alexei-led/spotinfo --type="^.(6g)(\S)*" --cpu=8 --memory=64 --region=us-west-2 --os=linux --output=table --sort=type
```

##### Output

```text
┌───────────────┬──────┬────────────┬────────────────────────┬───────────────────────────┬──────────┐
│ INSTANCE INFO │ VCPU │ MEMORY GIB │ SAVINGS OVER ON-DEMAND │ FREQUENCY OF INTERRUPTION │ USD/HOUR │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ c6g.12xlarge  │   48 │         96 │                    50% │ <5%                       │   0.8113 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ c6g.16xlarge  │   64 │        128 │                    50% │ <5%                       │   1.0818 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ c6g.8xlarge   │   32 │         64 │                    50% │ <5%                       │   0.5409 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ m6g.12xlarge  │   48 │        192 │                    54% │ <5%                       │   0.8519 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ m6g.16xlarge  │   64 │        256 │                    54% │ <5%                       │   1.1358 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ m6g.4xlarge   │   16 │         64 │                    54% │ <5%                       │    0.284 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ m6g.8xlarge   │   32 │        128 │                    54% │ <5%                       │   0.5679 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ m6gd.8xlarge  │   32 │        128 │                    61% │ <5%                       │   0.5679 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ r6g.12xlarge  │   48 │        384 │                    63% │ <5%                       │   0.8924 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ r6g.16xlarge  │   64 │        512 │                    63% │ <5%                       │   1.1899 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ r6g.2xlarge   │    8 │         64 │                    63% │ <5%                       │   0.1487 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ r6g.4xlarge   │   16 │        128 │                    63% │ <5%                       │   0.2975 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ r6g.8xlarge   │   32 │        256 │                    63% │ <5%                       │    0.595 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ r6g.metal     │   64 │        512 │                    63% │ <5%                       │   1.1899 │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┼──────────┤
│ r6gd.4xlarge  │   16 │        128 │                    68% │ 15-20%                    │   0.2975 │
└───────────────┴──────┴────────────┴────────────────────────┴───────────────────────────┴──────────┘
```

#### Use Case 2

Compare `m5a.xlarge` Linux Spot instances across 3 AWS regions, sorted by price. Output the result in a `JSON` format.

```shell
spotinfo --type="m5a.xlarge" --output=json --sort=price --order=asc --region=us-west-1 --region=us-east-1 --region=ap-south-1
```

##### Output

```json
[
  {
    "Region": "ap-south-1",
    "Instance": "m5a.xlarge",
    "Range": {
      "label": "<5%",
      "min": 0,
      "max": 5
    },
    "Savings": 50,
    "Info": {
      "cores": 4,
      "emr": true,
      "ram_gb": 16
    },
    "Price": 0.0554
  },
  {
    "Region": "us-west-1",
    "Instance": "m5a.xlarge",
    "Range": {
      "label": "<5%",
      "min": 0,
      "max": 5
    },
    "Savings": 65,
    "Info": {
      "cores": 4,
      "emr": true,
      "ram_gb": 16
    },
    "Price": 0.0715
  },
  {
    "Region": "us-east-1",
    "Instance": "m5a.xlarge",
    "Range": {
      "label": "<5%",
      "min": 0,
      "max": 5
    },
    "Savings": 56,
    "Info": {
      "cores": 4,
      "emr": true,
      "ram_gb": 16
    },
    "Price": 0.0759
  }
]
```

## Docker Image

The `spotinfo` uses Docker both as a CI tool and for releasing the final `spotinfo` Multi-Architecture Docker image (`scratch` with updated `ca-credentials` package).

Public Docker Image [ghcr.io/alexei-led/spotinfo](https://github.com/users/alexei-led/packages/container/package/spotinfo)

```shell
docker pull ghcr.io/alexei-led/spotinfo:latest
```

## Build Instructions

### Makefile

The `spotinfo` `Makefile` is used for task automation only: compile, lint, test, etc.
The project requires Go version 1.24+.

```text
> make help
all              Build program binary
check_deps       Verify the system has all dependencies installed
test-bench       Run benchmarks
test-short       Run only short tests
test-verbose     Run tests in verbose mode with coverage reporting
test-race        Run tests with race detector
check test tests Run tests
test-xml         Run tests with xUnit output
test-coverage    Run coverage tests
lint             Run golangci-lint
mockgen          Run mockery to re/generate mocks for all interfaces
fmt              Run gofmt on all source files
clean            Cleanup everything
```

### Continuous Integration

The project uses modern GitHub Actions workflows:

- **CI Pipeline** (`ci.yaml`): Runs tests, linting, and cross-platform builds on every push/PR
- **Release Pipeline** (`release.yaml`): Creates GitHub releases with binaries when tags are pushed  
- **Docker Pipeline** (`docker.yaml`): Builds and publishes multi-architecture Docker images to GitHub Container Registry
- **Auto-Release** (`auto-release.yaml`): Automated quarterly releases with smart change detection

### Build with Docker

Use Docker `buildx` plugin to build multi-architecture Docker image.

```sh
docker buildx build --platform=linux/arm64,linux/amd64 -t spotinfo -f Dockerfile .
```

#### GitHub Actions Configuration

The CI/CD pipelines use:

- **Built-in Secrets**: `GITHUB_TOKEN` for releases and GitHub Container Registry publishing
- **Go 1.24**: Modern Go version with latest features and security updates
- **Multi-Architecture Support**: Builds for Linux/macOS/Windows on AMD64/ARM64
- **Smart Caching**: Go modules and Docker layer caching for faster builds
- **Automated Releases**: Quarterly releases with semantic versioning

No additional secrets required - everything uses GitHub's built-in authentication.

