# ADR-035: Keep CLI commands thin and use the HTTP API for all server operations

**Status:** Accepted as the working design baseline when Phase 9 was authorized
**Date:** 2026-11-11

## Context

The CLI is an external interaction surface. Direct database access or duplicate target/authorization logic would diverge from server behavior and bypass auditing.

## Decision

CLI commands resolve local context, parse input, call typed API clients, format output, and map failures to stable exit codes. Runtime configuration, resources, Operations, and Secrets are managed only through API endpoints. Secret input uses stdin/protected files; the CLI never receives secret plaintext in responses. Long-running waits poll the Operation API; local wait interruption does not imply remote cancellation unless explicitly requested.

## Consequences

The CLI can be used against remote deployments and stays aligned with server authorization. API client surface grows with commands. Current read-only Host client can be extended incrementally. Existing local CLI config remains separate from server runtime configuration.

## Alternatives considered

- Let CLI open local SQLite directly: rejected because it fails for PostgreSQL/distributed deployments and bypasses application policy.
- Reimplement business validation in Cobra commands: rejected because rules drift and other clients still need server validation.
