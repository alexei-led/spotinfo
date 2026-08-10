# Makefile for spotinfo

# Build variables
MODULE   = $(shell go list -m)
VERSION ?= $(shell git describe --tags --always --dirty --match="v*" 2> /dev/null || echo v0)
DATE    ?= $(shell date +%FT%T%z)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BRANCH  ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)

# Build flags.
#
# -s -w strips the symbol table and DWARF, which is 27% of the binary (58.2 MB ->
# 42.3 MB measured). It costs nothing a released CLI needs: Go panics still
# symbolize, because runtime stack traces read pclntab rather than DWARF, and
# `go tool objdump` still disassembles. Only an external debugger attaching to a
# release binary loses information, and that is not how this is debugged — build
# without the flags for that.
LDFLAGS = -s -w -X main.Version=$(VERSION) -X main.BuildDate=$(DATE) -X main.GitCommit=$(COMMIT) -X main.GitBranch=$(BRANCH) -X main.GitHubRelease=$(GITHUB_RELEASE)

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
DATA_GATE_TESTS := $(DATA_GATE_TESTS)|TestEveryEmbeddedAdvisorRangeIsLabelled
DATA_GATE_TESTS := $(DATA_GATE_TESTS)|TestEmbeddedArchivesMatchTheirJSON

# Go environment. CGO stays off so `build` and `release` produce static binaries
# for the scratch-based image; test-race overrides it for itself, because the
# race detector is built on cgo and cannot run without it.
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

# CGO_ENABLED=1 is required, not preferred: the race detector is implemented in
# C and `go test -race` refuses to run without cgo. The repository-wide
# CGO_ENABLED=0 above keeps the shipped binaries static, so this target — which
# builds nothing that ships — opts back in for itself.
test-race:
	@echo "Running tests with race detector..."
	@CGO_ENABLED=1 go test -race ./...

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
#
# A download that arrives intact can still be malformed or short of the reviewed
# coverage floor, and only refresh-manifests — which parses the payload and
# checks the floor — can tell. That gate runs on the embedded file, so the swap
# has to happen first; the reviewed payload is kept in .bak and moved back when
# the gate fails, so a bad feed never survives as the tracked data. The manifests
# need no backup: the gate validates every snapshot before it writes any sidecar,
# and rewriting a sidecar byte-for-byte is a no-op, so swapping one payload
# rewrites one manifest — atomically — and leaves the other two untouched
# (internal/spot/manifest_test.go, internal/snapshot/write.go).
update-data: check-deps
	@echo "Updating spot advisor data..."
	@wget -nv $(SPOT_ADVISOR_URL) -O $(DATA_DIR)/spot-advisor-data.json.tmp || (rm -f $(DATA_DIR)/spot-advisor-data.json.tmp; exit 1)
	@test -s $(DATA_DIR)/spot-advisor-data.json.tmp || (rm -f $(DATA_DIR)/spot-advisor-data.json.tmp; echo "Error: empty advisor download"; exit 1)
	@cp $(DATA_DIR)/spot-advisor-data.json $(DATA_DIR)/spot-advisor-data.json.bak
	@mv $(DATA_DIR)/spot-advisor-data.json.tmp $(DATA_DIR)/spot-advisor-data.json
	@$(MAKE) --no-print-directory refresh-manifests \
		|| (mv $(DATA_DIR)/spot-advisor-data.json.bak $(DATA_DIR)/spot-advisor-data.json; \
		    echo "Error: the advisor download failed the manifest gate; restored the reviewed data"; exit 1)
	@rm -f $(DATA_DIR)/spot-advisor-data.json.bak

update-price: check-deps
	@echo "Updating spot pricing data..."
	@wget -nv $(SPOT_PRICE_URL) -O $(DATA_DIR)/spot-price-data.json.tmp || (rm -f $(DATA_DIR)/spot-price-data.json.tmp; exit 1)
	@test -s $(DATA_DIR)/spot-price-data.json.tmp || (rm -f $(DATA_DIR)/spot-price-data.json.tmp; echo "Error: empty price download"; exit 1)
	@cp $(DATA_DIR)/spot-price-data.json $(DATA_DIR)/spot-price-data.json.bak
	@mv $(DATA_DIR)/spot-price-data.json.tmp $(DATA_DIR)/spot-price-data.json
	@$(MAKE) --no-print-directory refresh-manifests \
		|| (mv $(DATA_DIR)/spot-price-data.json.bak $(DATA_DIR)/spot-price-data.json; \
		    echo "Error: the price download failed the manifest gate; restored the reviewed data"; exit 1)
	@rm -f $(DATA_DIR)/spot-price-data.json.bak

