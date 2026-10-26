# Operations guide

## Startup configuration

The API, control-manager, and monolith read configuration from environment variables. They do not parse `.env` files. See [`.env.example`](../.env.example) for a complete template.

Set `MACHINARCHY_OPERATOR_TOKEN` to a cryptographically random value of at least 32 characters. In production, set `MACHINARCHY_ENCRYPTION_SECRET` to an independently generated value and keep it stable across restarts and upgrades. Back it up securely: changing or losing it makes persisted encrypted SSH credentials unusable. Production also requires explicit `CORS_ALLOWED_ORIGINS`; wildcard origins are rejected. PostgreSQL URLs in production must use `sslmode=verify-full`.

SQLite migrations run at startup, and its parent directory is created with restrictive permissions. Use a durable mounted volume for the database file. The default SQLite pool is intentionally limited to one connection. PostgreSQL pool sizes, connection lifetime, and connect timeout can be configured with the `MACHINARCHY_DATABASE_*` variables shown in `.env.example`.

The event broker requires NATS JetStream. At startup Machinarchy creates the `MACHINARCHY` stream or validates that an existing stream has compatible subjects and bounded retention. Provision persistent NATS storage in environments where retained events matter.

## Kubernetes probes

Configure the container port from `MACHINARCHY_LISTEN_ADDR` (default `8080`) for the API, control-manager operations server, or monolith. All three expose `/healthz/live`, `/healthz/ready`, and `/metrics`; the control-manager does not expose application API routes. Use:

```yaml
livenessProbe:
  httpGet:
    path: /healthz/live
    port: 8080
readinessProbe:
  httpGet:
    path: /healthz/ready
    port: 8080
```

`/healthz` is an alias for liveness. Liveness reports whether the process is stopping. Readiness checks the database and NATS with a short timeout; the control-manager and monolith also report whether the subscription loop is active. Backend outages should make a pod unready without triggering repeated liveness restarts.

## Metrics and tracing

Prometheus scrapes `/metrics`. All processes report Go/process and database pool metrics, including `machinarchy_db_connections_*` and `machinarchy_db_connection_waits_total`. The API and monolith additionally report:

- `machinarchy_http_requests_total` and `machinarchy_http_request_duration_seconds`, labeled by method, route template, and status where applicable.
- `machinarchy_ssh_operations_in_flight`, `machinarchy_ssh_operations_limit`, and `machinarchy_ssh_capacity_rejections_total`.

HTTP server spans and NATS producer/consumer spans use OpenTelemetry. Configure an OTLP/HTTP endpoint with `OTEL_EXPORTER_OTLP_ENDPOINT` and standard SDK environment variables such as service resource attributes and sampling settings. Keep metrics and tracing endpoints behind network policy or authenticated infrastructure when exposed outside a trusted cluster.

## Shutdown and capacity

The processes handle SIGINT and SIGTERM, stop accepting HTTP requests, and drain in-flight requests with a bounded shutdown timeout. Worker shutdown cancels the NATS subscription after HTTP draining completes. Tune the orchestrator termination grace period to exceed the configured drain interval. SSH verification is bounded by a shared in-process bulkhead; watch the SSH utilization and rejection metrics before increasing concurrency. Database pool tuning and process replicas should be considered together, especially when using PostgreSQL.
