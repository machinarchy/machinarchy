# ADR 0004: Command execution as pinned, audited, structured runs

- **Status:** Accepted; implemented but not yet verified by an executed test run
- **Date:** 2026-10-29

## Context

Machinarchy can register SSH hosts, target them with labels, and preview a target set, but it cannot run anything. Adding execution is the highest-risk feature in the product: a mistake means arbitrary commands on many machines. The design has to make the safe path the default, make approval and outcome auditable, and be honest about what SSH cannot guarantee.

## Decision

1. **Opt-in, allow-listed.** Execution is off by default. Enabling it requires listing the programs that may run (or an explicit `*`); the server refuses to start otherwise.
2. **A command is an argument list.** There is no shell string. The program must be a plain name or path, arguments are validated, and the command line sent over SSH is built by single-quoting every element. This removes injection through arguments by construction, at the cost of not supporting pipes, redirects, or scripts. Those would be a separate, more heavily controlled feature.
3. **Separate permission.** `runs:execute` is distinct from inventory write; today it is held by owners and operators. Readers can see run status but not arguments or output.
4. **Pinned targets.** A run stores the resolved host IDs. A caller can bind the run to a preview with `targetsHash`, and at execution time a host whose address or port changed since submission is refused. Relabeling hosts therefore cannot widen or redirect an approved run.
5. **Asynchronous runs in the API process.** `POST /runs` returns 202 and the process executes in the background with bounded concurrency, per-host timeouts, the shared SSH capacity limiter, and capped, sanitized output. A heartbeat and a recovery sweep handle crashes; shutdown interrupts runs in flight and records the outcome.
6. **No retries, honest statuses.** A command is never retried. Timeouts, cancellation, and interruption are reported as such, with the caveat that the command may or may not have run.
7. **Transactional audit.** Run creation, cancellation requests, completion, and interruption write audit events in the same transaction as the state change. Arguments and output never enter the audit trail.

## Alternatives considered

- Accept a shell string and rely on the allow list: simple for users, but one allowed program plus a shell metacharacter defeats the allow list.
- A queue and separate worker processes now: better isolation and scale, but it needs delivery, leasing, and idempotency guarantees that the current single-process model avoids; the heartbeat and `owner_id` leave room to add it.
- Retry on failure: safe only for idempotent commands, which the system cannot know.
- Require a second approver: the right control for production fleets, deferred because it needs an approval workflow and identity model that do not exist yet.

## Consequences

- Operators can run any allowed program on any host in the workspace; the allow list should name narrow, single-purpose programs, not interpreters or `ssh`.
- Arguments are visible to other operators and owners, and on the remote host's process list; they must not carry secrets.
- Run output is stored unencrypted with no retention limit; a retention and export policy is future work.
- Execution capacity and availability are those of the API process.
- Scripts, file transfer, an approval step, agent execution, idempotency keys for submission, and CLI support are not included.
