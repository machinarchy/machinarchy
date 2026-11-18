# ADR-031: Integrate a future Agent as a capability adapter

**Status:** Accepted on the recommended defaults when Phase 8 was authorized
**Date:** 2026-11-11

## Context

An Agent may use authenticated polling or push transport, report observations, and execute durable task IDs. These behaviors differ from outbound SSH sessions.

## Decision

Represent Agent access through the same neutral Host connector kind/profile identity and implement only the capability ports the Agent protocol supports. Agent enrollment identity, mTLS, protocol version, heartbeat/lease, task acknowledgement, offline behavior, and idempotency remain inside the future Agent adapter and application integration. Capability support is negotiated or declared and checked before enqueue; never silently fall back to SSH.

## Consequences

Host domain and Operation APIs do not need a transport redesign. Agent work remains at-least-once and requires stable task/idempotency identity. No Agent implementation or protocol is invented in this phase; `cmd/agent` remains a placeholder until requirements are concrete.

## Alternatives considered

- Model Agent as an SSH session: rejected because transports and cancellation semantics differ.
- Assume all Agent connectors support execution: rejected because capabilities depend on protocol and agent policy.
- Introduce an Agent transport framework now: rejected until an actual protocol and deployment requirement exist.
