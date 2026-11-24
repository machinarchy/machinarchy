# ChangeLogAI

## 2026-11-24 — Replicas starting together migrate in turn (Phase 20: F20-01; branch audit/ha)

- **Found by the HA harness:** with two API replicas and two Control Managers starting at once on an empty PostgreSQL database, one replica crashed. The error was `migrate postgres database: duplicate key value violates unique constraint "pg_type_typname_nsp_index"`: two processes created the same table at the same moment. Deployments start replicas together on every rollout.
- **Fix:** `store.Migrator.Up` takes the session advisory lock `pg_advisory_lock` on a dedicated connection on PostgreSQL before it reads anything.
  - Each replica waits its turn, then finds the migrations already applied.
  - The lock is released explicitly; the database also releases it if the process crashes and its connection closes.
  - SQLite is unchanged: one process per database.
- **Test:** `TestConcurrentPostgresStartup` starts 4 stores at once on a fresh schema. It failed on every run before the fix; it passed 5 of 5 after.
- **Checks run:**
  - `go test ./internal/store/` with PostgreSQL required: pass;
  - lint: 0 issues.

## 2026-11-24 — Egress policy checks IPv4 inside NAT64, 6to4 and Teredo (Phase 19: S-19; branch audit/data-path)

- `pkg/netpolicy`: `CheckAddr` now decodes the IPv4 address that an IPv6 address carries and checks it under the same policy (metadata addresses, Deny ranges, loopback, link-local, private). The IPv6 address is refused if that IPv4 address would be.
  - NAT64 `64:ff9b::/96` (RFC 6052);
  - 6to4 `2002::/16` (RFC 3056);
  - Teredo `2001::/32` (RFC 4380): both the server and the inverted client address.
- **Before:** for example, `64:ff9b::a9fe:a9fe` (the metadata address 169.254.169.254) and `2002:c0a8:101::1` (192.168.1.1) counted as public IPv6 addresses and were allowed.
- **Tests:**
  - `TestEmbeddedIPv4IsChecked`: 11 denied cases (all allowed before the fix) and 4 allowed ones, including `2001:db8::1`, which is outside the Teredo range.
  - New `FuzzEmbeddedIPv4`: 20 s, no failures.
  - `FuzzParsePrefixes` corpus extended with the NAT64, 6to4 and Teredo prefixes: 10 s, no failures.
- **Checks run:** `go test ./pkg/... ./internal/hostaccess/... ./internal/config/`: pass. Lint: 0 issues.

## 2026-11-24 — Database statement timeout below the request deadline (Phase 19: Q-20; branch audit/data-path)

- **`store.Config.StatementTimeout`:**
  - PostgreSQL: set as the `statement_timeout` runtime parameter of every connection. The DSN is now parsed with `pgx.ParseConfig` and opened with `stdlib.OpenDB`.
  - SQLite: the value becomes the busy timeout.
  - Migrations run `SET LOCAL statement_timeout = 0`; `MACHINARCHY_DATABASE_MIGRATION_TIMEOUT` still bounds them.
- **Bootstrap:** derives the value with `HTTPConfig.StatementTimeout()`: the request deadline minus 5 s, and at least half of it. That is 20 s for the default 25 s, so a slow statement ends in the database before the response is abandoned.
- **Tests:**
  - `TestPostgresStatementTimeout`: every new connection reports `300ms`, and `pg_sleep(3)` fails with SQLSTATE 57014 within 2 s.
  - `TestSQLiteBusyTimeoutFollowsStatementTimeout`.
  - `TestStatementTimeoutStaysBelowTheRequestDeadline`.
  - Before the fix: `statement_timeout = "0"` and busy timeout 5000.
- **Docs:** the `docs/operations.md` Timeouts table gains the statement row. It also replaces the stale "Synchronous agent call" row (`invoke` was removed in Phase 17) with the capability timeout.
- **Checks run:**
  - `go test` with PostgreSQL required for store, bootstrap, config, operationworker and e2e: pass;
  - lint: 0 issues.

## 2026-11-24 — Outbox claims are released within a bound on stop (Phase 19: Q-18; branch audit/data-path)

- **`controlmanager.Engine.dispatchBatch`:** when the dispatcher stops with deliveries claimed, it hands them back through one shared context. That context is detached from the cancelled stop context and cancelled `stopReleaseTimeout` (2 s) after the first release.
  - Before, one path used `context.Background()` with no timeout, so a hung database blocked shutdown forever. The `QueueShuttingDown` path passed the cancelled context, so its release always failed at once.
  - A failed release is logged; the delivery is reclaimed after its lease.
- **Test:** `TestStoppingDispatcherReleasesClaimsWithinABound`, with an outbox whose `Retry` hangs.
  - Before the fix: `dispatchBatch` never returned (test timeout after 3 s).
  - After: 3 claims released within the shared bound, and the first release didn't start cancelled.
  - Also run with `-race -count=3`.
- **Checks run:** `go test ./internal/controlmanager/`: pass. Lint: 0 issues.

## 2026-11-24 — Runtime-config apply rolls back only what it applied (Phase 19: Q-16; branch audit/data-path)

- `runtimeconfig.PreparedApply` documents its contract and gains an optional `Abort`:
  - `Rollback` undoes a `Commit`;
  - `Abort` releases a prepared change that will never be committed.
- **`SnapshotManager.Reload`:**
  - A failed Commit at participant i rolls back i down to 0, newest first, and aborts the participants after i. Before, it rolled back every prepared participant, including ones never committed.
  - A failed Prepare aborts the prepared participants. Before, it rolled them back.
  - The leftover `_ = i` is gone.
- **Tests:**
  - `TestCommitFailureRollsBackOnlyWhatWasApplied` and `TestPrepareFailureAbortsWithoutRollback` check the exact call order. Both failed before the fix (`c.rollback` on a never-committed change; rollbacks after a failed Prepare).
  - `TestApplyIsAllOrNothing` now expects no rollback when nothing was committed.
- **Checks run:** `go test ./internal/runtimeconfig/ ./internal/bootstrap/`: pass. Lint: 0 issues.

## 2026-11-24 — Host resync pages through an index (Phase 19: Q-08; branch audit/data-path)

- **Migration 000010 (both engines):** `idx_managed_hosts_keyset ON managed_hosts(workspace_id, id)`.
- **`ListHostKeys`:** uses the row-value predicate `(workspace_id, id) > (?, ?)` instead of `workspace_id > ? OR (workspace_id = ? AND id > ?)`. The query is exported as `resources.HostKeysQuery` so a test can check its plan.
- **Before:**
  - SQLite: `MULTI-INDEX OR` plus `USE TEMP B-TREE FOR ORDER BY`;
  - PostgreSQL: `Incremental Sort` over a scan that filtered every row.
- **After:**
  - SQLite: `SEARCH managed_hosts USING COVERING INDEX idx_managed_hosts_keyset ((workspace_id,id)>(?,?))`;
  - PostgreSQL: `Index Only Scan using idx_managed_hosts_keyset`, `Index Cond: (ROW(workspace_id, id) > ROW(...))`.
- **Test:** store contract "host resync keyset uses an index" on both engines (PostgreSQL with sorts and sequential scans disabled). It failed before the fix on both.
- **Checks run:**
  - `go test ./internal/store/... ./internal/controlmanager/...` with PostgreSQL required: pass;
  - lint: 0 issues.

## 2026-11-24 — SQLite connection settings survive a reconnect (Phase 19: Q-10; branch audit/data-path)

- `internal/store/sqlite`:
  - `foreign_keys(1)` and `busy_timeout` are now DSN `_pragma` parameters, so the driver applies them to every connection it opens. Before, they were set with `Exec` on whichever connection ran it, and a reopened connection had foreign keys off and no busy timeout.
  - `Config.BusyTimeout`, default 5 s.
  - `Configure` keeps enabling WAL (stored in the file) and now verifies that foreign keys are on.
  - A database path containing `?` or `#` is refused, since the path becomes a DSN.
- **Tests:**
  - `TestPragmasSurviveReconnect` forces a new connection for every query. Without the fix it fails with `foreign_keys=0 busy_timeout=0`.
  - `TestOpenDBRefusesAQueryInThePath`.
- **Checks run:** `go test ./internal/store/sqlite/ ./internal/store/`: pass. `golangci-lint run --new-from-rev=HEAD ./...`: 0 issues.

## 2026-11-24 — Label selectors run in the database (Phase 18: A-12; branch audit/scale)

- **Migration 000009 (SQLite and PostgreSQL):** `managed_host_labels(host_id → managed_hosts ON DELETE CASCADE, workspace_id, label_key, label_value)`.
  - Primary key `(host_id, label_key)`; lookup index `(workspace_id, label_key, label_value, host_id)`.
  - Backfilled from `managed_hosts.labels` (`json_each` / `jsonb_each_text`).
  - The store rewrites the rows only in the transaction of a label change (`UpdateHostMetadata`) or a host insert; status updates don't touch them.
- **Domain:**
  - `Selector.LabelConstraints` turns label requirements into conditions every match satisfies: `=`/`in` become `LabelIn` (`LabelExists` with wildcards), `!=`/`notin` become `LabelNotIn` (nothing with wildcards), plus existence and non-existence.
  - `resource.IsHostBuiltinField`.
  - `HostListFilter` gains `Labels` and `Lookup` (ID or name, ignoring case).
- **Store:** positive conditions use `id IN (SELECT host_id …)` through the lookup index; negative ones use `id NOT IN (…)`.
- **Service:**
  - Host listings with a selector page through the database's candidates and apply the selector exactly. The 5,000-row scan cap is gone.
  - Previews and submissions load only the candidates: every host for `all`, label-filtered hosts that match the selector, and the hosts named in include and exclude. The 10,000-host limit now counts matched hosts, not the whole inventory.
- **Tests:**
  - store contract on both engines: 14 selector shapes produce the same result through SQL candidates as the selector alone; label changes and deletes update the index; workspace isolation; ID or name lookup; paging;
  - migration 9 backfill test (SQLite).
- **Benchmark** `BenchmarkHostSelectors`, laptop (i7-9750H), 10 iterations each:

  | Engine, hosts | Selective list page | Selective preview | `canary,tier!=batch` preview | No label requirement (full scan) |
  |---|---|---|---|---|
  | SQLite, 10k | 16 ms | 13 ms | 10 ms | 0.30 s |
  | SQLite, 50k | 81 ms | 135 ms | 55 ms | 1.31 s |
  | PostgreSQL, 10k | 14 ms | 9 ms | 38 ms | 0.38 s |
  | PostgreSQL, 50k | 54 ms | 57 ms | 150 ms | 1.89 s |

  The selective selector is `tier=db,zone=z9`, 1 % of hosts. Every case finishes well within the 25 s request deadline. Before this change, a preview over an inventory larger than 10,000 hosts failed.
- **Docs:** `docs/operations.md` "Upgrades and schema" (migration 9, selectors at scale).
- **Checks run:**
  - `go test` with PostgreSQL required for store, service, transport, domain, hostaccess and bootstrap: pass;
  - `golangci-lint run --new-from-rev=HEAD ./...`: 0 issues.

## 2026-11-24 — Attached runs fan out to RunSpec.Concurrency (Phase 18: D-10; branch audit/scale)

- **Operation worker:** attached (non-detached) runs used to execute their targets one after another, whatever `RunSpec.Concurrency` said. They now run `Concurrency` targets at a time (1–20); the SSH capacity limiter still caps the total.
  - Before each launch, the worker checks the deadline, cancellation and its own shutdown.
  - After a stop, no new target starts. Running targets finish (cancel closes their sessions), and the Operation is finished only after all of them have returned (`Cancelled`, `TimedOut` or `Interrupted`, as before).
  - The per-target body moved into `runAttachedTarget`, unchanged.
- **Capability targets:** already dispatched all at once (each agent runs on its own host); documented.
- **CLI:**
  - `run --concurrency N` (1–20);
  - the default is 5 with `--wait` (was 1, so attached CLI runs never fanned out) and 20 for detached launches;
  - both are capped by the host count.
- **Tests:**
  - with a fake connector at 300 ms per target: 12 targets at 4 took 930 ms (ideal 900 ms, sequential 3.6 s); 5 at 1 took 1.53 s; 3 at 20 took 310 ms; peak concurrency equalled the limit;
  - cancel mid-run starts no further target and ends `Cancelled`;
  - a worker stop leaves no target `Running` and ends `Interrupted`;
  - CLI `--concurrency` defaults and limits.
- **Checks run:**
  - `go test -race -count=3` for the fan-out and capability worker tests: pass;
  - `go test` for operationworker, hostaccess (real SSH), service, bootstrap, cli and e2e: pass;
  - `golangci-lint run --new-from-rev=HEAD ./...`: 0 issues.

## 2026-11-24 — Execution policy checks arguments (Phase 18: D-11/S-12; branch audit/scale)

- **Policy model (owner decision):** argument templates. Each policy entry is a program followed by one pattern per argument:
  - literals;
  - placeholders `{unit}`, `{word}`, `{int}`, `{path}` and `{any}`, none of which matches an argument starting with `-`;
  - an optional final `*` for any remaining arguments.

  A command must match one of its program's entries. `ExecutionPolicy.Check` takes the whole argv, and the service and the worker (at submit and at run time) call it that way.
- **Breaking:** a bare program name used to allow any arguments; it now allows none. `PROGRAM *` restores the old behaviour for one program. `docs/operations.md` "Execution policy" has the upgrade note and the runtime-config command to change the policy.
- **Refusals:** a refused command now gets HTTP 403 `execution_not_allowed` instead of 500 (guide known problem, G-13C). The message names the program and its allowed entries, never the arguments. The CLI hint explains how an owner adds an entry.
- **Runtime configuration:** entries may contain spaces, and validation errors say what is wrong with an entry.
- **Bootstrap:** seeds the runtime configuration from the normalized entries.
- **Quoting:** `domain.QuoteArgv` remains the only quoting implementation (unified in Phase 5). A new test sends hostile arguments through a real `/bin/sh`.
- **Tests:**
  - template matching with 20 argument-abuse cases (AUDIT/02 S-12);
  - parsing errors;
  - a new fuzz target, `FuzzArgumentTemplates`: about 950k runs in 20 s, no failures;
  - `FuzzQuoteArgv`: 10 s, no failures;
  - service refusal of allowed-program-but-wrong-arguments;
  - the HTTP mapping and the CLI hint.
- **Docs:**
  - operations guide "Execution policy";
  - SECURITY.md;
  - `.env.example`;
  - the guide's lab policy, now quoted because entries contain spaces, plus its known-problem note.
  - The guide's lab policy was checked by sourcing it and matching every command the guide runs.
- **Checks run:**
  - `go build ./...` and `go vet ./...`: clean;
  - `golangci-lint run --new-from-rev=HEAD ./...`: 0 issues;
  - `go test ./internal/... ./cmd/...`: pass.

## 2026-11-24 — Docs for capability Operations (Phase 17; branch audit/capability-ops)

- `docs/operations.md`:
  - "Enrolling a host" covers agent Access Profiles, agent hosts without an endpoint, and `hostId` tokens.
  - New "Capability Operations" section: submission, status mapping, `Lost`, the dispatch error classes, and agent-host `Ready` reasons.
  - The `invoke` note is gone.
- `docs/cli/commands.md`: `create host` without `--endpoint` for agent hosts; `run --capability`.
- `README.md`: the API table drops `/{id}/invoke`.
- Known limitation (documented): an agent host's `Ready` condition updates at the next host resync (5 minutes by default) after its agent enrols. Dispatch does not depend on it.
- Docs only; no checks run for this entry.

## 2026-11-24 — E2E tests run capability Operations (Phase 17; branch audit/capability-ops)

- `test/e2e`: both tests use capability Operations instead of the removed `POST /api/v1/agents/{id}/invoke`. New helpers are in `test/e2e/capability_test.go`.
- `TestAgentEnrolsAndRunsCapabilitiesThroughMachinarchy`:
  - two agent hosts, each enrolled with its own host-bound token;
  - `runtime.info` over both, with per-target output checked;
  - `shell.exec` fails with the agent's code;
  - after revocation, the host's target fails `agent_not_bound`;
  - the second agent stops cleanly.
- `TestControllerSigningKeyRotation`: an Operation submitted just before a controller restart finishes `Succeeded` after it.
- Checks run: `go test -count=1 ./test/e2e/` passes in 15.7 s when run alone. It took 40–80 s when other packages ran at the same time, which is why it is committed separately from the code.

## 2026-11-24 — Agent capabilities run as Operations (Phase 17: A-05, M-7, M-8, M-9; branch audit/capability-ops)

Implements ADR-057.

- **Domain:**
  - `OperationKind=Capability` with `CapabilitySpec{name, input, timeoutSeconds}`: the name is lowercase words joined by dots, the input is a JSON object of at most 16 KiB, and the timeout is 1–3600 s.
  - `Run` stays SSH-only.
  - Access Profile connector `agent`, which takes no username, credential, host-key or execution settings.
- **Migration 000008 (SQLite and PostgreSQL):**
  - the `Capability` operation kind;
  - `agent_enrollment_tokens.host_id`;
  - `agent_host_bindings`;
  - `operation_agent_commands`.
  - SQLite rebuilds `operations` and `operation_targets` together (copy both, then drop): rebuilding only the parent would cascade-delete targets. `TestSQLiteMigration8KeepsOperations` covers it.
- **Host binding:**
  - an enrolment token with `hostId` binds the enrolling agent to that managed host;
  - re-enrolment replaces the binding;
  - revoked agents serve no host;
  - tokens for unknown hosts are refused;
  - binding is audited as `agent.host_bound`.
- **Dispatch (M-9):** the Operation worker routes Capability Operations to agents. One durable command is written per target, in the same transaction that marks the target Running.
- **Relay (M-7):**
  - publishes with `Nats-Msg-Id` = command ID;
  - marks a command published only after the PubAck;
  - retries with backoff;
  - fails a target `agent_command_undelivered` when its command expires unsent.
- **Results (M-8):**
  - stored before the gateway acks them;
  - status mapping: `unknown` → `Interrupted` (never retried), no result by the due time → `Lost` (`no_result`);
  - duplicates are idempotent;
  - a result from the wrong agent is refused.
- **Cancel and restart:** cancelling sends `operation.cancel` to running targets. Capability Operations resume after a controller restart.
- **Ready:** agent hosts report `Ready` from their agent's lease. They are never contacted over SSH.
- **Removed:** `POST /api/v1/agents/{id}/invoke` and its synchronous waiters (A-05).
- **API:** `POST /api/v1/managed/operations` takes exactly one of `run` or `capability`. Each kind requires targets on a matching connector. Readers don't see a capability's input. API docs are regenerated.
- **CLI:**
  - `run --capability NAME [--input JSON]`;
  - `create host` needs `--endpoint` only for SSH hosts.
- **Fix found by the new tests:** the service checked a credential SecretRef even for agent profiles, so agent Access Profiles could not be created.
- **Tests:**
  - store contract (both engines);
  - migration 8;
  - worker flow on a real store with a fake transport: dispatch and relay with a refused first publish, results and duplicates, controller restart, status mapping, `Lost`, undelivered command, cancel, undispatchable targets;
  - domain validation, the reconciler's agent-host conditions, the HTTP handler, and the CLI.
  - The e2e tests move to capability Operations in the next commit; at this commit they still call the removed `invoke`.
