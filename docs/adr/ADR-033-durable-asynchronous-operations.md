# ADR-033: Represent long-running imperative work as durable Operations

**Status:** Accepted as the working design baseline when Phase 9 was authorized
**Date:** 2026-11-11

## Context

Remote execution outlives an HTTP request and can have uncertain outcomes after cancellation, timeout, or process failure. A `202` response must provide a monitor for ongoing work.

## Decision

Submit an Operation through a durable transaction that stores the Operation, pinned targets, audit event, and outbox notification. Return `202` with `Location` only after commit and queue eligibility. Control Manager workers independently claim and execute work; clients poll the Operation resource. Cancellation records a request and remains nonterminal until workers report final state. Use idempotency keys for duplicate submissions; do not automatically retry ambiguous commands.

## Consequences

Request acceptance, persistence, scheduling, running, and completion are distinct observable milestones. API remains available for reads when workers are down, while Operations may queue. Existing `/runs` routes can remain as a compatibility surface during migration.

## Alternatives considered

- Hold an HTTP request open until remote execution finishes: rejected due to unbounded client/server connection lifetime.
- Return 202 before durable persistence: rejected because an API crash could lose accepted work.
- Treat cancel request as immediate cancelled result: rejected because remote command may continue.
