# 09 - Acceptance tests

> **None of these tests has been run.** They are the executable definition of "done" for `10-roadmap.md`. Each has an ID, an initial state, the command and interaction, the expected result, the exit code, and what counts as failure. Exit codes are those of `02-commands.md` 2.3 and 3.10.5.

## 0. Conventions

**Layers** (column "L"): **F** = in-process, `cli.Run` with a fake API client, fake terminal, fake clock (`05-architecture.md` 11); **G** = golden file comparison; **P** = a real pseudo-terminal or the system `script` tool for the interactive and hidden-input cases; **L** = against a live lab controller (`docs/cli/user-full-guide.md` lab: three SSH servers, `make ssh-servers-up`) and, for agent cases, a lab NATS and agent; **C** = controller-side change required first (C-nn). A test that needs a missing controller change is written against a fake that implements the proposed contract and re-run live when the change ships.

**Fixtures:** `owner` (password session), `operator-token`, `reader-token`, `expired-session`, `revoked-token`; a fake fleet `web-01..web-14`, `db-01..db-03`; a synthetic fleet generator for 200, 500 and 5,000 hosts; a hostile fleet with host names containing `ESC[2J`, `$(touch /tmp/x)`, newlines, bidi controls.

**Global failure criteria** (apply to every test): the command prints a secret (password, token, key, enrolment token value outside its one allowed place); prints text from the server or a host that was not sanitised; makes a state-changing request that the test said must not happen; hangs (any test has a 60 s limit unless it states one); prints a stack trace; exits with a code other than the one stated.

## A. The five-minute test

### A-01 SSH path (L, P)
- **Initial state:** a controller running with a bootstrap owner; one lab SSH server reachable from the controller (key auth); the CLI binary just downloaded; no CLI config; a timer starts at `machinarchy` first typed.
- **Steps:** `machinarchy` -> `machinarchy login` (answers: server URL, default name, option 1, email, password, `Y` to stay signed in) -> `machinarchy hosts add` (answers: name, address, option 1 key, user, key path, send `Y`, option 1 approve) -> type `yes` when asked to trust the key -> `machinarchy run --on web-01 -- uptime` (answer `y`).
- **Expected:** every prompt matches `03-ux.md` section 2.1 in wording and order; the host reaches `ready`; the run prints `✔ 1 of 1 hosts succeeded` and the uptime line; the user never types a command that is not printed in a `Next:` line, except the first (`machinarchy`).
- **Exit codes:** 0 for each command.
- **Pass:** total time under 5 minutes of wall clock for a person following the printed prompts (measured in a usability session with a person who has not used it; the automated version under 90 s excluding human think-time); zero uses of documentation.
- **Fails if:** any prompt needs a term from the forbidden list (`07-brand.md` 2); a step needs a flag the CLI did not print; the fingerprint shown differs from `status.observedHostKeys`; the key was trusted without the typed prefix; time exceeds the limit.

### A-02 Agent path (L, C-07, Q1)
- **Initial state:** as A-01 but the target is an agent host; the controller has an agent gateway; install information per C-07.
- **Steps:** `login` as above, `machinarchy hosts add db-01 --agent`, run the printed install command on the target, wait, `machinarchy run --on db-01 --do runtime.info`.
- **Expected:** `03-ux.md` section 2.2; the install command was **built by the CLI** from structured fields; the enrolment token printed once; `db-01` becomes `ready`; the run prints the agent's runtime information.
- **Exit codes:** 0.
- **Fails if:** the install command contains text not derivable from validated fields; the token is printed twice or written to a file without `--token-out`; the host stays `waiting for agent` after the agent started; the path needs a value the CLI did not provide.
- **Without C-07:** the test asserts the "four values" fallback text and exit 0 for `hosts add`; A-02 itself is marked blocked.

### A-03 Timing budget (F)
- With a fake API returning instantly, `login` + `hosts add` (fake host reaching `ready` after 5 s of fake time) + `run` complete with fewer than 25 interactions (prompts answered) in total. **Fails if** more.

## B. Authentication and authorisation

