.PHONY: all build test test-race lint fmt vet generate sync-specs demo clean tools tools-build help

# ----- Identity -------------------------------------------------------------
MODULE       := github.com/zhangxuyang/ws1-uem-agent
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT       ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
BUILD_DATE   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
SPEC_VERSION ?= $(shell awk '/api_explorer_version:/ {print $$2}' spec/VERSION 2>/dev/null || echo unknown)

LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) \
           -X $(MODULE)/internal/version.Commit=$(COMMIT) \
           -X $(MODULE)/internal/version.BuildDate=$(BUILD_DATE) \
           -X $(MODULE)/internal/version.SpecVersion=$(SPEC_VERSION)

GO          ?= go
GOLANGCILINT ?= golangci-lint

BIN := bin

# ----- Targets --------------------------------------------------------------
all: lint test build  ## Build, test, lint everything

help:  ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build:  ## Build the ws1 binary
	@mkdir -p $(BIN)
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN)/ws1 ./cmd/ws1

tools-build:  ## Build the maintainer toolchain (ws1-build)
	@mkdir -p $(BIN)
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN)/ws1-build ./cmd/ws1-build

tools: tools-build  ## Alias for tools-build (maintainer pipeline expects this)

test:  ## Run unit + package tests
	$(GO) test ./...

test-race:  ## Run tests with the race detector
	$(GO) test -race ./...

lint:  ## Run golangci-lint (must be installed: brew install golangci-lint)
	@command -v $(GOLANGCILINT) >/dev/null 2>&1 || { \
	  echo "golangci-lint not installed. brew install golangci-lint or see https://golangci-lint.run"; \
	  exit 1; }
	$(GOLANGCILINT) run

fmt:  ## Run gofmt across the tree
	$(GO) fmt ./...

vet:  ## Run go vet
	$(GO) vet ./...

generate:  ## Regenerate code from spec/* (no-op until spec is populated)
	$(GO) generate ./...

sync-specs: tools  ## Refresh specs from a tenant: TENANT=foo.awmdm.com TOKEN=$$WS1_TOKEN make sync-specs
	@test -n "$(TENANT)" || { echo "TENANT required (e.g. TENANT=as1831.awmdm.com)"; exit 1; }
	@test -n "$(TOKEN)"  || { echo "TOKEN required (bearer for tenant)"; exit 1; }
	@mkdir -p .build
	$(BIN)/ws1-build discover --tenant=$(TENANT) --out=.build/sections.json
	$(BIN)/ws1-build fetch --sections=.build/sections.json --token=$(TOKEN) --out=spec/
	$(BIN)/ws1-build codegen-cli --specs=spec/ --out=internal/generated/
	$(BIN)/ws1-build codegen-skill --index=internal/generated/ops_index.json --policy=operations.policy.yaml --out=skills/ws1-uem/reference/
	$(GO) build ./...
	$(GO) test ./...
	$(BIN)/ws1-build classify-check --index=internal/generated/ops_index.json --policy=operations.policy.yaml
	@echo "Sync complete."

demo: build  ## Run the alice-lock end-to-end demo against a local mock tenant
	./scripts/demo.sh

clean:  ## Remove build artifacts
	rm -rf $(BIN) .build
