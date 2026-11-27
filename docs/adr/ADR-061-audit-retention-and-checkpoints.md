# ADR-061: Audit retention: keep by default, archive then prune behind a signed checkpoint

**Status:** Accepted and implemented (step 3.2), 2026-11-27. Retention defaults and the API shape approved by the owner the same day.
**Date:** 2026-11-27
**Builds on:** ADR-022 (audit is not pruned until verified archival/export exists), ADR-040 (head hashes belong outside the database), step 3.1 (CLI `audit export`)

## Context

`audit_events` is append-only and hash-chained per workspace. Database triggers (`audit_events_no_update`, `audit_events_no_delete`) reject any change, and `VerifyAuditChain` walks the chain from the first row. ADR-022 forbids automatic audit pruning until archival is designed. Step 3.1 supplies the archival half: a client-side export with chain check. Without a retention path the table grows forever, and the only way to shrink it today is to bypass the triggers, which would silently break `verify`.

## Decision

1. **Keep forever by default.** No retention limit is set out of the box. Nothing is ever pruned unless an owner runs the prune explicitly.
2. **Explicit, owner-only prune.** A human owner session (API tokens refused, like role changes) runs a prune up to a cutoff age. There is no scheduler in this step. The minimum cutoff is 30 days, so recent evidence cannot be removed. A suggested value of 1 year is documented, not enforced.
3. **Archive before delete.** The prune refuses to run unless the caller supplies the archive details of the range being removed: the last event id and hash from a verified export made by `audit export` (3.1). The server re-reads the range, recomputes its chain and compares the head and the count with what the caller supplied. Only on a match does it delete.
4. **Checkpoint.** In one transaction the prune writes a checkpoint row (workspace, last pruned event id, its hash, count removed, actor, time) and deletes the range. `VerifyAuditChain` starts from the latest checkpoint instead of the genesis value: the first retained event must link to the checkpoint hash. A missing or altered checkpoint makes `verify` fail. The checkpoint and the prune are themselves recorded as a normal audit event (`audit.pruned`) in the retained chain.
5. **Deletion path.** The no-delete trigger stays. Pruning goes through one narrow, guarded path (a trigger exception that applies only inside the prune transaction and only for ids at or below the checkpoint being written). No general delete is allowed.
6. **Anchor.** The off-database anchor remains ADR-040's open work. Until it exists, the archive file and the checkpoint hash an operator stores elsewhere are the only independent record, and this ADR does not claim the checkpoint is administrator-proof. `docs/SECURITY.md` states that a database administrator can still rewrite the checkpoint.
7. **Scope.** Applies to `audit_events` only. Operational retention (outbox, inbox, operation output, metrics) stays under ADR-022 and is unchanged. `management_audit_events` is not touched.
8. **Interface (to approve).**
   - `POST /api/v1/audit/prune` with `{"beforeId": N, "archiveHeadHash": "...", "archiveCount": N}`; returns the checkpoint. Owner session only.
   - `GET /api/v1/audit/verify` also reports the checkpoint it started from.
   - CLI: `audit prune --before-id N --archive FILE` reads the verified export file, sends its head hash and count, and prints the checkpoint. It is not offered as a default or automated action.
   - Implementation notes: confirmation is typing the `--before-id` value (`--confirm N` in scripts; `--yes` is not enough) rather than a bare flag; `--dry-run` is supported. The delete trigger is replaced (SQLite) or its function swapped (PostgreSQL) so a row can be deleted only when a checkpoint of the same workspace covers its ID; `audit_checkpoints` has update/delete triggers. The prune walks the range under the workspace audit lock (PostgreSQL advisory lock) and appends `audit.pruned` in the same transaction; a very large range holds that lock for the walk. The 30-day minimum is not configurable. The CLI checks only that the archive is an unfiltered, linked export; the server holds the authoritative check.

## Alternatives considered

- **Time-based automatic pruning:** deletes evidence without a person deciding; rejected for this step, can be added later on top of the same checkpoint.
- **Prune without archive:** simplest, but breaks the ADR-022 rule and makes the loss unrecoverable.
- **Truncate and restart the chain with a new genesis:** loses the link to earlier history, so a deleted range cannot be detected. Rejected.
- **Soft-delete flag:** keeps the table growing, so it is not retention.
- **Per-event-type retention:** more rules and more ways to leave holes in the chain; deferred.

## Consequences

- Operators who never prune see no change. Those who prune must keep the archive; the server cannot restore it.
- `verify` proves integrity only for the retained range plus its link to the checkpoint. It does not prove the pruned range was complete unless the archive is checked.
- A new table and a trigger change need forward-only migrations for SQLite and PostgreSQL with contract tests on both (ADR-020), and negative tests: prune without archive, mismatched head, below-minimum cutoff, API token, other workspace, tampered checkpoint, and prune racing an append.
- Implementation is the next step after approval and is L3 (security-sensitive, schema, API).
