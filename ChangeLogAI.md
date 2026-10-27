# ChangeLogAI

Project change history maintained alongside the code. Entries are newest first. Each entry distinguishes changes made in that update from the implementation baseline and records only validation that was actually performed.

## 2026-10-28 — Single shared workspace, bootstrap owner, and hardened bulk host registration

### Changes in this update

- Replaced the per-signup workspace logic with one shared workspace: `CreateUser` (SQLite and PostgreSQL) now inserts the user and a `reader` membership in `domain.DefaultWorkspaceID` and creates no workspace rows. This also fixes the PostgreSQL path that stored the `owner` role while returning `reader`.
- Removed the hardcoded `admin`/default-password seeding and the user-label table/queries. The first owner is now created only from `MACHINARCHY_BOOTSTRAP_ADMIN_*` configuration via `service.BootstrapOwner`, only while the workspace has no owner, with an explicit error if the username already exists as a non-owner. A startup warning is logged when no owner exists.
- Added `UserStore.UpdateWorkspaceMemberRole` and `CountWorkspaceOwners` for both engines. No migration changes: the earlier in-place edit of SQLite migration `000002` was reverted.
- Reworked `POST /api/v1/hosts/bulk`: it now accepts `HostPayload` records (credentials and fingerprint can be supplied), sets the workspace from the authenticated principal, validates environments per record, supports `validate_only`, detects duplicates (name or address:port, in the inventory and in the batch), uses a bounded worker pool, preserves request order, reports every record, and returns sanitized errors.
- Made the unwired fleet placeholder safe: it now reports `ErrExecutionNotImplemented` instead of success, takes the caller's workspace, and no longer records the command in trace attributes.
- Restored the workspace-isolation and API-token integration test (adapted to a second workspace created directly) and added tests for bulk registration, bootstrap owner, shared-workspace signups, and bootstrap config.
- Updated `.env.example` (bootstrap variables and `MACHINARCHY_BROKER_URL`), `docs/architecture.md`, and `docs/ROADMAP.md`.

### Known limitations

- Existing databases keep their per-account workspaces; only new signups join workspace 1. There is still no API to promote members.
- Duplicate detection is read-then-write; the schema has no uniqueness constraint for hosts.
- `docs/swagger.json` and `docs/swagger.yaml` were not regenerated; run `make api-docs`.

### Validation

- Not run: Go was unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `gofmt`/`make fmt`, `make vet`, `make test`, and `make api-docs` before merging.

## 2026-10-27 — Roadmap: build artifact checksums

### Changes in this update

- Added planned SHA-256 checksum generation after successful Go binary builds, with unambiguous artifact names and consumer verification instructions as acceptance criteria.

### Validation

- `git diff --check` — passed.
- No tests run; this update changes roadmap documentation only.

## 2026-10-27 — Roadmap: shared installation RBAC and future workspaces

### Changes in this update

- Recorded the near-term direction of one shared tenant per installation, with environments for grouping and installation-wide reader, operator/writer, and owner/admin roles.
- Kept the currently shipped private-workspace-per-account behavior clearly marked as current, and added a planned follow-up for trusted account provisioning, role assignment, and an explicit migration that never silently merges existing workspaces.
- Preserved organization workspaces, membership management, and possible environment-level access policies as a later multi-tenant feature.

### Validation

- `git diff --check` — passed.
- No tests run; this update changes roadmap documentation only.

## 2026-10-26 — Git hook and setup script alignment

### Changes in this update

- Updated `scripts/prepare.sh` to match Machinarchy: require Go 1.26+, create `.env` securely from the repository template, and configure the checked-in hooks. Removed unrelated frontend/assets setup and automatic `go mod tidy`/dependency download behavior.
- Made setup preserve an existing non-default `core.hooksPath` by stopping for review instead of silently replacing it.
- Updated the commit-message hook to validate conventional breaking-change syntax and reject invalid invocation; refreshed the Git-hook documentation to reference current paths and behavior.

### Validation

- `bash -n scripts/prepare.sh .githooks/pre-commit .githooks/commit-msg` — passed.
- `git diff --check` — passed.
- `shellcheck` — not run; executable is not installed.
- Setup and pre-commit hooks were not executed; setup changes clone-local Git configuration and creates `.env`, while pre-commit runs the repository test suite.

## 2026-10-26 — Workspace and API-token use-case examples

### Changes in this update

