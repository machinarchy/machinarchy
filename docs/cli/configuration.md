# CLI configuration, servers and credentials

## Files

The CLI keeps two files side by side. Their location is chosen in this order:

1. `--config PATH`
2. `MACHINARCHY_CONFIG`
3. The operating system's user config directory, under `.machinarchy/config.yaml` (for example `~/.config/.machinarchy/config.yaml` on Linux, `~/Library/Application Support/.machinarchy/config.yaml` on macOS)

| File | Holds | Mode |
| --- | --- | --- |
| `config.yaml` | The servers you have signed in to, which one is active, saved defaults. **No secrets.** | `0600` in a `0700` directory |
| `config.credentials.yaml` | The credentials: a login session and/or an API token per server. | `0600`, written atomically |
| `ca/NAME.pem` | A copy of the certificate authority you gave with `--ca-file` for a server. | `0600` |

The credentials file sits next to the config file with `.credentials` before the extension (`cli.yaml` → `cli.credentials.yaml`). `machinarchy config path` prints both paths and `machinarchy config show` prints everything that is saved with every secret replaced by `***`; both are hidden commands for support and scripts.

A credentials file that other users can read is **refused**, not tolerated: the command stops and prints `chmod 600 PATH`. A file that is damaged, too large, or written by a newer CLI also stops the command; the CLI never falls back to something less safe.

A config file in the format of earlier versions (contexts, clusters and users) is not read. The CLI names it, says that its tokens were not copied, and prints the two commands that move it aside and sign in again:

```sh
mv "$HOME/.config/.machinarchy/config.yaml" "$HOME/.config/.machinarchy/config.yaml.old"
machinarchy login YOUR-SERVER-URL
```

## Servers

A **server** is a Machinarchy API you have signed in to: a name, an address, and the choices made for it (`insecure-http`, a CA file, a default environment). `login` saves one and makes it active; `switch` changes the active one.

```yaml
version: 2
active: lab
servers:
  lab:
    url: https://machinarchy.example.com
    env: production        # optional default environment
  home:
    url: http://localhost:8080
    insecure-http: true
    ca-file: ca/home.pem
```

Server names are letters, digits, `.`, `_` and `-` (up to 63 characters). A name you do not choose is derived from the address (`machinarchy-example-com`, `localhost-8080`).

## Credentials and which one a command uses

Each saved server can hold two credentials: a **login session** (from an email and password, 24 hours by default) and an **API token** (a *device token* that "stay signed in" created, or a token you supplied with `login --with-token`).

A command uses, in this order:

1. `MACHINARCHY_TOKEN`, if set (never saved to disk)
2. the saved API token
3. the saved login session

A credential that is known to have expired is skipped. A command that acts as the workspace **owner** (`tokens`, `secret`, `runtime-config`, `hosts trust`, `create profile`, `get agent-tokens` and others) uses the saved login session and nothing else: an API token can never be an owner. With only a token it stops before sending anything:

```text
Error: this needs an owner signed in with a password; you are using an API token (device-laptop, operator)
Why:   API tokens can never act as the owner
Next:  machinarchy login
```

**A saved credential is only ever sent to the server it was saved for.** `--server NAME` (or `MACHINARCHY_SERVER`) picks another saved server. `--server https://other.example`, an address that is not saved, is for that one command and takes no saved credential: use `MACHINARCHY_TOKEN=... machinarchy --server https://other.example ...` or `machinarchy login https://other.example`.

## Secrets are never accepted as flags

A password is typed without echo or read from stdin with `--password-stdin`. A token is typed without echo or read from stdin with `login --with-token`. There is no `--token`, `--password` or `--secret` flag; `tokens create` prints a new token once, or writes it to a private file with `--out`. A credential prints as `***` in every error, log and output format; the only place a token is ever shown is the output of `tokens create`, once.

## TLS and plain HTTP

Servers are reached with TLS verification on. There is no flag that turns it off. For a private CA or a self-signed certificate, give the CA when you sign in:

```sh
machinarchy login https://machinarchy.lab --ca-file ca.pem
```

The CA is copied next to the config and trusted for that server only. Plain `http://` to a host that is not on this computer is refused, because a sign-in can be read on the network; on a network you trust, opt in per server with `machinarchy login http://host:8080 --insecure-http`. Redirects are never followed, so a credential is not replayed to another location.

## Environment variables and precedence

Global options are resolved in this order, highest priority first:

