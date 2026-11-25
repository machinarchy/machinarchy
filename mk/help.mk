# ============================================================================
# `make help`: lists every target with a `## description`, grouped by the
# preceding `##@ Section` line. Included last so it sees all makefiles.
# ============================================================================

.PHONY: help

help: ## Show this help
	@awk 'BEGIN { FS = ":.*##"; printf "\nMachinarchy developer commands\n\nUsage: make <target> [VAR=value ...]\n" } \
	/^##@/ { printf "\n%s\n", substr($$0, 5); next } \
	/^[a-zA-Z0-9_.-]+:.*##/ { printf "  %-22s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@printf '\nCommon variables: ENV_FILE=%s BIN_DIR=%s ARGS="..." VERSION=%s\n\n' "$(ENV_FILE)" "$(BIN_DIR)" "$(VERSION)"