| ID | L | Initial state | Command / interaction | Expected result | Exit | Fails if |
|---|---|---|---|---|---|---|
| B-01 | F,P | server with user `maxim` | `login` with a wrong password | `Sign-in failed: the email or password is not right.` plus `Next: machinarchy login`; nothing saved; the message does not say which part was wrong | 3 | credential saved; password echoed; different text for unknown user vs wrong password |
| B-02 | F | server returns 429 with `Retry-After: 40` | `login` | `Too many sign-in attempts. Try again in 40 s.` | 4 | retries by itself; prints raw JSON |
| B-03 | F | saved session whose `expiresAt` is past, server answers 401 | `machinarchy hosts` | four-part error naming the expiry time, `Next: machinarchy login` | 3 | says "forbidden"; suggests `config set-credentials` |
| B-04 | F | home screen with expired session | `machinarchy` | the "session expired" home variant; no stack of errors | 3 | prints a raw API error |
| B-05 | F | saved API token, server answers 401 | `machinarchy hosts` | `the saved device token was revoked or has expired` + `Next: machinarchy login` | 3 | claims "expired" for a 403 |
| B-06 | F | `reader-token` | `machinarchy run --on web-01 -- uptime` | refused **before** any submit request: `reader` is not allowed to run commands; `Next: machinarchy tokens create NAME --role operator` | 3 | a mutating request is sent; message says "expired" |
| B-07 | F | `operator-token` | `machinarchy hosts trust web-01` | refused locally (owner and password needed) with `Next: machinarchy login`; no request to `/trust` | 3 | request sent; generic 403 text |
| B-08 | F | `operator-token` | `machinarchy tokens create x --role reader` | same owner-needed message | 3 | request sent |
| B-09 | F,P | no config | `printf '%s' "$T" \| machinarchy login URL --with-token --no-input` | token validated with `GET /api/v1/me` before saving; saved; `whoami` shows the user | 0 | token saved before validation; token appears in output |
| B-10 | F | no config | `machinarchy login URL --with-token` with a rejected token | `the server did not accept this token`; nothing saved | 3 | token saved |
| B-11 | F | `MACHINARCHY_TOKEN` and `MACHINARCHY_SERVER` set, empty config | `machinarchy hosts` | works; config directory stays empty | 0 | file written; token in any output |
| B-12 | F | saved token for server A; `MACHINARCHY_SERVER=https://B` set, no `MACHINARCHY_TOKEN` | `machinarchy hosts` | refuses: no credential for B; **no request to B carries A's token** (assert on the fake transport) | 3 | A's token sent to B |
| B-13 | F | saved session for A; `login --server B` style invocation (legacy `--server`) | any command | token for A never sent to B; login to B creates a new entry | 0/3 | B's session stored under A |
| B-14 | F | precedence | env token, saved token and saved session all present | the env token is used; then saved token; then session (assert `Authorization` header) | 0 | any other order |
| B-15 | F | session login | `machinarchy logout` | `POST /api/auth/logout` sent, local credential removed, a later request is not sent with the old token; the fake server records the session as revoked | 0 | local copy kept; no server call |
| B-16 | F | token credential | `machinarchy logout` | local copy removed; message says the token still works; **no** call to revoke | 0 | claims the token is revoked |
| B-17 | F | owner session | `machinarchy logout --everywhere --yes` | `DELETE /api/v1/sessions`; local copy removed | 0 | tokens revoked by it |
| B-18 | F,P | owner session | `login` answering `y` to stay signed in | `POST /api/v1/tokens` with role operator, 90 days, name `device-HOST-DATE`; both session and device token saved; `tokens` lists it | 0 | role other than operator; password stored |
| B-19 | F | device token + expired session | `machinarchy hosts trust web-01` | owner-needed message with `Next: machinarchy login` | 3 | uses the token for an owner call |
| B-20 | F | `whoami` for session, device token, env token | `machinarchy whoami -o json` | method, server, user; role/expiry from `/sessions` or `/me`; `unknown` for a token until C-03 | 0 | guesses a role |
| B-21 | F,C | C-03 shipped | `login --with-token` | role and expiry shown from `/me` | 0 | still prints `unknown` |
| B-22 | F | token never visible: run `login --with-token`, `tokens create`, `whoami`, `hosts`, `run`, each with `-v`, `-o json`, `-o yaml`, quiet | grep **every** byte of stdout, stderr and the config/credentials files (file backend) for the token and password test values | the token appears only (a) once in `tokens create` output, (b) in the credentials file | 0 | any other occurrence |
| B-23 | P | real process | while `login --with-token` waits on stdin, list processes (`ps -ww`) and read `/proc/PID/environ` where available | no token or password in argv or environment of the CLI or any child | 0 | secret visible |
| B-24 | F | history | try `machinarchy login --password x`, `--token x`, `tokens create n --token x` | unknown flag errors (`--token` only exists on the deprecated `config set-credentials` alias, which warns) | 2 | secret accepted as a flag value on a new command |

