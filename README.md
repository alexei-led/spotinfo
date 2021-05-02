[![docker](https://github.com/alexei-led/spotinfo/actions/workflows/build.yaml/badge.svg)](https://github.com/alexei-led/spotinfo/actions/workflows/build.yaml) [![Go Report Card](https://goreportcard.com/badge/github.com/alexei-led/spotinfo)](https://goreportcard.com/report/github.com/alexei-led/spotinfo) [![Docker Pulls](https://img.shields.io/docker/pulls/alexeiled/spotinfo.svg?style=popout)](https://hub.docker.com/r/alexeiled/spotinfo) [![](https://images.microbadger.com/badges/image/alexeiled/spotinfo.svg)](https://microbadger.com/images/alexeiled/spotinfo "Get your own image badge on microbadger.com") 

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

The first feature is _advanced filtering+. You can filter spot instances by:

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

Data snapshoot from the both AWS data feeds is [embedded](https://golang.org/pkg/embed) into the `spotinfo` binary during the build.

## Install

### OS X with Homebrew

```sh
brew tap alexei-led/spotinfo
brew install spotinfo
```

### Download platform binary

Download OS/platform specific binary from the [Releases](https://github.com/alexei-led/spotinfo/releases) page and add it to the `PATH`.

- Supported OS: OS X, Windows, Linux
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

#### Output

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

#### Output

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
The project requires Go version 1.16+.

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

The GitHub action `docker` is used for the `spotinfo` CI.

### Build with Docker

Use Docker `buildx` plugin to build multi-architecture Docker image.

```sh
docker buildx build --platform=linux/arm64,linux/amd64 -t spotinfo -f Dockerfile .
```

#### Required GitHub secrets

Please specify the following GitHub secrets:

1. `DOCKER_USERNAME` - Docker Registry username
1. `DOCKER_PASSWORD` - Docker Registry password or token
1. `CR_PAT` - Current GitHub Personal Access Token (with `write/read` packages permission)
1. `DOCKER_REGISTRY` - _optional_; Docker Registry name, default to `docker.io`
1. `DOCKER_REPOSITORY` - _optional_; Docker image repository name, default to `$GITHUB_REPOSITORY` (i.e. `user/repo`)

Additional secret to create GitHub Release:

1. `RELEASE_TOKEN` - GitHub Personal Access Token (with `repo` scope)