- **Checks run:**
  - `go build ./...` and `go vet ./...`: clean;
  - `golangci-lint run --new-from-rev=HEAD ./...`: 0 issues;
  - `go test -race -count=3` on the worker capability tests: pass;
  - `go test ./...` with PostgreSQL required (`MACHINARCHY_REQUIRE_POSTGRES_TESTS=1`), together with the next commit's e2e tests: pass.

## 2026-11-24 — CLI guide step 16 shows the owner's secrets (owner report; branch audit/capability-ops)

- `docs/cli/user-full-guide.md` step 16: `machinarchy --context lab secret list` printed `[]`, which came from a separate fresh lab. A reader following the guide sees `lab-key` and `lab-password` from step 5. The example now prints their names through `jq`.
- Docs only; no checks run.

## 2026-11-24 — Audit Phase 17: ADR-057, agent capabilities as Operations (branch audit/capability-ops)

- `docs/adr/ADR-057-capability-operations.md` records the design and the owner's three decisions:
  - capability work extends `/api/v1/managed/operations` (`OperationKind=Capability`);
  - `POST /api/v1/agents/{id}/invoke` is removed;
  - hosts are bound to agents through a per-host enrolment token.
- The ADR also covers the `agent` Access Profile connector, the PubAck-gated command relay with `Nats-Msg-Id` (M-7), persist-then-ack results with a status mapping where `unknown` is never retried (M-8), `Lost` targets on no result, and restart behaviour.
- Docs only; implementation follows in later commits.

## 2026-11-24 — CLI guide step 15 works in zsh (owner report; branch audit/keys)

- **Bug:** `docs/cli/user-full-guide.md` built `KEYS` as one space-separated string and passed it unquoted (`… $KEYS`). zsh, the macOS default shell, does not split unquoted parameters, so the CLI received a single argument and failed with `unknown flag: --trust SHA256:… --trust …`.
- **Fix:** an array, `KEYS=($(… | jq -r '.spec.accessTarget.trust[] | "--trust", .'))`, passed as `"${KEYS[@]}"`, plus a short note on why. The printed output in the guide is unchanged.

### Verification

- With sample host JSON, bash 3.2 and zsh both pass 4 separate arguments (`--trust`, fingerprint, `--trust`, fingerprint), and `echo "${KEYS[@]}" | cut -c1-80` prints the line the guide shows.

## 2026-11-24 — Docs check after Phases 14–16 (owner request; branch audit/keys)

- **Links:** a script resolved every relative link and `#anchor` in `README.md`, `CONTRIBUTING.md` and `docs/` (77 files, `docs/archive` excluded). Two links pointed at a removed `#quick-start` heading; one was also relative to the wrong README. Both now point at the project README's "Run locally" (`docs/cli/README.md`, `docs/cli/example-cli.md`).
- **Settings:** all 56 `MACHINARCHY_*` settings read by `internal/config` appear in `README.md`, `.env.example`, `docs/operations.md` or `docs/runbook.md`. The reverse check found only two placeholder shell variables in `docs/SECURITY.md` curl examples (not settings).
- **Stale references:** none left for `NEXT_PUBLIC_KEY`, `make run-cli --`, `StaticKeyProvider` or a required signing-key file.
- **`machinarchy secret rewrap` help** (`internal/cli/commands/management.go`) still described secrets only ("Rewrap stored DEKs under the active external key"). It now says it moves secrets and Operation data, repeats until `done`, and keeps the old key until older backups expire; the flag texts and the confirmation prompt are updated too. The OpenAPI summary of `POST /api/v1/secrets/rewrap` is updated likewise; spec regenerated.
- `docs/cli/user-full-guide.md`: "A session lasts 24 hours" now names the setting (the default `MACHINARCHY_SESSION_TTL`).

### Verification

- The link check reported 0 problems after the fix. `go build ./...`; `go test ./internal/cli/... ./internal/transport/http/v1/...` passed; `secret rewrap --help` shows the new text; golangci-lint `--new-from-rev` 0 issues.

## 2026-11-24 — Back to `make run-cli ARGS="…"` only (owner decision; branch audit/keys)

- `mk/build.mk`: removed the `make run-cli -- …` support (`2b0050f`, `2f10e27`). `run-cli` is again `$(call run_with_env,./cmd/machinarchy)` with `ARGS`, byte-identical to before. Why: the `--` form relied on reading make's own command line through `ps`, lost quoting, ran make targets named like CLI words, and let `NAME=…` words change make's own settings. `ARGS` has none of these problems.
- Docs (`README.md`, `docs/cli/README.md`, `docs/cli/user-full-guide.md`, `docs/development/getting-started.md`, `docs/development/local-environment.md`, `docs/examples/example.md`) use `ARGS="…"` again. The notes on `.env`/`ENV_FILE` (the cause of the earlier lab login failure) and the troubleshooting entry stay.

### Verification

- `git diff 2b0050f~1 -- mk/build.mk` is empty: the Makefile is as before the `--` change.
- Make 3.81, scratch CLI context on a closed port: `ARGS="create host … --label tier=web --label auth=key"` and `ARGS='run --host ssh-key --yes -- sh -c "echo hi"'` both parsed and reached the API (connection refused); `make help` shows the `ARGS` usage.

## 2026-11-24 — `make run-cli -- …` passes arguments containing `=` (owner report; branch audit/keys)

- **Bug:** `make run-cli -- create host … --label tier=web --label auth=key` failed with `label "--label" must be KEY=VALUE`. Make takes any word containing `=` as a make variable, so those words never reached the CLI.
- **`mk/build.mk` fix:** `run-cli` now has its own recipe. With arguments after `--` (and no `ARGS` on the command line), the recipe reads make's own command line (`ps -o command= -p $PPID`) and passes everything after the first ` -- `, in order. Words containing `=` arrive, and a later `--` (`run … -- uptime`) is kept. Globbing is off (`set -f`) and nothing is `eval`ed. Without `ps` it falls back to the goals, as before. `ARGS="…"` is unchanged and still the way to pass quoted arguments.
- **Docs:** the `=` caveat is removed from `README.md`, `docs/cli/README.md`, `docs/cli/user-full-guide.md`, `docs/development/getting-started.md` and `docs/development/local-environment.md`. What remains: quoting is lost after `--`, and a word that is also a make target runs it.

### Verification

- Make 3.81, scratch CLI context on a closed port, so reaching the network proves the arguments parsed:
  - the reported command → "list access profiles: cannot reach the API server" (before: the label error);
  - `get hosts -l tier=web` → reached the API;
  - `run --host ssh-key --yes -- uptime` → reached target preview;
  - `ARGS="config current-context"` → `t`;
  - `ARGS=` together with `--` words is refused.

## 2026-11-24 — Docs: `make run-cli -- …` and the `.env` behaviour of `make run-*` (owner request; branch audit/keys)

- `docs/cli/user-full-guide.md`:
  - step 1 note: every `machinarchy …` command also runs as `make run-cli -- …`, with the `=`/quoting limits;
  - step 2 note: use `make run-monolit ENV_FILE="$LAB/server.env"`, because plain `make run-monolit` loads the repository's `.env`, and step 3's login then fails with `Invalid credentials`.
- `docs/cli/troubleshooting.md`: new "Login fails with `Invalid credentials`": the wrong `.env`; the bootstrap password applying only to an empty database; 429 backoff.
- `docs/cli/README.md`, `README.md`, `docs/development/local-environment.md`, `docs/examples/example.md`: the `--` form, `ARGS` for arguments with `=` or quotes, and that the `run-*` targets override exported settings (variables `.env` does not set, such as `MACHINARCHY_CONFIG`, are inherited).

### Verification

- With `MACHINARCHY_CONFIG` exported, `make run-cli -- config set-cluster lab --server http://127.0.0.1:8080` wrote to that file, so the guide's lab-only CLI config works through make. Neither `.env` nor `.env.example` sets `MACHINARCHY_CONFIG`.

## 2026-11-24 — `make run-cli -- <arguments>` (owner request; branch audit/keys)

- `mk/build.mk`: when `run-cli` is the first goal, the words after it become `ARGS`, and make treats them as goals that do nothing (`.DEFAULT`, silent, so an argument that is an existing file prints nothing). Giving both `ARGS=` and words is refused.
- Make limits, documented in `mk/build.mk` and `docs/development/getting-started.md`: a word containing `=` becomes a make variable, quoting is lost, and a word that is also a make target runs it. `ARGS="..."` still works for those.
- `docs/development/getting-started.md` also notes that the `run-*` targets load `.env` and ignore exported shell settings (use `ENV_FILE=` or `bin/`). This is what caused a failed lab login: `make run-monolit` used the repo's `.env` database instead of `$LAB/server.env`.

### Verification

- Make 3.81 (macOS):
  - `make run-cli -- --config <scratch> config set-cluster lab --server http://127.0.0.1:8080` → `Cluster "lab" configured.`, and `config get-contexts` lists it;
  - the `ARGS="…"` form works;
  - both forms together is refused with a clear error;
  - a CLI error passes through (exit status 2);
  - `make -n build-cli` is unchanged, and `make help` shows the new usage.

## 2026-11-24 — Audit Phase 16.7: ADR-056 and the root-key rotation drill (branch audit/keys)

- `docs/adr/ADR-056-key-providers-built-in-default.md` and its README row: one root of trust behind the provider interface; `local` by default; KMS providers optional (AUDIT/10 K1–K4); signing key and Operation data in the same envelope.
- **Drill run once**, real binaries and the repository's SSH lab:
  - **v1:** secret, profile and host created (the host verified over SSH); a command ran; everything stored as `local:v1`.
  - **v2 with v1 in the keyring:** old output read; a new command ran; one rewrap round answered `done: true` with 1 secret and 2 Operation values moved.
  - **v2 only:** old output read and a new command ran (the SSH key decrypted).
  - 0 server ERROR lines. Details in AUDIT/16 §3.

## 2026-11-24 — Audit Phase 16.5: Operation data carries key IDs and AAD; one rewrap covers secrets and Operation data (S-07) (branch audit/keys)

- **Gap closed:** Operation specs, per-host output and detached live progress were `pkg/crypto.Encrypt` under one key derived from the encryption secret, with no key ID and no AAD. Rotating the secret made every old Operation unreadable, and a value could be copied between rows.
- **`internal/store/resources/sealed.go` (`DataCipher`):** each value gets a fresh data key, wrapped by the key provider; stored as `{"v":2,"k":"<key ID>","w":…,"c":…}`. The AAD binds column, workspace, Operation and host. Values written before keep opening through legacy keys derived from the current secret and every keyring secret.
- **The store and the Secret Store share one provider:** bootstrap builds it with `secrets.NewLocalProvider` (new helper) before `store.Open`; `store.Config` gains `Keys` and `PreviousSecrets`; tests that pass only `EncryptionSecret` get the same local provider.
- **`RewrapOperationData`:** batches per column; updates only if the value is unchanged since read; also reseals legacy values; counts what remains. `POST /api/v1/secrets/rewrap` (and the CLI) now returns `operationData {rewrapped, remaining}` and `done` (all kinds finished); a failure answers 503 and the old key must stay. OpenAPI regenerated.
- Docs: `docs/operations.md` "Key management" (what is sealed, the root-key rotation drill, backups, the future KMS switch), runbook.

### Verification

- Store contract "operation data keys" on **SQLite and PostgreSQL 16**:
  - spec, output and progress are stored with `local:bootstrap-v1` and no plaintext;
  - a spec copied to another Operation does not open;
  - a legacy spec and output (old format) read;
  - after a root-key rotation, rewrap in batches of 1 moves all 6 values (including the legacy ones) to `local:v2`;
  - with only the new secret, the Operation, the legacy output and the live progress all read.
- `TestRewrapCoversOperationData`: `done` stays false until Operation data is finished; a failure is 503.
- `TestKeyPrefixesMatchSealedText`: the rewrap's prefix match equals the stored text for key IDs JSON escapes (`<`, `>`, `&`, quotes, `%`, `_`). A lint fix had briefly switched to `strconv.Quote`, which escapes differently and would have reported "done" with values left on the old key; caught before commit.
- Existing store, worker, host-access, control-manager, service and bootstrap tests pass on both engines with the new format.

## 2026-11-19 — Audit Phase 16.3/16.4: controller signing key in the Secret Store; rotation as owner actions (branch audit/keys)

- **Migration `000007_agent_signing_keys`** (SQLite and PostgreSQL): public key, state (`next`/`active`/`retired`), origin (`generated`/`imported`), who and when; the private seed is referenced by Secret Store ID and version. Partial unique indexes allow one active and one next key, also across replicas.
- **Secret Store system secrets**: new purpose `agent-signing-key`, created and read only through `CreateSystem`/`ResolveSystem`. Owner rotate and delete of such a secret, owner create with that purpose, and workload resolution are all refused. Same envelope and key provider as every secret.
- **`internal/agentkeys.Manager`**:
  - `Ensure` imports `MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE` at the first start, so enrolled agents keep working, or generates a key. Later a different file is ignored with a warning.
  - `Stage`/`Activate`/`Discard` are the owner actions.
  - `Refresh` every 5 s, so every replica follows an activation.
  - `Signer` hands the gateway a fixed snapshot per message (`SealWith` reads `Public` then `Sign`).
  - A seed that does not match its recorded public key is refused.
- **Owner API** (owner session, audited `agent.signing_key_created/_activated/_discarded`): `GET /api/v1/agents/signing-keys`, `POST …/next`, `POST …/next/activate`, `DELETE …/next`. Responses carry public data only. `/controller-key` reads the manager live. Authorization matrix and OpenAPI updated.
- **Config**: the key file is optional (import only); `MACHINARCHY_AGENT_GATEWAY_NEXT_PUBLIC_KEY` (added earlier today, never released) is removed in favour of the owner actions.
- Docs: `docs/operations.md` "Agent gateway" and "Rotating the controller signing key" (4 steps), `docs/runbook.md`, `.env.example`.

### Verification

