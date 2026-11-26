# Machinarchy CLI examples

These examples use the CLI from the repository root. Start the API or monolith before issuing API resource commands. See [the CLI guide](README.md) for the full command and configuration reference.

## Build and inspect help

```sh
go run ./cmd/machinarchy --help
go run ./cmd/machinarchy get --help
go run ./cmd/machinarchy config --help
make build
./bin/machinarchy --help
```

## Sign in to a local server

Start the local stack as described in the [project README](../../README.md#run-locally). `login` asks for what it needs; the password and tokens are typed without echo and are never flags:

```sh
go run ./cmd/machinarchy login http://localhost:8080 --name local    # asks for the email or username and the password
go run ./cmd/machinarchy whoami
```

Without a terminal, give everything it would ask for. The password comes from stdin:

```sh
printf '%s' "$ADMIN_PASSWORD" | go run ./cmd/machinarchy login http://localhost:8080 --name local --email admin --password-stdin
```

With an API token (created by an owner with `tokens create`), read it from stdin. It is checked with the server before it is saved:

```sh
printf '%s' "$TOKEN" | go run ./cmd/machinarchy login http://localhost:8080 --name local --with-token
```

The server is saved in the CLI config under the operating system's user config directory and the credentials in a private file next to it (mode 0600). Do not commit or share either.

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
go run ./cmd/machinarchy --server local get hosts -n production -o json
```

The current API does not offer a server-side environment filter. The CLI fetches the host and environment lists, resolves the requested environment, then filters the hosts locally.

## Add and switch between servers

```sh
go run ./cmd/machinarchy login https://api.staging.example.com --name staging
go run ./cmd/machinarchy switch               # list the servers; choose the active one
go run ./cmd/machinarchy switch staging
go run ./cmd/machinarchy logout               # end the session and forget it on this computer
```

Use another saved server for only one command without changing the active one:

```sh
go run ./cmd/machinarchy --server local get hosts
```

## Use a separate config file

```sh
export MACHINARCHY_CONFIG="$PWD/.machinarchy-dev.yaml"
go run ./cmd/machinarchy config path
go run ./cmd/machinarchy --config "$PWD/.machinarchy-staging.yaml" switch
```

The credentials are saved next to the config file (`.machinarchy-dev.credentials.yaml`). Add both to `.gitignore` before creating them.

## API tokens for scripts

```sh
go run ./cmd/machinarchy tokens create ci-deploy --role operator --expires 90d   # shown once
go run ./cmd/machinarchy tokens
go run ./cmd/machinarchy tokens revoke ci-deploy
```

In CI, give the token and the server by environment; nothing is saved to disk:

```sh
MACHINARCHY_SERVER=https://machinarchy.example.com MACHINARCHY_TOKEN="$CI_TOKEN" machinarchy get hosts
```

## Use environment overrides

```sh
MACHINARCHY_SERVER=staging go run ./cmd/machinarchy get hosts
MACHINARCHY_ENV=production go run ./cmd/machinarchy get hosts
MACHINARCHY_OUTPUT=json go run ./cmd/machinarchy get hosts
MACHINARCHY_VERBOSE=true go run ./cmd/machinarchy get hosts
```

`MACHINARCHY_SERVER` picks a saved server (or an address for one command) and `MACHINARCHY_TOKEN` is an API token used before any saved credential. A saved credential is never sent to an address that is not a saved server. Avoid exporting long-lived credentials into shared or logged environments.

## Troubleshooting and quality commands

```sh
# See who you are signed in as, and what is saved (secrets are shown as ***)
go run ./cmd/machinarchy whoami
go run ./cmd/machinarchy config show

# Enable structured debug logs on stderr
go run ./cmd/machinarchy --verbose get hosts

# Compile the project without changing dependency manifests
go build -mod=readonly ./...
```

For common authentication, connection, environment, and config errors, see [CLI troubleshooting](troubleshooting.md).
