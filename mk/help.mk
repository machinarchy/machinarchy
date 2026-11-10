# ============================================================================
# HELP
# ============================================================================

.PHONY: help

help: ## Show available developer commands
	@awk 'BEGIN { \
		FS = ":.*##"; \
		printf "\nMachinarchy - Developer Commands\n"; \
		printf "================================\n"; \
		category = "General"; \
	} \
	/^##@/ { \
		category = substr($$0, 5); \
		printf "\n%s\n", category; \
		for (i = 0; i < length(category); i++) printf "-"; \
		printf "\n"; \
		next; \
	} \
	/^[a-zA-Z0-9_.-]+:.*##/ { \
		description = $$2; \
		sub(/^[[:space:]]+/, "", description); \
		printf "  %-24s %s\n", $$1, description; \
	} \
	END { \
		printf "\nRun make <command> to execute a command.\n\n"; \
	}' $(MAKEFILE_LIST)
