# ============================================================================
# Shared configuration: tools, paths, build metadata and helper macros.
# Every variable can be overridden on the command line, e.g.
#   make build GO=/path/to/go BIN_DIR=out
# ============================================================================

# --- Tools ------------------------------------------------------------------
GO            ?= go
GOFMT         ?= gofmt
GOLANGCI_LINT ?= golangci-lint
GOSEC         ?= gosec
GOVULNCHECK   ?= govulncheck
SHELLCHECK    ?= shellcheck
SWAG          ?= swag
COMPOSE       ?= docker compose

# --- Paths ------------------------------------------------------------------
ENV_FILE      ?= .env
BIN_DIR       ?= bin
COVERAGE_FILE ?= coverage.out
COVERAGE_HTML ?= coverage.html

# Extra arguments for run-* targets: make run-cli ARGS="get hosts"
ARGS ?=

# --- Build metadata -----------------------------------------------------------
# Computed once per make invocation (not on every expansion). Pass VERSION,
# COMMIT or BUILD_TIME explicitly to override, e.g. in release pipelines.
MODULE_PATH := $(shell awk '$$1 == "module" { print $$2; exit }' go.mod 2>/dev/null)
ifeq ($(strip $(MODULE_PATH)),)
$(error Unable to read the module path from go.mod; run make from the repository root)
endif

ifndef VERSION
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
endif
ifndef COMMIT
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
endif
ifndef BUILD_TIME
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
endif

# Version information is read by internal/buildinfo (the `machinarchy version`
# command and the CLI User-Agent).
BUILDINFO_PKG     := $(MODULE_PATH)/internal/buildinfo
BUILDINFO_LDFLAGS := -X '$(BUILDINFO_PKG).Version=$(VERSION)' \
                     -X '$(BUILDINFO_PKG).Commit=$(COMMIT)' \
                     -X '$(BUILDINFO_PKG).BuildTime=$(BUILD_TIME)'

# GO_LDFLAGS (not LDFLAGS, which toolchains often export for C linkers).
BUILD_FLAGS ?= -buildvcs=true
GO_LDFLAGS  ?= $(BUILDINFO_LDFLAGS)

# --- Helpers ------------------------------------------------------------------

# $(call require_tool,<binary>,<install hint>)
# Fails with exit code 127 and a hint when a tool is missing.
define require_tool
	@command -v "$(1)" >/dev/null 2>&1 || { echo "$(1) is required. $(2)" >&2; exit 127; }
endef

# $(call go_build,<cmd directory>,<binary name>)
# Builds ./cmd/<cmd directory> into $(BIN_DIR)/<binary name>.
define go_build
	@mkdir -p "$(BIN_DIR)"
	$(GO) build $(BUILD_FLAGS) -ldflags "$(GO_LDFLAGS)" -o "$(BIN_DIR)/$(2)" ./cmd/$(1)
endef

# $(call run_with_env,<package>)
# Runs a Go package with $(ENV_FILE) exported. The file is sourced as a
# trusted, shell-compatible file; never point ENV_FILE at untrusted content.
define run_with_env
	@test -f "$(ENV_FILE)" || { echo "$(ENV_FILE) not found. Run 'make setup-local' first." >&2; exit 1; }
	@set -a && . "$(abspath $(ENV_FILE))" && set +a && exec $(GO) run $(1) $(ARGS)
endef
