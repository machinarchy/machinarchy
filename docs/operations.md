# Operations guide

How to configure, deploy, observe and stop Machinarchy. For failure diagnosis
see the [runbook](runbook.md).

## Processes

| Binary | Serves | Background work |
|---|---|---|
| `machinarchy-api` | `/api`, `/healthz*`, `/metrics`, `/openapi.json`, `/scalar` | none |
| `machinarchy-controlmanager` | `/healthz*`, `/metrics` only | Host reconcilers (outbox), operation worker, agent gateway |
| `machinarchy-monolith` | everything the API serves | everything the Control Manager runs |

The monolith was called `monolit` before Phase 21. For one release the old names still work: `make build` also writes `bin/machinarchy-monolit` as a symlink, `make run-monolit`, `build-monolit` and `image-monolit` print a deprecation note, and the image build accepts `MACHINARCHY_COMMAND=monolit`. The Compose service is now `monolith`.

Run several API replicas behind a load balancer against PostgreSQL. Run the
Control Manager once per database for SQLite, and one or more for PostgreSQL
(fenced leases make replicas safe). Capability Operations on agent hosts are
carried out by a process that runs the agent gateway (Control Manager role or
the monolith); the standalone API only records them.

## High availability

On PostgreSQL, run two or more API replicas behind a load balancer and two or more Control Managers. They coordinate through fenced leases in the database. A replica that crashes (rather than stopping cleanly) leaves leases that block the others until they expire, which sets the recovery times below. The values were measured with `test/ha` (AUDIT/20).

| Failure | What happens | Recovery |
|---|---|---|
| A Control Manager stops cleanly (`SIGTERM`) | It drains its queue, finishes running reconciles and releases its leases within `MACHINARCHY_SHUTDOWN_TIMEOUT` | Immediate: the others take over |
| A Control Manager crashes during a detached run | Commands keep running on the hosts | The others poll them once the host leases expire, **2 minutes** at most; each target is launched once |
| A Control Manager crashes during an attached run | The SSH sessions it held are gone | The Operation ends `Interrupted` once its lease expires, **2 minutes** at most; the commands may have run |
| A Control Manager crashes with capability Operations in flight | Commands and results are durable; every Control Manager relays and stores them | No wait |
| A Control Manager crashes with host changes queued in memory | Their outbox events were already acknowledged | The next periodic resync re-reads every host. The resync lease lasts 1.25 × `controlManager.resyncSeconds`, and the others retry every quarter interval, so expect about **1.5 × the resync interval** (7.5 minutes with the default 300 s) |
| PostgreSQL restarts | Readiness fails during the outage (the "readiness failing" alert) | Back as soon as the database accepts connections (seconds); running Operations finish |
| One Control Manager loses NATS | Its readiness fails; agents keep working through the other gateways | It reconnects on its own |

