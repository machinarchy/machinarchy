# ADR-053: Resolve cross-phase architecture conflicts and gate implementation

**Status:** Proposed — awaiting Phase 12 owner approval
**Date:** 2026-11-11

## Context

The approved phase designs are individually coherent but leave old text and current implementation behavior that conflict with later target decisions. The final blueprint must define one implementation target and distinguish it from the repository baseline.

## Decision

1. Amend ADR 007's broker timing: transactional outbox + local queue/poll is the initial path; PostgreSQL notifications are wake-ups; JetStream is optional under ADR 018/049 triggers.
2. Keep durable Operation ownership in the Control Manager as a subsystem separate from reconciliation. API persists and reports work. Until the worker claim protocol is implemented, the existing API-local RunManager is a transitional implementation and horizontal API scaling must not be treated as safe Operation scaling.
3. Persist valid Hosts as Pending before connectivity tests; reachability is asynchronous reconciliation. If immediate verification is required, expose a separately authorized bounded Operation.
4. Delete SSH-only inventory without waiting on an unreachable Host when no owned remote cleanup exists. Preserve audit/Operation snapshots and best-effort cancel in-flight work.
5. Treat API/storage boundary violations, missing outbox/leases, insecure current egress defaults, incomplete auth/CSRF/audit/key lifecycle, and process-local scaling limits as known implementation gaps. No production scale/security claim is allowed until the relevant acceptance criteria pass.

## Consequences

Early implementation increments must align APIs and persistence with this contract. Some behavior changes from the existing repository, notably Host create semantics and Run executor ownership, require explicit migration, compatibility, and client documentation. SQLite remains a single-node mode; PostgreSQL leases are prerequisite for multiple Control Managers. Agent, NATS, Kubernetes, and microservices remain deferred until stated triggers.

## Alternatives considered

- Treat each phase's prose as independent and let implementation choose: rejected because conflicting behavior would leak into API and schema.
- Keep current NATS/Run/Host-create behavior as target because it is implemented: rejected because current implementation does not satisfy approved recovery, scaling, or lifecycle requirements.
- Approve all scale/security claims from design alone: rejected; implementation and test evidence are required.
