# ============================================================================
# SHARED CONFIGURATION
# ============================================================================

# Toolchain. Override from the command line when needed, for example:
#   make build GO=/path/to/go
GO            ?= go
GOFMT         ?= gofmt
SWAG          ?= swag
GOLANGCI_LINT ?= golangci-lint
GOSEC         ?= gosec
GOVULNCHECK   ?= govulncheck
SHELLCHECK    ?= shellcheck

# Container tooling.
COMPOSE ?= docker compose

# Project paths and generated artifacts.
ENV_FILE      ?= .env
BIN_DIR       ?= bin
COVERAGE_FILE ?= coverage.out
COVERAGE_HTML ?= coverage.html

# Arguments forwarded to the local setup script.
SETUP_ARGS ?=

# SSH test fixtures are kept separate from the application Compose stack.
SSH_COMPOSE_FILE     ?= deployments/testing/ssh-servers.docker-compose.yml
SSH_COMPOSE_PROJECT  ?= machinarchy-ssh-test
SSH_SERVICE           ?=
SSH_COMPOSE = $(COMPOSE) --project-name "$(SSH_COMPOSE_PROJECT)" -f "$(SSH_COMPOSE_FILE)"

# Compose files live in separate locations and are operated independently.
INFRA_COMPOSE = $(COMPOSE) --env-file "$(ENV_FILE)" -f deployments/infra.docker-compose.yml
APP_COMPOSE   = $(COMPOSE) --env-file "$(ENV_FILE)" -f docker-compose.yml

# ============================================================================
# BUILD METADATA
# ============================================================================

MODULE_PATH := $(shell awk '$$1 == "module" { print $$2; exit }' go.mod 2>/dev/null)

ifeq ($(strip $(MODULE_PATH)),)
$(error Unable to determine Go module path from go.mod)
endif

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf 'dev')
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || printf 'unknown')
BUILD_TIME ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

BUILDINFO_PKG := $(MODULE_PATH)/internal/buildinfo

LDFLAGS_COMMON := \
	-X '$(BUILDINFO_PKG).Version=$(VERSION)' \
	-X '$(BUILDINFO_PKG).Commit=$(COMMIT)' \
	-X '$(BUILDINFO_PKG).BuildTime=$(BUILD_TIME)'

BUILD_FLAGS  ?= -buildvcs=true
LINKER_FLAGS ?= $(LDFLAGS_COMMON)
