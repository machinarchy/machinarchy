# Phase B — Repository Gap Analysis

**Status:** Discovery complete; no implementation changes recommended before Phase C approval.  
**Comparison baseline:** [Architecture Implementation Baseline](implementation-baseline.md), the Phase 1–12 architecture documents, and ADR-005 through ADR-053.  
**Repository inspected:** current Machinarchy tree on 2026-11-11.

This is a gap analysis, not an implementation authorization. The recommended actions are sequencing guidance for a later approved phase. A structural difference is not, by itself, a reason to move or rewrite code.

## 1. Repository-to-architecture mapping

| Current area | Current responsibility and implementation | Target responsibility | Assessment |
|---|---|---|---|
| `cmd/api`, `cmd/controlmanager`, `cmd/monolit`, `cmd/machinarchy`, `cmd/agent` | Executable entry points for API, Control Manager, combined runtime, CLI, and an Agent placeholder | Preserve separately executable API and Control Manager, all-in-one composition, API-only CLI; add Agent only when its protocol is approved | **Partial.** Good executable split exists; current shared runtime dependencies and worker placement blur process roles. Agent is not implemented. |
| `internal/bootstrap` | Shared runtime construction, store and broker startup, services, health, and shutdown | Role-specific composition roots and explicit lifecycle ownership | **Partial.** Useful composition root; role selection is not yet enough to avoid initializing unrelated infrastructure. |
| `internal/domain` | Host, Run, Job, User, Environment, Audit, authorization, metrics, labels, selectors, event concepts | Transport- and infrastructure-independent resource and operation model | **Partial.** Useful existing concepts and invariants; Host contains SSH data, and some application/transport concepts leak into the domain. |
| `internal/service` | Application use cases for auth, host registration, runs, audit, metrics, targets, membership, tokens | Shared application commands/queries used by API and Control Manager | **Partial.** Constructor-injected use cases exist, but synchronous host access, process-local workers, and a broad package combine responsibilities. |
| `internal/controlmanager` | NATS subscription and heartbeat-related entry point; event handler currently logs host reconnection | Durable level-triggered controller manager with resource queue, recovery, periodic resync, and separate operation workers | **Requires substantial work.** No reconciliation pipeline or durable coordination is present. |
| `internal/broker` | NATS JetStream publisher/subscriber and stream setup | Initial DB outbox plus local wake-up/queue; broker adapter only when justified | **Working but out of initial target.** JetStream is operational code, although initial architecture defers requiring it. |
| `internal/store`, `internal/store/sqlite`, `internal/store/postgres` | Shared storage contracts, engine construction, migrations, two SQL adapters | Common persistence ports, transaction boundaries, migrations, outbox/config/secret/lease storage | **Good foundation, incomplete capabilities.** Both engines and contract tests exist; schemas and startup migration ownership need evolution. |
| `internal/config` | Environment-loaded bootstrap settings and validation | Bootstrap-only settings plus typed persisted runtime revisions and process snapshots | **Partial.** Bootstrap config is established; runtime configuration management is absent. |
| `pkg/sshclient`, `pkg/netpolicy`, `pkg/crypto` | Go SSH client, target validation/dial policy, encrypted persistence fields | Internal host-access adapters, network boundary enforcement, purpose-bound Secret Store | **Partial, with useful security controls.** Low-level SSH/security behaviors are a strong base; package direction and secret lifecycle are incomplete. |
| `internal/transport/http` | Versioned HTTP API, middleware, handlers, OpenAPI/Scalar, health and metrics | Thin transport adapter over application interfaces, private operational endpoints, complete async resource APIs | **Partial.** API v1 and useful handler tests exist; some handlers directly use repositories and runtime semantics remain synchronous. |
| `internal/cli` | HTTP client, local contexts, host/environment listing, command registry | API-only client for resource, operation, config, and secret workflows | **Good base, limited features.** Existing CLI uses the API; management workflows are missing. |
| `internal/telemetry` and HTTP/broker metrics | JSON `slog`, request/trace correlation, optional OTLP, HTTP/database/SSH and process metrics | Bounded, secret-safe telemetry for reconciliation, queues, operations, outbox, config, secret failures, health | **Partial.** Instrumentation exists; target control-plane signals and endpoint isolation are missing. |
| `internal/domain/audit`, store audit adapters | Append-oriented audit records, hash-chain checks, transactional mutation integration | Security audit across authorization, configuration, secret access, and externally anchored integrity | **Partial but valuable.** Existing integrity controls should be retained; event coverage and external anchoring are incomplete. |
| `internal/testutil`, package tests, CI | Unit, integration, adapter contract, and DB-backed CI coverage | Preserve behavior tests, add lifecycle/recovery/security/parity coverage in implementation slices | **Good foundation.** CI includes PostgreSQL and migration/schema checks. A legacy history test debt is documented by `CONTRIBUTING.md`. |

