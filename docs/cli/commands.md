# CLI commands and output

Run `machinarchy --help` or `machinarchy COMMAND --help` for the built-in Cobra help. The examples below use `go run ./cmd/machinarchy`; substitute `./bin/machinarchy` after building.

## Help and typos

`machinarchy --help` lists the commands in four groups (Get started, Everyday, Account, Advanced) and points at tab completion. Every help screen ends with `Examples:` that parse with the command's own flags. A mistyped command, at any depth, names the closest command and prints the whole corrected command line:

```
$ machinarchy host add web-01
Error: unknown command "host" for "machinarchy"
Why:   "hosts" is the closest command.
Next:  machinarchy hosts add web-01
```

Nothing is run for you; with nothing close, `Next:` points at `--help`.

## Home screen (`machinarchy` with no command)

Bare `machinarchy` prints where you stand and the one next step. It never prompts. It asks the server for who you are, your hosts (in the active env, first 500 counted) and your last two runs, all at the same time, each limited to 3 seconds; a request that fails or is slow turns into one `not available` line and the rest of the screen still prints. Nothing else is sent, and the saved credential only goes to its own server.

```text
machinarchy · company · production                          maxim (owner)
Session ends in 21 h

Hosts      12 ready · 1 waiting for key approval · 1 cannot connect
           web-14  waiting for key approval
           web-09  cannot connect
Last runs  r-4k2p9x1a  ✔ 14 ok            2 min ago
           r-8d1q0b7c  ⚠ 11 ok, 3 failed  1 h ago

  Next:  machinarchy hosts trust web-14
```

The next step is the most urgent one: `hosts show NAME` for a changed host key, then `hosts trust NAME`, then `hosts show NAME` for a host that cannot connect, then `hosts add` when there are no hosts, otherwise an example `run --on TAG -- uptime` using a real tag of your hosts (or `--on all`). Not signed in, an expired or refused credential, and an unreachable server each get their own short screen. With `--plain` or a non-UTF-8 terminal the symbols become words (`OK`, `WARN`) and the flavour line is dropped.

`machinarchy -o json` prints the same facts as one document with a `state` of `not_signed_in`, `expired`, `unreachable`, `no_hosts`, `attention` or `ok`, plus `hosts`, `runs` and `next`. The role is shown for a login session (`owner`) or when the saved token has one; the session end comes from the saved credential, not the server. Exit code: 0 when signed in, 3 when not signed in or the credential is expired or refused, 4 when the server cannot be reached. "Last runs" does not show the command text (that would need one request per run).

## Listing hosts: selectors, env filter, output

