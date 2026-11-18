# ADR 015: Restrict SQLite to one manager and coordinate PostgreSQL replicas with leases

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

The current SQLite adapter uses one open connection and targets single-node operation. Multiple Controller Manager replicas need per-resource exclusion, while an in-process queue only coordinates workers in one process.

## Proposed decision

Support one Control Manager instance in the SQLite deployment profile. For PostgreSQL multi-replica operation, use short database-backed per-key leases with expiry and fencing tokens, plus generation-conditional status writes. Never hold database locks during remote calls. Use a shared durable notification consumer and leader-elected or deterministic-shard resync; retain periodic scans on failover.

## Consequences

- Lease loss cancels work and old fencing tokens cannot write status.
- Leases prevent concurrent local control loops but cannot guarantee exactly-once remote side effects; reconciliation must be idempotent.
- Multi-replica support is not claimed until lease expiry, fencing, crash recovery, and DB failure cases are validated.
