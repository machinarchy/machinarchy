# 02 - Commands

> Target state. "API" lines cite verified endpoints (`06-api-matrix.md`). "needs C-nn" means the controller must change first; until it does the CLI refuses with the message given, it never fakes the feature.

## 1. Command tree

Ten commands are listed in `--help`. `version` exists but is hidden (`machinarchy --version` is the documented form).

```
machinarchy                                   home screen (state-aware)
machinarchy login [SERVER-URL] [--with-token]
machinarchy logout [--everywhere]
machinarchy whoami
machinarchy switch [SERVER]
machinarchy tokens | tokens create NAME | tokens revoke NAME
machinarchy envs | envs add NAME | envs use NAME | envs remove NAME
machinarchy hosts | hosts add NAME ADDRESS | hosts show NAME | hosts tag NAME TAG... |
                   hosts move NAME --env ENV | hosts trust NAME | hosts enroll NAME |
                   hosts revoke-agent NAME | hosts remove NAME
machinarchy access | access add NAME | access show NAME | access remove NAME
machinarchy run --on TARGET [--except TARGET] -- PROGRAM [ARG...]
machinarchy run --on TARGET --do CAPABILITY [KEY=VALUE...]
machinarchy run | run ID | run ID --host NAME | run ID --follow | run ID --retry-failed | run ID --cancel
machinarchy admin settings ... | admin secrets ...
(hidden) machinarchy config path | config show | version
(hidden, documented) machinarchy completion bash|zsh|fish|install
```

Help groups: **Get started** (`login`, `hosts`, `run`), **Everyday** (`access`, `envs`, `tokens`), **Account** (`whoami`, `switch`, `logout`), **Advanced** (`admin`).

## 2. Conventions shared by every command

### 2.1 Global flags

| Flag | Env | Meaning |
|---|---|---|
| `--server NAME-OR-URL` | `MACHINARCHY_SERVER` | Use a saved server, or a URL for this one command. A saved credential is only ever sent to the server it was saved for; a URL that is not saved needs `MACHINARCHY_TOKEN` or `--with-token` input (rule R3, `04-security.md`). Old `--context` is a hidden alias for a saved server name. |
| `--env NAME` | `MACHINARCHY_ENV` | Scope to an env for this command. `--env any` removes the scope. Old `-n` is dropped. |
| `-o, --output table\|json\|yaml` | `MACHINARCHY_OUTPUT` | Output format. |
| `-q, --quiet` | | Identifiers only, one per line, where it makes sense. |
| `--no-input` | `MACHINARCHY_NO_INPUT=1` | Never prompt; fail with the missing flag named. |
| `-y, --yes` | (never an env var) | Answers a plain `[y/N]` confirmation. Never satisfies host-key trust, authentication, or a protected-env confirmation. |
| `--confirm-env ENV` | | Non-interactive answer to a protected-env confirmation. Must equal the env name. |
| `--confirm NAME` | | Non-interactive answer to a type-the-name confirmation. |
| `--dry-run` | | On every command that changes something: print the preview and what would be sent, exit 0, make no mutating request. |
| `--plain` | `MACHINARCHY_PLAIN=1` | No lore, colour, symbols or live redraw. `NO_COLOR` and a non-UTF-8 locale imply it for colour and symbols. |
| `--request-timeout DURATION` | `MACHINARCHY_REQUEST_TIMEOUT` | Per request, default 30 s. |
| `-v, --verbose` | | Debug log on stderr. Never contains credentials. |

The old `MACHINARCHY_CLI_*` variables keep working for one release and print a deprecation line on stderr (`08-migration.md`).

### 2.2 Output rules

