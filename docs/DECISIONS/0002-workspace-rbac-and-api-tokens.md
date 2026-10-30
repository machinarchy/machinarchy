# ADR 0002: Workspace-scoped authorization and API tokens

- **Status:** Accepted and implemented as the authorization baseline; the account-to-workspace assignment (decisions 1 and 6 and the matching consequences) is superseded by [ADR 0003](0003-shared-workspace-bootstrap-owner-and-member-roles.md)
- **Date:** 2026-10-26
- **Supersedes:** The pending workspace design in ADR 0001

## Context

Authenticated accounts could previously access shared hosts, environments, jobs, and operational history. Disabling public signup reduced exposure but did not prevent broken object-level authorization. Automation also needed credentials with narrower permissions than a human owner session.

## Decisions

1. Assign every account a workspace membership. The initial human role is `owner`; `operator` and `reader` define the permissions available to future memberships and API-token identities.
2. Pass workspace identity to repository operations for hosts, environments, jobs, and metrics history. Check that host environment references belong to the same workspace. Return non-disclosing not-found results for cross-workspace resource reads and mutations.
3. Represent API tokens as separate bearer identities bound to the issuing user and workspace. Allow only `operator` and `reader` token roles. Compute effective access as the less-privileged of the token role and the user's current membership role.
4. Generate random token secrets, return plaintext only at creation, and persist only a SHA-256 digest. Default to 30 days, cap lifetime at 365 days, and resolve expiry and revocation from storage on every request.
5. Require a human owner session to create, list, and revoke tokens. API-token credentials are accepted only in the Authorization bearer header.
6. Migrate legacy records to workspace 1. The lowest-ID existing account owns that workspace; later existing accounts get empty workspaces. On a fresh database, the first account claims workspace 1.
7. Keep the modular monolith and existing SQLite/PostgreSQL adapters. Do not add a new identity provider or dependency for this security boundary.

## Consequences

- New accounts are isolated by storage-enforced workspace scope; role-limited automation credentials can be revoked without rotating the JWT signing key.
- Legacy installations must inspect account IDs, verify backups, and rehearse migration 000003 before upgrading. The migration runner has no down-migration support; recovery is restore-from-backup or a reviewed forward migration.
- Workspace collaboration is not complete: invitations, member-role management, multiple workspace memberships, and durable audit events are future work. Every newly created user is currently an owner of a separate workspace.
- SQLite migration/repository integration and role/token tests exist. PostgreSQL integration and exhaustive HTTP denial-path coverage remain required before claiming equivalent test evidence for both engines.
