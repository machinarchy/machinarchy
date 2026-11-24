# Machinarchy

Machinarchy is a Go control plane for a fleet of hosts. It keeps an inventory of
managed hosts, connects to them over SSH with owner-controlled credentials,
runs shell-free commands on them as audited Operations, and talks to
`machinarchy-agent` on hosts that run it. It is an HTTP API
(Echo) over SQLite or PostgreSQL, with a CLI.

> **Status:** in development. Managed hosts, Access Profiles, Operations,
> environments, accounts and API tokens, the audit trail, runtime configuration,
> the Secret Store and a minimal agent gateway are implemented. Existing
> databases may need to be recreated between development releases.

## Architecture

```text
 machinarchy CLI ──HTTP──▶ API ─┐                       ┌─▶ SSH ──▶ hosts
                                ├─ SQLite / PostgreSQL ─┤
 machinarchy-agent ◀─NATS─▶ Control Manager ┘ (outbox)  └─ operation worker
          (JetStream, signed)   ├ Host reconcilers
                                └ agent gateway
```

| Binary | Role |
|---|---|
| `cmd/api` | HTTP API: accounts, inventory, Operations, configuration, secrets, agents. Writes intent; never runs SSH. |
| `cmd/controlmanager` | Background work: Host reconcilers driven by a transactional outbox, the operation worker (SSH execution) and the agent gateway. Serves health and metrics only. |
| `cmd/monolit` | Both roles in one process (the easiest way to run it). |
| `cmd/machinarchy` | CLI; uses only the public HTTP API. |

Code layout: `internal/domain` (entities, rules), `internal/service` (use
cases), `internal/store/{accounts,resources,management,agents}` (one SQL
implementation per area for both dialects), `internal/controlmanager`,
`internal/operationworker`, `internal/agentgateway`, `internal/transport/http`.
More in [docs/architecture.md](docs/architecture.md) and the
[ADRs](docs/adr/README.md).

## Requirements

- Go 1.27.4+ and a checkout of
  `machinarchy-agent` repository next to this one
  (`../machinarchy-agent`): the agent wire contract,
  `github.com/machinarchy/machinarchy-agent/protocol`, is used through a local
  `replace`.
- SQLite (built in) or PostgreSQL 16.
- NATS with JetStream only if you use agents.
- For development tools: golangci-lint v2, swag v1.16.4, Docker with BuildKit.

## Build

```sh
make build          # bin/machinarchy{,-api,-controlmanager,-monolit}
make images         # container images (needs ../machinarchy-agent)
```

Binaries carry their version (`machinarchy version`). Builds are reproducible:
`-trimpath -buildvcs=false`, with version, commit and commit time from ldflags.

## Run locally

```sh
cp .env.example .env            # set MACHINARCHY_OPERATOR_TOKEN (32+ chars)
set -a; . ./.env; set +a
mkdir -m 700 -p data
go run ./cmd/monolit            # http://localhost:8080, API reference at /scalar
```

Set `MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD` for the first start to create the
owner account (`admin`). Then, with the CLI:

```sh
TOKEN=$(curl -s localhost:8080/api/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"..."}' | jq -r .token)
go run ./cmd/machinarchy config set-cluster local --server http://localhost:8080
printf '%s' "$TOKEN" | go run ./cmd/machinarchy config set-credentials local --token-stdin
go run ./cmd/machinarchy config set-context local --cluster local --user local
go run ./cmd/machinarchy config use-context local
go run ./cmd/machinarchy get hosts
```

The same through `make`: `make run-monolit` and `make run-cli -- get hosts`.
These targets load `.env` (or `ENV_FILE=…`) themselves and ignore settings
exported in your shell; `make run-cli` takes the CLI's arguments after `--`,
or in `ARGS="…"` when an argument contains `=` or needs quotes.

The [full user guide](docs/cli/user-full-guide.md) is a step-by-step lab with
three SSH servers, from login to detached runs. The [CLI guide](docs/cli/README.md)
and [use cases](docs/examples/example-usecases.md) cover Access Profiles, hosts,
`run` and secrets. The HTTP API reference is in [docs/api](docs/api/README.md).

With Docker: `docker compose up` (`docker-compose.yml`, PostgreSQL from
`deployments/infra.docker-compose.yml`), or run an image directly:

```sh
make image-monolit IMAGE_TAG=dev
docker run -p 8080:8080 -v machinarchy-data:/data --env-file .env machinarchy-monolit:dev
```

## Test

```sh
make test           # all tests
make test-race      # with the race detector
make coverage       # race + coverage.out, prints the total
make test-postgres  # PostgreSQL contract suites (MACHINARCHY_TEST_POSTGRES_URL)
make smoke          # CLI end to end against a fresh monolith
make fuzz           # each fuzz target for FUZZTIME (default 30s)
make lint vet       # golangci-lint (with gosec rules) and go vet
make vuln gosec     # govulncheck (with the reviewed allowlist) and gosec
```

`test/e2e` builds the monolith and the real agent from `../machinarchy-agent`
and runs them against an embedded NATS server: enrolment, a signed capability
call, lease renewal, revocation, metrics and graceful shutdown. It is skipped
when the agent checkout is missing, unless `MACHINARCHY_E2E_REQUIRED=1` (CI).

## Configure

