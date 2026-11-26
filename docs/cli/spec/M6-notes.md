# M6 notes

## 6.3 Controller version endpoint: PROPOSAL for the owner (not implemented)

Checked 2026-11-26: the controller has no version endpoint and no `X-Machinarchy-Version` header (`rg` over `internal/` and `cmd/` found none); `GET /healthz/ready` reports the process only (`06-api-matrix.md`).

Proposal (C-11): `GET /api/v1/version`, public or any role, returning `{"version":"1.4.0","apiMin":1,"apiMax":2}` from `internal/buildinfo` plus a constant for the API generation; optionally the same value as `X-Machinarchy-Version` on every response. The CLI would read it once per command on the first failure to decode (not on every call), compare against its own range, and print `This server speaks API version X; this CLI supports Y` with the update hint (O-20, exit 4). Cost: one handler, one constant, one test; no data model change. Needs owner approval before any controller change.

Until then: decode failures split into "wrong shape" (update the CLI) and "not JSON" (check the address); `commands.md` "Supported servers" says same release. K-04 as written (old commands keep working) is not implemented by project rule (no legacy); see M4-notes.