- `test/e2e` `TestControllerSigningKeyRotation`, real binaries in NATS operator mode:
  - the first start imports the key file (`/controller-key` equals the file's key);
  - stage → both announced → the agent pins both → 3 commands;
  - activate with no restart → 3 commands and a lease renewal;
  - a restart **without** the key file keeps the new key, and a command succeeds;
  - the key list shows active + retired/imported;
  - the agent stays up and its log has no discard, signature or ERROR line.
- `internal/agentkeys` (race detector): import once then ignore a different file; generate without a file; 4 concurrent replicas end with one key; stage/activate/discard with another replica switching only after its refresh; a snapshot keeps its key; a tampered seed is refused.
- Store contract "agent signing keys" on **SQLite and PostgreSQL 16**: one active and one next at most, activation retires, discard, `created_by` from the actor, 5 audit events.
- `TestSystemSecretsAreOnlyForTheController`; `TestSigningKeyEndpoints` (409s, 201/200, no secret reference in the list, 503 without a gateway); authorization matrix with the 4 routes as owner-only; migration count 7.
- `test/e2e`: the monolith and agent binaries are built once per run and shared; the two tests run in parallel (each has its own NATS, ports, database and work directory); child processes are started with a context that ends 2 s before the test deadline. Before this, a test killed by `-timeout` left its monolith and agent running: 11 orphans from earlier runs were found and stopped.
- Pre-commit hook: it runs `go test -timeout=30s` per staged package. The e2e package needs about 37 s at load average 70–100 (each test about 30 s), so the hook fails on any commit that stages `test/e2e`. The hook was not changed (owner rule). With normal timeouts every check passes: gofmt, `go vet ./...`, `go test ./internal/... -count=1`, and e2e in 37.3 s.
- Known gap: if replicas race at the very first start, or a key row fails after its secret was written, the losing seed stays as an unused system secret (encrypted, never resolved).

## 2026-11-19 — Audit Phase 16.1/16.2/16.6: key providers wrap and unwrap; built-in local provider; provider contract suite (branch audit/keys)

- **Interface** (`internal/secrets/keyprovider.go`): `KeyProvider` no longer returns root-key bytes, which no KMS can do. It has `Name`, `CurrentKeyID`, `Owns`, `Wrap(keyID, dataKey, aad)`, `Unwrap`, `Capabilities` (optional `Ed25519Sign`). The Secret Store builds the same `dek:` AAD as before and passes it in, so stored data is byte-compatible.
- **`LocalProvider`** (the former `StaticKeyProvider`, kept as a deprecated alias): today's AES-256-GCM wrapping under root keys derived from `MACHINARCHY_ENCRYPTION_SECRET[_FILE]` and the keyring. New key IDs are `local:<id>`; bare IDs written before keep working. `Owns` means "holds this key", so two instances can share a keyring.
- **`Keyring`**: the current provider plus earlier ones; Unwrap routes by owner. This is what a later provider switch (local → a KMS) uses.
- **Rewrap** accepts the bare key ID operators know and processes both stored spellings (`kek-a` and `local:kek-a`); `Remaining` counts both. An unconfigured old key still returns `ErrKeyUnavailable` (HTTP 503), as before.
- **Contract suite** `internal/secrets/providertest`: key IDs name the provider, wrap/unwrap, changed AAD/ciphertext/key ID fail, wrap only under the current key, rotation keeps old data until the old key is retired, concurrent use, keyring routing. `LocalProvider` passes it; every future provider must.
- **`MACHINARCHY_KMS_PROVIDER`** (`--kms-provider`): `local` (default); any other value fails startup with "not available in this build".
- `MACHINARCHY_ENCRYPTION_KEY_ID` is now at most 122 characters and may not contain `:`: it is stored as `local:<id>` in a `VARCHAR(128)` column on PostgreSQL, and `:` separates provider from key. A deployment with a longer ID or a `:` gets a clear startup error. Keyring (previous) IDs are unchanged.
- Docs: operations settings, runbook, `.env.example`.

### Verification

- `TestLocalProviderContract` (7 cases); `TestLocalProviderReadsBareKeyIDs`.
- `TestSecretsStoredBeforeProvidersStillWork`: a secret written in the old format (bare key ID in the stored row and the AAD) resolves with the local provider, rewraps by the bare ID to `local:kek-b`, and resolves after the old key is removed.
- Existing `TestSecretLifecycle` (rotation, rewrap, wrong key) and `TestSecretAPIIsWriteOnly` (rewrap from an unknown key is 503) pass unchanged.
- `TestKMSProvider` (default, explicit, unavailable provider, key-ID bounds); bootstrap tests.
- Store contract "secret store key providers" on **SQLite and PostgreSQL 16**: create under a 128-character `local:` key ID, resolve, rewrap to the next key, resolve with only the next key. No earlier test ran the Secret Store on PostgreSQL.
- `TestSecretKEKRewrapResumesInBoundedBatches` (store): the rewrapped row's key ID is now `local:kek-v2` (expected change); its custom raw-key provider was replaced by `LocalProvider`.
- Cached `go test ./...` passes; golangci-lint `--new-from-rev`: 0 issues.
- The contract first failed on keyring routing: `Owns` matched any `local:` ID, so two local instances claimed each other's keys. Fixed before commit.

## 2026-11-19 — Audit Phase 16: gateway signs through crypto.Signer and announces the next key (branch audit/keys)

- `internal/agentgateway`: `Config.Signer crypto.Signer` replaces the raw `SigningKey`; messages are sealed with `agentv1.SealWith`, which checks each signature before sending. A KMS-held key can plug in through the same interface (K-phases).
- `GET /api/v1/agents/controller-key` returns `publicKey`, `nextPublicKey` (during a rotation) and `publicKeys`, the exact value agents pin. Existing clients reading `publicKey` keep working. OpenAPI regenerated.
- New `MACHINARCHY_AGENT_GATEWAY_NEXT_PUBLIC_KEY`; startup refuses a next key equal to the current one and logs both.
- `docs/operations.md` "Rotating the controller signing key", `.env.example`.
- Phase 16 re-planned with the owner (AUDIT/10): built-in default provider first, KMS providers as optional K-phases. A Vault Transit signer written earlier the same day is parked on the local branch `wip/vault-signer` (`4e3ed7f`) for K1; nothing from it is on `audit/keys`.

### Verification

- `TestControllerSigningKeyRotation` (`test/e2e`, real binaries, NATS operator mode): the controller signs with A and announces B; the agent pins both; 3 commands succeed; the controller restarts signing with B; the same agent process renews its lease and runs 4 more commands; its log has no discard, signature or ERROR line.
- `TestControllerKeyListsTheNextKeyDuringARotation`, `TestAgentGatewayNextPublicKey`; gateway tests with the signer.
- Cached `go test` of touched packages; e2e package passes (one hook run failed in the e2e package under load and passed on the next two runs).

## 2026-11-19 — Audit Phase 14: SSH call timeouts follow their settings (branch audit/sessions)

- The Control Manager's SSH connector used a fixed 30 s per call, so raising runtime configuration `reconciliation.timeoutSeconds` above 30 had no effect on host checks. The connector now uses that setting (default 30 s, unchanged).
- The Operation worker's connector used a literal 15 min; it now uses `domain.MaxRunTimeout` (900 s, the same value), so the two cannot drift apart.

### Verification

- `go build ./...`, `go vet`, and the cached `go test ./...` pass; golangci-lint `--new-from-rev`: 0 issues. No dedicated test: the value is passed straight through to the existing connector constructor.

## 2026-11-19 — Audit Phase 14: client address behind proxies, token-bucket rate limits, HTTP settings from config (S-10) (branch audit/sessions)

- **Trusted proxies.** New `MACHINARCHY_TRUSTED_PROXIES` (IPs or CIDRs; `0.0.0.0/0`-style "trust everyone" is refused).
  - Empty (default): the TCP peer is the client, as before.
  - Set: `X-Forwarded-For` is read only from a listed proxy, from the right, stopping at the first address that is not a listed proxy. Echo's default trust of loopback, link-local and private ranges is turned off. A client therefore cannot choose its address by sending the header.
  - Before, every client behind a load balancer shared the balancer's address, so one client's burst or failed logins (S-09 network limit) blocked everyone.
- **Rate limiter is a plain token bucket per client network** (`domain.ClientKey`: IPv4 address, IPv6 /64).
  - Removed the escalating bans (1 min doubling to 2 h, extended by every request while banned). A refused request gets 429 with `Retry-After` = seconds until the next token and costs no token, so a client that slows down is served at once.
  - Limits are per replica; documented.
- **HTTP settings come from configuration** (env, flags, JSON file), in a new `config.HTTPConfig`:
  - `MACHINARCHY_CORS_ALLOWED_ORIGINS` (the server read `CORS_ALLOWED_ORIGINS` itself with `os.Getenv`, so it was missing from flags and the config file; the old name still works, and production still requires origins where the API is served);
  - `MACHINARCHY_RATE_LIMIT_RPS`, `_BURST` (were hardcoded 20 and 30);
  - `MACHINARCHY_HTTP_MAX_BODY_BYTES` (was hardcoded 1 MiB);
  - `MACHINARCHY_HTTP_REQUEST_TIMEOUT` (was the constant 25 s); the listener's write timeout is now derived from it (+5 s), so the deadline always ends first (Q-20).
- Docs: `docs/operations.md` ("Behind a reverse proxy", "Rate limits", settings, timeouts), README, `.env.example`, `docs/SECURITY.md` and the govulncheck allowlist comment (GO-2026-6872 still does not apply: Echo's limiter is unused and our extractor trusts only listed proxies).

### Verification

- `TestClientIPExtractor`: no proxies ignores the header; a forged header from an untrusted peer is ignored; a client cannot prepend its own address; chains of proxies; private and loopback ranges not trusted unless listed; IPv6.
- `TestRateLimiterIsATokenBucketPerClientNetwork`: burst, 429 with `Retry-After: 2` that does not grow while hammering, other clients unaffected, one IPv6 /64 shares a bucket and another /64 does not.
- `TestLimitsUseTheClientBehindATrustedProxy` (wired server): through one proxy, client A's network login limit and client B's rate limit do not affect other clients; changing `X-Forwarded-For` from an untrusted peer does not escape the limit.
- `TestLoadHTTPSettings` (defaults, the old CORS name, every setting, bounds) and `TestAllowedOrigins`.
- `go test ./...` (cached) passes; golangci-lint `--new-from-rev`: 0 issues. govulncheck traces for GO-2026-6872/6875 are unchanged in kind (package-wide symbols).

## 2026-11-19 — Audit Phase 14: failed-login throttling and constant-time login (S-09) (branch audit/sessions)

- **Throttling shared in the database.** Migration `000006_login_failures` (SQLite and PostgreSQL) counts failures per account and per client network with one atomic upsert each, so every replica enforces the same limits and concurrent failures are never lost.
  - Per account: after `MACHINARCHY_LOGIN_FREE_FAILURES` (5) failures in `MACHINARCHY_LOGIN_FAILURE_WINDOW` (15m), each attempt waits 1 s, doubling up to `MACHINARCHY_LOGIN_MAX_BACKOFF` (15m). No lockout, so an attacker cannot lock an owner out.
  - Per network: `MACHINARCHY_LOGIN_IP_MAX_FAILURES` (20) failures across accounts block the network until the window ends; IPv6 clients count per /64 (`domain.ClientKey`).
  - Refused attempts get 429 with `Retry-After`. A success resets the account. The start of backoff is audited once per window (`auth.login_throttled`, outcome `denied`).
  - A wrong current password on `POST /api/v1/me/password` counts as a failed login.
- **Login no longer reveals which accounts exist.** An unknown user is compared against a dummy bcrypt hash, so it costs the same time, and is throttled under its name like a real account. Before: an unknown username answered in about 0.5 µs, a real one in about 470 ms.
- Docs: `docs/operations.md` (Login sessions, settings), `docs/runbook.md` ("A user cannot log in (429)"), README, `.env.example`.

### Verification

- `internal/domain`: policy bounds, the backoff curve, `ClientKey` (IPv4, IPv4-mapped, IPv6 /64, junk).
- `TestLoginTimingDoesNotRevealAccounts` fails on the previous code and passes now.
- Store contract "login throttling" on **SQLite and PostgreSQL 16**: counting, the window reset, account and network blocks, the audit once per window, reset, and 20 concurrent failures all counted.
- End to end (`TestLoginThrottlingEndToEnd`): 429 with `Retry-After: 1` for the right password during backoff, success after it, the network limit across accounts including unknown ones, another network unaffected, one audit event.
- Config: `TestLoadSessionAndLoginSettings`.
- `go test ./...` (cached) passes; golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 14: revocable sessions, key rotation, password change (S-08) (branch audit/sessions)

- **Server-side sessions.** Migration `000005_user_sessions` (SQLite and PostgreSQL; times stored as Unix nanoseconds).
  - Every signup and login records a session; the JWT carries its ID (`sid`).
  - `JWTAuth` accepts a session token only while its session is active. The check is one query that also returns the user's current role, replacing the membership lookup each request already made.
  - A user keeps at most 50 active sessions (the oldest is revoked past that), and long-ended rows are pruned on the next login.
- **Logout really logs out.** `POST /api/auth/logout` revokes the session of the token sent (bearer or cookie), so a copied token stops working at once. Before this, logout only cleared the cookie.
- **New self-service endpoints** (login sessions only; API tokens get 403; listed in the authorization matrix as `humans`):
  - `GET /api/v1/sessions`;
  - `DELETE /api/v1/sessions/{id}`;
  - `DELETE /api/v1/sessions` (log out everywhere);
  - `POST /api/v1/me/password`, which checks the current password, enforces 12–72 bytes, requires the new password to differ, and revokes every session atomically with the change.
  - Audited: `auth.session_revoked`, `auth.sessions_revoked`, `user.password_changed`.
- **Signing keyring with `kid`** (new `internal/sessionkey`).
  - Tokens are signed with `MACHINARCHY_OPERATOR_TOKEN` and name it by a derived key ID; `MACHINARCHY_SESSION_PREVIOUS_KEYS` lists earlier values, still accepted during a rotation.
  - Token validation lives in one place (`Keyring.Parse`) and is shared by the middleware and logout.
- New `MACHINARCHY_SESSION_TTL` (default 24h, 5m–168h) replaces the hardcoded 24 h in both the service and the middleware. Bootstrap resolves the effective value once for both: a zero-value config used to give the middleware a lifetime of 0, which rejected every token.
- **Upgrade note:** tokens issued before this release have no session ID, so users log in again once.
- Docs: `docs/operations.md` "Login sessions" (actions, rotation procedure, upgrade note) and the settings; README; `.env.example`; OpenAPI regenerated.

### Verification

- Unit tests:
  - `internal/service`: logout, revoke, log out everywhere, the current flag, API tokens refused, and every password-change rule;
  - `internal/transport/http/middleware`: no `sid`, a revoked session, no `kid`, an unknown `kid`, the previous key accepted, and the previous key's `kid` with the current secret refused;
  - the auth handler: logout passes the bearer or cookie token, and never an API token.
- Store contract "login sessions" on **SQLite and PostgreSQL 16**. It covers:
  - the role from an active session;
  - another user's, unknown, expired and malformed IDs;
  - revoking once, audited;
  - the 50-session cap;
  - log out everywhere;
  - password change atomic with revocation;
  - a multi-byte user agent cut without breaking UTF-8.
- End to end through the wired server (`internal/bootstrap/sessions_test.go`):
  - a copied token is refused after logout;
  - revoke by ID, and log out everywhere;
  - password change (wrong current gives 403, a short new one 400, every session ends, the old password fails and the new one works);
  - **key rotation across restarts** (the previous key is accepted while listed and refused once dropped).
- The authorization matrix covers the 4 new routes.
- `go test ./...` (cached) passes; golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13C: the guide uses the repository's SSH test servers (branch audit/docs-guide)

- `docs/cli/user-full-guide.md` rewritten on the existing fixtures, at the owner's request: `make ssh-servers-up` and `deployments/testing/ssh-servers.docker-compose.yml`, instead of ad-hoc `docker run` containers.
  - Hosts `ssh-key` (2222, key login, `tofu`), `ssh-password` (2221) and `ssh-hostcheck` (2223), both password login.
  - The password hosts show that Machinarchy never trusts keys on first contact for a password login: they wait for `trust host --all-pending`.
  - `Lost` is shown with `make ssh-servers-restart SSH_SERVICE=ssh-key`.
  - The changed-key case is a host pinned to another server's keys, so the fixtures' persisted host keys in `data/.ssh/` are never touched.
  - Cleanup is `make ssh-servers-down`, which keeps the lab's keys.
- Every command was run again for real against the fixtures, and the outputs were replaced. Absolute paths are shortened and cut lines are marked `…`. The lab's credentials are piped from `data/.ssh/` into write-only secrets, never printed.
- The run left only empty runner directories (`.local/state/machinarchy/ops`) in the fixtures' bind-mounted homes; those were removed with `rmdir`.

### Verification

- The full lab script ran end to end on the fixtures with no timeouts; the server log had no warnings or errors.

## 2026-11-19 — Audit Phase 13C: full CLI user guide (branch audit/docs-guide)

- DOC-GUIDE: new `docs/cli/user-full-guide.md`, a step-by-step lab with three SSH servers (OpenSSH containers `web-1`, `web-2`, `db-1` on ports 2221–2223). It covers:
  1. prerequisites and a lab-only CLI config;
  2. the SSH lab;
  3. server settings, each explained;
  4. login;
  5. CLI contexts;
  6. a write-only secret;
  7. Access Profiles with `tofu` and `approve`;
  8. hosts with labels;
  9. host-key trust (automatic, and `trust host`);
  10. attached runs;
  11. detached runs with `logs --follow` and `--stream stderr`;
  12. labels and selectors;
  13. cancel;
  14. long runs and `Lost`;
  15. a reinstalled host (`HostKeyChanged`, `trust host --replace`);
  16. history;
  17. an operator API token and a second context;
  18. troubleshooting, cleanup, and a cheat sheet.
- **Every command was run for real** on 2026-11-19 against that lab, with the make-built monolith and CLI (`ff666e1`), and the outputs are pasted from the run. The run surfaced three product problems; the guide documents them as known problems and AUDIT/13C records them.
- Linked from `docs/cli/README.md`, the main README (which also points at `docs/api`) and `docs/api/README.md`.

### Verification

- The lab script ran every step twice end to end (the second run is the guide's source); the server log had no warnings or errors.
- The API-token step ran against a separate fresh server.
- Link targets in the guide checked: `operations.md` and `runbook.md` anchors, `commands.md`, `docs/api/README.md`.

## 2026-11-19 — Audit Phase 13C: API reference moved to docs/api (branch audit/docs-guide)

- DOC-API: the generated OpenAPI files moved from `docs/swagger.{json,yaml}` to `docs/api/swagger.{json,yaml}` (with `git mv`). Updated everything that refers to them:
  - `make api-docs` now writes to `docs/api`;
  - `docs/embed.go` embeds `api/swagger.json`; the server still serves it at `/openapi.json`, and the reference UI at `/scalar`, when `MACHINARCHY_API_DOCS_ENABLED` is on;
  - the Dockerfile copies the new path, and `.dockerignore` allows it;
  - the spec test messages, and `docs/architecture.md`.
- New `docs/api/README.md`: how the files are generated and served.
- Regenerating also picked up one comment changed in Phase 13B and not yet in the spec (`TargetOutput.live`).

### Verification

- `make api-docs` writes only to `docs/api/`; no file is left at the old path.
- `go build ./...`: pass.
- `go test ./internal/transport/http/v1/` (`TestOpenAPISpecCoversEveryRoute` against the embedded spec) and `./internal/bootstrap/` (the authorization matrix, which reads every route from `/openapi.json`): pass.
- The container image build was not run (no Docker build in this phase); the changed `COPY` lines were checked by reading them.

## 2026-11-19 — Audit Phase 13B: detached tests poll at a sane pace (branch audit/detached-ssh)

- Each detached status poll runs the runner script on the "host", which in these tests is the machine itself: about 20 short processes per target. At 150 ms over 50 separate hosts, the restart test launched thousands of processes per second, and under `-race -count=3` it ran 157–199 s instead of about 15 s.
- The tests now poll at 300 ms (`fastPolls`), and the 50-target restart test at 1–2 s, like the PostgreSQL test. Production polls every 5–30 s, and each host runs only its own targets' scripts.

### Verification

- `go test -timeout 30s ./internal/operationworker/`: pass in 21.8 s (it was about 35 s).
- The full race suite could not be judged here: with no test running, this machine showed 471 runnable processes and a load average above 800 (Spotlight indexing, Chrome, macOS services). Timeouts across unrelated packages in the last full runs match that.

## 2026-11-19 — Audit Phase 13B: consistent Operation reads; full-suite stability (branch audit/detached-ssh)

- **Pre-existing bug, fixed:** `Store.GetOperation` read the Operation row and its target rows with two separate queries. A reader could combine an Operation still `Queued` with a target a worker had already started, and fail validation ("queued Operation targets must be pending"). That is a spurious error for any API reader. Detached runs move targets right after the start, which made it visible in the full suite. It now reads both in one read-only transaction (`REPEATABLE READ` on PostgreSQL; a deferred transaction, a WAL snapshot, on SQLite).
- **Older test race, fixed:** `TestManagedHostAndOperationEndToEnd` cancelled the `hold` command as soon as its target was `Running`. The attached worker marks it Running before the SSH session opens, so under load the cancel could win and the exec never happened ("remote exec count=2, want 3"). The test now also waits for the exec to reach the server.
- The contract fixture now keeps idle PostgreSQL connections (`MaxIdleConns: 8`, T-13B-01 for the fixture). The PostgreSQL contract run of the new read test went from 86–299 s to about 30 s.
- Detached integration tests: at most 4 environments run at once, and the wait limits rise to 90 s. Under the full shuffled race suite, ten parallel tests spawning real shells starved other packages; the authorization matrix's bootstrap hit its deadline.

### Verification

- New contract subtest "operation reads are consistent" (SQLite and PostgreSQL): 4 readers call `GetOperation` while a writer starts 40 Operations and moves their targets.
  - It **fails on the old code** on SQLite in 2 of 2 runs and on PostgreSQL in 1 of 2 runs;
  - it passes with the fix on both engines in 2 of 2 runs.
- `TestManagedHostAndOperationEndToEnd`: `-race -count=3` pass.
- `go test -timeout 30s ./internal/operationworker/` (the hook's limit): pass twice.
- golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13B: migrations get their own deadline (branch audit/detached-ssh)

- `store.Open` ran the ping, the SQLite pragmas **and every migration** under the connect timeout (5 s by default). Migration `000004` rewrites `operation_targets` on SQLite, which takes time proportional to the stored output: an 834 MB database with 100,000 targets took **3 minutes** on a laptop. **The first start after an upgrade would have failed.**
  - It showed up as SQLite stores timing out during migrations in the full shuffled race run.
  - Fix: migrations now run under `store.Config.MigrationTimeout` (default 10 min), set by the new `MACHINARCHY_DATABASE_MIGRATION_TIMEOUT` (`--database-migration-timeout`). The connect timeout still covers connecting and session setup.
- `docs/operations.md` (Upgrades): the new setting, and the rewrite and its cost on SQLite. `.env.example`: the setting.
- Removed the unused `resource.SanitizeOutput` (deadcode).

### Verification

- `TestLoadDatabaseMigrationTimeout`: default, override, and invalid values.
- Migration 000004 timed by hand with `sqlite3` on an 834 MB version-3 database: 179.5 s, and all 100,000 rows kept.
- `go test ./internal/config/ ./internal/store/ ./internal/bootstrap/`: pass. golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13B: benchmark, live run, docs (branch audit/detached-ssh)

- **Benchmark** (`TestBenchmark200TargetsFiveMinutes`, opt-in with `MACHINARCHY_BENCH_DETACHED=1`): 200 targets × a 5-minute command, 4 workers, production poll intervals.
  - All 200 launched in 27 s; the Operation succeeded after **5 min 49 s**. Run attached and one at a time, the same work would take about 16.7 h.
  - 4,657 polls, 0 unreachable, 0 lost.
- **Live run** against real OpenSSH (`linuxserver/openssh-server`: setsid available, no systemd), with the make-built monolith and CLI:
  - `run` detaches and `logs --follow` streams the output as it arrives;
  - a monolith restart mid-run: the command survived, finished `Succeeded` and ran exactly once;
  - cancel gives `Cancelled` with exit 143 and no process left;
  - a host restart gives `Lost` / `supervisor_lost`;
  - no state directories are left on the host;
  - the server log has no warnings or errors.
- Fixes found by the live run:
  - `logs --follow` returned at once while the target was still Pending: the output endpoint now reports `live` until the target is final;
  - `sshclient` no longer logs `WARN failed to close SSH session: EOF`. After `Run`, the server has normally closed the channel already, and that warning would have appeared on every detached poll.
- Docs:
  - `docs/operations.md`: new "Detached execution" section (how it runs, supervisors, timeouts and cancel, Lost, restarts, output, limitations), the metrics and alerts, and the timeouts table;
  - `docs/runbook.md`: "A detached target is Lost" and the launch failure classes.

### Verification

- `go test ./pkg/sshclient/ ./internal/domain/resource/`: pass, including a new check that a Pending target reads as live.
- The live run above.

## 2026-11-19 — Audit Phase 13B: runner on a real systemd host (branch audit/detached-ssh)

- New `test/systemd/`:
  - a Debian image with systemd as PID 1 and OpenSSH (`Dockerfile`; the user `deploy` has lingering, `nolinger` does not);
  - `systemd_test.go` (build tag `systemd`), which drives the runner over real SSH in a privileged container;
  - `run.sh`, which builds the image and runs it.
  - It is not part of `go test ./...`.
- What it proves:
  - root runs under the system manager: the command's cgroup is `machinarchy-op-1-t-1.service`;
  - a lingering user runs under `user@1000.service` (`scope=user`);
  - a user without lingering falls back to setsid;
  - **cancel under systemd also kills a process that escaped with `setsid`** (KillMode=control-group); in setsid mode that process survives, the documented limitation;
  - the host-side timeout works under systemd;
  - a stopped unit (what a reboot does) is reported `lost`;
  - no unit is left active.

### Verification

- `test/systemd/run.sh`: all 8 subtests pass (Debian bookworm with systemd 252, Docker Desktop, cgroup v2). golangci-lint `--build-tags systemd`: 0 issues. `shellcheck`: clean.

## 2026-11-19 — Audit Phase 13B: two workers on PostgreSQL; single-row credential authorization (branch audit/detached-ssh)

- New `TestTwoWorkersOnPostgresRunEachCommandOnce`: two workers with production-style leases share 50 detached targets, one is stopped mid-way, and every command still runs exactly once. It exercises the Operation lease for launches and the per-host lease for polls.
- New `internal/testutil/pgtest`: a fresh schema per test on `MACHINARCHY_TEST_POSTGRES_URL`; the test is skipped without it, or fails when `MACHINARCHY_REQUIRE_POSTGRES_TESTS` is set.
- `secrets.ProfileAuthorizer`: when the reader implements `resource.OperationTargetReader` (the store and `bootstrap.runtimeProfileBindings` do), operation-worker credential checks read one target row (`GetOperationTarget`) instead of loading and decrypting the whole Operation. Every detached poll resolves a credential, so this matters at scale. The rules are unchanged: active Operation, same Host generation, same captured profile revision.
- Detached integration tests can show worker logs with `MACHINARCHY_TEST_WORKER_LOGS=1`.

### Found while doing it

- `store.Config` with `MaxIdleConns` 0 keeps no idle PostgreSQL connections, so every query opens and authenticates a new connection: about 56 ms each here, against 8 ms pooled. Production sets 5 idle by default (`MACHINARCHY_DATABASE_MAX_IDLE_CONNS`), but tests that build `store.Config` directly (the contract fixture among them) pay it. This is why the PostgreSQL contract suite takes about 87 s. Recorded in AUDIT/13B; not changed here.
- Polling cost on PostgreSQL is roughly 10 queries plus one SSH session per host per poll (host lease, credential resolution, progress). Fine at the 5–30 s production intervals; the 150 ms test interval saturated a laptop, so the PostgreSQL test polls at 1–2 s.

### Verification

- `TestTwoWorkersOnPostgresRunEachCommandOnce`: pass in 33 s on PostgreSQL 16, running in a tmpfs container with `fsync=off`. Docker Desktop's disk made checkpoints take 50–74 s.
- `TestProfileAuthorizerSingleRowTargetRead`: a changed Host, an uncaptured revision and a finished Operation are all refused, and the full Operation is never read.
- `go test ./internal/secrets/ ./internal/operationworker/`: pass. golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13B: follow output by offset; CLI run --detach and logs (branch audit/detached-ssh)

- The output endpoint takes `from` (a stream byte offset) and returns `from` and `next`, computed from the capture's real byte positions (`StreamCapture.Slice`). A part of the stream that was not kept is reported as one `[… N bytes omitted …]` line, so a follower never repeats or misses bytes.
- A detached target now keeps its final capture after it ends (encrypted, at most 15 KiB head plus 15 KiB tail per stream), so output stays readable by offset afterwards; the poller stores the last chunk before recording the result. Domain validation allows a capture on any launched target.
- CLI:
  - `run` without `--wait` is detached, with launch concurrency up to 20; `--wait` stays attached unless `--timeout` is over 900 s or `--detach` is given;
  - new `logs operation ID [--host] [--stream stdout|stderr] [--follow]`, which prints every target with a header when no host is given;
  - `api.TargetResult` gains `stdout`, `stderr`, byte counts and `truncated`, and `get operation` shows the first line of stdout, stderr or output. Before this, every result stored with separate streams showed an empty OUTPUT column.
- Docs: `docs/cli/README.md`. OpenAPI regenerated.

### Verification

- `TestOutputOfByOffset`: follow reads, a read across the omitted part, a read at the end, attached and legacy results.
- `TestDetachedRunReportsOutputAndCleansUp` now also reads the final output from an offset.
- CLI tests:
  - `run` detaches without `--wait`, stays attached with `--wait`, and detaches with `--timeout 3600` or `--detach`;
  - `logs --follow` prints the whole stream exactly once;
  - stderr by host ID, all-target headers, and usage and not-found errors;
  - `TestTargetOutputRequest` (path escaping and query);
  - the command-tree test lists `logs operation`.
- `go test ./internal/cli/... ./internal/domain/... ./internal/service/ ./internal/operationworker/ ./internal/store/`: pass. golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13B: target output endpoint (branch audit/detached-ssh)

- `GET /api/v1/managed/operations/{id}/targets/{hostId}/output?stream=stdout|stderr` returns `resource.TargetOutput`:
  - live while a detached command runs, final afterwards;
  - `text`, `bytes` (the full size), `truncated` (a 15 KiB head and tail are kept) and `live`;
  - escape sequences are stripped.
  - Access: only callers allowed to execute runs (AUDIT S-04); readers get 403. Listed in the authorization matrix as `writers`.
- The live capture is never part of the Operation JSON (`OperationTarget.Live` is `json:"-"`; the store encodes it separately). Readers instead see `progress` (stream sizes, last contact, poll count, cancel reason, no output). `redactOperation` also drops the capture.
- `RunSpec.detach` is accepted by the submit endpoint, with timeouts up to 24 h when set.
- OpenAPI regenerated (`make api-docs`).

### Verification

- New tests:
  - `TestDetachedSubmissionAndOutputAccess`: an attached 2 h run is refused and a detached one accepted; an operator reads output, a reader gets 403; a bad stream gives 400 and an unknown target 404;
  - `TestOperationJSONOmitsLiveOutput`;
  - `TestOutputOfRunningAndFinishedTargets`.
- `go test ./internal/bootstrap/` (authorization matrix, including the new route), `./internal/service/` and `./internal/transport/...`: pass. golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13B: detached launcher and poller (branch audit/detached-ssh)

- Launcher (`internal/operationworker/detached.go`):
  - a detached Operation launches its targets `RunSpec.Concurrency` at a time; a worker slot is held only for the launch;
  - each target gets a launch handle, with its deadline set to the earlier of launch time plus the timeout and the Operation deadline;
  - a session that failed before anything could start fails the target with a `launch_*` class;
  - a connection lost during the launch records an unconfirmed handle for the poller to resolve;
  - a launch already sent is not cut off by worker shutdown or cancel; it finishes and the poller cancels the command if needed;
  - restart: a detached Operation is never marked Interrupted; Pending targets are resumed.
- Poller (`internal/operationworker/poller.go`), started with the worker when the store and connector support detached runs:
  - one status session per host (per profile revision and state root, up to 4 targets each); on PostgreSQL a per-host lease keeps two workers from polling the same host;
  - the interval grows from 5 s to 30 s with the target's age;
  - output is captured as head and tail; an exited target is finished only after all its output has been read;
  - cancel and timeout are sent by the poller; a timeout is enforced past the deadline plus a grace if the host watchdog did not act;
  - `Lost` when the supervisor is gone, the state directory vanished, or the host stays unreachable past the deadline (or 5 min after a cancel);
  - an unconfirmed launch the host knows about is adopted (`mode: adopted`); one proven never to have started is relaunched, up to 3 attempts;
  - a finished target's directory is cleaned up before its result is recorded, because the credential authorizer only serves active Operations; the Operation finishes once every target is terminal.
- Runner: a launch now records the launching shell's pid. A directory without `meta` whose launcher died is reclaimed (`busy` if the launcher is still alive), and status reports it `missing` instead of `lost`.
- Metrics: `operationworker_detached_launches_total{outcome}`, `_running_targets`, `_polls_total{outcome}`, `_lost_targets_total`, `_poll_duration_seconds`.

### Verification

- New `internal/operationworker/detached_test.go`: real SQLite store, secret resolution and SSH connector, against an SSH server running commands in a real shell. Tests:
  - live output while running, the final result with newlines and byte counts, and no state left on the host;
  - **a restart with 50 targets loses none, and each command runs exactly once**, even when the worker stops mid-launch (39 Pending and 11 Running at the stop);
  - cancel stops whole process trees;
  - timeout enforced on the host, and by the poller;
  - a killed supervisor gives `Lost`;
  - an unreachable host gives `Lost`;
  - an unconfirmed launch is adopted, or retried and then failed;
  - a profile with `supervisor: attached` refuses detached runs.
- `go test -race -count=2 -shuffle=on` on `operationworker`, `hostaccess/...` and `domain/...`: pass. golangci-lint `--new-from-rev`: 0 issues. `shellcheck`: clean.
- Test duration: the integration tests run in parallel; `operationworker` takes about 22 s and `hostaccess/runner` about 7.5 s, within the pre-commit hook's 30 s per package. The runner tests' fake `systemd-run`, `systemctl` and `loginctl` are now one shared script reached through symlinks, because macOS scans every new executable on its first run (about 3 s each).
- Problems found and fixed while writing these tests:
  - a stopping worker failed targets as `host_snapshot_stale`;
  - a cleanup after the last result was refused by the authorizer;
  - a half-made launch directory was adopted as a running command.

## 2026-11-19 — Audit Phase 13B: detached-execution model and persistence (branch audit/detached-ssh)

- Domain (`internal/domain/resource/detached.go`, `operation.go`, `access_profile.go`):
  - `RunSpec.Detach`: timeouts up to 24 h when detached, 900 s otherwise;
  - the terminal target state `Lost`;
  - `LaunchHandle` (mode, unit, pid, state root, endpoint, launch time, deadline; an empty mode means an unconfirmed launch);
  - `LiveOutput` with a `StreamCapture` per stream, which keeps a 15 KiB head and a 15 KiB tail, handles repeated chunks and gaps, and renders with an omission marker;
  - `TargetResult` gains `stdoutBytes`, `stderrBytes` and `truncated`;
  - Access Profiles gain `execution` (`supervisor` auto/systemd/setsid/attached, `stateRoot`, `maxOutputFileBytes`).
- Output keeps newlines and tabs: `sanitizeOutput` still removes other control characters (so no terminal escapes), replaces invalid UTF-8 and never splits a rune. Before this, stored stdout and stderr had every newline replaced by a space.
- Migration `000004_detached_targets`:
  - SQLite rebuilds `operation_targets` to allow `Lost`; PostgreSQL swaps the CHECK constraint;
  - both add `launch` (plain JSON) and `live` (encrypted) plus a partial index.
  - Checked by hand that the SQLite rebuild keeps existing rows (`integrity_check` ok).
- Store (`operation_detached.go`), all single-row writes:
  - `RecordTargetLaunch` (audited, fenced like target updates);
  - `UpdateTargetLive` (encrypted, not audited, because it is progress);
  - `ListDetachedTargets`.
  - `GetOperation` returns the launch and live data; a terminal write clears the live data.

### Verification

- New domain tests: capture fits, gap, unread bytes, output budget, sanitizer, timeout bounds, live output only while running, execution settings.
- New contract subtest "detached targets" passes on SQLite and PostgreSQL 16. It covers launch only on an active Operation, re-recording, encrypted progress, listing, the `Lost` round-trip with byte counts, and terminal targets refusing launch and progress.
- The migration-count contract now expects 4.
- `go test ./internal/...`: pass. golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13B: detached-execution runner (branch audit/detached-ssh)

- New `internal/hostaccess/runner`: the POSIX `sh` runner of AUDIT/10b §1.2 (`runner.sh`, embedded), plus Go command builders and strict reply parsers.
- Actions:
  - `launch`: an idempotent `mkdir` lock; a retry adopts the running process;
  - `status`: running, exited, lost, missing; output deltas with head and skip-ahead chunks;
  - `cancel`: TERM to the process group or tree, KILL after 10 s;
  - `cleanup`;
  - a `sweep` of directories older than the retention period, run on launch.
- Supervisor: `systemd-run` (system, or `--user` with lingering) when usable, otherwise `setsid`/`nohup`. A host-side watchdog enforces the timeout in both modes, and systemd adds `RuntimeMaxSec` and `KillMode=control-group`.
- Safety:
  - the command reaches the host only through `domain.QuoteArgv`;
  - identifiers and the state root are checked in Go and again by the script;
  - state is owner-only (`umask 077`);
  - protocol lines are prefixed, so output from shell start-up files is ignored.
- Not wired into execution yet.

### Verification

- `go test -race -count=3 ./internal/hostaccess/runner/`: pass with `/bin/sh` (bash 3.2) and `dash`. The tests cover:
  - idempotent launch;
  - verbatim arguments;
  - incremental output and skip-ahead;
  - cancel kills the whole tree;
  - host-side timeout;
  - a dead supervisor gives `lost`;
  - cleanup leaves running targets;
  - a fake `systemd-run` (arguments, fallback, and a forced `systemd` mode that is refused);
  - crafted IDs and roots refused by Go and by the script;
  - parser rejection cases.
- `shellcheck -s sh -S warning`: clean for the runner and the wrapper. golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13A: host-key change audit and metric (branch audit/host-keys)

- AUDIT/10b §2.2 asks for an audit event and a metric on a key change. Both were missing.
- Audit:
  - `internal/store/resources/store.go`: `ObserveHost` audits the transition into `HostKeyChanged` as `resource_host.host_key_changed` (`domain.AuditResourceHostKeyChanged`), with `presented` and `trusted` fingerprints, once per transition; other observations stay `resource_host.updated`.
  - `resource.ReadyReason` helper.
- Metric:
  - `internal/controlmanager/host_controller.go`: `HostController.RegisterMetrics` exports `machinarchy_controlmanager_host_key_events_total{event}` (`pending_approval`, `changed`, `tofu_pinned`), counted per transition;
  - registered in `bootstrap.Runtime`.
- Docs: `docs/operations.md` (metric and suggested alert), `docs/runbook.md` (`HostKeyChanged`).

### Verification

- `TestChangedHostKeyIsNeverAccepted` waits for two further controller retries, then expects exactly one `host_key_changed` audit row naming both keys and a counter of 1. It fails when the transition guard is removed (mutation run; file restored).
- `TestTOFUPinsEveryHostKeyThenVerifies` checks `tofu_pinned` = 1.
- `go test -race -count=2` on the host-key tests: pass.

## 2026-11-18 — Audit Phase 13A: host-key store contract on both engines (branch audit/host-keys)

- `internal/store/resource_contract_test.go`: new contract subtest "host-key observations and trust". It runs on SQLite and PostgreSQL and covers:
  - migration 000003: `observed_host_keys` round-trips including `lastSeenAt`, and an observation without keys keeps the stored ones;
  - `TrustHostKeys`: a stale revision and a change without replace are refused; trust bumps the generation; every trust writes one `resource_host.host_key_trusted` audit row and one `host.spec-changed` outbox event with change `host-keys-trusted`.

### Verification

- `go test -race -run TestManagedResourceContract ./internal/store/` on SQLite, and with `MACHINARCHY_TEST_POSTGRES_URL` set (postgres:16-alpine container, `MACHINARCHY_REQUIRE_POSTGRES_TESTS=1`): pass on both engines.
- `./internal/store/...`, `./internal/service/...` and `./internal/hostaccess/...` on PostgreSQL: pass.

## 2026-11-18 — Audit Phase 13A: complete host-key scans, rate-limited rescans (branch audit/host-keys)

- `pkg/sshclient/hostkeys.go`: `ScanHostKeys` used to skip a key type whose connection failed and return the rest. Seen live: an `approve` scan offered 2 of 3 keys. It now retries a failed type once, after 500 ms, and if it still fails the whole scan fails. A refused or policy-denied target is not retried.
- `internal/hostaccess/reconciler.go`: the reconciler reuses a Host's last scan for `DefaultHostKeyRescanInterval` (10 min, `WithHostKeyRescanInterval`) while the Host is pending approval or `HostKeyChanged`. A new generation, or a cached scan that still shows a trusted key (it predates the change), forces a rescan.
  - Why: seen live, OpenSSH 9.8+ `PerSourcePenalties` ("noauth") blocked the controller's address after it rescanned a pending Host on every retry. The block then broke the verify after approval.
- `ObservedHostKey.LastSeenAt` (domain and CLI): only a fresh scan sets it, so reuse cannot extend its own window.
- `docs/operations.md`: the scan is all or nothing; why scans are rate limited and what to do on `srclimit_penalise`.

### Verification

- New tests:
  - `TestScanHostKeysRetriesADroppedConnection` and `TestScanHostKeysNeverReturnsAPartialSet` (a dialer drops scan connections);
  - `TestPendingHostReusesARecentScan` (5 reconciles give 1 scan; after the interval there is a 2nd, after a spec change a 3rd);
  - `TestChangedKeyRescansOnceThenReuses`.
- `go test -race` on `internal/hostaccess`, `internal/domain/...`, `pkg/sshclient`, `internal/controlmanager` and `internal/cli/...`: pass. `golangci-lint --new-from-rev=HEAD`: 0 issues.

## 2026-11-18 — Audit Phase 13A: an event no longer waits behind a delayed retry (branch audit/host-keys)

- `internal/controlmanager/queue.go`: `Add` for a key that already had a delayed requeue (`AddAfter`, e.g. the Host controller's connector retry) was coalesced into the delayed entry. The outbox event was acknowledged, but the reconcile waited out the whole delay. An immediate `Add` now replaces the pending delay. Found live: an owner's host-key approval sat at `HostKeyPendingApproval` until the retry interval; this affected any spec change to a Host that was not Ready.

### Verification

- `TestResourceQueueAddOverridesDelayedRetry`: fails without the fix and passes with it. `go test -race -count=3 ./internal/controlmanager/`: pass.

## 2026-11-18 — Audit Phase 13A: endpoint changes clear trust; host-key docs (branch audit/host-keys)

- `internal/service/managed_resources.go`: changing a bound Host's endpoint (owner only, AUDIT S-02) clears its trusted keys unless new trust is sent with the change, so the new endpoint is scanned again under the profile's policy.
- `docs/operations.md`: new "SSH host keys" section (policies, the deployment default, the safety rules) and the setting. `docs/runbook.md`: `HostKeyPendingApproval`, `HostKeyChanged`, `HostKeyNotTrusted`. `README.md` and `.env.example`: the setting.

### Verification

- `TestEndpointChangeClearsHostKeyTrust` (an operator move is refused; an owner move clears trust; explicit new trust is kept), `TestOperatorCannotRedirectOwnerCredential` and `TestManagedResourceFlow`: pass. golangci-lint on `internal/service`: 0 issues.

## 2026-11-18 — Audit Phase 13A: host-key approval API and CLI (branch audit/host-keys)

- `POST /api/v1/managed/hosts/{id}/trust` `{generation, fingerprints, replace}` (owner only, through the service). It is in the OpenAPI spec and in the authorization matrix, which flagged the route as missing before its entry was added.
- CLI:
  - `machinarchy trust host NAME-OR-ID [--replace] [--fingerprint …]` and `--all-pending [--selector …]`;
  - it always prints the hosts and fingerprints to be trusted before asking for confirmation (also with `--yes`), and sends exactly those fingerprints with the generation it displayed;
  - it refuses to touch a host that already trusts keys without `--replace`, and refuses a host with no observed keys.
  - The CLI's `HostStatus` gains `observedHostKeys`.
- `docs/cli/README.md`: the commands.

### Verification

- `go test ./internal/cli/...`: pass. New subtests:
  - no change without confirmation, with the keys shown anyway;
  - trust sends the observed keys at the displayed generation;
  - `--replace` is required to change keys, and `--fingerprint` overrides them;
  - a host with no observed keys is refused;
  - `--all-pending` with a selector only approves pending matching hosts;
  - 3 usage errors;
  - the command tree test lists `trust host`.
- `TestRouteAuthorizationMatrix` and the route-coverage test pass. golangci-lint: only the 2 pre-existing issues.

## 2026-11-18 — Audit Phase 13A: automatic SSH host-key trust (branch audit/host-keys)

**Behaviour change:** a Host bound to an Access Profile no longer needs a hand-entered fingerprint. Its keys are trusted according to the profile's `hostKeyPolicy`: `tofu` pins them on first contact; `approve` (the production default) waits for an owner; `ca` trusts the profile's host certificate authorities; `pinned` is the old behaviour.

- **Domain** (`internal/domain/resource/hostkeys.go`, new):
  - `HostKeyPolicy`, and `AccessProfileSpec.HostKeyPolicy` plus `HostCertificateAuthorities` (validated: `tofu` requires private-key authentication; `ca` needs 1–8 keys). `EffectiveHostKeyPolicy` turns `tofu` into `approve` for password profiles, so an impostor on first contact never receives a password.
  - `HostStatus.ObservedHostKeys`.
  - `Host.TrustHostKeys` (1–8 distinct `SHA256:` fingerprints; it refuses to replace existing trust unless asked).
  - `HostObservation`.
- **Store:**
  - migration `000003` adds `managed_hosts.observed_host_keys`;
  - `ObserveHost` records observed keys;
  - new `TrustHostKeys`, which bumps the generation, emits a spec-changed event and audits `resource_host.host_key_trusted` with the fingerprints, the source (`tofu` or `owner`) and any replaced keys.
- **Reconciler** (`internal/hostaccess`):
  - no trust: scan before authenticating, then pin (`tofu`) or report `HostKeyPendingApproval` with the fingerprints (`approve`); `pinned` never scans;
  - a trusted key no longer presented gives `HostKeyChanged`, naming the presented and trusted keys, with no authentication attempted and nothing replaced.
  - The connector verifies against the whole fingerprint list and the profile's CAs, and refuses to authenticate with no trust at all. `WithDefaultHostKeyPolicy` sets the default.
- **Controller:** under TOFU it pins first; the spec-changed event then reconciles again with the pinned keys.
- **Service:** owner-only `TrustHostKeys`. It requires the exact fingerprints the owner reviewed (approving "whatever is observed now" could trust a key that changed in between) and the spec generation the owner saw (status writes do not conflict with it). CA keys are parsed when a profile is created or updated.
- **Config:** `MACHINARCHY_SSH_HOST_KEY_POLICY` (`--ssh-host-key-policy`): `tofu` in development and `approve` in production by default; `pinned` is allowed; `ca` is per profile only.

### Verification

- `internal/hostaccess/host_keys_test.go` (new), the real controller loop over SQLite, the Secret Store and SSH test servers:
  - TOFU pins every presented key (Ed25519 and ECDSA), then verifies, and audits `source=tofu`;
  - approve: a password profile under a `tofu` default waits with **0 authentication attempts**; operators are refused; approval needs the named fingerprints and the current spec generation, and succeeds after a status write lands in between;
  - a changed key gives `HostKeyChanged` with **0 authentication attempts**; trust is unchanged, approval without replace is refused, and an explicit replace works;
  - `pinned` makes **0 connections**;
  - `ca` verifies a certified host with no fingerprint;
  - profile validation.
- `TestLoadSSHHostKeyPolicy` (5 cases).
- `go test -race -count=3 -shuffle=on` on `hostaccess`, `controlmanager`, `service`, `store` and `sshclient`: pass. golangci-lint: only the 2 pre-existing issues.

## 2026-11-18 — Audit Phase 13A: SSH host-key scanning and multi-key verification (branch audit/host-keys)

- `pkg/sshclient/hostkeys.go` (new):
  - `ScanHostKeys` returns a host's keys, one per algorithm family (Ed25519, ECDSA, RSA). Each handshake stops right after the server proves its host key, **before authentication**, and the egress policy applies.
  - The host-key check accepts any fingerprint in `DialParams.TrustedFingerprints` (plus the old `ExpectedFingerprint`). When `HostCertAuthorities` is set, it also accepts host certificates signed by those CAs for the dialed host name. Any rejection is reported as `ErrHostKeyUnknown` with the presented fingerprint.
  - `ParseHostCertificateAuthorities` reads CA keys in authorized_keys format and refuses certificates.
- `pkg/sshclient/client.go`: uses the new check. The connection is now closed when setting the handshake deadline fails (it leaked before).
- `internal/testutil/sshtest`: options for an extra ECDSA host key and for a host certificate signed by a test CA; `Server.Fingerprints`.

### Verification

- `go test -race -count=3 ./pkg/sshclient/`: pass. New tests:
  - the scan returns every key and makes 0 authentication attempts;
  - the scan of a denied target is refused and never reaches the server;
  - a fingerprint list is accepted, and an unrelated one is refused with no authentication attempt;
  - a certificate from the trusted CA is accepted, while one from another CA and an uncertified host are refused; a pinned fingerprint still works next to a CA;
  - CA parsing.
- golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13: stable SSH and policy tests (Q-25, Q-15) (branch audit/tests-2)

- **Q-25, corrected diagnosis.** AUDIT/03 said encrypted SSH keys are decrypted "on every production connection". That is wrong: the connector never sets `DialParams.Passphrase`, so production keys are never passphrase-decrypted (the Secret Store already encrypts them at rest). The intermittent failure came only from the passphrase subtests in `pkg/sshclient`. There, bcrypt-pbkdf takes about 0.1 s normally and 1.3 s under `-race` (measured), far more under shuffled parallel runs, against a 5 s budget. Those subtests now get a 60 s budget, documented in the test. No signer cache was added: it would keep decrypted private keys in memory for no production benefit.
- **Q-15:** four sleeps that stood in for synchronisation now check the real condition:
  - `controlmanager/engine_test.go`: instead of sleeping 50 ms, it asserts that a key re-added while processing is not runnable (`queue.Len() == 0`), which is the guarantee itself;
  - `sshclient/dial_policy_test.go` and `netpolicy/policy_test.go`: instead of sleeping 50 ms, a sentinel connection proves no denied dial reached the listener (accepts are ordered);
  - `sshclient/client_integration_test.go`: cancellation now waits until the server holds the connection, and the time is measured from cancellation.
  The remaining sleeps in tests are poll intervals of deadline-bounded waits, or simulated durations.

### Verification

- `go test -race -count=8 -shuffle=on ./pkg/sshclient/`: pass. `go test -race -count=5 -shuffle=on` on `pkg/sshclient`, `pkg/netpolicy` and `internal/controlmanager`: pass. golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13: managed-resource use cases end to end (branch audit/tests-2)

- `internal/service/managed_flow_test.go` (new): the managed-resource service on real SQLite stores and a real Secret Store, with owner, operator and reader members. It covers:
  - Access Profiles: owner-only create, update and delete; a profile pointing at a missing secret is refused; readers see only the secret reference;
  - Hosts: create needs `inventory:write`; selector listing and an invalid selector; labels need the right revision and `inventory:write`;
  - Operations:
    - preview, then submit refused while execution is disabled, for readers, for a stale reviewed-target hash, for a rule matching nothing, for a rule that includes an unbound host, and for a program outside the allowlist;
    - a successful submit;
    - listing;
    - cancel (readers refused, a repeat cancel is idempotent);
  - deletion: revision checks; deleting a profile still used by a host gives `ErrConflict`, and it works once unused.

### Verification

- `go test -race -cover ./internal/service/`: **81.2%** (28.9% at the start of the phase). golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13: runtime configuration tests (branch audit/tests-2)

- `internal/runtimeconfig/service_test.go` (new, on a real SQLite repository):
  - publish and rollback by an owner, applied to the live snapshot;
  - refused for operators, readers and owner API tokens, and for stale revisions, restart-only changes and invalid configurations;
  - get, list, status and checksum;
  - **apply is all-or-nothing on prepare**: a participant that refuses leaves the snapshot unchanged, rolls back the others, and records `apply_failed`; the revision applies once every participant accepts it;
  - snapshots are copies;
  - the watcher applies a revision published by another process and stops cleanly on cancellation.
- `internal/transport/http/v1/configuration/handler_test.go` (new): every endpoint and status code against the real service, including strict decoding (unknown fields and trailing JSON are refused).
- Not covered on purpose: a commit failure rolls back every participant, including ones never committed (AUDIT/03 Q-16). That is a known bug scheduled for Phase 19, so no test locks it in.

### Verification

- `go test -race -cover`: `runtimeconfig` 13.3% → **78.2%**; `v1/configuration` 0% → **91.6%**. golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13: API token, secret and environment tests (branch audit/tests-2)

- `internal/service/api_token_service_test.go` (new):
  - tokens are `mch_pat_` plus a random value, and only their SHA-256 reaches the store;
  - the name is trimmed, the default lifetime is 30 days, and an explicit lifetime is honoured;
  - only owners can issue, list or revoke, and only `operator` or `reader` roles are accepted;
  - name and lifetime bounds are enforced;
  - list and revoke are scoped to the caller's user and workspace.
- `internal/transport/http/v1/secret/handler_test.go` (new), on a real SQLite secrets service:
  - **no response from any endpoint carries secret material**;
  - rotation needs `expectedVersion` (409 when stale);
  - delete retires the secret, keeping its metadata;
  - rewrap from an unknown key gives 503;
  - the service itself refuses operators, readers and owner API tokens (403), not only the router.
- `internal/transport/http/v1/tokens/handler_test.go` (new): the role is normalised; the stored hash is never returned; every error maps to its status, and causes are not leaked.
- `internal/transport/http/v1/environments/handler_test.go` (new): **every call uses the caller's workspace even when the body names another one**; every error status.

### Verification

- `go test -cover`: `v1/secret` 0% → **93.4%**; `v1/tokens` 0% → **100%**; `v1/environments` 0% → **100%**. golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13: login and session tests (branch audit/tests-2)

- `internal/service/auth_service_test.go` (new):
  - signup normalises the username and email, stores only a bcrypt hash, enforces the 12–72 byte password bounds, and refuses duplicates in any case;
  - login works by username or email in any case;
  - a wrong password and an unknown account give the same `ErrInvalidCredentials`, while a store failure does not;
  - session JWTs are HS256 with issuer `machinarchy`, carry the user and workspace claims, use the configured lifetime (24h when 0), and fail to verify with another secret;
  - `VerifySession`, and no session for a user without a workspace.
- `internal/transport/http/v1/auth/handler_test.go`:
  - the session cookie is HttpOnly, SameSite=Lax and `Path=/`, with `Secure` following production mode;
  - no cookie on any failure, and a store error is not leaked;
  - signup follows the runtime setting over the static one, with 409, 400 and 403 cases;
  - logout expires the cookie; `GET /me` cases.

### Verification

- `go test -cover`: `v1/auth` 12.1% → **93.1%**; `service` 28.9% → 38.5%. golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13: route authorization matrix (branch audit/tests-2)

- `internal/bootstrap/authz_matrix_test.go` (new): `TestRouteAuthorizationMatrix` builds the fully wired API server (`Runtime.NewAPIServer`: real SQLite stores, services, JWT and role middleware).
  - It creates an owner, an operator and a reader, plus reader and operator API tokens, through the API, and real resources so path IDs exist.
  - It calls every route documented in `/openapi.json` as each of the 6 principals: 401 for anonymous, 403 for a forbidden role, and anything else for an allowed one.
  - A documented route missing from the matrix, or a matrix entry that is not documented, fails the test.
- `internal/transport/http/server.go`: `Server.Handler()`, a test seam that returns the wired handler.

### Verification

- The test passes (59 routes × 6 principals).
- Mutation checks, reverted afterwards:
  - letting readers through the environments write group fails the test with 6 errors;
  - removing the owner check from `ManagedResourceService.CreateAccessProfile` fails it with 4 errors.
  - Removing `RequireHuman` from `/tokens` was **not** detected, because API tokens cannot hold the owner role that `tokens:manage` requires, so that check is redundant there. This is recorded rather than changed.
- golangci-lint on the changed packages: 0 issues.

## 2026-11-18 — Audit Phase 12: end-to-end test in NATS operator mode (branch audit/agent-authz)

- `test/e2e/agent_test.go`: the real monolith and the real agent now run against NATS in operator mode (`natstest`).
  - The gateway uses a controller user with `ControllerPermissions`, plus the account signing key; there is no shared enrolment token.
  - The owner issues a per-host token through `POST /api/v1/agents/enrollment-tokens`, bound to the machine's hostname.
  - The agent starts with the bootstrap credential.
  - New checks: a second agent with the same token is refused and exits 78; the token list shows the agent that used it and never the secret; the agent stored its NATS user JWT.
  - The earlier checks (signed calls, renewal, revocation with exit 78, metrics, SIGTERM) all now run over per-agent credentials.

### Verification

- `MACHINARCHY_E2E_REQUIRED=1 go test -race ./test/e2e/`: pass (20.3 s), against agent `996e8a3`.

## 2026-11-18 — Audit Phase 12: NATS operator-mode setup generator (branch audit/agent-authz)

- `deployments/nats/generate` (new, `go run ./deployments/nats/generate -out DIR`): creates the operator, the system account, the `MACHINARCHY` account with JetStream and a signing key, and two users:
  - `controller`, with `agentgateway.ControllerPermissions` and allow-responses;
  - `bootstrap`, with `agentv1.BootstrapPermissions`.
  It also writes `nats-server.conf` (memory resolver, TLS block to enable) and env snippets. All files are 0600, and it refuses to overwrite. Written in Go instead of an `nsc` script so it is tested; `nsc` was not available here (AUDIT M-10).
- `internal/agentgateway/natsjwt.go`: `ControllerPermissions()`, the single definition of the gateway's NATS reach. `natstest.ControllerCredentials` now takes permissions, so the gateway tests run with exactly those, not an unrestricted user.
- `docs/operations.md`: "NATS in operator mode", with the generated files and a permission table for `nsc` users.

### Verification

- `go test -race ./deployments/nats/generate/`: `TestGeneratedSetup` loads the generated config with `server.ProcessConfigFile`, starts it, and checks:
  - the controller can create the command stream and answer registrations;
  - bootstrap can send a registration request, but cannot read registrations or use the JetStream API;
  - the controller cannot read the default inbox;
  - the files are 0600, and a second run refuses to overwrite.
- `TestGatewayIssuesScopedNATSCredentials` passes with the restricted controller user.
- golangci-lint: only the 2 pre-existing issues. The first run found 4 unchecked `PublicKey` errors and a G302 on the directory chmod (now 0700 with a justified `#nosec`); both were fixed before committing.

## 2026-11-18 — Audit Phase 12: protocol contract test (branch audit/agent-authz)

- `internal/agentgateway/contract_test.go` (new): `TestProtocolGoldenFixtures` finds the protocol module (`go list -m`), and verifies every golden fixture in `agentv1/testdata` with the published test keys (controller seed 0x01, agent seed 0x02). Each fixture must also fail with the other party's key. It fails on a fixture it does not know, and on an expected fixture that is missing (AUDIT M-11).

### Verification

- `go test ./internal/agentgateway/ -run TestProtocolGoldenFixtures`: pass, against agent `fb5f243` (4 fixtures). golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 12: per-host, single-use agent enrolment tokens (branch audit/agent-authz)

**Behaviour change:** the shared `MACHINARCHY_AGENT_GATEWAY_ENROLLMENT_TOKEN_FILE` is now optional and refused in production. Hosts enrol with owner-issued tokens.

- Migration `000002_agent_enrollment_tokens` (both dialects): stores the token hash, an optional hostname, the issuer, the expiry, and the usage and revocation fields.
- `internal/agentgateway/enrollment.go` (new):
  - `NewEnrollmentToken`: `mae_` plus 32 random bytes, SHA-256 hash only, 1h default lifetime (1m–24h), optional hostname normalised to lower case without a trailing dot;
  - the `Enrollment` and `TokenStore` types;
  - `ErrEnrollmentRejected`.
- `Store.RegisterAgent` now takes the enrolment (AUDIT M-4):
  - agents are looked up by public key only (before: `request_id OR public_key`);
  - a retry of the same request needs no new token;
  - anything else consumes a valid token in the same transaction (the UPDATE re-checks `used_at`, so two registrations cannot share one token);
  - the `agent.registered` audit event records `enrollment` and `token_id`.
- The gateway hashes the presented token for the store, and accepts the shared token only if configured.
- `internal/store/agents`: the token store (create, list, revoke unused), with audit events `agent.enrollment_token_issued` and `agent.enrollment_token_revoked`.
- API (owner and human only), with views that never include the secret or its hash:
  - `POST /api/v1/agents/enrollment-tokens` (returns the secret once);
  - `GET /api/v1/agents/enrollment-tokens`;
  - `DELETE /api/v1/agents/enrollment-tokens/{id}`.
- OpenAPI regenerated. Docs: "Enrolling a host" in `docs/operations.md`, the runbook row, the README route list.

### Verification

- `go test -race ./internal/agentgateway/` (`TestPerHostEnrollmentTokens`, `TestNoSharedTokenAcceptsOnlyIssuedTokens`, `TestNewEnrollmentToken`), `./internal/transport/http/v1/agents/` (new `TestEnrollmentTokenEndpoints`) and `./internal/config/`: pass.
- Store contract `agents` (rewritten: single use, retry, hostname binding, expiry, revocation, no enrolment, shared token, audit details): pass on SQLite and on **PostgreSQL 16**. `migrations are idempotent` now expects 2 migrations.
- `make api-docs`; the route-coverage test passes. golangci-lint `./internal/...`: only the 2 pre-existing issues. gosec flagged the name `tokenColumns` as a hardcoded credential (false positive); it was renamed.

## 2026-11-18 — Audit Phase 12: per-agent NATS credentials from the gateway (branch audit/agent-authz)

**Behaviour change:** in production, the agent gateway now requires `MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT` and `_NATS_ACCOUNT_SIGNING_KEY_FILE` (NATS operator mode).

- `internal/agentgateway/natsjwt.go` (new): `NATSIssuer`. Each agent gets a NATS user JWT whose subject is its Ed25519 identity key as a user nkey and whose permissions are `agentv1.AgentPermissions(agentID)`. It is signed with an account signing key, with a lifetime of 1h by default (5m–24h, at least twice the heartbeat) (AUDIT M-10).
- `internal/agentgateway/gateway.go`: register and renew responses carry the JWT when an issuer is configured. A revoked or unknown agent gets none. The gateway uses the `_INBOX_CONTROLLER` inbox prefix, so enrolling agents cannot read its replies.
- `internal/config/config.go`: `MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT`, `_NATS_ACCOUNT_SIGNING_KEY_FILE` (0600, must be an account seed) and `_AGENT_JWT_TTL`. Both keys go together, and production requires them. `internal/bootstrap/agents.go` wires the issuer and logs whether it is on.
- `internal/testutil/natstest` (new): embedded NATS in operator mode (operator, system account, JetStream account with a signing key) for tests.
- `go.mod`: `nats-io/jwt/v2` and `nkeys` become direct requirements (they were already in the graph; same versions).
- `docs/operations.md`, `.env.example`: the three settings.

### Verification

- `go test -race ./internal/agentgateway/ ./internal/config/`: pass.
  - New `TestGatewayIssuesScopedNATSCredentials` (operator-mode NATS). The JWT subject is the agent key, the issuer account and permissions are exactly as specified, and the expiry is about the lifetime. With the JWT, the agent renews over its own connection and binds its own consumer, but cannot subscribe to registrations. A revoked agent gets no JWT.
  - New `TestNATSIssuerValidation` (6 cases), plus 9 new config cases and a production case.
- `MACHINARCHY_E2E_REQUIRED=1 go test -race ./test/e2e/`: pass, in development mode without JWT issuing, against agent `996e8a3`.
- golangci-lint on the changed packages: 0 issues.

## 2026-11-18 — Audit Phase 11: document invoke results (branch audit/http-hardening)

- `internal/transport/http/v1/agents/handler.go`: the OpenAPI description of `POST /api/v1/agents/{id}/invoke` now says that 200 means the agent answered and the body's `status` must be checked. `rejected` and `failed` are agent answers and return 200; 503 and 504 mean there was no answer. 504 is now listed. Behaviour is unchanged (decision for AUDIT/09 F9-05: keep 200, document it).
- `docs/swagger.json` and `docs/swagger.yaml` regenerated with swag v1.16.4; only the invoke entry changed.

### Verification

- `make api-docs`; `go test ./internal/transport/http/v1/` (route-coverage test): pass. golangci-lint on the agents handler: 0 issues.

## 2026-11-18 — Audit Phase 11: remove unreachable code (branch audit/http-hardening)

Removed everything `deadcode -test ./...` reported as unreachable from any program or test (AUDIT/09 F9-04 and §1.3):
- `Runtime.Metrics` (`internal/bootstrap/runtime.go`), never called;
- the `Gateway.nc` field (`internal/agentgateway/gateway.go`), written but never read;
- `SnapshotManager.Check` (`internal/runtimeconfig/config.go`), never called;
- `eventing.Command`, `CommandMetadata` and `HostSpecUpdateCommand` (`internal/eventing/contracts.go`), used only by their own test, which is removed too. Host spec changes go through `ManagedResourceService` and the outbox events.

### Verification

- `deadcode -test ./...` (x/tools v0.52.0): no findings in machinarchy (before: 4).
- `go test -race` for `eventing`, `bootstrap`, `agentgateway` and `runtimeconfig`: pass. golangci-lint on those packages: 0 issues.

## 2026-11-18 — Audit Phase 11: CLI refuses tokens over plain HTTP (branch audit/http-hardening)

**Behaviour change:** a CLI context whose server is `http://` on a non-loopback host now fails with a usage error until the cluster is re-added with `--insecure-http`.

- `internal/cli/config/config.go`: `checkTransport` and `IsLoopbackHost`. `Resolve` and `SetCluster` refuse plain `http://` to anything but `localhost`, `*.localhost` or a loopback IP. `Cluster.InsecureHTTP` (`insecure-http` in the config file) is the per-cluster opt-in. It does not apply to a server overridden by flag or environment (AUDIT S-18).
- `internal/cli/commands/config.go`: `config set-cluster --insecure-http`.
- Tests that used `http://*.example` servers for precedence checks now use `https://`; `docs/cli/README.md` documents the rule.

### Verification

- `go test ./internal/cli/...`: pass. New tests: `TestPlainHTTPTokenTransport` (10 cases, including an override bypass attempt and a `localhost.example.test` look-alike), `TestSetClusterRequiresOptInForRemoteHTTP`, and two command-table cases. With the guard disabled, both new config tests fail.
- golangci-lint `./internal/cli/...`: only the 2 pre-existing issues (`client_test.go` unused parameter, `APIError` stutter).

## 2026-11-18 — Audit Phase 11: API docs and metrics off the public listener (branch audit/http-hardening)

**Behaviour change:** in production, `/openapi.json`, `/scalar` and `/scalar/init.js` are no longer served unless `MACHINARCHY_API_DOCS_ENABLED=true`.

- `internal/config/config.go`: new `MACHINARCHY_API_DOCS_ENABLED` (`--api-docs-enabled`; default `true` in development, `false` in production) and `MACHINARCHY_METRICS_LISTEN_ADDR` (`--metrics-listen-addr`; must be host:port on a different port from the API).
- `internal/transport/http/server.go`: `registerOperationalRoutes` serves health always, `/metrics` only when there is no separate metrics listener, and the docs only when enabled. `useMetricsListener` binds a second listener that serves `/metrics` and nothing else. It is bound before readiness is signalled and shut down within the same budget as the API listener (AUDIT S-17).
- `docs/operations.md`, `README.md`, `.env.example`: the two settings.

### Verification

- `go test -race ./internal/transport/http/ ./internal/config/`: pass. New tests: `TestOperationalRoutesFollowConfiguration` (4 combinations), `TestMetricsListenerServesOnlyMetrics` (serves metrics, 404 for anything else, closed after shutdown) and `TestLoadOperationalEndpoints` (8 cases).
- golangci-lint on the changed packages: 0 issues. The first commit attempt had 2 (an unclosed response body in a test, and an unchecked `SplitHostPort`); both were fixed before amending.
- Manual: a production monolith on SQLite with `MACHINARCHY_METRICS_LISTEN_ADDR=127.0.0.1:19090`:
  - the API port answered `/healthz/ready` 200, and `/metrics`, `/openapi.json` and `/scalar` 404;
  - `/api/v1/me` returned 401 with HSTS, `no-store` and the deny-all CSP;
  - the metrics port served `/metrics` 200 and `/healthz/live` 404;
  - SIGTERM: exit 0, and both ports closed.

## 2026-11-18 — Audit Phase 11: security headers and API reference policy (branch audit/http-hardening)

- `internal/transport/http/middleware/headers.go` (new): `SecurityHeaders` sets `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, same-origin COOP and CORP, and a deny-all CSP on every response. HSTS is added in production only. `/api` responses add `Cache-Control: no-store`. It is used by the API server and by the operations server (AUDIT S-16).
- `internal/transport/http/scalar.go`: the reference page has its own CSP (scripts only from this origin and the pinned `@scalar/api-reference@1.75.0` file, no inline scripts). The init code moved to `/scalar/init.js`, and Scalar's telemetry and web fonts (`fonts.scalar.com`) are off (AUDIT S-11). SRI is not added: computing the hash needs the file, and fetching it was out of scope.
- `docs/operations.md`: new "Security headers" section.

### Verification

- `go test ./internal/transport/http/ ./internal/transport/http/middleware/`: pass (new `TestSecurityHeaders`, `TestSecurityHeadersCanBeOverriddenByHandler`, `TestScalarReferencePolicy`).
- golangci-lint on `internal/transport/http/...`: 0 issues.
- Manual: a development monolith served `/scalar` in the browser pane. The reference rendered the spec, and the console showed no CSP violations once the web fonts were disabled. Before that, only the 14 font loads from `fonts.scalar.com` were blocked.

## 2026-11-18 — Audit follow-up: e2e requires exit 78 from a revoked agent (branch audit/followup)

- `test/e2e/agent_test.go`: after revocation the test now waits for the agent to exit by itself and requires exit status 78 (machinarchy-agent `5ec5beb`, AUDIT/09 F9-02). Before, it accepted any exit, and usually stopped the agent with SIGTERM before it noticed. **CI's `MACHINARCHY_AGENT_REF` must point at an agent revision that includes `5ec5beb`.**
- `docs/runbook.md`: the revoked-agent row mentions exit 78 and the restart policy.

Verification: `MACHINARCHY_E2E_REQUIRED=1 go test -race -count=2 ./test/e2e/` passes with the current agent; against agent `a7aa03c` it fails with "revoked agent exited with exit status 1, want exit status 78".

## 2026-11-18 — Audit follow-up: govulncheck gate with reviewed allowlist (branch audit/followup)

- `scripts/security/govulncheck.sh` (new): runs `govulncheck -format json` and fails (exit 3) on any vulnerability the code calls, unless `scripts/security/govulncheck-allowlist.txt` has an unexpired entry for it and no fixed version exists. It notes allowlist entries that no longer match.
- `scripts/security/govulncheck-allowlist.txt` (new): accepts GO-2026-6875 and GO-2026-6872 (Echo v4.16.4, no fix; not exploitable here, AUDIT/02 S-22) until 2027-02-16.
- `make vuln` and the CI `security` job use the script. Before, CI ran plain `govulncheck ./...`, which exits 3 on these two advisories, and `orchestrate-release` needs `security`, so every release was blocked (AUDIT/09 F9-01).
- `docs/SECURITY.md` explains how to accept an advisory; `.github/WORKFLOWS.md` and `README.md` updated.

### Verification

- `scripts/security/govulncheck.sh` against the real module: exit 0, both advisories reported as accepted.
- Failure paths: empty allowlist → exit 3 for both; an expired entry → exit 3, and a stale entry is noted; a fake report with a fixed version → exit 3 ("upgrade the module").
- `shellcheck` clean. `make vuln`: exit 0; with `GOVULNCHECK_ALLOWLIST=/dev/null` it fails. `ci.yml` parses as YAML.
- Not verified: the workflow on GitHub (nothing pushed).

## 2026-11-18 — Audit Phase 8: tests, CI/CD and operations (branch audit/ops; recorded late)

This entry was missing when Phase 8 was committed; it is added here and summarizes those commits (`99b32db`..`d632af4`).

- **Agent gateway** (`internal/agentgateway`, `internal/store/agents`, `agents` table in both baselines, `/api/v1/agents` routes, `MACHINARCHY_AGENT_GATEWAY_*` settings): controller side of `protocol/agentv1`, imported from `../machinarchy-agent/protocol` through `replace`. Runs in the Control Manager and monolith roles. Synchronous capability calls only; no Operation integration.
- **Fixes found by new tests:** `globMatch` with `*` in values (fuzz); per-host timeouts recorded as `TimedOut`/`timeout`; secret rotation keeps the stored name; a revoked agent re-enrolling gets `enrollment_rejected`; the image's `/data` is `0700`.
- **Tests:** agent end-to-end test (`test/e2e`), 7 fuzz targets, operation worker, secrets, JWT middleware and gateway tests.
- **Runtime:** Control Manager, operation worker and gateway Prometheus metrics; 25 s API request deadline; `healthcheck` subcommand in the server binaries.
- **Build and CI:** digest-pinned multi-stage images with healthchecks and allowlist `.dockerignore`; reproducible ldflags builds; `ci.yml` rewritten (verify, security, images, release) with the agent repository checked out alongside.
- **Docs:** README, `docs/operations.md`, new `docs/runbook.md`, `.env.example`.

Verification (run at the time and repeated in AUDIT/09 on a clean clone): build, vet, `go test -race ./...` (33 packages, including the end-to-end test) and PostgreSQL 16 contract suites pass; lint has the 2 pre-existing issues; gosec 0; coverage 48.4%. CI never ran on GitHub.

## 2026-11-18 — Audit Phase 7: cleanup

**Recreate existing databases.** The schema is now one baseline per dialect; older databases are refused at startup.

- The legacy host, run, fleet, job and metrics-history stack is deleted (API, services, stores, tables). The managed API is complete (list/delete/labels, Access Profile reads, operation lists, target preview bound by a hash) and the CLI uses it: `get hosts|profiles|operations`, `create|delete|label host`, `run`, `cancel operation`.
- One store implementation per area for SQLite and PostgreSQL (`store/accounts`, `store/resources`, `store/management`, helpers in `store/sqlx`); PostgreSQL uses pgx.
- One error format, `{"error", "code"}`, and one list format, `{"items", "nextCursor"}`. The OpenAPI spec covers every route and a test keeps it that way.
- Destructive CLI commands ask for confirmation (`--yes`, `--no-input`).
- `make smoke` drives the CLI against a fresh monolith. ADRs are merged into `docs/adr` (ADR-055 records the managed-only model); phase documents moved to `docs/archive`.

## 2026-11-17 — Makefile refactor

### Changes in this update

- Restructured the developer Makefile into five topic files: `mk/config.mk`, `mk/build.mk`, `mk/checks.mk`, `mk/containers.mk` and `mk/help.mk`. These replace `go.mk`, `quality.mk`, `test.mk`, `security.mk`, `compose.mk` and `ssh.mk`. Removed the stale `mk/README.md`.
- **Fixed `make build` and `build-release`.** They no longer build the deleted `cmd/agent`. Removed `build-agent` and `run-agent`.
- Version metadata (`VERSION`, `COMMIT`, `BUILD_TIME`) is computed once per invocation and can be overridden.
- Go linker flags moved to `GO_LDFLAGS`, so an exported C `LDFLAGS` can no longer leak into Go builds.
- `build-release` uses target-specific variables instead of a recursive `make`.
- Shared helpers (`go_build`, `run_with_env`, `require_tool`) replace repeated recipes.
- **`run-*` targets take extra arguments through `ARGS="..."`.** The previous trick that turned extra command-line words into no-op targets is gone: it could silently trigger real targets such as `build`. The env file is now sourced by absolute path.
- Added `tidy-check` (`go mod verify` + `go mod tidy -diff`) and included it in `verify` and `verify-full`.
- `lint` no longer re-runs `fmt-check`; golangci-lint already enforces gofmt.
- `verify-full` uses `test-race` instead of running the suite twice.
- `help` now shows usage and common variables.
- Updated `docs/development/getting-started.md`.

### Verification

- All 57 targets shared with the previous Makefile were dry-run (`make -n`) and diffed against it. 34 are identical. The differences in the rest are the intended ones listed above.
- Ran successfully: `make -j4 build`; `make build-release VERSION=1.0.0` (binary reports 1.0.0 and was built with `-trimpath`); `make build-cli VERSION=9.9.9-test`; `make run-cli ARGS=version`; `make vet tidy-check`; `make test` (29 packages ok); `make clean`.
- `make verify`, `make shellcheck` and `make vuln` run correctly but exit non-zero on findings that predate this change: 4 golangci-lint issues, shellcheck errors in `scripts/dev/setup-local.sh` and `scripts/usecases/run.sh`, and the Echo advisories GO-2026-6875/6872. These are tracked in `AUDIT/00` and `AUDIT/02`.
- Tested with GNU Make 3.81, the macOS default.

## 2026-11-14 — CLI foundation and API client (Phase 2)

### Changes in this update

- Added `internal/cli/clierr`: classified CLI errors with stable exit codes (0 ok, 1 failure, 2 usage/config/input, 3 auth, 4 API/network/timeout, 6 not found, 7 conflict, 130 interrupted; 5 reserved for wait deadlines) and remediation hints.
- Reworked `internal/cli/api/client.go` around one request path: bearer authentication, `Accept`/`User-Agent` headers applied in a single place; configurable per-request timeout (`--request-timeout`, default 30s); caller-context cancellation; response bodies always closed; bounded success and error bodies; escaped path segments preserved (an identifier containing `/` can no longer change the route; dot and empty segments are rejected); redirects are no longer followed so the token cannot be replayed elsewhere; 202/204 empty bodies handled; token validated before use. Resource methods and the `Client`/`ManagementClient` interfaces are unchanged.
- Added `api.APIError` (status, sanitised message, request ID, `Retry-After`, structured fields) with status-to-exit-code mapping and hints; transport failures now report the cause without repeating the request URL.
- `internal/cli/config`: the YAML document is parsed directly (Viper now only resolves flag/environment overrides), so context, cluster and user names keep their exact case and dots; config errors are classified and carry hints; added request-timeout, quiet, no-headers and no-input settings, an explicit-output accessor, `LoadSettings` (no file read), `ValidateToken`, size-bounded reads, and a stderr warning (never fatal) when a config file containing tokens is readable by other users. `--yes` is never read from the environment or config file. Undocumented top-level preference keys in the YAML file (`output`, `server`, ...) are no longer honoured; the documented format is unchanged.
- `internal/cli/output`: added a shared `Printer` (table, JSON, YAML, `--no-headers`, `--quiet`); `PrintHosts`/`PrintHost` remain and produce identical output.
- `internal/cli/factory.go`: `Printer`, `OutputFormat` (per-command default), `Confirm`/`RequireConfirmation` (never prompts without a terminal or with `--no-input`), config warnings on stderr.
- `internal/cli/root.go`, `run.go`, `cmd/machinarchy/main.go`: added global flags `--server`, `--request-timeout`, `--quiet/-q`, `--no-headers`, `--no-input`, `--yes/-y`; `--output` now defaults to "per command" instead of a fixed `table` (existing behaviour preserved); global settings are validated before any command runs; duplicate or reserved command names panic at registration; `help`, `completion`, `version` and group help no longer read the config file; command groups are strict: an unknown subcommand (for example `secret lst`) now exits 2 instead of printing help and exiting 0; `cli.Run` owns error presentation (`Error:` / `Hint:`), maps errors to exit codes, and Ctrl-C now exits 130 (previously 0).
- Commands: `get hosts` uses the shared printer and classified errors (host/environment not found = 6, ambiguous = 2); `config` and `runtime-config`/`secret` input errors are usage errors (2); `writeJSON` tolerates an empty body; added `machinarchy version`.
- Tests: new unit tests for clierr, the API client (headers, serialization, error classification, redirects, cancellation, timeouts, body closure, size limits, path escaping), config (names, precedence, permissions, errors), output, factory/confirmation, root/exit codes, command registration/help/compatibility, `get hosts`, `config` end to end, and management commands against a real HTTP test server; plus an ADR-035 guard test that fails if CLI code imports application, database or transport packages or uses `net/http` outside `internal/cli/api`.
- Docs: `docs/cli/configuration.md`, `commands.md`, `troubleshooting.md` updated (global flags, precedence, environment variables, exit codes, command groups, `version`, config-file security). Corrected the documented default config path to `.machinarchy/config.yaml` to match the code.

### Validation

- **Not compiled and not run.** The authoring environment had no Go toolchain and no network access, so `go build`, `go vet`, `gofmt`, `golangci-lint` and `go test` were NOT executed. All code and tests were checked by reading and by a delimiter-balance script only; compile and test failures are possible and must be checked with `go build ./... && go test ./internal/cli/... ./cmd/...` before merging.
- Behaviour that depends on Cobra or Viper internals (group typo handling, flag/environment precedence, `help` skipping the config file) was reasoned from their documented behaviour, not observed.

## 2026-11-12 — Refresh implemented API and CLI use cases

### Changes in this update

- Rewrote `docs/examples/example-usecases.md` against the current API, CLI, managed Host/Access Profile/Operation, legacy Host/Run, runtime configuration, and Secret Store implementations. Clarified the distinct legacy and managed workflows, corrected endpoint and response semantics, and documented current limitations instead of planned functionality.

### Validation

- Cross-checked documented routes, payload fields, CLI commands, authorization requirements, and lifecycle behavior against handlers, services, runtime composition, and CLI command registrations.
- `bash -n` over all 26 shell examples and `git diff --check` — passed.
- `GOCACHE=/tmp/machinarchy-doc-gocache go test -count=1 ./internal/cli/...` and `GOCACHE=/tmp/machinarchy-doc-gocache go run ./cmd/machinarchy --help` — passed. No live API calls or full application tests were run; the examples require a configured Machinarchy service and credentials.

## 2026-11-12 — Phase H managed Host access and Operations

### Changes in this update

- Completed the managed Host Access Profile path from approved ADR-054: workspace-bound immutable profile revisions, SQLite/PostgreSQL migrations, binding-aware Secret Store authorization, Host-owned endpoint/trust, and durable profile-change notifications that fan out reconciliation.
- Added the connector-neutral HostConnector and Go SSH adapter with pinned host-key verification, version-pinned SecretRef resolution, bounded connections, context deadlines, argv quoting, separate bounded stdout/stderr capture, safe error classes, and cancellation cleanup. Wired managed Host reconciliation into Control Manager and canonical managed Host/Profile/Operation HTTP application routes.
- Added a separate bounded Operation worker with durable outbox scheduling and level scans, authorization/policy checks, cancellation, encrypted persisted results, PostgreSQL lease fencing, and restart recovery that marks uncertain executions Interrupted rather than replaying commands. Optimistic conflicts during cancellation, target updates, and finalization reload and retry; Host deletion cancels active Operations and waits for terminal state before removal.
- Changed SSH egress defaults to deny private/loopback ranges and all TCP ports unless explicitly configured. Added bootstrap and hot-reloadable runtime port allowlists; updated legacy test fixtures to opt in where they intentionally use private LAN addresses and ephemeral test ports. Updated architecture, security, environment-example, ADR-054, and Phase H documentation.

### Validation

- `GOCACHE=/tmp/machinarchy-go-cache go test -count=1 -timeout 90s ./internal/domain/resource ./internal/secrets ./internal/hostaccess ./internal/operationworker ./internal/controlmanager ./internal/store ./internal/service ./internal/bootstrap ./internal/transport/http/v1/managedresource ./pkg/netpolicy ./pkg/sshclient` — passed.
- `GOCACHE=/tmp/machinarchy-go-cache go test -count=1 -run '^TestManagedHostAndOperationEndToEnd$' -v -timeout 30s ./internal/hostaccess` with local-listener permission — passed, covering registration, persistence/outbox, SSH reconciliation, Operation execution/result, cancellation, restart recovery, and Host deletion.
- `GOCACHE=/tmp/machinarchy-go-cache go test -race -count=3 -run '^TestManagedHostAndOperationEndToEnd$' -timeout 2m ./internal/hostaccess` with local-listener permission — passed, including long-running command and cancellation conflict coverage.
- `GOCACHE=/tmp/machinarchy-go-cache go test -count=1 -timeout 2m ./...` with local-listener permission — passed. The same command without that permission failed only at the existing HTTP listener bind test because this sandbox disallows socket binding.
- `git diff --check` — passed. PostgreSQL integration was not run: `MACHINARCHY_TEST_POSTGRES_URL` is unset and Docker daemon access is denied. Lint checks were not run, per the current instruction to stop focusing on linters.

### Known limitations

- PostgreSQL migration, transaction, and multi-manager lease behavior still require integration validation against a live PostgreSQL service. Deployments must mirror application CIDR/port policy in an independent egress firewall. Encrypted private-key passphrases and automatic retry of uncertain remote commands are unsupported. Existing legacy Host and Run records are preserved and are not automatically migrated into managed Host resources.

## 2026-11-12 — Phase H.2 Access Profile domain and persistence foundation

### Changes in this update

- Added workspace-owned Access Profile domain/revision types, optimistic head update and repository contract; added SQLite/PostgreSQL migration 000011 for profile heads/revisions and Host lookup indexing.
- Added transactional profile create/revision, audit and outbox event persistence, workspace-bound Host profile validation, tombstone deletion checks, and a binding-aware Secret Store authorizer for Control Manager and Operation worker contexts.
- Added SQLite resource contract coverage for immutable revisions, stale update conflict, Host reference lookup, cross-workspace assignment rejection, referenced-profile deletion conflict, and profile outbox events.

### Validation

- `GOCACHE=/tmp/machinarchy-go-cache go test -count=1 -timeout 60s ./internal/store -run '^TestManagedResourceContract/sqlite/Access_Profile_revision_isolation_and_outbox$'` — passed.
- Initial broader focused run exposed the outbox schema requirement that subject generation be positive; profile events now use their revision as generation. PostgreSQL integration was skipped because `MACHINARCHY_TEST_POSTGRES_URL` is unset. Full H.2 validation remains incomplete.

## 2026-11-12 — Phase H scoped Secret Store resolution boundary

### Changes in this update

- Replaced purpose-only secret resolution with a typed workload/resource request and required an injected authorization port before any secret lookup or decryption.
- Configured bootstrap with a deny-all resolver until the subsequent Access Profile milestone supplies a binding-aware authorizer; updated Secret Store tests to use explicit authorization fixtures.
- Added a narrow `.gitignore` exception so the `internal/secrets` source package and this change appear in repository status/reviews.

### Validation

- `gofmt`, `git diff --check`, `GOCACHE=/tmp/machinarchy-go-cache go test -count=1 -timeout 60s ./internal/store/management ./internal/bootstrap` — passed. Lint checks were not run, consistent with the current work-session instruction to stop focusing on linters.

## 2026-11-12 — Proposed Phase H Access Profile ADR

### Changes in this update

- Added proposed ADR-054 covering workspace-scoped reusable Access Profiles, immutable revisions, Host/Operation binding, scoped Secret Store authorization required by ADR-038, deletion/disable behavior, outbox fan-out, and SQLite/PostgreSQL persistence implications.
- The ADR is explicitly proposed and requires owner approval; no implementation or migrations were changed.

### Validation

- Reviewed existing Host/Operation models, Secret Store contracts, ADR-028, ADR-038, and Phase 2/7 architecture documents. No tests were run because this update contains no code changes.

## 2026-11-12 — Phase G bounded KEK rewrapping

### Changes in this update

- Added migration 000010 to version secret value AAD while retaining the original AAD key ID for legacy ciphertext.
- Added resumable, owner-authorized batch rewrapping of stored DEKs to the active external KEK. Rewrapping leaves secret ciphertext and immutable version identifiers unchanged; each successful update is audited without key material or plaintext.
- Exposed rewrapping through the Secret API and CLI and documented the staged keyring/restart/rewrap/removal procedure.

### Validation

- `gofmt` and focused `go test -count=1 -timeout 60s ./internal/store/management ./internal/runtimeconfig ./internal/transport/http/v1/secret ./internal/cli/api ./internal/cli/commands` — passed, including bounded-batch resume, authorization, and legacy ciphertext compatibility.
- `go test -count=1 -timeout 2m ./...` — failed at `internal/transport/http/TestOperationsServerStartsAndShutsDown` waiting for its listener; other reported packages passed. PostgreSQL integration remains unavailable because no test URL is configured and Docker is unavailable. Lint checks were not run as directed.

## 2026-11-12 — Phase G runtime retry and key handling hardening

### Changes in this update

- Bounded configuration notification polling to one second and added capped exponential retry backoff after database or apply failures, avoiding rapid repeated reload attempts while preserving periodic recovery checks.
- Wipe temporary KEK copies after Secret Store use without modifying key-provider-owned buffers; added regression coverage for key ownership and retry backoff bounds.

### Validation

- `gofmt`, `git diff --check`, `go test -count=1 -timeout 60s ./internal/runtimeconfig ./internal/store/management`, focused API/CLI/bootstrap/config package tests — passed.
- `go test -count=1 -timeout 2m ./...` — failed at the existing `internal/transport/http/TestOperationsServerStartsAndShutsDown` listener test; the other reported packages passed. No HTTP code was changed in this update.
- PostgreSQL integration validation — blocked: `MACHINARCHY_TEST_POSTGRES_URL` is unset and the Docker daemon is unavailable. Lint checks were not run as directed for this work session.

## 2026-11-12 — Phase G configuration/secrets and bootstrap lifecycle verification

### Changes in this update

- Implemented global typed runtime configuration revisions, compare-and-swap updates, history/rollback, SQL outbox hints, process application status, atomic snapshots, periodic checks, and safe hot-reload of logging, SSH target policy, operation admission policy, and signup behavior. Restart-only worker/pool settings are reported as restart-required and rejected when changed at runtime.
- Added external bootstrap key/keyring file loading, encrypted versioned Secret Store records using per-version DEKs and AES-GCM authenticated data, owner/human authorization, metadata/audit endpoints, rotation, retirement, and CLI management through the API. Plaintext is write-only at the API and CLI.
- Fixed bootstrap lifecycle validation so non-cancellable contexts are rejected before component startup; lifecycle tests use explicit cancellation contexts.

### Validation

- Bootstrap regression test and full bootstrap package tests — passed.
- `go test -count=1 -timeout 2m ./...` — failed only at `internal/transport/http/TestOperationsServerStartsAndShutsDown` because its listener did not bind in this environment; all other listed packages passed.
- PostgreSQL test script — blocked because Docker cannot connect to the local daemon. PostgreSQL behavior remains unverified.
- `golangci-lint run` — not completed successfully; Go module cache writes under `/Users/mleus/go/pkg/mod/cache` are denied and package loading fails. No dependency files or lint rules were changed.
- Full Phase G docs/changelog completion is recorded here; configuration examples and user-facing documentation updated separately in this change.

Project change history maintained alongside the code. Entries are newest first. Each entry distinguishes changes made in that update from the implementation baseline and records only validation that was actually performed.

## 2026-11-11 — Phase F event infrastructure and control loop

### Changes in this update

- Added versioned command/event envelopes, transactional Host outbox records, per-consumer delivery claims, retry/quarantine state, retention, and PostgreSQL resource fencing/resync leases. SQLite remains the single-Control-Manager profile; PostgreSQL coordination uses database-backed claims and leases.
- Replaced the Control Manager's new resource path with an outbox dispatcher, deduplicating bounded resource-key queue, bounded workers, retries with jitter and global rate limiting, startup/periodic resynchronization, latest-state controller reads, and graceful drain/cancellation. Imperative Operation execution remains separate.
- Added a default Host controller that handles logical deletion and records `ConnectorUnavailable` for managed Hosts until a connector is wired. Existing legacy API/NATS Host flows are not dual-written to the canonical resource store and therefore are not reconciled by this path.
- Added migration 000008 for SQLite and PostgreSQL, cross-engine outbox/coordination contracts, lifecycle and failure tests, and Phase F execution/sequence documentation. No NATS or Kafka dependency was introduced.

### Validation

- `gofmt`, `git diff --check`, focused Phase F package tests, `go test -race ./internal/controlmanager ./internal/eventing ./internal/store ./internal/bootstrap ./internal/config`, and `go vet ./...` — passed.
- `go test ./...` — all listed packages passed except `internal/transport/http/TestOperationsServerStartsAndShutsDown`, which failed waiting for its listener to bind; no HTTP code was changed in this phase.
- PostgreSQL integration tests were skipped because `MACHINARCHY_TEST_POSTGRES_URL` was not configured. Benchmarks and security checks were not run.

### Known limitations

- The initial Host controller does not perform remote convergence; it reports unavailable connector capability and retries. The legacy API Host model is not migrated to or synchronized with canonical resource records. Multi-replica PostgreSQL coordination is implemented but was not integration-tested against a live PostgreSQL instance.

## 2026-11-11 — Phase E domain and persistence foundation

### Changes in this update

- Added connector-neutral managed Host and Operation domain types with UUID identity, desired/observed state, generation/revision semantics, conditions, validation, and lifecycle transitions. Kept the existing SSH-shaped Host and Run APIs intact.
- Added shared resource repository ports and SQLite/PostgreSQL persistence adapters, transactional audit writes, optimistic revision checks, encrypted Operation specifications and target output, target snapshots/results, workspace-safe environment references, indexes, and migration 000007 for each engine.
- Added domain and cross-engine repository contract tests for resource creation/read/update/deletion, generation and status updates, concurrent stale-write rejection, Operation lifecycle/encryption, rollback, and migrations. The canonical tables are additive; legacy `hosts` and `runs` data are not migrated or redirected, and event/outbox publishing remains out of scope.

### Validation

- `gofmt` — passed for changed Go files.
- `GOCACHE=/tmp/machinarchy-go-cache go test ./internal/domain/resource ./internal/store` — passed. SQLite migration and repository contracts ran; PostgreSQL contract tests were skipped because `MACHINARCHY_TEST_POSTGRES_URL` was not configured.
- `GOCACHE=/tmp/machinarchy-go-cache go test -race ./internal/domain/resource ./internal/store`, `go vet ./...`, and `git diff --check` — passed.
- `GOCACHE=/tmp/machinarchy-go-cache go test ./...` — all other packages passed, but `internal/transport/http/TestOperationsServerStartsAndShutsDown` failed waiting for its listener to bind. The same isolated test failed; no HTTP code was changed. Benchmarks and security checks were not run.

## 2026-11-11 — Phase D repository foundation and runtime composition

### Changes in this update

- Centralized API, Control Manager, and all-in-one process construction in `internal/bootstrap`; command entry points now load config, create signal contexts, and select a runtime role. All-in-one composes the existing API and worker components over one runtime.
- Added shared structured logger/telemetry initialization, listener-ready startup, bounded HTTP/worker shutdown, startup-context propagation, and propagation of runtime cleanup failures. Added broker URL validation with a setting-specific error.
- Updated runtime architecture documentation and added lifecycle/configuration tests. The Agent remains an explicit placeholder; no Agent runtime was added. No domain, API behavior, persistence schema, broker behavior, or security policy was changed.

### Validation

- `gofmt` and `git diff --check` — passed.
- `go test ./...` — passed.
- `go test -race ./internal/bootstrap ./internal/config ./internal/transport/http` — passed.
- `go vet ./...` — passed.
- `go build ./cmd/api ./cmd/controlmanager ./cmd/monolit ./cmd/machinarchy ./cmd/agent` — passed.
- `go run ./cmd/machinarchy --help` — passed. Runtime lifecycle tests cover API, Control Manager, and all-in-one component topologies; an external DB/NATS-backed process launch was not run.
- No PostgreSQL-specific integration startup, benchmark, or security test was run.

## 2026-11-11 — Phase C engineering roadmap and backlog

### Changes in this update

- Added `docs/architecture/phase-c-implementation-roadmap.md` with ten milestone outcomes, a 40-item GitHub-ready backlog, dependencies, acceptance/testing/documentation criteria, priority matrix, critical paths, deferred capabilities, and Definition of Done.
- Recorded approval-metadata reconciliation and product/security decision gates without introducing new architecture decisions. No source code, tests, migrations, or deployment files were changed.
- Phase D has not started and remains gated on owner approval.

### Validation

- `git diff --check` — passed. No tests, builds, benchmarks, or security checks were run because this update is documentation only.

## 2026-11-11 — Phase B repository gap analysis

### Changes in this update

- Added `docs/architecture/phase-b-repository-gap-analysis.md`, mapping the current repository to the approved architecture baseline, classifying implementation gaps and actions, recording risks and potential breaking changes, preserving existing test contracts, and ordering future implementation work.
- No source code, tests, configuration, or package layout were changed. No tests, builds, benchmarks, or security checks were run. The Go package listing could not be completed because the sandbox denied writes to the Go cache; dependency-cycle status remains unverified.
- Phase C is not started and remains gated on owner approval.

### Validation

- `git diff --check` — passed. No tests, builds, benchmarks, or security checks were run.

## 2026-11-11 — Implementation architecture baseline proposal

### Changes in this update

- Consolidated approved Phase 1–11 architecture decisions, ADR precedence, Phase 12 integration status, initial implementation scope, mandatory engineering/security/database/testing requirements, production-only conditions, deferred/rejected capabilities, known implementation gaps, and remaining owner decisions into `docs/architecture/implementation-baseline.md`.
- Read the supplied implementation execution prompt and repository guidance. No source code, migrations, deployment files, or tests were changed.
- The baseline is proposed for owner approval; Phase B remains gated on that approval.

### Validation

- `git diff --check` and whitespace checks of the baseline and changelog passed. No tests or implementation checks were run because this update is documentation only.

## 2026-11-11 — Phase 12 architecture validation and final blueprint

### Changes in this update

- Recorded approval to proceed from Phase 11 on its proposed deployment/scaling baseline.
- Added the integrated architecture review, package/dependency model, 16 end-to-end failure/recovery traces, cross-phase conflict register, architecture classifications, staged implementation roadmap, complete ADR index, acceptance checklist, and ADR-053. Amended ADR-007 and older Phase 1/3 text to align broker timing with the accepted ADR-018/Phase 4 and Phase 11 decisions. No source code, schema, manifests, or runtime behavior changed.
- The blueprint remains owner-approval pending. It recommends persist-first Host registration with asynchronous reachability, Control Manager ownership of durable Operation workers, and optional/deferred JetStream; these are target contracts, not implemented behavior.

### Validation

- `git diff --check` and documentation whitespace checks passed. No tests, architecture automation, load tests, or recovery exercises were run because this update is documentation only.

## 2026-11-11 — Phase 11 deployment, scalability and future evolution proposal

### Changes in this update

- Recorded approval to proceed from Phase 10 on its observability, reliability, and SRE design baseline.
- Added all-in-one, standalone API/Control Manager, and PostgreSQL multi-replica topologies; SQLite/PostgreSQL deployment boundaries; DB-backed lease/fencing requirements; JetStream adoption criteria; future Agent enrollment/heartbeat lifecycle; optional container/Kubernetes guidance; upgrade compatibility; and explicit 100/1,000/10,000 Host planning assumptions with no benchmark claims. Added ADRs 047–052. No code, deployment manifests, migrations, Agents, or coordination mechanisms were implemented.

### Validation

- `git diff --check` and documentation whitespace checks passed. No tests, load tests, benchmarks, or restore exercises were run because this update is documentation only.

## 2026-11-11 — Phase 10 observability, reliability and SRE proposal

### Changes in this update

- Recorded approval to proceed from Phase 9 on its security architecture baseline, including production deny-by-default SSH egress.
- Added observability architecture, current telemetry assessment, metrics catalog, logging/tracing conventions, health/readiness model, proposed SLOs, failure behavior, shutdown/drain sequence, backup/recovery procedures, operational requirements, and ADRs 042–046. No source code, dashboards, alerts, telemetry instruments, or operational procedures were implemented.

### Validation

- `git diff --check` and documentation whitespace checks passed. No tests, benchmarks, restore exercises, or security checks were run because this update is documentation only.

## 2026-11-11 — Phase 9 security architecture review

### Changes in this update

- Recorded approval to proceed from Phase 8 on its proposed API, CLI, and application interaction baseline.
- Added the privileged-control-plane threat model, trust boundaries, authentication and resource authorization requirements, secret/key handling, SSH and egress controls, API and configuration protections, audit model, attack scenarios, risk priorities, and security acceptance criteria. Added ADRs 036–041. No source code, runtime security controls, or tests changed.
- Challenged the earlier private-network-allowed default: production SSH egress should deny by default and require explicit destination and port policy plus independent network enforcement. Existing Phase 6 choices (shared SQLite/PostgreSQL database, global configuration, database-backed encrypted secrets, API/CLI management) remain intact.

### Validation

- `git diff --check` and documentation whitespace checks passed. No tests, penetration tests, or implementation checks were run because this update is documentation only.

## 2026-11-11 — Phase 8 API, CLI and application interaction proposal

### Changes in this update

- Recorded approval to proceed from Phase 7 on the recommended Host connector and execution defaults.
- Added API/runtime boundaries, endpoint inventory, resource and Operation lifecycle semantics, configuration/secret interfaces, validation/error/pagination contracts, authorization, CLI hierarchy and exit codes, interaction diagrams, and four draft ADRs. No HTTP handler, CLI command, or API schema was implemented.

### Validation

- `git diff --check` and whitespace checks of the new design documents passed. No tests or implementation checks were run because this update contains documentation only.

## 2026-11-11 — Phase 7 host access and execution architecture proposal

### Changes in this update

- Recorded approval to proceed from Phase 6 on the confirmed configuration/secrets constraints and recommended defaults.
- Added capability-oriented Host connector boundaries, Go SSH lifecycle and trust model, Secret Store resolution, bounded connections and command execution, network restrictions, Agent integration boundary, failure taxonomy, current implementation assessment, and four ADRs. These defaults were accepted when Phase 8 was authorized. No implementation or package changes were made.

### Validation

- `git diff --check` and whitespace checks of the new design documents passed. No tests or implementation checks were run because this update contains documentation only.

## 2026-11-11 — Phase 6 configuration and secrets management proposal

### Changes in this update

- Recorded approval to proceed from Phase 5 on its recommended persistence defaults.
- Added bootstrap/runtime configuration ownership, revision and snapshot propagation, hot-reload classifications, a database-backed envelope-encrypted Secret Store, API/CLI contracts, failure/security handling, Go package boundaries, and five ADRs. The user-confirmed global scope, shared database, external key, event-plus-poll propagation, and API/CLI constraints are preserved. These constraints and recommended defaults were accepted when Phase 7 was authorized. No source code, runtime config subsystem, or migration changed.

### Validation

- `git diff --check` and whitespace checks of the new design documents passed. No tests or schema validation were run because this update contains documentation only.

## 2026-11-11 — Phase 5 persistence and data architecture proposal

### Changes in this update

- Recorded approval to proceed from Phase 4 on its recommended event defaults, including the transactional outbox, durable consumer deduplication where needed, and deferral of NATS/Kafka until justified. Phase 5’s recommended persistence defaults were accepted when Phase 6 was authorized.
- Added the persistence boundaries, logical schema, transaction and concurrency contracts, migration/startup strategy, SQLite/PostgreSQL comparison, recovery/backup guidance, current-state observations, and four draft ADRs. No source code, package layout, repository implementation, or migration changed.

### Validation

- `git diff --check` passed. No tests or schema validation were run because this update contains documentation only.

## 2026-11-11 — Phase 4 event-driven architecture proposal

### Changes in this update

- Recorded approval to proceed from Phase 3 on its recommended initial topology, resync interval, and bounded Host worker count.
- Added the event taxonomy, envelope/versioning contract, transactional outbox and consumer strategy, delivery guarantees, retry/retention policy, database behavior, and future broker criteria. Added three ADRs, including deferral of NATS adoption. These defaults were approved when Phase 5 was authorized. No code or package layout changed.

### Validation

- Not run: this documentation-only messaging proposal was not checked with automated tooling.

## 2026-11-11 — Phase 3 hybrid control loop proposal

### Changes in this update

- Recorded approval to proceed from Phase 2 on the recommended domain baseline.
- Added a level-triggered, edge-driven controller manager design covering resource-key workqueues, deduplication, bounded workers, retries, recovery, resynchronization, shutdown, replica coordination, failure handling, metrics, and Kubernetes pattern comparison. Added three ADRs. These defaults are approved design assumptions, not implementation. No code or package layout changed.

### Validation

- Not run: this documentation-only controller proposal was not checked with automated tooling.

## 2026-11-11 — Phase 2 domain model proposal

### Changes in this update

- Recorded approval to proceed from Phase 1 on its recommended modular-monolith baseline; the Phase 1 defaults remain the working assumptions for this design.
- Added a domain/resource lifecycle model for Host, Operation, conditions, generations, revisions, concurrency, deletion, and domain events, plus four ADRs. These are approved design decisions, not implementation changes.

### Validation

- Not run: this documentation-only domain proposal was not checked with automated tooling.

## 2026-11-11 — Phase 1 target architecture proposal

### Changes in this update

- Added a proposed target architecture covering component ownership, runtime composition, deployment modes, package boundaries, dependency direction, and a distributed evolution path.
- Added ADRs for modular-monolith boundaries, Control Manager and operation-worker ownership, persistence/messaging, and configuration/secrets. These Phase 1 decisions were accepted when approval to proceed to Phase 2 was given; no implementation or package reorganization was performed.

### Validation

- Not run: this documentation-only architecture proposal was not checked with automated tooling.

## 2026-10-29 — Command execution foundation: structured, pinned, audited runs

### Changes in this update

- New permission `runs:execute` (owners and operators; readers cannot start or cancel runs). Execution is disabled by default: `MACHINARCHY_EXECUTION_ENABLED` and a required `MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS` list (`*` must be explicit; enabling without a list stops startup).
- A command is an argument list (`domain.ValidateArgv`, `QuoteArgv`): the program must be a plain name or path, arguments are bounded and free of control characters, and every element is single-quoted when sent, so the remote shell sees one word per argument. There is no shell string.
- `POST /api/v1/runs` validates policy and shape before touching the inventory, resolves the target rule once (at most 200 hosts), pins it as host IDs, optionally verifies a preview's `targetsHash` (409 with the new hash if the hosts differ), supports `dryRun`, and returns 202 with the run. Previews now include `targetsHash`.
- `GET /api/v1/runs`, `GET /api/v1/runs/{id}`, `POST /api/v1/runs/{id}/cancel`. Arguments and per-host output are returned only to callers with `runs:execute`.
- Migration 6 (`runs`, `run_hosts`); the arguments are stored encrypted with the installation key; run-store methods write `run.created`, `run.cancel_requested`, `run.finished`, and `run.interrupted` audit events transactionally (arguments and output never included).
- `service.RunManager`: background executor with a per-run worker pool, per-host timeouts, SSH capacity waiting, sanitized and truncated output (terminal escapes and control characters removed, 64 KiB per host), exit-code reporting (`sshclient.ExitStatus`), refusal to contact a host whose address or port changed since submission, heartbeats, cancellation through the heartbeat, recovery of abandoned runs at startup and periodically, a limit on concurrently executing runs, and orderly shutdown (runs in flight are interrupted and recorded before the database closes). Commands are never retried.
- Removed the unwired fleet-execution placeholder and its result types.
- Tests: argv validation and quoting (including a POSIX-style round-trip of hostile arguments), execution policy, targets hash, status aggregation, output sanitization; run service behavior with in-memory fakes (policy before inventory, validation, dry run, preview binding, results and attribution, mixed outcomes and timeouts, concurrency bound, cancellation, shutdown, moved or deleted hosts, active-run limit, SSH capacity queueing, redaction, authorization, recovery); real-SSH-server runs (quoting reaches the remote shell intact, exit codes, wrong host key never sees credentials or the command, timeouts, forbidden addresses, twelve hosts); store contract tests on SQLite and PostgreSQL (lifecycle, encryption at rest, listing, recovery, audit contents); handler guards and error mapping; configuration parsing.
- Docs: new command-execution section in the security assessment, ADR 0004, architecture, ROADMAP, usage guide (preview, run, watch, cancel), operations guide, README, and `.env.example`.

### Known limitations

- No CLI `exec` command, scripts, file transfer, approval step, agent execution, submission idempotency keys, or retention and export of output. Output is stored unencrypted. Runs execute in the API process, so a crash interrupts them (the next recovery sweep records it). The program allow list cannot judge arguments; do not allow interpreters or `ssh`.
- Swagger files were not updated for the run endpoints; run `make api-docs`.

### Validation

- Not run: Go and Docker were unavailable. Static checks only. Run `gofmt -w`, `make vet test`, and `make test-postgres`. The most timing-sensitive tests are the cancellation, shutdown, recovery, and real-SSH timeout cases; watch them under `-race`.

## 2026-10-29 — Append-only, hash-chained audit trail

### Changes in this update

- Added migration 5 (`audit_events`) for SQLite and PostgreSQL: one row per audited change with actor (user, username, API token, request ID, entry point), action, target, outcome, a small set of non-secret details, and the previous and own SHA-256 hash. Triggers abort UPDATE and DELETE.
- The stores now write the event in the same transaction as the change: account creation (self-registration is attributed to the new account, bootstrap to `bootstrap`), member role changes (not when the role is unchanged), host create, update, and delete, label patches, and API token create and revoke. Failed or no-op operations write nothing. `DeleteHost`, `CreateAPIToken`, `RevokeAPIToken`, and `UpdateWorkspaceMemberRole` are now transactional so they can read what they record and write it atomically.
- Events of a workspace form a hash chain (length-prefixed fields plus the exact timestamp text). Appends are serialized per workspace (single SQLite connection; transaction-scoped advisory lock in PostgreSQL) so the chain cannot fork. `domain.AuditChainVerifier` walks it and reports the first modified, removed, or reordered event.
- New `AuditActor` middleware attaches the authenticated principal and request ID to the request context after authentication; stores read it with `domain.AuditActorFrom`.
- New owner-only endpoints (new `audit:read` permission; human sessions only, API tokens refused): `GET /api/v1/audit/events` (newest first; `action`, `before_id`, `limit` up to 500) and `GET /api/v1/audit/verify` (valid flag, event count, head hash, first invalid event and reason).
- Tests: hash coverage of every field, unambiguous encoding, detail sanitization, filter normalization, actor context, and the verifier (intact, modified, removed, reordered, forged-with-recomputed-hash, truncated, empty); store contract cases on SQLite and PostgreSQL for the full trail (every audited action, actors, request IDs, details, no secrets in any event, failures leaving nothing, paging and filtering, workspace isolation), append-only enforcement, tamper detection by modification and by removal (triggers disabled to simulate a privileged attacker), and concurrent appends keeping one valid chain; service permission matrix; handler guards and error mapping; actor middleware; bootstrap attribution.
- Docs: new audit sections in the security assessment, architecture, ADR 0003, ROADMAP, and an upgrade note in the operations guide (including recording the head hash outside the database).

### Known limitations

- Denied requests, failed logins, reads, bulk-import summaries, and automatic host status changes are not recorded. No retention, archival, or export. A database administrator can disable the triggers and rewrite the chain; removal of the newest events is only detectable by comparing a head hash recorded elsewhere. New mutating store methods must write an event themselves.
- Swagger files were not updated for the two audit endpoints; run `make api-docs` (the handler annotations are in place).

### Validation

- Not run: Go and Docker were unavailable. Static checks only (identifier resolution, interface and fake signatures, call arity, unused variables and imports, printf formats, missing imports). Run `gofmt -w`, `make vet test`, and `make test-postgres`; the PostgreSQL triggers, advisory lock, and `ALTER TABLE ... DISABLE TRIGGER` in the tamper tests are the most likely places to need adjustment.

## 2026-10-29 — Verification pass (static review without a Go toolchain)

### Problems found and fixed

- Two test fakes (`targetFakeEnvs`, `listFakeEnvs`) implemented `ListEnvironments` with the wrong return type (`[]domain.Environment` instead of `[]*domain.Environment`), which would not compile.
- `internal/transport/http/v1/router.go` had an unsorted import group (gofmt reorders imports, so the CI formatting check would have failed), and `pkg/netpolicy/policy.go` ended with extra blank lines.
- CORS did not allow `PATCH`, so browser clients could not call `PATCH /api/v1/hosts/{id}/labels` (preflight would be refused). Added `PATCH` and a preflight regression test (`middleware/cors_test.go`).
- The embedded OpenAPI spec (`docs/swagger.json`, served at runtime) lacked the members, bulk registration, fleet preview, and label patch endpoints, the `selector` query parameter, and the 409 responses. Added them by hand and regenerated `swagger.yaml` from the same data; every `$ref` resolves. Run `make api-docs` when the Swaggo CLI is available so the generator becomes the source of truth again (the handler annotations are in place).
- Stale documentation: roadmap, architecture, and usage docs still described a private workspace per account. Corrected them, added ADR 0003 (shared workspace, bootstrap owner, owner-managed roles, last-owner protection, pre-flight migrations) and marked the superseded parts of ADR 0002, and added an "Upgrading" section to `docs/operations.md` covering the first-owner setting, the duplicate-host migration check, the SSH target policy, and label rules.

### Checks performed (static only)

- Qualified identifiers resolve to real declarations across all module packages; no duplicate top-level declarations; struct-literal field names exist; store and service interfaces are satisfied by both stores and by every fake, and fakes that embed an interface override its methods with matching signatures; call arity for module functions; every method called on the store interfaces exists; field access through `SSH`/`AuthType` chains; capitalized and lowercase unqualified identifiers resolve; no unused imports or unused variables (scope-aware); import groups sorted; indentation and end-of-file newlines; 923 printf-style calls have matching verb and argument counts and `%w` appears only in `Errorf`; no non-constant format strings; the shell script and workflow YAML parse; the OpenAPI JSON parses and all references resolve; environment variable names agree across code, `.env.example`, and docs. The checkers were validated by injecting known errors into a scratch copy and confirming each was reported.
- Assertions in the integration tests were compared against the store and client implementations (not-found and conflict error types, status guards, token semantics, DSN handling for the PostgreSQL schema isolation, literal status/connection values in raw INSERTs, column widths).

### Not verified

- Nothing was compiled, vetted, formatted, or executed: Go and Docker were unavailable. Heuristic checks cannot find type errors in expressions, missing returns, or gofmt alignment differences (column alignment of struct and map literals and comments); expect `gofmt -w` to touch the new files. Run `make fmt`, then `make vet test` and `make test-postgres` before merging.

## 2026-10-29 — Fleet targeting: labels, selectors, include/exclude rules, and target preview

### Changes in this update

- Labels are now validated: keys of 1-63 characters from letters, digits, `.`, `_`, `-`, `/` (starting and ending alphanumeric), values up to 63 characters (may be empty), at most 50 labels per host, and the built-in field names `id`, `name`, `hostname`, `address`, `status`, `environment`, and `connection` are reserved. Validation applies in `Host.SSHValidate` and at the API boundary for create, update, and each bulk record. Existing stored labels are not rewritten.
- Replaced the old `domain.HostSelector` (which matched every host when empty) with a parsed `domain.Selector`: comma-separated AND requirements `key=value`, `key!=value`, `key in (a,b)`, `key notin (a,b)`, `key`, and `!key`, with `*` and `?` wildcards, case-insensitive built-in fields (`name`, `hostname`, `address`, `status`, `connection`, `environment`, `id`), case-sensitive labels, and caps on length (2048), requirements (20), set size (50), and value length. There are no regular expressions; wildcard matching is non-backtracking. An empty or malformed selector is a parse error.
- Added `domain.TargetRule` and `domain.ResolveTargets`: a selector or an include list or an explicit `all`, minus an exclude list that always wins; entries are host IDs or names; unknown include/exclude entries are reported; results are sorted deterministically. A rule with nothing to select, `all` combined with a selector or include list, oversized lists, and blank entries are rejected.
- Added `POST /api/v1/fleet/targets/preview` (inventory read permission): returns the exact total, up to 500 credential-free host views, the hosts removed by the exclude list, and unknown entries. Nothing is contacted.
- Added `GET /api/v1/hosts?selector=...` filtering (an empty or invalid selector returns 400, never everything).
- Added `PATCH /api/v1/hosts/{id}/labels` (`{"set": {...}, "remove": [...]}`) backed by the new `HostStore.PatchHostLabels`, which merges atomically (a single-connection transaction in SQLite, `SELECT ... FOR UPDATE` in PostgreSQL), leaves credentials and connection settings untouched, enforces the label limit on the merged result, and is workspace-scoped.
- The unwired fleet placeholder now takes a `TargetRule`, resolves it with the shared resolver, and still reports `ErrExecutionNotImplemented`; `NewFleetService` also takes the environment store.
- Tests: selector parsing (accepted and rejected forms), matching semantics, wildcard matching (including a pathological pattern), rule resolution (selector/include/exclude/all, IDs vs names, unknown entries, determinism, never-default-to-all), label validation and patch rules, target service (workspace scoping, authorization, no credentials in output, truncation), fleet and host handler behavior, and store contract cases for label patches (merge, remove, limits, isolation, concurrent patches) on SQLite and PostgreSQL.
- Docs: ROADMAP.md (Phase 4 status), architecture.md, SECURITY.md (new fleet-targeting section).

### Known limitations

- No execution yet; selectors are evaluated at request time, so execution must re-resolve or pin the previewed host IDs. No OR or grouping in selectors. No bulk relabeling by selector. Label changes are not audited. Swagger files were not regenerated; run `make api-docs`.
- Existing databases may hold labels that no longer satisfy the new rules; they keep working for reads and selectors and can be removed with a patch, but a full host update (`PUT`) that resends them is rejected until they are fixed.

### Validation

- Not run: Go was unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `make fmt vet test` and `make test-postgres` before merging.

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
- Added `make test-postgres` and `scripts/db/test-postgres.sh` to run the suite against a throwaway Docker container, and documented both in `CONTRIBUTING.md`.
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
- Reworked `POST /api/v1/hosts/bulk`: it now accepts `Payload` records (credentials and fingerprint can be supplied), sets the workspace from the authenticated principal, validates environments per record, supports `validate_only`, detects duplicates (name or address:port, in the inventory and in the batch), uses a bounded worker pool, preserves request order, reports every record, and returns sanitized errors.
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

- Updated `scripts/dev/setup-local.sh` to match Machinarchy: require Go 1.27+, create `.env` securely from the repository template, and configure the checked-in hooks. Removed unrelated frontend/assets setup and automatic `go mod tidy`/dependency download behavior.
- Made setup preserve an existing non-default `core.hooksPath` by stopping for review instead of silently replacing it.
- Updated the commit-message hook to validate conventional breaking-change syntax and reject invalid invocation; refreshed the Git-hook documentation to reference current paths and behavior.

### Validation

- `bash -n scripts/dev/setup-local.sh .githooks/pre-commit .githooks/commit-msg` — passed.
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
