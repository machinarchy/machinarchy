# ADR-030: Execute structured commands through bounded, non-retrying Operations

**Status:** Accepted on the recommended defaults when Phase 8 was authorized
**Date:** 2026-11-11

## Context

Imperative remote commands have side effects and may continue after local connection loss. Unbounded concurrency/output can exhaust the control plane or database.

## Decision

Commands are structured argv, validated and safely quoted for SSH exec; no raw-shell API is provided. Operation workers pin target identity/profile/trust revisions, check execution authorization and policy, use bounded workers and a shared process SSH bulkhead, enforce an end-to-end deadline, capture bounded stdout/stderr, sanitize output, and persist typed outcomes. Cancellation closes local I/O but does not assert that remote work stopped. Do not automatically retry an Operation after an ambiguous dispatch.

## Consequences

Non-zero exit is distinct from network failure. Timeout/cancel after exec may be uncertain. Output can contain secrets despite sanitization; restrict read access, encrypt at rest, and retain for a bounded period. Current plaintext Run output is a known security debt.

## Alternatives considered

- Shell command strings: rejected because callers could inject shell syntax.
- Infinite output capture: rejected due to memory/storage exhaustion.
- Automatic retries for every failure: rejected because the command may already have executed.
- SSH connection pooling initially: rejected because credential, pin, cancellation, and invalidation lifecycles are not yet justified by measured need.
