# ADR 0001: Keep a modular monolith and disable production self-registration by default

- **Status:** Accepted
- **Date:** 2026-10-26

## Context

Machinarchy has separate API and control-manager binaries plus a combined monolith, with domain/service, transport, and storage boundaries. It also has public self-registration and authenticated APIs whose host, environment, job, and history records are not scoped to users or organizations. Splitting into more services would not address this authorization gap and would add deployment and consistency costs.

## Decisions

1. Continue with the modular monolith and keep existing process roles independently deployable. Add services or distributed components only to meet a measured requirement.
2. Make self-registration default to enabled in development and disabled in production. Operators may explicitly set `MACHINARCHY_SIGNUP_ENABLED=true` when registration is intended.
3. Treat the signup toggle as exposure reduction; it is not the authorization boundary.
4. Design workspace lifecycle, roles, and legacy data migration together before adding resource-owner columns or claiming multi-user isolation. This design requirement was implemented by ADR 0002.

## Consequences

- Local development remains convenient while production deployments no longer accept new accounts by default.
- Production bootstrap requires an intentional signup configuration step; a dedicated owner bootstrap workflow remains future work.
- At the time this decision was made, existing authenticated accounts remained equally privileged. Workspace-scoped authorization and API tokens were later implemented by ADR 0002; member management and exhaustive endpoint tests remain open.
- Existing database, NATS, and SSH boundaries remain intact; no dependencies or services are added by this decision.