## S. Security

| ID | L | Initial state | Command / interaction | Expected result | Exit | Fails if |
|---|---|---|---|---|---|---|
| S-01 | F | server certificate signed by an unknown CA | `machinarchy login https://lab` | `the server's certificate is not trusted (certificate signed by unknown authority)`; `Next: machinarchy login URL --ca-file ca.pem`; nothing saved | 4 | proceeds; offers a skip-verify |
| S-02 | F | same server, valid `--ca-file` | `login --ca-file ca.pem` | works; CA copied to `ca/NAME.pem` (0600); used by later commands for that server only | 0 | CA applied to other servers |
| S-03 | F | no flag exists | `machinarchy login URL --insecure-skip-verify` | unknown flag error | 2 | flag exists |
| S-04 | F | `http://10.0.0.5:8080` | `login` | refused with `Use https://`; `--insecure-http` accepted, saved per server, `whoami` shows `http (insecure)`; home screen warns | 2 then 0 | token sent over http without the flag |
| S-05 | F | password test value `P@ss-SENTINEL` | run login flows with `-v` | the sentinel appears nowhere in stdout, stderr, config, credentials | 0 | any occurrence |
| S-06 | F,P | host trusted with key K1; controller reports K2 | `machinarchy hosts show web-01` | `key changed` alarm text of `03-ux.md` section 8 with both fingerprints | 0 | looks like a routine approval |
| S-07 | F,P | same | `hosts trust web-01 --replace` interactively | asks for the first 8 characters of the **new** key and then `web-01`; protected env also asks for the env name; only then `POST .../trust` with `replace: true` | 0 | fewer confirmations; accepts `y` |
| S-08 | F | same | `hosts trust web-01 --replace --yes --no-input` | `--yes cannot approve an SSH host key`; no request | 2 | request sent |
| S-09 | F | host `waiting for key approval` | `hosts trust web-01 --yes` | refused as S-08 | 2 | trust sent |
| S-10 | F | same | `hosts trust --all-pending --no-input` | refused: `would trust keys you have not seen` | 2 | trust sent |
| S-11 | F | same | `hosts trust web-01 --fingerprint SHA256:WRONG --no-input` | refused: not among the observed keys; no request | 2 | trust sent |
| S-12 | F | same | `hosts trust web-01 --fingerprint <observed> --no-input` | request sent with that fingerprint set; success | 0 | no request |
| S-13 | F | hostile fleet (ESC sequences, bidi, `$(...)`) | `hosts`, `hosts show`, `run` listing, error messages, `-o table` | no ESC, C1, bidi or zero-width characters reach stdout/stderr; replaced as `04-security.md` section 7 | 0 | any raw control byte (byte-level assertion) |
| S-14 | F | run output containing `ESC]52;c;...BEL` (clipboard write), `ESC[2J`, `\r` overwrite | `run r-ID --host web-01` on a pty-like stdout | sanitised; message `[output contained N control characters; shown sanitised]` | 0 | sequence passed through |
| S-15 | F | same, stdout is a pipe | `run r-ID --host web-01 --raw` | raw bytes pass | 0 | sanitised when `--raw` and not a terminal |
| S-16 | F | same, stdout is a terminal | `run r-ID --host web-01 --raw` | `--raw` refused on a terminal | 2 | raw output on a terminal |
| S-17 | F | `-o json` with hostile output | `run r-ID -o json` | control characters JSON-escaped; valid JSON | 0 | invalid JSON; raw ESC byte |
| S-18 | F | secret redaction | `config show`, `whoami -o json`, `-v` logs, `hosts -o yaml`, `access show -o json`, error output | no credential appears; `config show` prints `***` for credentials | 0 | any credential |
| S-19 | F | no env, interactive | `hosts remove web-01` answering `y` (not the name) | not confirmed; `aborted; nothing was changed`; no DELETE | 1 | request sent |
| S-20 | F | non-interactive | `hosts remove web-01 --no-input` | exit with the missing `--confirm web-01` named; no DELETE | 2 | request sent |
| S-21 | F | `hosts remove web-01 --yes --no-input` | `--yes` is enough only where the standard allows it: for D3 outside protected envs the typed name is required: needs `--confirm web-01` | 2 | removes with `--yes` alone |
| S-22 | F | protected env `production`, CI (no terminal) | `machinarchy run --on tier=web --yes -- systemctl restart nginx` | refused: `needs --confirm-env production`; **no submit request** | 2 | submit sent |
| S-23 | F | same | add `--confirm-env production` | submit sent with `confirmEnvironment: "production"` (C-02) or the local guard satisfied | 0 | not sent |
| S-24 | F | same | `--confirm-env staging` | refused: must equal `production` | 2 | submit sent |
| S-25 | F,P | protected env, interactive | answer the typed prompt with `prod` | not confirmed; no submit | 1 | submit sent |
| S-26 | F | `--on all`, no env, 37 hosts | interactive | asks to type `37`; wrong value aborts | 1 | proceeds |
| S-27 | F | config file mode 0644 with credentials | any command | fails closed with `chmod 600` instruction | 2 | runs |
| S-28 | F | redirect 302 from server | any command | not followed; error with the target in the hint; token not sent to the target | 2 | request to `Location` |
| S-29 | F | response larger than 16 MiB or malformed JSON | any list command | `exceeds 16777216 bytes` / `did not answer like a Machinarchy server`; bounded memory | 4 | OOM; partial output |
| S-30 | F | `--dry-run` | every D2/D3 command with `--dry-run` | prints the preview; fake records **no** non-GET request except `POST .../operations/preview` | 0 | a mutating request |
| S-31 | F | server-supplied install info with a hostile `downloadUrl` (`http://`, `;rm -rf`, spaces) | `hosts add db-01 --agent` | install command not printed; validation error and the four-value fallback | 0 | hostile string printed |

