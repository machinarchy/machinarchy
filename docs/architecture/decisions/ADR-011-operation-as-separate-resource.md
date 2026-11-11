# ADR 011: Model imperative work as an Operation separate from Host

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

Host inventory is durable desired/observed state. Remote command runs are individual imperative requests with pinned targets, cancellation, per-target results, and uncertain outcomes after connection loss. Storing that history on Host would mix unrelated lifecycles and grow the Host aggregate without bound.

## Proposed decision

Keep each Operation as a separate workspace-owned resource with immutable accepted intent, actor attribution, fixed target IDs/snapshots, lifecycle state, cancellation, and per-target results. Use typed operation payloads; the existing command Run is the first kind. Do not put operation history or execution results on Host. Default command retry to none after uncertain delivery.

## Consequences

- Operation history survives Host deletion subject to retention policy.
- A common lifecycle can clarify worker ownership, but a generic dynamic operation schema is not justified.
- Existing Run records and API representations need a later compatibility/migration design.
