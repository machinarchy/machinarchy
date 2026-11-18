# ADR-050: Use outbound per-Agent identity and durable leases

**Status:** Accepted as the working design baseline when Phase 12 was authorized
**Date:** 2026-11-11

## Context

Agents may be useful for Hosts behind NAT or where inbound SSH is prohibited. A shared token or inbound agent listener would create broad identity and network risk; the current Agent command is a placeholder.

## Decision

Future Agents initiate outbound mutually authenticated TLS connections with unique short-lived identities bound to one Host/workspace and explicit capabilities. Use one-time enrollment, rotation/revocation, server-side heartbeat leases, replay-safe assignment epochs, durable Operation assignment/results, and bounded reconnect/backpressure. A proposed starting heartbeat is 30 seconds with a 90-second lease; load and failure-detection requirements must validate these values.

## Consequences

Agent infrastructure requires CA lifecycle, protocol versioning, signed upgrades, and connection gateway operations. Agent liveness does not alter Host desired state or prove an Operation outcome. No Agent traffic is enabled before these controls are implemented.

## Alternatives considered

- Inbound connections to Agents: rejected as a default because NAT/firewall traversal and attack surface are worse.
- Shared fleet credential: rejected because compromise affects every Host.
- Heartbeat events as the only source of task/result state: rejected because reconnect and message loss require durable assignment recovery.
