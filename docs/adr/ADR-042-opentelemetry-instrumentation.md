# ADR-042: Use OpenTelemetry APIs for application observability

**Status:** Accepted as the working design baseline when Phase 11 was authorized
**Date:** 2026-11-11

## Context

The repository already configures optional OTLP trace/metric exporters and instruments HTTP/NATS traces, while Prometheus metrics are registered separately. Application metrics and end-to-end operation/reconciliation traces are missing.

## Decision

Use OpenTelemetry API/SDK for unified application spans and metrics, semantic conventions where available, and OTLP export through a collector. Preserve a Prometheus scrape endpoint where deployment needs it, but avoid duplicate instrument sets. Keep structured JSON `slog`; defer OTel log bridging until there is an explicit operational need. Export failure must be bounded and must not block core application work. Durable audit remains a separate store/contract.

## Consequences

Instrumentation needs bounded attribute schemas and explicit SDK/exporter lifecycle. The current Prometheus collectors should be migrated or bridged deliberately. Collector availability is not a readiness dependency unless a deployment explicitly makes it one; telemetry may be lost during outage without losing durable work or audit.

## Alternatives considered

- Continue separate metrics/tracing systems indefinitely: rejected because naming and correlation drift.
- Make every telemetry backend a synchronous dependency: rejected because observability outage must not stop control-plane work.
- Use logs as the only signal: rejected because queue depth, latency distribution, and SLO calculations require metrics/traces.
