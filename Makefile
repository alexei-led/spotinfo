# Makefile for spotinfo

# Build variables
MODULE   = $(shell go list -m)
VERSION ?= $(shell git describe --tags --always --dirty --match="v*" 2> /dev/null || echo v0)
DATE    ?= $(shell date +%FT%T%z)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BRANCH  ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)

# Build flags
LDFLAGS = -X main.Version=$(VERSION) -X main.BuildDate=$(DATE) -X main.GitCommit=$(COMMIT) -X main.GitBranch=$(BRANCH) -X main.GitHubRelease=$(GITHUB_RELEASE)

# Directories
BIN_DIR = .bin

# Release platforms
PLATFORMS = darwin linux windows
ARCHITECTURES = amd64 arm64

# Data URLs
SPOT_ADVISOR_URL = "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json"
# Feed behind https://aws.amazon.com/ec2/spot/pricing/. Replaces the legacy JSONP
# spot-price.s3.amazonaws.com/spot.js, frozen since 2024-05-13 and missing every
# instance family newer than that. Undocumented endpoint: runtime falls back to
# embedded data on any failure (see internal/spot/data.go).
SPOT_PRICE_URL = "https://website.spot.ec2.aws.a2z.com/spot.json"
# must match the //go:embed paths in internal/spot/data.go
DATA_DIR = internal/spot/data
# Pinned so every developer and CI regenerate byte-identical mocks.
MOCKERY_VERSION = v3.7.2

# Go environment
export GO111MODULE=on
export CGO_ENABLED=0

.PHONY: all build test test-verbose test-race test-coverage lint fmt clean help version
.PHONY: update-data update-price verify-data check-deps setup-tools release mocks

# Default target
all: build

# Build binary for current platform.
# Deliberately does NOT depend on update-data/update-price: the build must be
# hermetic and embed exactly the committed data. Refreshing the feeds is a
# separate, explicit step (make update-data update-price, or the scheduled
# update-data workflow that opens a PR).
build:
	@echo "Building binary..."
	@go build -tags release -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(shell basename $(MODULE)) ./cmd/spotinfo

# Test targets (no formatting requirement)
test:
	@echo "Running tests..."
	@go test ./...

test-verbose:
	@echo "Running tests with verbose output..."
	@go test -v ./...

test-race:
	@echo "Running tests with race detector..."
	@go test -race ./...

test-coverage:
	@echo "Running tests with coverage..."
	@go test -covermode=atomic -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@go tool cover -func=coverage.out

# Code quality
lint: setup-tools
	@echo "Running linter..."
	@golangci-lint run -v -c .golangci.yaml ./...

fmt:
	@echo "Formatting code..."
	@go fmt ./...

# Data updates
check-deps:
	@command -v wget > /dev/null 2>&1 || (echo "Error: wget is required" && exit 1)
	@echo "Dependencies satisfied"

# Both targets download to .tmp and only replace the tracked file on success.
# wget -O truncates its target before it knows the HTTP status, so writing
# straight to the embedded file would clobber good data on a 403 or a dropped
# connection. wget also exits non-zero on an incomplete transfer, which is the
# guard against a truncated-but-non-empty download that `test -s` would pass.
update-data: check-deps
	@echo "Updating spot advisor data..."
	@wget -nv $(SPOT_ADVISOR_URL) -O $(DATA_DIR)/spot-advisor-data.json.tmp || (rm -f $(DATA_DIR)/spot-advisor-data.json.tmp; exit 1)
	@test -s $(DATA_DIR)/spot-advisor-data.json.tmp || (rm -f $(DATA_DIR)/spot-advisor-data.json.tmp; echo "Error: empty advisor download"; exit 1)
	@mv $(DATA_DIR)/spot-advisor-data.json.tmp $(DATA_DIR)/spot-advisor-data.json

update-price: check-deps
	@echo "Updating spot pricing data..."
	@wget -nv $(SPOT_PRICE_URL) -O $(DATA_DIR)/spot-price-data.json.tmp || (rm -f $(DATA_DIR)/spot-price-data.json.tmp; exit 1)
	@test -s $(DATA_DIR)/spot-price-data.json.tmp || (rm -f $(DATA_DIR)/spot-price-data.json.tmp; echo "Error: empty price download"; exit 1)
	@mv $(DATA_DIR)/spot-price-data.json.tmp $(DATA_DIR)/spot-price-data.json

# Parse gate: proves the embedded files are valid JSON in the expected shape.
# Deliberately only the LoadEmbedded tests — they read the //go:embed strings directly,
# so the result is deterministic and does not depend on AWS being reachable.
verify-data:
	@echo "Verifying embedded data parses..."
	@go test ./internal/spot/ -run 'TestLoadEmbeddedAdvisorData|TestLoadEmbeddedPricingData' -count=1

# Development tools
setup-tools:
	@echo "Installing development tools..."
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	@go install github.com/vektra/mockery/v3@$(MOCKERY_VERSION)

# Regenerate the testify mocks declared in .mockery.yaml.
# Every mocked interface must be listed there — a mock that exists only by hand
# inside the generated file is silently deleted by the next run.
mocks:
	@echo "Regenerating mocks..."
	@command -v mockery > /dev/null 2>&1 || go install github.com/vektra/mockery/v3@$(MOCKERY_VERSION)
	@mockery
	@go build ./... && echo "mocks regenerated and compiling"

# Multi-platform release
release: clean
	@echo "Building release binaries..."
	@for os in $(PLATFORMS); do \
		for arch in $(ARCHITECTURES); do \
			if [ "$$arch" = "arm64" ] && [ "$$os" = "windows" ]; then continue; fi; \
			echo "Building $$os/$$arch..."; \
			GOOS=$$os GOARCH=$$arch go build \
				-tags release \
				-ldflags "$(LDFLAGS)" \
				-o $(BIN_DIR)/$(shell basename $(MODULE))_$${os}_$${arch} \
				./cmd/spotinfo; \
		done; \
	done

# Cleanup
clean:
	@echo "Cleaning up..."
	@rm -rf $(BIN_DIR)
	@rm -f coverage.out coverage.html

# Utility targets
version:
	@echo $(VERSION)

help:
	@echo "Available targets:"
	@echo "  build         Build binary for current platform"
	@echo "  test          Run tests"
	@echo "  test-verbose  Run tests with verbose output"
	@echo "  test-race     Run tests with race detector"
	@echo "  test-coverage Run tests with coverage report"
	@echo "  lint          Run golangci-lint"
	@echo "  fmt           Format Go code"
	@echo "  update-data   Update embedded spot advisor data"
	@echo "  update-price  Update embedded spot pricing data"
	@echo "  verify-data   Verify the embedded data files parse"
	@echo "  mocks         Regenerate testify mocks from .mockery.yaml"
	@echo "  release       Build binaries for all platforms"
	@echo "  clean         Remove build artifacts"
	@echo "  setup-tools   Install development tools"
	@echo "  version       Show version"
	@echo "  help          Show this help"