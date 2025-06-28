MODULE   = $(shell $(GO) list -m)
DATE    ?= $(shell date +%FT%T%z)
VERSION ?= $(shell git describe --tags --always --dirty --match="v*" 2> /dev/null || \
			cat $(CURDIR)/.version 2> /dev/null || echo v0)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BRANCH  ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)
PKGS     = $(shell $(GO) list ./...)
TESTPKGS = $(shell $(GO) list -f \
			'{{ if or .TestGoFiles .XTestGoFiles }}{{ .ImportPath }}{{ end }}' \
			$(PKGS))
LDFLAGS_VERSION = -X main.Version=$(VERSION) -X main.BuildDate=$(DATE) -X main.GitCommit=$(COMMIT) -X main.GitBranch=$(BRANCH)
LINT_CONFIG = $(CURDIR)/.golangci.yaml
BIN      = $(CURDIR)/.bin

PLATFORMS     = darwin linux windows
ARCHITECTURES = amd64 arm64

TARGETOS   ?= $(GOOS)
TARGETARCH ?= $(GOARCH)

GO      ?= go
TIMEOUT = 15
V = 0
Q = $(if $(filter 1,$V),,@)
M = $(shell printf "\033[34;1m▶\033[0m")

export GO111MODULE=on
export CGO_ENABLED=0
export GOPROXY=https://proxy.golang.org

.PHONY: all
all: update-data update-price fmt lint test-verbose ; $(info $(M) building $(TARGETOS)/$(TARGETARCH) binary...) @ ## Build program binary
	$Q env GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) $(GO) build \
		-tags release \
		-ldflags "$(LDFLAGS_VERSION)" \
		-o $(BIN)/$(basename $(MODULE)) ./cmd/spotinfo

.PHONY: build
build: update-data update-price ; $(info $(M) building $(TARGETOS)/$(TARGETARCH) binary...) @ ## Build program binary
	$Q env GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) $(GO) build \
		-tags release \
		-ldflags "$(LDFLAGS_VERSION)" \
		-o $(BIN)/$(basename $(MODULE)) ./cmd/spotinfo

# Release for multiple platforms

.PHONY: release
release: clean ; $(info $(M) building binaries for multiple os/arch...) @ ## Build program binary for platforms and os
	$(foreach GOOS, $(PLATFORMS),\
		$(foreach GOARCH, $(ARCHITECTURES), \
			$(shell \
				if [ "$(GOARCH)" = "arm64" ] && [ "$(GOOS)" == "windows" ]; then exit 0; fi; \
				GOPROXY=$(GOPROXY) CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
				$(GO) build \
				-tags release \
				-ldflags "$(LDFLAGS_VERSION)" \
				-o $(BIN)/$(basename $(MODULE))_$(GOOS)_$(GOARCH) ./cmd/spotinfo || true)))

.PHONY: check-file-types
check-file-types: ; $(info $(M) check file type os/arch...) @ ## Check file types for release
	@for f in $(BIN)/* ; do \
        file $${f} ; \
    done

# Tools

setup-tools: setup-lint setup-mockery

setup-lint:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.6
setup-mockery:
	$(GO) install github.com/vektra/mockery/v3@latest

GOLINT=golangci-lint
GOMOCK=mockery

# upstream data
SPOT_ADVISOR_DATA_URL := "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json"
SPOT_PRICE_DATA_URL := "http://spot-price.s3.amazonaws.com/spot.js"
DEPS := "wget"

.PHONY: check-deps
check-deps: ; @ ## Verify the system has all dependencies installed
	@for DEP in $(shell echo "$(DEPS)"); do \
		command -v "$$DEP" > /dev/null 2>&1 \
		|| (echo "Error: dependency '$$DEP' is absent" ; exit 1); \
	done
	@echo "all dependencies satisfied: $(DEPS)"

.PHONY: update-data
update-data: check-deps; @ ## Update Spot Advisor data file
	@mkdir -p public/spot/data
	@wget -nv $(SPOT_ADVISOR_DATA_URL) -O - > public/spot/data/spot-advisor-data.json
	@echo "spot advisor data updated"

.PHONY: update-price
update-price: check-deps; @ ## Update Spot pricing data file
	@mkdir -p public/spot/data
	@wget -nv $(SPOT_PRICE_DATA_URL) -O - > public/spot/data/spot-price-data.json
	@sed -i'' -e "s/callback(//g" public/spot/data/spot-price-data.json
	@sed -i'' -e "s/);//g" public/spot/data/spot-price-data.json

# Tests

TEST_TARGETS := test-default test-bench test-short test-verbose test-race
.PHONY: $(TEST_TARGETS) test-xml check test tests
test-bench:   ARGS=-run=__absolutelynothing__ -bench=. ## Run benchmarks
test-short:   ARGS=-short        ## Run only short tests
test-verbose: ARGS=-v            ## Run tests in verbose mode with coverage reporting
test-race:    ARGS=-race         ## Run tests with race detectorß
$(TEST_TARGETS): NAME=$(MAKECMDGOALS:test-%=%)
$(TEST_TARGETS): test
check test tests: fmt ; $(info $(M) running $(NAME:%=% )tests...) @ ## Run tests
	$Q $(GO) test -timeout $(TIMEOUT)s $(ARGS) $(TESTPKGS)

COVERAGE_MODE    = atomic
COVERAGE_PROFILE = coverage.out
COVERAGE_HTML    = coverage.html
.PHONY: test-coverage
test-coverage: fmt ; $(info $(M) running coverage tests...) @ ## Run coverage tests with HTML output
	$Q $(GO) test -covermode=$(COVERAGE_MODE) -coverprofile=$(COVERAGE_PROFILE) ./...
	$Q $(GO) tool cover -html=$(COVERAGE_PROFILE) -o $(COVERAGE_HTML)
	$Q $(GO) tool cover -func=$(COVERAGE_PROFILE)

.PHONY: lint
lint: setup-lint; $(info $(M) running golangci-lint...) @ ## Run golangci-lint linters
	# updating path since golangci-lint is looking for go binary and this may lead to
	# conflict when multiple go versions are installed
	$Q env $(GOLINT) run -v -c $(LINT_CONFIG) ./...



# generate test mock for interfaces
.PHONY: mockgen
mockgen: | setup-mockery ; $(info $(M) generating mocks...) @ ## Generate mocks using go:generate annotations
	$Q $(GO) generate ./...

.PHONY: fmt
fmt: ; $(info $(M) running gofmt...) @ ## Run gofmt on all source files
	$Q $(GO) fmt $(PKGS)

# Misc

.PHONY: clean
clean: ; $(info $(M) cleaning...)	@ ## Cleanup everything
	@rm -rf $(BIN)
	@rm -f coverage.out coverage.html

.PHONY: help
help:
	@grep -E '^[ a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

.PHONY: version
version:
	@echo $(VERSION)
