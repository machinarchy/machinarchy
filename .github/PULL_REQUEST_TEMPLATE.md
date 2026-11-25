<!-- Prefer a Conventional Commit PR title (feat:, fix:, docs:, refactor:, etc.). -->

## Summary
Describe what changed and why. For changes to host enrollment, SSH execution,
agent orchestration, persistence, or security boundaries, explain the design
and threat-model implications.

## Related issue
Fixes #

## Change type
- [ ] `feat` — new capability
- [ ] `fix` — bug fix
- [ ] `refactor` — internal change without intended behavior change
- [ ] `docs` — documentation only
- [ ] `chore` / `build` / `ci` — maintenance or delivery pipeline
- [ ] Breaking change (explain migration/compatibility impact)

## Verification
- [ ] `make fmt-check`
- [ ] `make vet`
- [ ] `make lint`
- [ ] `make test`
- [ ] `make test-postgres` (when PostgreSQL persistence/schema is affected)
- [ ] Verified relevant Docker build/runtime behavior (when Docker files change)
- [ ] Added or updated tests for the behavior changed

## Operational/security impact
- [ ] No configuration or migration required
- [ ] Configuration or migration changes are documented
- [ ] Secrets, permissions, and authentication boundaries were reviewed
- [ ] Logs and errors do not expose credentials or sensitive data
