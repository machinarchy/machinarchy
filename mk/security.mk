# ============================================================================
# SECURITY CHECKS
# ============================================================================

.PHONY: gosec vuln security

##@ Security
gosec: ## Run Go security analysis
	@command -v "$(GOSEC)" >/dev/null 2>&1 || { \
		echo "gosec is required." >&2; exit 127; \
	}
	$(GOSEC) -fmt=text ./...

vuln: ## Check dependencies and packages with govulncheck
	@command -v "$(GOVULNCHECK)" >/dev/null 2>&1 || { \
		echo "govulncheck is required." >&2; exit 127; \
	}
	$(GOVULNCHECK) ./...

security: gosec vuln ## Run Go security checks
