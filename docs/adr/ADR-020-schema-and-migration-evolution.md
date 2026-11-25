# ADR-020: Logical schema parity and forward-only migrations

**Status:** Accepted on recommended defaults when Phase 6 was authorized  
**Date:** 2026-11-11

## Context

SQLite and PostgreSQL have separate SQL migration sets and different capabilities. The target data model adds Host generations/revisions, Operations, runtime configuration revisions, outbox deliveries, and inbox deduplication.

## Decision

Maintain one logical schema contract with engine-specific numbered forward migrations. Released migrations are immutable. Every material schema change has a higher-numbered migration for both engines, data preflight where necessary, and a parity review of columns, constraints, indexes, and defaults. Use expand/backfill/contract for rolling PostgreSQL changes. Production PostgreSQL migrations run once under a migration lock or controlled deploy step; development may migrate on startup. Keep core identity, tenancy, lifecycle, uniqueness, and query columns relational; use versioned JSON only for bounded extension/spec/event payloads.

## Consequences

Database-specific indexes and JSON behavior are allowed where semantics stay equivalent. SQLite rebuild migrations require careful bounded maintenance. There is no general rollback assumption; backup/restore and forward repair are the recovery path. The current migrator lacks an explicit cross-process migration lock, which must be addressed before multiple replicas may start against an unmigrated schema.

## Alternatives considered

- One shared SQL file for both engines: rejected because DDL and indexing capabilities differ.
- Editing old migrations in place: rejected because already-deployed databases would diverge.
- Every application replica migrates independently: rejected due to concurrent startup races and difficult deployment ordering.