- **stdout is data, stderr is everything else** (prompts, progress, warnings, logs).
- `table` is the default on a terminal and for lists; `json` and `yaml` use the API's field names and are the stable contract (`09-acceptance-tests.md`, automation).
- Every text printed from the server or from a remote host passes through the sanitiser (`04-security.md`, T8) before it reaches a terminal. `--raw` on `run ID --host` bypasses it only when stdout is not a terminal.
- A state-changing command begins with the context line `[SERVER · ENV]` on stderr, for example `[company · production]`. It is also printed when the env is "none".

### 2.3 Errors

Every error has four parts, in this order: **what happened**, **why** (when known), **what to do**, and the exact next command.

```
Error: could not sign in to company.example (HTTP 401).
Why:   the saved session expired at 2026-11-26 09:14.
Next:  machinarchy login
```

Exit codes: `0` ok, `1` unexpected, `2` usage, `3` authentication or permission, `4` server or network, `5` local wait deadline elapsed while work continues (detached follow with `--wait-timeout`), `6` not found, `7` conflict or precondition, `8` run finished with some hosts failed, `9` run finished with every host failed, `130` interrupted. Codes 0-7 and 130 keep their current meaning (`docs/cli/commands.md`).

### 2.4 Target grammar for `--on`

| Form | Meaning | API |
|---|---|---|
| `web-01,web-02` | named hosts | `include` |
| `web-*` | name glob, expanded by the CLI over the host list; the resolved names are shown and bound by the preview hash | `include` |
| `tier=web,region in (eu,us)` | tag selector (same grammar as the controller) | `selector` |
| `all` | every host in scope | `all` or `selector environment=ENV` when an env is active |
| `--except web-02,web-03` | remove hosts | `exclude` |

A single `--on` value is either all names or all selector terms, never a mix. The active env is added as `environment=ENV` to the selector. `--on environment=production` is allowed; `env=` is not rewritten (D12).

### 2.5 IDs

Hosts: name or ID. Runs: the controller's operation ID, or any unique prefix of at least 6 characters, optionally written `r-PREFIX`; the CLI shows the first 8 characters as `r-xxxxxxxx`. The ID format is not stated in the API contract; if the controller later adds a short ID (C-10) the CLI uses it. Prefix resolution searches the 500 most recent runs (`GET /api/v1/managed/operations?limit=500`).

### 2.6 Credentials in play

Precedence for the credential used by a command: `MACHINARCHY_TOKEN` -> saved API token of the active server -> saved session of the active server. If a command needs an owner and the credential is a token, it fails before any request with:

```
Error: this needs an owner signed in with a password; you are using an API token (ci-deploy, operator).
Next:  machinarchy login
```

---

## 3. Command contracts

### 3.1 `machinarchy` (home screen)

| | |
|---|---|
| Purpose | Show where you are and the one next step. |
| Arguments | none. `-o json\|yaml` prints the same data. |
| Interactive | Prints the home screen (`03-ux.md` section 1) and returns. It never prompts. |
| Non-interactive | Same content, plain, no live redraw. Not signed in: exit 3 and the sign-in line, so scripts can test. |
| Permissions | any signed-in role. |
| API | `GET /api/v1/me`, `GET /api/v1/managed/hosts?limit=...` (health counts), `GET /api/v1/managed/operations?limit=5`, `GET /api/v1/sessions` for the session expiry (human sessions only). |
| Output | Server, env, user and role, host counts by readiness, last runs, one suggested next command. |
| Errors | Unreachable server: shows what is cached locally (server name, URL) and `Next: machinarchy switch` or retry. Expired credential: the sign-in line. |

### 3.2 `login`

