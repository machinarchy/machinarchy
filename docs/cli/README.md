# Machinarchy CLI

The `machinarchy` CLI connects to a Machinarchy API and lets you inspect resources using a kubectl-style command structure. It uses named contexts to keep API endpoints, credentials, and default environments together.

## Build and run

Run from the repository root with Go installed:

```sh
go run ./cmd/machinarchy --help
```

Or build the executable:

```sh
make build
./bin/machinarchy --help
```

The CLI does not start the API. Start the API or monolith separately; the project [README](../../README.md#quick-start) describes local server setup.

## Quick setup

Obtain a JWT from the API's signup or login endpoint. Then configure the CLI, sending the token through standard input so it does not appear in shell history or process arguments:

```sh
go run ./cmd/machinarchy config set-cluster local --server http://localhost:8080
printf '%s' "$MACHINARCHY_USER_TOKEN" | go run ./cmd/machinarchy config set-credentials local --token-stdin
go run ./cmd/machinarchy config set-context local --cluster local --user local
go run ./cmd/machinarchy config use-context local
go run ./cmd/machinarchy get hosts
```

The CLI creates its config file on the first `config` write. See [configuration](configuration.md) for context setup, file format, and precedence rules.

## Command overview

```text
machinarchy get hosts [name-or-id]
machinarchy config current-context
machinarchy config get-contexts
machinarchy config use-context NAME
machinarchy config set-cluster NAME --server URL
machinarchy config set-credentials NAME --token-stdin
machinarchy config set-context NAME --cluster CLUSTER --user USER [--env ENVIRONMENT]
```

The `get hosts` command supports table, JSON, and YAML output. `--env` / `-n` filters by Machinarchy environment name or numeric ID. The current API does not support server-side environment filtering, so the CLI fetches hosts and environments and filters the hosts locally.

See [commands and flags](commands.md) for details and [example-cli.md](../../example-cli.md) for end-to-end recipes.

## Security notes

- Prefer `config set-credentials NAME --token-stdin`; `--token TOKEN` is available but may expose secrets through shell history or process inspection.
- The config file contains bearer tokens in plaintext. The CLI creates its parent directory with owner-only permissions and writes the config with mode `0600`; protect backups and any copied config files accordingly.
- Use HTTPS for remote API servers. HTTP is useful for local development but sends the bearer token without transport encryption.
- Treat `--verbose` output as operational logs. Current debug logging records the selected context and environment, not the token.

## Extending the command tree

The root command accepts `cli.CommandGroup` values through `cli.Options`. Add a command builder to a group in `internal/cli/commands/registry.go` or supply an additional group from another application entry point. Command implementations should receive the shared `*cli.Factory`, depend on the narrow interfaces they need, use `command.Context()` for I/O, and write results through Cobra's injected output streams.

For example, a future `apply` command can be registered in the core group without adding resource behavior to the root command. Mutation commands should define explicit validation and confirmation behavior before being exposed.

## Further reading

- [Configuration and contexts](configuration.md)
- [Commands and flags](commands.md)
- [Troubleshooting](troubleshooting.md)
- [Runnable CLI examples](../../example-cli.md)
- [SSH host registration and execution status](../../example-usecases.md)