`machinarchy hosts` lists the managed hosts (`GET /api/v1/managed/hosts`; the CLI follows every page). `hosts show NAME` shows one host by name or ID (see [Hosts](#hosts)); if a name is ambiguous, use the ID or narrow it with `--env`.

`-l` / `--tagged` filters by tags, for example `-l tier=web`, `-l 'role in (web,db),!canary'` or `-l name=web-*`. Built-in fields are `id`, `name`, `environment`, `phase`, `presence` and `profile`; any other key is a tag.

`--env` / `-n` accepts a case-insensitive environment name or its numeric ID. The active server may also define a default environment.

```sh
go run ./cmd/machinarchy hosts --env production
go run ./cmd/machinarchy hosts -n 3
go run ./cmd/machinarchy --server staging hosts -n production
```

The default output is a human-readable table. JSON and YAML keep the API field names and are meant for scripts:

```sh
go run ./cmd/machinarchy hosts -o json | jq '.[].metadata.name'
go run ./cmd/machinarchy hosts show web-01 -o yaml
```

## Tab completion

The first interactive `login` on a computer prints one line on stderr pointing at `machinarchy completion --help`; it is remembered in the config (`hints:`) and never printed again, and not with `--plain` or `-q`.

`machinarchy completion bash|zsh|fish|powershell` prints the completion script for your shell (the command is hidden from help). Completion offers host names (`hosts show|tag|move|trust|enroll|revoke-agent|remove`), access entry names (`access show|remove`, `hosts add --access`) env names (`--env`, `envs use|remove`; `any` for `envs use`) and, for `run`, hosts and `key=value` tags for `--on` and `--except` (the last entry of a comma list), the 20 most recent run IDs, the hosts of a run for `--host`, and the known capabilities for `--do` (marked "known; the server decides") by asking the selected server with your saved credential. It never prompts and prints no errors: with no server, no login or a slow server (3 s limit) it offers nothing. Names with spaces or control characters are never offered.

## Signing in

```sh
machinarchy login https://machinarchy.example.com          # asks what it needs
machinarchy login                                          # sign in to the active server again
printf '%s' "$PASSWORD" | machinarchy login https://machinarchy.example.com --email admin@example.com --password-stdin
printf '%s' "$TOKEN"    | machinarchy login https://machinarchy.example.com --with-token
```

`login [SERVER]` signs in with an email address (or username) and password, or with an API token (`--with-token`). On a terminal it asks for the server address, a name to save it under, the method, and the credentials; the password and the token are typed without echo. Without a terminal, or with `--no-input`, it asks nothing: the address is an argument, the password comes from stdin (`--email` and `--password-stdin`) and a token from stdin (`--with-token`), and a missing piece is named in the error. A password or a token is never a flag value.

- A **token is checked with the server before it is saved**; a token the server rejects is not saved.
- After a password sign-in on a terminal it asks "Stay signed in on this computer?" (default yes): that saves a **90-day operator device token** next to the session, named `device-<computer>-<date>`. In a script it is off unless you pass `--stay-signed-in`. Owner actions still use the password session while it lasts, because an API token can never act as the owner.
- A session lasts the server's `MACHINARCHY_SESSION_TTL` (24 hours by default); the expiry comes from the server.
- The server's role for a session or a token and a token's expiry are not reported by the API (`GET /api/v1/me` returns only the account), so the CLI prints "not reported by this server" instead of guessing.
- `--name NAME` chooses the name the server is saved under. `--insecure-http` allows plain `http://` to a host that is not on this computer (saved for that server). `--ca-file ca.pem` trusts a private CA for that server. There is no way to skip certificate verification.

```sh
machinarchy whoami            # server, user, how you are signed in, role and expiry where known
machinarchy whoami -o json
machinarchy switch            # list your servers; choose the active one
machinarchy switch staging
machinarchy logout            # end the session on the server, revoke the device token, forget both here
machinarchy logout --everywhere   # end every login session of your account (asks first; --yes answers)
```

`logout` tells the server to end the session and revokes the device token this CLI created, then removes the credentials from this computer. If the server cannot be reached the credentials are still removed and the command exits 4, saying the session may still be active. An API token you supplied yourself is only removed from this computer; it keeps working until it expires or is revoked.

## Agent enrolment tokens (owner)

```sh
go run ./cmd/machinarchy get agent-tokens
```

Lists the agent enrolment tokens (never their values): host, expiry and state, where a used token shows `used by AGENT-ID`. This is where the agent ID for `hosts revoke-agent` comes from until the server reports it per host (gap C-09). New tokens are issued by `hosts add --agent` and `hosts enroll`. The command needs an owner's login session; API tokens are refused locally, before anything is sent.

## API tokens (owner)

```sh
machinarchy tokens                                              # list: name, role, created, expires, state
machinarchy tokens create ci-deploy --role operator --expires 90d
machinarchy tokens create ci-read --role reader -q > ci-read.token   # the token alone, for a pipe
machinarchy tokens create ci-read --role reader --out ci-read.token  # or into a private file
machinarchy tokens revoke ci-deploy
machinarchy tokens revoke ci-deploy --confirm ci-deploy         # in a script
```

A token has a role, `reader` or `operator` (never owner), and an expiry: `--expires` takes days or hours (`90d`, `720h`), 90 days by default and 365 at most. **The token is shown once**, when it is created, and is never listed; `-o json` and `-o yaml` print the whole response including it. `--out FILE` writes it to a new file with mode 0600 and never overwrites an existing file; it cannot be combined with `-o json` or `-o yaml`. Use a token in a script as `MACHINARCHY_TOKEN` with `MACHINARCHY_SERVER`; it is not written to disk.

`tokens revoke` stops a token at once. It is confirmed by typing the token's name on a terminal or with `--confirm NAME`; `--yes` is deliberately not enough. When several active tokens share a name it lists their IDs and asks for `--id`. Managing tokens needs an owner's login session; an API token is refused locally before anything is sent.

## Access (how Machinarchy reaches machines)

```sh
machinarchy access                            # list: name, how (ssh-key, ssh-password, agent), user, hosts using it
machinarchy access show deploy-key            # how it works and which hosts use it (never the key)
machinarchy access add deploy-key --how ssh-key --user deploy --key-file ~/.ssh/id_ed25519
printf '%s' "$PW" | machinarchy access add lab --how ssh-password --user ops --password-stdin
machinarchy access add agents --how agent
machinarchy access remove deploy-key          # type the name; in a script: --confirm deploy-key
```

`access add` (owner) asks what it needs when run on a terminal. The key or password comes from a hidden prompt, `--key-file` or `--password-stdin`, never from a flag. It is stored as an encrypted secret on the server and is never shown again (`access show` and `-o json` hold no secret reference or value); a file that is not a private key (for example a `.pub` file) is refused. Host keys (`--host-key-policy`): `approve` (default: you approve each key), `pinned` (only keys you pin) or `tofu` (the first key seen is trusted; a lab setting, allowed only with a key). If the entry cannot be created after the secret was stored, the secret is retired again. `access remove` (owner) is confirmed by typing the name (`--yes` is not enough), is refused by the server while hosts use the entry, and does **not** retire the stored key or password: remove it with `machinarchy secret delete`.

## Environments

```sh
machinarchy envs                              # list: env, host count, protected, active
machinarchy envs add staging --description "pre-production"
machinarchy envs add production --protected   # asks first; --yes answers
machinarchy envs use staging                  # the active env for this server (local only)
machinarchy envs use any                      # clear it
machinarchy envs remove old-lab               # type the name; in a script: --confirm old-lab
```

`envs use` only changes the CLI config; the env must exist on the server. `--protected` is a **local guard**: this server cannot enforce protected environments yet, so the guard lives in the CLI config of this computer and protects you from your own typing mistakes, not from other people, other computers or other tools. It is shown as `yes (local)`. `envs remove` is confirmed by typing the env's name (`--confirm NAME` in a script, `--yes` is not enough); a locally protected env also needs `--confirm-env NAME` in a script. Removing an env clears it as the active env and drops its local guard; this command deletes no host. Every host is in exactly one env: a new server has an env called `default` (it cannot be deleted or renamed), and a host added without `--env` or an active env joins it. The server refuses to delete an env that still has hosts (move or remove them first); `hosts move` moves a host.

## Hosts

```sh
machinarchy hosts                             # NAME ENV ADDRESS HOW STATUS TAGS
machinarchy hosts --status attention          # everything that is not ready (or: ready, all)
machinarchy hosts show web-01                 # status, env, address, access, conditions, host keys, recent runs
machinarchy hosts tag web-01 tier=web canary- # KEY=VALUE sets, KEY- removes
machinarchy hosts remove web-01               # type the name; in a script: --confirm web-01
machinarchy hosts add web-01 --agent          # register an agent host and print its install blob (owner)
machinarchy hosts enroll web-01 --ttl 1h --token-out web-01.blob   # also --revoke-old, --show-controller-key
machinarchy hosts revoke-agent web-01 --agent-id ID   # type the name; --yes is not enough
```

`STATUS` is one of `ready`, `waiting for key approval`, `key changed`, `checking`, `cannot connect` or `needs attention`, worked out from the host's conditions (`hosts show` and `-o json` show the raw reasons). `HOW` is `ssh` or `agent` (from the access entry). Tags follow the controller's rules (letters, digits, `.`, `_`, `-`; keys may also hold `/`; at most 63 characters; `name`, `id`, `status`, `environment` and a few more are reserved); the CLI checks them before sending, and a host that changed meanwhile is reported as "the host changed", not overwritten. `hosts remove` takes the host out of the inventory **only**: an installed agent is not uninstalled or revoked, the access entry and credentials are kept, and the machine is not touched; the output says so. A host in a locally protected env (`envs add --protected`) also needs the env's name (typed, or `--confirm-env ENV` in a script) for `tag` and `remove`. `hosts move NAME --env ENV` moves a host to another env (it keeps its name, tags and access); a locally protected source or target env needs its name typed (or `--confirm-env` in a script), and against a server that predates envs on hosts it reports that the host did not move. `hosts trust NAME` (owner) is the only way to approve SSH host keys (the old `trust host` is gone). First trust: the CLI shows the keys the controller saw and you compare the key with the machine and type `yes`; Enter skips. In a script use `--fingerprint SHA256:...`, which must be a key the controller saw; all seen keys are trusted together (the controller refuses an incomplete set). `--yes` never approves a key, and without a terminal and without `--fingerprint` nothing is trusted. A host that already trusts keys is changed only with `--replace`: the old and new keys are shown, you type the first 8 characters of the new key and then the host's name (`--confirm NAME` in a script); a locally protected env also needs its name. `hosts trust --all-pending [-l TAGS]` asks about each waiting host in turn, needs a terminal and is refused with `--no-input`. `hosts add [NAME [ADDRESS]]` (owner) brings a machine reached over SSH under management. Without arguments it asks (name, address, how to sign in: a new key, a new password or an existing access entry, then the host-key policy for a new entry); `back` returns to the previous question and nothing is sent to the server until every answer is in. Then it registers the host, waits (`--wait`, default 90 s, `0` = do not wait) for the server to see the host's SSH key, shows the key **the server saw** and asks you to type `yes`; Enter skips and leaves the host `waiting for key approval`. With the `tofu` policy (lab only) nothing is asked and the server trusts the first key. `--yes` never approves a key. Scripted form: `hosts add NAME ADDRESS --access ENTRY --no-input` registers the host and trusts nothing (it prints the exact `hosts trust` command); `--trust-fingerprint SHA256:...` trusts only a key the server saw, otherwise it refuses and lists the seen keys. `--user` with `--key-file` or `--password-stdin` creates the access entry on the way (`--access-name`, `--host-key-policy`); `--tag K=V` is repeatable; `ADDRESS` is `HOST` or `HOST:PORT` (default 22) and is checked before any request. A duplicate name exits 7; a host the server cannot reach is registered and reported as `cannot connect` with the server's reason (exit 0). The host is placed in the active env (`--env ENV` or `envs use`); without one the server uses `default`. There is no env question in the wizard: the active env is used. A server that predates envs on hosts is reported ("this server ignored the env"). `-o json` prints the host. The wizard ends by printing the command that repeats it without questions.

**Agent hosts.** `hosts add NAME --agent` (no address) reuses or creates the access entry `agents`, registers the host and issues a one-time token; `hosts enroll NAME` issues a new one later. The install blob goes to standard output only, as one base64 line holding the agent's name, the server URL, the controller public key and the token (shown once, valid `--ttl` 1m-24h); the instructions go to standard error and never repeat it. `--token-out FILE` writes the blob to a new file (mode 0600, never overwrites); `--revoke-old` revokes this host's earlier unused tokens. Agents reach the controller only through its gateway (ADR-059), so there is no broker address or credential to give. The old `--nats-url`, `--nats-creds-file` and `--remember` are removed: using one is refused (exit 2, nothing sent) and names `machinarchy hosts enroll NAME`. `hosts revoke-agent` revokes the agent identity only (the host stays, the machine is untouched); until the server says which agent belongs to a host, the ID comes from `get agent-tokens` ("used by ID"). A server without an agent gateway answers "this server has no agent gateway".

Binding a host to an access entry needs the workspace owner. Hosts on an `agent` entry are reached through their enrolled agent (see [enrolling a host](../operations.md#enrolling-a-host)). The older `hosts|profiles`, `create host|profile|agent-token`, `delete host|profile|agent-token`, `label host` and `trust host` commands are removed, without aliases: use `hosts`, `hosts show`, `hosts add`, `hosts remove`, `hosts tag`, `hosts trust`, `hosts enroll` and `access`.

## Running programs

`run` is the one command for running, listing, following, retrying and cancelling runs. A program is an argument list, never a shell string. Before anything is sent it shows the hosts, the command and the limits on stderr and asks (`[y/N]`); the run is bound to the hosts you saw, so if the set changed meanwhile the server refuses it (exit 7, "Nothing ran") and you run it again.

```sh
machinarchy run --on tier=web -- systemctl is-active nginx
machinarchy run --on 'web-*' --except web-02 -- uptime
machinarchy run --on db-01 --do service.status service=postgresql.service
machinarchy run --on all --detach -- /opt/backup/run.sh
machinarchy run                                  # the recent runs
machinarchy run r-4k2p9x1a                       # the grouped result of one run
machinarchy run r-4k2p9x1a --host web-01         # the full output of one host
machinarchy run r-4k2p9x1a --follow              # watch a run that is going
machinarchy run r-4k2p9x1a --retry-failed        # a new run on the hosts that failed
machinarchy run r-4k2p9x1a --cancel              # ask for the run to be cancelled
```

**Where to run (`--on`, `--except`).** Host names (`web-01,web-02`), a name pattern (`web-*`, expanded by the CLI over the hosts in scope so the preview shows the real hosts), tags (`tier=web` or `tier=web,region in (eu,us)`), or `all`. Names and tags are not mixed in one `--on`. `--except` takes names or patterns. The active env limits the hosts (`environment=ENV` is added to a tag selector; a name outside the env is refused); `--env any` lifts the limit. A tag called `env` is just a tag: use `--env` to pick an env, and `--on environment=X` is refused when the active env is not X. One run targets at most 200 hosts; more is refused before the preview.

**The preview and the confirmation.** The first line is `[SERVER · ENV]`, then the number of hosts and their names (compressed: `web-[01-14]`), the command, which env the hosts are in and the limits. `--yes` answers the `[y/N]`. In an env that `envs add --protected` guards on this computer, you type the env name instead (`--confirm-env ENV` in a script, plus `--yes`; `--yes` alone is refused); a run whose hosts reach a guarded env is guarded wherever it was started. `--on all` outside any env asks you to type the number of hosts (or `--yes`). `--dry-run` shows the preview and sends nothing.

**Waiting.** A run is watched by default: a progress line on stderr (redrawn on a terminal, a plain line every 10 seconds otherwise), then the grouped result on stdout: hosts that printed the same are one line, failures first, `web-[01-12,14]` host lists, a sample of each group's output, a legend for hosts that timed out or whose outcome is unknown ("may still have run"), and a note when succeeded hosts printed different output. `--no-groups` shows one block per host, `--groups N|all` the number of groups, `-o json|yaml` the same result as data, `-q` only the run ID. The exit code is 0 when every host succeeded, 8 when some did not, 9 when none did; `--fail-on-drift` makes a clean run that printed different output exit 8. `--timeout` (default 60s, at most 24h) is per host and `--concurrency` (1-20, default 5, 20 when detached) is how many hosts work at once.

**Detaching.** `--detach`, or a `--timeout` above 15 minutes, runs the command detached on the hosts (a supervisor keeps it running) and `run` returns as soon as the run is accepted; follow it with `run ID --follow`. `--wait-timeout 10m` stops watching after that long and exits 5; the run keeps going.

**Ctrl+C.** Pressing it while watching only stops the watching: "Stopped watching. The run keeps going on the hosts." with the `--follow` and `--cancel` commands, exit 130; a second Ctrl+C ends the process at once. Pressing it while the run is being sent prints that the run "may or may not have been created" with `Next: machinarchy run`; the CLI never sends it again by itself. The same message appears when the connection drops at that moment (exit 4). A watch that loses the server (several failed polls in a row) prints "Lost contact with the server; the run keeps going." with the resume command (exit 4).

**Retry and cancel.** `--retry-failed` starts a **new** run (new ID) of the same command on the hosts of run ID that failed or timed out. Hosts whose outcome is unknown (`Lost`, `Interrupted`) and hosts that were cancelled are skipped and listed, because the command may already have run there; `--include-unknown` and `--include-cancelled` add them with a warning. The preview, confirmation and protected-env rules apply as for any run, and the old run is not changed. The command is read from the old run, which only operators and owners can see. `--cancel` asks the controller to cancel and then follows the run to its end (or returns at once with `--no-wait`). A cancel is a request: the CLI never says a run "stopped" before the controller reports the final state; a host that finished first keeps its result, and a host that could not be reached while cancelling is flagged ("the command may still be running there").

**Capabilities.** On agent hosts, `--do NAME [KEY=VALUE...]` calls one of the agent's capabilities instead of a program. The inputs are text and are sent unchanged as one JSON object; `--input-json '{...}'` is for values that are not text (not together with `KEY=VALUE`). The known capabilities are `runtime.info`, `system.facts`, `service.status` and `service.control`; the server decides what it accepts and the CLI changes no name or value. A program on an agent host is refused by the server, and the CLI points at `--do`.

**IDs.** A run is shown as `r-` and the first 8 characters of its ID. Any unique prefix of at least 6 characters works, with or without `r-`; it is looked up among the 500 most recent runs. Readers can list runs and see states and exit codes but not the command (`(hidden)`) or the output.

The old `run -l|--host|--all|--exclude|--wait|--capability|--input`, `get operations`, `cancel operation` and `logs operation` are removed, without aliases.

Errors and verbose structured logs go to stderr. Resource output goes to stdout, so it can be piped without log lines mixed into the data.

## `version`

```sh
go run ./cmd/machinarchy version
go run ./cmd/machinarchy version -o json
```

Prints the CLI version, commit, build time, Go version, and platform. It needs no config file or server, so it works on a fresh or damaged installation. On a terminal it starts with the logotype (also on the home screen when not signed in and on the first `login`); `--plain`, pipes and `-o json` never show it. The server's version is not available from the API.

## `config`

Hidden commands for support; nothing on the usual path needs them.

```sh
machinarchy config path       # the config file and the credentials file
machinarchy config show       # saved servers, credentials as ***, and every option with where it came from
machinarchy config show -o json
```

`config show` never prints a secret: every credential is `***`, and the token variable is `***` when it is set.

## Output compatibility (`-o json`, `-o yaml`)

Scripts may rely on the data output. The promise is: **keys are added, never renamed, removed or retyped** within a major version, and key names are the API's own names. The same command on the same data prints the same bytes (stable key and list order). `-o yaml` has the same names and types as `-o json`, but may leave out an empty optional value (for example a zero `lastSeenAt` that JSON prints). The key paths and types of these outputs are pinned by golden files in `internal/cli/commands/testdata/schema/`: `hosts`, `hosts show`, `envs`, `access`, `access show`, `tokens`, `whoami`, `run`, `run ID`, the home screen and `version`. A change that removes or retypes a pinned key fails `TestOutputSchemas`; adding keys does not. To record added keys, run `go test ./internal/cli/commands -run TestOutputSchemas -update` and review the diff. Outputs not pinned yet: `config show`, `hosts add`, `hosts enroll`, `tokens create`, `secret` and `runtime-config` commands.

## Command groups

`get`, `create`, `delete`, `config`, `runtime-config`, and `secret` are command groups; `tokens` lists tokens when run without a subcommand. Running a group with no subcommand prints its help and exits 0. An unknown subcommand (for example `machinarchy secret lst`) is a usage error and exits 2. Help for a group never reads the config file.

## Exit codes

Every failure prints up to three lines to stderr, in this order, and the process exit code identifies the kind of failure so scripts can react to it:

```text
Error: <what happened>
Why:   <the cause, when it is known>
Next:  <the exact command or action>
```

`Next:` was called `Hint:` before; the text is the same kind of advice. With `-o json` (or `MACHINARCHY_OUTPUT=json`) the same parts are written to stderr as one JSON object, and stdout stays empty:

```json
{"error":{"code":"auth","exit":3,"message":"...","why":"...","next":"..."}}
```

`code` is one of `usage`, `auth`, `unavailable`, `waitTimeout`, `notFound`, `conflict`, `partial`, `allFailed` or `failure`. `-o json` and `--plain` apply to the error even when the config file could not be loaded. Text that came from a server or a host is cleaned of terminal escape sequences before it is printed.

| Code | Meaning |
| ---: | --- |
| 0 | Success |
| 1 | Unexpected failure, or a question that was answered no |
| 2 | Usage error: unknown command or flag, invalid argument, bad value, unusable config file, missing confirmation, or input the API rejected as invalid (HTTP 400, 422) |
| 3 | Authentication or authorization failure (HTTP 401, 403) |
| 4 | API server, network, or timeout failure (HTTP 5xx, 408, 429, connection refused, request timeout, response that is not Machinarchy API JSON) |
| 5 | A local wait deadline (`run --wait-timeout`) elapsed while the work it was watching is still running. |
| 6 | Not found (HTTP 404, or a named host or environment that does not exist) |
| 7 | Conflict or failed precondition (HTTP 409, 412), for example a stale `--expected-revision` |
| 8 | A run finished with some hosts failed (`run`; also `--fail-on-drift`). |
| 9 | A run finished with every host failed (`run`). |
| 130 | Interrupted (Ctrl-C or SIGTERM) |

Ctrl-C now exits 130; it previously exited 0. Interrupting a command stops only the local process: it does not cancel work already accepted by the server.

HTTP redirects are not followed and are reported as exit 2 with the redirect target in the hint, which usually means the server URL should use `https://`.

## Context, config, and verbosity examples

Flags can be combined with commands:

```sh
go run ./cmd/machinarchy --config ./cli.yaml --server staging hosts -n production -o json
go run ./cmd/machinarchy --verbose hosts
```

See [configuration](configuration.md) for file paths, servers and credentials, and environment overrides.

## Scripts and CI

Set `MACHINARCHY_SERVER`, `MACHINARCHY_TOKEN` (an API token), `MACHINARCHY_ENV`, `MACHINARCHY_NO_INPUT=1` and `MACHINARCHY_OUTPUT=json`; nothing is written to the config directory. Without a terminal, or with `--no-input`, a command never waits: it exits 2 and names the flag that answers the question (`--confirm NAME`, `--yes`, `--confirm-env ENV`, `--email`). With `-o json` an error is one JSON object on stderr and stdout stays empty. `-q` prints identifiers only. `--dry-run` is supported by `run`; every other changing command refuses it (exit 2) instead of ignoring it. Exit codes are in the table above; `8` is a partial run and `9` a run where every host failed.

## Supported servers

The CLI and the controller are released together: use a CLI of the same release as the server (`machinarchy version` shows the CLI's). The server does not yet report its version, so the CLI cannot compare them. When an answer does not fit what this CLI expects, the command exits 4: valid JSON of the wrong shape says "Update the CLI"; anything that is not JSON says to check that the address points at the Machinarchy API.