## 2. Gap analysis matrix

Risk is the consequence if the current gap remains while the target feature is implemented or operated. Relative effort estimates implementation work, not this read-only assessment.

| ID / action | Location | Current implementation | Expected responsibility / gap | Recommended action | Risk | Effort |
|---|---|---|---|---|---|---|
| G1 **Improve** | `internal/domain/host.go` | Host includes `SSHAccess`, credential fields, connection method values, and SSH verification/status details. | Host is a connector-neutral managed resource; access profile and secret references belong to host-access/application boundaries. | Introduce a compatibility-preserving resource/access split when Host lifecycle work is approved; keep stable identity, labels, workspace ownership, and useful domain validation. | High: connector change and credential exposure are coupled to Host schema/API. | Large |
| G2 **Improve** | `internal/domain/run.go`; `internal/service/run_service.go`; `internal/domain/job.go` | Run is a distinct aggregate and has encrypted command arguments, status/output, cancellation and recovery behavior. Job/history concepts coexist. | Operation is a separate durable domain resource; reconciliation and imperative execution are distinct. Current Run workers are process-local and Run/Job naming needs explicit mapping. | Preserve Run behavior/tests; map Run to initial Operation kind and explicitly decide Job/history compatibility before schema/API migration. Do not collapse history records without evidence. | High: client, data, and in-flight operation compatibility. | Large |
| G3 **Refactor** | `internal/service/host_service.go`, `internal/service/host_bulk.go`, host HTTP handlers | Registration tests SSH synchronously (bounded timeout/capacity), then persists Host and separately publishes a notification; publish failure can occur after persistence. | Persist desired resource first, notify through transactional outbox, and reconcile latest state asynchronously. Connectivity verification is an Operation or condition, not a prerequisite for persistence. | Split request validation/persistence from verification; atomically persist resource + event; retain bulk preview/policy behavior while making execution semantics explicit. | High: changes create semantics and failure handling. | Large |
| G4 **Create** | `internal/controlmanager/engine.go`, `subscriber.go` | NATS durable consumer receives reconnection messages; handler logs only. No resource-key queue, controller registry, startup scan, resync, or desired/observed reconciliation. | Bounded workers reconcile latest database state from one queue for event and periodic triggers; stale work is safe and restart-safe. | Add controller/queue lifecycle only after persistence claims/outbox and resource model are ready; do not put business logic in subscriber handlers. | Critical: no desired-state repair loop exists. | Large |
| G5 **Refactor** | `internal/service/run_service.go`, `internal/bootstrap/runtime.go`, `cmd/api` / `cmd/controlmanager` | API runtime starts a bounded in-process `RunManager`; accepted work executes in that process and stale runs are recovered as interrupted. | Long-running Operations need durable claims and CM-owned workers; API persists/schedules and reports lifecycle. | Migrate execution ownership behind a durable operation port/claim model, with compatibility mapping and shutdown/cancel semantics; preserve per-run bounds and redaction. | Critical for API crash/restart and horizontal API replicas. | Large |
| G6 **Replace** (initial path) | `internal/broker/nats.go`, `internal/broker/publisher.go`, `internal/bootstrap/runtime.go` | NATS JetStream is initialized as a required runtime service; current host publishing is not transactionally coupled to DB writes. | Initial reliable notification is transactional outbox plus local wake/poll; PostgreSQL NOTIFY is optional wake-up only. NATS adoption is deferred pending evidence. | Decouple event port from required NATS startup; first add outbox persistence/dispatcher/consumer idempotency, then make the broker optional or defer its adapter. | High: loss/duplication and startup coupling. | Large |
| G7 **Create** | `internal/store/migrations/{sqlite,postgres}`, `internal/store`, `internal/broker` | No outbox/inbox tables, config revision, secret metadata/version, worker lease/fencing, resource generation, or observed-generation schema. | Durable event handoff, idempotent consumption, revisions, resource concurrency, encrypted secret metadata, and (later) PG coordination. | Design additive, dual-engine migrations and transactional contracts; stage worker lease tables for PostgreSQL deployment rather than pretending SQLite supports distributed locking. | Critical for reliable rollout and data safety. | Large |
| G8 **Improve** | `internal/store/store.go`, `internal/store/migrator.go`, `internal/bootstrap/runtime.go` | Each process opens stores and applies migrations during startup; assembled store exposes `*sql.DB` along with repository interfaces. | Explicit migration lifecycle and narrow persistence ports; API/CM runtime composition should not depend on broad database handles. | Keep shared repository behavior and contract suite; introduce an explicit migration command/startup policy and reduce direct DB exposure incrementally. | High with multiple processes/concurrent migration. | Medium |
| G9 **Keep** | `internal/store/contract_test.go`, `internal/store/sqlite`, `internal/store/postgres`, migrations | SQLite and PostgreSQL adapters share behavioral contracts; Postgres-specific audit locking and SQLite single-connection/WAL choices are handled separately. | Same application contract with explicit engine-specific capabilities; SQLite single-node, PostgreSQL distributed. | Preserve the common contract suite and DB-specific behavior; extend it as features land. No rewrite is indicated. | Low; regression risk if replaced unnecessarily. | Small per future extension |
| G10 **Improve** | `internal/config/config.go`, `internal/cli/config` | Typed bootstrap configuration is environment-driven and validated; CLI config stores API contexts locally. No DB-backed global runtime revisions or atomic process snapshots. | Bootstrap configuration remains external; runtime settings are persisted, immutable, validated, versioned, hot-reloaded by safe category. | Keep bootstrap loader; add a separate runtime-configuration subsystem and classify each setting before enabling live updates. | High: unvalidated runtime mutation can destabilize workers/security. | Large |
| G11 **Create** | `internal/domain`, `internal/store/migrations`, `internal/service`, `internal/transport/http/v1` | No configuration history/rollback/status API, encrypted Secret Store records/versions, secret access authorization, or secret management endpoints. Existing `pkg/crypto` encrypts selected fields only. | Global configuration revision service plus envelope-encrypted database Secret Store with external root key, purpose-bound resolution and audited access. | Build as separate application/storage/API slices; do not expose plaintext via normal reads/events/logging. Keep `pkg/crypto` only if suitable primitives pass design review. | Critical: SSH credential handling and key recovery are privileged boundaries. | Large |
| G12 **Refactor** | `pkg/sshclient/client.go`, `pkg/netpolicy/policy.go`, `pkg/crypto/crypto.go` | Useful Go SSH, host-key pinning, cancellation, output bounding, DNS-answer validation and checked dialing; public-looking `pkg` code imports `internal/domain`. | Host-access and security adapters are infrastructure under `internal`; domain remains connector-independent; secret crypto is purpose-aware. | Preserve tested behavior while moving or decoupling internal dependencies. No evidence justifies a generic connector framework beyond capability interfaces. | Medium: package API/call-site breakage; high if security behavior regresses. | Medium |
| G13 **Improve** | `internal/config/config.go`; `pkg/netpolicy/policy.go`; `internal/service/ssh_capacity.go` | DNS answers are checked and actual dial target validated; loopback/link-local/metadata denied. Production allows private destinations by default and has no destination port allowlist; process-local SSH cap is bounded. | Production egress deny-by-default with explicit destination/port grants and independent network firewall; limits understood per process. | Change production policy only alongside documented allowlist/deployment migration; retain DNS rebinding defense and add tests for approved CIDRs/ports and rebinding cases. | Critical: privileged outbound host access can reach unintended internal services. | Medium |
| G14 **Improve** | `internal/domain/auth.go`, `authorization.go`; HTTP middleware and route handlers | JWT/cookie auth, role checks, workspace checks and hashed API tokens exist. In-memory IP rate limit is process-local; no CSRF/Origin protection was found for cookie-authenticated mutations; operational endpoints share API listener. | Authentication, authorization, audit, secret authorization, CSRF protection, rate limiting, and private health/metrics are independent controls. | Preserve existing access-control paths; add route-level authorization tests, cookie CSRF/Origin strategy, identity/cost rate limits, and endpoint isolation before production exposure. | Critical: authenticated but unauthorized or cross-site actions. | Large |
| G15 **Improve** | `internal/domain/audit.go`, `internal/store/{sqlite,postgres}/audit.go`, `internal/transport/http/middleware/audit.go` | Per-workspace audit chain and mutation linkage exist; middleware records selected actions. | Audit denied access and security-sensitive configuration/secret actions, and anchor chain integrity externally per ADR-040. | Keep chain and transaction behavior; expand event coverage and operational anchor/verification procedure. | High: security investigation and tamper detection have coverage gaps. | Medium |
| G16 **Improve** | `internal/bootstrap/runtime.go`, `internal/telemetry/telemetry.go`, `internal/transport/http/server.go` | JSON structured logs, trace/request IDs, optional OTLP, HTTP/broker tracing and several Prometheus metrics exist. `/metrics`, health, and docs are served on API listener. | Add queue/reconcile/operation/outbox/config/secret lifecycle visibility; keep labels bounded and secrets/host data safe; private operational endpoints. | Retain existing telemetry wiring; add target signals when corresponding subsystems exist and split management endpoints by deployment policy. | High operational blind spots; sensitive operational metadata exposure. | Medium |
| G17 **Improve** | `internal/transport/http/v1/router.go`, endpoint handlers, `internal/service` | Versioned API exists, but router passes repository set into endpoint packages and handlers may query stores directly; Create Host currently means SSH verified and persisted. | HTTP is a transport adapter over application command/query interfaces; API clearly distinguishes persisted resource, scheduled operation, and operation completion. | Migrate handler-by-handler to use cases while preserving API v1 compatibility or versioning; document async semantic transition and add operation endpoints. | High: externally visible contract and authorization regression. | Large |
| G18 **Improve** | `internal/cli` | CLI is already API-client based and has contexts, host/environment reads and command registry; config command is local context config. | CLI remains a thin API client and gains resource/operation/config/secret commands, structured outputs and stable exit codes. | Keep current API-only boundary; extend only as endpoints become available. | Low. | Medium |
| G19 **Keep** | `internal/telemetry/telemetry.go`, HTTP middleware, `internal/store` | Tracing, structured logging, and database/HTTP/SSH metrics already exist. | Build on current instrumentation rather than add a second telemetry system. | Preserve; incrementally add bounded target metrics and readiness semantics. | Low. | Small per extension |
| G20 **Improve** | `internal/bootstrap/runtime.go`, `cmd/*/main.go`, `internal/store/store.go` | Shared runtime is convenient but every composition currently opens DB and broker; service and health initialization is centralized. | Role-specific startup owns only needed dependencies; coordinated shutdown drains CM workers/operations and flushes outbox/telemetry. | Keep shared constructors; split role assembly and lifecycle ownership once queue/outbox owners are defined. | High availability and deployability risk. | Medium |
| G21 **Remove** | `pkg/sshclient/connection_pool.go` | File contains only `package sshclient`; no pool implementation is in use. | Phase 7 explicitly defers connection pooling until evidence requires it; avoid implying a capability that is absent. | Remove the empty file when the relevant code cleanup is authorized, or leave it harmless until then. | Low. | Small |
| G22 **Improve** | `internal/service` | Large use-case package contains unrelated auth, audit, host, run, SSH capacity, target and metrics services. | Cohesive application capabilities with narrow ports; package ownership should follow behavior, not force a textbook layer split. | Split only when new code or ownership pressure makes boundaries concrete; avoid a big-bang move to a new `application` tree. | Medium maintainability risk. | Medium |
| G23 **Investigate** | `internal/domain/auth.go`, `internal/domain/metrics.go`, `internal/domain/job.go`; `internal/transport/http/v1/history` | Auth includes response-shaped token data; domain metrics are host diagnostics/history; Job and Run both describe execution-related records; history route/test exists. | Domain should hold business concepts; transport DTOs belong to API; operations and historical records require explicit semantics. | Map usages and persisted data before moving types. Keep host metrics distinct from service telemetry; retain history API/tests until replacement is proven. | Medium: accidental behavior/data loss from premature consolidation. | Medium |
| G24 **Create** | `cmd/agent`, future `internal/agent` | Agent entry point is a placeholder; no identity, registration, heartbeat lease, or outbound channel implementation. | Agent is a future connector/runtime with workload identity and leases; Host remains unchanged. | Defer until SSH/control-plane lifecycle is stable and an agent protocol is selected. | Low initially; high only if treated as implemented. | Large |
| G25 **Keep** | `.github/workflows/ci.yml`, `Makefile`, `CONTRIBUTING.md` | CI covers Go tests with PostgreSQL, schema validation, lint/audit and Docker build; contributor guide records history test debt. | Preserve parity and security gates; add feature-specific failure/recovery tests with each slice. | Keep CI structure; resolve the documented history test debt in its own approved change and do not mask it during refactors. | Medium if behavior changes bypass current gates. | Small to medium |

