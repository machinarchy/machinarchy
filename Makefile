GO ?= go
SWAG ?= swag
GOLANGCI_LINT ?= golangci-lint

.PHONY: setup build run-api run-controlmanager run-monolit run-cli api-docs fmt vet test lint

setup:
	./scripts/prepare.sh

build:
	mkdir -p bin
	$(GO) build -o bin/machinarchy-api ./cmd/api
	$(GO) build -o bin/machinarchy-controlmanager ./cmd/controlmanager
	$(GO) build -o bin/machinarchy-monolit ./cmd/monolit
	$(GO) build -o bin/machinarchy ./cmd/machinarchy
	$(GO) build -o bin/machinarchy-agent ./cmd/agent

run-api:
	set -a; . ./.env; set +a; $(GO) run ./cmd/api

run-controlmanager:
	set -a; . ./.env; set +a; $(GO) run ./cmd/controlmanager

run-monolit:
	set -a; . ./.env; set +a; $(GO) run ./cmd/monolit

run-cli:
	$(GO) run ./cmd/machinarchy

api-docs:
	$(SWAG) init -g main.go -d cmd/api,internal/transport/http,internal/domain --parseInternal -o docs --outputTypes json,yaml

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

lint:
	$(GOLANGCI_LINT) run --timeout=5m
