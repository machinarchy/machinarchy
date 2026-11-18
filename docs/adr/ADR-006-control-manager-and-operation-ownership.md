# ADR 006: Separate resource reconciliation from durable operations

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

The current Control Manager logs one event type and does not reconcile. Command runs are durable records, but their executor runs in the API process that accepted each run. Continuous convergence and one-off operations have different lifecycle and retry semantics.

## Proposed decision

The Control Manager runtime owns controller lifecycle, startup recovery, periodic resynchronization, and bounded reconciliation. Controllers enqueue stable resource IDs, load the latest committed state, and apply idempotent transitions. Operation execution remains a separate subsystem with durable identity, status, cancellation, bounded workers, and recovery. The recommended target is for that subsystem to run under the Control Manager runtime while remaining separate from controllers; the API validates, persists, and returns operation requests.

## Consequences

- Moving execution out of API processes requires a reliable claim/lease protocol, duplicate-safe recovery, and explicit cancellation semantics before enabling multiple workers.
- Database state remains authoritative; event payloads only schedule work.
- The existing API-process executor is an implementation baseline, not the desired ownership boundary if the owner approves this ADR.
