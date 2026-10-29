# Security Assessment

This assessment describes the repository state on 2026-10-26. It is an engineering threat model, not a claim of certification or a complete security audit. Reassess it before adding remote execution, fleet imports, or agents.

## Assets and trust boundaries

- **SSH credentials and host trust data:** passwords, private keys, passphrases, and pinned host-key fingerprints stored by the database adapter.
- **Managed hosts:** remote systems reachable from the Machinarchy API process over SSH.
- **Inventory and operational records:** hosts, environments, job output, and metric history.
- **Account and signing material:** user password hashes, JWT signing secret, and API-token secrets. API tokens are stored only as SHA-256 hashes.
- **Control-plane infrastructure:** SQLite/PostgreSQL, NATS JetStream, HTTP listeners, telemetry, and deployment secrets.

Trust boundaries are the public HTTP listener, authenticated API routes, database/broker connections, and outbound SSH connections. The operator controls the deployment network and secrets; an API caller may submit host addresses and SSH authentication data. Remote SSH servers and their output are untrusted.

## Threats and current controls

| Threat | Current control | Remaining gap |
| --- | --- | --- |
| Unauthenticated API use | Protected `/api/v1` routes validate JWT signature, issuer, age, expiry, and algorithm. Signup defaults off in production and can be explicitly enabled with `MACHINARCHY_SIGNUP_ENABLED=true`. | An operator must bootstrap the first trusted owner deliberately; an enabled signup endpoint creates read-only accounts in the shared workspace and does not replace an invitation/account lifecycle. |
| Broken object-level authorization | Authenticated users have workspace membership; host, environment, job, and metrics-history storage queries are scoped to the principal's workspace. Cross-workspace host/environment references are rejected. | Owners can list members and change roles (`/api/v1/members`); there are no invitations or member removal. All new accounts share one workspace. A PostgreSQL contract suite exists but has not yet been run green, and complete HTTP denial-path coverage remains outstanding. |
| Over-privileged automation credentials | API tokens are workspace-bound, limited to `operator` or `reader`, and effective permissions are intersected with the issuing user's current workspace role. Token management requires a human owner session. | No service-account/member lifecycle or per-token permission customization exists. Tokens inherit the issuer's identity for audit attribution; durable audit events are not yet implemented. |
| SSH man-in-the-middle | Host registration requires an expected SHA-256 fingerprint and SSH host-key verification fails closed on mismatch. | Fingerprint acquisition, rotation, operator review, and API-level validation need a complete lifecycle. |
| Credential disclosure | Storage adapters encrypt SSH credential fields; host credential fields are excluded from ordinary JSON serialization. | Protect DB files, backups, encryption keys, debug dumps, and all error/log paths. Rotation and external secret references are not implemented. |
| Arbitrary internal network reachability / SSRF | Every SSH dial goes through `pkg/netpolicy`: hostnames are resolved once, every answer must be permitted, and the validated IP is the address dialed (DNS-rebinding safe). Loopback, link-local, metadata, multicast, unspecified, and zoned addresses are denied by default; private ranges and explicit allow/deny CIDRs are configurable. Literal IPs are rejected before any SSH capacity is spent. | The policy limits destinations but is not a network control: keep egress restrictions at the network layer. Allowed private ranges remain reachable by anyone who can register hosts (owners/operators). Host updates are not re-verified when saved; the policy is enforced the next time the host is dialed. No port restrictions. |
| Resource exhaustion | HTTP body limit, rate limiter, seven-second host-registration timeout, SSH semaphore, output cap, database pool limits. | Limits are partly process-local; replicas multiply capacity. Host/environment lists are unpaginated. Import, job queue, and fleet-wide admission controls do not exist. |
| Command injection / remote command abuse | There is no public API endpoint that submits arbitrary commands. Registration only validates connectivity and does not execute a command. SSH client command helper uses a bounded output buffer. | Do not expose command execution until argument semantics, authorization, approval/preview, audit, timeout, and per-host result contracts are designed. |
| Audit tampering or missing attribution | Structured server logs and job `triggered_by` field exist. | No durable audit trail for sensitive inventory mutations; log fields/retention are not an audit guarantee. |
| Database and event failures | Context-aware SQL operations, startup migrations, DB readiness, NATS JetStream readiness, and graceful process shutdown exist. | Host persistence and event publishing are not atomic; no outbox/replay reconciliation currently closes the gap. |

## Immediate deployment guidance