| | |
|---|---|
| Purpose | Add a server, sign in, and make it active. |
| Usage | `login [SERVER-URL] [--with-token] [--name NAME] [--email EMAIL] [--password-stdin] [--stay-signed-in\|--no-stay-signed-in]` |
| Interactive | Asks, in order: server URL (if not given), name for this server (default derived from the host name), "Email and password" or "API token", then the credentials. Password and token are read without echo. After a password sign-in on a terminal it asks "Stay signed in on this computer?" (default yes). `Ctrl+C` or `back` on an empty line returns to the previous question. |
| Non-interactive | `SERVER-URL` required. Password: `--email` plus `--password-stdin`. Token: `--with-token` reads the token from stdin. No question is ever asked; a missing value names the flag. `--stay-signed-in` is off unless given. |
| Validation | Token: `GET /api/v1/me` with the token before anything is saved (it accepts both session and API tokens, `middleware/auth.go:32`). Password: `POST /api/auth/login` (`email` or `username`). |
| Saved | Server name and URL, and the credential in the credential store (`05-architecture.md`). The device token (see below) is saved too. The password is never saved. |
| Shows | User, role, and expiry. Session expiry comes from `GET /api/v1/sessions` (the item with `current: true`, field `expiresAt`). Token role and expiry are **not** returned by `/me`; until C-03 ships the CLI shows `role: not reported by this server` and `expires: unknown` for a token rather than guessing. |
| Stay signed in | Creates an API token named `device-HOSTNAME-DATE`, role **operator**, 90 days (`POST /api/v1/tokens`, human session, max 365 d), saves it for this server, and keeps the session as well. Because a token cannot do owner actions (`tokens/routes.go:13`, `agents/routes.go:16`), the CLI uses the session for owner commands while it lasts and the token afterwards. When the session has expired and an owner action is needed the CLI says so (section 2.6). |
| Permissions | none. A device token can only be created by a human session. |
| Errors | Wrong password: `HTTP 401 Invalid credentials`, "Check the email and password" and the `login` command again; the response does not say which of the two was wrong, and neither does the CLI. Throttled (HTTP 429): shows the wait. Non-loopback `http://`: refused, "Use https://", and `--insecure-http` only on `login` for lab networks, per server, remembered per server. Self-signed certificate: see `04-security.md` T6; `--ca-file FILE` is accepted on `login` and saved per server. Server not Machinarchy: "that address did not answer like a Machinarchy server". |

### 3.3 `logout [--everywhere]`

| | |
|---|---|
| Purpose | End this computer's access to the active server. |
| Behaviour | Session credential: `POST /api/auth/logout` (revokes the session on the server), then removes the local copy. Token credential: removes the local copy only, and says the token still works until revoked (`tokens revoke`); a token cannot be revoked by itself (`RequireHuman`). `--everywhere`: `DELETE /api/v1/sessions`, needs a session; ends every login session of the user but not API tokens. |
| Interactive | `[y/N]` for `--everywhere`. |
| Non-interactive | `--yes` for `--everywhere`. |
| Errors | Server unreachable: the local copy is removed, and the CLI says "the session may still be active on the server" with `machinarchy login` then `logout` as the retry. |

### 3.4 `whoami`

Prints server, env, user, auth method (password session, device token, token from the environment), role, and expiry where known. API: `GET /api/v1/me`, `GET /api/v1/sessions`. Exit 3 when not signed in. `-o json` is stable.

### 3.5 `switch [SERVER]`

Lists saved servers and makes one active. No argument on a terminal: a numbered list. No argument without a terminal: prints the list. Local only; no API call. `--env` is remembered per server. Errors: unknown name lists the known ones.

### 3.6 `tokens`

| Command | Behaviour | API | Permission |
|---|---|---|---|
| `tokens` | Lists the user's API tokens (name, role, created, expires, revoked). | `GET /api/v1/tokens` | session of any role with token rights (owner; `PermissionTokenManage`) |
| `tokens create NAME --role reader\|operator [--expires 90d]` | Creates a token. **The token is printed once**, on stdout alone when `-q`, otherwise with a one-time notice. Default expiry 90 d (server default is 30 d; max 365 d). | `POST /api/v1/tokens` | owner session |
| `tokens revoke NAME` | Finds the token by name in the list, confirms (type the name), revokes. | `DELETE /api/v1/tokens/{id}` | owner session |

