# Migrating from the earlier CLI

The CLI was redesigned around tasks. **There is no compatibility layer**: the
old commands, flags, environment variables and config format are gone, not
deprecated. An old command exits 2, sends nothing, and prints the command to
use instead ("did you mean ...?").

| Before | Now |
|---|---|
| `config set-cluster/set-credentials/set-context`, `login --server --context --username` | `machinarchy login URL --name NAME` |
| `config use-context NAME`, `config get-contexts`, `config current-context` | `machinarchy switch [NAME]`, `machinarchy whoami` |
| `get hosts [NAME] -l SEL` | `machinarchy hosts [--tagged SEL]`, `machinarchy hosts show NAME` |
| `get profiles`, `create profile`, `delete profile` | `machinarchy access`, `access add`, `access remove` |
| `create host`, `delete host`, `label host` | `machinarchy hosts add`, `hosts remove`, `hosts tag` |
| `trust host NAME`, `trust host --all-pending` | `machinarchy hosts trust NAME`, `hosts trust --all-pending` |
| `create agent-token`, `delete agent-token` | `machinarchy hosts enroll`, `hosts revoke-agent` |
| `get tokens`, `create token`, `delete token` | `machinarchy tokens`, `tokens create`, `tokens revoke` |
| `run -l SEL \| --host H \| --all`, `get operations`, `cancel operation`, `logs operation` | `machinarchy run --on TARGET -- PROGRAM`, `run`, `run ID --cancel`, `run ID --host H --follow` |
| `MACHINARCHY_CLI_SERVER`, `_TOKEN`, `_ENV`, `_OUTPUT`, `_NO_INPUT`, ... | `MACHINARCHY_SERVER`, `MACHINARCHY_TOKEN`, `MACHINARCHY_ENV`, `MACHINARCHY_OUTPUT`, `MACHINARCHY_NO_INPUT` |
| `--selector` | `--tagged` (and `--on` for `run`) |

## Behaviour changes to check in your scripts

- `run` **waits** and shows a grouped result. Use `--detach` to return at once. Exit 8 means some hosts failed, 9 all; the old form exited 1.
- `--yes` no longer approves host-key trust. Use `hosts trust NAME --fingerprint SHA256:...` for a key you checked, or an access entry with host-key policy `pinned`/`tofu` (`tofu` only for labs).
- `hosts trust --all-pending` needs a terminal; it is refused with `--no-input`.
- A saved credential is sent only to the server it was saved for. `--server URL` with an unsaved address uses no saved token: pass `MACHINARCHY_TOKEN`.
- A password or token is never a flag (`--token` is gone). Use stdin: `login URL --with-token`.
- The old config file (contexts, clusters, users) is refused, and its tokens are not copied. Move it aside and run `machinarchy login URL` again; see [troubleshooting](troubleshooting.md).
- `hosts` has new table columns. Parse `-o json`: field names did not change.
- The short `-n` stays for `--env`.

Design record of the plan that was replaced by this decision:
[spec/08-migration.md](spec/08-migration.md).
