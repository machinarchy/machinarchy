# Machinarchy Product and Engineering Roadmap

This roadmap reflects the code in the repository on 2026-10-27. Status refers to shipped behavior, not intent. The immediate product is a self-hosted SSH host inventory and control plane; remote command submission and execution are not currently available.

## Product direction

Make it straightforward and safe for operators to inventory, verify, and eventually operate fleets of 10–100+ SSH hosts. Preserve a credible path to larger fleets by measuring latency, database contention, SSH fan-out, and failure behavior before adding distributed components.

Design principles:

- Keep a modular monolith with explicit domain, service, transport, and storage boundaries.
- Treat authentication, authorization, network egress, secrets, and audit as core capabilities, not deployment afterthoughts.
- Make inventory import and remote execution explicit operations with bounded work, cancellation, and per-host outcomes.
- Prefer safe, repeatable operations and clear previews over implicit side effects.
- Add infrastructure only for a defined reliability or scale requirement.
- Never execute commands as a side effect of registering or importing hosts.

## Near-term tenancy and authorization direction

For the current single-team product, treat one Machinarchy installation as one shared tenant. Environments such as `development`, `staging`, and `production` organize inventory; they are not separate customer workspaces or authorization boundaries by themselves. Apply a simple installation-wide RBAC policy:

- **Reader:** read inventory and operational records.
- **Operator/Writer:** reader access plus inventory changes.
- **Owner/Admin:** operator access plus account, token, and other administrative controls.

Accounts in one installation now share a default workspace (workspace 1): self-registered accounts join it as `reader`, the first owner comes from the bootstrap configuration, and owners promote others through the members API. The workspace key stays as an internal single-tenant boundary so multiple workspaces can return later; do not require customers to manage multiple workspaces or treat each environment as a workspace. Databases created before this change keep their per-account workspaces until an operator consolidates them deliberately.

Do not grant elevated roles merely because someone can self-register. Define a trusted bootstrap/provisioning path and assign each account its installation-wide role through that path. Environment-specific roles and access lists are deferred until there is a concrete need for different users to access different environments.

Preserve the later multi-tenant idea: organizations may eventually have separate workspaces, member invitations and role assignment, and possibly environment-level policies. That future feature must be designed as an explicit organization lifecycle and authorization model, rather than emerging from account signup. Before changing the shipped workspace behavior, provide a reviewed migration that identifies which existing workspace's data becomes shared; never silently merge independently owned workspaces.

## Current-state assessment

### Verified capabilities

- Separate API, control-manager, monolith, CLI, and placeholder agent entry points exist.
- Echo HTTP API, JWT authentication, SQLite and PostgreSQL adapters, versioned SQL migrations, NATS JetStream event plumbing, health endpoints, Prometheus metrics, and OpenTelemetry HTTP/NATS instrumentation exist.
- Host registration validates SSH authentication against a caller-supplied SHA-256 host-key fingerprint before persistence. The SSH client uses the Go SSH library, applies timeouts/cancellation, and caps captured command output.
- SSH operations have a process-local concurrency limit. Host credentials are encrypted by the storage adapters and omitted from ordinary host JSON responses.
- Environments, host list/create/update/delete, job history reads, and CLI context configuration are present.
- Accounts are assigned a workspace; host, environment, job, and metrics-history queries are workspace-scoped. Human JWT access rechecks current membership on each request.
- Workspace owners can issue operator or reader API tokens. Tokens are random, shown once, stored as SHA-256 hashes, expire within 365 days, and can be revoked immediately. API token permissions are intersected with the user's current workspace role.

### Verified gaps and risks

- The installation uses one shared workspace. Owners can list members and change roles through `/api/v1/members` (never leaving the workspace without an owner); there is still no member invitation or removal, workspace switching, or durable audit trail. API tokens provide the automation identities.
- Public account signup defaults off in production and on in development. When deliberately enabled, each new account receives an isolated workspace.
- Workspace authorization is covered by SQLite integration tests, but PostgreSQL migration/repository integration and exhaustive HTTP denial-path coverage are still outstanding.
- Host listing and environment listing are unpaginated. Single registration tests one SSH connection synchronously. Bounded synchronous batch registration with per-record results exists (`POST /api/v1/hosts/bulk`); YAML/CSV import and asynchronous/resumable imports do not. Host name and address:port uniqueness per workspace is enforced by unique indexes (migration 4), which refuse to apply, with a conflict report, if existing data already violates them.
- No public API submits remote commands. The agent process is a placeholder; job routes read existing rows only.
- The control-manager currently logs reconnection events rather than reconciling host state. Host persistence and event publishing do not share an outbox transaction.
- The SSH capacity limit is process-local. Multiple API replicas multiply the effective limit. Audit records and credential rotation/secret references need explicit designs. The SSH target address policy and DNS-rebinding-safe dialing (`pkg/netpolicy`) are implemented; port restrictions and re-checking addresses on host update are not.
- The repository still has sparse test coverage outside the authorization baseline. A shared store contract suite (users, members, hosts, uniqueness, environments, API tokens, migrations) runs on SQLite and, when configured, PostgreSQL, and CI provisions a PostgreSQL service for it. The PostgreSQL half has been written but not yet observed running, so treat it as unproven until the first CI run is green. Fleet benchmarks are outstanding.

