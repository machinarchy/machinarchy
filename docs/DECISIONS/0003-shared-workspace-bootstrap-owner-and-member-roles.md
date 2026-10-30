# ADR 0003: One shared workspace, a configured bootstrap owner, and owner-managed roles

- **Status:** Accepted; implemented but not yet verified by an executed test run
- **Date:** 2026-10-28
- **Supersedes:** The account-to-workspace assignment in ADR 0002 (every new account owns a private workspace)

## Context

ADR 0002 gave every new account its own workspace and the `owner` role. That isolates accounts, but it prevents the teams Machinarchy is built for from sharing one inventory, and it made every self-registered account an owner. A first version of the shared-workspace change also seeded an `admin` account with a fixed password, assigned roles inconsistently between SQLite and PostgreSQL (PostgreSQL stored `owner` while reporting `reader`), and edited an already-applied migration.

## Decision

1. All accounts belong to the default workspace (`domain.DefaultWorkspaceID`). Self-registration creates a `reader` membership and no workspace rows. Workspace-scoped storage queries and the `workspace_memberships` table are kept, so multiple workspaces can return without a rewrite.
2. There is no built-in credential. The first owner is created at startup from `MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD` (with optional username and email) only while the workspace has no owner, safely from several processes at once, and a username collision with a non-owner is an explicit error. Without the setting the server logs a warning instead of inventing an account.
3. Owners manage roles through `GET /api/v1/members` and `PUT /api/v1/members/{id}/role`. The routes require a human owner session (not an API token), the workspace comes from the authenticated principal, and changes are logged as structured `audit` events.
4. A workspace can never lose its last owner. The store enforces it atomically with the update (a single guarded statement in SQLite; a transaction that locks the owner rows in PostgreSQL), because a check in the service layer would race.
5. Schema changes are new migrations, never edits to applied ones. A migration that could fail on existing data gets a pre-flight check that reports the conflicting rows and changes nothing (first use: host uniqueness, migration 4).

## Alternatives considered

- Keep one private workspace per account and add invitations: more machinery than a single-team product needs now, and it does not remove the self-registration-grants-owner problem.
- Create the admin account with a default password: convenient, and a standing backdoor on every deployment.
- Enforce the last-owner rule in the service: simpler, but two owners demoting each other concurrently could both pass the check.
- Make a database migration rename or delete duplicate hosts automatically: hides data loss from the operator.

## Consequences

- Databases migrated before this change keep their per-account workspaces next to the shared one; consolidating them is a deliberate operator task that is not automated.
- In production, with signup disabled, nobody can write until the bootstrap variables are set once. Remove the password from the environment afterwards.
- Role changes are only logged. A durable, tamper-resistant audit trail, invitations, member removal, and step-up confirmation for promoting an owner remain future work.
- Because every account sees the same inventory, the permission to relabel hosts must stay separate from any future permission to execute commands (see the fleet-targeting section of the security assessment).
