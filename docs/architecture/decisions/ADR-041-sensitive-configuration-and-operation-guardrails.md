# ADR-041: Gate security-sensitive configuration and remote execution separately

**Status:** Accepted as the working design baseline when Phase 10 was authorized
**Date:** 2026-11-11

## Context

Runtime configuration can change SSH egress and command execution policy. Owners may also manage secrets and trust pins. Remote commands can have irreversible effects and uncertain outcomes.

## Decision

Keep execution disabled by default, separate `operations:execute` from inventory mutation, restrict target/resource scope, and require owner step-up for changing egress, enabling/widening execution, changing trust roots/pins, or rotating encryption/authentication keys. In production, require a second-person approval for widening egress or enabling unrestricted command policy. Persist revisions/audit before apply; each process reports applied policy version. Revalidate policy and target snapshot when workers execute. Do not automatically retry ambiguous remote commands.

## Consequences

High-risk changes have an approval workflow and may take longer to apply. An approved config revision is not evidence that every process applied it. A compromised Control Manager can still act within its granted scope, so workload isolation and egress controls remain required.

## Alternatives considered

- Let an owner immediately widen all egress and execution scope with a normal session: rejected for privileged production operation.
- Combine inventory write and execute permissions: rejected because discovery/maintenance and remote code execution have different blast radius.
- Assume SSH timeout means command did not run: rejected because request delivery and remote process completion are not atomic.