## O. Operations and failures

| ID | L | Initial state | Command / interaction | Expected result | Exit | Fails if |
|---|---|---|---|---|---|---|
| O-01 | F | server down (connection refused) | `machinarchy hosts` | `Cannot reach {server}: connection refused.` + `Next: check the address, or: machinarchy switch` | 4 | multi-line library noise; retry loop |
| O-02 | F | request timeout | `--request-timeout 2s hosts` | timeout message naming the flag | 4 | hangs longer than 3 s |
| O-03 | F | invalid address | `hosts add web-01 'bad address'` | refused locally: address must be HOST or HOST:PORT; no request | 2 | request sent |
| O-04 | L | host unreachable (closed port) | `hosts add web-09 10.0.0.9:22 ...` | host registered; wait ends with `cannot connect` and the controller's reason; the command exits 0 with status shown and `Next: machinarchy hosts show web-09` | 0 | claims ready; hangs beyond `--wait` |
| O-05 | F | duplicate | `hosts add web-01 ...` when exists | `A host named web-01 already exists` + `Next: machinarchy hosts show web-01` | 7 | creates a second |
| O-06 | F | run with 14 hosts, 11 succeed, 3 fail | `run --on tier=web -- cmd` | output of G-03; exit | 8 | exit 1 or 0 |
| O-07 | F | all fail | same | G-02; exit | 9 | exit 8 |
| O-08 | F | run reaches terminal state `PartiallySucceeded` | same | counts match the per-target states | 8 | uses only the operation state |
| O-09 | F | `run r-ID --retry-failed` with 3 `Failed`, 2 `Lost`, 1 `Cancelled`, 8 `Succeeded` | interactive `y` | new submit with `include` = the 3 failed hosts; `Lost` and `Cancelled` listed as skipped; new run ID; old run unchanged | 0/8/9 | includes unknown outcomes by default |
| O-10 | F | same with `--include-unknown` | | adds `Lost`/`Interrupted` hosts and prints the "may already have run" warning | | no warning |
| O-11 | F | retry in a protected env, non-interactive | `--retry-failed --yes` | refused without `--confirm-env` | 2 | submit sent |
| O-12 | F | cancel | `run r-ID --cancel` then fake moves `CancelRequested` -> `Cancelled` | prints `Cancellation requested`, follows to the final state, never prints "stopped" before the terminal state; hosts with `Lost (unreachable_during_cancel)` are flagged | 8/9 | says stopped early |
| O-13 | F | cancel, `--no-wait` | returns right after the request | 0 | waits |
| O-14 | P,F | following a running run | send SIGINT once | prints `Stopped watching...` with both resume lines; no cancel request sent; exit | 130 | cancel sent |
| O-15 | P,F | same | send SIGINT twice | process ends immediately | 130 | hangs |
| O-16 | P,F | between "Run it? y" and the reply | SIGINT during submit | prints the ambiguous-submit message and `Next: machinarchy run`; no resubmit | 130 | silent; resubmits |
| O-17 | F | connection dropped during submit (no reply) | `run ... -- cmd` | same ambiguous message; no automatic retry (assert exactly one POST) | 4 | second POST |
| O-18 | F | connection dropped while following | poll fails twice then recovers | continues; if it keeps failing prints `lost contact; the run keeps going` + resume line | 4 if given up | gives up without the resume line |
| O-19 | F | operation JSON missing fields / wrong types | `run r-ID` | `did not answer like a Machinarchy server` naming the endpoint | 4 | panic |
| O-20 | F | incompatible version (C-11 header outside range) | any command | `This server speaks API version X; this CLI supports Y` + update hint | 4 | proceeds |
| O-21 | F | `targets_changed` 409 at submit | `run ...` | `The set of hosts changed while you were deciding. Nothing ran.` | 7 | submits again |
| O-22 | F | selection > 200 hosts | `run --on all -- uptime` | refused before the preview prompt; mentions 200; no submit | 2 | submit sent |
| O-23 | F | active env `production` | `run --on tier=web -- uptime` | preview request carries selector `environment=production,tier=web` (audit F-02 regression) | 0 | env ignored |
| O-24 | F | conflicting env | `--env staging` with `--on environment=production` | refused as conflicting | 2 | silently picks one |
| O-25 | F | `--on` parsing | `web-01,web-02`; `web-*`; `tier=web`; `tier=web,region in (eu,us)`; `all`; `--except web-02`; mixing names and selector terms | maps to `include`, expanded `include`, `selector`, `selector`, `all` or env selector, `exclude`; the mixed form is a usage error | 0/2 | wrong TargetRule |
| O-26 | F | `--on env=production` | | treated as a **tag** named `env` (no rewrite); help says use `--env` | 0 | rewritten to `environment` |
| O-27 | F | timeouts | run where 2 hosts hit `TimedOut` | shown as `timed out after 60 s`; legend line; exit | 8 | counted as succeeded |
| O-28 | F | program run on an agent-only host | `run --on db-01 -- uptime` | the controller's refusal is shown with a `Next` (use `--do`) | 2/7 | raw JSON |
| O-29 | F | `run --on web-01 --do service.status service=nginx` | | request body `capability{name:"service.status", input:{"service":"nginx"}}`; **no** `.service` suffix added | 0 | value modified |
| O-30 | F | `--do` with `--` | | usage error | 2 | runs |
| O-31 | F | `run` as reader | `machinarchy run` (list) | works; argument column shows `(hidden)` | 0 | error |
| O-32 | F | `hosts remove` while a run targets the host (409) | | server message with `Next: machinarchy run` | 7 | retries |
| O-33 | F | `hosts move` before C-01 | | `This server cannot move hosts between environments yet.` no request | 2 | request sent |
| O-34 | L | real lab | `hosts remove` an agent host | output lists the four "not done" items; the agent identity remains valid until `hosts revoke-agent` (verified by the agent still connecting) | 0 | claims the agent is revoked |
| O-35 | F | host removal text | `hosts remove web-01` | contains the strings "does NOT", "revoke-agent" (agent hosts), "authorized_keys" (ssh hosts) | 0 | missing items |
| O-36 | F | `access remove` in use | | server 409 + `Next: machinarchy hosts` | 7 | |
| O-37 | F | `envs add production --protected` before C-02 | | warning about local-only guard; `y` required; stored as `protected (local)` | 0 | claims server enforcement |
| O-38 | F | stale revision | `hosts tag web-01 tier=web` when the host changed | `the host changed, rerun` | 7 | silently overwrites |

