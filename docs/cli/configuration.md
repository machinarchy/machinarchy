# CLI configuration and contexts

## Config file location

The CLI reads a YAML file. Its path is selected in this order:

1. `--config PATH`
2. `MACHINARCHY_CONFIG`
3. The operating system's user config directory, under `.machinarchy/config.yaml` (for example `~/.config/.machinarchy/config.yaml` on Linux)

Use `--config` consistently when managing or using a non-default config file. For example:

```sh
go run ./cmd/machinarchy --config ./dev-cli.yaml config get-contexts
```

If the default config file does not exist, commands that need an API context (such as `get hosts`) report that no context is selected. `config get-contexts` can still show an empty context list. The first config write creates the parent directory and file.

## Context model

A context connects three things:

- A **cluster** stores the Machinarchy API base URL.
- A **user** stores the API bearer token.
- A **context** selects a cluster, a user, and optionally a default Machinarchy environment.

This allows, for example, `local`, `staging`, and `production` contexts to use different endpoints and credentials. `config use-context` changes the persisted default. `--context NAME` selects a context for only one invocation.

## Recommended setup

With a username and password, `login` creates the cluster, user and context in one step and stores the session token:

```sh
go run ./cmd/machinarchy login --server http://localhost:8080 --context local --username admin
```

With an API token, put it in an environment variable without committing it to a file and configure the context by hand:

```sh
export MACHINARCHY_USER_TOKEN='paste-the-JWT-here'
go run ./cmd/machinarchy config set-cluster local --server http://localhost:8080
printf '%s' "$MACHINARCHY_USER_TOKEN" | go run ./cmd/machinarchy config set-credentials local --token-stdin
go run ./cmd/machinarchy config set-context local --cluster local --user local
go run ./cmd/machinarchy config use-context local
unset MACHINARCHY_USER_TOKEN
```

Use `printf '%s'` rather than `echo` so the token is not modified by an extra newline. `--token-stdin` accepts at most 64 KiB.

## YAML format

The CLI can create this structure through the `config` commands. A hand-written example is:

```yaml
apiVersion: machinarchy.io/v1
current-context: local
clusters:
  local:
    server: http://localhost:8080
users:
  local:
    token: replace-with-a-user-jwt
contexts:
  local:
    cluster: local
    user: local
    environment: development
```

Prefer the commands over editing the token field directly. The config file stores the bearer token in plaintext, protected by filesystem permissions. Do not commit it or include it in support bundles.

## Environment variables and precedence

Settings are resolved in this order, highest priority first:

1. Command-line flags
2. `MACHINARCHY_CLI_*` environment variables
3. The selected context in the config file (server, token, default environment)
4. Built-in defaults

The config file is read as plain YAML, so context, cluster, and user names are preserved exactly as written (`Prod.EU` stays `Prod.EU`). Each environment variable is the upper-cased flag name with `-` replaced by `_`:

| Variable | Purpose |
| --- | --- |
| `MACHINARCHY_CLI_CONTEXT` | Select a context for the process |
| `MACHINARCHY_CLI_ENV` | Override the context's default environment filter |
| `MACHINARCHY_CLI_OUTPUT` | Set output format (`table`, `json`, or `yaml`) |
| `MACHINARCHY_CLI_VERBOSE` | Enable debug logging (`true` or `false`) |
| `MACHINARCHY_CLI_SERVER` | Override the selected context's server for this process |
| `MACHINARCHY_CLI_TOKEN` | Override the selected context's token for this process |
| `MACHINARCHY_CLI_REQUEST_TIMEOUT` | Per-request HTTP timeout, for example `10s` or `2m` (default `30s`) |
| `MACHINARCHY_CLI_QUIET` | Suppress status output (`true` or `false`) |
| `MACHINARCHY_CLI_NO_HEADERS` | Omit table headers (`true` or `false`) |
| `MACHINARCHY_CLI_NO_INPUT` | Never prompt (`true` or `false`) |

The config path has its separate `MACHINARCHY_CONFIG` variable. For API server and token, the process environment overrides the current context's stored values.

`--yes` is deliberately **not** available as an environment variable or config setting. Skipping a confirmation must be an explicit choice on each command line.

A value that cannot be used (for example `--output xml` or `--request-timeout banana`) fails immediately with exit code 2, before any command runs.

Global flags:

| Flag | Meaning |
| --- | --- |
| `--config PATH` | Use a particular CLI config file |
| `--context NAME` | Use a context for one invocation |
| `--env NAME_OR_ID`, `-n` | Filter hosts by Machinarchy environment |
| `--output FORMAT`, `-o FORMAT` | Select `table`, `json`, or `yaml` output. Without it each command uses its own default (lists are tables; the existing `runtime-config` and `secret` commands print JSON). |
| `--verbose`, `-v` | Emit debug-level structured logs to stderr |
| `--server URL` | Use this API server instead of the context's server |
| `--request-timeout DURATION` | Timeout for each API request (default `30s`) |
| `--quiet`, `-q` | Suppress status output; print only identifiers where a command supports it |
| `--no-headers` | Omit table headers |
| `--no-input` | Never prompt; fail with exit code 2 if input or confirmation would be needed |
| `--yes`, `-y` | Pre-confirm destructive or sensitive actions (flag only) |

`secret delete`, `secret rewrap` and `runtime-config rollback` ask for confirmation; `--yes` skips the prompt and `--no-input` makes them fail instead of prompting.

`config set-cluster` has its own `--server` flag, and `config set-context` has its own `--env` flag; on those commands the flag sets the value being stored rather than overriding the active context.

## Config file security

The config file stores bearer tokens in plaintext. New files are created with mode `0600` in a `0700` directory. If an existing config file that contains tokens is readable by other users, the CLI prints a warning to stderr (it keeps working) and shows the command to fix it:

```sh
chmod 600 ~/.config/.machinarchy/config.yaml
```

The CLI never prints tokens in output, errors, warnings, or verbose logs, and never follows HTTP redirects, so a token is not replayed to another location.

## Managing multiple contexts

```sh
go run ./cmd/machinarchy config set-cluster staging --server https://api.staging.example.com
printf '%s' "$STAGING_USER_TOKEN" | go run ./cmd/machinarchy config set-credentials staging --token-stdin
go run ./cmd/machinarchy config set-context staging --cluster staging --user staging --env production
go run ./cmd/machinarchy config get-contexts
go run ./cmd/machinarchy config use-context staging
```

Use `--context staging` to make a one-command choice without changing the saved current context:

```sh
go run ./cmd/machinarchy --context staging get hosts
```
