# ADR 014: Schedule reconciliation through a bounded resource-key workqueue

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

Unbounded goroutines per event risk memory, connection, and database exhaustion. Duplicate events and repeated updates to one resource should collapse without concurrent same-key reconciliation.

## Proposed decision

Use a bounded coalescing queue keyed by resource kind, workspace, and stable ID. Track dirty and processing keys so duplicate adds coalesce and updates during processing cause one later pass. Use fixed worker pools, per-key exponential backoff with jitter, a global token bucket, bounded delayed requeues, and admission backpressure. Do not drop keys after a finite retry count.

## Consequences

- Worker counts and queue limits require operational metrics and configuration.
- Broker messages are acknowledged after queue admission; periodic resync recovers an admitted key lost with process memory.
- Long-running imperative Operations use separate queues and workers.
