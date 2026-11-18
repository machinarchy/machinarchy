# ADR-040: Anchor application audit evidence outside the application database

**Status:** Accepted as the working design baseline when Phase 10 was authorized
**Date:** 2026-11-11

## Context

The current audit trail is append-only and hash-chained inside the same database. A database administrator can disable triggers, recompute the chain, or truncate its newest tail without detection from the database alone.

## Decision

Continue transactionally recording state-changing audit events in the application database, then export periodic sequence/head hashes and security events to an independently administered append-only/WORM destination. Include authentication denials, authorization decisions, secret lifecycle/access metadata, security configuration changes, Host trust changes, Operation execution, and privileged database/control-plane actions. Never include secret values, ciphertext, argv, or output.

## Consequences

External audit becomes a required deployment dependency or a clearly observable asynchronous durability window. Define behavior when export is unavailable, retention/rotation, clock integrity, alerting, and restore verification. Existing internal chain verification remains useful for corruption diagnosis but is not treated as administrator-proof.

## Alternatives considered

- Trust database triggers alone: rejected because database administrators can alter schema/data.
- Write only to application logs: rejected because logs may be incomplete, mutable, or contain secret data.
- Emit audit only after commit without an outbox: rejected because a crash can lose the audit event after state change.
