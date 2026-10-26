# CLI troubleshooting

CLI failures are printed as readable text on stderr. For command syntax, run `machinarchy COMMAND --help`. If `config set-context` is called without its required name, the CLI prints a local setup sequence and its expected syntax.

## No active context configured

Create a cluster, store a user token, create a context, and select it:

```sh
go run ./cmd/machinarchy config set-cluster local --server http://localhost:8080
printf '%s' "$MACHINARCHY_USER_TOKEN" | go run ./cmd/machinarchy config set-credentials local --token-stdin
go run ./cmd/machinarchy config set-context local --cluster local --user local
go run ./cmd/machinarchy config use-context local
```

To use a separate file, pass the same `--config PATH` to each command or set `MACHINARCHY_CONFIG`.

## Unauthorized response

An HTTP 401 usually means the token is missing, expired, or belongs to a different API deployment. Log in to the intended deployment, update the matching named credentials, and confirm that the context selects that user and cluster:

```sh
go run ./cmd/machinarchy config get-contexts
go run ./cmd/machinarchy --context local get hosts
```

Do not paste tokens into issue reports or logs.

## Connection errors

Check that the API is reachable and that the configured server URL is its base URL. The CLI accepts absolute `http://` and `https://` URLs without embedded credentials, query strings, or fragments. Remote deployments should use HTTPS.

```sh
curl -i http://localhost:8080/healthz/ready
go run ./cmd/machinarchy --verbose get hosts
```

The CLI applies a 30-second HTTP timeout and honors cancellation from Ctrl-C / SIGTERM.

## Environment not found or ambiguous

`--env` / `-n` accepts the environment name or numeric ID. Names are matched without case sensitivity. If names collide, use the ID:

```sh
go run ./cmd/machinarchy get hosts -n 3
```

## Unsupported output format

The supported formats are `table`, `json`, and `yaml`:

```sh
go run ./cmd/machinarchy get hosts -o yaml
```

## Malformed or unreadable config

Check that the selected YAML file is valid and readable. The default config lives in the platform user config directory under `machinarchy/config.yaml`. Set `MACHINARCHY_CONFIG` or pass `--config PATH` to inspect a different file.
