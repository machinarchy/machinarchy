# ============================================================================
# Setup, dependencies, builds, local runs and API documentation.
# ============================================================================

.PHONY: setup setup-local hooks-install
.PHONY: deps deps-tidy
.PHONY: build build-api build-controlmanager build-monolit build-cli build-release
.PHONY: run-api run-controlmanager run-monolit run-cli
.PHONY: api-docs clean

##@ Development setup
setup: setup-local ## Alias for setup-local

setup-local: ## Prepare the local development environment (pass options via SETUP_ARGS)
	@./scripts/dev/setup-local.sh $(SETUP_ARGS)

hooks-install: ## Configure the repository Git hooks (.githooks)
	@git rev-parse --is-inside-work-tree >/dev/null 2>&1 || { echo "Not inside a Git repository." >&2; exit 1; }
	@test -x .githooks/pre-commit && test -x .githooks/commit-msg || { echo "Git hooks are missing or not executable." >&2; exit 1; }
	@git config --local core.hooksPath .githooks
	@echo "Git hooks configured: .githooks"

##@ Dependencies
deps: ## Download and verify Go modules
	$(GO) mod download
	$(GO) mod verify

deps-tidy: ## Reconcile go.mod and go.sum (modifies files)
	$(GO) mod tidy

##@ Build
build: build-api build-controlmanager build-monolit build-cli ## Build all binaries into BIN_DIR (default ./bin)

build-api: ## Build the API server (bin/machinarchy-api)
	$(call go_build,api,machinarchy-api)

build-controlmanager: ## Build the control manager (bin/machinarchy-controlmanager)
	$(call go_build,controlmanager,machinarchy-controlmanager)

build-monolit: ## Build the single-process monolith (bin/machinarchy-monolit)
	$(call go_build,monolit,machinarchy-monolit)

build-cli: ## Build the CLI (bin/machinarchy)
	$(call go_build,machinarchy,machinarchy)

# Target-specific variables are inherited by the prerequisites, so this
# reuses the build targets above with release flags and no recursive make.
build-release: BUILD_FLAGS := -buildvcs=true -trimpath
build-release: GO_LDFLAGS := -s -w $(BUILDINFO_LDFLAGS)
build-release: build ## Build all binaries with release flags (-trimpath, stripped)

##@ Run locally (loads ENV_FILE, default .env; extra arguments via ARGS="...")
run-api: ## Run the API server
	$(call run_with_env,./cmd/api)

run-controlmanager: ## Run the control manager
	$(call run_with_env,./cmd/controlmanager)

run-monolit: ## Run the monolith (API + control manager)
	$(call run_with_env,./cmd/monolit)

run-cli: ## Run the CLI: make run-cli -- get hosts (or ARGS="get hosts")
	$(call run_with_env,./cmd/machinarchy)

# `make run-cli -- <arguments>`: the words after run-cli are passed to the
# CLI, and make treats them as goals that do nothing. Make imposes three
# limits; use ARGS="..." when one applies:
#   - a word containing '=' (e.g. --label tier=web) is taken as a variable;
#   - quoting is lost (sh -c "echo hi" arrives as three words);
#   - a word that is also a make target (help, build, ...) runs it too.
ifeq (run-cli,$(firstword $(MAKECMDGOALS)))
  RUN_CLI_ARGS := $(wordlist 2,$(words $(MAKECMDGOALS)),$(MAKECMDGOALS))
  ifneq ($(strip $(RUN_CLI_ARGS)),)
    ifeq ($(origin ARGS),command line)
      $(error use either ARGS="..." or arguments after --, not both)
    endif
    ARGS := $(RUN_CLI_ARGS)
  endif
  # Extra goals that are existing files would print "is up to date".
  MAKEFLAGS += --silent
.DEFAULT:
	@:
endif

##@ API documentation
api-docs: ## Regenerate docs/api/swagger.{json,yaml} with swag
	$(call require_tool,$(SWAG),See docs/development/prerequisites.md.)
	$(SWAG) init -g main.go -d cmd/api,internal/transport/http,internal/domain,internal/service --parseInternal -o docs/api --outputTypes json,yaml

##@ Cleanup
clean: ## Remove build and coverage artifacts
	rm -rf "$(BIN_DIR)" "$(COVERAGE_FILE)" "$(COVERAGE_HTML)"