Settings come from environment variables, a JSON file (`--config FILE`) and
non-secret flags (flags > environment > file > defaults) and are validated at
startup. Secrets are accepted only from the environment or `0600` files.
`.env.example` lists everything; the most important:

| Variable | Purpose |
|---|---|
| `MACHINARCHY_OPERATOR_TOKEN` | Login-session signing secret (required, 32+ characters). Rotate with `MACHINARCHY_SESSION_PREVIOUS_KEYS`; see [sessions](docs/operations.md#login-sessions). |
| `MACHINARCHY_SESSION_TTL` | Login session lifetime, default `24h`. |
| `MACHINARCHY_LOGIN_FREE_FAILURES`, `_MAX_BACKOFF`, `_IP_MAX_FAILURES`, `_FAILURE_WINDOW` | Failed-login throttling, defaults `5`, `15m`, `20`, `15m`. |
| `MACHINARCHY_ENCRYPTION_SECRET` / `_FILE` | Root key of the Secret Store. Keep it stable and backed up. |
| `MACHINARCHY_ENVIRONMENT` | `production` (default: secure cookies, explicit CORS, verified DB TLS, TLS to NATS) or `development`. |
| `MACHINARCHY_DATABASE_ENGINE`, `_PATH`, `_URL` | `sqlite` (default, `./data/machinarchy.db`) or `postgres`. |
| `MACHINARCHY_LISTEN_ADDR` | Default `:8080`. |
| `MACHINARCHY_METRICS_LISTEN_ADDR` | Optional separate listener for `/metrics` (recommended in production). |
| `MACHINARCHY_API_DOCS_ENABLED` | `/openapi.json` and `/scalar`; on in development, off in production by default. |
| `MACHINARCHY_SHUTDOWN_TIMEOUT` | Total graceful-shutdown budget, default 25s. |
| `MACHINARCHY_EXECUTION_ENABLED`, `_ALLOWED_PROGRAMS` | Command execution is off by default. |
| `MACHINARCHY_SSH_*` | SSH egress policy (deny by default). |
| `MACHINARCHY_SSH_HOST_KEY_POLICY` | How host keys become trusted: `tofu` (dev default), `approve` (prod default) or `pinned`; profiles can also use `ca`. See [operations](docs/operations.md#ssh-host-keys). |
| `MACHINARCHY_AGENT_GATEWAY_*` | Agent gateway (off unless `_NATS_URL` is set); see [operations](docs/operations.md#agent-gateway). |
| `MACHINARCHY_SIGNUP_ENABLED` | Public sign-up, default `false`. |
| `MACHINARCHY_CORS_ALLOWED_ORIGINS` | Browser origins; required in production (`CORS_ALLOWED_ORIGINS` still works). |
| `MACHINARCHY_TRUSTED_PROXIES` | Proxy IPs/CIDRs whose `X-Forwarded-For` is believed; see [operations](docs/operations.md#behind-a-reverse-proxy). |
| `MACHINARCHY_RATE_LIMIT_RPS`, `_BURST` | Per-client API rate limit, defaults `20`/`30`, per replica. |
| `MACHINARCHY_HTTP_MAX_BODY_BYTES`, `MACHINARCHY_HTTP_REQUEST_TIMEOUT` | Request size and deadline, defaults 1 MiB and `25s`. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Optional trace export. |

Runtime settings (worker counts, timeouts, SSH limits) live in the database as
immutable revisions: `machinarchy runtime-config get|publish|rollback`.

## Deploy

- Images: distroless, non-root, digest-pinned bases, `HEALTHCHECK` built in,
  `/data` volume for SQLite. One image per process (`api`, `controlmanager`,
  `monolit`) plus `cli`. Releases publish signed images to GHCR.
- Single node: the monolith with SQLite on a volume.
- Scaled: several API replicas and one or more Control Managers against
  PostgreSQL (`sslmode=verify-full`).
- Probes: `/healthz/live` (liveness) and `/healthz/ready` (readiness);
  `/metrics` for Prometheus. Set the orchestrator grace period above
  `MACHINARCHY_SHUTDOWN_TIMEOUT`.

See [operations](docs/operations.md) for probes, metrics, alerts and timeouts,
the [runbook](docs/runbook.md) for failure modes, and [security](docs/SECURITY.md).

## API

The OpenAPI document is generated from handler annotations (`make api-docs`)
and served at `/openapi.json` (with a reference at `/scalar`) when
`MACHINARCHY_API_DOCS_ENABLED` is on; a test fails when a route is missing from it.
Errors are `{"error": "...", "code": "..."}`, lists are
`{"items": [...], "nextCursor": "..."}`.

| Area | Routes |
|---|---|
| Auth | `POST /api/auth/signup`, `/login`, `/logout`; `GET /api/v1/me` |
| Managed hosts | `/api/v1/managed/hosts[/{id}[/labels]]` |
| Access Profiles | `/api/v1/managed/access-profiles[/{id}]` |
| Operations | `/api/v1/managed/operations[/preview|/{id}[/cancel]]` |
| Agents | `/api/v1/agents`, `/controller-key`, `/{id}/invoke`, `/{id}/revoke`, `/enrollment-tokens[/{id}]` |
| Workspace | `/api/v1/environments`, `/tokens`, `/members`, `/audit` |
| Owner only | `/api/v1/runtime-config`, `/api/v1/secrets` |

## License

[MIT](LICENSE).
