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
| Unauthenticated API use | Protected `/api/v1` routes validate JWT signature, issuer, age, expiry, and algorithm. Signup defaults off in production and can be explicitly enabled with `MACHINARCHY_SIGNUP_ENABLED=true`. | An operator must bootstrap the first trusted owner deliberately; an enabled signup endpoint creates isolated workspaces but does not replace an invitation/account lifecycle. |
| Broken object-level authorization | Authenticated users have workspace membership; host, environment, job, and metrics-history storage queries are scoped to the principal's workspace. Cross-workspace host/environment references are rejected. | Human membership management is not implemented. Each account currently owns its own workspace. PostgreSQL integration and complete HTTP denial-path coverage remain outstanding. |
| Over-privileged automation credentials | API tokens are workspace-bound, limited to `operator` or `reader`, and effective permissions are intersected with the issuing user's current workspace role. Token management requires a human owner session. | No service-account/member lifecycle or per-token permission customization exists. Tokens inherit the issuer's identity for audit attribution; durable audit events are not yet implemented. |
| SSH man-in-the-middle | Host registration requires an expected SHA-256 fingerprint and SSH host-key verification fails closed on mismatch. | Fingerprint acquisition, rotation, operator review, and API-level validation need a complete lifecycle. |
| Credential disclosure | Storage adapters encrypt SSH credential fields; host credential fields are excluded from ordinary JSON serialization. | Protect DB files, backups, encryption keys, debug dumps, and all error/log paths. Rotation and external secret references are not implemented. |
| Arbitrary internal network reachability / SSRF | API rate limiting and SSH concurrency bounds constrain request volume and parallel dials. | Host addresses are caller-controlled and there is no explicit allow/deny policy or DNS rebinding defense. Restrict network egress and access to trusted operators; design policy before imports or execution. |
| Resource exhaustion | HTTP body limit, rate limiter, seven-second host-registration timeout, SSH semaphore, output cap, database pool limits. | Limits are partly process-local; replicas multiply capacity. Host/environment lists are unpaginated. Import, job queue, and fleet-wide admission controls do not exist. |
| Command injection / remote command abuse | There is no public API endpoint that submits arbitrary commands. Registration only validates connectivity and does not execute a command. SSH client command helper uses a bounded output buffer. | Do not expose command execution until argument semantics, authorization, approval/preview, audit, timeout, and per-host result contracts are designed. |
| Audit tampering or missing attribution | Structured server logs and job `triggered_by` field exist. | No durable audit trail for sensitive inventory mutations; log fields/retention are not an audit guarantee. |
| Database and event failures | Context-aware SQL operations, startup migrations, DB readiness, NATS JetStream readiness, and graceful process shutdown exist. | Host persistence and event publishing are not atomic; no outbox/replay reconciliation currently closes the gap. |

## Immediate deployment guidance

1. In production, leave `MACHINARCHY_SIGNUP_ENABLED` unset or set it to `false`. If account registration is required, enable it deliberately and disable it afterward.
2. Keep signup disabled in production. Each account currently owns an isolated workspace, but public signup is not a substitute for an invitation and account lifecycle process.
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

New accounts receive a private workspace and owner membership. Human JWT authorization reloads the membership role on each request, so deleted memberships and role downgrades take effect without waiting for the JWT to expire. Current API-token creation is restricted to `operator` and `reader`; token creation, listing, and revocation require an owner authenticated by a human JWT session. A token's effective role is the less-privileged of its configured role and the current membership role.

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

This implementation is not yet a full collaborative organization system: there are no invitations, workspace member management, workspace switching, or durable audit events. Do not promise multi-user collaboration until those flows exist and are tested.

### Existing database migration

Migration 000003 assigns pre-existing hosts, environments, jobs, and metrics history to workspace 1. The earliest existing user (lowest database ID) becomes that workspace's owner. Each later existing user gets a new empty workspace and owner membership. If there are no existing users, the first account created claims workspace 1. This preserves all records while removing shared access for later accounts.

Before upgrading an existing installation, inspect the user IDs and confirm the earliest account is the intended owner of legacy records. Take and verify a database backup, rehearse the upgrade on a copy, and keep the original backup until application behavior is confirmed. The migration framework does not run down migrations; recovery is restore-from-backup or a reviewed forward migration. Do not manually change workspace IDs in production without a reviewed data migration.

Do not rely on hidden UI controls, unguessable IDs, or disabling signup as the authorization boundary. Repository scoping remains authoritative even when the CLI or UI hides resources.

## Required SSH target policy

Machinarchy intentionally connects to private infrastructure, so blanket rejection of private IP ranges is not appropriate. Before host discovery/import or fleet execution, define configurable allowed destination CIDRs and explicit treatment for loopback, link-local, multicast, unspecified, metadata, and public addresses. Resolve hostnames under policy and ensure the validated address is the address actually dialed; otherwise DNS rebinding can bypass the check. Apply egress restrictions at the network layer as defense in depth.

## Execution and audit gate

Command execution is not currently exposed through the API. Before adding it, require scoped authorization, exact target preview, bounded concurrency/timeouts/output, cancellation, nonzero per-host failure reporting, an explicit safe command/argument model, durable initiator/target/command-reference/outcome audit, secret redaction, and defined retry/idempotency semantics. Do not execute commands during host registration or import.

## Validation status

SQLite integration coverage verifies fresh migrations, separate account workspaces, scoped environments/jobs/metrics, cross-workspace host-environment reference rejection, and API-token resolution/revocation. Role permission tests and an API-token HTTP middleware test verify least privilege and bearer-only use. These tests do not demonstrate PostgreSQL behavior, exhaustive endpoint authorization, SSH network policy, a full account lifecycle, or resilience under fleet load. See [the roadmap](ROADMAP.md) for follow-up evidence.