## 3. Correctly implemented components to preserve

- **Dual database base:** SQLite and PostgreSQL stores, six migration sets per engine, and a shared store contract suite are concrete foundations. Preserve adapter parity and engine-specific constraints.
- **SSH trust and network dialing:** `pkg/sshclient` verifies pinned host fingerprints before authentication, uses Go SSH libraries, supports cancellation/timeouts, and bounds captured output. `pkg/netpolicy` checks DNS results and validates the actual address used for dialing. Preserve these controls through package changes.
- **Bounded execution:** `internal/service/ssh_capacity.go` and `internal/service/run_service.go` bound SSH/run work; run execution has cancellation, concurrency, target pinning, stale-run recovery, and redaction coverage. Move ownership deliberately; do not discard behavior.
- **Authorization and audit foundations:** domain role/permission checks, workspace scoping, token hashing, transactional audit behavior, and hash-chain integrity have implementation and tests. Extend their coverage rather than replacing them wholesale.
- **API and CLI separation:** CLI uses an HTTP API client; API routes are versioned and OpenAPI-backed. This matches the desired interaction direction.
- **Operational base:** structured logs, correlation IDs, OTel hooks, Prometheus instrumentation, production-aware PostgreSQL TLS, non-root/distroless Docker build, and PostgreSQL CI already exist.

