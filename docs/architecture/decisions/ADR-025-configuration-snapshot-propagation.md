# ADR-025: Propagate revisions as hints and atomically publish process snapshots

**Status:** Accepted on the confirmed constraints and recommended defaults when Phase 7 was authorized
**Date:** 2026-11-11

## Context

SQLite and PostgreSQL have different notification options, and no notification channel alone provides the durable current configuration or applied state. Components must not observe partially applied configuration.

## Decision

Persist the full active revision and emit an outbox event transactionally. Each process combines edge-driven wake-up with periodic head-version checks, loads and validates the complete latest revision, prepares all affected components, and publishes one immutable snapshot atomically. Record per-process applied and attempted revisions. A failed prepare retains the last known good snapshot and reports an error; new processes must load the current valid revision before readiness. PostgreSQL NOTIFY is optional wake-up only; SQLite uses polling/local signals.

## Consequences

Events can be duplicated, missed, or out of order without losing configuration. Settings that cannot be safely staged are restart-required. In-flight work uses one captured snapshot; it is not retroactively rewritten. PostgreSQL LISTEN setup must inspect the database after subscribing to close the startup race.

## Alternatives considered

- Push the full config document in event payloads: rejected because events are not authoritative and may leak secrets or become oversized.
- Require notifications for correctness: rejected because SQLite has no shared durable notifier and broker outages must not prevent eventual convergence.
- Mutate component fields independently: rejected because readers could observe a mixed revision.
