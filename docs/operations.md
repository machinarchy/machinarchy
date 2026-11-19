# Operations guide

How to configure, deploy, observe and stop Machinarchy. For failure diagnosis
see the [runbook](runbook.md).

## Processes

| Binary | Serves | Background work |
|---|---|---|
| `machinarchy-api` | `/api`, `/healthz*`, `/metrics`, `/openapi.json`, `/scalar` | none |
| `machinarchy-controlmanager` | `/healthz*`, `/metrics` only | Host reconcilers (outbox), operation worker, agent gateway |
| `machinarchy-monolit` | everything the API serves | everything the Control Manager runs |

Run several API replicas behind a load balancer against PostgreSQL. Run the
Control Manager once per database for SQLite, and one or more for PostgreSQL
(fenced leases make replicas safe). `POST /api/v1/agents/{id}/invoke` works
only in a process that runs the agent gateway (Control Manager role or the
monolith); the standalone API answers 503.

## Configuration

Configuration comes from environment variables, a JSON file (`--config FILE`)
and non-secret flags; precedence is flags, environment, file, defaults.
Everything is validated at startup and the process exits with a message
naming the setting. `.env.example` lists every setting.

Secrets are accepted only from the environment or from files with permissions
`0600` or stricter, never from flags or the JSON file:
`MACHINARCHY_OPERATOR_TOKEN`, `MACHINARCHY_ENCRYPTION_SECRET[_FILE]`, the
keyring file, and the agent gateway key and enrollment token.

