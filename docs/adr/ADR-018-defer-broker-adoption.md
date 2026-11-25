# ADR 018: Defer NATS until durable cross-process messaging is justified

- **Status:** Accepted on recommended defaults when Phase 5 was authorized
- **Date:** 2026-11-11
- **Amends:** ADR 007's proposed NATS adoption timing, if accepted

## Context

ADR 007 proposed NATS JetStream for distributed notifications. The current project has not yet demonstrated a messaging scale or recovery need, and PostgreSQL plus the transactional outbox can support independent processes with polling and optional wake-up notifications.

## Proposed decision

Do not introduce NATS or Kafka in the initial event architecture. Use the SQLite/PostgreSQL transactional outbox, an in-process queue, PostgreSQL LISTEN/NOTIFY only as a wake-up, and polling/resync as correctness paths. Evaluate NATS JetStream when independently deployed components need durable fan-out/replay, Agent delivery, or measured PostgreSQL polling limits. Evaluate Kafka only when measured stream throughput/retention/replay and consumer-group requirements justify its operational cost.

## Consequences

- PostgreSQL notifications never replace durable outbox reads because listeners can disconnect and notifications can coalesce.
- If NATS is later added, consumers remain at-least-once and idempotent; broker retention does not replace database state or recovery scans.
- Accepting this ADR explicitly amends the broker timing in ADR 007 without changing its database-source-of-truth decision.