## G. Grouped-output golden tests

All G tests compare stdout (and, where stated, stderr) byte-for-byte with files in `testdata/group/`, in rich, plain and non-TTY modes. Inputs are synthetic `Result` sets; time is fake.

| ID | Input | Expected | Exit | Fails if |
|---|---|---|---|---|
| G-01 | 14 hosts `web-01..14`, all exit 0, stdout `active` | one group `web-[01-14]` exit 0 `active`; header `✔ 14 of 14 hosts succeeded` (`03-ux.md` 7.3) | 0 | more than one group |
| G-02 | 14 hosts all exit 3, stdout `inactive` | one failed group (7.4) | 9 | |
| G-03 | 14 hosts: 9 ok, `web-09` exit 3, `web-13` unreachable, `web-14` timed out, `web-[05,11]` Lost (9 + 1 + 1 + 1 + 2 = 14) | classes in order failed, timed out, unknown, succeeded (7.5); legend printed | 8 | wrong order; Lost counted as failed or ok |
| G-04 | **500** synthetic hosts `app-001..500`; 480 ok identical; 12 fail with 3 distinct messages; 8 unreachable | output at most 40 lines at width 100; groups listed; host lists compressed (`app-[001-017,019-...]`); `(+N more)` used; total counts exact; `-o json` has every host | 8 | more than 40 lines; a host missing from the JSON; wall time over 1 s on the fixture |
| G-05 | **5,000** synthetic hosts, 5 outcome classes, 200 distinct output groups | at most 60 lines at width 100 (10 groups + `and 190 more groups, N hosts`); exact counts; peak heap under 50 MB measured with `runtime.ReadMemStats` while feeding 15 KiB per host; under 2 s | 8 | memory growth proportional to total output bytes; any line over the width limit |
| G-06 | timeouts: 3 hosts `TimedOut` | `timed out after 60 s`, grouped, with legend | 8 | |
| G-07 | drift: 11 hosts `nginx/1.24.0`, 2 `nginx/1.22.1`, 1 `nginx/1.24.0 (Debian)` | warning `3 hosts printed something different from the other 11` (7.6); exit | 0 | no warning; or exit non-zero without `--fail-on-drift` |
| G-08 | time-dependent: 14 hosts each with a different `uptime` line | `every host printed something different (normal for time-dependent output)`; **no** drift warning; no 14-group dump | 0 | warning shown; 14 groups printed |
| G-09 | normalisation: lines differing only by timestamp, PID, UUID, `0x` address | one group | 0 | split into groups |
| G-10 | normalisation limits: `nginx/1.24.0` vs `nginx/1.22.1` | two groups (digits are not masked) | 0 | merged |
| G-11 | range compression: `web-01..web-12,web-14`; `app-1,app-2,app-10`; `a`, `db-primary`, `db-01`; width 20 | `web-[01-12,14]`; `app-[1-2,10]`; mixed names listed whole; cut with `(+N more)` | | numeric range wrong; natural order wrong (`app-10` before `app-2`) |
| G-12 | input order independence | shuffle the same 500 results 20 times | identical output | any difference |
| G-13 | progress lines | feed 200 results over fake time, stdout not a terminal | one progress line every 10 s and at each 10 %, none redrawn; none on stdout | `\r` or cursor codes in output |
| G-14 | sample text sanitising | group sample containing ESC and bidi | removed; counts reported | raw |
| G-15 | sample size | 300-line output | first 5 lines + `[… 295 more lines]` | prints all |
| G-16 | `--no-groups` | one block per host | | groups |
| G-17 | `--groups all -o json` | every group and every host | | truncation |
| G-18 | stdout/stderr | progress on stderr, result on stdout | | mixed |
| G-19 | plain mode | `OK`/`FAILED` markers, no symbols, no colour | | symbols present |
| G-20 | 200-host run against the lab, 3 hosts down | same shape as 7.8 | 8 | L only |

