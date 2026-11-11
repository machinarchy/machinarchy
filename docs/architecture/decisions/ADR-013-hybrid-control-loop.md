# ADR 013: Use one level-triggered, edge-driven reconciliation pipeline

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

The current Control Manager subscribes to one event and logs it; it does not reconcile. Events can be duplicated, delayed, reordered, or missed. Persistent resource state is authoritative.

## Proposed decision

Both edge notifications and startup/periodic scans enqueue the same stable resource key. Controllers load the latest committed state, compare desired and observed generations, perform an idempotent bounded step, and conditionally write status. Events only trigger work; they do not carry authoritative state.

## Consequences

- Startup and periodic resync are required correctness paths, not optional monitoring.
- Event ingress remains small and contains no reconciliation business logic.
- A local queue may lose admitted keys on process crash; persistent state and resync recover them.
