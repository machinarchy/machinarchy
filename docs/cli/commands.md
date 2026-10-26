# CLI commands and output

Run `machinarchy --help` or `machinarchy COMMAND --help` for the built-in Cobra help. The examples below use `go run ./cmd/machinarchy`; substitute `./bin/machinarchy` after building.

## `get hosts`

List hosts visible to the configured API token:

```sh
go run ./cmd/machinarchy get hosts
go run ./cmd/machinarchy get host
```

Show one host by name or ID:

```sh
go run ./cmd/machinarchy get hosts web-01
go run ./cmd/machinarchy get hosts 01JABCDEF123
```

If host names are ambiguous, use the resource ID or filter to one environment.

### Environment filter

`--env` / `-n` accepts a case-insensitive environment name or its numeric ID. The active context may also define a default environment.

```sh
go run ./cmd/machinarchy get hosts --env production
go run ./cmd/machinarchy get hosts -n 3
go run ./cmd/machinarchy --context staging get hosts -n production
```

The API currently returns host and environment lists without an environment filter parameter. The CLI resolves the environment and filters the hosts locally.

### Output formats

The default is a human-readable table. JSON and YAML are intended for scripts and tooling:

```sh
go run ./cmd/machinarchy get hosts -o table
go run ./cmd/machinarchy get hosts -o json
go run ./cmd/machinarchy get hosts -o yaml
```

For example, pipe JSON through `jq`:

```sh
go run ./cmd/machinarchy get hosts -o json | jq '.[].name'
```

Errors and verbose structured logs go to stderr. Resource output goes to stdout, so it can be piped without log lines mixed into the data.

## `config`

```sh
go run ./cmd/machinarchy config current-context
go run ./cmd/machinarchy config get-contexts
go run ./cmd/machinarchy config use-context local
go run ./cmd/machinarchy config set-cluster local --server http://localhost:8080
printf '%s' "$MACHINARCHY_USER_TOKEN" | go run ./cmd/machinarchy config set-credentials local --token-stdin
go run ./cmd/machinarchy config set-context local --cluster local --user local --env development
```

`set-credentials` also accepts `--token TOKEN`, but this can place the token in shell history or process arguments; prefer `--token-stdin`.

## Context, config, and verbosity examples

Flags can be combined with commands:

```sh
go run ./cmd/machinarchy --config ./cli.yaml --context staging get hosts -n production -o json
go run ./cmd/machinarchy --verbose get hosts
```

See [configuration and contexts](configuration.md) for file paths, environment overrides, and the context model.
