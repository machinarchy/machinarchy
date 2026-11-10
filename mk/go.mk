# ============================================================================
# DEVELOPMENT SETUP, DEPENDENCIES, BUILDS, AND LOCAL EXECUTION
# ============================================================================

.PHONY: setup setup-local hooks-install deps deps-tidy
.PHONY: build build-api build-controlmanager build-monolit build-cli build-agent build-release
.PHONY: run-api run-controlmanager run-monolit run-cli run-agent
.PHONY: api-docs clean

# --------------------------------------------------------------------------
# Local development setup
# --------------------------------------------------------------------------

##@ Development setup
setup: setup-local ## Alias for setup-local

setup-local: ## Prepare local development environment
	@./scripts/dev/setup-local.sh $(SETUP_ARGS)

hooks-install: ## Explicitly configure repository Git hooks
	@git rev-parse --is-inside-work-tree >/dev/null 2>&1 || { \
		echo "Not inside a Git repository." >&2; exit 1; \
	}
	@test -x .githooks/pre-commit && test -x .githooks/commit-msg || { \
		echo "Git hooks are missing or not executable." >&2; exit 1; \
	}
	@git config --local core.hooksPath .githooks
	@echo "Git hooks configured: .githooks"

# --------------------------------------------------------------------------
# Go dependencies
# --------------------------------------------------------------------------

##@ Dependencies
deps: ## Download and verify Go dependencies
	$(GO) mod download
	$(GO) mod verify

deps-tidy: ## Reconcile go.mod and go.sum (modifies files)
	$(GO) mod tidy

# --------------------------------------------------------------------------
# Build binaries
# --------------------------------------------------------------------------

$(BIN_DIR):
	@mkdir -p "$@"

##@ Build
build: build-api build-controlmanager build-monolit build-cli build-agent ## Build all application binaries

build-api: | $(BIN_DIR) ## Build API binary
	$(GO) build $(BUILD_FLAGS) -ldflags "$(LINKER_FLAGS)" -o "$(BIN_DIR)/machinarchy-api" ./cmd/api

build-controlmanager: | $(BIN_DIR) ## Build control manager binary
	$(GO) build $(BUILD_FLAGS) -ldflags "$(LINKER_FLAGS)" -o "$(BIN_DIR)/machinarchy-controlmanager" ./cmd/controlmanager

build-monolit: | $(BIN_DIR) ## Build monolith binary
	$(GO) build $(BUILD_FLAGS) -ldflags "$(LINKER_FLAGS)" -o "$(BIN_DIR)/machinarchy-monolit" ./cmd/monolit

build-cli: | $(BIN_DIR) ## Build CLI binary
	$(GO) build $(BUILD_FLAGS) -ldflags "$(LINKER_FLAGS)" -o "$(BIN_DIR)/machinarchy" ./cmd/machinarchy

build-agent: | $(BIN_DIR) ## Build agent binary
	$(GO) build $(BUILD_FLAGS) -ldflags "$(LINKER_FLAGS)" -o "$(BIN_DIR)/machinarchy-agent" ./cmd/agent

build-release: ## Build all binaries with release-oriented flags
	$(MAKE) --no-print-directory build \
		BUILD_FLAGS="-buildvcs=true -trimpath" \
		LINKER_FLAGS="-s -w $(LDFLAGS_COMMON)"

# --------------------------------------------------------------------------
# Local application execution
# --------------------------------------------------------------------------

# The .env file is sourced as a trusted, shell-compatible local configuration
# file. Do not put arbitrary untrusted content into it.
define run_with_env
	@test -f "$(ENV_FILE)" || { \
		echo "$(ENV_FILE) not found. Run 'make setup-local' first." >&2; \
		exit 1; \
	}; \
	set -a; . "$(ENV_FILE)"; set +a; \
	exec $(GO) run $(1)
endef

##@ Run applications
run-api: ## Run API locally using .env
	$(call run_with_env,./cmd/api)

run-controlmanager: ## Run control manager locally using .env
	$(call run_with_env,./cmd/controlmanager)

run-monolit: ## Run monolith locally using .env
	$(call run_with_env,./cmd/monolit)

run-cli: ## Run CLI locally using .env
	$(call run_with_env,./cmd/machinarchy)

run-agent: ## Run agent locally using .env
	$(call run_with_env,./cmd/agent)

# --------------------------------------------------------------------------
# API documentation
# --------------------------------------------------------------------------

##@ API documentation
api-docs: ## Generate Swagger JSON and YAML documentation
	@command -v "$(SWAG)" >/dev/null 2>&1 || { \
		echo "swag is required. See docs/development/prerequisites.md." >&2; \
		exit 127; \
	}
	$(SWAG) init -g main.go -d cmd/api,internal/transport/http,internal/domain --parseInternal -o docs --outputTypes json,yaml

# --------------------------------------------------------------------------
# Cleanup
# --------------------------------------------------------------------------

##@ Cleanup
clean: ## Remove build and coverage artifacts only
	rm -rf "$(BIN_DIR)" "$(COVERAGE_FILE)" "$(COVERAGE_HTML)"
