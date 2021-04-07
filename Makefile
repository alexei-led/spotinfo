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
LINT_CONFIG = $(CURDIR)/.golangci.yml
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
all: update-data update_price fmt lint test-verbose ; $(info $(M) building $(TARGETOS)/$(TARGETARCH) binary...) @ ## Build program binary
	$Q env GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) $(GO) build \
		-tags release \
		-ldflags "$(LDFLAGS_VERSION)" \
		-o $(BIN)/$(basename $(MODULE)) ./cmd/main.go

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
				-o $(BIN)/$(basename $(MODULE))_$(GOOS)_$(GOARCH) ./cmd/main.go || true)))

.PHONY: check_file_types
check_file_types: $(BIN)/* ; $(info $(M) check file type os/arch...) @ ## Check file types for release
	@for f in $^ ; do \
        file $${f} ; \
    done

# Tools

setup-tools: setup-lint setup-gocov setup-gocov-xml setup-go2xunit setup-mockery setup-ghr

setup-lint:
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.39
setup-gocov:
	$(GO) install github.com/axw/gocov/...
setup-gocov-xml:
	$(GO) install github.com/AlekSi/gocov-xml
setup-go2xunit:
	$(GO) install github.com/tebeka/go2xunit
setup-mockery:
	$(GO) install github.com/vektra/mockery/v2/
setup-ghr:
	$(GO) install github.com/tcnksm/ghr@v0.13.0

GOLINT=golangci-lint
GOCOV=gocov
GOCOVXML=gocov-xml
GO2XUNIT=go2xunit
GOMOCK=mockery
GHR=ghr

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

.PHONY: update_price
update_price: check-deps; @ ## Update Spot pricing data file
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

test-xml: fmt | setup-go2xunit ; $(info $(M) running xUnit tests...) @ ## Run tests with xUnit output
	$Q mkdir -p test
	$Q 2>&1 $(GO) test -timeout $(TIMEOUT)s -v $(TESTPKGS) | tee test/tests.output
	$(GO2XUNIT) -fail -input test/tests.output -output test/tests.xml

COVERAGE_MODE    = atomic
COVERAGE_PROFILE = $(COVERAGE_DIR)/profile.out
COVERAGE_XML     = $(COVERAGE_DIR)/coverage.xml
COVERAGE_HTML    = $(COVERAGE_DIR)/index.html
.PHONY: test-coverage test-coverage-tools
test-coverage-tools: | setup-gocov setup-gocov-xml
test-coverage: COVERAGE_DIR := $(CURDIR)/test/coverage.$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
test-coverage: fmt test-coverage-tools ; $(info $(M) running coverage tests...) @ ## Run coverage tests
	$Q mkdir -p $(COVERAGE_DIR)
	$Q $(GO) test \
		-coverpkg=$$($(GO) list -f '{{ join .Deps "\n" }}' $(TESTPKGS) | \
					grep '^$(MODULE)/' | \
					tr '\n' ',' | sed 's/,$$//') \
		-covermode=$(COVERAGE_MODE) \
		-coverprofile="$(COVERAGE_PROFILE)" $(TESTPKGS)
	$Q $(GO) tool cover -html=$(COVERAGE_PROFILE) -o $(COVERAGE_HTML)
	$Q $(GOCOV) convert $(COVERAGE_PROFILE) | $(GOCOVXML) > $(COVERAGE_XML)

.PHONY: lint
lint: setup-lint; $(info $(M) running golangci-lint...) @ ## Run golangci-lint linters
	# updating path since golangci-lint is looking for go binary and this may lead to
	# conflict when multiple go versions are installed
	$Q env $(GOLINT) run -v -c $(LINT_CONFIG) ./...

# generate github draft release
.PHONY: github-release
github-release: setup-ghr release | check_file_types; $(info $(M) generating github draft release...) @ ## run ghr tool
ifndef RELEASE_TOKEN
	$(error RELEASE_TOKEN is undefined)
endif
	$Q $(GHR) \
		-t $(RELEASE_TOKEN) \
		-u alexei-led \
		-r spotinfo \
		-n "v$(RELEASE_TAG)" \
		-b "Draft Release" \
		-prerelease \
		-draft \
		$(RELEASE_TAG) \
		$(BIN)/$(dir $(MODULE))


# generate test mock for interfaces
.PHONY: mockgen
mockgen: | setup-mockery ; $(info $(M) generating mocks...) @ ## Run mockery to generate mocks for all interfaces
	$Q $(GOMOCK)  --dir internal --recursive --all
	$Q $(GOMOCK)  --dir public --recursive --all

.PHONY: fmt
fmt: ; $(info $(M) running gofmt...) @ ## Run gofmt on all source files
	$Q $(GO) fmt $(PKGS)

# Misc

.PHONY: clean
clean: ; $(info $(M) cleaning...)	@ ## Cleanup everything
	@rm -rf $(BIN)
	@rm -rf test/tests.* test/coverage.*

.PHONY: help
help:
	@grep -E '^[ a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

.PHONY: version
version:
	@echo $(VERSION)