## Architecture decisions

1. **Keep the modular monolith.** Existing command entry points already separate deployment roles. Preserve shared domain and service code; split processes further only when measured scale or fault-isolation requirements justify the operational cost.
2. **Keep ports at real boundaries.** Domain/service interfaces isolate HTTP, SSH, event broker, and SQL implementations. Avoid interfaces that only wrap a single internal function without a testing or evolution benefit.
3. **Enforce workspace authorization at the storage boundary.** Users have workspace membership and owner/operator/reader roles. Host, environment, job, history, and API-token reads and writes carry workspace identity into repository queries. API tokens are workspace-bound, least-privilege credentials whose effective permission cannot exceed the current membership role. Do not implement isolated `owner_id` columns that leave alternate access paths unscoped.
4. **Keep SSH as a transport adapter.** Retain the Go SSH implementation and strict host-key pinning. Define address/egress policy, identity rotation, and secret references before adding discovery or broad execution.
5. **Keep event delivery distinct from job execution.** NATS JetStream currently carries control-plane events. Do not use it as a general-purpose job scheduler until delivery, idempotency, retries, cancellation, and recovery guarantees are defined.
6. **Treat SQLite as single-instance storage.** Use SQLite for local/single-process installations; use PostgreSQL for API/control-manager processes sharing state. Do not promise multi-writer behavior from SQLite.
7. **Use previews and explicit approval for fleet changes.** A future fleet operation should resolve targets before dispatch and return the exact selected host set before execution when the operation is destructive or broad.

## Reference-system evaluation

Machinarchy should reuse the problems solved by established tools without copying their internals or terminology:

