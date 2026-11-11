# Machinarchy Architecture Implementation Baseline

**Status:** Proposed baseline; owner approval required before Phase B
**Scope:** Architecture and engineering constraints for incremental implementation. This document does not authorize code changes.

## 1. Purpose and source of truth

This baseline consolidates the approved Phase 1–11 direction and the Phase 12 integrated review. It distinguishes accepted architecture from current repository behavior and from future capabilities. It does not add a new product architecture.

Apply sources in this order when wording differs:

1. Explicit user-confirmed constraints, including Phase 6: one global runtime configuration scope, same SQLite/PostgreSQL database, encrypted database-backed secrets with an external root key, API/CLI management, event plus periodic version checking, and safe hot reload only where supported.
2. Accepted ADR decisions, including amendments and superseding ADRs. In particular ADR-018 amends ADR-007's NATS timing.
3. The Phase 12 integrated blueprint, **once the owner approves this implementation baseline**.
4. Earlier phase documents for detail not superseded by later decisions.
5. Current source code as evidence of what exists, not as authority for target behavior.

The Phase 12 blueprint and ADR-053 are currently marked approval-pending. The Phase 1–11 recommendations were accepted as working baselines when the user authorized the following phases. Some older documents still contain historical “owner input” or “proposed” language after that approval. Treat accepted ADR/status text and this baseline as current; do not reopen historical questions unless listed in §8.

### ADR disposition

All repository ADRs 005–052 are marked accepted through the successive phase approvals. They group as follows: 005–008 runtime and component boundaries; 009–015 domain/control loop; 016–018 events and broker deferral; 019–022 persistence; 023–027 configuration/secrets; 028–031 host access/Operations/future Agent boundary; 032–035 API/CLI; 036–041 security; 042–046 observability/recovery; 047–052 deployment/scaling/evolution. ADR-018 supersedes ADR-007's original broker timing. ADR-053 records Phase 12 integration resolutions but remains proposed until owner approval. The complete per-record index is in [Phase 12](phase-12-final-architecture-blueprint.md#15-complete-adr-index).

## 2. Approved architecture

- **Modular Go monolith:** keep one codebase and shared domain/application logic, with independently executable API and Control Manager roles and an all-in-one runtime. Do not split into microservices without measured scaling, fault-isolation, or ownership need.
- **CLI/API boundary:** CLI communicates with the API only. It does not open the DB, contact Hosts, implement business workflows, or own authorization rules.
- **Domain:** Host is a connector-neutral managed resource with stable Machinarchy identity, desired and observed state, generation, revision, conditions, and deletion lifecycle. Operation is a separate durable resource; Run is its first typed kind. Reconciliation and imperative Operations have independent state and worker lifecycles.
- **Resource IDs:** use application-generated opaque UUIDs represented as canonical text consistently across SQLite and PostgreSQL, with a controlled migration/mapping from existing Host/Run identifiers. Names and addresses are mutable attributes, not identity.
- **Application and dependencies:** application use cases own authorization and transaction orchestration; domain owns concepts/invariants; infrastructure adapters implement application ports. Domain does not depend on Echo, SQL, NATS, SSH, or key providers. Keep interfaces narrow and meaningful.
- **Control loop:** level-triggered state recovery plus edge-driven wake-ups enter one bounded, resource-key-based reconciliation pipeline. Controllers reload latest DB state, are idempotent, and reject stale generations/fencing epochs. No goroutine per event and no exactly-once assumption.
- **Initial controller profile:** one Control Manager, five-minute jittered full resync, four bounded Host workers, and the existing SSH capacity as an independent process-level ceiling. Tune from measured load; these are not benchmark claims.
- **Persistence:** SQLite and PostgreSQL implement common logical behavior through engine-specific adapters. State changes, audit, and outbox rows commit atomically. Network calls never occur inside DB transactions.
- **Events:** the database is authoritative. Use transactional outbox, local queue/poll for one-node operation, and PostgreSQL poll/`LISTEN`/`NOTIFY` as DB-backed coordination/wakeup in initial distributed mode. Notifications are hints; consumers deduplicate where required and re-read state.
- **Broker timing:** NATS JetStream is optional and deferred until the ADR-018/049 criteria are met; Kafka is deferred until measured streaming/retention needs justify it. NATS is not a prerequisite for SQLite or initial PostgreSQL deployments.
- **Runtime configuration and secrets:** one global, strongly typed, immutable runtime-config revision stream shares the selected DB. Per-process config snapshots apply atomically; failed application preserves the last known-good snapshot and reports the applied revision separately. Secrets are separate, versioned records encrypted in the same DB; root/KEK material is external. Secret metadata management, reference assignment, and plaintext resolution are distinct permissions.
- **Host access:** Host does not encode SSH protocol details. Initial connector uses Go SSH, pinned host keys, validated DNS/IP/port policy, purpose-bound secret resolution, bounded connections/time/output, cancellation, and structured argv. Production egress denies by default and has independent network enforcement.
- **API:** versioned HTTP transport owns DTOs, request validation/mapping, authentication boundary, safe errors, pagination, idempotency/preconditions, and async status semantics. Business logic remains in application services.
- **Security:** authentication, function/object/property/workload authorization, audit, and secret protection are independent controls. Production requirements include TLS, CSRF/Origin defense for cookie writes, separate execution/output permission, strong key separation/rotation, audit anchoring, private management endpoints, and fail-closed secret access.
- **Observability/reliability:** structured `slog`, OpenTelemetry API/OTLP, bounded-cardinality metrics, correlation IDs, distinct liveness/readiness, graceful bounded drain, and joint DB/key recovery. Audit is durable and separate from sampled telemetry.
- **Deployment:** SQLite is a one-node/one-active-composition mode on local durable storage; it is not a shared network-file coordination system. PostgreSQL is required for supported independent API/Control Manager processes and multi-replica coordination. Kubernetes is optional.

