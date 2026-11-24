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

Or through `make` (it loads `.env` first; exported variables such as
`MACHINARCHY_CONFIG` still apply):

```sh
make run-cli ARGS="get hosts"
make run-cli ARGS="create host web-01 --endpoint 10.0.0.5:22 --profile deploy --label tier=web"
make run-cli ARGS='run --host web-01 --yes -- sh -c "echo hi"'
```

All CLI arguments go in `ARGS`, quoted as one value; quotes inside it are
kept.

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
machinarchy config set-cluster NAME --server URL [--insecure-http]
machinarchy config set-credentials NAME --token-stdin
machinarchy config set-context NAME --cluster CLUSTER --user USER [--env ENVIRONMENT]
machinarchy trust host NAME-OR-ID [--replace] [--fingerprint SHA256:...]
machinarchy trust host --all-pending [--selector SELECTOR]
machinarchy run (-l SELECTOR | --host NAME... | --all) [--timeout SECONDS] [--wait] [--detach] -- PROGRAM [ARG...]
machinarchy logs operation ID [--host NAME-OR-ID] [--stream stdout|stderr] [--follow]
```

`run` without `--wait` is **detached**:

- each host's command starts under a supervisor (`systemd-run` when usable, otherwise `setsid`) and the SSH session ends at once, so a command may run up to 24 hours (`--timeout`);
- the controller polls the hosts, and a controller restart loses nothing;
- `logs operation ID --follow` prints the output as it arrives;
- a stream keeps its first and last 15 KiB, and a skipped middle part shows as one `[… N bytes omitted …]` line.

With `--wait` the session stays open (at most 15 minutes) unless the timeout is longer or `--detach` is given. Reading output needs the right to run commands: readers see states and exit codes, not output.

`trust host` (owner only) shows the SSH host keys the controller observed for a
host and, after confirmation, trusts exactly those keys. `--all-pending`
approves every host waiting with `HostKeyPendingApproval`. A host that already
trusts keys is changed only with `--replace`: check the new key out of band
first. Hosts whose Access Profile uses `tofu` or `ca` need no approval (see
[operations](../operations.md#ssh-host-keys)).

The `get hosts` command supports table, JSON, and YAML output. `--env` / `-n` filters by Machinarchy environment name or numeric ID. The current API does not support server-side environment filtering, so the CLI fetches hosts and environments and filters the hosts locally.

The CLI refuses to send its token over plain `http://` to a host that is not
loopback (`localhost`, `*.localhost`, `127.0.0.0/8`, `::1`): use `https://`.
For a trusted lab network, opt in per cluster with `--insecure-http`. The
opt-in does not cover a server given with `--server` or
`MACHINARCHY_CLI_SERVER`.

**New here?** Follow the [full user guide](user-full-guide.md): a step-by-step lab with three SSH servers, from login and CLI context to hosts, host-key trust, detached runs with `logs --follow`, cancel and cleanup.

See [commands and flags](commands.md) for details and [example-cli.md](example-cli.md) for end-to-end recipes.

## Security notes

- Prefer `config set-credentials NAME --token-stdin`; `--token TOKEN` is available but may expose secrets through shell history or process inspection.
- The config file contains bearer tokens in plaintext. The CLI creates its parent directory with owner-only permissions and writes the config with mode `0600`; protect backups and any copied config files accordingly.
- Use HTTPS for remote API servers. HTTP is useful for local development but sends the bearer token without transport encryption.
- Treat `--verbose` output as operational logs. Current debug logging records the selected context and environment, not the token.

## Extending the command tree

The root command accepts `cli.CommandGroup` values through `cli.Options`. Add a command builder to a group in `internal/cli/commands/registry.go` or supply an additional group from another application entry point. Command implementations should receive the shared `*cli.Factory`, depend on the narrow interfaces they need, use `command.Context()` for I/O, and write results through Cobra's injected output streams.

For example, a future `apply` command can be registered in the core group without adding resource behavior to the root command. Mutation commands should define explicit validation and confirmation behavior before being exposed.

## Further reading

- [Full user guide: a three-host SSH lab](user-full-guide.md)
- [Configuration and contexts](configuration.md)
- [Commands and flags](commands.md)
- [Troubleshooting](troubleshooting.md)
- [Runnable CLI examples](example-cli.md)
- [SSH host registration and execution status](../examples/example-usecases.md)
