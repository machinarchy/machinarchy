# ADR 009: Model Host as a connector-neutral managed resource

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

The current Host combines identity, SSH endpoint, credentials, connection method, and a single operational status. This couples the aggregate to SSH and conflates lifecycle with reachability. Agent connectivity is a future requirement.

## Proposed decision

Give Host an immutable opaque Machinarchy ID, workspace ownership, mutable workspace-unique name/labels, desired spec, observed status, and a reference to a connector access profile. Keep connector-specific configuration and secrets outside Host. Represent reachability/configuration through Conditions, not a Host lifecycle status. Preserve distinct desired Generation and resource Revision semantics.

## Consequences

- Renames and endpoint/profile changes do not alter resource identity.
- Existing Host JSON and persistence shape will need a later migration/API compatibility plan.
- AccessProfile ownership and sharing remain an open product decision.
