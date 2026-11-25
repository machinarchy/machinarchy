# Troubleshooting

This guide covers common local development failures. Run commands from the repository root unless a section says otherwise.

## Database connection failures

**Symptoms:** the API or another component cannot connect to SQLite or PostgreSQL, database migrations fail, or PostgreSQL integration tests cannot start.

1. Confirm the expected environment file exists and contains the intended values:

   ```bash
   test -f .env && echo ".env exists"
   ```

2. For PostgreSQL, start and inspect infrastructure:

   ```bash
   make infra-config
   make infra-up
   make infra-ps
   make infra-logs
   ```

3. Check the effective Compose configuration and published ports:

   ```bash
   docker compose --env-file .env -f deployments/infra.docker-compose.yml config
   docker compose --env-file .env -f deployments/infra.docker-compose.yml ps
   ```

4. Check that the application connection settings agree with the actual Compose service name, host, port, database name, username, and password. A process running on the host normally uses the published host port; a container on the same Compose network normally uses the Compose service name and container port.

5. For SQLite, check `MACHINARCHY_DATABASE_PATH`, create its parent directory if necessary, and verify that the application user has read/write access.

For PostgreSQL integration tests, the repository test harness accepts `DATABASE_URL` and/or `MACHINARCHY_TEST_POSTGRES_URL`. Inspect `scripts/db/test-postgres.sh` for exact precedence and setup behavior. Use a disposable test database, not a production database.

## Go module download failures

**Symptoms:** `go mod download`, `go mod verify`, `go test`, or `go build` fails while resolving dependencies.

Check network access and the Go module proxy configuration:

```bash
go version
go env GOPROXY GOSUMDB GOPRIVATE
```

Retry dependency download and verification:

```bash
make deps
```

If the project uses private modules, confirm that your Git credentials and `GOPRIVATE` configuration are set correctly. Follow your organization's approved proxy and certificate configuration when behind a corporate proxy.

Do not delete `go.sum` as a troubleshooting shortcut and do not disable checksum verification merely to make a download pass. If dependency metadata is intentionally changed, review the diff from:

```bash
make deps-tidy
git diff -- go.mod go.sum
```

## Git hook problems

**Symptoms:** hooks do not run, Git reports that a hook cannot execute, or the wrong hook directory is configured.

Install the repository hooks:

```bash
make hooks-install
```

Check the configured hook path and executable bits:

```bash
git config --get core.hooksPath
ls -l .githooks/
test -x .githooks/pre-commit && echo "pre-commit is executable"
test -x .githooks/commit-msg && echo "commit-msg is executable"
```

The expected local hook path is `.githooks`. If executable permissions are missing, restore them in the working tree:

```bash
chmod +x .githooks/pre-commit .githooks/commit-msg
make hooks-install
```

If hooks still do not run, check whether a per-command `git -c core.hooksPath=...` override or another repository configuration is overriding the setting. The hook installation target changes only the local repository configuration.

## Docker startup problems

**Symptoms:** Docker commands report that the daemon is unavailable, Compose cannot parse a file, or containers exit immediately.

Check Docker and Compose first:

```bash
docker info
docker compose version
```

Validate the Compose files before starting services:

```bash
make infra-config
make apps-config
make ssh-servers-config
```

Inspect container state and logs:

```bash
make infra-ps
make infra-logs
make apps-ps
make apps-logs
```

For a single service, use the relevant Compose file with `docker compose logs <service>` and `docker compose ps`. Check the container's exit code and logs before repeatedly restarting it.

Common causes include:

- Docker daemon is stopped or the current user cannot access it.
- `.env` is missing or required Compose variables are unset.
- A required host port is already in use.
- An image cannot be pulled because of registry, DNS, proxy, or authentication issues.
- An application container starts before its dependency is healthy. Check the Compose health checks and application retry behavior.

The full development startup target starts infrastructure before applications:

```bash
make dev-up
```

## Port conflicts

**Symptoms:** a service cannot bind to a port, Compose reports that a port is already allocated, or an application is reachable at an unexpected address.

Find the effective port mappings:

```bash
docker compose --env-file .env -f deployments/infra.docker-compose.yml config
docker compose --env-file .env -f docker-compose.yml config
make infra-ps
make apps-ps
```

On Linux, identify a process listening on a port with:

```bash
ss -ltnp
```

On macOS or Linux systems with `lsof` installed:

```bash
lsof -nP -iTCP:<port> -sTCP:LISTEN
```

Replace `<port>` with the port reported by the error. Stop or reconfigure the conflicting process, or change the relevant published port in the Compose configuration and its corresponding client URL in `.env`.

For the API process, check `MACHINARCHY_LISTEN_ADDR`. Its documented development default is `127.0.0.1:8080`; change it only when the desired address/port is available and consistent with any container or client configuration.

## SSH test fixture problems

**Symptoms:** SSH fixture Compose validation fails, servers do not start, or generated test credentials are missing.

Prepare and validate the fixtures:

```bash
make ssh-servers-prepare
make ssh-servers-config
make ssh-servers-up
make ssh-servers-ps
make ssh-servers-logs
```

The Make configuration expects `deployments/testing/ssh-servers.docker-compose.yml`. Verify the filename and capitalization match `SSH_COMPOSE_FILE` in `mk/config.mk`; capitalization matters on case-sensitive filesystems.

The setup script generates local test keys, passwords, and policies. Do not commit those generated secrets or reuse them outside the test environment. Stop the fixtures with:

```bash
make ssh-servers-down
```

## Still stuck?

When reporting a problem, include the exact command, relevant error output, Go and Docker/Compose versions, and the affected service name. Remove tokens, passwords, private keys, database credentials, and other secrets from logs and configuration snippets before sharing them.
