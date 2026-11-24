# ADR-057: Agent capabilities run as Operations

**Status:** Accepted (owner decisions, 2026-11-24; AUDIT/10 Phase 17)

## Context

Agent work (`runtime.info`, `system.facts`, `service.status`, `service.control`) went through `POST /api/v1/agents/{id}/invoke`. That call was synchronous, held only in memory, not an Operation, and not audited (AUDIT A-05, M-7, M-8, M-9). A controller restart lost the call and its result.

SSH work, by contrast, is an Operation:
- persisted, with encrypted spec and output;
- audited, leased and cancellable;
- visible in `get operations`.

Enrolled agents were a separate list with no link to managed hosts.

## Decision

1. **One Operation model.** `OperationKind` gains `Capability`. The spec holds either `run {argv, …}` (SSH) or `capability {name, input, timeoutSeconds}` (agent), never both. Both live in `/api/v1/managed/operations`, the same list, audit and CLI (`machinarchy run --capability NAME`).
2. **The Access Profile decides the transport.** A new connector `agent` has no username or credential: the agent authenticates itself. A capability Operation targets only hosts whose profile is `agent`; `run` (argv) stays SSH-only.
3. **Hosts are bound to agents by a per-host enrolment token.** An owner issues the token for a managed host (`hostId`). When the agent enrols with it, the host is bound to that agent. Issuing a new token for the host and enrolling again moves the binding, which is how a machine is replaced. A host is Ready when it is bound to an agent with a live lease. Hostnames reported by agents are never trusted for binding.
4. **Durable dispatch (M-7).** Starting a capability Operation writes one command row per target, holding the agent's operation ID and command ID, in the same transaction that marks the targets `Running`. A relay publishes each row to the agent's JetStream subject with `Nats-Msg-Id` = command ID, and marks it published only after the PubAck. A crash between publish and mark re-publishes the same ID, which JetStream's 15-minute window deduplicates; the agent's replay ledger is the second guard. Commands carry the protocol's lifetime (at most 15 minutes) to be picked up.
5. **Durable results (M-8).** A result is checked:
   - signature;
   - subject matching the signer;
   - agent matching the command row.

   It is then written to the target in one transaction, and only after that is `result.ack` sent. The agent republishes unacknowledged results, so a controller that was down receives them when it returns. Status mapping:

   | Agent status | Target |
   |---|---|
   | `accepted` | `Running` |
   | `succeeded` | `Succeeded` (output stored, encrypted) |
   | `failed` / `rejected` | `Failed` (the agent's code as error class) |
   | `timed_out` | `TimedOut` |
   | `cancelled` | `Cancelled` |
   | `unknown` | `Interrupted` (`outcome_unknown`), never retried |
6. **No silent hangs.** A target with no terminal result by its command's expiry plus its timeout plus a grace period becomes `Lost` (`no_result`). The Operation finishes when every target is terminal. Cancelling sends `operation.cancel` to targets still running; their final status comes from the agent.
7. **Restarts.** Capability Operations, like detached runs, are not interrupted by a controller restart. Unpublished commands are published, and results keep arriving.
8. **`invoke` is removed.** `run --capability … --wait` replaces it for diagnostics, with an audit trail.

## Consequences

- Agent work gains persistence, audit, encryption at rest, cancellation and per-target results, with no new API surface beyond a spec field and a profile connector.
- `Run` and `Capability` share the target state machine; only the transport differs.
- Agents without a bound host cannot be targeted. Existing enrolments must be bound with a per-host token.
- The agent protocol and the agent are unchanged: the controller only uses `operation.request`, `operation.cancel` and `result.ack` as the agent already implements them.
