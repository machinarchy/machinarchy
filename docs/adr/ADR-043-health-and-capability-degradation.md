# ADR-043: Separate process health from dependency and capability degradation

**Status:** Accepted as the working design baseline when Phase 11 was authorized
**Date:** 2026-11-11

## Context

Current readiness checks database, broker, and Control Manager subscription state. A remote Host failure should not cause the entire API/worker process to be restarted, and a future outbox changes whether broker availability is required for durable API writes.

## Decision

Liveness answers whether the process can make progress and is not stopping. Readiness answers whether that process can accept the work it advertises. Expose a private per-component degraded state for optional capabilities. A single Host/SSH/key-provider failure must not mark unrelated metadata APIs unready. Revisit API broker readiness when writes are backed by a transactional outbox; until then, reflect current synchronous event dependencies honestly.

## Consequences

Health responses stay minimal and non-sensitive. Alerting must use capability-specific signals in addition to readiness. Deployment probes must not restart a live process solely because the database, broker, or a Host is temporarily unavailable.

## Alternatives considered

- Use one health boolean for all dependencies: rejected because it creates restart loops and hides partial service availability.
- Probe every remote Host in readiness: rejected because fleet size and expected Host downtime make it noisy and expensive.
