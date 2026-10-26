# Contributing

## Before changing code

Read [the architecture notes](docs/architecture.md) and inspect the existing interfaces and migrations before adding dependencies or widening a package API. Keep HTTP concerns in `internal/transport/http`, use cases in `internal/service`, domain contracts in `internal/domain`, and infrastructure adapters in their existing packages.

## Change workflow

1. Make the smallest change that fits the existing package boundaries.
2. Add or update SQL migrations for persistent schema changes. Migrations are embedded separately for SQLite and PostgreSQL.
3. Keep API annotations beside the handlers and regenerate the checked-in contract with `make api-docs` when API behavior changes.
4. Format Go code with `gofmt` and run the relevant checks below.
5. Document configuration changes in `.env.example` and the README or operations guide.

## Local checks

```sh
make fmt
make vet
make test
make lint
make build
```

Install `golangci-lint` and the Swaggo CLI before using the corresponding targets. Do not commit credentials, local database files, generated binaries, or unreviewed generated API changes.

Current test debt: `internal/transport/http/v1/history/history_test.go` still targets the removed `internal/metrics` package and old handler API. Update that legacy test before treating `make test` as a passing repository check.

## API and security guidelines

- Pass the request context into all downstream calls.
- Return stable, sanitized client errors; do not expose database, SSH, or credential details.
- Never serialize host passwords, private keys, passphrases, or encryption material.
- Require pinned SSH host-key fingerprints; do not add insecure host-key bypasses.
- Keep metrics labels bounded; do not use user IDs, hostnames, or other unbounded values as labels.
- Make background goroutines cancelable and ensure subscriptions, connections, and timers have a clear shutdown owner.