## 3. Mandatory implementation requirements

### Architecture and lifecycle

- Preserve the approved package direction and make `internal/service` an incremental application/use-case boundary; do not introduce duplicate service and application layers.
- Move HTTP multi-step workflows out of direct repository access. Keep composition in `internal/bootstrap`; entry points own process lifecycle.
- Persist a valid Host before network reachability verification. The approved domain direction is Pending/unknown reachability followed by asynchronous reconciliation. If the exact HTTP status or immediate test UX differs, stop for an API contract decision before changing behavior.
- Delete an SSH-only Host without contacting an unreachable remote endpoint when no remote cleanup is owned. Block new Operations immediately; preserve audit and Operation snapshots. Use finalizers only for a real, bounded cleanup obligation.
- Persist an Operation before reporting acceptance. Keep Operation worker queue, retries, cancellation, results, and recovery separate from reconciliation. Target ownership is the Control Manager runtime. Do not remove the current API RunManager until a durable worker cutover is verified.
- Generation changes only for desired state; status writes do not increment generation. Use storage revision/ETag for optimistic concurrency. Operations pin target and access-profile revision and must not silently retarget.
- Resource notifications carry identity/revision hints, not authoritative object snapshots. Duplicate, out-of-order, malformed, missed, or replayed messages must not bypass current-state reads or authorization.

### Persistence, events and upgrades

- Maintain SQLite and PostgreSQL adapters with equivalent logical constraints, audit behavior, optimistic concurrency, migration semantics, and contract tests.
- Use short DB transactions; atomically commit state + audit + outbox. Add inbox/durable deduplication for consumers whose side effects require it. Resource-key notifications may coalesce.
- Provide startup recovery and periodic resync; pending outbox and incomplete resource/Operation state remain recoverable from DB.
- Run PostgreSQL multi-replica queue claims in short transactions using lease expiry, renewal, monotonic fencing, generation CAS, and stale-worker rejection. Do not claim multiple Control Manager support until this exists and passes crash/partition/takeover tests.
- Use expand-migrate-contract schema evolution and a controlled migration step for distributed production. Do not let every replica race through migrations.
- Keep Operation details bounded and sanitized; the initial persistence proposal retains detailed output for 30 days and compact Operation/audit summaries for one year, subject to the unresolved deployment retention requirements below.

### Configuration, secrets and access

