# ADR-054: Workspace-scoped Access Profiles and immutable secret bindings

**Status:** Proposed for owner review; not approved for implementation
**Date:** 2026-11-12

## Context

The managed Host resource references an Access Profile, and ADR-028 requires connector-neutral Hosts and capability-oriented adapters. Phase 2 recommends reusable workspace-scoped profiles but leaves sharing and ownership as product decisions. Phase 7 describes version-pinned SecretRefs and pinned Host trust, but the repository has no Access Profile model or repository. The Phase G Secret Store currently resolves a reference by purpose only; ADR-038 requires authenticated workload, purpose, and resource context.

## Decision

### Ownership and authorization

- An Access Profile belongs to exactly one workspace. A profile may be reused by Hosts in that workspace only; cross-workspace sharing is not supported.
- Workspace owners may create, revise, disable, and assign profiles. Other principals require an explicit authorization grant; absent such a grant, these actions are denied. API authorization and application-service authorization are both required.
- Host/profile assignment is valid only when both resources belong to the same workspace. Profile IDs are opaque resource IDs, never credentials or endpoint data.
- A profile is disabled through a new immutable revision. Disabled profiles cannot be used by newly submitted Operations or new reconciliation attempts. An active action already using a resolved profile snapshot may finish under its captured deadline; emergency revocation must cancel affected work explicitly.

### Profile contents and revisions

- An Access Profile contains reusable authentication settings: connector kind, login name, authentication method, and immutable version-pinned SecretRefs for required credential material. It does not contain a concrete endpoint or a Host-specific SSH host-key fingerprint.
- The Host owns its concrete endpoint and trust data. The domain represents these as opaque connector-neutral access-target/trust values; the SSH adapter validates and interprets its own supported format. No SSH client, auth method, or key bytes enter the Host domain object.
- Every profile mutation appends a revision and compare-and-swaps the profile head using an expected revision. Revisions are immutable and retained for audit and Operations that captured them. The head points to the current revision.
- Host stores a stable AccessProfileID. Reconciliation loads the current profile revision after loading the latest Host. Operation submission captures the exact profile revision and credential reference versions in the immutable target snapshot. A profile update never silently retargets an existing Operation.
- Concurrent updates with a stale expected revision fail with a conflict. Profile updates do not change Host generation; they produce a profile event that schedules reconciliation for all Hosts referencing that profile.

### Secret authorization and ADR-038

- Secret plaintext resolution is a workload-only capability. The caller supplies an authenticated workload identity, SecretRef, purpose, workspace, and binding context. The Secret Store rejects API/CLI human principals and unrecognized workload identities.
- The authorization check verifies that the referenced immutable profile revision belongs to the requested workspace, contains that exact SecretRef/version for the requested purpose, and is bound to the Host (for reconciliation) or the captured Operation target/profile revision (for execution). A caller cannot substitute an arbitrary SecretRef.
- A Host reconciler may resolve only the SSH credential versions bound by the Host’s current Access Profile revision. An Operation worker may resolve only references in that Operation’s captured target snapshot. Profile/Host/Operation workspace and identity must agree.
- Secret resolution is audited with workload identity, purpose, workspace, profile ID/revision, Host or Operation ID, and secret ID/version. Audit records contain no plaintext, ciphertext, key material, or raw errors.
- Resolution remains in process and returns bytes only to the SSH adapter. API responses, profile revisions, resource events, logs, traces, metrics, and error messages contain references or safe error classes only.

### Lifecycle and failure behavior

- A profile may be disabled at any time by publishing a new revision. Disabled or missing profiles cause reconciliation to record a safe `AccessProfileUnavailable`/`AccessProfileDisabled` condition and retry only after a durable profile/Host change or bounded resync.
- Deleting a profile with one or more Hosts assigned is rejected with a conflict. The caller must first reassign or delete those Hosts. Profiles are tombstoned rather than physically removed so historical Operation snapshots remain auditable.
- A SecretRef whose version is missing, retired, purpose-mismatched, or undecryptable fails closed before SSH authentication. No fallback to another version, empty credentials, or another connector is allowed. The Host records a safe credential condition; the Operation target records a bounded failure class.
- Historical profile revisions and referenced SecretRef metadata are retained while needed by Operations and the configured backup recovery horizon. Secret ciphertext/key-retirement policy remains governed by ADR-026 and ADR-027.
- If the current profile revision changes during a reconcile, generation/revision checks protect the status write. The next queued reconciliation loads the latest Host and profile. No database transaction is held during network work.

### Notifications and Operations

