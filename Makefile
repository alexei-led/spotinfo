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
# Pinned so a lint or architecture gate cannot change verdict on a tool release
# nobody in this repository chose. Bump deliberately.
GOLANGCI_LINT_VERSION := v2.12.2
ARCHFIT_VERSION := v1.6.0

# The embedded-data gate. Every test here runs offline against committed bytes.
DATA_GATE_TESTS = TestLoadEmbeddedAdvisorData|TestLoadEmbeddedPricingData
DATA_GATE_TESTS := $(DATA_GATE_TESTS)|TestLoadEmbeddedArchitectureLookup_ClassifiesAdvisorFamiliesAndReviewedRegressions
DATA_GATE_TESTS := $(DATA_GATE_TESTS)|TestParseArchitectureSnapshotRejectsInvalidData
DATA_GATE_TESTS := $(DATA_GATE_TESTS)|TestEmbeddedSnapshotManifests|TestEmbeddedManifestsDescribeTheirOwnFiles
DATA_GATE_TESTS := $(DATA_GATE_TESTS)|TestEmbeddedSourceRefsCoverEverySnapshot
DATA_GATE_TESTS := $(DATA_GATE_TESTS)|TestValidateCoverageRejectsATruncatedLiveFeed
DATA_GATE_TESTS := $(DATA_GATE_TESTS)|TestEmbeddedPricesSatisfyTheNeutralRecordContract

# Go environment
export GO111MODULE=on
export CGO_ENABLED=0

.PHONY: all build test test-verbose test-race test-coverage lint fmt clean help version
.PHONY: update-data update-price update-gcp-data update-azure-data refresh-manifests verify-data check-deps setup-tools release mocks hooks
.PHONY: verify-architecture-rules verify-architecture

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
	@$(MAKE) --no-print-directory refresh-manifests

update-price: check-deps
	@echo "Updating spot pricing data..."
	@wget -nv $(SPOT_PRICE_URL) -O $(DATA_DIR)/spot-price-data.json.tmp || (rm -f $(DATA_DIR)/spot-price-data.json.tmp; exit 1)
	@test -s $(DATA_DIR)/spot-price-data.json.tmp || (rm -f $(DATA_DIR)/spot-price-data.json.tmp; echo "Error: empty price download"; exit 1)
	@mv $(DATA_DIR)/spot-price-data.json.tmp $(DATA_DIR)/spot-price-data.json
	@$(MAKE) --no-print-directory refresh-manifests

# Rewrites each sidecar manifest's payload hash, and the source hash and fetch
# time of the raw feeds, for whatever was just downloaded. Coverage floors and
# reviewed provenance stay hand-curated: regenerating a floor from the data that
# just arrived would ratchet the gate into always passing.
refresh-manifests:
	@echo "Refreshing snapshot manifests..."
	@UPDATE_GOLDEN=1 go test ./internal/spot/ -run TestEmbeddedSnapshotManifests -count=1

# Rebuilds the committed GCP catalogue from the pages its approved source
# contract names. It needs no credentials, writes nothing until every gate
# passes, and leaves the reviewed snapshot untouched on failure. Not part of
# build: refreshing data is always an explicit, reviewable step.
update-gcp-data:
	@echo "Updating GCP catalogue from the contracted pricing pages..."
	@go run ./cmd/update-gcp-data

# Rebuilds the committed Azure catalogue from the anonymous Retail Prices API
# and the contracted Microsoft Learn size pages. Same rules as the GCP target:
# no credentials, nothing written until every gate passes.
update-azure-data:
	@echo "Updating Azure catalogue from the Retail Prices API and size pages..."
	@go run ./cmd/update-azure-data

# Deterministic embedded-data gate, run offline. It checks the generic snapshot
# manifest and source-contract validators, then every committed AWS snapshot:
# it must parse, hash to what its sidecar manifest declares, and still cover its
# reviewed floor. The architecture snapshot additionally validates its review
# metadata and fail-closed coverage of every Advisor family, so a feed refresh
# surfaces families that require a manual reviewed snapshot update.
verify-data:
	@echo "Verifying embedded data, snapshot manifests and reviewed architecture coverage..."
	@go test ./internal/snapshot/ -count=1
	@go test ./internal/spot/ -run '$(DATA_GATE_TESTS)' -count=1
	@go test ./internal/providers/gcp/ -count=1
	@go test ./internal/providers/azure/ -count=1
	@go test ./cmd/update-gcp-data/ ./cmd/update-azure-data/ -count=1

# Package-boundary gate. Checks the declared layer direction and module
# metadata in .archfit.yaml; it is not a data-correctness or test substitute.
# Balanced-Coupling advisories are warnings here, which is why it is only half
# the gate — verify-architecture adds the severity check below.
verify-architecture-rules:
	@echo "Verifying architecture rules..."
	@command -v archfit > /dev/null 2>&1 || go install github.com/alexei-led/archfit/cmd/archfit@$(ARCHFIT_VERSION)
	@archfit --gate --config .archfit.yaml --full --format json

# The full architecture gate: package boundaries, then finding severity.
# archfit's own verdict is "pass" while a Critical Balanced-Coupling advisory
# stands, so cmd/archfitcheck reads the analysis and fails on any open Critical
# or High finding. The report goes through a temporary file rather than a pipe:
# a pipeline reports the exit status of the last command, which would swallow an
# archfit failure and feed the checker an empty document.
verify-architecture: verify-architecture-rules
	@echo "Verifying architecture findings..."
	@report=$$(mktemp); \
	archfit analyze --config .archfit.yaml --json > $$report; \
	status=$$?; \
	if [ $$status -ne 0 ]; then rm -f $$report; echo "archfit analyze failed"; exit $$status; fi; \
	go run ./cmd/archfitcheck < $$report; \
	status=$$?; \
	rm -f $$report; \
	exit $$status

# Development tools
setup-tools:
	@echo "Installing development tools..."
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@go install github.com/vektra/mockery/v3@$(MOCKERY_VERSION)

# Opt-in git hooks: staged gofmt/gitleaks on commit, lint+test+gitleaks on push.
# Local config only, so a clone stays hook-free until a developer runs this.
hooks:
	@git config core.hooksPath scripts/git-hooks
	@echo "git hooks enabled (disable: git config --unset core.hooksPath)"

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
	@echo "  refresh-manifests Rewrite snapshot manifest hashes for refreshed data"
	@echo "  verify-data   Verify embedded data, manifests and architecture coverage"
	@echo "  verify-architecture-rules Verify package boundaries with archfit"
	@echo "  verify-architecture Verify boundaries and fail on Critical/High findings"
	@echo "  mocks         Regenerate testify mocks from .mockery.yaml"
	@echo "  release       Build binaries for all platforms"
	@echo "  clean         Remove build artifacts"
	@echo "  setup-tools   Install development tools"
	@echo "  hooks         Enable the pre-commit/pre-push git hooks"
	@echo "  version       Show version"
	@echo "  help          Show this help"