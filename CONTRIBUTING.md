# Contributing

## Before changing code

Read [the architecture notes](docs/architecture.md) and inspect the existing interfaces and migrations before adding dependencies or widening a package API. Keep HTTP concerns in `internal/transport/http`, use cases in `internal/service`, domain contracts in `internal/domain`, and infrastructure adapters in their existing packages.

## Change workflow

1. Make the smallest change that fits the existing package boundaries.
2. Add or update SQL migrations for persistent schema changes. Migrations are embedded separately for SQLite and PostgreSQL.
3. Keep API annotations beside the handlers and regenerate the checked-in contract with `make api-docs` when API behavior changes.
4. Format Go code with `gofmt` and run the relevant checks below.
5. Document configuration changes in `.env.example` and the README or operations guide.
6. Update [`ChangeLogAI.md`](ChangeLogAI.md) in the same change for every modification, feature, fix, or documentation/configuration update. Record only checks that were actually run and distinguish implemented work from planned work.

## Local checks

```sh
make fmt
make vet
make test
make lint
make build
```

`make test` runs the store contract tests against SQLite only. To run the same suite against PostgreSQL, either use `make test-postgres` (starts a throwaway Docker container) or point the tests at your own server:

```sh
export MACHINARCHY_TEST_POSTGRES_URL='postgres://user:password@localhost:5432/postgres?sslmode=disable'
go test ./internal/store/... -count=1
```

Each test creates and drops its own schema, so the database may be shared, but use a disposable server. Set `MACHINARCHY_REQUIRE_POSTGRES_TESTS=1` to fail instead of skip when the URL is missing. The URL must use the `postgres://` form. GitHub Actions (`.github/workflows/ci.yml`) runs formatting, build, vet, and `go test -race` with a PostgreSQL service on every pull request.

Install `golangci-lint` and the Swaggo CLI before using the corresponding targets. Do not commit credentials, local database files, generated binaries, or unreviewed generated API changes.

Current test debt: `internal/transport/http/v1/history/history_test.go` still targets the removed `internal/metrics` package and old handler API. Update that legacy test before treating `make test` as a passing repository check.

## API and security guidelines

- Pass the request context into all downstream calls.
- Return stable, sanitized client errors; do not expose database, SSH, or credential details.
- Never serialize host passwords, private keys, passphrases, or encryption material.
- Require pinned SSH host-key fingerprints; do not add insecure host-key bypasses.
- Keep metrics labels bounded; do not use user IDs, hostnames, or other unbounded values as labels.
- Make background goroutines cancelable and ensure subscriptions, connections, and timers have a clear shutdown owner.
