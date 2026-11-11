# ADR-048: Require durable leases and fencing before Control Manager replicas

**Status:** Accepted as the working design baseline when Phase 12 was authorized
**Date:** 2026-11-11

## Context

In-memory workqueues recover through resync but do not coordinate workers across processes. Duplicate reconciliation may be harmless only when controllers are idempotent; stale workers must not overwrite newer desired state or perform unsafe duplicate Operations.

## Decision

Before running multiple Control Manager replicas, add a PostgreSQL-backed per-resource claim/lease with monotonically increasing fencing epoch, generation/revision compare-and-set, expiry/renewal, bounded retries, and startup recovery. Keep transactions short; never hold a row lock during network I/O. Use local coalescing queues as acceleration, with DB state authoritative.

## Consequences

Coordination adds schema and failure modes, which must be tested under crash, pause, network partition, DB failover, and lease expiry. It does not promise exactly-once SSH side effects. SQLite remains one Control Manager instance.

## Alternatives considered

- Multiple managers with only NATS durable consumers: rejected because delivery ownership does not fence resource writes or remote side effects.
- Advisory locks held during SSH: rejected because long network calls hold locks and fail awkwardly.
- Assume duplicate work never happens: rejected because crashes and lease expiry necessarily permit retries.