## 4. Existing tests and checks to preserve

Preserve these as regression contracts while evolving the architecture:

- Domain: audit chain/sanitization, authorization, Host bulk rules, labels, Run argument/policy/status/output, selectors (`internal/domain/*_test.go`).
- Application services: owner bootstrap, audit, Host bulk/policy/SSH registration, membership, Run lifecycle/cancel/recovery/concurrency/target pinning/redaction, target preview (`internal/service/*_test.go`).
- Persistence: shared engine contract and workspace/migration scope (`internal/store/contract_test.go`, `authorization_scope_test.go`); retain SQLite and PostgreSQL paths.
- HTTP: authentication, middleware, CORS, audit, handler role/error cases, fleet preview, Host selector/labels, async Run lifecycle (`internal/transport/http/**/_test.go`).
- Security adapters: network policy and DNS/dial behavior (`pkg/netpolicy/*_test.go`); SSH pinned-key ordering, authentication, unreachable targets, connection release, concurrency, and command behavior (`pkg/sshclient/*_test.go`).
- CI schema validation and PostgreSQL-backed tests in `.github/workflows/ci.yml`.

`CONTRIBUTING.md` documents `internal/transport/http/v1/history/history_test.go` as legacy test debt: it targets a removed metrics package/old handler. This analysis did not run tests and does not claim a current test result. Preserve the history behavior while replacing or repairing that test in an explicitly scoped change.

