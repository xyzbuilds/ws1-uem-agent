.PHONY: all build install uninstall test test-race lint fmt vet generate sync-specs demo clean tools tools-build help release release-clean

# ----- Identity -------------------------------------------------------------
MODULE       := github.com/xyzbuilds/ws1-uem-agent
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

# INSTALL_DIR: where `make install` drops ws1. Override on the command
# line (e.g. INSTALL_DIR=~/.local/bin make install). Defaults to
# /usr/local/bin (system-wide, needs sudo on most setups).
INSTALL_DIR ?= /usr/local/bin

install: build  ## Install ws1 to $(INSTALL_DIR) (default /usr/local/bin)
	@if [ ! -d "$(INSTALL_DIR)" ]; then \
	  echo "$(INSTALL_DIR) does not exist; create it or override INSTALL_DIR=~/.local/bin"; \
	  exit 1; \
	fi
	@if [ ! -w "$(INSTALL_DIR)" ]; then \
	  echo "$(INSTALL_DIR) is not writable; rerun with sudo, or set INSTALL_DIR=~/.local/bin"; \
	  exit 1; \
	fi
	install -m 0755 $(BIN)/ws1 $(INSTALL_DIR)/ws1
	@echo "installed: $(INSTALL_DIR)/ws1 (version $(VERSION))"

uninstall:  ## Remove ws1 from $(INSTALL_DIR)
	rm -f $(INSTALL_DIR)/ws1
	@echo "removed: $(INSTALL_DIR)/ws1"

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

sync-specs: tools  ## Diff-driven spec sync: pulls from TENANT=foo.awmdm.com, merges new ops into policy
	@test -n "$(TENANT)" || { echo "TENANT required (e.g. TENANT=as1831.awmdm.com)"; exit 1; }
	@mkdir -p .build
	$(BIN)/ws1-build discover --tenant=$(TENANT) --out=.build/sections.json
	$(BIN)/ws1-build fetch --sections=.build/sections.json $(if $(TOKEN),--token=$(TOKEN),) --out=spec/
	$(BIN)/ws1-build diff --baseline=HEAD --new=spec/ --out=.build/diff-report.json || true
	$(BIN)/ws1-build codegen-cli --specs=spec/ --out=internal/generated/
	$(BIN)/ws1-build bulk-classify --index=internal/generated/ops_index.json --out=operations.policy.yaml
	$(BIN)/ws1-build codegen-skill --index=internal/generated/ops_index.json --policy=operations.policy.yaml --out=skills/ws1-uem/reference/
	$(GO) build ./...
	$(GO) test ./...
	$(BIN)/ws1-build classify-check --index=internal/generated/ops_index.json --policy=operations.policy.yaml
	@echo "Sync complete. Review .build/diff-report.json + git diff operations.policy.yaml before committing."

sync-specs-regenerate: tools  ## Full bootstrap: throws away manual policy overrides; rarely the right call
	@test -n "$(TENANT)" || { echo "TENANT required"; exit 1; }
	@mkdir -p .build
	$(BIN)/ws1-build discover --tenant=$(TENANT) --out=.build/sections.json
	$(BIN)/ws1-build fetch --sections=.build/sections.json $(if $(TOKEN),--token=$(TOKEN),) --out=spec/
	$(BIN)/ws1-build codegen-cli --specs=spec/ --out=internal/generated/
	$(BIN)/ws1-build bulk-classify --regenerate --index=internal/generated/ops_index.json --out=operations.policy.yaml
	$(BIN)/ws1-build codegen-skill --index=internal/generated/ops_index.json --policy=operations.policy.yaml --out=skills/ws1-uem/reference/
	$(GO) build ./... && $(GO) test ./... && $(BIN)/ws1-build classify-check
	@echo "Regenerate complete. ALL manual overrides have been replaced by heuristic defaults."

demo: build  ## Run the alice-lock end-to-end demo against a local mock tenant
	./scripts/demo.sh

clean:  ## Remove build artifacts
	rm -rf $(BIN) .build dist

# ----- Release build (cross-compile) ---------------------------------------
# `make release` produces stripped, version-stamped binaries for every
# supported target platform under dist/. Used by the GitHub Release
# workflow; can also be run locally to share a binary out-of-band.
#
# Targets are deliberately a small set to keep the release surface tight:
#   - darwin/arm64    Apple Silicon Macs (M1/M2/M3)
#   - darwin/amd64    Intel Macs
#   - linux/amd64     standard cloud VMs / WSL
#   - linux/arm64     Graviton / Raspberry Pi
#   - windows/amd64   PMs running Windows
#
# CGO is off so binaries are static and copy-runnable. -s -w strips the
# symbol table + DWARF for smaller artifacts.
RELEASE_LDFLAGS := -s -w $(LDFLAGS)

DIST := dist

release: release-clean  ## Cross-compile release binaries for every supported target into dist/
	@mkdir -p $(DIST)
	@echo "→ darwin/arm64"
	@CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 $(GO) build -trimpath -ldflags '$(RELEASE_LDFLAGS)' -o $(DIST)/ws1-darwin-arm64  ./cmd/ws1
	@echo "→ darwin/amd64"
	@CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 $(GO) build -trimpath -ldflags '$(RELEASE_LDFLAGS)' -o $(DIST)/ws1-darwin-amd64  ./cmd/ws1
	@echo "→ linux/amd64"
	@CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 $(GO) build -trimpath -ldflags '$(RELEASE_LDFLAGS)' -o $(DIST)/ws1-linux-amd64   ./cmd/ws1
	@echo "→ linux/arm64"
	@CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 $(GO) build -trimpath -ldflags '$(RELEASE_LDFLAGS)' -o $(DIST)/ws1-linux-arm64   ./cmd/ws1
	@echo "→ windows/amd64"
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags '$(RELEASE_LDFLAGS)' -o $(DIST)/ws1-windows-amd64.exe ./cmd/ws1
	@cd $(DIST) && shasum -a 256 ws1-* > SHA256SUMS && cat SHA256SUMS
	@echo ""
	@echo "Release artifacts in $(DIST)/ — version $(VERSION)"

release-clean:
	@rm -rf $(DIST)
