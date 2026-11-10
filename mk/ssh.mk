# ============================================================================
# LOCAL SSH TEST FIXTURES
# ============================================================================

.PHONY: ssh-servers-prepare ssh-servers-config ssh-servers-up ssh-servers-down
.PHONY: ssh-servers-ps ssh-servers-logs ssh-servers-restart ssh-servers-pull

##@ SSH test fixtures
ssh-servers-prepare: ## Generate local SSH test keys, password and policies (no Docker)
	@./scripts/dev/setup-ssh-servers.sh

ssh-servers-config: ssh-servers-prepare ## Validate the isolated SSH test Compose file
	$(SSH_COMPOSE) config --quiet

ssh-servers-up: ssh-servers-config ## Start all local SSH test servers
	@PUID="$$(id -u)" PGID="$$(id -g)" $(SSH_COMPOSE) up -d

ssh-servers-down: ## Stop SSH test servers (preserve local keys and host identities)
	$(SSH_COMPOSE) down

ssh-servers-ps: ## Show SSH test-server status
	$(SSH_COMPOSE) ps

ssh-servers-logs: ## Follow SSH test-server logs; optionally set SSH_SERVICE=ssh-key
	$(SSH_COMPOSE) logs --follow --tail=100 $(SSH_SERVICE)

ssh-servers-restart: ## Restart SSH test servers
	$(SSH_COMPOSE) restart $(SSH_SERVICE)

ssh-servers-pull: ## Pull SSH test-server images
	$(SSH_COMPOSE) pull
