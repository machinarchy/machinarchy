# ADR-029: Require pinned SSH host keys and validate destinations at dial time

**Status:** Accepted on the recommended defaults when Phase 8 was authorized
**Date:** 2026-11-11

## Context

Host registration and later SSH actions handle credentials and can reach arbitrary network destinations. DNS and routing can change after registration.

## Decision

Use Go `x/crypto/ssh`, require an explicit SHA-256 host-key pin, and fail closed on absent/mismatched keys before sending credentials. Trust changes require explicit audited confirmation. Every outbound dial uses the destination policy: resolve once, validate all answers, dial the exact validated IP, deny metadata/unsafe special ranges, and apply deployment egress restrictions as defense in depth. Resolve versioned credentials from Secret Store only in the SSH adapter.

## Consequences

SSH-specific trust/auth/network details remain in the adapter. Registration-time validation is not sufficient; policy is re-applied for every dial. Host-key and secret rotation update profile versions and trigger subsequent actions/reconciliation. No automatic host-key acceptance or external SSH binary is permitted.

## Alternatives considered

- Trust on first use: rejected because a first-connection attacker could be pinned.
- Disable host-key checks: rejected because credentials could be sent to an impostor.
- Check DNS and then dial the hostname: rejected because rebinding could change the destination between check and connection.
