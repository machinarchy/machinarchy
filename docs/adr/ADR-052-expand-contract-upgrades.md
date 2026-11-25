# ADR-052: Use expand-contract upgrades across database and Agent protocol

**Status:** Accepted as the working design baseline when Phase 12 was authorized
**Date:** 2026-11-11

## Context

Independent API and Control Manager versions may overlap during deployment, while future Agents can remain disconnected through a rollout. A destructive schema or protocol change can strand old processes or agents.

## Decision

Version database schema, API, event envelopes, runtime configuration, and Agent protocol independently. Use expand-migrate-contract changes and a tested compatibility window for rolling deployments. Run distributed migrations once through a controlled release step. Reject unsupported Agent capabilities and config schema explicitly; do not silently downgrade persisted state.

## Consequences

Some schema fields and event versions must remain supported across at least one rollout/rollback window. Compatibility tests and upgrade/restore exercises are required before production release. Destructive cleanup is a later release after all consumers have advanced.

## Alternatives considered

- Run destructive migrations and all process upgrades atomically: rejected because outages and rollback become harder.
- Let every replica perform all migrations independently: rejected because DDL races and lock contention can prevent startup.
- Assume all Agents are online during rollout: rejected because agents can be disconnected for extended periods.
