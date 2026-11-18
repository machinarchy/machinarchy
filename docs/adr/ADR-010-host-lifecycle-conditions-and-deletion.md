# ADR 010: Separate Host lifecycle from observed health

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

The current Host status mixes pending, active, and disconnected concepts. Reconciliation needs to report health and configuration without treating a failed connection as deletion or loss of management.

## Proposed decision

Use a small Host lifecycle (`Pending`, `Active`, `Deleting`) and independent typed Conditions such as `Ready`, `Reachable`, and `Configured`. `ObservedGeneration` records the latest desired generation examined, including failed attempts. For SSH-only Hosts without remote cleanup obligations, deletion is immediate logical removal; retain an asynchronous `Deleting`/finalizer path only when a connector has a concrete cleanup requirement.

## Consequences

- Existing `ACTIVE`/`DISCONNECTED` status semantics must be mapped into conditions.
- Asynchronous registration is recommended but changes current synchronous registration behavior and requires explicit approval.
- Finalizers require bounded, idempotent, observable cleanup owners; they are not a generic default.
