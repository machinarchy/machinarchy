# Machinarchy CLI examples

These examples use the CLI from the repository root. Start the API or monolith before issuing API resource commands. See [the CLI guide](docs/cli/README.md) for the full command and configuration reference.

## Build and inspect help

```sh
go run ./cmd/machinarchy --help
go run ./cmd/machinarchy get --help
go run ./cmd/machinarchy config --help
make build
./bin/machinarchy --help
```

## Configure a local API context

Start the local stack as described in the [project quick start](README.md#quick-start). Obtain a user JWT from signup or login, then set it in your shell. The CLI's `--token-stdin` option avoids placing the token in the command line:

```sh
export MACHINARCHY_USER_TOKEN='paste-the-user-jwt-here'
go run ./cmd/machinarchy config set-cluster local --server http://localhost:8080
printf '%s' "$MACHINARCHY_USER_TOKEN" | go run ./cmd/machinarchy config set-credentials local --token-stdin
go run ./cmd/machinarchy config set-context local --cluster local --user local --env development
go run ./cmd/machinarchy config use-context local
unset MACHINARCHY_USER_TOKEN
```

The config file is stored under the operating system's user config directory. Its user credential is a bearer token in plaintext, protected by owner-only filesystem permissions. Do not commit or share it.

## List and inspect hosts

```sh
# Default table
go run ./cmd/machinarchy get hosts

# Select one host by its name or ID
go run ./cmd/machinarchy get hosts web-01

# JSON for scripts
go run ./cmd/machinarchy get hosts -o json
go run ./cmd/machinarchy get hosts -o json | jq '.[].name'

# YAML output
go run ./cmd/machinarchy get hosts -o yaml
```

## Filter by environment

```sh
# Environment name, case-insensitive
go run ./cmd/machinarchy get hosts --env production

# Short flag and numeric environment ID
go run ./cmd/machinarchy get hosts -n 3

# Filter and print JSON
go run ./cmd/machinarchy --context local get hosts -n production -o json
```

The current API does not offer a server-side environment filter. The CLI fetches the host and environment lists, resolves the requested environment, then filters the hosts locally.

## Add and switch between environments

```sh
go run ./cmd/machinarchy config set-cluster staging --server https://api.staging.example.com
printf '%s' "$STAGING_USER_TOKEN" | go run ./cmd/machinarchy config set-credentials staging --token-stdin
go run ./cmd/machinarchy config set-context staging --cluster staging --user staging --env production

go run ./cmd/machinarchy config get-contexts
go run ./cmd/machinarchy config current-context
go run ./cmd/machinarchy config use-context staging
```

Use a context for only one command without changing the saved current context:

```sh
go run ./cmd/machinarchy --context local get hosts
```

## Use a separate config file

```sh
export MACHINARCHY_CONFIG="$PWD/.machinarchy-dev.yaml"
go run ./cmd/machinarchy config get-contexts
go run ./cmd/machinarchy --config "$PWD/.machinarchy-staging.yaml" config get-contexts
```

Add config files containing credentials to `.gitignore` before creating them.

## Use environment overrides

```sh
MACHINARCHY_CLI_CONTEXT=staging go run ./cmd/machinarchy get hosts
MACHINARCHY_CLI_ENV=production go run ./cmd/machinarchy get hosts
MACHINARCHY_CLI_OUTPUT=json go run ./cmd/machinarchy get hosts
MACHINARCHY_CLI_VERBOSE=true go run ./cmd/machinarchy get hosts
```

`MACHINARCHY_CLI_SERVER` and `MACHINARCHY_CLI_TOKEN` can temporarily override the endpoint and bearer token selected by the context. Avoid exporting long-lived credentials into shared or logged environments.

## Troubleshooting and quality commands

```sh
# See the effective saved selection and known contexts
go run ./cmd/machinarchy config current-context
go run ./cmd/machinarchy config get-contexts

# Enable structured debug logs on stderr
go run ./cmd/machinarchy --verbose get hosts

# Compile the project without changing dependency manifests
go build -mod=readonly ./...
```

For common authentication, connection, environment, and config errors, see [CLI troubleshooting](docs/cli/troubleshooting.md).
