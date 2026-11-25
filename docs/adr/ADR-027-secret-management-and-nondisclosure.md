# ADR-027: Separate secret management from configuration and prohibit read-back

**Status:** Accepted on the confirmed constraints and recommended defaults when Phase 7 was authorized
**Date:** 2026-11-11

## Context

Configuration needs to refer to secrets, while management APIs and CLI must avoid becoming a plaintext retrieval channel. Secret changes also need separate authorization, versioning, audit, and resource impact semantics.

## Decision

Configuration revisions store typed secret references pinned to explicit versions. Secret metadata and lifecycle are owned by a separate Secret Management capability. Create/update/rotate accept plaintext over TLS, persist a new encrypted version, return metadata only, and do not provide plaintext read-back. Secret resolution is limited to authorized in-process consumers with a purpose. Owners are the initial secret writers; mutations and denied resolution attempts are audited without value material. Secret/config/outbox events contain IDs and versions only. A secret version cannot be retired while referenced or within the configured backup recovery horizon.

## Consequences

Rotation that changes a config reference creates a new runtime config revision; rotation used by a Host/access profile triggers reconciliation. Secret values in the database remain encrypted but are plaintext in memory during use. Endpoint body logging and telemetry capture require explicit redaction controls.

## Alternatives considered

- Return plaintext after create or provide a general reveal endpoint: rejected because API clients, logs, and traces become retrieval surfaces.
- Embed secret bytes into runtime config: rejected because config history, events, and status interfaces would expand the exposure boundary.
- Treat secret rotation as an in-place overwrite: rejected because it erases history and makes safe rollback/recovery difficult.
