# ADR-019: Shared persistence contracts with engine-specific adapters

**Status:** Accepted on recommended defaults when Phase 6 was authorized  
**Date:** 2026-11-11

## Context

Machinarchy must support SQLite for a single-node deployment and PostgreSQL for future distributed API and Control Manager processes. Application behavior must remain consistent without exposing SQL dialects to business logic.

## Decision

Define minimal persistence ports by meaningful application capability, shared across engines. Application services own/use those ports. `internal/store` composes the selected engine; `internal/store/sqlite` and `internal/store/postgres` implement the same semantic contracts. A transaction boundary is expressed through intent-bearing methods or a narrow unit of work; do not leak `*sql.DB`, `*sql.Tx`, or dialect-specific repository contracts into application services.

## Consequences

The adapters may use different SQL and locking strategies while preserving observable lifecycle, concurrency, audit, and outbox guarantees. Store interfaces should be moved near application consumers when packages are reorganized, but Phase 5 does not require one interface per method or duplicated SQLite/PostgreSQL contracts. The current public `Stores.DB` is a boundary leak to remove in a later implementation phase.

## Alternatives considered

- Separate application repository interfaces per engine: rejected because callers should not branch on configured database.
- One generic CRUD repository: rejected because it hides transactional intent and encourages persistence-shaped business logic.
- Expose `database/sql` transactions to services: rejected because it couples application to infrastructure.
