[![build](https://github.com/doitintl/spotinfo/workflows/docker/badge.svg)](https://github.com/doitintl/spotinfo/actions?query=workflow%3A"docker") [![Go Report Card](https://goreportcard.com/badge/github.com/alexei-led/spotinfo)](https://goreportcard.com/report/github.com/alexei-led/spotinfo) [![Docker Pulls](https://img.shields.io/docker/pulls/alexeiled/spotinfo.svg?style=popout)](https://hub.docker.com/r/alexeiled/spotinfo) [![](https://images.microbadger.com/badges/image/alexeiled/spotinfo.svg)](https://microbadger.com/images/alexeiled/spotinfo "Get your own image badge on microbadger.com") 

# spotinfo

The `spotinfo` is a command-line tool that helps you determine AWS Spot instance types with the least chance of interruption and provides the savings you get over on-demand rates. 

You should weigh your application’s tolerance for interruption and your cost saving goals when selecting a Spot instance. The lower your interruption rate, the longer your Spot instances are likely to run.

## Usage

With `spotinfo` command you can get a filtered and sorted list of Spot instance types as a plain text, json, pretty table or CSV format.

```shell
spotinfo --help
NAME:
   spotinfo - spotinfo CLI

USAGE:
   spotinfo [global options] command [command options] [arguments...]

VERSION:
   0.2.0

COMMANDS:
   help, h  Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --type value    EC2 instance type (can be RE2 regexp patten)
   --os value      instance operating system (windows/linux) (default: "linux")
   --region value  AWS region (default: "us-east-1")
   --output value  format output: number|text|json|table|csv (default: "table")
   --cpu value     filter: minimal vCPU cores (default: 0)
   --memory value  filter: minimal memory GiB (default: 0)
   --sort value    sort results by interruption|type|savings (default: "interruption")
   --help, -h      show help (default: false)
   --version, -v   print the version (default: false)
```

### Example

Get all Graviton2 Linux Spot instances in the AWS Oregon (`us-west-2`) region, with CPU cores > 8 and memory > 64gb, sorted by type, and output the result in a table format.

```shell
spotinfo --type="^.(6g)(\S)*" --cpu=8 --memory=64 --region=us-west-2 --os=linux --output=table --sort=type
```

```text
┌───────────────┬──────┬────────────┬────────────────────────┬───────────────────────────┐
│ INSTANCE INFO │ VCPU │ MEMORY GIB │ SAVINGS OVER ON-DEMAND │ FREQUENCY OF INTERRUPTION │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ c6g.12xlarge  │   48 │         96 │                    50% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ c6g.16xlarge  │   64 │        128 │                    50% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ c6g.8xlarge   │   32 │         64 │                    50% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ m6g.12xlarge  │   48 │        192 │                    54% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ m6g.16xlarge  │   64 │        256 │                    54% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ m6g.4xlarge   │   16 │         64 │                    54% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ m6g.8xlarge   │   32 │        128 │                    54% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ m6gd.8xlarge  │   32 │        128 │                    61% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ r6g.12xlarge  │   48 │        384 │                    63% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ r6g.16xlarge  │   64 │        512 │                    63% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ r6g.2xlarge   │    8 │         64 │                    63% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ r6g.4xlarge   │   16 │        128 │                    63% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ r6g.8xlarge   │   32 │        256 │                    63% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ r6g.metal     │   64 │        512 │                    63% │ <5%                       │
├───────────────┼──────┼────────────┼────────────────────────┼───────────────────────────┤
│ r6gd.4xlarge  │   16 │        128 │                    68% │ >20%                      │
└───────────────┴──────┴────────────┴────────────────────────┴───────────────────────────┘
```

## Docker Image

The `spotinfo` uses Docker both as a CI tool and for releasing the final `spotinfo` Multi-Architecture Docker image (`scratch` with updated `ca-credentials` package).

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
