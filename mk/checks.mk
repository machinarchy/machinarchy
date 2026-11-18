# ============================================================================
# Code quality, tests, coverage and security checks.
# ============================================================================

.PHONY: fmt fmt-check tidy-check vet lint shellcheck
.PHONY: test test-short test-race test-postgres coverage coverage-html bench smoke
.PHONY: gosec vuln security
.PHONY: verify verify-full ci

##@ Code quality
fmt: ## Format Go source files
	$(GO) fmt ./...

fmt-check: ## Fail if any Go file needs gofmt
	@unformatted="$$(find . -type f -name '*.go' -not -path './.git/*' -not -path './vendor/*' -not -path './$(BIN_DIR)/*' -exec $(GOFMT) -l {} +)"; \
	if [ -n "$$unformatted" ]; then printf 'These Go files need gofmt (run make fmt):\n%s\n' "$$unformatted" >&2; exit 1; fi; \
	echo "Go formatting looks good."

tidy-check: ## Fail if go.mod/go.sum are not tidy or modules fail verification
	$(GO) mod verify
	$(GO) mod tidy -diff

vet: ## Run go vet
	$(GO) vet ./...

lint: ## Run golangci-lint (.golangci.yml; includes gosec and gofmt)
	$(call require_tool,$(GOLANGCI_LINT),See https://golangci-lint.run/welcome/install/.)
	$(GOLANGCI_LINT) run --timeout=5m ./...

shellcheck: ## Lint shell scripts under scripts/
	$(call require_tool,$(SHELLCHECK),Install shellcheck from your package manager.)
	find scripts -type f -name '*.sh' -exec $(SHELLCHECK) {} +

##@ Testing
test: ## Run all Go tests
	$(GO) test -count=1 ./...

test-short: ## Run Go tests in -short mode
	$(GO) test -short -count=1 ./...

test-race: ## Run Go tests with the race detector (requires CGO)
	@test "$$($(GO) env CGO_ENABLED)" = "1" || { echo "The race detector requires CGO_ENABLED=1 and a C toolchain." >&2; exit 1; }
	$(GO) test -race -count=1 ./...

test-postgres: ## Run PostgreSQL integration tests (needs MACHINARCHY_TEST_POSTGRES_URL or DATABASE_URL)
	bash scripts/db/test-postgres.sh

coverage: ## Run tests and write COVERAGE_FILE (coverage.out)
	$(GO) test -count=1 -coverprofile="$(COVERAGE_FILE)" ./...

coverage-html: coverage ## Render the coverage profile as COVERAGE_HTML (coverage.html)
	$(GO) tool cover -html="$(COVERAGE_FILE)" -o "$(COVERAGE_HTML)"
	@echo "Coverage report: $(COVERAGE_HTML)"

smoke: build ## Run the CLI end to end against a fresh monolith (scripts/smoke.sh)
	./scripts/smoke.sh

bench: ## Run Go benchmarks
	$(GO) test -run '^$$' -bench . -benchmem ./...

##@ Security
gosec: ## Run gosec on its own (lint already includes it)
	$(call require_tool,$(GOSEC),See https://github.com/securego/gosec.)
	$(GOSEC) -fmt=text ./...

vuln: ## Check modules and code paths with govulncheck
	$(call require_tool,$(GOVULNCHECK),Install with: go install golang.org/x/vuln/cmd/govulncheck@latest)
	$(GOVULNCHECK) ./...

security: gosec vuln ## Run gosec and govulncheck

##@ Verification
verify: fmt-check tidy-check vet lint test ## Standard local check before pushing

verify-full: fmt-check tidy-check vet lint test-race test-postgres security shellcheck ## Everything, incl. race, PostgreSQL and security

ci: verify security ## What CI runs: verify plus security
