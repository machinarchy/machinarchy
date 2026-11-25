# ADR-024: Use immutable, global runtime configuration revisions

**Status:** Accepted on the confirmed constraints and recommended defaults when Phase 7 was authorized
**Date:** 2026-11-11

## Context

API and CLI need validated updates, history, optimistic concurrency, and rollback while all processes share SQLite or PostgreSQL.

## Decision

Persist one strongly typed global runtime configuration document per immutable revision. Keep one global active-head row; revisions include schema version, checksum, actor, timestamp, and secret references. Updates validate a complete candidate and compare-and-swap the expected revision. The revision, head advance, audit entry, and outbox notification commit in one transaction. Rollback copies a prior effective document into a new revision. Secret values never enter configuration documents.

## Consequences

History is stable and rollback is auditable. All runtimes observe the same desired revision but can report different applied revisions. Runtime configuration cannot create additional scope until a future explicit product decision. Schema evolution needs versioned decoding and compatibility policy.

## Alternatives considered

- Mutable single configuration row: rejected because it loses history and makes rollback/audit ambiguous.
- Event-sourced configuration: rejected because immutable snapshots plus an audit/outbox are sufficient without replaying every patch.
- Arbitrary map of settings: rejected because validation, classification, and safe application require typed fields.
