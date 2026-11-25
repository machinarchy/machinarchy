# ADR-034: Standardize pagination, concurrency preconditions, and API errors

**Status:** Accepted; amended 2026-11-25 (Phase 21) to the implemented design
**Date:** 2026-11-11

## Context

List endpoints mixed unbounded arrays, `before_id` and offset paging; error shapes varied by handler; concurrent updates needed explicit conflict semantics. The first version of this ADR chose RFC 9457 problem details and ETag/`If-Match`. Phases 9–20 implemented a simpler design that covers the same needs, and the CLI and tests depend on it. AUDIT D-15 asked to finish the original decision or amend it. The owner chose to amend it.

## Decision

**Pagination.** Managed-resource lists (hosts, Access Profiles, Operations) use keyset pagination with stable ordering:
- `?limit=` (bounded by `resource.MaxPageSize`) and `?cursor=` (the previous page's `nextCursor`);
- responses are `{"items": [...], "nextCursor": "..."}`, and `nextCursor` is absent on the last page.

Small bounded lists (sessions, enrolment tokens, signing keys) return `{"items": [...]}`. The audit log keeps `before_id` and `limit` (newest first, maximum 500).

**Optimistic concurrency.** The expected revision travels in the request:
- `revision` in the body for label and spec updates;
- `?revision=` on deletes;
- `expectedRevision` for Access Profiles and the runtime configuration;
- `expectTargetsHash` binds an Operation to its preview.

A stale value is `409` with code `conflict`, or `targets_changed` for a changed target set. No ETag or `If-Match` headers.

**Errors.** Every error is `{"error": "<safe message>", "code": "<stable code>"}`. The request ID is in the `X-Request-Id` response header. The codes are:
- `invalid_request`, `unauthorized`, `permission_denied`, `not_found`;
- `conflict`, `targets_changed`;
- `execution_not_allowed`, `too_many_requests`, `unavailable`, `internal`.

Validation errors are `400`. An unexpected error is `500 internal` with no detail: its cause is logged with the request ID (AUDIT/20 F20-06). Internal errors and secrets are never exposed.

## Consequences

- Clients page large results consistently and retry conflicts deliberately; the CLI maps `code` and status to exit codes and hints.
- Clients that expect `application/problem+json` or ETags must use the fields above instead.
- RFC 9457 problem details and ETag/`If-Match` remain a possible later addition. They can be added alongside the current fields (problem details by content negotiation, ETag mirroring the revision) without breaking existing clients.

## Alternatives considered

- Unbounded arrays: rejected, list size grows without limit.
- Offset paging: rejected, expensive and unstable under concurrent inserts and deletes.
- Each handler inventing its own errors: rejected, clients need a reliable mapping.
- **Implement RFC 9457 and ETag now (the first version of this ADR):** deferred. It touches every handler, the CLI and the API docs for no capability the current design lacks.