- Keep bootstrap-only settings separate from runtime revisions; never mutate process environment to reload.
- For every runtime setting, identify hot-reloadable, reconciliation-triggering, or restart-required semantics. Validate and prepare before atomic publication; record persisted and per-process applied versions separately.
- Security-tightening revisions (egress narrowing, credential/trust revocation, execution disablement) must gate protected work on Control Manager processes with stale policy snapshots.
- Encrypt secret versions before DB persistence using authenticated envelope encryption and an external KEK/root source. No JWT-key fallback. Implement purpose-bound runtime authorization, rotation, retirement, restore, and access audit before general Secret Store use.
- Never return plaintext secrets through normal APIs or expose secret values/ciphertext in events, logs, traces, metrics, errors, support bundles, or audit details.
- Verify pinned SSH host keys before sending credentials. Enforce production destination and port restrictions at application and network layers. Accept structured argv only; no arbitrary shell string and no automatic retry after uncertain dispatch.

## 4. Initial implementation scope versus future evolution

“Initial” describes the first supported architecture profile; it does not mean every capability must ship in one milestone. Phase B must still be divided into owner-approved, reviewable milestones.

| Initial architecture scope | Future or gated evolution |
|---|---|
| Modular monolith; all-in-one runtime; separate API/CM executables; CLI-over-API | Microservices or independently owned products |
| Host and typed Operation lifecycle; SSH connector; desired/observed state and conditions | Agent connector/protocol, Agent enrollment, mTLS identity, heartbeat leases and task gateway |
| SQLite local profile and PostgreSQL adapter/schema parity | Multiple Control Manager replicas until durable PG lease/fencing and global admission are implemented |
| Local in-process queue, startup recovery, periodic resync; durable DB outbox and initial local/PG polling path | NATS JetStream after measured fan-out/Agent/polling need; Kafka only after streaming-scale evidence |
| Global runtime configuration, DB-backed revisions, per-process snapshots; encrypted DB Secret Store with external keys | Additional config scopes, external Vault/KMS provider, config values that cannot safely hot reload |
| Go-native SSH, no connection pooling, bounded execution, secure egress | SSH pooling only after measured handshake pressure and invalidation design |
| OTel instrumentation, logs, health and private metrics endpoints | Kubernetes manifests/platform-specific operators; Kubernetes is never a runtime dependency |
| Current shared-workspace model as a one-trusted-operator-group working assumption (not a multi-tenant commitment) | Multi-tenant/customer isolation, environment as security boundary, multi-region active-active |

The single shared workspace is only a working assumption for one trusted operator group; it is not an approved SaaS/customer-isolation boundary. Decide explicitly before supporting multiple independent teams or customers in one installation. Production-oriented security requirements are part of the initial implementation whenever remote execution or secret handling is enabled. They are not deferrable to a later “hardening” phase. HA, multi-replica coordination, Agents, and broker adoption are gated capabilities and should not be represented as implemented before their prerequisites pass.

## 5. Security requirements

### Required before production remote execution or secret management

- Strong, separate authentication-signing and encryption root keys; external key provisioning, versioning/rotation and restore procedure. No silent fallback.
- Authentication plus function/object/property/workload authorization; workspace scope derived from principal, not request payload. The current `operator` permission bundle must be split so inventory write does not implicitly grant remote execution.
- Owner step-up/MFA for sensitive actions and an explicit decision on two-person approval for production egress/execution widening.
- CSRF token and strict Origin/Referer validation for cookie-authenticated writes, or bearer-only unsafe routes.
- API-token expiration, scope, ownership and revocation; identity/source-aware auth and expensive-route rate limits that work across replicas.
- Deny-by-default production SSH CIDR/port policy plus independent egress firewall; deny metadata endpoints at network layer; validate DNS answers and dial only checked addresses.
- Explicit SSH host-key pinning; auth only after server key verification; restricted remote accounts; bounded command, time, output and concurrency; no unsafe shell API.
- Purpose-bound secret resolution; no plaintext read-back; redaction and no-secret telemetry contracts.
- Distinct API/Control Manager workload identities and least-privilege DB/broker permissions; authenticated TLS for any selected broker.
- Private management/health/metrics endpoints; audit success/denial and security changes, external hash anchoring, retention and failure behavior.
- Documented backup/restore for database plus external keyring, including secret decrypt check and audit/outbox recovery.