The resync interval is `controlManager.resyncSeconds` (default 300). It is a restart-required setting: publish it through the runtime configuration and restart the Control Managers (see "Restart-required settings" below). The 2-minute Operation and host lease is fixed.

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
| `MACHINARCHY_ENVIRONMENT` | `production` (default) enforces secure cookies, explicit `MACHINARCHY_CORS_ALLOWED_ORIGINS`, PostgreSQL `sslmode=verify-full` and TLS to NATS. |
| `MACHINARCHY_KMS_PROVIDER` | Root of trust for the Secret Store: `local` (default, built in: root keys from `MACHINARCHY_ENCRYPTION_SECRET[_FILE]` and the keyring file). External KMS providers are optional adapters added later. New key IDs are written as `local:<MACHINARCHY_ENCRYPTION_KEY_ID>`; IDs stored without the prefix keep working. |
| `MACHINARCHY_METRICS_LISTEN_ADDR` | Serve `/metrics` only on this address (for example `:9090`), not on the API port. Recommended in production; scrape this port from inside the cluster only. |
| `MACHINARCHY_API_DOCS_ENABLED` | Serve `/openapi.json` and the `/scalar` reference. Default `true` in development, `false` in production. |
| `MACHINARCHY_CORS_ALLOWED_ORIGINS` | Comma-separated exact browser origins (`https://console.example.com`). Required in production; in development the console dev server (`http://localhost:5173`, `http://127.0.0.1:5173`). The earlier name `CORS_ALLOWED_ORIGINS` still works. |
| `MACHINARCHY_TRUSTED_PROXIES` | Reverse proxies whose `X-Forwarded-For` is believed: IPs or CIDRs. Empty (default): the TCP peer is the client. See [behind a reverse proxy](#behind-a-reverse-proxy). |
| `MACHINARCHY_RATE_LIMIT_RPS`, `_BURST` | Requests per second and burst per client network on `/api`, defaults `20` and `30`. See [rate limits](#rate-limits). |
| `MACHINARCHY_HTTP_MAX_BODY_BYTES` | Largest request body, default `1048576` (1 MiB). |
| `MACHINARCHY_HTTP_REQUEST_TIMEOUT` | Deadline of one `/api` request, default `25s` (1s–5m). The listener's write timeout is 5s longer. |
| `MACHINARCHY_SESSION_TTL` | Login session lifetime, default `24h` (5m–168h). See [sessions](#login-sessions). |
| `MACHINARCHY_LOGIN_FREE_FAILURES`, `_MAX_BACKOFF`, `_IP_MAX_FAILURES`, `_FAILURE_WINDOW` | Failed-login throttling, defaults `5`, `15m`, `20`, `15m`. See [sessions](#login-sessions). |
| `MACHINARCHY_SESSION_PREVIOUS_KEYS` | Secret. Earlier `MACHINARCHY_OPERATOR_TOKEN` values (comma-separated) still accepted to verify existing sessions during a key rotation. |
| `MACHINARCHY_SHUTDOWN_TIMEOUT` | One budget (default 25s) for HTTP drain, worker drain and closing the database. Keep it below the orchestrator grace period. |
| `MACHINARCHY_EXECUTION_ENABLED` / `_ALLOWED_PROGRAMS` | Operations stay disabled until both are set. The list holds [argument templates](#execution-policy), comma-separated. It seeds the runtime configuration of a new database only; afterwards the policy is changed in the runtime configuration. It is re-checked when each Operation runs. |
| `MACHINARCHY_SSH_*` | Egress policy: private ranges, loopback and all ports are denied unless allowed. |
| `MACHINARCHY_SSH_HOST_KEY_POLICY` | Host-key trust for profiles without their own policy: `tofu` (development default), `approve` (production default) or `pinned`. See [SSH host keys](#ssh-host-keys). |
| `MACHINARCHY_AGENT_GATEWAY_*` | See [agent gateway](#agent-gateway). Off unless `_NATS_URL` is set. |

### Execution policy

The execution policy allows commands, not just programs (D-11). Each entry is a template: the program, then one pattern per argument.

| Entry | Allows |
|---|---|
| `uptime` | `uptime` with no arguments |
| `systemctl is-active {unit}` | exactly `systemctl is-active` and one unit name |
| `df -h {path}` | `df -h` and one absolute path without `..` |
| `journalctl -u {unit} -n {int}` | those words, a unit name and a number |
| `backup.sh *` | `backup.sh` with any arguments |
| `*` | any program with any arguments |

- A literal must equal the argument.
- Placeholders are `{unit}` (letters, digits, `@ _ . : -`), `{word}` (letters, digits, `_ . -`), `{int}` (digits), `{path}` (absolute, no `..`) and `{any}` (any one argument).
- No placeholder matches an argument that starts with `-`, so an option cannot be slipped in where a value is expected.
- A final `*` allows any further arguments: an explicit choice for scripts you trust.
- A program can have several entries; a command must match one of them.
- A literal can't contain spaces, and an entry for one path (`/usr/bin/df`) doesn't cover another (`df`).

A refused command gets HTTP 403 with code `execution_not_allowed`. The message names the program and its allowed entries, never the arguments.

To change the policy on a running deployment, an owner publishes a new runtime configuration revision. Restarting with a different `MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS` does nothing once the database has a configuration:

```sh
curl -s https://machinarchy.example/api/v1/runtime-config -H "Authorization: Bearer $OWNER_SESSION" |
  jq '{expectedRevision: .revision, changeNote: "allow systemctl is-active",
       config: (.config | .operations.allowedPrograms += ["systemctl is-active {unit}"])}' |
  curl -s -X PUT https://machinarchy.example/api/v1/runtime-config -H "Authorization: Bearer $OWNER_SESSION" \
    -H 'Content-Type: application/json' --data-binary @- | jq '{revision, allowed: .config.operations.allowedPrograms}'
```

**Upgrading from a version before Phase 18:** a bare program name used to allow any arguments. It now allows none. If your commands pass arguments, add templates for them, or `PROGRAM *` to keep the old behaviour for that program. Do this before upgrading, or with the command above afterwards.

### Behind a reverse proxy

Behind a load balancer or ingress every connection comes from the proxy, so
without help all clients share one address: one client's failed logins or
burst would block everyone. List the proxies in `MACHINARCHY_TRUSTED_PROXIES`
(for example the ingress pods' CIDR `10.42.0.0/16`). Then:

- `X-Forwarded-For` is read only on connections from a listed proxy;
- it is read from the right, skipping listed proxies, and the first address
  that is not one is the client, so a client cannot choose its address by
  sending the header itself;
- loopback and private ranges are not trusted unless listed.

List only the proxies' own addresses: a range that includes clients lets those
clients pick their address. Configure the proxy to append to, not pass
through, `X-Forwarded-For`. The client address is used for rate limits,
login throttling and the session list.

### Rate limits

Each client network has a token bucket on `/api`: `MACHINARCHY_RATE_LIMIT_BURST`
requests at once, refilled at `MACHINARCHY_RATE_LIMIT_RPS` per second. IPv6
clients count per /64. A request without a token gets 429 with `Retry-After`
(seconds until the next token); there is no escalating ban, so a client that
slows down is served at once.

These counters are in memory, **per replica**: with N replicas a client can
make up to N times as many requests. Failed-login throttling is different: it
is kept in the database and shared (see [sessions](#login-sessions)).

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

**Failed logins are throttled**, never locked, so an attacker cannot lock an
owner out:

- **Per account:** after `MACHINARCHY_LOGIN_FREE_FAILURES` (5) failures within
  `_FAILURE_WINDOW` (15 min), each attempt must wait 1 s, doubling up to
  `_MAX_BACKOFF` (15 min). The correct password waits too.
- **Per client network:** `_IP_MAX_FAILURES` (20) failures across all accounts
  block that network until the window ends. IPv6 clients count per /64.
- **Unknown accounts** count exactly like real ones, and cost the same bcrypt
  time, so neither the timing nor the throttle reveals which usernames exist.
- A wrong current password on `POST /api/v1/me/password` counts as a failed
  login.

The client receives 429 with `Retry-After`. Counts are kept in the database,
so every replica enforces the same limits. An account entering backoff is
audited once per window (`auth.login_throttled`, outcome `denied`). A
successful login clears the account's count.

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

The gateway is on when `MACHINARCHY_AGENT_GATEWAY_NATS_URL` is set. Its
signing key is generated at the first start and kept in the Secret Store;
see [rotating the controller signing key](#rotating-the-controller-signing-key).

| Variable | Purpose |
|---|---|
| `MACHINARCHY_AGENT_GATEWAY_NATS_URL` | `tls://host:4222` (production) or `nats://` (development). No credentials in the URL. |
| `MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE` | Optional. The controller signing key lives in the Secret Store. This file (a base64 32-byte Ed25519 seed) is imported once, at the first start after upgrading, so enrolled agents keep working; without it a key is generated. Remove the setting afterwards. The public key agents pin is at `GET /api/v1/agents/controller-key`. |
| `MACHINARCHY_AGENT_GATEWAY_ENROLLMENT_TOKEN_FILE` | **Development only** (refused in production): a shared token any host can enrol with. Normally hosts use per-host tokens; see [enrolling a host](#enrolling-a-host). |
| `MACHINARCHY_AGENT_GATEWAY_NATS_CREDENTIALS_FILE`, `_NATS_CA_FILE`, `_NATS_CERT_FILE`, `_NATS_KEY_FILE` | NATS authentication and TLS. |
| `MACHINARCHY_AGENT_GATEWAY_LEASE_TTL` / `_HEARTBEAT` | Default 60s / 15s; the heartbeat must be at least 5s and at most half the TTL. |
| `MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT` | Public key (`A...`) of the NATS account the agents use. With the signing key below, every agent gets its own NATS user JWT. **Required in production.** |
| `MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE` | Seed (`SA...`, file `0600`) of a signing key listed in that account's JWT. The gateway signs agent JWTs with it. |
| `MACHINARCHY_AGENT_GATEWAY_AGENT_JWT_TTL` | Agent JWT lifetime, default `1h` (5m–24h, at least twice the heartbeat). Renewals refresh it. After a revocation, the agent keeps NATS access only until its current JWT expires. |

#### Rotating the controller signing key

The gateway signs everything it sends to agents with the **active** key,
whose private half is a Secret Store secret (protected by
`MACHINARCHY_KMS_PROVIDER`). Agents pin its public key and can pin two
during a rotation, so a key changes without stopping anything. Owner
actions, each audited (`agent.signing_key_*`):

| Step | Action | Effect |
|---|---|---|
| 1 | `POST /api/v1/agents/signing-keys/next` | Generates the next key; `GET /api/v1/agents/controller-key` returns `publicKeys` with both |
| 2 | On every agent: `MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=<publicKeys, comma-separated>`, restart the agent | Agents accept either key; identity and lease survive the restart |
| 3 | `POST /api/v1/agents/signing-keys/next/activate` | The next key signs; the old one retires. Other controller replicas follow within 5 s. No restart. |
| 4 | Later, on every agent: pin only the new key | — |

`DELETE /api/v1/agents/signing-keys/next` discards a staged key;
`GET /api/v1/agents/signing-keys` lists all keys (never key material). An
agent that skipped step 2 logs `discarding unauthenticated or malformed
command` after step 3: pin both keys on it and restart it.

`test/e2e/rotation_test.go` runs these steps with real binaries: import of
the old key file, stage, pin, activate without a restart, and a restart
without the file. Commands keep succeeding and the same agent process stays
up.

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

A host reached through its agent is a managed host on an Access Profile with
the `agent` connector. Such a profile takes no username, credential, host-key
or execution settings, and its hosts need no endpoint:

```sh
curl -s -X POST https://machinarchy.example/api/v1/managed/access-profiles \
  -H "Authorization: Bearer $OWNER_SESSION" -H 'Content-Type: application/json' \
  -d '{"name":"agents","spec":{"connectorKind":"agent","enabled":true}}' | jq -r .id
machinarchy create host web-01 --profile agents --label tier=web
```

An owner then issues one token per host. It is single-use, expires after 1
hour by default (at most 24 hours), can be bound to the host's name, and with
`hostId` binds the agent that enrols with it to that managed host:

```sh
curl -s -X POST https://machinarchy.example/api/v1/agents/enrollment-tokens \
  -H "Authorization: Bearer $OWNER_SESSION" -H 'Content-Type: application/json' \
  -d '{"hostname":"web-01.example","hostId":"<host id>","ttlSeconds":3600}' | jq -r .token
```

Enrolling with a host-bound token replaces any earlier binding of that host
(a replaced machine takes over its host) and is audited as
`agent.host_bound`. A revoked agent no longer serves its host. A token for a
host that does not exist is refused.

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

#### Capability Operations

Agent work is an Operation like an SSH run ([ADR-057](adr/ADR-057-capability-operations.md)):
`POST /api/v1/managed/operations` with `capability` instead of `run`, or

```sh
machinarchy run -l tier=web --capability service.status --input '{"service":"nginx.service"}' --wait
```

It needs the same permission as a run, every target must be on an `agent`
profile (and a run's targets on an `ssh` profile), and the capability input
is hidden from readers like a run's argv. Each target gets one durable
command. A relay publishes it with `Nats-Msg-Id` set to the command ID and
records it as sent only after JetStream confirms. Results are stored before
the agent's result is acknowledged, so a controller restart in the middle of
an Operation loses nothing. Cancelling sends `operation.cancel` to running
targets, whose final state then comes from the agent.

| Agent result | Target |
|---|---|
| `succeeded` | `Succeeded`, with the agent's output |
| `failed`, `rejected` | `Failed`; the error class is the agent's code |
| `timed_out` / `cancelled` | `TimedOut` / `Cancelled` |
| `unknown` | `Interrupted` (`outcome_unknown`): the agent cannot tell whether it ran; never retried |
| no result by lifetime + timeout + 2 min | `Lost` (`no_result`) |

All targets of a capability Operation are dispatched at once, since each agent runs on its own host. The number of targets per Operation bounds the load.

Targets that cannot be dispatched fail with these error classes:
- `agent_not_bound`: no agent is enrolled for the host.
- `not_an_agent_host`: the host's profile is not an agent profile.
- `access_profile_unavailable`: the profile is missing or disabled.
- `host_snapshot_stale`: the host changed after submission.
- `agent_gateway_unavailable`: this process does not run the gateway.
- `agent_command_undelivered`: the command could not be published before it expired.

An agent host's `Ready` condition reports `AgentConnected`,
`AgentDisconnected`, `AgentNotEnrolled` or `AgentGatewayDisabled`.
Machinarchy never contacts such a host over SSH. Enrolling or revoking its
agent queues the host for reconciliation, so `Ready` follows within seconds.
An agent losing its lease is noticed at the next host resync (5 minutes by
default). Dispatch does not wait for `Ready`.

### Restart-required settings

Most runtime settings apply to every running process at once (SSH policy, allowed programs, log level, signup). These are read once at process start, so a change applies only after each process restarts:

| Setting | Meaning |
|---|---|
| `controlManager.workers`, `controlManager.resyncSeconds` | Control Manager workers; full resync interval |
| `workQueue.capacity`, `retryBaseSeconds`, `retryMaxSeconds`, `ratePerSecond` | Reconcile queue and its retry and rate limits |
| `reconciliation.timeoutSeconds`, `managedHostResyncSeconds` | Reconcile budget; per-host resync interval |
| `ssh.connectTimeoutSeconds`, `ssh.maxConcurrent` | SSH dial timeout; concurrent SSH sessions |
| `operations.workers`, `operations.timeoutSeconds` | Operation workers and timeout |

Publishing a revision that changes one of them succeeds: the revision is stored, the response lists the settings in `restartRequired`, and any hot-reloadable change in it applies at once. Restart the processes one at a time (a rolling restart keeps the service up). Until then `GET /api/v1/runtime-config/status` shows `restartPending: true` for every process that started with other values (`startedRevision` is the revision it read at startup), and the flag clears when the process has restarted or the settings are rolled back.

## Key management

Everything sensitive at rest is sealed the same way: each value has its own
data key, and the **key provider** (`MACHINARCHY_KMS_PROVIDER`, the root of
trust) wraps that data key under a named root key. Covered:

| Data | Where |
|---|---|
| Secrets (SSH keys, passwords) | Secret Store |
| The agent-gateway signing key | Secret Store (system secret) |
| Operation specs, per-host output, live progress of detached runs | `operations`, `operation_targets` |

Every value records its key ID (`local:<id>`) and is bound to its row
(AAD), so a value copied to another row does not decrypt. Operation data
written before key IDs existed is still read, and the rewrap reseals it.

**Built-in provider (`local`, default).** Root keys are derived from
`MACHINARCHY_ENCRYPTION_SECRET[_FILE]` under `MACHINARCHY_ENCRYPTION_KEY_ID`.
Earlier secrets go in the keyring file (`MACHINARCHY_ENCRYPTION_KEYRING_FILE`,
JSON `{"<old key ID>": "<old secret>"}`, `0600`). Prefer the `_FILE` form:
environment variables are visible in `docker inspect` and process listings.

### Rotating the root key (drill)

Only data keys are rewrapped; the data itself is not re-encrypted.

1. Write the current secret into the keyring under its key ID, e.g.
   `{"bootstrap-v1": "<current secret>"}`.
2. Set a new `MACHINARCHY_ENCRYPTION_SECRET[_FILE]` and a new
   `MACHINARCHY_ENCRYPTION_KEY_ID` (e.g. `v2`); restart every replica.
   New values use `local:v2`; old ones still open through the keyring.
3. Rewrap until done (owner session):
   `machinarchy secret rewrap --old-key-id bootstrap-v1` and repeat while the
   result has `"done": false`. It covers secrets and Operation data
   (`operationData.remaining`).
4. Remove the old entry from the keyring and restart.
5. Keep the old secret until your **database backup retention** has passed:
   older backups hold data keys wrapped by it.

If step 3 answers 503, a value did not open with any configured key: keep the
keyring entry and check that the secrets in it are the ones that were in use.

### Switching to an external KMS (future)

Optional providers (Vault, AWS, GCP, Azure) plug into the same interface and
pass the same contract suite (`internal/secrets/providertest`). Switching is
the same drill with two providers configured at once: the new one current,
the old one previous; rewrap until done; then remove the old one. Their key
IDs name the provider (`vault:…`), so both can be configured together.

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
make images AGENT_SOURCE=../machinarchy-agent   # api, controlmanager, monolith, cli
```

- Multi-stage BuildKit builds from digest-pinned bases; distroless, non-root
  (65532), `STOPSIGNAL SIGTERM`.
- Reproducible: `-trimpath -buildvcs=false`, version, commit and commit time
  through ldflags (`machinarchy version`, and `buildinfo` in logs).
- `HEALTHCHECK` runs `/machinarchy healthcheck`, which probes the process's
  own `/healthz/ready`. It finds the port the way the server does: the
  `--listen-addr` flag, `MACHINARCHY_LISTEN_ADDR`, the `--config` bootstrap
  file, then `:8080`. A container started with flags needs the same flags on
  the health check (`healthcheck --listen-addr :9090`, or `--config FILE`);
  with only the environment variable nothing changes.
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
| HTTP read header / read / write / idle | 5s / 15s / request timeout + 5s / 60s |
| API request context (database and gateway calls) | `MACHINARCHY_HTTP_REQUEST_TIMEOUT`, default 25s |
| One database statement | Request timeout − 5s (at least half of it): 20s by default. PostgreSQL `statement_timeout` on every connection; the SQLite busy timeout. Migrations are exempt and bounded by `MACHINARCHY_DATABASE_MIGRATION_TIMEOUT` |
| Agent capability call | `timeoutSeconds` of the Operation (default 60s, max 3600s); no result by command lifetime + timeout + 2 min makes the target `Lost` |
| Per-host command | `timeoutSeconds` of the Operation: max 900s attached, 86400s detached |
| Reconcile | runtime configuration `reconciliation.timeoutSeconds` |
| Shutdown | `MACHINARCHY_SHUTDOWN_TIMEOUT` |

## Upgrades and schema

The schema is one baseline per dialect (`internal/store/migrations`). The
baseline was squashed again on 2026-11-25, before the first release, so a
database created by an earlier development build records versions this build
does not ship: the process refuses it (`ErrIncompatibleSchema`) and it must be
recreated (delete the SQLite file, or drop and recreate the PostgreSQL
database). From the first release (0.0.1) on, schema changes are additive
migrations and the baseline is not squashed again. Take a backup before any
upgrade.

Migrations run at startup within `MACHINARCHY_DATABASE_MIGRATION_TIMEOUT`
(10 minutes by default), separately from `MACHINARCHY_DATABASE_CONNECT_TIMEOUT`.

Some future migrations will rewrite tables on SQLite, which cannot change a
CHECK constraint in place; those take time proportional to the stored output
(an 834 MB database took 3 minutes on a laptop in a test). Raise the timeout
for larger databases. PostgreSQL changes constraints in place.

`managed_host_labels` is an indexed copy of every host's labels that label
selectors query. The API still returns labels from `managed_hosts`; both are
written in one transaction.

**Selectors at scale.** Label requirements (`tier=web`, `role in (a,b)`,
`tier!=legacy`, `canary`, `!canary`) run in the database, so neither a host
listing nor a preview scans the inventory, and an inventory over 10,000 hosts
no longer makes previews fail. Built-in fields and wildcards are checked on
the candidates. A selector with no label requirement (`name=web-*`) still
reads the whole inventory: 1.3 s on SQLite and 1.9 s on PostgreSQL for 50,000
hosts on a laptop, within the 25 s request deadline. A preview may match at
most 10,000 hosts.
