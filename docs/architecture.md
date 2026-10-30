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
- The host service verifies SSH connection details and the expected host-key fingerprint before persistence. SSH operations are bounded by a process-wide capacity limiter, and every SSH dial is resolved and checked by the target address policy in `pkg/netpolicy` (see the security assessment).
- OpenTelemetry propagates trace context through HTTP requests and NATS messages. Prometheus metrics use a process-local registry.

## Current boundaries and known limitations

- HTTP handlers use narrow domain interfaces for storage and services. `internal/bootstrap` constructs shared infrastructure once and assembles role-specific components. The API and control-manager remain separate process entry points, while monolith mode composes both without coupling their application roles.
- The API has job read/list routes, but no command submission/execution route. The `cmd/agent` runtime is a placeholder, so job execution should not be described as available.
- JWT authentication reloads the user's workspace role on each request. Host, environment, job, and metrics-history operations are scoped in storage; host-to-environment references are checked within the same workspace. Workspace owners can issue hash-only operator/reader API tokens; token permissions cannot exceed the current membership role and revocation is checked on every request.
- The installation currently uses one shared workspace (`domain.DefaultWorkspaceID`). Every self-registered account joins it as a read-only `reader`; no per-account workspace rows are created. The first owner is created at startup from `MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD` (with optional `_USERNAME`/`_EMAIL`) only while the workspace has no owner, and there is no built-in default credential. Owners list members and change roles with `GET /api/v1/members` and `PUT /api/v1/members/{id}/role` (human owner sessions only; the last owner cannot be demoted, enforced atomically in the store; changes are recorded in the audit trail); there is no invitation or removal flow yet. Workspace-scoped storage queries and the `workspace_memberships` table are kept so multiple workspaces can return later. Databases created before this change keep their existing per-account workspaces. Member invitation/removal and workspace switching are not implemented. Production signup remains disabled by default.
- Fleet targeting is shared by listing, preview, and (later) execution. Hosts carry validated labels (keys of 1-63 characters from letters, digits, `.`, `_`, `-`, `/`; values up to 63 characters; at most 50 per host; the built-in field names `id`, `name`, `hostname`, `address`, `status`, `environment`, and `connection` are reserved). A selector is a comma-separated AND of requirements: `key=value`, `key!=value`, `key in (a,b)`, `key notin (a,b)`, `key`, and `!key`; values may use `*` and `?`; built-in fields compare case-insensitively and labels case-sensitively; `!=` and `notin` also match hosts without the label. An empty or invalid selector is an error, never "all hosts". `GET /api/v1/hosts?selector=...` filters the listing, `PATCH /api/v1/hosts/{id}/labels` sets and removes labels atomically without touching connection settings or credentials, and `POST /api/v1/fleet/targets/preview` resolves a rule (`selector`, `include`, `exclude`, or an explicit `all`) to the exact host set, the hosts the exclude list removed, and entries that matched nothing, without contacting any host and without returning credentials. Exclude always wins over selector and include; `all` cannot be combined with a selector or include list.
- Sensitive changes are audited by the stores themselves: account creation, member role changes, host create/update/delete, label patches, and API token create/revoke each write an `audit_events` row in the same transaction. The API middleware `AuditActor` attaches the authenticated principal and request ID to the request context; the stores read it with `domain.AuditActorFrom`. Events are append-only (database triggers) and hash-chained per workspace; owners read them with `GET /api/v1/audit/events` and check integrity with `GET /api/v1/audit/verify`. Event details are small, sanitized, non-secret strings.
- `POST /api/v1/hosts/bulk` registers up to 100 hosts with partial-success semantics and per-record results (`success`, `valid`, `failed`, `duplicate`). Records are SSH-verified by a bounded worker pool through the same path as single registration, `validate_only` checks without contacting hosts, and no commands are executed. Duplicate detection (case-insensitive name or address:port, against the inventory and earlier records) is a fast path; the authority is the pair of unique indexes added by migration 4 (`(workspace_id, lower(name))` and `(workspace_id, lower(ip_address), port)`), so a record that loses a race with a concurrent registration is reported as a `duplicate` and single create/update return 409. Migration 4 runs a pre-flight check and refuses to start, listing the conflicting host IDs and changing nothing, if existing data already contains duplicates; resolve them by renaming or deleting hosts and restart.
- The control-manager subscription currently logs host-reconnected events; its reconciliation/state-machine behavior remains a TODO.
- Host persistence and event publication are separate operations. A database write can succeed while event publication fails. Use a transactional outbox before relying on those events as a guaranteed delivery mechanism.
- SQLite is configured with one open connection by default. It is suitable for local monolith mode. For independently deployed or horizontally scaled API and control-manager processes, use PostgreSQL and a shared NATS JetStream deployment.
- The current readiness checks cover the database, NATS, and the control-manager subscription where applicable. Add dependency-specific checks for any new required backend and keep liveness independent of transient backend failures.

## Architectural direction

Keep the current modular monolith and ports-and-adapters boundaries while inventory, identity, authorization, and execution contracts are made reliable. Separate API and control-manager binaries already exist for independent deployment; do not split them into more services until measured load, fault isolation, or ownership requires it. Treat SQLite as a single-instance/local option and PostgreSQL as the shared multi-process database.

The workspace authorization baseline is implemented in migration 000003. Existing rows are assigned to workspace 1; the earliest existing account becomes its owner and later existing accounts receive empty private workspaces. Accounts created after the single-workspace change join workspace 1 as readers, so a database migrated earlier keeps its legacy per-account workspaces alongside the shared one. Verify the intended legacy owner and take a database backup before upgrading. See the [security assessment](SECURITY.md) and [roadmap](ROADMAP.md) for migration recovery, token handling, and remaining work.

Use the existing NATS JetStream integration only for its current durable event flow. Do not turn it into a general job queue until job submission, delivery guarantees, deduplication, retry policy, and cancellation semantics are specified.

## API contract

Handler comments are the source for the generated API contract. `make api-docs` regenerates `docs/swagger.json` and `docs/swagger.yaml`; Scalar renders that Swagger 2.0 document at `/scalar`, and the JSON is served at `/openapi.json`. Keep route annotations, request/response types, and this contract synchronized when changing handlers.