## 5. Dependency correction plan

Desired compile-time direction:

```text
cmd / bootstrap / transport / controller manager
                  ↓
             application
                  ↓
               domain

infrastructure adapters ── implement application-owned ports
```

Specific corrections:

1. Remove infrastructure/SSH types and credential material from `internal/domain/host.go`; application ports should describe capability without importing `x/crypto/ssh`.
2. Move or decouple `pkg/sshclient` and `pkg/netpolicy` from `internal/domain` imports. Because the code is currently project-internal and no external public API need is established, an `internal/hostaccess/...` boundary is likely clearer than making `pkg` depend on project-private domain types.
3. Keep repository interfaces at the narrowest application-owned boundary; adapters under `internal/store/{sqlite,postgres}` implement them. Reduce handlers’ direct repository access over time rather than forcing a one-shot package move.
4. Define one event/outbox application contract. Avoid widening consumer contracts; the currently observed publisher shapes are not identical enough to call exact duplication, but overlap should be reviewed when the outbox port is introduced.
5. Keep telemetry independent of domain types. Rename/clarify host runtime metrics vs service telemetry only if concrete ambiguity causes misuse.
6. Split `internal/bootstrap` into role composition without making domain/application import bootstrap, config loader, transport, or adapters.

The source import scan shows no evidence of an import cycle in the inspected files, but `go list ./...` could not complete because the sandbox denied writes to the Go module/build cache. A compiler-confirmed dependency graph remains unverified; no claim of verified cycle-freedom is made.

