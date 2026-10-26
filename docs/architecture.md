# Architecture and implementation status

## Runtime shape

Machinarchy uses a ports-and-adapters layout inside a single Go module:

```text
HTTP / process entry points
        ↓
service use cases ─────── broker / SSH client adapters
        ↓
domain contracts
        ↑
SQLite and PostgreSQL adapters
```

`internal/domain` contains the core entities and interfaces. Services depend on those ports and injected adapters. HTTP handlers translate requests to service or repository calls, and the process entry points in `cmd/` construct the dependency graph. SQLite and PostgreSQL implement the same storage contracts. Schema changes live in embedded, engine-specific SQL migrations.

## Processes and dependencies

- `machinarchy-api` serves application HTTP routes and requires a database and NATS JetStream connection during startup.
- `machinarchy-controlmanager` runs the durable JetStream consumer. It serves only health and metrics endpoints; readiness includes the database, NATS, and active worker subscription.
- `machinarchy-monolit` runs the API and control-manager worker in one process, sharing one database pool and one NATS connection. Its readiness includes the worker subscription.
- `machinarchy-agent` currently exits with an explicit “not implemented” error.
- The host service verifies SSH connection details and the expected host-key fingerprint before persistence. SSH operations are bounded by a process-wide capacity limiter.
- OpenTelemetry propagates trace context through HTTP requests and NATS messages. Prometheus metrics use a process-local registry.

## Current boundaries and known limitations

- HTTP handlers use narrow domain interfaces for storage and services. `internal/bootstrap` constructs shared infrastructure once and assembles role-specific components. The API and control-manager remain separate process entry points, while monolith mode composes both without coupling their application roles.
- The API has job read/list routes, but no command submission/execution route. The `cmd/agent` runtime is a placeholder, so job execution should not be described as available.
- The control-manager subscription currently logs host-reconnected events; its reconciliation/state-machine behavior remains a TODO.
- Host persistence and event publication are separate operations. A database write can succeed while event publication fails. Use a transactional outbox before relying on those events as a guaranteed delivery mechanism.
- SQLite is configured with one open connection by default. It is suitable for local monolith mode. For independently deployed or horizontally scaled API and control-manager processes, use PostgreSQL and a shared NATS JetStream deployment.
- The current readiness checks cover the database, NATS, and the control-manager subscription where applicable. Add dependency-specific checks for any new required backend and keep liveness independent of transient backend failures.

## API contract

Handler comments are the source for the generated API contract. `make api-docs` regenerates `docs/swagger.json` and `docs/swagger.yaml`; Scalar renders that Swagger 2.0 document at `/scalar`, and the JSON is served at `/openapi.json`. Keep route annotations, request/response types, and this contract synchronized when changing handlers.
