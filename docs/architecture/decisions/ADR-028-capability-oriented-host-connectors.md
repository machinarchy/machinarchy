# ADR-028: Keep Host transport-neutral and expose connector capabilities

**Status:** Accepted on the recommended defaults when Phase 8 was authorized
**Date:** 2026-11-11

## Context

Hosts use SSH today, while a future Agent may be asynchronous and expose different operations. A common session abstraction would force incompatible transports into one lowest-common-denominator model.

## Decision

Host stores a connector kind and opaque access-profile reference. Connector adapters live outside the domain. Application consumers request narrow capabilities such as reachability, observation, or command execution through capability-oriented interfaces. A registry selects adapters and checks the requested capability before work starts. Connectors do not have to implement every capability and do not expose protocol clients to application services.

## Consequences

Domain Host identity remains usable with SSH and future Agent access. Application Operations depend on `CommandExecutor`, not SSH types. Existing SSH-specific Host fields and `DialParams` require staged migration into access profiles and the adapter. No universal session, plugin, or RPC abstraction is introduced.

## Alternatives considered

- Put `ssh.Client` or SSH auth types on Host: rejected because domain would depend on infrastructure and prevent Agent parity.
- Universal `Connect/Session` interface: rejected because an Agent task protocol may not have a caller-owned session.
- One connector must implement all capabilities: rejected because capability sets differ.
