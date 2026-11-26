# CLI commands and output

Run `machinarchy --help` or `machinarchy COMMAND --help` for the built-in Cobra help. The examples below use `go run ./cmd/machinarchy`; substitute `./bin/machinarchy` after building.

## `get hosts`

List the managed hosts visible to the configured API token (`GET /api/v1/managed/hosts`; the CLI follows every page):

```sh
go run ./cmd/machinarchy get hosts
go run ./cmd/machinarchy get host
```

Show one host by name or ID:

```sh
go run ./cmd/machinarchy get hosts web-01
go run ./cmd/machinarchy get hosts 01JABCDEF123
```

If host names are ambiguous, use the resource ID or filter to one environment.

`-l` / `--selector` filters with the selector syntax, for example `-l tier=web`, `-l 'role in (web,db),!canary'` or `-l name=web-*`. Built-in fields are `id`, `name`, `environment`, `phase`, `presence` and `profile`; any other key is a label.

### Environment filter

`--env` / `-n` accepts a case-insensitive environment name or its numeric ID. The active server may also define a default environment.

```sh
go run ./cmd/machinarchy get hosts --env production
go run ./cmd/machinarchy get hosts -n 3
go run ./cmd/machinarchy --server staging get hosts -n production
```

The CLI resolves the environment name to its ID and the server filters the hosts.

### Output formats

The default is a human-readable table. JSON and YAML are intended for scripts and tooling:

```sh
go run ./cmd/machinarchy get hosts -o table
go run ./cmd/machinarchy get hosts -o json
go run ./cmd/machinarchy get hosts -o yaml
```

For example, pipe JSON through `jq`:

```sh
go run ./cmd/machinarchy get hosts -o json | jq '.[].metadata.name'
```

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

## Access Profiles (owner)

```sh
go run ./cmd/machinarchy create profile deploy --connector ssh --username deploy --secret deploy-key --auth private-key --host-key-policy approve
go run ./cmd/machinarchy create profile agents --connector agent
go run ./cmd/machinarchy delete profile deploy

go run ./cmd/machinarchy create agent-token --host web-02 --hostname web-02.internal --ttl 30m
go run ./cmd/machinarchy get agent-tokens
go run ./cmd/machinarchy delete agent-token 9b1c…
```

`create profile --secret` takes a secret's name or ID and binds its current version. A new enrolment token is printed once: `-q` prints it alone for a pipe, `-o json` prints the whole response. `get agent-tokens` never shows token values. These commands need an owner's login session; API tokens are refused locally, before anything is sent.

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

`access add` (owner) asks what it needs when run on a terminal. The key or password comes from a hidden prompt, `--key-file` or `--password-stdin`, never from a flag. It is stored as an encrypted secret on the server and is never shown again (`access show` and `-o json` hold no secret reference or value); a file that is not a private key (for example a `.pub` file) is refused. Host keys (`--host-key-policy`): `approve` (default: you approve each key), `pinned` (only keys you pin) or `tofu` (the first key seen is trusted; a lab setting, allowed only with a key). If the entry cannot be created after the secret was stored, the secret is retired again. `access remove` (owner) is confirmed by typing the name (`--yes` is not enough), is refused by the server while hosts use the entry, and does **not** retire the stored key or password: remove it with `machinarchy secret delete`. The older `create profile` and `delete profile` still work until step 3.8 removes them.

## Environments

```sh
machinarchy envs                              # list: env, host count, protected, active
machinarchy envs add staging --description "pre-production"
machinarchy envs add production --protected   # asks first; --yes answers
machinarchy envs use staging                  # the active env for this server (local only)
machinarchy envs use any                      # clear it
machinarchy envs remove old-lab               # type the name; in a script: --confirm old-lab
```

`envs use` only changes the CLI config; the env must exist on the server. `--protected` is a **local guard**: this server cannot enforce protected environments yet, so the guard lives in the CLI config of this computer and protects you from your own typing mistakes, not from other people, other computers or other tools. It is shown as `yes (local)`. `envs remove` is confirmed by typing the env's name (`--confirm NAME` in a script, `--yes` is not enough); a locally protected env also needs `--confirm-env NAME` in a script. Removing an env clears it as the active env and drops its local guard; this command deletes no host. Moving a host between envs needs controller support (gap C-01) and arrives with `hosts move`.

## Hosts

```sh
machinarchy hosts                             # NAME ENV ADDRESS HOW STATUS TAGS
machinarchy hosts --status attention          # everything that is not ready (or: ready, all)
machinarchy hosts show web-01                 # status, env, address, access, conditions, host keys, recent runs
machinarchy hosts tag web-01 tier=web canary- # KEY=VALUE sets, KEY- removes
machinarchy hosts remove web-01               # type the name; in a script: --confirm web-01
```

