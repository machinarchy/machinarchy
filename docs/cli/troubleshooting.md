# CLI troubleshooting

CLI failures are printed as readable text on stderr: what happened (`Error:`), why when it is known (`Why:`), and the command to run next (`Next:`). For command syntax, run `machinarchy COMMAND --help`.

## "You are not signed in"

Sign in; the command asks for what it needs:

```sh
go run ./cmd/machinarchy login http://localhost:8080
```

To use a separate file, pass the same `--config PATH` to each command or set `MACHINARCHY_CONFIG`.

## "The CLI config ... is in an older format"

Earlier versions kept contexts, clusters and users in the config file. This version does not read that format and does not copy its tokens. Move the file aside and sign in again:

```sh
mv "$HOME/.config/.machinarchy/config.yaml" "$HOME/.config/.machinarchy/config.yaml.old"
go run ./cmd/machinarchy login http://localhost:8080
```

## "This needs an owner signed in with a password"

`tokens`, `secret`, `runtime-config`, `hosts trust`, `access add` and a few more act as the workspace owner, and an API token can never be the owner. The command stops before sending anything. Sign in with the password; the session is used for these commands and the saved device token for everything else:

```sh
go run ./cmd/machinarchy login
```

## "The credentials file can be read by other users"

The CLI will not use a credentials file that others can read. Run the `chmod 600` command it prints.

## "The certificate ... is not trusted"

The server's certificate was not signed by an authority this computer trusts. If it is your own server, give its CA (the CLI trusts it for that server only; there is no switch that turns certificate checking off):

```sh
go run ./cmd/machinarchy login https://machinarchy.lab --ca-file ca.pem
```

## Login fails with `Invalid credentials`

The admin password from `MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD` applies only
when the server creates the first owner, on an empty database. If the login is
refused:

- **Started with `make run-monolith`?** It loads the repository's `.env`, not
  the variables you exported, so it may use another database whose `admin`
  has another password. Start it with
  `make run-monolith ENV_FILE=path/to/your.env`, or run `bin/machinarchy-monolith`
  after exporting your settings.
- **The database already existed** when you set the password: the setting is
  ignored. Use the password the owner was created with, or start from an empty
  database.
- After several failures the server slows logins down (HTTP 429 with
  `Retry-After`); wait and retry.

## Unauthorized response

An HTTP 401 (exit code 3) means the server did not accept the credential: a login session expired (24 hours by default), a token expired or was revoked, or the credential belongs to a different server. Sign in again and check who and where you are:

```sh
go run ./cmd/machinarchy login
go run ./cmd/machinarchy whoami
go run ./cmd/machinarchy switch
```

Do not paste tokens into issue reports or logs.

An HTTP 403 (also exit code 3) means the credential is valid but not permitted. Commands that manage runtime configuration or secrets need a human owner session; API tokens (`mch_pat_...`) are refused for them.

## Connection errors

Check that the API is reachable and that the configured server URL is its base URL. The CLI accepts absolute `http://` and `https://` URLs without embedded credentials, query strings, or fragments. Remote deployments should use HTTPS.

```sh
curl -i http://localhost:8080/healthz/ready
go run ./cmd/machinarchy --verbose hosts
```

Connection failures and timeouts exit with code 4. The CLI applies a 30-second timeout to each request; raise it for a slow server with `--request-timeout 2m` or `MACHINARCHY_REQUEST_TIMEOUT=2m`. Ctrl-C and SIGTERM cancel the request and exit with code 130.

If the error says the server did not return JSON, or reports a redirect, the configured URL probably points at a proxy, a login page, or an `http://` address that forwards to `https://`. The CLI does not follow redirects so that it never sends your token somewhere unexpected.

## Environment not found or ambiguous

`--env` / `-n` accepts the environment name or numeric ID. Names are matched without case sensitivity. If names collide, use the ID:

```sh
go run ./cmd/machinarchy hosts -n 3
```

## Unsupported output format

The supported formats are `table`, `json`, and `yaml`:

```sh
go run ./cmd/machinarchy hosts -o yaml
```

## Malformed or unreadable config

Check that the selected YAML file is valid and readable. The default config lives in the platform user config directory under `.machinarchy/config.yaml`. Set `MACHINARCHY_CONFIG` or pass `--config PATH` to inspect a different file. A damaged file fails with exit code 2; `help`, `completion`, `version`, and group help still work.

## Config file permissions warning

`Warning: CLI config ... is readable by other users` means the file that stores your tokens can be read by other accounts. Run the `chmod 600` command shown in the warning.

## Confirmation required

`secret delete`, `secret rewrap` and `runtime-config rollback` ask for confirmation. They never prompt without a terminal or with `--no-input`: they fail with exit code 2 and tell you to re-run with `--yes`. `--yes` cannot be supplied through an environment variable.
