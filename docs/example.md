# Machinarchy: run and development examples

This guide shows how to start Machinarchy locally, run its processes separately, inspect the API, and use the repository's build and quality commands.

## Requirements

- Go 1.26 or newer
- Docker, for the local NATS example
- NATS with JetStream enabled
- PostgreSQL for running the API and control-manager as separate local processes; SQLite is the simple local option for monolith mode

Run all commands from the repository root.

## 1. Configure the environment

Create a local environment file:

~~~sh
cp .env.example .env
~~~

Set MACHINARCHY_OPERATOR_TOKEN to a random value with at least 32 characters. For example, generate one with:

~~~sh
openssl rand -hex 32
~~~

Set MACHINARCHY_ENCRYPTION_SECRET to a separate stable value, especially when running in production. Keep it unchanged while data encrypted with it is in use.

Start NATS with JetStream in another terminal:

~~~sh
docker run --rm --name machinarchy-nats -p 4222:4222 nats:2-alpine -js
~~~

The default broker address in .env.example points to this local NATS instance.

## 2. Run API and worker together

For local development with the default SQLite database, run both components in one process:

~~~sh
make run-monolit
~~~

This target loads .env, then starts cmd/monolit. It serves the API on MACHINARCHY_LISTEN_ADDR, defaulting to :8080.

Open the Scalar API reference at <http://localhost:8080/scalar>. The raw API document is at <http://localhost:8080/openapi.json>.

Check health and metrics:

~~~sh
curl -i http://localhost:8080/healthz/live
curl -i http://localhost:8080/healthz/ready
curl -s http://localhost:8080/metrics
~~~

Readiness reports ready after the database, NATS, and control-manager subscription are available.

## 3. Configure and use the CLI

The CLI keeps named API endpoints, tokens, and default environments in a YAML config file under the platform user config directory. It creates the file with owner-only permissions. Configure a local context and provide the user JWT through stdin:

~~~sh
go run ./cmd/machinarchy config set-cluster local --server http://localhost:8080
printf '%s' "$MACHINARCHY_USER_TOKEN" | go run ./cmd/machinarchy config set-credentials local --token-stdin
go run ./cmd/machinarchy config set-context local --cluster local --user local
go run ./cmd/machinarchy config use-context local
~~~

For initial setup, create a context with a human JWT returned by signup or login. For routine CLI use, prefer a workspace-scoped API token with the `reader` or `operator` role; create it with a human owner JWT as described in [the SSH and API token use cases](example-usecases.md). API tokens are returned once and should be injected from a secret manager rather than stored in `.env` or shell startup files. Signup creates a private workspace per account; it does not support invitations or shared team workspaces. In production, signup is disabled by default.

Set MACHINARCHY_USER_TOKEN to the human JWT for initial setup, or to the provisioned API token for routine CLI use. To add another deployment, create another cluster, credential, and context, then switch with `config use-context`:

~~~sh
go run ./cmd/machinarchy config set-cluster staging --server https://api.staging.example.com
printf '%s' "$STAGING_USER_TOKEN" | go run ./cmd/machinarchy config set-credentials staging --token-stdin
go run ./cmd/machinarchy config set-context staging --cluster staging --user staging --env production
go run ./cmd/machinarchy config use-context staging
~~~

Use --context to override the selected context for one command. Use --env to filter hosts by environment name or numeric ID. The current API has no server-side environment filter, so the CLI applies that filter after fetching the host and environment lists.

Query resources and choose table, JSON, or YAML output:

~~~sh
go run ./cmd/machinarchy get hosts
go run ./cmd/machinarchy get hosts web-01
go run ./cmd/machinarchy --context local get hosts --env production -o json
go run ./cmd/machinarchy get hosts -o yaml
~~~

List and switch contexts:

~~~sh
go run ./cmd/machinarchy config get-contexts
go run ./cmd/machinarchy config current-context
go run ./cmd/machinarchy config use-context local
~~~

The CLI also accepts MACHINARCHY_CLI_CONTEXT, MACHINARCHY_CLI_ENV, MACHINARCHY_CLI_SERVER, and MACHINARCHY_CLI_TOKEN environment overrides. Global flags are --config, --context, --env, --output/-o, and --verbose/-v.

## 4. Run processes separately

The API and control-manager are separate executables. For a production-like setup, configure PostgreSQL and shared NATS. Each process opens its own database and broker connections.

Start the API:

~~~sh
make run-api
~~~

Start the control-manager in another terminal. Its HTTP listener serves health and metrics only; application API routes remain on the API process. When both processes run on the same machine, use a different port for the worker's operations endpoint:

~~~sh
set -a
. ./.env
set +a
MACHINARCHY_LISTEN_ADDR=:8081 go run ./cmd/controlmanager
~~~

The worker's operations endpoints are then available on port 8081:

~~~sh
curl -i http://localhost:8081/healthz/ready
curl -s http://localhost:8081/metrics
~~~

In separate containers or Kubernetes pods, both processes can use port 8080 because they have separate network namespaces. For local SQLite development, use monolith mode rather than having both processes open the same database file.

To run only the API, use make run-api or go run ./cmd/api with the required environment variables loaded.

## 5. Build and inspect

Build all command binaries into bin/:

~~~sh
make build
~~~

Compile all packages without modifying dependency manifests:

~~~sh
go build -mod=readonly ./...
~~~

The repository also provides:

~~~sh
make vet
make lint
make fmt
~~~

make lint requires golangci-lint. make fmt runs gofmt over Go source files and modifies them.

## 6. Run tests

Run all package tests:

~~~sh
make test
~~~

Or use the Go command directly:

~~~sh
go test ./...
~~~

There is known test-suite debt: internal/transport/http/v1/history/history_test.go still imports the removed internal/metrics package and uses the previous handler API. As documented in CONTRIBUTING.md, this legacy test needs updating before the full suite can pass. A focused test command for the configuration package is:

~~~sh
go test ./internal/config
~~~

## 7. Regenerate API documentation

The checked-in Swagger 2.0 API contract is rendered by Scalar. Regenerate the JSON and YAML contract after changing API annotations:

~~~sh
make api-docs
~~~

This command requires the Swaggo CLI on PATH. It is a documentation generator, not a runtime dependency. Scalar is served by the API and monolith at /scalar.

## 8. Build a container for each process

Build the API image (the default):

~~~sh
docker build -t machinarchy-api .
~~~

Choose another command with the Docker build argument:

~~~sh
docker build --build-arg MACHINARCHY_COMMAND=controlmanager -t machinarchy-controlmanager .
docker build --build-arg MACHINARCHY_COMMAND=monolit -t machinarchy-monolit .
docker build --build-arg MACHINARCHY_COMMAND=machinarchy -t machinarchy-cli .
~~~

Provide the required server environment variables to server containers. Mount durable storage at /data when using SQLite. Prefer PostgreSQL for separately deployed API and control-manager processes. The CLI container also needs its config file mounted at the path passed with --config.

## 9. Useful files

- README.md: project overview, configuration, and API route summary
- docs/architecture.md: service boundaries and current implementation limitations
- docs/operations.md: health probes, metrics, tracing, and shutdown behavior
- CONTRIBUTING.md: code change workflow and known test-suite debt
- .env.example: environment variable template
