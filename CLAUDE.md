# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`spotinfo` is a Go CLI tool that provides command-line access to AWS EC2 Spot Instance pricing and interruption data. It uses embedded AWS data feeds as fallback when network connectivity is unavailable.

## Development Commands

### Building
- `make build` - Build binary for current OS/arch
- `make all` - Build with full pipeline (update data, format, lint, test, build)
- `make release` - Build binaries for multiple platforms

### Testing
- `make test` - Run tests with formatting
- `make test-verbose` - Run tests with verbose output and coverage
- `make test-race` - Run tests with race detector
- `make test-coverage` - Run tests with coverage reporting

### Code Quality
- `make lint` - Run golangci-lint with config from `.golangci.yaml`
- `make fmt` - Run gofmt on all source files

### Data Updates
- `make update-data` - Update embedded Spot Advisor data from AWS
- `make update-price` - Update embedded spot pricing data from AWS

### Dependencies
- `make check-deps` - Verify system has required dependencies (wget)
- `make setup-tools` - Install all development tools

## Architecture

### Core Components
- `cmd/main.go` - CLI entry point using urfave/cli/v2
- `public/spot/` - Core business logic package
  - `info.go` - Spot advisor data processing and filtering
  - `price.go` - Spot pricing data processing
  - `data/` - Embedded JSON data files from AWS feeds

### Data Sources
The tool uses two AWS public data feeds:
1. Spot Instance Advisor data: `https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json`
2. Spot pricing data: `http://spot-price.s3.amazonaws.com/spot.js`

Both are embedded in the binary during build for offline capability.

### Key Libraries
- `github.com/urfave/cli/v2` - CLI framework
- `github.com/jedib0t/go-pretty/v6` - Table formatting
- `github.com/pkg/errors` - Error handling
- `github.com/stretchr/testify` - Testing framework with assertions

## Build Requirements
- Go 1.24+
- wget (for data updates)
- golangci-lint (installed via make setup-tools)

## Output Formats
The CLI supports multiple output formats: number, text, json, table, csv

## CI/CD Pipeline

### GitHub Actions Workflows
- **ci.yaml**: Modern CI with Go 1.24, tests, linting, matrix builds for all platforms
- **release.yaml**: Tag-triggered releases with binary uploads using standard Go toolchain
- **docker.yaml**: Multi-arch Docker images published to GitHub Container Registry (ghcr.io)
- **auto-release.yaml**: Quarterly automated releases with smart change detection and semantic versioning

### Docker
- **Build**: `docker build -t spotinfo .` (uses Go 1.24 and `make build`)
- **Multi-arch**: Supports linux/amd64 and linux/arm64 platforms
- **Registry**: Published to `ghcr.io/alexei-led/spotinfo`
- **Base**: Uses scratch image with ca-certificates for minimal attack surface

### Release Process
1. **Manual Release**: Create and push a tag starting with 'v' (e.g., `git tag v1.2.3 && git push origin v1.2.3`)
2. **Automated Release**: Runs quarterly (Jan/Apr/Jul/Oct) with automatic version bumping
3. **Artifacts**: Cross-platform binaries for Linux/macOS/Windows on AMD64/ARM64

## Testing
- **Framework**: Uses testify for assertions and test structure
- **Parallel Execution**: Tests run in parallel for better performance
- **Resilient Patterns**: Tests are resilient to data changes from external feeds
- **Coverage**: Comprehensive test coverage with `make test-coverage`

## Development Guidance
- Use `make` commands for all development tasks
- Run `make test-verbose` before committing changes
- Update embedded data with `make update-data update-price` when needed
- Follow Go 1.24 best practices and modern testing patterns
- NEVER add Claude as co-author to git commit message