## 6. Missing functionality

### Required for the initial approved architecture

- Connector-neutral Host desired/observed state, generation/revision and conditions; asynchronous deletion/finalizer policy if unreachable hosts must be cleaned up safely.
- Durable reconciliation queue pipeline: controller registration, latest-state loading, deduplication, bounded workers, retry/backoff, startup recovery, periodic resync, cancellation and readiness/metrics.
- Transactional outbox, event envelope/version/identity, durable dispatch/retry, and idempotent consumer handling. NATS must not be a prerequisite for the initial single-node architecture.
- Persist-first host registration and explicit separation of resource persistence from connectivity verification.
- Durable operation claims/worker ownership and multi-process coordination for PostgreSQL deployments; API-local run execution is insufficient for horizontal API scaling.
- Runtime configuration revisions, validation, rollback, snapshot apply status and safe reload lifecycle.
- Encrypted Secret Store with external root-key provisioning, key/secret versioning, purpose-bound authorization, audit and recovery procedures.
- Production deny-by-default SSH egress/port policy and separate network controls.
- Secret authorization, complete sensitive-action audit coverage, CSRF protection where cookie authentication is used, private operational endpoints, and rate limits that account for identity and multiple API instances.
- Queue, reconciliation, outbox, configuration propagation, and secret-decryption observability.
- Explicit migration lifecycle and documented backup/key recovery process.

### Deliberately deferred by the approved baseline

- NATS JetStream as required initial messaging; Kafka; microservices; Kubernetes runtime dependency.
- Agent protocol/registration/leases and Agent executable behavior.
- Multiple Controller Manager replicas before PostgreSQL worker leases/fencing and global quotas exist.
- Runtime configuration scopes beyond global; external Vault/KMS provider; speculative universal connector or event abstractions.
- SSH connection pooling until measured need; no current implementation should be inferred from the empty file.

## 7. Potentially breaking changes

These require explicit compatibility/migration planning during Phase C and subsequent approved implementation slices:

1. **Host registration semantics:** current API only returns a persisted Host after synchronous SSH verification. Persist-first asynchronous registration changes what successful `POST` means and how connectivity failure is reported.
2. **Host and access schema:** extracting SSH access and credentials into profiles/SecretRefs changes payloads, storage, redaction, and possibly identifiers. Existing Host IDs and references must survive migration.
3. **Run/Job/Operation model:** moving Run execution from API to Control Manager changes worker ownership and recovery; changing integer Run paths or merging Job/history can break clients and stored records. Prefer compatibility endpoints/aliases and staged expand-contract migrations.
4. **Role/permission semantics:** narrowing or separating `runs:execute`, inventory writes, secret reads, or configuration writes can change current operator behavior. Define explicit grants and migration defaults.
5. **Runtime startup dependencies:** removing required NATS and separating role bootstrap changes environment variables, health and deployment composition.
6. **Security defaults:** production egress deny-by-default requires configured destination/port allowlists; changing defaults without operator migration can make existing SSH destinations unreachable.
7. **Operational routes and authentication:** moving metrics/health/docs to a private listener can change probes and dashboards; coordinate deployment changes.
8. **Encryption key behavior:** removing development key fallback or introducing external root keys affects existing encrypted Host/Run data; key migration and rollback/recovery must be tested before rollout.

## 8. Risk register

