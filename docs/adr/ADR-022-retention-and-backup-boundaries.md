# ADR-022: Separate operational retention from audit and backup policy

**Status:** Accepted on recommended defaults when Phase 6 was authorized  
**Date:** 2026-11-11

## Context

Outbox delivery, consumer deduplication, Operations/results, metrics, and append-only audit data have different replay, diagnostic, privacy, and compliance needs. SQLite WAL files and external encryption keys are part of reliable backup planning.

## Decision

Use independent, configurable retention policies. Carry forward Phase 4 defaults of 7 days for delivered outbox records and 30 days for quarantined records; never automatically expire pending delivery. Keep inbox records for at least the maximum replay horizon. Do not automatically prune audit history until verified archival/export is designed. Propose 30 days for detailed Operation output and metrics, with compact Operation/audit history retained separately. Backups must capture a consistent SQLite database state or use the selected PostgreSQL backup/PITR mechanism and must include a separately protected matching encryption-key version set. Validate restoration.

## Consequences

Retention windows must be configurable and pruned in bounded batches. Exact Operation and metrics horizons remain owner decisions. The audit hash chain detects edits/removals within the retained chain but does not replace external backup or a separately recorded chain head.

## Alternatives considered

- One retention period for all tables: rejected because event replay, audit, metrics, and command output have different requirements.
- Prune audit with outbox: rejected because it destroys audit history and its chain guarantees.
- Treat successful backup creation as proof of recoverability: rejected; restoration must be exercised.