A token is never accepted as an argument. `tokens create` never writes a token to a file unless `--out FILE` is given (mode 0600, refuses to overwrite). Duplicate names: the CLI lists them with ids and asks for `--id`. Errors: API token used: section 2.6.

### 3.7 `envs`

| Command | Behaviour | API | Permission |
|---|---|---|---|
| `envs` | List envs, mark the active one, show host counts. | `GET /api/v1/environments`, host list | any |
| `envs add NAME [--description T] [--protected]` | Create an env. `--protected` needs C-02; before that, `--protected` offers the local-only guard (`04-security.md` section 6) after saying it is not enforced by the server. | `POST /api/v1/environments` | operator or owner (`inventory:write`) |
| `envs use NAME` | Make it the active env for this server. Local. `envs use any` clears it. | `GET /api/v1/environments` to validate | any |
| `envs remove NAME` | Confirms by typing the name, then deletes. | `DELETE /api/v1/environments/{id}` | operator or owner |

What the server does when an env still has hosts is **not verified**; the CLI passes the server's answer through with a next step.

### 3.8 `hosts`

#### `hosts [--env ENV] [--on TARGET] [--status ready|attention|all]`
Lists hosts with the same table everywhere: `NAME  ENV  ADDRESS  HOW  STATUS  TAGS`. `HOW` is `ssh` or `agent`. `STATUS` is the plain readiness (section 3.8.1). API: `GET /api/v1/managed/hosts` (`selector`, `environment`, `phase`, `cursor`, `limit`).

#### 3.8.1 Readiness mapping
Derived only from the host's conditions (`status.conditions`, types `Ready`, `Reachable`, `Configured`) and `status.phase`:

| Plain status | Rule |
|---|---|
| `ready` | `Ready` condition status is `True` |
| `waiting for key approval` | `Ready` reason is `HostKeyPendingApproval` |
| `key changed` | `Ready` reason is `HostKeyChanged` |
| `checking` | phase `Pending` or `Ready` reason `NotObserved` |
| `cannot connect` | `Ready` reason `ConnectionFailed` or `TargetNotAllowed` (controller network policy; the message names the setting) |
| `needs attention` | any other `Ready` not `True`; the raw reason is shown by `hosts show` |

The reason strings are the ones in the codebase today (`hostaccess`, `trust.go:51`); the CLI shows the raw reason in `hosts show` and in `-o json` so nothing is hidden.

#### `hosts add NAME ADDRESS`
| | |
|---|---|
| Purpose | Bring one machine under management. |
| Arguments | `NAME` host name. `ADDRESS` is `HOST[:PORT]` for SSH (default port 22). With `--agent`, `ADDRESS` is omitted. |
| Flags | `--access NAME` (existing access), `--user LOGIN`, `--key-file FILE` or `--password-stdin` (to create access on the way), `--agent`, `--env ENV`, `--tag K=V` (repeatable), `--trust-fingerprint SHA256:...` (pin), `--wait DURATION` (default 90 s). |
| Interactive | Section `03-ux.md` 2. Picks or creates access, registers the host, waits for the controller to observe the host key, shows the fingerprint with a one-line explanation, asks to trust it, waits for `ready`. For `--agent`: creates or picks an agent access, registers the host, issues the one-time enrolment token, prints the install command. |
| Non-interactive | Needs `--access`. SSH hosts: does not trust anything unless `--trust-fingerprint` is given and equals an observed key; otherwise registers the host and exits 0 with `status: waiting for key approval` and the exact `hosts trust` command. `--yes` has no effect on trust. |
| Permissions | **owner** (binding a host to an Access Profile is owner-only, `service/managed_resources.go:50`). |
| API | `GET /api/v1/managed/access-profiles`, `POST /api/v1/managed/access-profiles`, `POST /api/v1/secrets`, `POST /api/v1/managed/hosts`, `GET /api/v1/managed/hosts/{id}` (poll), `POST /api/v1/managed/hosts/{id}/trust`, `POST /api/v1/agents/enrollment-tokens`, `GET /api/v1/agents/controller-key`. Env assignment needs C-01; agent install details need C-07. |
| Output | Success line, status, next command. `-o json` returns the host and, for agents, the enrolment token once. |
| Errors | Duplicate name (HTTP 409 `already_exists`): "A host named web-01 already exists" and `hosts show web-01`. Address invalid: refused before any request. Unreachable: the host is registered, status `cannot connect`, with the controller's reason and "Next: check that web-01:22 is reachable from the controller". Controller cannot support agents (HTTP 503 from controller-key): "this server has no agent gateway". |
| Not done by this command | It never connects to the machine itself and never reads its host key from the user's computer: the fingerprint shown is what **the controller** observed, which is the connection that will actually be used (`04-security.md` T4). |

