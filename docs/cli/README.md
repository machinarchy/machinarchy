# Machinarchy CLI

The `machinarchy` CLI connects to a Machinarchy API and lets you inspect and manage resources using a kubectl-style command structure. It remembers the servers you have signed in to and keeps their credentials in a private file.

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

The CLI does not start the API. Start the API or monolith separately; the project [README](../../README.md#run-locally) describes local server setup.

## Quick setup

Sign in. The command asks for what it needs; the password is typed without echo:

```sh
go run ./cmd/machinarchy login http://localhost:8080 --name local
go run ./cmd/machinarchy whoami
go run ./cmd/machinarchy get hosts
```

On a terminal it also offers to "stay signed in" with a 90-day device token. With an API token instead of a password (for example one from `machinarchy tokens create`), read it from stdin so it does not appear in shell history or process arguments:

```sh
printf '%s' "$TOKEN" | go run ./cmd/machinarchy login http://localhost:8080 --name local --with-token
```

In a script or CI, set `MACHINARCHY_SERVER` and `MACHINARCHY_TOKEN` instead; nothing is saved. See [configuration](configuration.md) for the files, which credential a command uses, and the precedence rules.

## Command overview

```text
machinarchy get hosts [name-or-id]
machinarchy login [SERVER] [--with-token] [--name NAME] [--email EMAIL --password-stdin] [--stay-signed-in]
machinarchy logout [--everywhere]
machinarchy whoami
machinarchy switch [SERVER]
machinarchy tokens | tokens create NAME --role reader|operator [--expires 90d] [--out FILE] | tokens revoke NAME
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
For a trusted lab network, opt in per server with `login --insecure-http`. The
opt-in belongs to that saved server: an address given with `--server` or
`MACHINARCHY_SERVER` never inherits it, and a saved credential is never sent to
an address that is not a saved server.

**New here?** Follow the [full user guide](user-full-guide.md): a step-by-step lab with three SSH servers, from login to hosts, host-key trust, detached runs with `logs --follow`, cancel and cleanup.

See [commands and flags](commands.md) for details and [example-cli.md](example-cli.md) for end-to-end recipes.

## Security notes

- A password or a token is never a flag: it is typed without echo or read from stdin. A credential prints as `***` in every error, log and output format; `tokens create` shows a new token once.
- Credentials live in their own file (`config.credentials.yaml`, mode `0600`, in a `0700` directory), apart from the settings. The CLI refuses a credentials file that other users can read. Protect backups and any copied config files accordingly.
- A saved credential is sent only to the server it was saved for, over TLS with certificate verification that cannot be switched off (`login --ca-file` trusts a private CA for one server). Plain `http://` to a host that is not on this computer needs `--insecure-http`.
- Commands that act as the owner use the login session only: an API token can never be an owner, and the CLI says so before sending anything.
- Text that comes from a server or a remote host (host names, command output, error messages) is cleaned of terminal escape sequences and control characters before it is printed, so a remote command cannot clear your screen or write to your clipboard. `-o json` and `-o yaml` escape control characters instead.
- Treat `--verbose` output as operational logs. Debug logging records the selected server and environment, never a credential.

## Extending the command tree

The root command accepts `cli.CommandGroup` values through `cli.Options`. Add a command builder to a group in `internal/cli/commands/registry.go` or supply an additional group from another application entry point. Command implementations should receive the shared `*cli.Factory`, depend on the narrow interfaces they need, use `command.Context()` for I/O, and write results through Cobra's injected output streams.

For example, a future `apply` command can be registered in the core group without adding resource behavior to the root command. Mutation commands should define explicit validation and confirmation behavior before being exposed.

## Further reading

- [Full user guide: a three-host SSH lab](user-full-guide.md)
- [Configuration, servers and credentials](configuration.md)
- [Commands and flags](commands.md)
- [Troubleshooting](troubleshooting.md)
- [Runnable CLI examples](example-cli.md)
- [SSH host registration and execution status](../examples/example-usecases.md)
