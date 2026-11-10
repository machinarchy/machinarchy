# ============================================================================
# CODE QUALITY
# ============================================================================

.PHONY: fmt fmt-check vet lint shellcheck

##@ Code quality
fmt: ## Format Go source files
	$(GO) fmt ./...

fmt-check: ## Check Go formatting without modifying files
	@unformatted="$$(find . -type f -name '*.go' \
		-not -path './.git/*' \
		-not -path './vendor/*' \
		-not -path './$(BIN_DIR)/*' \
		-exec "$(GOFMT)" -l {} +)"; \
	if [ -n "$$unformatted" ]; then \
		printf 'The following Go files need gofmt:\n%s\n' "$$unformatted"; \
		exit 1; \
	fi; \
	echo "Go formatting looks good."

vet: ## Run go vet
	$(GO) vet ./...

lint: fmt-check ## Run golangci-lint
	@command -v "$(GOLANGCI_LINT)" >/dev/null 2>&1 || { \
		echo "golangci-lint is required." >&2; exit 127; \
	}
	$(GOLANGCI_LINT) run --timeout=5m ./...

shellcheck: ## Lint repository shell scripts
	@command -v "$(SHELLCHECK)" >/dev/null 2>&1 || { \
		echo "shellcheck is not installed." >&2; exit 127; \
	}
	@find scripts -type f -name '*.sh' -exec "$(SHELLCHECK)" {} +