# Rewrites the AWS sidecar manifests in internal/spot/data only — the payload
# hash of each, plus the source hash and fetch time of the raw feeds. The GCP
# and Azure manifests are written by their own updaters and are not touched
# here. Coverage floors and reviewed provenance stay hand-curated: regenerating
# a floor from the data that just arrived would ratchet the gate into always
# passing.
#
# REFRESH_MANIFESTS, not UPDATE_GOLDEN: this gate rewrites and passes, while the
# CLI and MCP contract goldens rewrite and fail. One shared name meant an ambient
# UPDATE_GOLDEN=1 turned verify-data into a rubber stamp that re-blessed whatever
# data was on disk and exited 0.
refresh-manifests:
	@echo "Refreshing snapshot manifests..."
	@REFRESH_MANIFESTS=1 go test ./internal/spot/ -run TestEmbeddedSnapshotManifests -count=1

# Rebuilds the committed GCP catalogue from the pages its approved source
# contract names. It needs no credentials, writes nothing until every gate
# passes, and leaves the reviewed snapshot untouched on failure. Not part of
# build: refreshing data is always an explicit, reviewable step.
# Built, not `go run`, so the binary exists for a caller that needs its exit
# code. The updater exits 75 when Google is serving two different documents
# moments apart, and the weekly workflow branches on that to report a wait
# rather than a break. Neither `go run` nor make preserves that: `go run`
# collapses every non-zero exit to 1, and make reports its own 2. The workflow
# therefore invokes $(BIN_DIR)/update-gcp-data directly — see
# .github/workflows/update-gcp-data.yaml.
update-gcp-data:
	@echo "Updating GCP catalogue from the contracted pricing pages..."
	@go build -o $(BIN_DIR)/update-gcp-data ./cmd/update-gcp-data
	@$(BIN_DIR)/update-gcp-data

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
#
# Every line clears the regeneration switches. A gate that rewrites the thing it
# is checking is not a gate, and inheriting either variable from the environment
# is enough to make this target pass on data no reviewer accepted.
verify-data:
	@echo "Verifying embedded data, snapshot manifests and reviewed architecture coverage..."
	@REFRESH_MANIFESTS= UPDATE_GOLDEN= go test ./internal/snapshot/ -count=1
	@REFRESH_MANIFESTS= UPDATE_GOLDEN= go test ./internal/spot/ -run '$(DATA_GATE_TESTS)' -count=1
	@REFRESH_MANIFESTS= UPDATE_GOLDEN= go test ./internal/providers/gcp/ -count=1
	@REFRESH_MANIFESTS= UPDATE_GOLDEN= go test ./internal/providers/azure/ -count=1
	@REFRESH_MANIFESTS= UPDATE_GOLDEN= go test ./cmd/update-gcp-data/ ./cmd/update-azure-data/ -count=1

# Package-boundary gate. Checks the declared layer direction and module
# metadata in .archfit.yaml; it is not a data-correctness or test substitute.
# Balanced-Coupling advisories are warnings here, which is why it is only half
# the gate — verify-architecture adds the severity check below.
#
# archfit is installed unconditionally rather than only when absent: reusing
# whatever build happens to be on PATH is how the invocation drifted from the
# pinned release's CLI without anything noticing.
verify-architecture-rules:
	@echo "Verifying architecture rules..."
	@go install github.com/alexei-led/archfit/cmd/archfit@$(ARCHFIT_VERSION)
	@archfit check --config .archfit.yaml --format json

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
	@go install github.com/vektra/mockery/v3@$(MOCKERY_VERSION)
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
	@echo "  update-gcp-data   Rebuild the embedded GCP catalogue from its contracted pages"
	@echo "  update-azure-data Rebuild the embedded Azure catalogue from its contracted sources"
	@echo "  refresh-manifests Rewrite the AWS sidecar manifest hashes for refreshed data"
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