## N. Automation

| ID | L | Initial state | Command | Expected | Exit | Fails if |
|---|---|---|---|---|---|---|
| N-01 | F,G | fixed fleet | `hosts -o json`, `-o yaml`, `access -o json`, `tokens -o json`, `run -o json`, `run ID -o json`, `whoami -o json`, `-o json` home | field names equal the API's; golden schema check (types and required keys); new keys allowed, none removed | 0 | a key renamed or removed |
| N-02 | F | | the same commands twice | byte-identical output (ordering stable) | 0 | nondeterminism |
| N-03 | F | every exit code | provoke each: 0,1,2,3,4,5,6,7,8,9,130 | matches table (2.3) | each | wrong code |
| N-04 | F,P | stdin not a terminal | any command that would prompt, without `--no-input` | does not block; exits naming the missing flag | 2 | waits for input |
| N-05 | F | `--no-input` on a terminal | `hosts add` | refuses to prompt | 2 | prompts |
| N-06 | F | CI | `MACHINARCHY_TOKEN`, `MACHINARCHY_SERVER`, `MACHINARCHY_ENV`, `MACHINARCHY_NO_INPUT=1`, `MACHINARCHY_OUTPUT=json` | all honoured; config dir stays empty | 0 | writes files; ignores a variable |
| N-07 | F | errors in JSON mode | `hosts -o json` with the server down | stdout empty; stderr JSON `{"error":{"code","message","next"}}` | 4 | text on stdout |
| N-08 | F | `-q` | `hosts -q`, `run -q`, `tokens create n -q` | identifiers only (token only for create) | 0 | decoration |
| N-09 | F | `--dry-run` | covered by S-30 | | | |
| N-10 | F | stdout is a pipe | `machinarchy` (home) | plain text, no redraw, no colour | 0 | ANSI codes |
| N-11 | F | `NO_COLOR=1` on a terminal | any | no colour codes, symbols still shown | 0 | colour |
| N-12 | F | `LANG=C` | any | ASCII markers | 0 | non-ASCII bytes |

