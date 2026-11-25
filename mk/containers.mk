# ============================================================================
# Docker Compose environments and SSH test fixtures.
#   infra-*        PostgreSQL and NATS   (deployments/infra.docker-compose.yml)
#   apps-*         Machinarchy services  (docker-compose.yml)
#   dev-*          both, in dependency order
#   ssh-servers-*  isolated SSH fixtures (deployments/testing/ssh-servers.docker-compose.yml)
# ============================================================================

INFRA_COMPOSE = $(COMPOSE) --env-file "$(ENV_FILE)" -f deployments/infra.docker-compose.yml
APP_COMPOSE   = $(COMPOSE) --env-file "$(ENV_FILE)" -f docker-compose.yml

SSH_COMPOSE_FILE    ?= deployments/testing/ssh-servers.docker-compose.yml
SSH_COMPOSE_PROJECT ?= machinarchy-ssh-test
SSH_SERVICE         ?=
SSH_COMPOSE          = $(COMPOSE) --project-name "$(SSH_COMPOSE_PROJECT)" -f "$(SSH_COMPOSE_FILE)"

.PHONY: images image-api image-controlmanager image-monolith image-monolit image-cli
.PHONY: infra-config infra-up infra-down infra-ps infra-logs
.PHONY: apps-config apps-up apps-down apps-ps apps-logs
.PHONY: dev-up dev-down dev-ps dev-logs
.PHONY: ssh-servers-prepare ssh-servers-config ssh-servers-up ssh-servers-down
.PHONY: ssh-servers-ps ssh-servers-logs ssh-servers-restart ssh-servers-pull

##@ Container images
# Images build with BuildKit; the agent wire contract comes from the sibling
# checkout AGENT_SOURCE. BUILD_TIME is the commit time, so rebuilding a commit
# yields the same binary.
AGENT_SOURCE ?= ../machinarchy-agent
IMAGE_PREFIX ?= machinarchy
IMAGE_TAG    ?= $(VERSION)
COMMIT_TIME  := $(shell git log -1 --format=%cI 2>/dev/null || echo unknown)
IMAGE_BUILD   = docker buildx build --load \
	--build-context agent-protocol=$(AGENT_SOURCE)/protocol \
	--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_TIME=$(COMMIT_TIME)

images: image-api image-controlmanager image-monolith image-cli ## Build all container images

image-api image-controlmanager image-monolith: image-%: ## Build one server image
	$(IMAGE_BUILD) --target server --build-arg MACHINARCHY_COMMAND=$* -t $(IMAGE_PREFIX)-$*:$(IMAGE_TAG) .

image-monolit: image-monolith # Deprecated alias of image-monolith
	@echo "make image-monolit is deprecated; use make image-monolith" >&2

image-cli: ## Build the CLI image
	$(IMAGE_BUILD) --target cli -t $(IMAGE_PREFIX)-cli:$(IMAGE_TAG) .

##@ Infrastructure (PostgreSQL, NATS)
infra-config: ## Validate the infrastructure Compose file
	$(INFRA_COMPOSE) config --quiet

infra-up: ## Start infrastructure in the background
	$(INFRA_COMPOSE) up -d

infra-down: ## Stop infrastructure (volumes are kept)
	$(INFRA_COMPOSE) down

infra-ps: ## Show infrastructure status
	$(INFRA_COMPOSE) ps

infra-logs: ## Follow infrastructure logs
	$(INFRA_COMPOSE) logs -f --tail=100

##@ Applications
apps-config: ## Validate the applications Compose file
	$(APP_COMPOSE) config --quiet

apps-up: ## Build and start the applications in the background
	$(APP_COMPOSE) up -d --build

apps-down: ## Stop the applications
	$(APP_COMPOSE) down

apps-ps: ## Show application status
	$(APP_COMPOSE) ps

apps-logs: ## Follow application logs
	$(APP_COMPOSE) logs -f --tail=100

##@ Full development environment
# Sequential sub-makes keep the order (infrastructure first) even under -j.
dev-up: ## Start infrastructure, then applications
	$(MAKE) --no-print-directory infra-up
	$(MAKE) --no-print-directory apps-up

dev-down: ## Stop applications, then infrastructure
	$(MAKE) --no-print-directory apps-down
	$(MAKE) --no-print-directory infra-down

dev-ps: ## Show application and infrastructure status
	$(MAKE) --no-print-directory infra-ps
	$(MAKE) --no-print-directory apps-ps

dev-logs: ## Follow infrastructure and application logs together
	@$(INFRA_COMPOSE) logs -f --tail=100 & infra_pid=$$!; \
	trap 'kill $$infra_pid 2>/dev/null || true' INT TERM EXIT; \
	$(APP_COMPOSE) logs -f --tail=100

##@ SSH test fixtures (SSH_SERVICE=<name> limits logs/restart to one service)
ssh-servers-prepare: ## Generate local SSH test keys, password and policies (no Docker)
	@./scripts/dev/setup-ssh-servers.sh

ssh-servers-config: ssh-servers-prepare ## Validate the SSH fixture Compose file
	$(SSH_COMPOSE) config --quiet

ssh-servers-up: ssh-servers-config ## Start the SSH test servers
	@PUID="$$(id -u)" PGID="$$(id -g)" $(SSH_COMPOSE) up -d

ssh-servers-down: ## Stop the SSH test servers (keys and host identities are kept)
	$(SSH_COMPOSE) down

ssh-servers-ps: ## Show SSH test server status
	$(SSH_COMPOSE) ps

ssh-servers-logs: ## Follow SSH test server logs
	$(SSH_COMPOSE) logs --follow --tail=100 $(SSH_SERVICE)

ssh-servers-restart: ## Restart the SSH test servers
	$(SSH_COMPOSE) restart $(SSH_SERVICE)

ssh-servers-pull: ## Pull the SSH test server images
	$(SSH_COMPOSE) pull