- Salt demonstrates flexible host targeting by identity, labels/grains, lists, and compound selectors. Machinarchy targets by ID, name, environment, built-in fields, and validated labels through a deliberately small selector language (AND-only requirements, set membership, simple wildcards, no regular expressions or OR/NOT grouping), with preview and authorization semantics settled before any execution exists. [Salt targeting reference](https://docs.saltproject.io/en/3008/topics/targeting/index.html)
- Ansible emphasizes repeatable task execution and separates changed-state notifications from ordinary tasks. Machinarchy should require idempotency declarations or safe retry semantics for reusable workflows; arbitrary shell commands must remain explicitly non-idempotent unless proven otherwise. [Ansible playbooks and idempotency](https://docs.ansible.com/projects/ansible-core/devel/playbook_guide/playbooks_intro.html) and [handlers](https://docs.ansible.com/projects/ansible/latest/user_guide/playbooks_handlers.html)
- Terraform shows why shared infrastructure state needs concurrency protection, secure storage, and an inspectable plan before applying changes. Machinarchy should add operation previews, durable audit records, and conflict control before concurrent fleet mutation. [Terraform state](https://developer.hashicorp.com/terraform/language/state) and [state locking](https://developer.hashicorp.com/terraform/language/state/locking)

These are functional references only. Machinarchy remains SSH-first and should avoid duplicating another product's control plane or introducing a new DSL until concrete workflows demand it.

## Milestones

### Phase 0 — Assessment and architecture

- **Objective:** Establish an evidence-based baseline, risk ordering, and target boundaries.
- **Scope:** Repository and dependency review; current behavior inventory; architecture decisions; roadmap and security assessment; production signup default.
- **Prerequisites:** None.
- **Deliverables:** This roadmap, `docs/SECURITY.md`, architecture updates, signup setting and handler guard.
- **Acceptance criteria:** Implemented and planned features are distinguished; the highest-priority known access risk and residual limitation are stated; production signup is disabled unless explicitly enabled; tests cover configuration defaults and the route guard.
- **Security considerations:** The signup change reduces public account creation but does not grant or enforce per-resource privileges.
- **Tests required:** Configuration tests for development/production/override/invalid values and an HTTP handler test for disabled signup.
- **Status:** Completed for the initial assessment. Revisit when authorization and import designs are approved by implementation evidence.

### Phase 1 — Reliable host inventory and authorization

- **Objective:** Make inventory durable, bounded, correctly scoped, and safe to query or mutate.
- **Scope:** Workspace and membership model; owner/operator/reader capabilities; workspace-scoped hosts, environments, jobs, and metrics history; host-to-environment scope validation; API-token issue/list/revoke; deterministic migration of existing rows.
- **Prerequisites:** Choose a trusted owner for legacy data and take a verified database backup before the migration.
- **Deliverables:** Domain authorization contracts, SQLite/PostgreSQL migrations, workspace-aware repositories and HTTP handlers, hash-only API tokens, token lifecycle endpoints, security guidance, and cross-workspace integration coverage.
- **Acceptance criteria:** Protected API paths carry workspace identity into storage; missing membership blocks JWT requests; API token role is capped by current membership role; token expiry/revocation takes effect on the next request; cross-workspace resource lookups are non-disclosing; legacy rows have one deterministic owner.
- **Security considerations:** Prevent broken object-level authorization on indirect routes and references; keep API-token secrets out of storage and logs; preserve backup data; do not accept credentials that can grant a role higher than their issuer.
- **Tests required:** Role/permission tests; SQLite migration and repository isolation tests; API token expiry/revocation tests; HTTP allow/deny tests; PostgreSQL migration and integration tests.
- **Status:** Core authorization and token lifecycle implemented. A bootstrap-owner flow and owner-only member role management are implemented. Member invitation/removal, a verified PostgreSQL contract-suite run, durable audit records, pagination, and broader HTTP denial-path tests remain planned.

### Phase 1 follow-up — Shared installation and global roles

- **Objective:** Make the current single-team installation usable by multiple accounts without turning environments into tenant boundaries.
- **Scope:** One shared default workspace per installation; installation-wide reader/operator(owner-admin) roles; trusted account provisioning and role assignment; preserve workspace-scoped storage as the internal tenant boundary; defer per-environment ACLs.
- **Prerequisites:** Decide how an operator bootstraps the first owner and creates or promotes additional accounts. Review existing workspaces and data ownership before migration.
- **Deliverables:** A safe provisioning/role-assignment flow, migration plan for existing account workspaces, updated signup behavior, and docs distinguishing environment grouping from tenant isolation.
- **Acceptance criteria:** Authorized accounts in one installation see the same inventory; each request still enforces the caller's assigned role; self-signup cannot grant operator/admin privileges; environment IDs cannot bypass installation-wide authorization; no existing workspace data is merged without explicit operator selection and backup.
- **Security considerations:** Keep owner/admin assignment privileged; preserve server-side authorization for every route and storage query; document that environments alone do not isolate access.
- **Tests required:** Multi-account shared-inventory integration tests; reader/operator/admin allow/deny matrix; signup/provisioning privilege tests; migration tests proving explicit ownership selection and no accidental data loss.
- **Status:** In progress. Implemented: one shared default workspace, read-only self-registration, a bootstrap owner from configuration (no built-in credential), and owner-only role management with last-owner protection. Not done: a supported, backed-up path to consolidate legacy per-account workspaces into the shared one, invitations and member removal, a durable audit trail, and multi-account HTTP integration tests for the shared inventory.

### Phase 2 — Bulk onboarding

- **Objective:** Import many hosts safely with clear partial-success semantics.
- **Scope:** Start with bounded synchronous batch registration for small files/requests; evaluate YAML/CSV only after a stable host schema; define validate-only and dry-run modes, deduplication keys, per-record outcomes, and idempotent re-import behavior. Add asynchronous jobs only if measured request duration requires them.
- **Prerequisites:** Phase 1 identity, authorization, validation, and duplicate rules.
- **Deliverables:** Versioned import schema, batch API/use case, CLI import command, bounded worker pool, resumable job model only if required, per-item result report.
- **Acceptance criteria:** A malformed or unreachable record does not erase successes unless atomic mode is explicitly selected; repeated imports produce documented outcomes; cancellation stops queued work and reports completed/in-flight items; maximum concurrency and memory are bounded; no command executes during registration.
- **Security considerations:** Enforce body/file size limits, reject unsafe YAML features, sanitize errors, constrain egress, and audit who imported which records.
- **Tests required:** Table-driven malformed/duplicate/partial-failure tests, cancellation and concurrency-limit tests, parser fuzzing, and representative 100-host import benchmark.
- **Status:** In progress. Bounded synchronous batch registration (max 100, partial success, `validate_only`, duplicate detection, bounded workers, sanitized per-record errors) is implemented. Not done: YAML/CSV parsing, audit of who imported what, parser fuzzing, a 100-host benchmark, and resumable asynchronous imports. Host uniqueness is enforced in the database.

### Phase 3 — Secure SSH connectivity

- **Objective:** Make trust, authentication, and connectivity checks operable at fleet scale.
- **Scope:** Preserve current pinned fingerprint checking; add documented fingerprint acquisition/rotation, explicit validation endpoint, and credential references/rotation interface. Define address policy and safe DNS resolution/dial behavior.
- **Prerequisites:** Phase 1 authorization and target validation; operator workflow for trust bootstrapping.
- **Deliverables:** SSH transport contract, trust lifecycle, egress policy configuration, structured error taxonomy, bounded check operation, and operational guidance.
- **Acceptance criteria:** Unknown or changed host keys fail closed; checks have caller and server timeouts; cancellation releases connections; credentials never appear in logs or API responses; private networks work only under the chosen explicit policy; per-host failures are independently reported.
- **Security considerations:** Mitigate MITM, SSRF, DNS rebinding, credential leakage, and lateral movement; test private, loopback, link-local, and public destinations under policy.
- **Tests required:** SSH integration tests with ephemeral test servers and host keys, cancellation/resource cleanup tests, host-key rotation tests, address policy tests, and race detector.
- **Status:** In progress. Pinned-key SSH auth, a process-local capacity limit, and a configurable target address policy with resolve-once/dial-validated-IP behavior exist. Not done: fingerprint acquisition/rotation workflow, an explicit validation endpoint, credential references/rotation, and a structured error taxonomy. SSH behavior is covered by integration tests against an in-process SSH server (`internal/testutil/sshtest`): pinned-key success, wrong or missing fingerprint, wrong password, key and passphrase auth, refused/hung/non-SSH hosts, cancellation, output caps, and connection release. These tests are written but have not yet been run.

### Phase 4 — Fleet targeting and execution

- **Objective:** Execute explicit operations against a previewed, authorized host set with predictable partial failures.
- **Scope:** Exact ID, environment, and label targeting; selector preview; bounded concurrency; per-host timeouts/results; dry-run; audit; safe argument model; execution policy.
- **Prerequisites:** Phases 1 and 3; signed-off execution threat model and permissions.
- **Deliverables:** Fleet service and executor interface, operation/run records, API endpoints, status/result model, CLI preview/execute workflow.
- **Acceptance criteria:** Preview and execution use the same normalized target selection; concurrency and output limits are enforced; unreachable hosts do not block the fleet; cancellation and retry semantics are explicit; sensitive actions have immutable initiator/target/outcome audit records.
- **Security considerations:** Prevent command injection, overbroad targeting, privilege escalation, replay, and cross-tenant execution. No automatic execution on host import.
- **Tests required:** Deterministic selector tests, bounded fan-out tests, partial-failure/cancellation tests, authorization tests, output cap tests, race detector, and 100-host latency/resource benchmarks.
- **Status:** In progress. Targeting is implemented: a validated label model, a set-based selector language, explicit include/exclude rules, host list filtering, atomic label patches, and a read-only target preview (`POST /api/v1/fleet/targets/preview`), all sharing one resolver (`domain.ResolveTargets`) that execution will reuse. Not done: operation and run records, the executor and bounded fan-out, dry-run, retries and cancellation semantics, per-host results, audit records, an execution permission, and the CLI workflow. No public command execution route exists today, and the internal fleet placeholder reports `ErrExecutionNotImplemented` for every host.

### Phase 5 — Operational maturity

- **Objective:** Make deployment health, persistence, recovery, and SLOs observable.
- **Scope:** Complete structured audit, actionable dependency health, DB integration coverage, backup/restore guidance, outbox where event delivery is business-critical, queue semantics only where jobs need durability, metrics cardinality review, measured capacity targets, and build artifact integrity.
- **Prerequisites:** Operational semantics from Phases 1–4.
- **Deliverables:** Deployment profiles, test containers or equivalent integration setup, recovery playbooks, runbook SLOs, performance baselines, and a SHA-256 checksum manifest generated after successful `go build` output creation.
- **Acceptance criteria:** Liveness remains independent of transient dependencies; readiness checks only required dependencies and active worker state; restart recovery has documented guarantees; load tests report p50/p95/p99 and resource usage at 100 hosts; no unbounded goroutine, queue, label, or result growth.
- **Artifact integrity criteria:** The build workflow writes checksums for every distributed binary only after all builds succeed, names each artifact unambiguously, and documents how consumers verify the checksum before installation or execution.
- **Security considerations:** Least-privilege DB/broker credentials, secure backup handling, audit retention, secret redaction, TLS verification, and bounded metrics labels.
- **Tests required:** SQLite and PostgreSQL integration suites, failure/restart tests, race/vet/lint, benchmark baselines, and backup/restore exercise.
- **Status:** In progress. Health, metrics, tracing, and JetStream infrastructure exist; end-to-end resilience, integration coverage, and measured targets do not.

### Phase 6 — CLI and automation

- **Objective:** Provide a scriptable, safe operator workflow over the authorized API.
- **Scope:** Extend current context-aware CLI with host add/check/import, filtered/paginated listing, preview-first fleet commands, stable JSON/YAML output, and clear auth/bootstrap ergonomics.
- **Prerequisites:** Stable API contracts and authorization; Phases 1–4 as applicable.
- **Deliverables:** Modular Cobra commands, generated help/reference docs, shell completion, examples, backward-compatible output and exit codes.
- **Acceptance criteria:** Interactive and non-interactive use has explicit context selection; secrets are read from secure input sources and never echoed; destructive/broad operations show the selected target set; machine output is stable and errors have nonzero exits.
- **Security considerations:** Protect local token files, avoid secrets in process arguments/history, and apply server-side authorization regardless of CLI behavior.
- **Tests required:** Command tests with fake API clients, config merge precedence tests, output golden tests, and end-to-end CLI/API smoke tests.
- **Status:** In progress. Context configuration and host listing exist; registration/import/operation commands do not.

### Phase 7 — Advanced orchestration and optional agents

- **Objective:** Add desired-state workflows or alternative transports only for demonstrated product needs.
- **Scope:** Reusable declarative workflows, idempotent modules, schedule/approval, optional agent transport, additional backends, and scale-out partitioning.
- **Prerequisites:** Reliable authorization, execution/audit/recovery model, support and threat model, measured evidence that SSH-only central execution is insufficient.
- **Deliverables:** Versioned workflow model, execution engine extensions, signed agent identity and upgrade lifecycle if agents are justified.
- **Acceptance criteria:** Reconciliation is idempotent and observable; workflows are versioned and reviewable; agent support has authenticated enrollment, revocation, secure updates, and bounded resource use; scale claims are benchmarked.
- **Security considerations:** Prevent untrusted workflow code, supply-chain compromise, agent impersonation, and privilege escalation.
- **Tests required:** State transition/property tests, compatibility tests, agent protocol security tests, upgrade/revocation tests, and scale benchmarks.
- **Status:** Planned.

## Risks and sequencing

| Risk | Mitigation / dependency |
| --- | --- |
| Legacy rows become owned by the earliest account during migration | Inventory existing account IDs first, verify a database backup, and confirm that the earliest account is the intended owner. Other existing accounts receive empty private workspaces. If ownership needs correction or the installation moves to a shared workspace, use a reviewed, auditable forward migration with explicit source/target selection; do not merge workspaces or edit production rows ad hoc. |
| Role changes are only logged, and there are no invitations or removals | Keep signup disabled in production; bootstrap a trusted owner, promote members deliberately, use reader/operator tokens for automation, and add durable audit records plus invitation/removal flows before multi-user collaboration is promised. |
| SSH target inputs can reach unintended networks | Design explicit egress/address policy and DNS-safe dialing before discovery/import or broad execution. |
| Per-process SSH limits multiply across replicas | Use fleet-level admission control only if measurements show the local bound is inadequate; until then, document aggregate capacity as replicas × process limit. |
| Event publication can be lost after host persistence | Add transactional outbox only if reconnection events become required for correctness, not just as speculative infrastructure. |
| Test suite is sparse and one legacy history test is stale | Repair the stale test and add repository/use-case coverage before expanding execution features. |
| Scale target is asserted without evidence | Baseline 100-host import/list/check/execute scenarios and record latency, resource use, error rates, and environment before making claims. |

## Definition of done for roadmap changes

A milestone is complete only when its acceptance criteria and required tests pass, documentation matches shipped behavior, security limitations are explicit, migrations are reversible or have a documented recovery plan, and `ChangeLogAI.md` records the actual update and checks.
