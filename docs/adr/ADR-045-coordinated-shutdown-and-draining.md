# ADR-045: Coordinate process shutdown and bounded worker draining

**Status:** Accepted as the working design baseline when Phase 11 was authorized
**Date:** 2026-11-11

## Context

API, Control Manager, runtime, run executor, broker, database, and telemetry currently have separate shutdown paths and timeouts. A long remote command may outlive the HTTP shutdown budget, and its completion may be uncertain after cancellation.

## Decision

One process lifecycle coordinator marks readiness false, stops admission/claims, drains requests and workers in order, records interrupted or uncertain Operations, closes broker/database after users stop, and flushes telemetry within one total deadline. Never wait without a bound or automatically replay a command whose remote dispatch outcome is unknown.

## Consequences

The deployment termination grace period must exceed the configured drain budget. Worker types need explicit stop-claim, drain, cancellation, and persistence semantics. Shutdown timeout becomes a measurable operational signal.

## Alternatives considered

- Let each subsystem use unrelated timeouts: rejected because cleanup can exceed orchestrator termination grace.
- Cancel every worker immediately: rejected because durable status and safe reconciliation work can be lost.
- Wait indefinitely for all remote work: rejected because remote execution and process shutdown are bounded by deployment deadlines.
