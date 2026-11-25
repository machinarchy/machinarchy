# ADR-047: Keep SQLite single-node and use PostgreSQL for distributed coordination

**Status:** Accepted as the working design baseline when Phase 12 was authorized
**Date:** 2026-11-11

## Context

SQLite is the simple local deployment database; Machinarchy's future independent API/Control Manager processes and worker replicas require shared durable coordination. SQLite WAL permits reader/writer overlap but has one writer and requires processes to use the database on the same host.

## Decision

Support SQLite for one-node, one active application composition on local durable storage. Support PostgreSQL as the shared coordination database for separately deployed API/Control Manager processes and horizontal replicas. Never use a shared network filesystem SQLite database as a distributed store.

## Consequences

Users may need a controlled migration to PostgreSQL when they need independent process scaling or HA. SQLite performance at 100 or 1,000 Hosts remains workload-dependent and must be measured. PostgreSQL does not itself provide application-level worker correctness; leases/fencing are required.

## Alternatives considered

- Use SQLite on shared storage for multiple nodes: rejected due to WAL/filesystem and concurrency constraints.
- Require PostgreSQL for all installs: rejected because it adds an unnecessary dependency to single-node deployments.
- Infer database choice from Host count alone: rejected because writes, latency, and availability requirements dominate.
