# Machinarchy

Machinarchy is a Go control-plane service for registering SSH-managed hosts, grouping them into environments, and querying host and job history. It exposes an Echo-based HTTP API, stores application data in SQLite or PostgreSQL, and uses NATS JetStream for events.

> **Implementation status:** Host registration, authentication, environment management, history queries, health checks, and event plumbing are implemented. The agent runtime is currently a stub, and there is no HTTP endpoint that submits or executes jobs. Job endpoints expose records already present in storage. See [architecture and current limitations](docs/architecture.md).

## Components

- `cmd/api`: HTTP API process.
- `cmd/controlmanager`: NATS event consumer/control-manager worker with health and metrics endpoints.
- `cmd/monolit`: single-process mode that runs the API and control-manager worker together.
- `cmd/machinarchy`: Cobra-based CLI for API resources and named contexts.
- `cmd/agent`: reserved entry point; the agent runtime is not implemented yet.
- `internal/bootstrap`: shared composition for database, NATS, API, and worker dependencies.
- `internal/domain`: application entities and storage/service ports.
- `internal/service`: use cases, including host registration and SSH capacity limiting.
- `internal/store`: SQLite and PostgreSQL adapters and schema migrations.
- `internal/transport/http`: HTTP server, middleware, health endpoints, and versioned handlers.
- `internal/broker`: NATS JetStream event adapter.
- `pkg/sshclient`: SSH connection and host-key verification utilities.

## Requirements

- Go 1.26.0 or newer.
- NATS with JetStream enabled, reachable at `MACHINARCHY_BROKER_URL` (default `nats://localhost:4222`).
- SQLite (default) or PostgreSQL (optional).

Each application process connects to NATS during startup and creates or validates the bounded `MACHINARCHY` JetStream stream. For a local NATS instance with JetStream enabled:

```sh
docker run --rm -p 4222:4222 nats:2-alpine -js
```

## Quick start

```sh
cp .env.example .env
```

Set `MACHINARCHY_OPERATOR_TOKEN` to a random value of at least 32 characters. For production, also set a separate `MACHINARCHY_ENCRYPTION_SECRET` and `CORS_ALLOWED_ORIGINS`. The encryption secret must remain stable: stored SSH credentials cannot be decrypted after it changes. Self-registration is disabled by default in production; use `MACHINARCHY_SIGNUP_ENABLED=true` only when account registration is intentionally available.

Load the environment and start both application components together:

```sh
set -a
. ./.env
set +a
mkdir -p data
go run ./cmd/monolit
```

SQLite migrations run automatically at startup. The database directory is created as needed. To run only the API, use `go run ./cmd/api`. To run the API and worker as separate local processes, use PostgreSQL and give the worker a distinct operations port, for example `MACHINARCHY_LISTEN_ADDR=:8081 go run ./cmd/controlmanager`. In a multi-container deployment, each service can use its own container port.

`cmd/api` and `cmd/controlmanager` are separate deployable processes. The API owns application HTTP routes; the control-manager owns the durable NATS consumer and exposes only `/healthz`, `/healthz/live`, `/healthz/ready`, and `/metrics` for operations. For independent production scaling, deploy them separately against shared PostgreSQL and NATS. The monolith mode shares one database pool and one NATS connection within the process.

The service listens on `MACHINARCHY_LISTEN_ADDR` (default `:8080`). Open the interactive Scalar API reference at <http://localhost:8080/scalar>.

Configure the CLI to use a running API, then query hosts:

```sh
go run ./cmd/machinarchy config set-cluster local --server http://localhost:8080
printf '%s' "$MACHINARCHY_USER_TOKEN" | go run ./cmd/machinarchy config set-credentials local --token-stdin
go run ./cmd/machinarchy config set-context local --cluster local --user local
go run ./cmd/machinarchy config use-context local
go run ./cmd/machinarchy get hosts
```

The CLI stores its context file under the user config directory by default. Use `--context` to select a context for one command, `--env` (or `-n`) to filter hosts by Machinarchy environment, and `-o json` or `-o yaml` for structured output. See the [CLI guide](docs/cli/README.md), [CLI command examples](example-cli.md), and [SSH host use case](example-usecases.md) for setup and usage.

Build the default API container with `docker build -t machinarchy-api .`. Select another process with `docker build --build-arg MACHINARCHY_COMMAND=controlmanager -t machinarchy-controlmanager .`, `docker build --build-arg MACHINARCHY_COMMAND=monolit -t machinarchy-monolit .`, or `docker build --build-arg MACHINARCHY_COMMAND=machinarchy -t machinarchy-cli .`. Run server containers on a Docker network that can reach NATS, pass the values from `.env`, set `MACHINARCHY_BROKER_URL` to the NATS service address on that network, and mount a named volume at `/data` for SQLite persistence. The image runs as an unprivileged user.

## API overview

