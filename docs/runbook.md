# Runbook

Symptoms, how to confirm them, and what to do. Logs are JSON on stdout;
filter on `"level":"ERROR"` and read the `error` field. Metrics are described
in [operations](operations.md#metrics).

## The process exits at startup

| Log message contains | Cause | Fix |
|---|---|---|
| `load configuration: ... MACHINARCHY_...` | A setting is missing or invalid. | Fix the named setting; secrets must come from the environment or `0600` files. |
| `must have permissions 0700 or stricter` | The SQLite directory is group- or world-readable. | `chmod 700` the directory (in containers, mount a volume the image initialises, or `chown 65532 && chmod 700` it). |
| `incompatible schema version` / `ErrIncompatibleSchema` | The database was created by an older migration chain. | Development: delete the SQLite file or drop the PostgreSQL database. Restore a backup if data matters. |
| `production requires TLS to NATS` | Agent gateway with `nats://` in production. | Use `tls://` or set `MACHINARCHY_AGENT_GATEWAY_NATS_CA_FILE`. |
| `connect agent gateway to NATS` | NATS unreachable or credentials wrong. | Check the URL, the credentials file and network policy. The process will not start without the gateway once configured; unset `_NATS_URL` to run without agents. |
| `provision agent command stream` | JetStream disabled or the stream exists with incompatible settings. | Enable JetStream (`-js`); inspect `nats stream info MACHINARCHY_AGENT_COMMANDS`. |
| `sslmode=verify-full` | Production PostgreSQL URL without full TLS verification. | Fix the URL and CA. |

## Readiness is failing

`GET /healthz/ready` returns the failing check by name.

- **database**: the pool cannot reach the database. Check
  `machinarchy_db_connection_waits_total` (exhausted pool) and database
  health. The process recovers on its own once the database is back.
- **control-manager**: startup recovery has not finished or the engine
  stopped. Look for `Control Manager startup resync failed` and its `error`.
- **agent-gateway**: the NATS connection is down (`agent gateway disconnected
  from NATS`). The client reconnects indefinitely; check NATS.

## Operations stay Queued

1. Execution enabled? A disabled policy fails Operations when they run
   (`execution is disabled`); set `MACHINARCHY_EXECUTION_ENABLED` and the
   program list, then submit again.
2. Is a Control Manager or monolith running against the same database? The
   standalone API does not execute.
3. `machinarchy_operationworker_operations_in_progress` at the worker count
   for a long time means hosts are slow or hanging; compare with
   `target_duration_seconds` and each Operation's `timeoutSeconds`.

## Operations end Interrupted

- Reason `worker_stopped`: the process stopped while a command ran. The
  command may or may not have run on the host; Machinarchy never replays it.
  Check the host, then submit again if needed.
- Reason `worker_panic`: a bug. `machinarchy_operationworker_panics_total`
  increased and the log has the stack under `stack`. File it with the log.

## A detached target is Lost

The outcome is unknown, and Machinarchy never retries it. Look at the host
before running the command again.

| Error class | Meaning |
|---|---|
| `supervisor_lost` | The host rebooted, the OOM killer took the supervisor, or someone stopped its unit (`machinarchy-OPERATION-HOST`) |
| `unreachable` / `unreachable_during_cancel` | The controller could not reach the host past the deadline plus 5 minutes, or for 5 minutes after a cancel. Check `polls_total{outcome="unreachable"}` and the host's `Ready` condition. A Host whose spec changed during the run (endpoint, trusted keys) also ends here |
| `state_missing` | The target's directory under the state root was removed |

For a detached target that failed to start:
- `launch_failed`: a launch whose reply was lost was never seen on the host, after 3 attempts.
- `launch_*`: the session failed before anything could start (host key, authentication, refused, egress policy).
- `runner_*`: the host's runner refused, for example `systemd_unavailable` when the profile forces `systemd`.
- `detached_disabled_by_profile`: the profile's supervisor is `attached`.

## A target is TimedOut or Failed

`GET /api/v1/managed/operations/{id}` (operator or owner) shows each target:

- `TimedOut` / `timeout`: the command exceeded `timeoutSeconds`.
- `Failed` with an exit code: the program ran and failed; read stdout/stderr.
- `Failed` / `remote_execution_failed`: SSH connection or authentication
  failed. Check the host's `Ready` condition (`hosts`), its endpoint and
  trusted key, and the Access Profile's secret version.
- `Failed` / `access_profile_unavailable`: the profile or its pinned revision
  was deleted or disabled after submission.

## Hosts never become Ready

The Host controller verifies SSH with the bound Access Profile. Read
`status.conditions` on the host:

- egress denied: the endpoint is outside `MACHINARCHY_SSH_ALLOWED_CIDRS` or
  its port is not allowed;
- `HostKeyPendingApproval`: the keys were scanned and wait for an owner. Check
  them out of band if you can, then `machinarchy hosts trust NAME` (or
  `--all-pending`, which asks about each host);
- `HostKeyChanged`: the host presents a key that is not trusted. Nothing was
  sent to it. This may be a reinstalled host or someone in the middle: verify
  the new key out of band before `machinarchy hosts trust NAME --replace`. The
  change is audited once as `resource_host.host_key_changed`, with the
  presented and trusted fingerprints, and counted in
  `host_key_events_total{event="changed"}`;
- `HostKeyNotTrusted`: the `pinned` policy with no trusted key, or a `ca`
  profile whose CA did not sign the host's certificate (or the certificate
  does not name the endpoint's host name);
- authentication failed: rotate the secret (`machinarchy secret rotate`) and
  update the profile revision.

`machinarchy_controlmanager_reconciles_total{outcome="retry"}` rising with a
growing `queue_depth` means many hosts are failing at once (network outage
or a broken credential).

## Agents

| Symptom | Check | Fix |
|---|---|---|
| Agent not listed after start | Agent log `controller rejected enrollment` (exit 78); gateway metric `registrations_total{outcome="rejected"}`. | The token is unknown, already used, expired, revoked, or bound to another hostname. Check `GET /api/v1/agents/enrollment-tokens`, then issue a new one. |
| `registrations_total{outcome="invalid"}` rising | Messages failed signature or schema checks. | A misconfigured or hostile client. Check NATS ACLs. |
| Agent listed but `connected: false` | Lease expired: no renewals arrive. | Agent down or partitioned; on the agent host `machinarchy-agent --healthcheck` exits 1 with the expiry time. |
| Agent stops after a renewal with exit code 78; if restarted it is refused (`enrollment_rejected`), gateway metric `registrations_total{outcome="revoked"}` | An owner revoked it; revoked keys are never re-admitted. | Intended. The systemd unit does not restart on 78; under compose, stop the service. To re-admit the host, delete its state file (a new key is generated) and let it enrol again. |
| Invoke returns 503 | This process does not run the gateway, or the agent has no live lease. | Call a monolith/Control Manager; check `connected`. |
| Invoke returns 504 | The agent did not answer within the timeout. | Check the agent log for the operation ID; results that arrive later are ignored. |
| Result `rejected` / `capability_unavailable` | The agent does not offer that capability. | Only `runtime.info`, `system.facts` and opt-in `service.*` exist. |

## Shutdown takes too long or is killed

The process logs `skipping runtime close because a background worker is still
running` when a worker outlived `MACHINARCHY_SHUTDOWN_TIMEOUT`. Running
commands are cancelled and their Operations end `Interrupted`. Make sure the
orchestrator grace period (`stop_grace_period`,
`terminationGracePeriodSeconds`) is longer than the shutdown timeout.

## A user cannot log in (429)

`429` with `Retry-After` on `/api/auth/login` means failed-login backoff, not
an outage. Check the audit trail (`GET /api/v1/audit/events?action=auth.login_throttled`):
it records which account and network.

- One account: someone (or a stale script) keeps trying a wrong password. It
  clears by itself, and a successful login resets it.
- A whole network: more than `MACHINARCHY_LOGIN_IP_MAX_FAILURES` failures from
  it in the window. Behind a proxy, check that `MACHINARCHY_TRUSTED_PROXIES` is
  set, or every client shares the proxy's address.

## Secrets and keys

- `decrypt` errors after a restart (secrets or Operations): `MACHINARCHY_ENCRYPTION_SECRET`
  or the key ID changed. Restore the previous secret, or list it in the
  keyring file, then `machinarchy secret rewrap --old-key-id OLD` until the
  result says `"done": true` (`docs/operations.md`, "Rotating the root key").
- Losing the encryption key makes stored credentials unrecoverable; keep it
  backed up with the database backups that need it.
- Rotating the agent gateway signing key: stage the next key, let agents pin
  both, then activate it (`docs/operations.md`, "Rotating the controller
  signing key"). An agent that pins only the old key rejects every command
  after activation.
- `MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE differs from the active signing
  key … and is ignored`: the key already lives in the Secret Store; remove the
  setting.
- `MACHINARCHY_KMS_PROVIDER=… is not available in this build`: only `local`
  (the built-in provider) exists so far.

## Audit trail

`GET /api/v1/audit/verify` (owner) checks the hash chain. `valid: false`
means a stored event was changed or removed: preserve the database for
investigation and compare with the last recorded `headHash`.

To shrink the trail, an owner runs `machinarchy audit export --output FILE`, keeps the file, then `machinarchy audit prune --before-id N --archive FILE --confirm N` (ADR-061). Nothing is pruned automatically, events younger than 30 days are never removed, and the server cannot restore pruned events. `verify` after a prune reports the checkpoint it started from; record that hash with the archive. After restoring a database backup, an archive made after the backup is the only record of events pruned since.
