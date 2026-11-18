# ADR-039: Give API, Control Manager, broker clients, and Agents distinct workload identities

**Status:** Accepted as the working design baseline when Phase 10 was authorized
**Date:** 2026-11-11

## Context

API and Control Manager have different responsibilities, while a future Agent may report observations and receive tasks. Sharing one database/broker credential expands the impact of any single process compromise.

## Decision

Assign separate, rotatable workload credentials and least-privilege database/broker permissions to API and Control Manager. Require authenticated TLS and subject-level broker ACLs. Future Agents use unique short-lived mTLS identities bound to one Host and explicit capabilities, with enrollment, rotation, revocation and replay protection. mTLS proves workload identity/channel possession; application authorization still decides the permitted action.

## Consequences

Deployment becomes more operationally involved but compromise blast radius decreases. Current NATS/Store bootstrap must gain credentials/role support before being considered secure for distributed production. Agent traffic remains disabled until its identity lifecycle is implemented and tested.

## Alternatives considered

- Shared API/worker/Agent secret: rejected because credentials are reusable across trust boundaries.
- Treat mTLS certificate as all required authorization: rejected because certificate identity does not grant arbitrary resource or secret access.
- Invent a generic Agent protocol before requirements exist: rejected.
