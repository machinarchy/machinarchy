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

`--env` / `-n` accepts a case-insensitive environment name or its numeric ID. The active context may also define a default environment.

```sh
go run ./cmd/machinarchy get hosts --env production
go run ./cmd/machinarchy get hosts -n 3
go run ./cmd/machinarchy --context staging get hosts -n production
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

## Managing hosts

```sh
go run ./cmd/machinarchy get profiles
go run ./cmd/machinarchy create host web-01 --endpoint 10.0.0.5:22 --profile deploy --label tier=web --trust SHA256:...
go run ./cmd/machinarchy label host web-01 canary=yes tier-
go run ./cmd/machinarchy delete host web-01
```

Binding a host to an Access Profile, or changing its endpoint or trusted keys, needs the workspace owner. `delete host` asks for confirmation (`--yes` skips it) and is refused while an operation targets the host.

## Running programs

`run` executes one program, without a shell, on the selected hosts. It first previews the targets, asks for confirmation, and then submits the operation bound to that preview: if the selection changes in between, the server refuses it (exit code 7) and nothing runs.

```sh
go run ./cmd/machinarchy run -l tier=web -- uptime
go run ./cmd/machinarchy run --host web-01 --host web-02 --exclude web-02 --wait -- systemctl is-active nginx
go run ./cmd/machinarchy get operations
go run ./cmd/machinarchy get operation 01JABCDEF123
go run ./cmd/machinarchy cancel operation 01JABCDEF123
```

`--wait` polls until the operation finishes, prints each host's state, exit code and first output line, and exits non-zero unless every host succeeded. Readers can list operations but never see their arguments or output.

Errors and verbose structured logs go to stderr. Resource output goes to stdout, so it can be piped without log lines mixed into the data.

## `version`

```sh
go run ./cmd/machinarchy version
go run ./cmd/machinarchy version -o json
```

Prints the CLI version, commit, build time, Go version, and platform. It needs no config file or server, so it works on a fresh or damaged installation. The server's version is not available from the API.

## `config`

```sh
go run ./cmd/machinarchy config current-context
go run ./cmd/machinarchy config get-contexts
go run ./cmd/machinarchy config use-context local
go run ./cmd/machinarchy config set-cluster local --server http://localhost:8080
printf '%s' "$MACHINARCHY_USER_TOKEN" | go run ./cmd/machinarchy config set-credentials local --token-stdin
go run ./cmd/machinarchy config set-context local --cluster local --user local --env development
```

`set-credentials` also accepts `--token TOKEN`, but this can place the token in shell history or process arguments; prefer `--token-stdin`.

## Command groups

`get`, `config`, `runtime-config`, and `secret` are command groups. Running a group with no subcommand prints its help and exits 0. An unknown subcommand (for example `machinarchy secret lst`) is a usage error and exits 2. Help for a group never reads the config file.

## Exit codes

Every failure prints `Error: <what happened>` and, when there is advice, `Hint: <how to fix it>` to stderr. The process exit code identifies the kind of failure so scripts can react to it:

| Code | Meaning |
| ---: | --- |
| 0 | Success |
| 1 | Unexpected failure, or an operation that completed unsuccessfully |
| 2 | Usage error: unknown command or flag, invalid argument, bad value, unusable config file, missing confirmation, or input the API rejected as invalid (HTTP 400, 422) |
| 3 | Authentication or authorization failure (HTTP 401, 403) |
| 4 | API server, network, or timeout failure (HTTP 5xx, 408, 429, connection refused, request timeout, response that is not Machinarchy API JSON) |
| 5 | Reserved: a local wait deadline elapsed while work is still running |
| 6 | Not found (HTTP 404, or a named host or environment that does not exist) |
| 7 | Conflict or failed precondition (HTTP 409, 412), for example a stale `--expected-revision` |
| 130 | Interrupted (Ctrl-C or SIGTERM) |

Ctrl-C now exits 130; it previously exited 0. Interrupting a command stops only the local process: it does not cancel work already accepted by the server.

HTTP redirects are not followed and are reported as exit 2 with the redirect target in the hint, which usually means the server URL should use `https://`.

## Context, config, and verbosity examples

Flags can be combined with commands:

```sh
go run ./cmd/machinarchy --config ./cli.yaml --context staging get hosts -n production -o json
go run ./cmd/machinarchy --verbose get hosts
```

See [configuration and contexts](configuration.md) for file paths, environment overrides, and the context model.
