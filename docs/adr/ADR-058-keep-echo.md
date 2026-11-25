# ADR-058: Keep Echo; track its fixes instead of migrating

**Status:** Accepted
**Date:** 2026-11-25

## Context

AUDIT D-14 / S-22: Echo v4.16.4 had two published vulnerabilities, GO-2026-6875 (the proxy middleware forwards `X-Real-IP`) and GO-2026-6872 (Echo's rate limiter trusts `X-Forwarded-For`). Neither was exploitable here: the servers take the client IP from `echo.ExtractIPDirect`, or from `ExtractIPFromXFFHeader` with only the configured proxies trusted (AUDIT S-10), the rate limiter is our own, and the proxy middleware is not used. Both were allowlisted until 2027-02-16 with that reasoning. AUDIT/10 Phase 21 asked for a decision: renew the allowlist, or migrate to `net/http` with the Go 1.22+ `ServeMux`.

Echo is used in 45 packages and files: routing, binding, middleware, error handling, and the swaggo annotations the API docs are generated from.

## Decision

Keep Echo and upgrade it when fixes appear. Echo v4.16.6 fixes both findings: `govulncheck ./...` reports no vulnerabilities, and `make vuln` reports both allowlist entries as no longer needed.

A migration to `net/http` is reconsidered only if Echo stops shipping security fixes, or if a vulnerability affects code paths this project uses.

## Consequences

- The dependency stays; its vulnerabilities are caught by `make vuln` and CI's govulncheck, as before.
- No handler, middleware or API-docs change.
- The two allowlist entries can be removed (`make vuln` asks for it).

## Alternatives considered

- **Migrate to `net/http` now:** removes a dependency, but touches every handler, the binding and validation code, and the generated API docs (2–3 days) for no security gain once the upgrade fixes the findings.
- **Renew the allowlist without upgrading:** unnecessary now that fixed versions exist.
