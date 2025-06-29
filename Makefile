# Makefile for spotinfo

# Build variables
MODULE   = $(shell go list -m)
VERSION ?= $(shell git describe --tags --always --dirty --match="v*" 2> /dev/null || echo v0)
DATE    ?= $(shell date +%FT%T%z)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BRANCH  ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)

# Build flags
LDFLAGS = -X main.Version=$(VERSION) -X main.BuildDate=$(DATE) -X main.GitCommit=$(COMMIT) -X main.GitBranch=$(BRANCH)

# Directories
BIN_DIR = .bin

# Release platforms
PLATFORMS = darwin linux windows
ARCHITECTURES = amd64 arm64

# Data URLs
SPOT_ADVISOR_URL = "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json"
SPOT_PRICE_URL = "http://spot-price.s3.amazonaws.com/spot.js"

# Go environment
export GO111MODULE=on
export CGO_ENABLED=0

.PHONY: all build test test-verbose test-race test-coverage lint fmt clean help version
.PHONY: update-data update-price check-deps setup-tools release

# Default target
all: build

# Build binary for current platform
build: update-data update-price
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

update-data: check-deps
	@echo "Updating spot advisor data..."
	@mkdir -p public/spot/data
	@wget -nv $(SPOT_ADVISOR_URL) -O public/spot/data/spot-advisor-data.json

update-price: check-deps
	@echo "Updating spot pricing data..."
	@mkdir -p public/spot/data
	@wget -nv $(SPOT_PRICE_URL) -O public/spot/data/spot-price-data.json
	@sed -i'' -e "s/callback(//g" public/spot/data/spot-price-data.json
	@sed -i'' -e "s/);//g" public/spot/data/spot-price-data.json

# Development tools
setup-tools:
	@echo "Installing development tools..."
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

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
	@echo "  release       Build binaries for all platforms"
	@echo "  clean         Remove build artifacts"
	@echo "  setup-tools   Install development tools"
	@echo "  version       Show version"
	@echo "  help          Show this help"