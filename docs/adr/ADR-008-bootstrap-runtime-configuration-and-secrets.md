# ADR 008: Separate bootstrap settings, runtime configuration, and secrets

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

Server bootstrap settings currently come from environment variables. SSH credentials and run arguments are encrypted in the selected database using a derived key, but there is no runtime configuration revision store, Secret Store port, key rotation flow, or envelope encryption.

## Proposed decision

Keep immutable process-start settings in `internal/config` and composition in `internal/bootstrap`. Treat runtime configuration as a separate typed, versioned application capability with validation and optimistic updates. Treat secret values as a separate Secret Store capability, referenced by resources rather than embedded in ordinary configuration. The initial secret adapter may use encrypted database storage, with its key-encryption key supplied by an external protected source. Never store that key beside the encrypted data.

## Consequences

- Settings must be classified as startup-only, hot-reloadable, or reconciliation-triggered; do not promise universal hot reload.
- Rotation, version history, authorization, auditing, and last-known-good configuration behavior require detailed design in later phases.
- This ADR does not claim the runtime configuration or Secret Store exists today.
