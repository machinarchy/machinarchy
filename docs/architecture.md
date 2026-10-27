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
- JWT authentication reloads the user's workspace role on each request. Host, environment, job, and metrics-history operations are scoped in storage; host-to-environment references are checked within the same workspace. Workspace owners can issue hash-only operator/reader API tokens; token permissions cannot exceed the current membership role and revocation is checked on every request.
- The installation currently uses one shared workspace (`domain.DefaultWorkspaceID`). Every self-registered account joins it as a read-only `reader`; no per-account workspace rows are created. The first owner is created at startup from `MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD` (with optional `_USERNAME`/`_EMAIL`) only while the workspace has no owner, and there is no built-in default credential. Promoting other members is possible only through the store's `UpdateWorkspaceMemberRole`; no API or invitation flow exists yet. Workspace-scoped storage queries and the `workspace_memberships` table are kept so multiple workspaces can return later. Databases created before this change keep their existing per-account workspaces. Workspace member administration, workspace switching, and durable audit events are not implemented. Production signup remains disabled by default.
- `POST /api/v1/hosts/bulk` registers up to 100 hosts with partial-success semantics and per-record results (`success`, `valid`, `failed`, `duplicate`). Records are SSH-verified by a bounded worker pool through the same path as single registration, `validate_only` checks without contacting hosts, and no commands are executed. Duplicate detection (case-insensitive name or address:port, against the inventory and earlier records) is a read-then-write check; the schema has no uniqueness constraint, so concurrent batches can still insert the same host.
- The control-manager subscription currently logs host-reconnected events; its reconciliation/state-machine behavior remains a TODO.
- Host persistence and event publication are separate operations. A database write can succeed while event publication fails. Use a transactional outbox before relying on those events as a guaranteed delivery mechanism.
- SQLite is configured with one open connection by default. It is suitable for local monolith mode. For independently deployed or horizontally scaled API and control-manager processes, use PostgreSQL and a shared NATS JetStream deployment.
- The current readiness checks cover the database, NATS, and the control-manager subscription where applicable. Add dependency-specific checks for any new required backend and keep liveness independent of transient backend failures.

## Architectural direction

Keep the current modular monolith and ports-and-adapters boundaries while inventory, identity, authorization, and execution contracts are made reliable. Separate API and control-manager binaries already exist for independent deployment; do not split them into more services until measured load, fault isolation, or ownership requires it. Treat SQLite as a single-instance/local option and PostgreSQL as the shared multi-process database.

The workspace authorization baseline is implemented in migration 000003. Existing rows are assigned to workspace 1; the earliest existing account becomes its owner and later existing accounts receive empty private workspaces. Verify the intended legacy owner and take a database backup before upgrading. See the [security assessment](SECURITY.md) and [roadmap](ROADMAP.md) for migration recovery, token handling, and remaining work.

Use the existing NATS JetStream integration only for its current durable event flow. Do not turn it into a general job queue until job submission, delivery guarantees, deduplication, retry policy, and cancellation semantics are specified.

## API contract

Handler comments are the source for the generated API contract. `make api-docs` regenerates `docs/swagger.json` and `docs/swagger.yaml`; Scalar renders that Swagger 2.0 document at `/scalar`, and the JSON is served at `/openapi.json`. Keep route annotations, request/response types, and this contract synchronized when changing handlers.
