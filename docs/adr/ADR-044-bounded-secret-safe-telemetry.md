# ADR-044: Keep telemetry bounded-cardinality and secret-safe

**Status:** Accepted as the working design baseline when Phase 11 was authorized
**Date:** 2026-11-11

## Context

Host identity, addresses, secret references, command arguments, and outputs are sensitive and high-cardinality. Metrics backends multiply each label combination, while logs/traces may be retained and widely searchable.

## Decision

Metrics use only bounded route templates and fixed result/error/controller classes. Never place Host/user/token/Operation/secret IDs, raw URLs, arguments, outputs, fingerprints, or error text in metric labels. Apply explicit allowlists/redaction to logs and span attributes; correlate by request/trace IDs and use access-controlled audit for attributable security facts. Never rely on sampled telemetry as durable evidence.

## Consequences

Incident investigation uses a restricted join from opaque correlation IDs to application records, with authorization and retention. Dashboards aggregate by bounded dimensions. Instrumentation changes require a cardinality and data-classification review.

## Alternatives considered

- Label every metric with Host or Operation ID: rejected due to unbounded cardinality and disclosure risk.
- Put full request/command payloads in traces for debugging: rejected due to credential and infrastructure leakage.
- Treat logs as audit: rejected because logs can be sampled, dropped, or retained differently.
