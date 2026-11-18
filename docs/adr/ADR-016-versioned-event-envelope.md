# ADR 016: Use versioned fact events with stable identity and correlation

- **Status:** Accepted on recommended defaults when Phase 5 was authorized
- **Date:** 2026-11-11

## Context

Machinarchy needs internal facts, resource-change notifications, and future cross-process contracts without treating messages as current state. Consumers need to identify duplicates, correlate workflows, and evolve schemas safely.

## Proposed decision

Distinguish commands, domain events, integration events, and coalescible reconciliation notifications. Integration events use a stable lowercase namespaced fact type, immutable event ID, per-type schema version, occurred time, producer, workspace/resource subject, optional aggregate sequence, correlation/causation IDs, optional actor, and small typed payload. Resource notifications carry keys/revision hints, never authoritative snapshots. Events exclude credentials, command arguments, and raw remote output.

## Consequences

- Additive optional fields are compatible; breaking changes get a new schema version and an explicit compatibility window.
- Consumers reload resource state and tolerate duplicate/out-of-order events.
- Agent commands remain Operation deliveries, not domain events.