## K. Compatibility

| ID | L | Initial state | Command | Expected | Exit | Fails if |
|---|---|---|---|---|---|---|
| K-01 | F | v1 config with 2 contexts, one `mch_pat_` token, one session JWT | any command | converted to v2, backup byte-identical to the original (0600), notice printed once, `switch` lists both | 0 | credential lost; backup differs |
| K-02 | F | v1 config unparsable | any | stops naming the file; nothing rewritten | 2 | overwritten |
| K-03 | F | v2 config newer than the CLI | any | stops; says update | 2 | |
| K-04 | F | **every** old command in `08-migration.md` section 1 (table-driven, 40+ rows) | the old invocation with the same arguments | old output and exit code unchanged (golden from the pre-change CLI), plus one stderr line `Deprecated: "OLD" is now "NEW"...` | as before | output changed; missing deprecation line; line printed twice |
| K-05 | F | legacy `run -l tier=web -- uptime` (no `--wait`) | | detached submit message exactly as today, exit 0 | 0 | waits |
| K-06 | F | legacy `run ... --wait` with a failed host | | exit 1 (not 8/9) | 1 | 8/9 |
| K-07 | F | new `run --on ... ` | | waits; exit 8/9 | 8/9 | |
| K-08 | F | old env vars `MACHINARCHY_CLI_*` | | honoured with a one-line warning naming the replacement; new variable wins when both set | 0 | silently ignored; no warning |
| K-09 | F | `config set-credentials n --token abc` | | works, warns about history, deprecation line | 0 | silent |
| K-10 | F | `trust host web-01 --yes` | | refused per `08-migration.md` section 3 | 2 | trust sent |
| K-11 | F | `--server URL` with saved credentials (legacy) | | no saved credential sent (B4) | 3 | token sent |
| K-12 | F | `MACHINARCHY_NO_DEPRECATION_WARNINGS=1` | any old command | no deprecation line; command works | as before | line printed |
| K-13 | F | R+1 stub (removed commands) | `machinarchy get hosts` | `"get hosts" was removed in R+1. Use "machinarchy hosts".` | 2 | `unknown command` only |
| K-14 | F | hidden/deprecated commands | `--help` and completion | not listed | 0 | listed |
| K-15 | F | downgrade | copy `config.yaml.v1.bak` over `config.yaml` and run the previous release | works | | (manual) |

## C. Tab completion

Tests drive Cobra's hidden `__complete` entry with the fake API, so no real shell is needed; a lab test installs the script in bash and zsh in a container and presses Tab through `script`/`expect`.

| ID | L | Initial state | Input | Expected | Fails if |
|---|---|---|---|---|---|
| C-01 | F | signed in, fleet `web-01..14` | `__complete hosts show we` | the 14 names with readiness descriptions, directive no-file | file names offered |
| C-02 | F | hostile fleet | `__complete hosts show ''` | names containing spaces, quotes, `$`, backtick, `;`, newline, ESC, bidi are **dropped**; safe names remain | any hostile name offered |
| C-03 | F | `h` at top level | `__complete h` | `hosts`; not `help` aliases and not deprecated commands | deprecated alias listed |
| C-04 | F | fake API answers after 3 s | `__complete hosts show ''` | returns within 1 s with no candidates, **nothing printed to stderr** | blocks; error text printed |
| C-05 | F | not signed in / expired session | `__complete hosts show ''` | no candidates, no stderr, exit 0 | prompts; prints error |
| C-06 | F | `--on t`, then `--on tier=` | | `tier=` then `tier=db`, `tier=web` from host tags; `environment=` offered | values from other servers |
| C-07 | F | `run r-` | | at most 20 recent IDs with descriptions | more than 20; shows hidden arguments to a reader |
| C-08 | F | `completion install --dry-run` for zsh with and without a user-owned `fpath` dir | prints the path and, when no dir, the two `.zshrc` lines; **no file written, no rc file touched** | any write |
| C-09 | F | `completion install --yes` bash | writes only `${XDG_DATA_HOME:-~/.local/share}/bash-completion/completions/machinarchy` (0644), nothing else | wrote elsewhere; `sudo` |
| C-10 | F | completion makes no mutating request | assert the fake records GETs only | any non-GET |
| C-11 | L | bash and zsh in a container | press Tab after `machinarchy h`, `machinarchy hosts show we` | `hosts`; host names listed | no list |
| C-12 | F | `--server B` typed before the cursor | | candidates come from B only if B has a saved credential | uses A's credential for B |

