# ============================================================================
# LOCAL CONTAINER ENVIRONMENT
# ============================================================================

.PHONY: infra-config infra-up infra-down infra-ps infra-logs
.PHONY: apps-config apps-up apps-down apps-ps apps-logs
.PHONY: dev-up dev-down dev-ps dev-logs

# --------------------------------------------------------------------------
# Infrastructure (PostgreSQL, NATS, and other shared dependencies)
# --------------------------------------------------------------------------

##@ Docker Compose: infrastructure
infra-config: ## Validate infrastructure Docker Compose configuration
	$(INFRA_COMPOSE) config --quiet

infra-up: ## Start infrastructure dependencies
	$(INFRA_COMPOSE) up -d

infra-down: ## Stop infrastructure (preserve volumes)
	$(INFRA_COMPOSE) down

infra-ps: ## Show infrastructure status
	$(INFRA_COMPOSE) ps

infra-logs: ## Follow infrastructure logs
	$(INFRA_COMPOSE) logs -f --tail=100

# --------------------------------------------------------------------------
# Machinarchy applications
# --------------------------------------------------------------------------

##@ Docker Compose: applications
apps-config: ## Validate Machinarchy applications Docker Compose configuration
	$(APP_COMPOSE) config --quiet

apps-up: ## Build and start Machinarchy applications
	$(APP_COMPOSE) up -d --build

apps-down: ## Stop Machinarchy applications
	$(APP_COMPOSE) down

apps-ps: ## Show application status
	$(APP_COMPOSE) ps

apps-logs: ## Follow application logs
	$(APP_COMPOSE) logs -f --tail=100

# --------------------------------------------------------------------------
# Complete local development environment
# --------------------------------------------------------------------------

# Keep startup ordered: application containers may depend on infrastructure
# being available. Separate recursive Make calls also behave correctly with -j.
##@ Docker Compose: full development environment
dev-up: ## Start infrastructure and applications
	$(MAKE) --no-print-directory infra-up
	$(MAKE) --no-print-directory apps-up

dev-down: ## Stop applications and infrastructure
	$(MAKE) --no-print-directory apps-down
	$(MAKE) --no-print-directory infra-down

dev-ps: ## Show applications and infrastructure service status
	$(MAKE) --no-print-directory infra-ps
	$(MAKE) --no-print-directory apps-ps

dev-logs: ## Follow infrastructure and application logs
	@$(INFRA_COMPOSE) logs -f --tail=100 & infra_pid=$$!; \
	trap 'kill $$infra_pid 2>/dev/null || true' INT TERM EXIT; \
	$(APP_COMPOSE) logs -f --tail=100
