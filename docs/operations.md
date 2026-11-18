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
| `MACHINARCHY_SHUTDOWN_TIMEOUT` | One budget (default 25s) for HTTP drain, worker drain and closing the database. Keep it below the orchestrator grace period. |
| `MACHINARCHY_EXECUTION_ENABLED` / `_ALLOWED_PROGRAMS` | Operations stay disabled until both are set; the program list is re-checked when each Operation runs. |
| `MACHINARCHY_SSH_*` | Egress policy: private ranges, loopback and all ports are denied unless allowed. |
| `MACHINARCHY_AGENT_GATEWAY_*` | See [agent gateway](#agent-gateway). Off unless `_NATS_URL` is set. |

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
| `machinarchy_operationworker_operations_in_progress`, `_targets_total{state}`, `_target_duration_seconds`, `_panics_total` | Command execution. |
| `machinarchy_agent_gateway_registrations_total{outcome}`, `_lease_renewals_total{outcome}`, `_results_total{status}`, `_commands_total{type,outcome}` | Agent traffic; `invalid` counts unauthenticated messages. |

Suggested alerts: readiness failing for 5 minutes; `queue_depth` above 1000
for 10 minutes; any increase of `operationworker_panics_total`; a rate of
`agent_gateway_*_total{outcome="invalid"}` above zero (forged or misconfigured
agents); `lease_renewals_total{outcome="error"}` above zero.

Traces: HTTP spans through OpenTelemetry when `OTEL_EXPORTER_OTLP_ENDPOINT`
is set. Logs: JSON (`log/slog`) on stdout; every error log carries the cause
under `"error"`.

## Timeouts

| Bound | Value |
|---|---|
| HTTP read header / read / write / idle | 5s / 15s / 30s / 60s |
| API request context (database and gateway calls) | 25s |
| Synchronous agent call | 15s default, 20s maximum |
| Per-host command | `timeoutSeconds` of the Operation (max 900s) |
| Reconcile | runtime configuration `reconciliation.timeoutSeconds` |
| Shutdown | `MACHINARCHY_SHUTDOWN_TIMEOUT` |

## Upgrades and schema

The schema is one baseline per dialect. While the project is in development a
release may squash it again; the process then refuses an older database
(`ErrIncompatibleSchema`) and it must be recreated. Take a backup before any
upgrade. After the first production release, schema changes will be additive
migrations.
