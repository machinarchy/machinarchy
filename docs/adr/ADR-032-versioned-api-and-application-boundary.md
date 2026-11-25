# ADR-032: Keep a versioned transport boundary over application commands and queries

**Status:** Accepted as the working design baseline when Phase 9 was authorized
**Date:** 2026-11-11

## Context

Machinarchy has an HTTP API consumed by a CLI and future clients. Current handlers expose some domain/store types directly, and endpoint behavior is not uniform.

## Decision

Preserve `/api/v1` and define stable request/response DTOs. Handlers authenticate, decode/validate transport input, call application command/query interfaces, and map results/errors. Business rules remain in application/domain services, not HTTP or CLI. Additive changes stay in v1; incompatible semantics require a new major API path with migration support.

## Consequences

Transport representations can evolve independently of Go domain structs. OpenAPI becomes the public contract. Existing routes can be migrated incrementally; a wholesale endpoint rewrite is not required.

## Alternatives considered

- Serialize domain entities indefinitely: rejected because internal model changes would break clients and could expose fields accidentally.
- Version every minor release in the URL: rejected because additive compatible changes do not require a new major contract.
- Put business logic in transport handlers: rejected because CLI, workers, and API must share use cases.