These requirements apply before production claims. Local development may use clearly isolated test keys and test hosts, but shortcuts must not silently flow into production configuration.

## 6. Database requirements

### SQLite

- Single node and one active application composition; local durable filesystem; WAL/busy behavior and short transactions.
- Never place the DB on a shared network filesystem or use it as a distributed lease service.
- Consistent WAL-aware backup/restore; protect database and keyring independently; test recovery.
- One Control Manager; local queue rebuilt by scans. SQLite's single writer means sustained heartbeat/outbox/write workloads need measurements.

### PostgreSQL

- Shared DB for standalone API/CM and future replicas; TLS verification, least-privilege roles, pool budgets, indexes, connection limits, backup/PITR and failover behavior.
- One controlled migration owner in production. DB-backed leases/fencing are prerequisites for multiple CM replicas; DB alone does not provide exactly-once external effects.
- API/CM pool counts must be budgeted together; process-local rate/concurrency limits multiply with replicas.
- PostgreSQL correctness and concurrency tests are required before declaring distributed readiness.

## 7. Testing and engineering requirements

The supplied engineering execution prompt and repository guidance require each milestone to define scope, acceptance criteria, dependencies, exclusions and follow-up work; inspect affected interfaces/migrations; implement only that milestone; run relevant checks; and stop for review before advancing.

Required test coverage as features are implemented:

- Domain lifecycle/invariant and generation/revision tests.
- SQLite and PostgreSQL repository contract tests, uniqueness, rollback, transaction/audit/outbox atomicity, migrations and optimistic concurrency.
- Concurrency/lease/fencing/expiry/takeover tests for PostgreSQL before CM replicas.
- Duplicate, out-of-order, replayed, poison and missed-notification tests; startup and resync recovery tests.
- Worker crash, cancellation, stale-result, shutdown/drain and uncertain remote dispatch tests.
- Configuration validation, atomic snapshot, last-good retention, per-process apply status, security-policy staleness and restart-required classification tests.
- Secret AEAD/AAD, unauthorized resolution, ciphertext-at-rest, key rotation, key loss, restore and no-plaintext-leak tests.
- SSH host-key mismatch-before-auth, DNS rebinding/mixed DNS, destination/port restrictions, command quoting, output cap, timeout/cancellation and error classification tests.
- API authorization negative tests for cross-workspace/object/property access, CSRF, rate limit, idempotency, ETags, async status and safe errors; CLI/API contract tests.
- OTel/log cardinality and redaction tests; liveness/readiness partial-failure tests; backup restore drills; load/chaos tests before SLO/capacity or horizontal-scaling claims.

Run the repository's applicable workflow checks for each code milestone: `gofmt`/`make fmt`, `make vet`, relevant tests, `make lint`, and `make build`. Follow the CI workflow for race and PostgreSQL coverage. Do not claim a check passed unless it was run. Current `CONTRIBUTING.md` records legacy test debt in `internal/transport/http/v1/history/history_test.go`, which still targets the removed `internal/metrics` package/old handler API; resolve or isolate that debt before treating full `make test` as green. No tests were run for this baseline document.

## 8. Outstanding architectural questions

These are the remaining decisions that the approved architecture does not settle completely. Recommendations are included to make approval concrete; they do not silently change ADRs.

| Question | Recommended default | Why it remains open |
|---|---|---|
| Accept Phase 12 blueprint/ADR-053 as integrated implementation authority? | Approve it with the conditions in this baseline | Phase 12 and ADR-053 are explicitly marked owner-approval pending. |
| Exact Host create HTTP/CLI response for persisted-but-Pending resource | `201 Created` for persisted Host plus Pending condition; expose optional reachability check as a separate Operation | Asynchronous persistence is the accepted domain direction; public response/pending UX needs a stable contract. |
| Planned SSH credential rotation for Operations pinned to an older version | Explicit bounded overlap; immediate revocation on suspected compromise; cancel/reject queued Operations that cannot use the old version | Profile revision pinning and emergency revocation can conflict unless policy is explicit. |
| Production two-person approval and owner MFA mechanics | Require step-up MFA; require second approver for production egress widening/unrestricted execution | Security objective is approved, but deployment identity/approval workflow is not specified. |
| Operational retention values | Start with Phase 4 7/30-day outbox retention; bounded Operation details 30 days/compact record 1 year; decide audit archive/metrics retention per deployment policy | Phase 4/5 proposed windows are defaults; legal/product retention requirements are installation-specific. |
| SLO/RPO/RTO and capacity thresholds | Treat Phase 10/11 values as initial targets only; replace with measured values before production commitments | No benchmarks or recovery exercises have been run. |
| Installation trust boundary | One trusted operator group per installation; no multi-tenant guarantee | Current system uses a shared workspace; customer/team isolation needs product commitment and separate design. |

