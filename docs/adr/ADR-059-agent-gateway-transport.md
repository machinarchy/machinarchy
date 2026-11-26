# ADR-059: Agents reach the controller only through the agent gateway

**Status:** Proposed (design for owner approval; no code written)
**Date:** 2026-11-26

## Context

Today an agent connects to the controller's NATS broker. `internal/agentgateway` imports `nats.go` and JetStream directly, `agentv1/subjects.go` makes NATS subjects and JetStream names the wire contract, per-agent NATS JWTs are the agent credential, and the agent configuration is NATS-only. Operators therefore have to expose the broker and hand its address to every agent.

Owner decision (2026-11-26): no broker address or credential is exposed. An agent needs only the server URL and an enrolment token. The broker becomes an internal detail that can be swapped (NATS now; RabbitMQ or Kafka later).

## Decision (owner answers 2026-11-26)

1. **Q7, compatibility:** none. Existing NATS agents re-enrol on the gateway transport; the NATS agent path is removed, not kept as an alias.
2. **Q8, transport:** gRPC over the controller's own TLS endpoint (HTTP/2). Methods: `Register`, `Renew`, a long-lived server-to-agent `Commands` stream, `SendResult`. Message bodies stay the existing signed `agentv1` envelopes; the NATS subject names stop being the contract.
3. **Q9, delivery:** the internal `Bus` keeps today's guarantees (table below).
4. **Q10, release:** the owner owns the `machinarchy-agent` release and tags `protocol/v0.1.0`; the assistant never tags or pushes.
5. Controller side: `internal/agentgateway` splits into the agent-facing service (auth, leases, enrolment: existing logic) and a `Bus` interface (publish command, durable per-agent queue, ack, result subscription). `bus/nats` is the first adapter; the agent never sees it. Selected by controller configuration.
6. The CLI prints only the server URL, the enrolment token and the controller public key for pinning; `--nats-*` and `--remember` broker defaults are removed without aliases.

## Transport-neutral flow

| Step | Direction | Authentication | Result |
|---|---|---|---|
| Register | agent to gateway | single-use enrolment token plus agent Ed25519 signature | agent ID, lease (ID, expiry, heartbeat interval), signed by the controller key |
| Renew | agent to gateway | signed by the agent key; accepted for revoked agents too | new lease, or a signed `lease_revoked` / `unknown_agent` / `unavailable` |
| Commands | gateway to agent, one stream per agent | lease credential issued by the gateway (not a broker credential) | signed commands, each acknowledged after the agent has accepted it |
| SendResult | agent to gateway | signed by the agent key; `agent_id` must equal the authenticated stream identity | stored, then a `result_ack` command for terminal results |

Unauthenticated or malformed requests get no answer, as now. Errors use the existing codes (`invalid_request`, `enrollment_rejected`, `unknown_agent`, `lease_revoked`, `unavailable`). Size limits stay as in `agentv1` (identifiers 128, tokens 32-256, input 64 KiB, output 256 KiB, command lifetime at most 15 min, clock skew 2 min, heartbeat 5 s to 5 min).

## Current guarantees: kept, changed, dropped

| Guarantee (current code) | Verdict | Reason |
|---|---|---|
| Signed envelopes, controller key pinned by agents, per-agent key proof | Kept | authenticity must not depend on transport or broker ACLs |
| Single-use, expiring enrolment tokens (1 min to 24 h), consumed atomically with registration | Kept | unchanged logic, moved behind gRPC |
| Lease with TTL (default 1 min) and heartbeat at most half the TTL | Kept | unchanged |
| Revoked agents get a signed `lease_revoked` instead of a timeout | Kept | agents must learn they are revoked |
| Result subject must match the signer (one agent cannot report for another) | Changed | becomes: result `agent_id` must equal the authenticated stream identity and the signature |
| Per-agent isolation via NATS JWT subject permissions | Changed | becomes the gateway binding each stream to one agent ID; the `Bus` adapter uses internal credentials only |
| Per-agent NATS user JWT (default TTL 1 h) in register/renew responses | Dropped | no broker credential leaves the controller; replaced by the gateway lease credential |
| Durable per-agent command queue, file storage | Kept | `Bus` must provide a durable offline queue |
| At-least-once delivery, explicit ack, AckWait 30 s, MaxDeliver 20 | Kept | `Bus` contract; agents already deduplicate by command ID |
| Per-agent ordering | Kept | `Bus` contract |
| Result redelivery when the result handler fails (not acknowledged) | Kept | agent republishes until acknowledged |
| Agent-visible NATS subjects and stream names | Dropped | broker is internal |
| `MACHINARCHY_AGENT_NATS_*` configuration | Dropped | no legacy path (Q7) |

## Threats and required controls

- **Token replay:** the enrolment token is single-use and consumed atomically; a second use returns `enrollment_rejected`. Test: reuse, expired, wrong hostname.
- **Lease theft:** the lease credential is bound to the agent key and the TLS stream, expires with the lease and is rejected after revocation; requests must also be signed by the agent key, so a copied lease alone does nothing. Test: expired lease, lease from another agent.
- **Cross-agent read:** the stream is bound to the authenticated agent ID; the gateway never accepts an agent ID from the request body for subscription. Test: agent A never receives agent B's commands.
- **Revoked agent:** renewal returns a signed `lease_revoked`, the open stream is closed, and no new commands are delivered. Test: revoke during an open stream.
- **Downgrade to the old NATS path:** the gateway stops exposing agent-facing NATS (config and ACLs removed); an agent that cannot speak gRPC fails closed and logs the reason. TLS is never skippable on the agent.
- **Flooding and large messages:** per-message size limits as above, per-agent rate limit and a bound on concurrent streams. No secrets in logs; the token never appears in argv or logs.

## Consequences

- Wire contract and trust boundary change in two repositories; implementation is Phase 8 steps 8.2 to 8.5, each verified at L2 or L3 including a real-controller compatibility run.
- The agent-repo spec (`agentv1/doc.go`, a transport-neutral `transport.md`) is to mirror this ADR. It is not written yet because the agent repo had uncommitted changes owned by someone else when this ADR was written.
- Existing NATS agents must re-enrol after the controller upgrade.

## Alternatives considered

- **HTTP/2 plus WebSocket:** friendlier to some proxies, but needs hand-written framing and keep-alive; rejected by the owner (Q8).
- **Keep NATS exposed with narrower ACLs:** still publishes a broker address and credential; rejected by the owner.
- **Overlap window for NATS agents:** rejected (Q7), consistent with the no-legacy rule.