| Setting | Notes |
|---|---|
| `MACHINARCHY_ENVIRONMENT` | `production` (default) enforces secure cookies, explicit `CORS_ALLOWED_ORIGINS`, PostgreSQL `sslmode=verify-full` and TLS to NATS. |
| `MACHINARCHY_METRICS_LISTEN_ADDR` | Serve `/metrics` only on this address (for example `:9090`), not on the API port. Recommended in production; scrape this port from inside the cluster only. |
| `MACHINARCHY_API_DOCS_ENABLED` | Serve `/openapi.json` and the `/scalar` reference. Default `true` in development, `false` in production. |
| `MACHINARCHY_SESSION_TTL` | Login session lifetime, default `24h` (5m–168h). See [sessions](#login-sessions). |
| `MACHINARCHY_SESSION_PREVIOUS_KEYS` | Secret. Earlier `MACHINARCHY_OPERATOR_TOKEN` values (comma-separated) still accepted to verify existing sessions during a key rotation. |
| `MACHINARCHY_SHUTDOWN_TIMEOUT` | One budget (default 25s) for HTTP drain, worker drain and closing the database. Keep it below the orchestrator grace period. |
| `MACHINARCHY_EXECUTION_ENABLED` / `_ALLOWED_PROGRAMS` | Operations stay disabled until both are set; the program list is re-checked when each Operation runs. |
| `MACHINARCHY_SSH_*` | Egress policy: private ranges, loopback and all ports are denied unless allowed. |
| `MACHINARCHY_SSH_HOST_KEY_POLICY` | Host-key trust for profiles without their own policy: `tofu` (development default), `approve` (production default) or `pinned`. See [SSH host keys](#ssh-host-keys). |
| `MACHINARCHY_AGENT_GATEWAY_*` | See [agent gateway](#agent-gateway). Off unless `_NATS_URL` is set. |

### Login sessions

Every login creates a session on the server. The session token (cookie or
bearer) carries the session's ID and is accepted only while that session is
active, so ending a session takes effect at once:

| Action | API | Effect |
|---|---|---|
| Log out | `POST /api/auth/logout` | Ends the session of the token sent and clears the cookie |
| List my sessions | `GET /api/v1/sessions` | Active sessions with time, client IP and user agent; `current` marks this one |
| End one session | `DELETE /api/v1/sessions/{id}` | E.g. a lost laptop |
| Log out everywhere | `DELETE /api/v1/sessions` | Ends every session of the caller, this one included |
| Change my password | `POST /api/v1/me/password` | Needs the current password; ends every session of the caller |

These need a login session: API tokens are refused, and they are not
affected by logout or a password change (revoke them with
`DELETE /api/v1/tokens/{id}`). A user keeps at most 50 active sessions; a new
login past that ends the oldest. Logout, session ends and password changes
are audited (`auth.session_revoked`, `auth.sessions_revoked`,
`user.password_changed`).

**Rotating the signing key.** Sessions are signed with
`MACHINARCHY_OPERATOR_TOKEN`, and each token names its key (`kid`).

1. Set a new `MACHINARCHY_OPERATOR_TOKEN`.
2. Set `MACHINARCHY_SESSION_PREVIOUS_KEYS` to the old value, and restart.
   Existing sessions keep working, and new ones use the new key.
3. After `MACHINARCHY_SESSION_TTL` has passed, remove the old value and
   restart.

To end every session at once instead, rotate without listing the old key.

**Upgrading to this release ends existing sessions once:** tokens issued before
it carry no session ID. Log in again.

### Agent gateway

```sh
openssl rand -base64 32 > /etc/machinarchy/agent-signing.key   # Ed25519 seed
chmod 600 /etc/machinarchy/agent-signing.key
```

| Variable | Purpose |
|---|---|
| `MACHINARCHY_AGENT_GATEWAY_NATS_URL` | `tls://host:4222` (production) or `nats://` (development). No credentials in the URL. |
| `MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE` | Base64 32-byte Ed25519 seed. Its public key is logged at startup and served at `GET /api/v1/agents/controller-key`; agents pin it (`MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY`). |
| `MACHINARCHY_AGENT_GATEWAY_ENROLLMENT_TOKEN_FILE` | **Development only** (refused in production): a shared token any host can enrol with. Normally hosts use per-host tokens; see [enrolling a host](#enrolling-a-host). |
| `MACHINARCHY_AGENT_GATEWAY_NATS_CREDENTIALS_FILE`, `_NATS_CA_FILE`, `_NATS_CERT_FILE`, `_NATS_KEY_FILE` | NATS authentication and TLS. |
| `MACHINARCHY_AGENT_GATEWAY_LEASE_TTL` / `_HEARTBEAT` | Default 60s / 15s; the heartbeat must be at least 5s and at most half the TTL. |
| `MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT` | Public key (`A...`) of the NATS account the agents use. With the signing key below, every agent gets its own NATS user JWT. **Required in production.** |
| `MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE` | Seed (`SA...`, file `0600`) of a signing key listed in that account's JWT. The gateway signs agent JWTs with it. |
| `MACHINARCHY_AGENT_GATEWAY_AGENT_JWT_TTL` | Agent JWT lifetime, default `1h` (5m–24h, at least twice the heartbeat). Renewals refresh it. After a revocation, the agent keeps NATS access only until its current JWT expires. |

#### NATS in operator mode

Production runs NATS in operator (JWT) mode, so each party can reach only its
own subjects. Generate a setup once:

```sh
go run ./deployments/nats/generate -out ./nats-auth
docker run -d --name nats -p 4222:4222 -v "$PWD/nats-auth:/etc/nats:ro" \
  nats:2.15.3-alpine -c /etc/nats/nats-server.conf
```

It writes, all `0600`:

| File | Use |
|---|---|
| `nats-server.conf` | Operator, system account and the `MACHINARCHY` account (JetStream on), preloaded in a memory resolver. Enable the `tls` block for production. |
| `controller.creds` | The agent gateway (`MACHINARCHY_AGENT_GATEWAY_NATS_CREDENTIALS_FILE`). |
| `keys/account-signing.nk` | The gateway signs per-agent JWTs with it (`MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE`). |
| `bootstrap.creds` | Every agent host (`MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE`), used to enrol and to recover an expired JWT. |
| `machinarchy.env`, `agent.env` | The settings above, ready to copy. |
| `keys/operator.nk`, `keys/sys.nk`, `keys/machinarchy.nk` | Keep offline; needed only to change accounts. |

The permissions, if you create the users with `nsc` instead:

| User | Publish | Subscribe | Other |
|---|---|---|---|
| controller | `machinarchy-agent.v1.>`, `$JS.API.>` | `machinarchy-agent.v1.register`, `machinarchy-agent.v1.lease.renew`, `machinarchy-agent.v1.agent.*.result`, `_INBOX_CONTROLLER.>` | allow responses (1 message) |
| bootstrap | `machinarchy-agent.v1.register`, `machinarchy-agent.v1.lease.renew` | `_INBOX.>` | — |
| each agent (issued by the gateway) | register, renew, its own result subject, `$JS.API.CONSUMER.INFO` / `MSG.NEXT` and `$JS.ACK` for its own consumer | `_INBOX_AGENT.<agent-id>.>` | expires after `MACHINARCHY_AGENT_GATEWAY_AGENT_JWT_TTL` |

The lists come from `agentgateway.ControllerPermissions` and
`agentv1.BootstrapPermissions` / `AgentPermissions`.

#### Enrolling a host

An owner issues one token per host. It is single-use, expires after 1 hour
by default (at most 24 hours), and can be bound to the host's name:

```sh
curl -s -X POST https://machinarchy.example/api/v1/agents/enrollment-tokens \
  -H "Authorization: Bearer $OWNER_SESSION" -H 'Content-Type: application/json' \
  -d '{"hostname":"web-01.example","ttlSeconds":3600}' | jq -r .token
```

The secret (`mae_...`) is shown only in that response; only its SHA-256 is
stored. Put it on the host as `MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE`
(`0600`). The agent uses it once; retries of the same registration need no
new token. `GET /api/v1/agents/enrollment-tokens` lists tokens with their
state (never the secret), and `DELETE /api/v1/agents/enrollment-tokens/{id}`
revokes an unused one. Issuing and revoking are audited, and an agent's
`agent.registered` event names the token it used.

The gateway provisions the `MACHINARCHY_AGENT_COMMANDS` JetStream stream (file
storage, 24h max age, 15 min duplicate window) and one durable consumer per
agent. JetStream must be enabled on the NATS server.

## SSH host keys

A Host does not need a hand-entered fingerprint. The Host controller scans its
SSH host keys before anything authenticates; the scan sends no credential and
follows the egress policy. The Access Profile's `hostKeyPolicy` then decides:

| Policy | What happens | Use |
|---|---|---|
| `tofu` | The keys seen on first contact (every algorithm the host offers) are pinned automatically and audited as `resource_host.host_key_trusted` with `source=tofu`. Allowed only with private-key authentication; a password profile falls back to `approve`. | Networks you trust at enrolment time |
| `approve` | The Host waits with `HostKeyPendingApproval` and `status.observedHostKeys`. An owner runs `machinarchy trust host NAME`, or `--all-pending [--selector …]` for many hosts, which shows the fingerprints and trusts exactly those. | Production default |
| `ca` | Hosts present SSH host certificates signed by a CA listed in the profile's `hostCertificateAuthorities` (authorized_keys format), for the dialed host name. No per-host data. | Fleets with an SSH CA |
| `pinned` | Only fingerprints set in `spec.accessTarget.trust` are trusted, and nothing is scanned. | Keys verified out of band |

A profile without a policy uses `MACHINARCHY_SSH_HOST_KEY_POLICY`: `tofu` in
development and `approve` in production by default, or `pinned`.

**Safety rules:**
- **A changed key is never accepted automatically.** If a host presents a key
  that is not trusted, the Host becomes `HostKeyChanged`, and the condition
  names both the presented and the trusted fingerprints. Nothing is sent to
  the host. An owner must check the change out of band and run
  `machinarchy trust host NAME --replace`.
- **Moving a Host clears its trust.** Changing a Host's endpoint (owner only)
  clears its trusted keys, unless new ones are sent with the change, and the
  new endpoint is scanned again.
- **Approval names exact keys.** It sends the exact fingerprints the owner
  reviewed and the spec generation they saw, so a key that changed in the
  meantime is not trusted by mistake.
- **A scan is all or nothing.** It opens one connection per key type
  (Ed25519, ECDSA, RSA). If one of them still fails after a retry, the scan
  fails and nothing is pinned or offered for approval. An incomplete set would
  break later connections that negotiate the missing type.

**Scans are rate limited.** Each scan connection ends before authentication.
OpenSSH 9.8 and later (`PerSourcePenalties`, `noauth`) penalises an address
that makes many such connections and then refuses all of them for a while,
including real logins.

The controller therefore reuses a Host's last scan for 10 minutes while the
Host waits for approval or reports `HostKeyChanged`. A spec change, such as
an approval or a new endpoint, triggers a fresh scan right away.

If a Host shows `ConnectionFailed`, and sshd logs `srclimit_penalise` for the
controller's address, wait out the penalty. Do not restart the controller in a
loop. To exempt the controller, use `PerSourcePenaltyExemptList` in
`sshd_config`.

## Detached execution

A run with `detach: true` (the CLI's default without `--wait`) does not keep an SSH session open.

**How it runs:**
- One short session per host starts the command under a supervisor and returns at once.
- The worker's poller then reads the state and output with short sessions: one per host for all of that host's targets, every 5 s for the first minute, then less often, up to every 30 s.
- A run may last up to 24 hours (`timeoutSeconds` up to 86400; attached runs keep 900 s).
- Targets launch `concurrency` at a time, so 200 hosts × a 5-minute command take about 5 minutes.

**Nothing is installed on the host.** Each session sends a small POSIX `sh` script, so the login shell must be POSIX (sh, bash, dash, ash). It keeps one directory per target under `$HOME/.local/state/machinarchy/ops/` (owner-only) holding:
- the command;
- `stdout` and `stderr`, capped by `ulimit -f` at the profile's `execution.maxOutputFileBytes` (100 MiB by default);
- the exit status.

A finished target's directory is removed. Leftovers from a crash are swept after 7 days, at the next launch on that host.

**Supervisor**, per Access Profile `execution.supervisor`:

| Value | Behaviour |
|---|---|
| `auto` (default) | `systemd-run` when usable, otherwise `setsid` |
| `systemd` | Always `systemd-run`: as root on the system manager, otherwise the login user's manager, which needs lingering (`loginctl enable-linger USER`) or the unit stops when the session ends |
| `setsid` | Always `setsid`/`nohup` |
| `attached` | Detached runs are refused with `detached_disabled_by_profile` |

Prefer `systemd`: cancel and timeout then also kill processes that left the command's process group (a daemon that double-forks), through the unit's cgroup. Under `setsid` such processes survive; this is a known limitation. On hosts with `KillUserProcesses=yes` in logind, non-root runs need lingering in both modes.

**Timeouts and cancel** are enforced twice:
- on the host, by the supervisor's watchdog (TERM, then KILL after 10 s) and systemd's `RuntimeMaxSec`;
- by the poller, which sends a cancel if a command is still running 60 s past its deadline.

**Lost.** A target ends `Lost` when its outcome cannot be known. It is never retried:

| Error class | Cause |
|---|---|
| `supervisor_lost` | The supervisor died without recording an exit: a reboot, the OOM killer, or the unit stopped |
| `state_missing` | The target directory disappeared |
| `unreachable` | The host stayed unreachable past the deadline plus 5 minutes |
| `unreachable_during_cancel` | The host stayed unreachable for 5 minutes after a cancel |

**Restarts are safe.**
- Commands keep running on the hosts while workers restart. Nothing is marked `Interrupted`, and the poller picks the targets up again.
- A launch whose reply was lost is adopted if the command started, and launched again (up to 3 attempts) if it provably did not.
- A command never starts twice: the runner takes a `mkdir` lock per target.

**Output.**
- Each stream keeps its first and last 15 KiB plus the total size.
- `GET …/operations/{id}/targets/{hostId}/output?stream=&from=` serves it by byte offset, live while the command runs. `machinarchy logs operation ID --follow` uses it.
- New output appears at the poll interval: 5 s for the first minute, then up to 30 s.
- Only callers allowed to run commands can read output; readers see the `progress` sizes only.

**Avoid changing a Host during a run.** A change to the Host's spec (endpoint, trust) means the credential check no longer matches the Operation, so polls stop and the target ends `Lost`.

## Containers

```sh
make images AGENT_SOURCE=../machinarchy-agent   # api, controlmanager, monolit, cli
```

- Multi-stage BuildKit builds from digest-pinned bases; distroless, non-root
  (65532), `STOPSIGNAL SIGTERM`.
- Reproducible: `-trimpath -buildvcs=false`, version, commit and commit time
  through ldflags (`machinarchy version`, and `buildinfo` in logs).
- `HEALTHCHECK` runs `/machinarchy healthcheck`, which probes the process's
  own `/healthz/ready`.
- `/data` is created `0700` and owned by the runtime user; the SQLite store
  refuses looser permissions. Mount a volume there for SQLite.
- The build needs the agent protocol as a named context:
  `--build-context agent-protocol=../machinarchy-agent/protocol`.

`docker compose up` (`docker-compose.yml`) sets `stop_grace_period: 30s`.

## Kubernetes probes

```yaml
livenessProbe:
  httpGet: {path: /healthz/live, port: 8080}
readinessProbe:
  httpGet: {path: /healthz/ready, port: 8080}
terminationGracePeriodSeconds: 30   # > MACHINARCHY_SHUTDOWN_TIMEOUT
```

Liveness only reports whether the process is stopping. Readiness checks the
database and, where they run, the Control Manager's startup recovery and the
agent gateway's NATS connection: an outage makes the pod unready without
restarting it.

## Security headers

Every response carries `X-Content-Type-Options: nosniff`,
`X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, same-origin
`Cross-Origin-Opener-Policy` and `-Resource-Policy`, and a deny-all
`Content-Security-Policy`. In production, responses also carry
`Strict-Transport-Security` (one year, subdomains): terminate TLS in front of
machinarchy and do not serve it on plain HTTP. Responses under `/api` add
`Cache-Control: no-store`.

The API reference page (`/scalar`) has its own policy. It allows only scripts
from this origin and the version-pinned Scalar file on cdn.jsdelivr.net, and
no inline scripts. Scalar's web fonts are turned off, so no other third-party
host is contacted.

## Metrics

Every process serves `/metrics`: on the API port by default, or only on
`MACHINARCHY_METRICS_LISTEN_ADDR` when that is set (API and monolith). The
Control Manager's operations listener is never public.

| Metric | Meaning |
|---|---|
| `machinarchy_http_requests_total`, `_http_request_duration_seconds` | API traffic by method, route template, status. |
| `machinarchy_db_connections_*`, `_db_connection_waits_total` | Database pool. Rising waits: pool too small or slow queries. |
| `machinarchy_controlmanager_reconciles_total{controller,outcome}` | `success`, `retry` (re-queued with backoff), `permanent_error`. |
| `machinarchy_controlmanager_reconcile_duration_seconds` | Per-controller latency. |
| `machinarchy_controlmanager_queue_depth` | Keys waiting; sustained growth means reconciles are slower than changes. |
| `machinarchy_controlmanager_outbox_deliveries_total{outcome}` | `claimed`, `quarantined` (invalid events). |
| `machinarchy_controlmanager_host_key_events_total{event}` | Hosts entering `pending_approval` or `changed` (an untrusted key), and TOFU pins (`tofu_pinned`); once per transition, not per retry. |
| `machinarchy_operationworker_operations_in_progress`, `_targets_total{state}`, `_target_duration_seconds`, `_panics_total` | Command execution. |
| `machinarchy_operationworker_running_targets`, `_detached_launches_total{outcome}`, `_polls_total{outcome}`, `_poll_duration_seconds`, `_lost_targets_total` | Detached runs: targets running on hosts; launches `confirmed`, `unconfirmed`, `relaunched` or `failed`; polls `ok` or `unreachable`; targets ended `Lost`. |
| `machinarchy_agent_gateway_registrations_total{outcome}`, `_lease_renewals_total{outcome}`, `_results_total{status}`, `_commands_total{type,outcome}` | Agent traffic; `invalid` counts unauthenticated messages. |

Suggested alerts: readiness failing for 5 minutes; `queue_depth` above 1000
for 10 minutes; any increase of `operationworker_panics_total`; a rate of
`agent_gateway_*_total{outcome="invalid"}` above zero (forged or misconfigured
agents); `lease_renewals_total{outcome="error"}` above zero; any increase
of `host_key_events_total{event="changed"}`; any increase of
`lost_targets_total`; a sustained rate of `polls_total{outcome="unreachable"}`.

Traces: HTTP spans through OpenTelemetry when `OTEL_EXPORTER_OTLP_ENDPOINT`
is set. Logs: JSON (`log/slog`) on stdout; every error log carries the cause
under `"error"`.

## Timeouts

| Bound | Value |
|---|---|
| HTTP read header / read / write / idle | 5s / 15s / 30s / 60s |
| API request context (database and gateway calls) | 25s |
| Synchronous agent call | 15s default, 20s maximum |
| Per-host command | `timeoutSeconds` of the Operation: max 900s attached, 86400s detached |
| Reconcile | runtime configuration `reconciliation.timeoutSeconds` |
| Shutdown | `MACHINARCHY_SHUTDOWN_TIMEOUT` |

## Upgrades and schema

The schema is one baseline per dialect. While the project is in development a
release may squash it again; the process then refuses an older database
(`ErrIncompatibleSchema`) and it must be recreated. Take a backup before any
upgrade. After the first production release, schema changes will be additive
migrations.

Migrations run at startup within `MACHINARCHY_DATABASE_MIGRATION_TIMEOUT`
(10 minutes by default), separately from `MACHINARCHY_DATABASE_CONNECT_TIMEOUT`.

On SQLite, migration `000004` rewrites `operation_targets` because SQLite
cannot change a CHECK constraint, so it takes time proportional to the stored
output. An 834 MB database (100,000 targets) took 3 minutes on a laptop.
Raise the timeout for larger databases. PostgreSQL changes the constraint in
place.