`STATUS` is one of `ready`, `waiting for key approval`, `key changed`, `checking`, `cannot connect` or `needs attention`, worked out from the host's conditions (`hosts show` and `-o json` show the raw reasons). `HOW` is `ssh` or `agent` (from the access entry). Tags follow the controller's rules (letters, digits, `.`, `_`, `-`; keys may also hold `/`; at most 63 characters; `name`, `id`, `status`, `environment` and a few more are reserved); the CLI checks them before sending, and a host that changed meanwhile is reported as "the host changed", not overwritten. `hosts remove` takes the host out of the inventory **only**: an installed agent is not uninstalled or revoked, the access entry and credentials are kept, and the machine is not touched; the output says so. A host in a locally protected env (`envs add --protected`) also needs the env's name (typed, or `--confirm-env ENV` in a script) for `tag` and `remove`. `hosts move NAME --env ENV` checks the names and then refuses: the server cannot change a host's env yet (gap C-01). `hosts trust NAME` (owner) is the only way to approve SSH host keys (the old `trust host` is gone). First trust: the CLI shows the keys the controller saw and you type the first 8 characters after `SHA256:` of the key, compared with the machine; Enter skips. In a script use `--fingerprint SHA256:...`, which must be a key the controller saw; all seen keys are trusted together (the controller refuses an incomplete set). `--yes` never approves a key, and without a terminal and without `--fingerprint` nothing is trusted. A host that already trusts keys is changed only with `--replace`: the old and new keys are shown, you type the first 8 characters of the new key and then the host's name (`--confirm NAME` in a script); a locally protected env also needs its name. `hosts trust --all-pending [-l SELECTOR]` asks about each waiting host in turn, needs a terminal and is refused with `--no-input`. `hosts add` and the agent commands follow in later steps; the older `get hosts`, `label host` and `delete host` still work until step 3.8.

## Managing hosts (older commands)

```sh
go run ./cmd/machinarchy get profiles
go run ./cmd/machinarchy create host web-01 --endpoint 10.0.0.5:22 --profile deploy --label tier=web --trust SHA256:...
go run ./cmd/machinarchy create host web-02 --profile agents   # an agent host: no endpoint
go run ./cmd/machinarchy label host web-01 canary=yes tier-
go run ./cmd/machinarchy delete host web-01
```

Binding a host to an Access Profile, or changing its endpoint or trusted keys, needs the workspace owner. `--endpoint` is needed for hosts on an SSH profile. Hosts on an `agent` profile are reached through their enrolled agent instead (see [enrolling a host](../operations.md#enrolling-a-host)). `delete host` asks for confirmation (`--yes` skips it) and is refused while an operation targets the host.

## Running programs

`run` executes one program, without a shell, on the selected hosts. It first previews the targets, asks for confirmation, and then submits the operation bound to that preview: if the selection changes in between, the server refuses it (exit code 7) and nothing runs.

```sh
go run ./cmd/machinarchy run -l tier=web -- uptime
go run ./cmd/machinarchy run --host web-01 --host web-02 --exclude web-02 --wait -- systemctl is-active nginx
go run ./cmd/machinarchy get operations
go run ./cmd/machinarchy get operation 01JABCDEF123
go run ./cmd/machinarchy cancel operation 01JABCDEF123
```

`--wait` polls until the operation finishes, prints each host's state, exit code and first output line, and exits non-zero unless every host succeeded. `--concurrency N` (1–20) sets how many hosts run at once: 5 by default with `--wait`, 20 for detached launches. N hosts take about N ÷ concurrency times one host. Readers can list operations but never see their arguments or output.

On agent hosts, `run --capability NAME` calls one of the agent's capabilities instead of a program. `--input` takes a JSON object and `--timeout` defaults to 60 seconds. It previews, confirms and supports `--wait` the same way; `--detach` and a program after `--` are refused:

```sh
go run ./cmd/machinarchy run -l tier=web --capability service.status --input '{"service":"nginx.service"}' --wait
```

Errors and verbose structured logs go to stderr. Resource output goes to stdout, so it can be piped without log lines mixed into the data.

## `version`

```sh
go run ./cmd/machinarchy version
go run ./cmd/machinarchy version -o json
```

Prints the CLI version, commit, build time, Go version, and platform. It needs no config file or server, so it works on a fresh or damaged installation. The server's version is not available from the API.

## `config`

Hidden commands for support; nothing on the usual path needs them.

```sh
machinarchy config path       # the config file and the credentials file
machinarchy config show       # saved servers, credentials as ***, and every option with where it came from
machinarchy config show -o json
```

`config show` never prints a secret: every credential is `***`, and the token variable is `***` when it is set.

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
| 1 | Unexpected failure, or an operation that completed unsuccessfully |
| 2 | Usage error: unknown command or flag, invalid argument, bad value, unusable config file, missing confirmation, or input the API rejected as invalid (HTTP 400, 422) |
| 3 | Authentication or authorization failure (HTTP 401, 403) |
| 4 | API server, network, or timeout failure (HTTP 5xx, 408, 429, connection refused, request timeout, response that is not Machinarchy API JSON) |
| 5 | A local wait deadline (`--wait-timeout`) elapsed while the work it was watching is still running. No command uses it yet. |
| 6 | Not found (HTTP 404, or a named host or environment that does not exist) |
| 7 | Conflict or failed precondition (HTTP 409, 412), for example a stale `--expected-revision` |
| 8 | A run finished with some hosts failed. No command uses it yet; it is for the grouped run result. |
| 9 | A run finished with every host failed. No command uses it yet. |
| 130 | Interrupted (Ctrl-C or SIGTERM) |

Ctrl-C now exits 130; it previously exited 0. Interrupting a command stops only the local process: it does not cancel work already accepted by the server.

HTTP redirects are not followed and are reported as exit 2 with the redirect target in the hint, which usually means the server URL should use `https://`.

## Context, config, and verbosity examples

Flags can be combined with commands:

```sh
go run ./cmd/machinarchy --config ./cli.yaml --server staging get hosts -n production -o json
go run ./cmd/machinarchy --verbose get hosts
```

See [configuration](configuration.md) for file paths, servers and credentials, and environment overrides.
