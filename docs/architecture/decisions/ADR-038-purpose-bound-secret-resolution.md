# ADR-038: Authorize secret resolution separately and bind it to workload purpose

**Status:** Accepted as the working design baseline when Phase 10 was authorized
**Date:** 2026-11-11

## Context

Phase 6 places encrypted secret versions in the shared database. Application or database access alone must not automatically permit every caller to decrypt every secret.

## Decision

Use external KEKs and per-version DEKs. Keep management, reference assignment, and plaintext resolution as separate permissions. Only an authenticated runtime workload may resolve an immutable secret version for an explicitly authorized Host/profile/Operation purpose. No normal HTTP read endpoint returns plaintext. Audit secret resolution metadata without value/ciphertext. Encrypt sensitive Operation arguments/results separately and restrict their read permissions.

## Consequences

The SecretProvider must accept caller/purpose/resource context and validate it, not just a SecretRef. API and Control Manager should use separate workload identities/DB roles where feasible. Key loss requires restore from an external keyring; deletion is cryptographic erasure only after reference and backup-retention checks.

## Alternatives considered

- Let any API/worker with database access resolve any secret: rejected as a confused-deputy path.
- Return plaintext from secret detail endpoints: rejected.
- Store KEK beside ciphertext: rejected because database compromise would reveal both.