## H. Host lifecycle and wizards

| ID | L | Initial state | Command | Expected | Exit | Fails if |
|---|---|---|---|---|---|---|
| H-01 | F | no hosts | `hosts add web-01 10.0.0.5 --access deploy --no-input` | host registered; status `waiting for key approval`; prints the exact `hosts trust` command; no trust sent | 0 | trusts |
| H-02 | F | same + `--trust-fingerprint` equal to the observed key | | trusts; reaches ready | 0 | trusts a different key |
| H-03 | F | `--trust-fingerprint` not observed | | refuses to trust; says which keys were observed | 2 | trusts |
| H-04 | F | policy `tofu` (key auth) | `hosts add` | no prompt; line "trusted automatically (lab setting)" | 0 | prompt shown for tofu |
| H-05 | F | wizard, user presses Enter at the trust question | | host left `waiting for key approval`; Next printed | 0 | trusted |
| H-06 | F | wizard back-navigation | type `back` at each question | returns to the previous question without side effects (no request has been sent before the confirm point) | 0 | request sent early |
| H-07 | F | wizard prints the non-interactive twin | | the printed command, run with `--no-input`, produces the same host | 0 | differs |
| H-08 | F | agent host, controller without gateway (503) | `hosts add db-01 --agent` | stops before creating anything | 4 | creates the host |
| H-09 | F | `hosts enroll db-01 --revoke-old` | | revokes earlier unused tokens via `DELETE`; prints the new token once | 0 | revokes a used token |
| H-10 | F | `hosts revoke-agent db-01` before C-09 | | requires `--agent-id`; names `GET /api/v1/agents` source | 2 | guesses an id |
| H-11 | F | `hosts tag` | `hosts tag web-01 tier=web canary-` | label PATCH with the merged set and the host revision | 0 | drops other tags |
| H-12 | F | `hosts tag web-01 'bad key=1'` | | rejected locally with the label pattern | 2 | request sent |

## U. Usability and wording checks (static, run in CI)

| ID | Check | Fails if |
|---|---|---|
| U-01 | Every error ID in the `msg` catalogue has a `Next` line, and the four-part form is used by `reportError` | any error without `Next` |
| U-02 | Every command's `--help` ends with an `Examples:` block and every example line parses as a valid invocation (flags and arguments accepted by the command's own parser) | missing block; example that fails to parse |
| U-03 | The forbidden words (cluster, context, operation, selector, connector, supervisor, "Access Profile", "profile", label) do not appear in any non-hidden command's help, prompts or messages; allowed only in hidden/deprecated commands and in JSON | any occurrence |
| U-04 | Lore appears only in the allowed messages (`07-brand.md` 2.1); never in error, warning, security or prompt kinds | any other place |
| U-05 | `--plain` output contains only ASCII, no ANSI sequences, no `\r` | non-ASCII or ANSI |
| U-06 | Each success message ends with a `Next:` line where the catalogue defines one | missing |
| U-07 | Wrapping: at widths 60, 80, 120 no line exceeds the width except unbreakable tokens (IDs, fingerprints, commands) | overflow |
| U-08 | Symbols are always accompanied by words | a bare symbol in a table cell |
| U-09 | Every deprecation line names a replacement that exists | dangling replacement |
| U-10 | The home screen suggestion priority of `03-ux.md` section 1 | wrong suggestion for each state fixture |
| U-11 | Import graph obeys `05-architecture.md` section 2 | a forbidden import |
| U-12 | `go test ./internal/cli/...` coverage does not fall below today's per-package figures (cli 87.1 %, api 75.5 %, clierr 88.2 %, commands 80.9 %, config 68.0 %, output 92.0 %) and the new pure packages (`sanitize`, `target`, `group`, `msg`) reach at least 90 % | below threshold |

## Z. Full acceptance run (M6)
Run A, B, S, O, G, N, K, C, H, U in full in CI (F, G, U layers on every pull request; P and L layers nightly or on release candidates), plus a recorded usability session of A-01 and A-02 with two people who have never used Machinarchy. The release is blocked by any failure of S-series, K-04, G-04, G-05 or A-01.