1. In production, leave `MACHINARCHY_SIGNUP_ENABLED` unset or set it to `false`. If account registration is required, enable it deliberately and disable it afterward.
2. Keep signup disabled in production. New accounts join the shared workspace as read-only members and owners promote them through the members API, but public signup is not a substitute for an invitation and account lifecycle process.
3. Use a random `MACHINARCHY_OPERATOR_TOKEN` of at least 32 characters and an independent, stable `MACHINARCHY_ENCRYPTION_SECRET`. Back up both through a secret-management process; loss/change of the latter makes encrypted SSH credentials unreadable.
4. Require PostgreSQL `sslmode=verify-full` in production, exact CORS origins, TLS termination with trusted configuration, restrictive database/broker credentials, and private network placement.
5. Apply outbound network policy to the API process. Permit only intended SSH destinations and block cloud metadata and other sensitive control-plane ranges unless the operator has an explicit need and compensating controls.
6. Protect SQLite files, PostgreSQL backups, NATS credentials, `/metrics`, Scalar, and telemetry endpoints. Metrics and diagnostics may reveal topology or operational data.
7. Never send passwords, private keys, passphrases, bearer tokens, or the encryption/JWT secrets in logs, command-line arguments, support bundles, or issue reports.

## Workspace authorization and API tokens

The current baseline uses workspace memberships and three roles:

- **Owner:** full workspace permissions, including API-token management.
- **Operator:** inventory read/write and operations read.
- **Reader:** inventory and operations read-only.

New accounts join the shared default workspace as `reader`; the first owner comes from the bootstrap configuration and owners promote others with `PUT /api/v1/members/{id}/role`. Human JWT authorization reloads the membership role on each request, so deleted memberships and role downgrades take effect without waiting for the JWT to expire. Current API-token creation is restricted to `operator` and `reader`; token creation, listing, and revocation require an owner authenticated by a human JWT session. A token's effective role is the less-privileged of its configured role and the current membership role.

API tokens use a random `mch_pat_` secret, are returned only when created, and are stored as SHA-256 hashes. They default to a 30-day lifetime, have a maximum lifetime of 365 days, and can be revoked. Token resolution checks expiry and revocation against storage on every request. Treat the plaintext token like a password: store it in a secret manager, send it only in an HTTPS `Authorization: Bearer` header, and never put it in source control, URLs, shell history, logs, or support bundles. Rotate and revoke immediately after suspected exposure. Cookie authentication is not accepted for API-token credentials.

Create a read-only token with a human owner JWT, then use it for API automation:

The shell variables below should be injected from your secret manager at runtime; do not persist token values in shell startup files or project `.env` files.

```sh
curl -X POST http://localhost:8080/api/v1/tokens \
  -H "Authorization: Bearer $MACHINARCHY_USER_JWT" \
  -H 'Content-Type: application/json' \
  -d '{"name":"ci-read","role":"reader","expiresInHours":168}'

curl http://localhost:8080/api/v1/hosts \
  -H "Authorization: Bearer $MACHINARCHY_API_TOKEN"

curl -X DELETE http://localhost:8080/api/v1/tokens/123 \
  -H "Authorization: Bearer $MACHINARCHY_USER_JWT"
```

The first response contains the token's plaintext exactly once. Copy it directly into a secret manager; do not save it in a shell variable or file with broad permissions.

This implementation is not yet a full collaborative organization system: there are no invitations, member removal, workspace switching, or durable audit events (role changes are written to the structured server log only, which is not tamper-resistant). Do not promise multi-user collaboration until those flows exist and are tested.

### Existing database migration

Migration 000003 assigns pre-existing hosts, environments, jobs, and metrics history to workspace 1. The earliest existing user (lowest database ID) becomes that workspace's owner. Each later existing user gets a new empty workspace and owner membership. If there are no existing users, the first account created claims workspace 1. This preserves all records while removing shared access for later accounts. Databases migrated this way keep those per-account workspaces; accounts created after the single-workspace change join workspace 1.

Before upgrading an existing installation, inspect the user IDs and confirm the earliest account is the intended owner of legacy records. Take and verify a database backup, rehearse the upgrade on a copy, and keep the original backup until application behavior is confirmed. The migration framework does not run down migrations; recovery is restore-from-backup or a reviewed forward migration. Do not manually change workspace IDs in production without a reviewed data migration.

Do not rely on hidden UI controls, unguessable IDs, or disabling signup as the authorization boundary. Repository scoping remains authoritative even when the CLI or UI hides resources.

## Fleet targeting

