# Machinarchy developer commands.

GO            ?= go
SWAG          ?= swag
GOLANGCI_LINT ?= golangci-lint
GOSEC         ?= gosec
GOVULNCHECK   ?= govulncheck


VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS_COMMON = -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildTime=$(BUILD_TIME)
GCFLAGS_COMMON = -trimpath

.PHONY: setup build run-api run-controlmanager run-monolit run-cli api-docs fmt fmt-check vet test test-postgres lint gosec vuln clean

setup:
	@echo "Installing local development dependencies..."
	@./scripts/prepare.sh

build: clean
	@mkdir -p bin
	$(GO) build $(GCFLAGS_COMMON) -ldflags "$(LDFLAGS_COMMON)" -o bin/machinarchy-api ./cmd/api
	$(GO) build $(GCFLAGS_COMMON) -ldflags "$(LDFLAGS_COMMON)" -o bin/machinarchy-controlmanager ./cmd/controlmanager
	$(GO) build $(GCFLAGS_COMMON) -ldflags "$(LDFLAGS_COMMON)" -o bin/machinarchy-monolit ./cmd/monolit
	$(GO) build $(GCFLAGS_COMMON) -ldflags "$(LDFLAGS_COMMON)" -o bin/machinarchy ./cmd/machinarchy
	$(GO) build $(GCFLAGS_COMMON) -ldflags "$(LDFLAGS_COMMON)" -o bin/machinarchy-agent ./cmd/agent

run-api: api-docs
	@set -a; . ./.env; set +a; $(GO) run ./cmd/api

run-controlmanager:
	@set -a; . ./.env; set +a; $(GO) run ./cmd/controlmanager

run-monolit: api-docs
	@set -a; . ./.env; set +a; $(GO) run ./cmd/monolit

run-cli:
	$(GO) run ./cmd/machinarchy

api-docs:
	$(SWAG) init -g main.go -d cmd/api,internal/transport/http,internal/domain --parseInternal -o docs --outputTypes json,yaml

fmt:
	$(GO) fmt ./...

fmt-check:
	@unformatted="$$(find . -type f -name '*.go' -not -path './vendor/*' -exec gofmt -l {} +)"; \
	if [ -n "$$unformatted" ]; then \
	  printf 'The following Go files need gofmt:\n%s\n' "$$unformatted"; \
	  exit 1; \
	fi

vet:
	$(GO) vet ./...

test:
	$(GO) test -race -v -count=1 ./...

# scripts/test-postgres.sh is the repository-owned PostgreSQL test harness.
# The workflow expects this script to exist and consume DATABASE_URL and/or
# MACHINARCHY_TEST_POSTGRES_URL.
test-postgres:
	@bash scripts/test-postgres.sh

lint: fmt-check
	$(GOLANGCI_LINT) run --timeout=5m

gosec:
	$(GOSEC) -fmt=text -exclude-dir=docs,scripts,test ./...

vuln:
	$(GOVULNCHECK) ./...

clean:
	rm -rf bin docs/swagger.*
