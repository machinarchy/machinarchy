# ChangeLogAI

Project change history maintained alongside the code. Entries are newest first. Each entry distinguishes changes made in that update from the implementation baseline and records only validation that was actually performed.

## 2026-10-28 — SSH integration tests against an in-process server

### Changes in this update

- Added `internal/testutil/sshtest`, a loopback-only SSH server for tests: generated ed25519 host key, password and public-key authentication, `exec` requests with configurable output and exit status, counters (accepted and open connections, credential attempts, execs), and two deliberately broken modes (accepts TCP but never answers; answers with HTTP instead of SSH).
- Added `pkg/sshclient/client_integration_test.go` covering the real client: success with a pinned key; mismatched and missing fingerprints (the server's actual key is reported and, crucially, no credential is ever sent to the unverified host); wrong password, wrong user, and a server that offers no password auth; unencrypted and passphrase-protected key auth, a wrong passphrase failing before any connection, and an untrusted key; connection refused; a hung host honoring the deadline; cancellation during the handshake; a non-SSH service; connection release after 20 sequential and 8 concurrent connections; command output, non-zero exit status, the 1 MiB output cap, no command execution on a host-key mismatch, blank-command rejection, and a deadline stopping a running command.
- Added `internal/service/host_ssh_integration_test.go`: `RegisterHost` and bulk registration against real servers (success stored as active; wrong key, wrong password, refused, and hung hosts stored nothing and were isolated from each other; the default policy never dials loopback).
- Fix found while writing the tests: when the handshake read timed out a moment before the context reported its deadline, `pkg/sshclient` returned an opaque "negotiate SSH connection" error. It now returns `context.DeadlineExceeded` when the context deadline has passed and `domain.ErrConnectionTimeout` otherwise.
- Updated `docs/ROADMAP.md`.

### Known limitations

- The new tests have not been run. Timing-based cases (hung host, deadline during a command) use generous bounds but should be watched for flakiness under `-race` on slow CI runners.
- The server does not implement shells, ptys, port forwarding, or SFTP.

### Validation

- Not run: Go was unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `make fmt vet test` (and `go test -race ./pkg/sshclient/... ./internal/service/...`) before merging.

## 2026-10-28 — Store contract tests for SQLite and PostgreSQL, and CI

### Changes in this update

- Replaced the SQLite-only member and host-uniqueness tests with one shared contract suite (`internal/store/contract_test.go`) that runs every case against each available engine: signup defaults and uniqueness (including accounts without an email), member roles and last-owner protection, concurrent owner demotion, host lifecycle and workspace isolation (including credential decryption round-trip and status guards), host uniqueness and update collisions, environments, API tokens (expiry, revocation, role capping), migration idempotence, and the migration-4 pre-flight.
- SQLite always runs. PostgreSQL runs when `MACHINARCHY_TEST_POSTGRES_URL` is set; each test gets its own schema (via `search_path`) that is dropped afterwards. `MACHINARCHY_REQUIRE_POSTGRES_TESTS=1` turns a missing URL into a failure instead of a skip.
- Added `.github/workflows/ci.yml`: gofmt check, `go build`, `go vet`, and `go test -race ./...` with a PostgreSQL 16 service (PostgreSQL tests required), plus a Docker image build.
- Added `make test-postgres` and `scripts/test-postgres.sh` to run the suite against a throwaway Docker container, and documented both in `CONTRIBUTING.md`.
- Updated `docs/ROADMAP.md`, `docs/SECURITY.md`, and `README.md` to say the PostgreSQL suite exists but has not yet been observed passing.

### Known limitations

- Not run: the PostgreSQL half of the suite has never been executed, so it may expose real PostgreSQL bugs or incorrect test assumptions; fix what it reports rather than loosening tests.
- CI does not run `golangci-lint` (no configuration exists in the repository) and does not regenerate Swagger files.
- Email uniqueness is case-insensitive in SQLite but case-sensitive in PostgreSQL; the suite does not assert either, so confirm the service normalizes emails before relying on it.

### Validation

- Not run: Go and Docker were unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `make fmt vet test` and `make test-postgres` (or push and let CI run) before merging.

## 2026-10-28 — Host uniqueness enforced by the database

### Changes in this update

- Added migration `000004_host_uniqueness` for SQLite and PostgreSQL: unique indexes on `(workspace_id, name)` (case-insensitive) and `(workspace_id, ip_address, port)` (case-insensitive address). The previous schema had no uniqueness constraint, so duplicate detection in bulk import was a read-then-write check that concurrent requests could beat.
- Added a migrator pre-flight hook (`internal/store/preflight.go`). Before applying migration 4 it scans for existing conflicts and, if any exist, refuses to start with a message listing each workspace, the conflicting name or address:port, and the host IDs. Nothing is renamed, merged, or deleted automatically; resolve the duplicates and restart. A fresh database or one without duplicates migrates normally.
- Stores now map unique violations on create and update to the new `domain.ErrHostAlreadyExists`. `POST /api/v1/hosts` and `PUT /api/v1/hosts/{id}` return 409 Conflict; bulk registration reports a record that loses a race as `duplicate` (with no database error text) and leaves the others unaffected.
- Tests: database-enforced duplicates (name, address:port, case, per-workspace independence, no partial rows), update collisions and self-update, the pre-flight conflict report and recovery after resolving duplicates, bulk insert-time duplicates, and handler status mapping.
- Updated `docs/ROADMAP.md` and `docs/architecture.md`.

### Upgrade note

- Databases that already contain two hosts with the same name (ignoring ASCII case) or the same address:port in one workspace will fail to start until the duplicates are renamed or deleted. The error lists the host IDs.
- Case-insensitivity is ASCII-only in SQLite; the application-level duplicate check folds Unicode case, so it can be slightly stricter than the SQLite index.

### Known limitations

- The migration pre-flight and PostgreSQL indexes have no PostgreSQL integration test. Host deletion is unaffected.

### Validation

- Not run: Go was unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `make fmt vet test` (ideally with `-race`) before merging.

## 2026-10-28 — Owner-only member role administration

### Changes in this update

- Added `GET /api/v1/members` and `PUT /api/v1/members/{id}/role` (body `{"role": "owner|operator|reader"}`). Both require a human owner session (new `members:manage` permission plus the existing human-session guard, repeated in `service.MemberService`), so API tokens cannot use them. The workspace comes from the authenticated principal.
- `UserStore.UpdateWorkspaceMemberRole` now refuses to demote the last owner with the new `domain.ErrLastOwner`, atomically with the update: one guarded statement in SQLite, and a transaction that locks owner rows in PostgreSQL. Changing a role within a workspace the member does not belong to returns `ErrUserNotFound`.
- Added `UserStore.ListWorkspaceMembers` (bounded to 1000, ordered by user ID) for both engines.
- Role changes are written to the structured log as `audit` events (actor, target, previous and new role); unchanged roles are a no-op.
- Tests: member service permission matrix and behavior, handler error mapping and route guards (operator, reader, API token), SQLite listing, last-owner protection, cross-workspace refusal, and a concurrent-demotion test.
- Docs: SECURITY.md (new member-administration section and corrected workspace statements left stale by the single-workspace change), ROADMAP.md, architecture.md, and README.md.

### Known limitations

- No invitations, member removal, or workspace switching. Audit records are log-only. No confirmation or step-up check when promoting an owner. The PostgreSQL implementation has no integration test. Swagger files were not regenerated; run `make api-docs`.

### Validation

- Not run: Go was unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `make fmt vet test` (ideally with `-race`) before merging.

## 2026-10-28 — SSH target address policy and DNS-rebinding-safe dialing

### Changes in this update

- Added `pkg/netpolicy`: a destination policy (always-denied unspecified/multicast/broadcast/zoned/cloud-metadata addresses; denied-by-default loopback and link-local; configurable private ranges; explicit allow and deny CIDRs) and a `DialContext` that resolves a hostname once, requires every answer to be permitted, and dials the validated IP.
- `pkg/sshclient` now dials through the policy; with no dialer supplied it uses `netpolicy.Default()`, so the policy cannot be skipped. Policy denials surface as `domain.ErrTargetNotAllowed`.
- Host registration (single and bulk) rejects forbidden literal IPs before spending SSH capacity; the host and metrics services accept `service.WithTargetPolicy`, wired from configuration in the API runtime. Single and bulk responses report a fixed, non-revealing denial message.
- New settings: `MACHINARCHY_SSH_ALLOW_PRIVATE` (default true), `MACHINARCHY_SSH_ALLOW_LOOPBACK` (default false), `MACHINARCHY_SSH_ALLOWED_CIDRS`, `MACHINARCHY_SSH_DENIED_CIDRS`.
- Behavior change: loopback and link-local targets are now refused by default. Development setups that register hosts on 127.0.0.1 must set `MACHINARCHY_SSH_ALLOW_LOOPBACK=true`.
- Updated `.env.example`, `docs/SECURITY.md`, `docs/architecture.md`, and `docs/ROADMAP.md`.

### Known limitations

- Not a substitute for network-layer egress controls; no port restrictions; `PUT /api/v1/hosts/{id}` does not re-check the address until the next dial.

### Validation

- Not run: Go was unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `make fmt vet test` (ideally with `-race`) before merging.

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
