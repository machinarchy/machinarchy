# ADR-036: Treat authentication, authorization, auditing, and secret protection as independent controls

**Status:** Accepted as the working design baseline when Phase 10 was authorized
**Date:** 2026-11-11

## Context

Machinarchy manages privileged infrastructure. A valid identity does not imply permission to read every Host, change every property, execute commands, resolve secrets, or suppress audit evidence.

## Decision

Enforce authentication, function authorization, object/resource authorization, property-level access, workload authorization, and audit as separate checks. Derive workspace from authenticated identity; scope every lookup and mutation. Secret resolution requires an explicit workload, purpose, resource binding, secret reference and version. Every security-sensitive action emits an audit record independently of operational logs.

## Consequences

API middleware is an early guard, not the only enforcement layer. Existing coarse owner/operator/reader roles must be refined for remote execution and resource scopes. Application services and persistence adapters must preserve tenant boundaries. Cross-resource IDs and secret references require negative authorization tests.

## Alternatives considered

- Treat valid JWT/API token as sufficient authorization: rejected.
- Rely on UI or unguessable IDs: rejected.
- Treat successful encryption as secret authorization: rejected.
- Use application logs as audit: rejected because logs are mutable/incomplete and may contain secrets.
