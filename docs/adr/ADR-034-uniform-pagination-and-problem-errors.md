# ADR-034: Standardize pagination, concurrency preconditions, and API errors

**Status:** Accepted as the working design baseline when Phase 9 was authorized
**Date:** 2026-11-11

## Context

Current list endpoints mix unbounded arrays, `before_id`, and offset paging; error response shapes vary by handler. Concurrent resource/config updates also need explicit conflict semantics.

## Decision

Use bounded cursor pagination with stable ordering and response envelopes for new/normalized list endpoints. Use resource revision ETags and `If-Match` for optimistic updates. Standardize errors on RFC 9457 problem details with stable error codes, request ID, and safe field errors; never expose internal errors or secrets. Preserve compatibility endpoints/fields until clients migrate.

## Consequences

Clients can paginate large result sets consistently and make conflict retries intentionally. CLI decodes machine-readable errors. Host full-list and job offset endpoints require a compatibility transition. Choose one validation status for v1; recommendation is `400` to align with current behavior.

## Alternatives considered

- Continue unbounded arrays: rejected because list size grows without limit.
- Offset paging everywhere: rejected as expensive and unstable under concurrent inserts/deletes.
- Each handler invents its own errors: rejected because clients need reliable mapping.
