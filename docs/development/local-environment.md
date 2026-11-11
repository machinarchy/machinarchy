# Local Development Environment

This document describes the local configuration and supporting services used when developing Machinarchy.

The repository has two primary Docker Compose projects:

| Compose file | Responsibility | Make targets |
|---|---|---|
| `deployments/infra.docker-compose.yml` | Shared dependencies, including PostgreSQL and NATS | `infra-config`, `infra-up`, `infra-down`, `infra-ps`, `infra-logs` |
| `docker-compose.yml` | Machinarchy application containers | `apps-config`, `apps-up`, `apps-down`, `apps-ps`, `apps-logs` |

SSH test servers use a separate Compose file under `deployments/testing/` and are not part of the normal application or infrastructure stacks.

## Environment files

The Makefile uses `.env` by default:

```makefile
ENV_FILE ?= .env
```

Both main Compose projects receive this file through `docker compose --env-file`. The local `make run-*` targets also source it as a shell file with automatic environment export enabled.

Create a local file from the example when needed:

```bash
cp .env.example .env
```

Keep `.env` local and do not commit credentials, tokens, passwords, private keys, or other secrets. Treat it as trusted shell-compatible input because local Go run targets source it. Use simple, valid shell assignments and quote values containing characters that have shell meaning.

### Machinarchy runtime configuration

The following application settings are part of the Machinarchy configuration interface. Check `.env.example` and the configuration-loading code for the exact accepted values and defaults in the current checkout.

| Variable | Purpose |
|---|---|
| `MACHINARCHY_LISTEN_ADDR` | HTTP/API listen address. The documented development default in the current project configuration is `127.0.0.1:8080`. |
| `MACHINARCHY_ENVIRONMENT` | Runtime environment name, such as `development`. |
| `MACHINARCHY_DATABASE_ENGINE` | Selects the configured database backend, such as SQLite or PostgreSQL. |
| `MACHINARCHY_DATABASE_PATH` | Filesystem path used by the SQLite database. |
| `MACHINARCHY_OPERATOR_TOKEN` | Operator token used by the application, where enabled. Treat it as a secret. |
| `MACHINARCHY_LOG_LEVEL` | Runtime logging verbosity. |

Do not assume that the table is the complete list of environment variables. Compose may define additional variables for its services. Keep `.env.example` as the canonical inventory of settings expected by the current code and deployment definitions.

### PostgreSQL integration test configuration

The repository-owned test harness at `scripts/db/test-postgres.sh` accepts:

- `DATABASE_URL`
- `MACHINARCHY_TEST_POSTGRES_URL`

The Make target is:

```bash
make test-postgres
```

Configure a disposable test database. Do not point integration tests at a production or personal database: tests may create, modify, or remove test data. Check the harness script for its exact precedence and database setup behavior.

## Local database

The repository currently has a local data directory containing `data/machinarchy.db`. The configured SQLite path is controlled by `MACHINARCHY_DATABASE_PATH`; use the actual value in your `.env` and configuration code rather than assuming every checkout uses the same path.

For local SQLite development:

1. Ensure the parent directory of the configured database path exists.
2. Ensure the current user can read and write that directory.
3. Stop local application processes before manually copying or replacing the database file.
4. Keep local database files and backups out of commits unless a deliberately sanitized fixture is being added.

For PostgreSQL development, start the infrastructure stack and use the host/port and credentials configured by the Compose file and `.env`:

```bash
make infra-up
make infra-ps
make infra-logs
```

The application runtime and integration-test harness may use different connection variables. Check the current configuration code and `scripts/db/test-postgres.sh` before assuming the test URL is also the runtime database URL.

## Docker Compose dependencies and services

Start and inspect infrastructure independently:

```bash
make infra-config
make infra-up
make infra-ps
make infra-logs
```

Start and inspect the application containers independently:

```bash
make apps-config
make apps-up
make apps-ps
make apps-logs
```

Start both stacks in the intended order:

```bash
make dev-up
make dev-ps
```

Stop them in reverse order:

```bash
make dev-down
```

The infrastructure Compose file currently documents PostgreSQL and NATS as shared dependencies. Service names, image versions, health checks, published ports, and credentials are defined by the Compose files and `.env`; inspect those files when changing the local topology.

Useful inspection commands:

```bash
# Render the effective infrastructure configuration.
docker compose --env-file .env -f deployments/infra.docker-compose.yml config

# Render the effective application configuration.
docker compose --env-file .env -f docker-compose.yml config

# Show published ports and container state.
docker compose --env-file .env -f deployments/infra.docker-compose.yml ps
docker compose --env-file .env -f docker-compose.yml ps
```

To inspect a specific service's published port, use `docker compose ... port <service> <container-port>` with the actual service name and container port from the relevant Compose file. Avoid maintaining a second, potentially stale copy of port mappings in this document.

## Local directories and generated files

| Path | Purpose | Notes |
|---|---|---|
| `.env` | Local environment configuration | Machine-specific; do not commit secrets. |
| `.env.example` | Example environment configuration | Keep this aligned with supported configuration. |
| `bin/` | Locally built application binaries | Created by build targets; removed by `make clean`. |
| `data/` | Local application data | Contains the local SQLite database in the current repository layout. Avoid committing mutable data. |
| `coverage.out` | Go coverage profile | Generated by `make coverage`; removed by `make clean`. |
| `coverage.html` | HTML coverage report | Generated by `make coverage-html`; removed by `make clean`. |
| `deployments/` | Docker Compose deployment definitions | Contains the infrastructure Compose file and testing definitions. |
| `scripts/` | Development and database helper scripts | Includes local setup and PostgreSQL test harness scripts. |

SSH fixture setup generates local SSH test material through `scripts/dev/setup-ssh-servers.sh`. The current repository layout includes `data/.ssh/`; inspect the setup script before deleting or relocating generated SSH keys, passwords, host keys, or policies. Never reuse test credentials outside the local test environment.

## Ports and services

The API's development listen-address default is `127.0.0.1:8080`, configurable with `MACHINARCHY_LISTEN_ADDR`.

Other ports are intentionally not duplicated here because they are owned by the active Compose definitions and may be overridden through `.env`. To see the actual effective mappings for your checkout:

```bash
docker compose --env-file .env -f deployments/infra.docker-compose.yml config
docker compose --env-file .env -f docker-compose.yml config
make infra-ps
make apps-ps
```

Use `docker compose ... port <service> <container-port>` to print a service's published host address. If a port is changed, update the corresponding client connection setting as well as the Compose port mapping.

## Local SSH fixtures

SSH test servers are isolated from the normal development Compose projects. Their Make targets are:

```bash
make ssh-servers-prepare
make ssh-servers-config
make ssh-servers-up
make ssh-servers-ps
make ssh-servers-logs
make ssh-servers-down
```

The Make configuration expects `deployments/testing/ssh-servers.docker-compose.yml`. Keep the filename and case consistent with `SSH_COMPOSE_FILE` in `mk/config.mk` on case-sensitive filesystems.