## 9. Consistency review and known repository gap

### Cross-document resolution

- ADR-007's older NATS wording is amended by accepted ADR-018; initial single-node and PostgreSQL paths use DB/outbox/polling, with optional JetStream later. Phase 12 records this resolution and Phase 1/3 text has been amended, but historical diagrams/examples may still mention NATS as an available adapter.
- ADR-006/Phase 1 make Control Manager the target owner of durable Operation workers; Phase 8 and Phase 11 correctly identify API-local `RunManager` as current transitional behavior. It is not an alternative target architecture.
- Phase 2 recommends async Host observation and Phase 12 reconciles it with current synchronous SSH-before-persist behavior. Persist-first is the target; response details remain in §8.
- Secret Store decisions consistently preserve same database/global scope while keeping key material external. Envelope encryption and purpose-bound authorization are target capabilities, not current behavior.
- SQLite single-node versus PostgreSQL distributed and single-CM versus leased multiple-CM boundaries agree across Phases 3, 5, 11, ADR-015, ADR-047 and ADR-048.
- Phases 10/11 SLO, scaling, heartbeat and RPO/RTO values are estimates. None are implementation guarantees.

### Current implementation is not the baseline

The repository currently has SQLite/PostgreSQL stores, NATS JetStream, Go SSH with host-key pins and target network policy, JWT/API-token authentication, basic RBAC/audit, partial OTel/Prometheus, and API-local Run execution. The Control Manager currently logs a reconnect event rather than reconciling. The transactional outbox, controller workqueue, Secret Store, runtime configuration revisions, external key rotation, PG leases/fencing, durable Operation claims in CM, full application boundary, and Agent are not implemented. The current bootstrap requires NATS; current egress defaults are not production deny-by-default; the operator role includes Runs; Host create probes SSH synchronously; and some HTTP paths use stores directly. Implementation must preserve useful existing behavior while replacing these mismatches through explicit, tested migrations.

## 10. Deferred and rejected approaches

### Intentionally deferred

- NATS JetStream until accepted adoption criteria are met; Kafka until measured stream-processing scale/retention need.
- Agent runtime/protocol, enrollment CA, mTLS rollout, Agent gateway, heartbeat leases, Agent task execution.
- Multiple Control Manager replicas until DB claim leases, fencing, global limits and takeover testing pass.
- Kubernetes-specific manifests/operators; microservices; multi-region/active-active; SSH connection pooling; additional config scopes; external KMS/Vault integration.
- Large-scale partitioning, sharding, or capacity claims until load tests demonstrate need.

### Rejected

- Treat event delivery as reconciliation or Operation success; use event payload as authoritative state; assume exactly-once events or remote commands.
- Spawn unbounded workers per event; hold DB locks across SSH; retry remote side effects after ambiguous dispatch.
- Put SSH implementation in Host domain, accept arbitrary shell strings, trust unknown SSH host keys, or default production egress to broad private/public ranges.
- Put KEK beside ciphertext; return plaintext secrets; log secret values/command output; use authn as a substitute for object/workload authorization.
- Use SQLite over shared network storage for replicas; scale CM without leases/fencing; require NATS/Kubernetes for a local single-node installation; split to microservices without evidence.

## 11. Approval boundary

This baseline is the concrete proposal for Phase B. Approval will establish it as the implementation source of truth; each implementation milestone will still require its own scoped review and acceptance criteria. No source code, migrations, runtime configuration, deployment files, or tests were changed or run while preparing this baseline.
