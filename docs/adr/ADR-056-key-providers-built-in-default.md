# ADR-056: One root of trust behind a key-provider interface, built-in by default

**Status:** Implemented (owner decision, 2026-11-19; AUDIT/10 Phase 16)

## Context

Three things were encrypted three ways:

- Secrets used envelope encryption (ADR-026) under a root key derived from `MACHINARCHY_ENCRYPTION_SECRET`, through an interface that returned raw root-key bytes.
- Operation data used one key derived from the same secret, with no key ID or AAD, so rotating the secret made old Operations unreadable (AUDIT S-07).
- The agent-gateway signing key was a separate `0600` file, and changing it meant re-pinning every agent during an outage (AUDIT M-3).

The owner plans to offer external KMSs (Vault, AWS, GCP, Azure) as choices, next to a default that needs no extra server.

## Decision

- **One root of trust:** a `secrets.KeyProvider` that only wraps and unwraps data keys (`Wrap`, `Unwrap`, `CurrentKeyID`, `Owns`, `Capabilities`). It never hands out root-key bytes, because no KMS does. Ed25519 signing is an optional capability.
- **`local` is the default** (`MACHINARCHY_KMS_PROVIDER=local`). Root keys are derived from `MACHINARCHY_ENCRYPTION_SECRET[_FILE]` and the keyring file. It stays supported.
- **Key IDs name their provider** (`local:<id>`). A `Keyring` routes `Unwrap` by owner, so two providers can be configured during a switch.
- **The Secret Store is the only home for sensitive values,** including the controller signing key (a system secret) and, through the same envelope, Operation data. Each value records its key ID and is bound to its row by AAD.
- **One rewrap** (`POST /api/v1/secrets/rewrap`) moves secrets and Operation data off an old key and reseals pre-Phase-16 Operation data.
- **The signing key rotates as owner actions** (stage → agents pin two keys → activate → retire). The agent protocol is unchanged (`SealWith`, `OpenAny`).
- **External providers are optional adapters** (AUDIT/10 phases K1–K4). Each implements the interface and passes `internal/secrets/providertest`; none adds Secret Store features.

## Consequences

- No new server is required. A KMS is a deployment choice, switched by a rewrap rather than a re-encryption.
- With `local`, the root key exists in the controller's environment and memory. A KMS moves it out, at the cost of a network call per wrap, unwrap and sign. Operation data uses one data key per value, so a KMS provider should cache unwrapped data keys or batch (to decide in its K-phase).
- An old key must be kept until database backup retention has passed: older backups hold data keys wrapped by it.
- `MACHINARCHY_ENCRYPTION_KEY_ID` is limited to 122 characters without `:`, so `local:<id>` fits `VARCHAR(128)`.