#### `hosts show NAME`
Readiness, address, access, env, tags, observed and trusted keys, last runs for the host. API: `GET /api/v1/managed/hosts/{id}`, `GET /api/v1/managed/operations?host=ID&limit=5`.

#### `hosts tag NAME TAG...`
`tier=web` sets, `canary-` removes. Sends the full label set with the host's revision (compare and swap). API: `PATCH /api/v1/managed/hosts/{id}/labels`. Tag keys and values follow the controller's pattern (`domain/labels.go:20`); the CLI validates before sending. Errors: stale revision (409): "the host changed, rerun".

#### `hosts move NAME --env ENV`
Changes the env. **Needs C-01.** Until then: `Error: this server cannot move hosts between envs yet.` Protected source or target env: typed confirmation (`04-security.md` section 6).

#### `hosts trust NAME [--replace] [--fingerprint SHA256:...] [--all-pending]`
| | |
|---|---|
| Purpose | Approve the SSH host keys the controller observed. |
| Interactive | Prints the observed keys with the explanation; asks "Do the fingerprints match what the machine reports? Type yes to trust it". A changed key still asks for the first 8 characters. A changed key (`--replace`) uses the stronger flow in `03-ux.md` section 8. |
| Non-interactive | `--fingerprint` is required and must equal one of the observed keys. `--yes` is ignored for trust. `--all-pending` is refused with `--no-input` (it would trust unseen keys); use one `--fingerprint` per host. |
| Permissions | owner. |
| API | `GET /api/v1/managed/hosts/{id}`, `POST /api/v1/managed/hosts/{id}/trust` (`generation`, `fingerprints`, `replace`). |
| Errors | No key observed yet: wait or retry; key not in the observed set: refuses. |

#### `hosts enroll NAME [--ttl 1h]`
Issues a new one-time enrolment token for an existing agent host and prints the install command. Owner. API: `POST /api/v1/agents/enrollment-tokens` (`hostId`, `ttlSeconds` 60-86400 as in the current CLI). The token is shown once. Flags: `--ttl`, `--token-out FILE` (mode 0600, refuses to overwrite), `--revoke-old` (also revokes this host's earlier unused tokens, `DELETE /api/v1/agents/enrollment-tokens/{id}`), `--show-controller-key` (prints `GET /api/v1/agents/controller-key`), and the hidden `--list` used by the `get agent-tokens` alias.

#### `hosts revoke-agent NAME`
Revokes the agent's **identity** so it can no longer serve commands (`POST /api/v1/agents/{id}/revoke`, owner). Finding the agent ID from the host name is **not verified** (the list response's shape is not in the API contract); until C-09 confirms it the command takes `--agent-id ID` copied from `GET /api/v1/agents`. Type-the-name confirmation.

#### `hosts remove NAME`
Removes the host from the **inventory only** (`DELETE /api/v1/managed/hosts/{id}?revision=N`; refused with 409 while a run targets it). It does not uninstall an agent, does not revoke the agent identity, does not retire credentials, and does not touch the machine. The output says exactly that and lists the separate commands (`03-ux.md` section 6). Type-the-name confirmation; protected env: type the env name.