Targeting decides which hosts an operation may touch, so it is deliberately conservative. A target rule must name something: an empty selector, a blank selector, or a rule with only an exclude list is rejected, and targeting every host requires an explicit `all=true` that cannot be combined with a selector or include list, so a typo or a missing parameter can never widen a rule to the whole fleet. Exclude entries always win. Include and exclude entries are resolved by host ID or name inside the caller's workspace only, and entries that match nothing are reported rather than ignored. Selectors have no regular expressions (wildcards are matched without backtracking), and their length, requirement count, set size, and value length are capped, so a hostile selector cannot make evaluation expensive. Label keys and values are validated and bounded when written, and the names of built-in selector fields are reserved so a label cannot shadow `environment` or `status`. Previews copy only non-secret host fields and are limited to 500 listed hosts with an exact total. Previews and listings need inventory read permission; label changes need inventory write permission.

Residual risks: a selector is evaluated against the inventory at the moment of the request, so hosts added or relabeled between a preview and a later execution can change the target set, and execution must therefore re-resolve and compare or pin the previewed host IDs; anyone with inventory write permission can relabel hosts and thereby change what a label-based rule selects, so execution permissions must be separate from labeling; and label changes are not yet recorded in a durable audit trail.

## Member administration

`GET /api/v1/members` and `PUT /api/v1/members/{id}/role` require a human owner session: the routes use the `members:manage` permission (owner only) plus the same human-session guard as token management, and the service repeats both checks, so API tokens can never list or change roles. Roles are `owner`, `operator`, and `reader`. The target must already be a member of the caller's workspace, and the workspace comes from the authenticated principal, never from the request. A workspace cannot be left without an owner: the store enforces this atomically with the update (a single guarded statement in SQLite, a transaction that locks the owner rows in PostgreSQL), so two owners demoting each other concurrently cannot both succeed. Role changes apply on the member's next request, and API tokens never exceed the current membership role. Each change is logged as a structured `audit` event with actor, target, previous role, and new role.

Residual risks: there is no durable, tamper-resistant audit trail yet; there is no step-up authentication or confirmation for promoting an owner; there is no member removal or invitation flow; and an owner can demote themselves while another owner exists.

## SSH target policy

Implemented in `pkg/netpolicy` and applied by `pkg/sshclient` to every outbound SSH connection (host registration, bulk registration, and metrics collection). A caller cannot opt out: when no dialer is supplied the client uses `netpolicy.Default()`.

Rules, in order: unspecified, multicast, broadcast, "this network", zoned, and cloud-metadata addresses (169.254.169.254, 169.254.170.2, 100.100.100.200, fd00:ec2::254) are always denied, even inside an allowed range. Addresses in `MACHINARCHY_SSH_DENIED_CIDRS` are denied. Addresses in `MACHINARCHY_SSH_ALLOWED_CIDRS` are allowed, which is how loopback or link-local ranges are opened deliberately. Loopback is denied unless `MACHINARCHY_SSH_ALLOW_LOOPBACK=true`; link-local is denied; private ranges (RFC 1918, CGNAT, fc00::/7) are allowed unless `MACHINARCHY_SSH_ALLOW_PRIVATE=false`; public addresses are allowed. IPv4-mapped IPv6 addresses are unmapped before checking. A hostname is permitted only if all of its addresses are permitted, and the connection is made to the validated IP rather than the hostname.

Residual risks: the policy is not a substitute for network egress controls; permitted private ranges stay reachable by users who can register hosts; ports are not restricted; the host fingerprint check still decides whether a reachable server is trusted; and `PUT /hosts/{id}` does not re-check the address until the next dial.

## Execution and audit gate

Command execution is not currently exposed through the API. Before adding it, require scoped authorization, exact target preview, bounded concurrency/timeouts/output, cancellation, nonzero per-host failure reporting, an explicit safe command/argument model, durable initiator/target/command-reference/outcome audit, secret redaction, and defined retry/idempotency semantics. Do not execute commands during host registration or import.

## Validation status

SQLite integration coverage verifies fresh migrations, separate account workspaces, scoped environments/jobs/metrics, cross-workspace host-environment reference rejection, and API-token resolution/revocation. Role permission tests and an API-token HTTP middleware test verify least privilege and bearer-only use. These tests do not demonstrate PostgreSQL behavior, exhaustive endpoint authorization, SSH network policy, a full account lifecycle, or resilience under fleet load. See [the roadmap](ROADMAP.md) for follow-up evidence.
