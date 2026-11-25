# ADR 007: Keep database state authoritative and use events as wake-ups

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

SQLite and PostgreSQL adapters already exist. Host persistence and NATS publication are separate operations, so notifications can be lost. The current API also requires NATS at startup even when running alone.

## Decision (broker timing amended by ADR-018)

Persistent state is authoritative. Persist business-significant events in a transactional outbox with the state change. Use in-process wake-ups and periodic polling in all-in-one SQLite mode. In PostgreSQL mode, use durable DB delivery rows/polling and optional `LISTEN/NOTIFY` wake-ups first. NATS JetStream is optional and may be adopted only when ADR-018/ADR-049 criteria are met; consumers remain duplicate-safe and reload current state before acting. Do not introduce Kafka or a general-purpose broker queue without a demonstrated requirement.

## Consequences

- Outbox dispatch, retry, retention, and deduplication semantics need specification before implementation.
- NATS is not required for single-node or initial distributed mode; if adopted, it is an optional notification adapter, not source of truth.
- SQLite remains a single-node deployment mode; it is not the shared multi-replica coordination backend.