### 3.9 `access`

| Command | Behaviour | API | Permission |
|---|---|---|---|
| `access` | Lists access entries: name, how (`ssh` or `agent`), user, hosts using it. | `GET /api/v1/managed/access-profiles` | any |
| `access add NAME` | Wizard: how the controller reaches machines (`ssh with a key`, `ssh with a password`, `agent`), login user, the key or password (hidden prompt, or `--key-file`, `--password-stdin`), the host-key policy in plain words ("ask me to approve keys" (default), "trust on first use (lab only)", "only keys I pin"). Creates the credential secret and the entry. Flags: `--how ssh-key\|ssh-password\|agent`, `--user`, `--key-file`, `--password-stdin`, `--host-key-policy approve\|tofu\|pinned`. | `POST /api/v1/secrets`, `POST /api/v1/managed/access-profiles` | owner |
| `access show NAME` | Shows how it works and which hosts use it. Never shows credentials. | `GET /api/v1/managed/access-profiles/{id}` | any |
| `access remove NAME` | Type-the-name confirmation. Refused by the server while hosts use it. Does **not** retire the credential secret; says so and prints `admin secrets delete`. | `DELETE /api/v1/managed/access-profiles/{id}` | owner |

`tofu` is only offered with key authentication and prints "a lab setting: the first key seen is trusted" (`docs/operations.md`: `tofu` is allowed only with private-key authentication).

### 3.10 `run`

| Form | Behaviour |
|---|---|
| `run --on T [--except T] -- PROG [ARG...]` | Execute a program (no shell, argument list). Preview, confirm, submit, then **wait by default** and show grouped output. |
| `run --on T --do CAP [KEY=VALUE...]` | Execute an agent capability on agent hosts. `KEY=VALUE` are string inputs sent as one JSON object; `--input-json '{...}'` for non-string values. Names are passed through; the CLI help lists the four known today (`runtime.info`, `system.facts`, `service.status`, `service.control`) and says the server decides (no catalog endpoint exists, C-08). Values are sent verbatim: the CLI does not add `.service` or any other suffix. |
| `run` | List recent runs (`GET /api/v1/managed/operations`, `state`, `host`, `createdAfter`, `limit`; `--state`, `--host`, `--limit`). |
| `run ID` | Show one run: grouped result (`03-ux.md` section 7). |
| `run ID --host NAME [--stream stdout\|stderr]` | Full output of one host (`GET .../targets/{hostId}/output`), complete and ungrouped. |
| `run ID --follow` | Watch a running run (section 7.7). Follow of a single host is the same with `--host`. |
| `run ID --retry-failed` | New run on the hosts that failed with a known outcome. Semantics in section 3.10.3. |
| `run ID --cancel` | Ask the controller to cancel. Semantics in section 3.10.4. |

Flags: `--timeout DURATION` (default 60 s; up to 24 h; sent as seconds), `--concurrency N` (1-20, default 5 attached, 20 detached), `--detach` (return after submit), `--wait-timeout DURATION` (stop watching after this long, exit 5), `--no-groups` (one block per host), `--groups N\|all` (show up to N groups, default 10), `--fail-on-drift` (exit 8 when succeeded hosts printed different output), `--include-unknown` and `--include-cancelled` (with `--retry-failed`), `--raw` (with `--host`; only when stdout is not a terminal, otherwise refused), `--no-wait` (with `--cancel`), `--env`.

