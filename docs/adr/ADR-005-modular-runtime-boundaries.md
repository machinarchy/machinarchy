# ADR 005: Keep a modular monolith with independently executable roles

- **Status:** Accepted
- **Date:** 2026-11-11

## Context

Machinarchy already has API, Control Manager, CLI, all-in-one, and placeholder Agent entry points. It shares one Go module and currently has no measured requirement that justifies the consistency and operational costs of microservices.

## Proposed decision

Keep shared domain and application logic in one modular Go codebase. Preserve independently executable API and Control Manager roles and the all-in-one runtime. The CLI continues to use the HTTP API. Add independently deployed services only when measured scaling, fault isolation, or ownership requirements justify them.

## Consequences

- Package boundaries and role-specific composition must be explicit; sharing a binary does not imply mixing reconciliation and operation execution.
- SQLite targets single-node/all-in-one use. Shared multi-process deployments use PostgreSQL.
- Microservices remain an evolution option rather than a Phase 1 target.
