# ADR-037: Deny outbound Host access by default in production

**Status:** Accepted as the working design baseline when Phase 10 was authorized
**Date:** 2026-11-11

## Context

Host registration, metrics, reconciliation, and Operations can cause outbound SSH. The current policy allows public and private IP ranges by default, which lets an inventory writer probe any reachable SSH port and creates an SSRF-like path into internal networks.

## Decision

Production policy begins with no permitted destination CIDRs or ports. Owners explicitly configure approved destinations, and the deployment egress firewall independently enforces them. Always deny metadata and unsafe special ranges. Resolve DNS once, validate every answer, dial the exact checked address, cap DNS work, and re-check at execution time. Changing egress policy is a high-risk authorized/audited configuration change.

## Consequences

Installations must configure intended Host networks before connecting. Private CIDRs are not implicitly trusted. Development may offer a documented convenience profile, but the production mode cannot inherit permissive defaults. Port restrictions are required in addition to address filtering.

## Alternatives considered

- Allow all private networks because the product manages private infrastructure: rejected because users and compromised accounts can pivot into control-plane-adjacent networks.
- Rely only on host-key pins: rejected because a reachable internal service can still be probed and a user may know/pin its key.
- Rely only on application policy: rejected because process compromise can bypass it; network egress controls are required.