Waiting: attached when `--timeout` is at most 15 minutes (the controller's attached limit, `maxAttachedTimeout`); above that the CLI says "this run is long, it runs detached; follow it with `run r-xxxx --follow`" and returns after submit (decision 8).

#### 3.10.1 Interactive and non-interactive
Interactive: prints the blast-radius preview (`03-ux.md` section 5) and asks. Non-interactive: needs `--yes`; in a protected env also `--confirm-env ENV`; `--no-input` without them exits 2 naming the flag. The preview is always printed to stderr, also with `--yes`.

#### 3.10.2 Permissions and API
`runs:execute` (operator, owner) to submit or cancel; readers can list and see states and exit codes but not output or arguments. API: `POST /api/v1/managed/operations/preview`, `POST /api/v1/managed/operations` with `expectTargetsHash`, `GET /api/v1/managed/operations/{id}` (poll), `GET .../targets/{hostId}/output`, `POST .../cancel`. More than 200 hosts: refused before the preview is shown, "A run can target at most 200 hosts. Narrow it with --on" (C-05 raises this).

#### 3.10.3 `--retry-failed`
Creates a **new** run (new ID) with the same program or capability on the hosts of run ID whose target ended `Failed` or `TimedOut`. Excluded and reported, never silently: `Lost` and `Interrupted` (the controller says the outcome is unknown and never retries them), `Cancelled`, `Succeeded`. `--include-unknown` and `--include-cancelled` add them, with a warning that the command may already have run there. The program is re-read from the original run (visible to operators and owners only). The preview, confirmation and protected-env rules apply as for any run. The CLI does **not** claim the original run is "fixed": it stays as it was. The controller offers no retry endpoint and no idempotency key (no `Idempotency-Key` in the API contract), so a retry is just another submission; if the connection drops during submit the CLI reports "unknown whether the run was created" and shows `machinarchy run` as the way to check (`03-ux.md` section 9).

#### 3.10.4 `--cancel`
Sends `POST .../cancel` and reports the controller's state, usually `CancelRequested`. It then follows until a terminal state, or exits at once with `--no-wait`. It never says "stopped" before the controller does. What a cancel guarantees is in `03-ux.md` section 9.

#### 3.10.5 Exit codes of `run`
0 every host succeeded; 8 some hosts failed; 9 every host failed; 5 `--wait-timeout` elapsed; 130 interrupted; 2 usage; 3 not permitted; 7 `targets_changed` (the selection changed between preview and submit; nothing ran). A detached submit exits 0 after the run is created.

### 3.11 `admin`

| Command | Maps to | API | Permission |
|---|---|---|---|
| `admin settings get\|history\|status\|validate\|publish\|rollback` | `runtime-config ...` | `/api/v1/runtime-config*` | owner session |
| `admin secrets list\|create\|versions\|rotate\|rewrap\|delete` | `secret ...` | `/api/v1/secrets*` | owner session |

Behaviour is unchanged from today. Secret values come from stdin only (`--value-stdin`) or a hidden prompt. These commands are not on the beginner path and are never mentioned in the home screen.

### 3.12 Hidden: `config path`, `config show`, `version`

`config path` prints the config file location. `config show` prints it with every credential replaced by `***`. `version` prints version, commit, build time, Go version and platform (`machinarchy --version` prints the first line). The old `config set-cluster|set-credentials|set-context|use-context|get-contexts|current-context` stay as deprecated aliases for one release (`08-migration.md`).

---

### 3.13 `completion` (hidden from the list, documented, `--help` footer points to it)

| | |
|---|---|
| Purpose | Tab completion like git: type a prefix, press Tab, pick from the list. |
| Usage | `completion bash\|zsh\|fish` prints the script to stdout (Cobra generator). `completion install [--shell bash\|zsh\|fish] [--dry-run]` installs it. |
| `install` behaviour | Detects the shell from `$SHELL` (override with `--shell`). **Shows the exact file it will write and the exact line it would add, then asks `[y/N]`.** bash: `${XDG_DATA_HOME:-~/.local/share}/bash-completion/completions/machinarchy` (auto-loaded by bash-completion v2). fish: `~/.config/fish/completions/machinarchy.fish` (auto-loaded). zsh: the first user-owned directory already on `$fpath`; if there is none it prints the two lines to add to `~/.zshrc` and **does not edit it**. It never edits a shell rc file and never uses `sudo`. `--dry-run` prints the plan only. Non-interactive: `--yes` accepts the file write (not an rc edit, there are none). |
| API | none when installing. Completion at the prompt uses the API (section 3.13.1). |
| Permissions | none |
| Errors | Unknown shell: lists the three supported. No writable location: prints the script path to create and the content with `completion zsh > FILE`. |

#### 3.13.1 What completes

| Position | Candidates | Source |
|---|---|---|
| command and subcommand | visible commands only (`h` -> `hosts`); hidden and deprecated aliases are not offered | static |
| flags | flags of the command, with a one-line description | static |
| `hosts show\|tag\|move\|trust\|enroll\|revoke-agent\|remove` NAME | host names in the active env, description = readiness | `GET /api/v1/managed/hosts` |
| `--on`, `--except` | host names; tag keys as `tier=`; after `key=` the values seen in the host list; `environment=` | host list |
| `--env`, `envs use\|remove` | env names | `GET /api/v1/environments` |
| `--access`, `access show\|remove` | access names | `GET /api/v1/managed/access-profiles` |
| `switch` | saved servers | local config, no request |
| `tokens revoke` | token names | `GET /api/v1/tokens` (session only) |
| `run` ID | the 20 most recent run IDs, description = program and result | `GET /api/v1/managed/operations?limit=20` |
| `run ID --host` | hosts that are targets of that run | `GET /api/v1/managed/operations/{id}` |
| `--do` | `runtime.info`, `system.facts`, `service.status`, `service.control`, marked "known; the server decides" | static (no catalog endpoint, C-08) |
| enumerated flags (`--role`, `--output`, `--stream`, `--how`, `--host-key-policy`) | their fixed values | static |

#### 3.13.2 Rules for dynamic completion
1. **Never prompts, never writes, never asks for a password**; read-only requests only.
2. **Total budget 800 ms** per Tab press (including the request). On timeout, any error, no credential, or an expired session it returns no candidates silently: nothing is printed to the terminal (no `Error:` line on the user's prompt line) and file-name completion is **not** offered as a fallback for these positions.
3. Uses the same credential and server as a normal command (R3 applies) and never sends `confirmEnvironment`.
4. Every candidate and description is sanitised and must match a conservative pattern (letters, digits, `. _ - : / = @ , + ~ %`) before it is offered; anything else is dropped. This keeps a hostile host name from becoming a shell metacharacter or an injected command on the user's command line (`04-security.md` T16).
5. At most 200 candidates are returned; the rest are available by typing more characters.
6. Completion honours the active env scope and `--server`/`--env` already typed on the line.
7. No on-disk cache in V1 (a cache of host names would be a new sensitive file). If latency proves too high the cache is a separate decision.

## 4. Decisions on the open items

| Open item | Decision | Reason |
|---|---|---|
| `status` vs bare `machinarchy` | Bare `machinarchy` is the home screen. No `status`. | D6 |
| Is `access` under `hosts`? | No: top-level. | D7 |
| Where does agent enrolment live? | `hosts add --agent` and `hosts enroll`; identity revocation is `hosts revoke-agent`. | D8 |
| `switch` vs `servers use` | `switch`. | D9 |
| Friendly form of capabilities | `--do CAP key=value...`, `--input-json` as escape. | D10 |
| How are envs created? | `envs add NAME [--protected]`, then `hosts add --env` / `hosts move --env` (C-01). | D11 |

## 5. What the contract deliberately does not do

- No command takes a password, token or secret as a flag value. `--with-token`, `--password-stdin`, `--value-stdin` and hidden prompts only.
- No command combines inventory removal, agent uninstall, credential revocation and identity revocation (`03-ux.md` section 6).
- No command guesses a host-key fingerprint, an env, or a target.
- No command prints lore in an error.
