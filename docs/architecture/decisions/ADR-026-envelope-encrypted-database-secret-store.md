# ADR-026: Store secret versions with envelope encryption and external KEKs

**Status:** Accepted on the confirmed constraints and recommended defaults when Phase 7 was authorized
**Date:** 2026-11-11

## Context

Secrets must initially share the configured database, while the encryption root key must remain external. Current Host credential encryption derives a purpose-specific AES-GCM key directly from an installation secret and does not support generic versioned secrets or KEK rotation.

## Decision

Use a database-backed SecretProvider behind a small provider port. Generate a random DEK per secret version; encrypt secret bytes with AES-256-GCM and a fresh nonce; wrap each DEK with an externally provisioned KEK. Store ciphertext, nonce, wrapped DEK, wrap nonce, algorithm, purpose, and KEK identifier/version. Bind identity and purpose through authenticated associated data. Secret versions are immutable; rotation creates a version and changes a pointer. KEK rotation rewraps DEKs in batches and retains old keys until data and recovery copies no longer need them. Plaintext is never returned by normal management API responses or written to revisions/events/logs.

## Consequences

Database-only backups are insufficient; external keyring backup and restore are required. Key loss makes associated values unrecoverable. Legacy Host ciphertext needs a compatibility reader and explicit migration/re-encryption plan. Deleting a row cannot guarantee physical erasure from WAL, MVCC, or backups; cryptographic erasure and backup expiry define retirement.

## Alternatives considered

- Store plaintext in the database: rejected.
- Store the root key in the same database: rejected because database compromise would expose both ciphertext and decryption key.
- Require Vault or cloud KMS immediately: rejected by the approved initial infrastructure constraint; provider abstraction leaves that path open.
- Encrypt all secrets directly with one installation key: rejected for the target because it complicates version retirement and per-secret re-encryption/rotation.
