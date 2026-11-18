# ADR-049: Scale bounded workers first and adopt brokers only on measured need

**Status:** Accepted as the working design baseline when Phase 12 was authorized
**Date:** 2026-11-11

## Context

The product needs an initial single-node installation and a possible distributed control plane. NATS JetStream already exists in code, but Phase 4 selected a transactional database outbox/local workqueue first; current throughput and multi-consumer demand have not been measured.

## Decision

Use bounded database-backed queue/leases and periodic resync as the first distributed coordination model. Adopt JetStream only when independent durable fan-out/Agent delivery is required or PostgreSQL poll/claim load or latency demonstrably violates an SLO after tuning. Defer Kafka until measured streaming, replay, retention, or consumer-group needs exceed PostgreSQL plus JetStream.

## Consequences

Load testing and operational evidence become explicit adoption gates. Event delivery remains at-least-once and secondary to database state even after a broker is introduced. No infrastructure platform is mandatory based on Host count alone.

## Alternatives considered

- Deploy NATS immediately for every installation: rejected because it increases operations and does not replace queue/lease correctness.
- Use Kafka preemptively: rejected because no streaming requirement or throughput evidence exists.
- Scale by unbounded goroutines: rejected because SSH, DB, and remote Hosts require controlled admission.
