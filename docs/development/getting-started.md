# Getting Started

This guide takes you from a fresh checkout to a running local Machinarchy development environment.

For the exact tool requirements, see [Prerequisites](prerequisites.md). For configuration, databases, local directories, and ports, see [Local Environment](local-environment.md).

## 1. Clone the repository

```bash
git clone <repository-url>
cd machinarchy
```

Run the remaining commands from the repository root (the directory containing `go.mod` and `Makefile`).

## 2. Check the prerequisites

Confirm that Go, Git, GNU Make, Docker Engine, and the Docker Compose v2 plugin are available:

```bash
go version
git --version
make --version
docker --version
docker compose version
```

Use the Go version declared by the repository's `go.mod` file. See [Prerequisites](prerequisites.md) for optional tools used by individual targets.

## 3. Prepare the local environment

Run the repository setup target:

```bash
make setup-local
```

This invokes `scripts/dev/setup-local.sh`. Review its output and any generated or updated local files. The application run targets expect the configured environment file (by default `.env`) to exist.

If `.env` was not created by the setup script, create it from the committed example and edit it:

```bash
cp .env.example .env
```

Do not overwrite an existing `.env` without first preserving any local settings. Keep secrets and machine-specific values out of Git. The `.env` file is treated as trusted, shell-compatible input by the local `make run-*` targets; do not put untrusted content in it.

Install the repository's Git hooks explicitly:

```bash
make hooks-install
```

This configures the local repository to use `.githooks`. It does not change the hooks configuration globally for your machine.

## 4. Review available commands

```bash
make help
```

The help output groups targets by category. Common commands include:

| Command | Purpose |
|---|---|
| `make setup-local` | Prepare local development files and environment |
| `make deps` | Download and verify Go modules |
| `make build` | Build all application binaries into `bin/` |
| `make test` | Run the Go test suite |
| `make verify` | Run standard local verification |
| `make verify-full` | Run extended verification, including race, PostgreSQL, and security checks |
| `make help` | List documented developer commands |

## 5. Start the application environment

### Option A: Start the complete Docker Compose environment

This is the simplest way to start the infrastructure and application containers together:

```bash
make infra-config
make apps-config
make dev-up
make dev-ps
```

`dev-up` starts infrastructure first and then starts the application stack. The infrastructure Compose file is `deployments/infra.docker-compose.yml`; the application Compose file is the root `docker-compose.yml`.

Follow logs with:

```bash
make infra-logs
make apps-logs
```

Use `make dev-down` to stop the application stack and then the infrastructure stack. The normal `down` targets do not request removal of named volumes.

### Option B: Run a Go application locally

Start the required infrastructure first:

```bash
make infra-up
```

Then, in a separate terminal, run the component you are working on. For example:

```bash
make run-api
```

Other available local run targets are `make run-controlmanager`, `make run-monolit`, `make run-cli`, and `make run-agent`. These targets load `.env` and execute the corresponding Go package. They do not start PostgreSQL or NATS automatically.

To run a local component against PostgreSQL or another dependency, make sure the corresponding service is running and the values in `.env` match that service's connection settings.

## 6. Run tests

Start with the standard Go test suite:

```bash
make test
```

Run the standard local verification checks:

```bash
make verify
```

For additional coverage, use the targets below when their prerequisites are available:

```bash
make test-race       # Requires CGO_ENABLED=1 and a working C toolchain
make test-postgres   # Requires a reachable PostgreSQL test database
make security        # Requires gosec and govulncheck
make shellcheck      # Requires ShellCheck
make verify-full     # Runs the extended checks above
```

The PostgreSQL test harness accepts `DATABASE_URL` and/or `MACHINARCHY_TEST_POSTGRES_URL`. See [Local Environment](local-environment.md) before configuring integration tests.

## 7. Build the binaries

```bash
make build
```

Binaries are written to `bin/`:

- `bin/machinarchy-api`
- `bin/machinarchy-controlmanager`
- `bin/machinarchy-monolit`
- `bin/machinarchy`
- `bin/machinarchy-agent`

Build an individual component with `make build-api`, `make build-controlmanager`, `make build-monolit`, `make build-cli`, or `make build-agent`.

## 8. Stop local services and clean generated artifacts

```bash
make dev-down
make ssh-servers-down
make clean
```

Only stop the SSH fixtures if you started them. `make clean` removes the local `bin/` directory and coverage output files; it does not remove the local database, `.env`, SSH fixture data, or Docker volumes.

## Next steps

- Read [Local Environment](local-environment.md) to understand configuration and service connectivity.
- Read [Troubleshooting](troubleshooting.md) if setup, database, Go modules, Git hooks, or Docker startup fails.
- Use `docs/operations.md` for operational guidance and `docs/architecture.md` for the current system overview.