The interactive reference is rendered by Scalar. The generated API contract is available as [JSON](docs/swagger.json) or [YAML](docs/swagger.yaml); Scalar accepts the Swagger 2.0 document and upgrades it when rendering.

| Area | Routes |
| --- | --- |
| Authentication | `POST /api/auth/signup`, `/api/auth/login`, `/api/auth/logout` |
| Current user | `GET /api/v1/me` |
| Environments | `GET`, `POST /api/v1/environments`; `PUT`, `DELETE /api/v1/environments/{id}` |
| Hosts | `GET`, `POST /api/v1/hosts`; `PUT`, `DELETE /api/v1/hosts/{id}`; `GET /api/v1/hosts/{id}/jobs` |
| Jobs | `GET /api/v1/jobs`, `GET /api/v1/jobs/{id}` |
| Metrics history | `GET /api/v1/history?range=1h` |
| API tokens | `GET`, `POST /api/v1/tokens`; `DELETE /api/v1/tokens/{id}` |

Each account receives its own workspace. Existing databases assign legacy rows to the earliest account and give later accounts empty workspaces; review [the migration guidance](docs/SECURITY.md#existing-database-migration) and take a backup before upgrading. Workspace owners can issue operator or reader API tokens. The plaintext is returned once; use an `Authorization: Bearer <token>` header and store it in a secret manager. Token details and examples are in the [security guide](docs/SECURITY.md#workspace-authorization-and-api-tokens).

Human protected routes accept an `Authorization: Bearer <JWT>` header or the `auth_token` cookie returned by signup/login. API tokens are bearer-only and are not accepted as cookies. In Scalar, enter `Bearer <credential>` in the Authorization field.

## Configuration

All settings use environment variables; the application does not load `.env` files itself. `.env.example` lists the available settings. Key requirements:

| Variable | Purpose |
| --- | --- |
| `MACHINARCHY_OPERATOR_TOKEN` | JWT signing secret; required, at least 32 characters, no whitespace. |
| `MACHINARCHY_ENCRYPTION_SECRET` | Secret for at-rest SSH credential encryption. Required in production and independent of the JWT secret. |
| `MACHINARCHY_ENVIRONMENT` | `development` or `production`. |
| `MACHINARCHY_SIGNUP_ENABLED` | Enables public account self-registration. Defaults to `true` in development and `false` in production. |
| `MACHINARCHY_LISTEN_ADDR` | HTTP bind address, default `:8080`. |
| `MACHINARCHY_DATABASE_ENGINE` | `sqlite` (default) or `postgres`. |
| `MACHINARCHY_DATABASE_PATH` | SQLite file path, default `./data/machinarchy.db`. |
| `MACHINARCHY_DATABASE_URL` | PostgreSQL connection URL; production requires `sslmode=verify-full`. |
| `MACHINARCHY_BROKER_URL` | NATS URL, default `nats://localhost:4222`. |
| `CORS_ALLOWED_ORIGINS` | Comma-separated exact origins; required in production. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Optional OTLP/HTTP collector endpoint. |

Production deployments should manage secrets through a secret manager, restrict the Scalar reference and Prometheus endpoint at the network layer if they are not intended to be public, and use durable storage for the SQLite file or PostgreSQL. Keep signup disabled except during controlled onboarding. Workspace scoping is implemented, while invitations, member removal, durable audit events, and PostgreSQL integration coverage remain future work. See [the security assessment](docs/SECURITY.md) and [roadmap](docs/ROADMAP.md).

## Operations

- `GET /healthz` and `GET /healthz/live`: process liveness.
- `GET /healthz/ready`: readiness, including database and NATS checks. The control-manager and monolith also check that the control-manager subscription loop is active.
- `GET /metrics`: process and database pool metrics on all processes; the API and monolith also report HTTP request and SSH bulkhead metrics.
- Structured logs use Go's `log/slog` JSON handler. HTTP and NATS message paths are instrumented with OpenTelemetry; OTLP export can be configured through standard `OTEL_*` variables.

See [operations](docs/operations.md) for probes, metrics, telemetry, secret handling, and deployment notes.

## Development

Install the Swaggo CLI to regenerate the checked-in API contract used by Scalar:

```sh
make api-docs
make build
```

Install the generator with `go install github.com/swaggo/swag/cmd/swag@v1.16.4` if it is not already on your `PATH`. Scalar's pinned browser bundle loads from jsDelivr, so the browser needs access to that CDN; mirror the bundle and update `internal/transport/http/scalar.go` for air-gapped deployments.

`make run-api`, `make run-controlmanager`, and `make run-monolit` load `.env` before launching their respective processes. `make lint`, `make vet`, and `make test` run golangci-lint, `go vet`, and `go test ./...` respectively. See [CONTRIBUTING.md](CONTRIBUTING.md) for the change workflow.

## License

Machinarchy is distributed under the [MIT License](LICENSE).