- Expanded the use-case and development examples with workspace limitations, owner-managed API-token issuance, reader/operator guidance, token lifecycle and CLI credential use.
- Marked all `Principal` fields as excluded from JSON serialization and added a regression test to prevent accidental disclosure of request identity metadata.
- Reviewed the new authorization and workspace-related structs: API-token hashes and workspace ownership fields are excluded from JSON; issued tokens expose plaintext only in the one-time create response. No other serialization changes were needed.

### Validation

- `gofmt` — run on changed Go files.
- Focused `go test` for domain, middleware, API-token service, token, host, and environment packages — passed.
- `git diff --check` — passed.
- Full repository tests, benchmarks, race detection, and lint — not run.

## 2026-10-26 — Workspace RBAC and scoped API tokens

### Changes in this update

- Added workspace ownership and owner/operator/reader permission contracts. JWT requests reload current membership; API-token requests resolve a workspace-bound identity whose effective role cannot exceed its current membership.
- Added migration 000003 for SQLite and PostgreSQL. Legacy hosts, environments, jobs, and metrics history move to workspace 1; the earliest existing account owns it, and later accounts receive empty workspaces. Environment-name uniqueness is now workspace-local.
- Scoped host, environment, job, and metrics-history storage and API paths. Host-to-environment and job-to-host relationships are checked within the caller's workspace.
- Added API token create/list/revoke endpoints. Tokens are high-entropy bearer secrets, returned once, stored as SHA-256 hashes, default to 30 days, cap at 365 days, and are checked for membership, expiry, and revocation on each request. Token administration requires a human owner session.
- Updated the roadmap, security and architecture documentation, README, ADR 0002, and generated Swagger/Scalar contract with role semantics, token usage, legacy migration behavior, and remaining member-management limitations.
- Added role tests, middleware permission/bearer tests, and SQLite fresh-install plus legacy-upgrade isolation tests.

### Validation

- `gofmt` — run on changed Go files.
- `make api-docs` — passed.
- `go test ./...` — passed, including SQLite migration and workspace-isolation tests.
- `go vet ./...` — passed.
- `git diff --check` — passed after the final documentation update.
- PostgreSQL integration tests, benchmarks, race detection, and `golangci-lint` — not run. No PostgreSQL test server was configured.

## 2026-10-26 — Security baseline and production signup guard

### Changes in this update

- Added the authoritative product roadmap, a threat assessment, and an architecture decision record based on the inspected repository and official Salt, Ansible, and Terraform references.
- Added `MACHINARCHY_SIGNUP_ENABLED`; it defaults on in development and off in production. The signup handler returns HTTP 403 when disabled. Documented that this limits account creation but does not provide resource-level authorization.
- Regenerated the OpenAPI contract to include the signup-disabled response.
- Replaced the obsolete history handler test that imported a removed metrics package with table-driven tests using the current metrics service boundary, unblocking repository-wide tests.
- Recorded the next security milestone: workspace membership and authorization enforced across inventory, job, and history paths, with a deterministic migration plan for existing rows.

### Validation

- `gofmt` — run on changed Go files.
- `make api-docs` — passed.
- `go test ./...` — passed.
- `go vet ./...` — passed.
- `git diff --check` — passed after the final documentation update.
- `golangci-lint` — not run; executable is not installed.
- No benchmark, database integration, or end-to-end authorization/security test was run.

## 2026-10-26 — Initialize project change log

### Repository baseline at `f3bb16e`

The initial tracked project snapshot includes:

- Go API, control-manager, combined monolith, and Machinarchy CLI entry points.
- Echo HTTP API, authentication, environment and host management, SSH host registration with expected host-key verification, and SQLite/PostgreSQL storage adapters.
- NATS JetStream event plumbing, health and metrics endpoints, OpenTelemetry setup, and structured server logging.
- CLI context configuration and host listing. The CLI does not currently register hosts or submit arbitrary commands.
- Job read endpoints and an agent placeholder; there is no public job-submission or arbitrary command-execution endpoint.
- README, operations and architecture notes, CLI guides, use-case examples, and the Scalar-rendered OpenAPI contract.

This section records the existing baseline; it does not claim these capabilities were added by the changelog update.

### Changes in this update

- Added `ChangeLogAI.md` and this initial baseline entry.
- Added repository instructions and a contributor workflow requirement to update this log with every future project change.

### Validation

- `git diff --check` — passed.
- No tests or build checks were run; this update changes documentation and repository instructions only.
