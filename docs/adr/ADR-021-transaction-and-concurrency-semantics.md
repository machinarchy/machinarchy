# ADR-021: Atomic state changes and optimistic concurrency

**Status:** Accepted on recommended defaults when Phase 6 was authorized  
**Date:** 2026-11-11

## Context

Host desired state, Operation lifecycle, audit history, and outbox notifications must survive crashes without exactly-once assumptions. SQLite serializes writes; PostgreSQL will support multiple application processes.

## Decision

Use short database transactions, conditional writes, unique constraints, and resource revision checks as the common concurrency contract. A desired Host change, its audit record, outbox event, and initial consumer-delivery rows commit atomically. Each Host has a desired `generation` and all-mutation `revision`; Operation claims use expiring leases plus fencing tokens. No transaction spans SSH, Agent, or broker calls. PostgreSQL uses `READ COMMITTED` by default, row locks for multi-row invariants, and `FOR UPDATE SKIP LOCKED` for queue-like delivery claims. SQLite uses WAL for reader/writer overlap, a bounded single-node write path, and finite busy retries. Use serializable PostgreSQL transactions only for invariants that cannot be expressed by constraints, CAS, or locks, with whole-transaction retries.

## Consequences

Processing is at least once. Consumers deduplicate by stable event ID, and controllers always reload current state. Duplicate/out-of-order notifications are harmless hints. Expired leases can be reclaimed, but a fenced stale worker cannot commit a result. SQLite remains single-node and should not be used as a shared multi-host file database.

## Alternatives considered

- Exactly-once processing: rejected because external effects cannot be atomically committed with the database.
- Long locks across remote calls: rejected because they hold database resources and amplify outages.
- Serializable isolation for all transactions: rejected due to unnecessary contention and retry complexity.
