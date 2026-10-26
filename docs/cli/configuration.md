# CLI configuration and contexts

## Config file location

The CLI reads a YAML file. Its path is selected in this order:

1. `--config PATH`
2. `MACHINARCHY_CONFIG`
3. The operating system's user config directory, under `machinarchy/config.yaml`

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

Get a token by signing up or logging in through the API or Scalar, then put it in an environment variable without committing it to a file:

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

Viper reads these CLI overrides using the `MACHINARCHY_CLI_` prefix:

| Variable | Purpose |
| --- | --- |
| `MACHINARCHY_CLI_CONTEXT` | Select a context for the process |
| `MACHINARCHY_CLI_ENV` | Override the context's default environment filter |
| `MACHINARCHY_CLI_OUTPUT` | Set output format (`table`, `json`, or `yaml`) |
| `MACHINARCHY_CLI_VERBOSE` | Enable debug logging (`true` or `false`) |
| `MACHINARCHY_CLI_SERVER` | Override the selected context's server for this process |
| `MACHINARCHY_CLI_TOKEN` | Override the selected context's token for this process |

The config path has its separate `MACHINARCHY_CONFIG` variable. Explicit flags override config values; environment variables override file values when the corresponding flag is not explicitly set. For API server and token, process environment overrides the current context's stored values.

Global flags:

| Flag | Meaning |
| --- | --- |
| `--config PATH` | Use a particular CLI config file |
| `--context NAME` | Use a context for one invocation |
| `--env NAME_OR_ID`, `-n` | Filter hosts by Machinarchy environment |
| `--output FORMAT`, `-o FORMAT` | Select `table`, `json`, or `yaml` output |
| `--verbose`, `-v` | Emit debug-level structured logs to stderr |

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
