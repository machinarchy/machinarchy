# Architecture decision records

All ADRs live in this directory; the two earlier directories (`docs/DECISIONS`, `docs/architecture/decisions`) were merged here. "Accepted" means the decision stands as written; this audit did not check every one of those against the code. Where the code differs, the status and note say how.

| Decision | Status | Note |
|---|---|---|
| [ADR 0001: Keep a modular monolith and disable production self-registration by default](0001-modular-monolith-and-production-signup.md) | Accepted |  |
| [ADR 0002: Workspace-scoped authorization and API tokens](0002-workspace-rbac-and-api-tokens.md) | Accepted |  |
| [ADR 0003: One shared workspace, a configured bootstrap owner, and owner-managed roles](0003-shared-workspace-bootstrap-owner-and-member-roles.md) | Accepted |  |
| [ADR 0004: Command execution as pinned, audited, structured runs](0004-command-execution-model.md) | Superseded | Legacy runs were removed; commands run as managed Operations (ADR-033, ADR-055). |
| [ADR 005: Keep a modular monolith with independently executable roles](ADR-005-modular-runtime-boundaries.md) | Accepted |  |
| [ADR 006: Separate resource reconciliation from durable operations](ADR-006-control-manager-and-operation-ownership.md) | Accepted |  |
| [ADR 007: Keep database state authoritative and use events as wake-ups](ADR-007-persistence-and-event-notification.md) | Accepted |  |
| [ADR 008: Separate bootstrap settings, runtime configuration, and secrets](ADR-008-bootstrap-runtime-configuration-and-secrets.md) | Accepted |  |
| [ADR 009: Model Host as a connector-neutral managed resource](ADR-009-host-resource-identity-and-state.md) | Accepted |  |
| [ADR 010: Separate Host lifecycle from observed health](ADR-010-host-lifecycle-conditions-and-deletion.md) | Accepted |  |
| [ADR 011: Model imperative work as an Operation separate from Host](ADR-011-operation-as-separate-resource.md) | Accepted |  |
| [ADR 012: Use generation-aware resource concurrency and fact events](ADR-012-domain-events-and-resource-concurrency.md) | Accepted |  |
| [ADR 013: Use one level-triggered, edge-driven reconciliation pipeline](ADR-013-hybrid-control-loop.md) | Accepted |  |
| [ADR 014: Schedule reconciliation through a bounded resource-key workqueue](ADR-014-bounded-resource-workqueue.md) | Accepted |  |
| [ADR 015: Restrict SQLite to one manager and coordinate PostgreSQL replicas with leases](ADR-015-controller-replica-coordination.md) | Accepted |  |
| [ADR 016: Use versioned fact events with stable identity and correlation](ADR-016-versioned-event-envelope.md) | Accepted |  |
| [ADR 017: Persist events transactionally and process them at least once](ADR-017-transactional-outbox-and-consumers.md) | Accepted |  |
| [ADR 018: Defer NATS until durable cross-process messaging is justified](ADR-018-defer-broker-adoption.md) | Superseded | The legacy NATS broker was removed (AUDIT A-04/S-05); NATS returns only with the agent gateway (ADR-049, ADR-050). |
| [ADR-019: Shared persistence contracts with engine-specific adapters](ADR-019-shared-persistence-contracts.md) | Accepted |  |
| [ADR-020: Logical schema parity and forward-only migrations](ADR-020-schema-and-migration-evolution.md) | Accepted |  |
| [ADR-021: Atomic state changes and optimistic concurrency](ADR-021-transaction-and-concurrency-semantics.md) | Accepted |  |
| [ADR-022: Separate operational retention from audit and backup policy](ADR-022-retention-and-backup-boundaries.md) | Accepted |  |
| [ADR-023: Separate immutable bootstrap configuration from runtime settings](ADR-023-bootstrap-configuration-precedence.md) | Accepted |  |
| [ADR-024: Use immutable, global runtime configuration revisions](ADR-024-global-immutable-runtime-revisions.md) | Accepted |  |
| [ADR-025: Propagate revisions as hints and atomically publish process snapshots](ADR-025-configuration-snapshot-propagation.md) | Accepted |  |
| [ADR-026: Store secret versions with envelope encryption and external KEKs](ADR-026-envelope-encrypted-database-secret-store.md) | Accepted |  |
| [ADR-027: Separate secret management from configuration and prohibit read-back](ADR-027-secret-management-and-nondisclosure.md) | Accepted |  |
| [ADR-028: Keep Host transport-neutral and expose connector capabilities](ADR-028-capability-oriented-host-connectors.md) | Accepted |  |
| [ADR-029: Require pinned SSH host keys and validate destinations at dial time](ADR-029-ssh-trust-and-network-policy.md) | Accepted |  |
| [ADR-030: Execute structured commands through bounded, non-retrying Operations](ADR-030-bounded-operation-execution.md) | Accepted |  |
| [ADR-031: Integrate a future Agent as a capability adapter](ADR-031-agent-as-capability-adapter.md) | Accepted, not implemented | Agent integration is pending (AUDIT/04 M-1..M-11). |
| [ADR-032: Keep a versioned transport boundary over application commands and queries](ADR-032-versioned-api-and-application-boundary.md) | Accepted |  |
| [ADR-033: Represent long-running imperative work as durable Operations](ADR-033-durable-asynchronous-operations.md) | Implemented | Managed Operations with a durable worker, cancellation and non-replaying recovery. |
| [ADR-034: Standardize pagination, concurrency preconditions, and API errors](ADR-034-uniform-pagination-and-problem-errors.md) | Implemented (amended 2026-11-25) | Keyset pagination `{items, nextCursor}`; revisions in the request for optimistic concurrency; `{error, code}` errors. RFC 9457 and ETag deferred. |
| [ADR-035: Keep CLI commands thin and use the HTTP API for all server operations](ADR-035-cli-as-api-client.md) | Implemented | Enforced by TestCLIStaysAnAPIClient. |
| [ADR-036: Treat authentication, authorization, auditing, and secret protection as independent controls](ADR-036-separate-security-controls.md) | Accepted |  |
| [ADR-037: Deny outbound Host access by default in production](ADR-037-production-egress-deny-by-default.md) | Accepted |  |
| [ADR-038: Authorize secret resolution separately and bind it to workload purpose](ADR-038-purpose-bound-secret-resolution.md) | Accepted |  |
| [ADR-039: Give API, Control Manager, broker clients, and Agents distinct workload identities](ADR-039-workload-and-agent-identity.md) | Accepted, not implemented | Agent identity lives in machinarchy-agent (protocol/agentv1); the controller side is pending. |
| [ADR-040: Anchor application audit evidence outside the application database](ADR-040-externally-anchored-security-audit.md) | Accepted |  |
| [ADR-041: Gate security-sensitive configuration and remote execution separately](ADR-041-sensitive-configuration-and-operation-guardrails.md) | Accepted |  |
| [ADR-042: Use OpenTelemetry APIs for application observability](ADR-042-opentelemetry-instrumentation.md) | Accepted |  |
| [ADR-043: Separate process health from dependency and capability degradation](ADR-043-health-and-capability-degradation.md) | Accepted |  |
| [ADR-044: Keep telemetry bounded-cardinality and secret-safe](ADR-044-bounded-secret-safe-telemetry.md) | Accepted |  |
| [ADR-045: Coordinate process shutdown and bounded worker draining](ADR-045-coordinated-shutdown-and-draining.md) | Implemented | One shutdown budget, MACHINARCHY_SHUTDOWN_TIMEOUT (AUDIT Q-01). |
| [ADR-046: Recover the database and external encryption keys as one tested system](ADR-046-tested-database-and-key-recovery.md) | Accepted |  |
| [ADR-047: Keep SQLite single-node and use PostgreSQL for distributed coordination](ADR-047-sqlite-single-node-postgres-distributed.md) | Partially implemented | SQLite is single-node by documentation; nothing prevents two managers on one SQLite file (AUDIT A-11). |
| [ADR-048: Require durable leases and fencing before Control Manager replicas](ADR-048-database-backed-worker-leases.md) | Implemented | Fenced PostgreSQL leases for Operations and Host reconciles. |
| [ADR-049: Scale bounded workers first and adopt brokers only on measured need](ADR-049-scale-and-broker-adoption-on-evidence.md) | Accepted |  |
| [ADR-050: Use outbound per-Agent identity and durable leases](ADR-050-agent-outbound-identity-and-leases.md) | Accepted, not implemented | See machinarchy-agent and AUDIT/04. |
| [ADR-051: Keep Kubernetes optional](ADR-051-kubernetes-optional.md) | Accepted |  |
| [ADR-052: Use expand-contract upgrades across database and Agent protocol](ADR-052-expand-contract-upgrades.md) | Deferred | Applies after the first release with real data; during development the schema is one squashed baseline (ADR-055). |
| [ADR-053: Resolve cross-phase architecture conflicts and gate implementation](ADR-053-integrated-architecture-review.md) | Proposed |  |
| [ADR-054: Workspace-scoped Access Profiles and immutable secret bindings](ADR-054-workspace-access-profiles-and-secret-bindings.md) | Implemented | Access Profiles with immutable revisions and version-pinned SecretRefs; owner-only binding (AUDIT S-02). |
| [ADR-055: The managed resource model is the only host model; the schema is one baseline](ADR-055-managed-model-only-and-schema-baseline.md) | Implemented |  |
| [ADR-056: One root of trust behind a key-provider interface, built-in by default](ADR-056-key-providers-built-in-default.md) | Implemented | `local` provider; KMS providers optional (AUDIT/10 K1–K4); signing key and Operation data in the same envelope. |
| [ADR-057: Agent capabilities run as Operations](ADR-057-capability-operations.md) | Accepted | Phase 17: `OperationKind=Capability`, `agent` connector, per-host enrolment binding, durable dispatch and results; `invoke` removed. |
| [ADR-058: Keep Echo; track its fixes instead of migrating](ADR-058-keep-echo.md) | Accepted | D-14: Echo v4.16.6 fixes GO-2026-6875 and GO-2026-6872; no migration to `net/http`. |
| [ADR-059: Agents reach the controller only through the agent gateway](ADR-059-agent-gateway-transport.md) | Accepted | Gateway-only agent transport (gRPC), internal swappable `Bus`; no broker address or credential exposed. |
| [ADR-060: Member onboarding: email invitations and owner-added users, no temporary passwords](ADR-060-member-onboarding.md) | Accepted (implemented) | Single-use hashed onboarding tokens; optional SMTP; no temporary passwords. |
| [ADR-061: Audit retention: keep by default, archive then prune behind a signed checkpoint](ADR-061-audit-retention-and-checkpoints.md) | Proposed | Owner-only explicit prune after verified export; checkpoint anchors `verify`. API shape needs approval. |
