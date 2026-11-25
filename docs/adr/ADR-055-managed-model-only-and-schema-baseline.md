# ADR-055: The managed resource model is the only host model; the schema is one baseline

**Status:** Accepted (owner decision, 2026-11-17; implemented 2026-11-18)

## Context

Two host models existed side by side: the SSH-shaped legacy `hosts`/`jobs`/`runs` stack with its own API, CLI commands, in-API executor and duplicated SQL adapters, and the connector-neutral managed model (`managed_hosts`, Access Profiles, Operations). Every feature had to pick one, and most audit findings (AUDIT A-01 to A-03, A-06, A-08, A-12) were consequences of the split. The project is in development and has no external API clients or data worth migrating.

## Decision

- The managed model is the only host model. The legacy API (`/api/v1/hosts*`, `/runs*`, `/fleet`, `/jobs`, `/history`), services, domain types and tables are deleted without a compatibility facade. The CLI uses `/api/v1/managed`.
- The 14 per-dialect migrations are squashed into one baseline per dialect. During development the baseline may be squashed again; the migrator refuses a database from an older chain (`store.ErrIncompatibleSchema`) and it must be recreated.
- Both dialects store timestamps as RFC 3339 text and share one store implementation per area (`store/accounts`, `store/resources`, `store/management`).

## Consequences

- Existing development databases must be deleted (SQLite) or dropped (PostgreSQL).
- ADR-0004 (legacy command execution) and the legacy-host parts of ADR-009/ADR-010 are superseded; ADR-052 (expand/contract upgrades) applies again from the first release with real data, not during development.
- After the first production release, schema changes go through additive migrations on top of the baseline.