1. A flag on the command line
2. The environment variable `MACHINARCHY_<NAME>`
3. A saved default under `defaults:` in the config file (below)
4. The option's built-in default

Each variable is the upper-cased flag name with `-` replaced by `_`. A variable that is set but empty counts as not set.

| Variable | Purpose |
| --- | --- |
| `MACHINARCHY_SERVER` | A saved server name, or an address for this command only |
| `MACHINARCHY_TOKEN` | An API token for this command only; used before any saved credential |
| `MACHINARCHY_ENV` | Override the server's default environment filter |
| `MACHINARCHY_OUTPUT` | Set output format (`table`, `json`, or `yaml`) |
| `MACHINARCHY_VERBOSE` | Enable debug logging (`true` or `false`) |
| `MACHINARCHY_REQUEST_TIMEOUT` | Per-request HTTP timeout, for example `10s` or `2m` (default `30s`) |
| `MACHINARCHY_QUIET` | Suppress status output (`true` or `false`) |
| `MACHINARCHY_NO_HEADERS` | Omit table headers (`true` or `false`) |
| `MACHINARCHY_NO_INPUT` | Never prompt (`true` or `false`) |
| `MACHINARCHY_PLAIN` | Plain output: no colour, symbols or flavour text (`true` or `false`) |

The config path has its separate `MACHINARCHY_CONFIG` variable.

`--yes`, `--confirm`, `--confirm-env` and `--dry-run` are deliberately **not** available as environment variables or saved defaults. Each is an answer to one invocation, and a standing "yes" would defeat the confirmation it answers.

### Saved defaults

A few options can be saved in the config file so they apply when neither a flag nor a variable is given:

```yaml
defaults:
  output: json
  plain: true
  request-timeout: 60s
```

Allowed keys: `no-headers`, `no-input`, `output`, `plain`, `quiet`, `request-timeout`, `verbose`. Anything else (including `server`, `token`, `env`, `yes`) is refused when the file is read, with exit code 2 and the list of allowed keys; a boolean that is not `true` or `false` is refused too.

A value that cannot be used (for example `--output xml` or `--request-timeout banana`) fails immediately with exit code 2, before any command runs.

## Global flags

| Flag | Meaning |
| --- | --- |
| `--config PATH` | Use a particular CLI config file |
| `--server NAME-OR-ADDRESS` | A saved server, or an address for this command only (needs `MACHINARCHY_TOKEN`) |
| `--env NAME_OR_ID`, `-n` | Filter hosts by Machinarchy environment |
| `--output FORMAT`, `-o FORMAT` | Select `table`, `json`, or `yaml` output. Without it each command uses its own default (lists are tables; the existing `runtime-config` and `secret` commands print JSON). |
| `--verbose`, `-v` | Emit debug-level structured logs to stderr |
| `--request-timeout DURATION` | Timeout for each API request (default `30s`) |
| `--quiet`, `-q` | Suppress status output; print only identifiers where a command supports it |
| `--no-headers` | Omit table headers |
| `--no-input` | Never prompt; fail with exit code 2 if input or confirmation would be needed |
| `--yes`, `-y` | Pre-confirm destructive or sensitive actions (flag only). It never answers a "type the name" confirmation. |
| `--plain` | Plain output: no colour, symbols or flavour text. Also on when `MACHINARCHY_PLAIN` is true or `defaults.plain` is true. |

`--dry-run`, `--confirm NAME` and `--confirm-env ENV` are accepted but hidden: they belong to commands that do not exist yet. A command that does not support `--dry-run` refuses it (exit 2) instead of ignoring it, so a "dry run" can never change anything. `--confirm NAME` answers the "type the name" confirmation of `tokens revoke` in a script.

Colour is used only on a terminal, and not when `NO_COLOR` is set or `TERM=dumb`. Symbols such as ✔ and ✖ are used only when the locale is UTF-8 (`LC_ALL`, `LC_CTYPE` or `LANG`); otherwise the words `OK` and `FAILED` are printed. `--plain` turns all of it off.

`secret delete`, `secret rewrap` and `runtime-config rollback` ask for confirmation; `--yes` skips the prompt and `--no-input` makes them fail instead of prompting.

## Several servers

```sh
machinarchy login https://machinarchy.example.com --name prod
machinarchy login https://staging.example.com --name staging
machinarchy switch                    # list them and choose one
machinarchy switch staging            # make staging the active server
machinarchy --server prod get hosts   # one command on another server
```

`switch` lists every saved server with an `*` on the active one and whether you are signed in.