- Creating a profile revision, advancing its head, writing audit metadata, and appending a `machinarchy.access-profile.changed` outbox event are one transaction.
- The event contains workspace ID, profile ID, and revision only. A consumer queries Hosts by `(workspace_id, access_profile_id)` and enqueues their resource keys; duplicate/out-of-order delivery is harmless. Startup and periodic Host resynchronization recover missed fan-out.
- Profile update fan-out is idempotent. Reconciliation reads current persisted Host and profile state, never event payload state.
- Operation specs persist the exact profile revision and SecretRef versions used at submission. Workers recheck that the Operation remains authorized and that captured profile/Host workspace bindings are consistent before resolving. A newer profile revision does not mutate a queued or running Operation; disable/revocation policy may reject or cancel it explicitly.

## Domain model proposal

```go
type AccessProfile struct {
    ID          resource.ResourceID
    WorkspaceID int
    Name        string
    Revision    uint64
    Enabled     bool
    Spec        AccessProfileSpec
    CreatedAt   time.Time
    CreatedBy   int
}

type AccessProfileSpec struct {
    ConnectorKind string // initially "ssh"
    Username      string
    AuthMethod    string // initially "password" or "private-key"
    Credential    SecretRef // immutable ID + version; private key passphrase may be another ref
}

type SecretRef struct {
    ID      string
    Version uint64
}

type HostAccessTarget struct {
    Endpoint string // host-owned address/port representation
    Trust    []string // opaque connector trust identities; SSH adapter validates fingerprints
}
```

The proposal intentionally does not put endpoint or Host-key fingerprint into the reusable profile. Profile revisions hold SecretRefs, never secret values. The Host profile binding stays a stable ID; reconciliation selects the current revision. Operation targets retain the selected revision and version-pinned secret references.

## Persistence and migration implications

- Add `access_profiles` for workspace, stable ID, current revision, name, lifecycle/tombstone state, and timestamps; enforce uniqueness by workspace and normalized name.
- Add append-only `access_profile_revisions` keyed by `(profile_id, revision)`, storing typed non-secret profile documents, actor/time, and checksum. Store SecretRefs as IDs and versions only.
- Add a workspace/profile lookup index and a reverse Host-reference index on `managed_hosts(workspace_id, access_profile_id)` for deletion checks and event fan-out.
- Add Host-owned connector-neutral endpoint/trust fields or a typed access-target document in the managed Host spec, with generation/revision CAS. Do not put credentials in Host rows.
- Extend secret authorization metadata/ports so resolution can verify authenticated workload + workspace/profile revision + Host/Operation binding. Do not duplicate encryption, versioning, or decryption in the profile store.
- Add the same migrations and indexes for SQLite and PostgreSQL. Profile revision/head/audit/outbox writes are transactional. PostgreSQL uses row locking/CAS; SQLite remains a single-node profile and uses its existing transaction/WAL behavior.
- Existing `managed_hosts` rows with an empty AccessProfileID remain unconfigured and report a safe `AccessProfileRequired` condition. Do not synthesize a profile or migrate legacy SSH credentials automatically; a separate explicit migration must preserve host IDs and avoid plaintext exposure.

## Connector integration

The Host application use case resolves a Host and its current profile, validates workspace/revision bindings, then passes a minimal request to the connector capability. The SSH adapter translates the endpoint, trust values, username, auth method, and authorized SecretRefs into private SSH parameters. It reuses `pkg/sshclient`, its strict pinned-host-key verification, bounded SSH capacity, context deadlines, output cap, and cleanup behavior. The domain and profile repository do not import `golang.org/x/crypto/ssh`. A future Agent profile may use its own identity/reference data and asynchronous capabilities without changing Host identity or pretending to be an SSH session.

## Alternatives considered

- **Host-owned profiles:** rejected as the default because it duplicates reusable login and credential bindings. It remains possible to create one profile per Host where operational isolation requires it.
- **Cross-workspace profile sharing:** rejected initially because it complicates authorization, secret resolution, audit ownership, and tenant isolation.
- **Store endpoints and Host fingerprints in reusable profiles:** rejected because a shared profile cannot faithfully represent distinct Host endpoints or trust identities.
- **Resolve any SecretRef by purpose alone:** rejected by ADR-038 as a confused-deputy path.
- **Retarget existing Operations to the current profile:** rejected because it changes accepted intent after submission and can execute with credentials the submitter did not authorize.
- **Hard-delete profiles immediately:** rejected because it breaks Host references and historical Operation auditability.

## Consequences

This ADR requires a scoped Secret Store resolver and Access Profile persistence before managed Host SSH reconciliation can be enabled. The first implementation supports SSH only. Existing legacy Host and Run services remain unchanged while migration is staged. A profile revision fan-out may enqueue many Hosts, so queue deduplication and periodic resync remain required. Retained SecretRefs do not guarantee plaintext availability: external KEK recovery remains governed by ADR-026.

## Approval required

Approve or amend the proposed ownership, write permissions, Host-owned endpoint/trust representation, disable/delete behavior, and Operation snapshot policy before implementation begins.
