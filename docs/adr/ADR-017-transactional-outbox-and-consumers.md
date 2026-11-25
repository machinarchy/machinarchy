# ADR 017: Persist events transactionally and process them at least once

- **Status:** Accepted on recommended defaults when Phase 5 was authorized
- **Date:** 2026-11-11

## Context

Publishing after a database write can lose notifications. Broker acknowledgements and consumer effects can be lost or duplicated around process crashes. Reconciliation can recover from current resource state, while some future integration consumers may need each distinct event.

## Proposed decision

Persist authoritative state and outbox facts in one SQLite/PostgreSQL transaction. Relay pending events at least once with bounded backoff. Resource notifications hand off stable keys to the coalescing queue and rely on startup/periodic resync. Durable DB consumers use an Inbox unique by consumer and event ID, committed atomically with consumer DB effects. Do not claim exactly-once external effects.

## Consequences

- Outbox events are an integration log, not event-sourced authoritative state or audit retention.
- Pending events do not expire; delivered and dead-letter retention is bounded and configurable.
- External side effects require their own idempotency/Operation model.
