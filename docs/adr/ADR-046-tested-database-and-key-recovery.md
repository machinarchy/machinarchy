# ADR-046: Recover the database and external encryption keys as one tested system

**Status:** Accepted as the working design baseline when Phase 11 was authorized
**Date:** 2026-11-11

## Context

SQLite/PostgreSQL hold authoritative desired state, secret ciphertext, Operations, audit, and eventually outbox data. Secret decryption additionally requires external key material. A database-only backup can be unusable, while key material stored only with the database defeats separation.

## Decision

Back up database state and external keyring through separately controlled procedures with compatible recovery points. Restore tests must validate schema, audit chain/anchor, secret decryption using a canary, outbox replay, Operation uncertainty, and worker re-reconciliation. Publish RPO/RTO per deployment mode and alert on backup and restore-test age.

## Consequences

Key loss remains unrecoverable for ciphertext protected by that key. Restore operators need dual access paths and runbooks. SQLite backup procedures must account for WAL; PostgreSQL recovery should use snapshots and WAL/PITR where configured. Broker state is not the source of truth.

## Alternatives considered

- Back up only the database file: rejected because WAL may contain committed data and external keys are absent.
- Store the KEK in the database backup: rejected because database compromise would include both ciphertext and root key.
- Assume provider backups work without restore tests: rejected because they do not establish application-level recoverability.
