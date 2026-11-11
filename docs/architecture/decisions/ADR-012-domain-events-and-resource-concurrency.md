# ADR 012: Use generation-aware resource concurrency and fact events

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

Controllers may process stale notifications while desired state changes concurrently. A single revision is insufficient to express desired-state versions, read freshness, and observed progress. Events must tolerate duplicate, delayed, or missed delivery.

## Proposed decision

Increment Host Generation only for desired spec changes. Maintain a resource Revision for accepted state changes. Require expected-generation preconditions for spec writes; controllers conditionally update status only for the generation they observed. `ObservedGeneration` records processing, while Conditions carry success/failure. Domain events are immutable facts keyed by stable resource ID and generation/revision; consumers reload current state before acting.

## Consequences

- Stale controller results cannot overwrite newer desired state.
- Event delivery is a scheduling optimization; startup/periodic reconciliation remains necessary.
- Persistence and outbox mechanisms are specified in later architecture phases, not embedded in domain types.