| Priority | Risk | Evidence / affected area | Required treatment |
|---|---|---|---|
| Critical | Resource writes and notification delivery are not atomic; no reconciliation repairs missed notifications. | `host_service.go`, `broker`, `controlmanager` | Outbox + authoritative resync before relying on notifications. |
| Critical | API-process ownership of long-running runs loses execution continuity on process failure and scales limits per replica. | `run_service.go`, API runtime | Durable operation claims and CM-owned execution before multi-instance API. |
| Critical | Production outbound SSH policy permits private destinations by default and does not enforce a destination port allowlist. | config and netpolicy | Deny-by-default policy, deployment allowlists, independent egress firewall. |
| Critical | SSH credential handling is embedded in Host and there is no purpose-authorized Secret Store/key lifecycle. | domain Host, crypto, schemas | Separate secrets, key recovery/rotation, authorization/audit before expanding credential workflows. |
| High | Cookie-authenticated mutation CSRF posture, process-local rate limiting, and public operational endpoints are incomplete. | HTTP middleware/server | Threat-model and implement before production exposure. |
| High | Automatic migrations and broad `*sql.DB` exposure complicate concurrent startup and persistence boundary enforcement. | store/bootstrap | Explicit migration policy and narrow ports before PG multi-process deployment. |
| High | Direct repository access in handlers risks transport bypass of application authorization and lifecycle rules. | HTTP router/handlers | Migrate to application use cases with route-level regression tests. |
| High | Runtime config, outbox, secret store, worker leases and resource revisions do not exist despite being baseline requirements. | store/domain/app | Implement in dependency order, not as scattered schema additions. |
| Medium | Package boundaries are broad (`service`) and public-looking `pkg` packages import `internal/domain`. | service, pkg | Incremental ownership cleanup; avoid unrelated rewrite. |
| Medium | Run, Job and history overlap is unclear, and one history test is known legacy debt. | domain/job/run, history | Map routes/schema/consumers before consolidation; retain observable behavior. |
| Medium | Reconciliation, outbox and configuration signals are absent from telemetry. | telemetry/HTTP metrics | Add metrics as those subsystems are created; keep labels low-cardinality and secret-safe. |
| Low | Empty connection-pool source file may imply unsupported behavior. | `pkg/sshclient/connection_pool.go` | Remove or leave until cleanup; do not build speculative pooling. |

## 9. Recommended implementation order

This is a dependency-aware order, not authorization to start implementation.

1. **Baseline and compatibility contracts:** preserve the test groups above; document current API/data behavior for Host, Run, Job/history, roles, encryption, and deployment. Repair or isolate known history test debt.
2. **Security foundations:** external encryption root-key provisioning/recovery, production egress policy and port allowlists, CSRF/authorization audit, endpoint/rate-limit model. These constrain secret and remote execution designs.
3. **Persistence/application ports:** additive cross-engine migrations, transaction boundaries, explicit migration lifecycle, repository contract extension, resource revisions and event outbox schema.
4. **Host resource lifecycle:** connector-neutral model, persist-first create/update/delete, conditions and asynchronous verification; retain existing API behavior through compatibility/version plan.
5. **Reliable change notification and reconciliation:** outbox publisher, event identity/idempotency, resource-key queue, bounded worker pool, retries, startup scan, periodic resync, metrics and shutdown.
6. **Operation ownership:** durable operation claims, Control Manager execution, API scheduling/status/cancel, preserve Run behavior and migrate records/routes deliberately. Enable multi-replica CM only with PG fencing/leases and global limits.
7. **Runtime configuration and Secret Store:** schema/revisions/snapshots first; envelope encryption/key IDs and secret ACL/audit before secret CRUD endpoints; then CLI commands.
8. **API/CLI and operational hardening:** application ports under handlers, pagination/errors/status semantics, private health/metrics, SLO signals, backup/restore and deployment docs.
9. **Evolution after evidence:** NATS, Agent, Kubernetes packaging, pooling, and additional scale features only when a measured requirement or approved product decision warrants them.

## 10. Scope, method, and verification limits

- Repository source, entry points, package files, migrations, test names, workflow, Makefile, contributor and architecture documents were inspected. The approved baseline is linked above.
- This report and changelog entry are the only intended changes for this task; source code, tests, package layout, and configuration were not changed.
- No tests, build, benchmarks, or security checks were run. The source import scan was used after `go list ./...` was blocked by sandbox permissions for the Go module/build cache; therefore import-cycle status is unverified.
- Recommendations are findings for review. No Phase C implementation should begin until the user approves it.
