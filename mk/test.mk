# ============================================================================
# TESTING AND VERIFICATION
# ============================================================================

.PHONY: test test-short test-race test-postgres
.PHONY: coverage coverage-html bench verify verify-full ci

##@ Testing
test: ## Run Go tests without race detector
	$(GO) test -count=1 ./...

test-short: ## Run short Go tests
	$(GO) test -short -count=1 ./...

test-race: ## Run Go tests with race detector (requires CGO)
	@test "$$($(GO) env CGO_ENABLED)" = "1" || { \
		echo "Race detector requires CGO_ENABLED=1 and a working C toolchain." >&2; \
		exit 1; \
	}
	$(GO) test -race -count=1 ./...

# Repository-owned PostgreSQL test harness.
# Expects DATABASE_URL and/or MACHINARCHY_TEST_POSTGRES_URL.
test-postgres: ## Run PostgreSQL integration tests
	@test -f scripts/db/test-postgres.sh || { \
		echo "scripts/db/test-postgres.sh not found." >&2; exit 1; \
	}
	bash scripts/db/test-postgres.sh

##@ Coverage and benchmarks
coverage: ## Run tests and generate coverage profile
	$(GO) test -count=1 -coverprofile="$(COVERAGE_FILE)" ./...

coverage-html: coverage ## Generate HTML coverage report
	$(GO) tool cover -html="$(COVERAGE_FILE)" -o "$(COVERAGE_HTML)"
	@echo "Coverage report: $(COVERAGE_HTML)"

bench: ## Run Go benchmarks
	$(GO) test -run '^$$' -bench . -benchmem ./...

# Standard local verification.
##@ Verification
verify: fmt-check vet test lint ## Run standard local verification

# Extended local verification, including integration and security checks.
verify-full: verify test-race test-postgres security shellcheck ## Run extended local verification

# CI-level verification and security checks.
ci: verify security ## Run CI-level verification and security checks
