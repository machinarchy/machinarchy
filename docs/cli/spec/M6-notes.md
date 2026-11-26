# M6 notes

## 6.3 Controller version endpoint: PROPOSAL for the owner (not implemented)

Checked 2026-11-26: the controller has no version endpoint and no `X-Machinarchy-Version` header (`rg` over `internal/` and `cmd/` found none); `GET /healthz/ready` reports the process only (`06-api-matrix.md`).

Proposal (C-11): `GET /api/v1/version`, public or any role, returning `{"version":"1.4.0","apiMin":1,"apiMax":2}` from `internal/buildinfo` plus a constant for the API generation; optionally the same value as `X-Machinarchy-Version` on every response. The CLI would read it once per command on the first failure to decode (not on every call), compare against its own range, and print `This server speaks API version X; this CLI supports Y` with the update hint (O-20, exit 4). Cost: one handler, one constant, one test; no data model change. Needs owner approval before any controller change.

Until then: decode failures split into "wrong shape" (update the CLI) and "not JSON" (check the address); `commands.md` "Supported servers" says same release. K-04 as written (old commands keep working) is not implemented by project rule (no legacy); see M4-notes.

## 6.6 Hardening

**Fuzz targets (CLI).** Sanitiser `FuzzClean`, `FuzzStream`; target parser `FuzzParse`; normaliser `FuzzNormalise`; v1/v2 config reader `FuzzReadDocument` (`config/fuzz_test.go`); API error decoder `FuzzNewError` and success decoder `FuzzDecodeResponse` (`api/fuzz_test.go`). Each ran 10-20 s locally with no finding. Wired into `make fuzz` via `FUZZ_TARGETS` in `mk/checks.mk` (owner approved).

**Scans.** `make vuln` (govulncheck with the repo allowlist): no unaccepted vulnerabilities in called code. gosec runs inside golangci-lint (0 issues).

**Review against `04-security.md` (evidence = test names under `internal/cli`).**

| Threat | Evidence |
|---|---|
| T1 password | `TestLoginWithPasswordFromStdin`, `TestWrongPassword`, `TestLoginDoesNotFollowRedirects` |
| T2 token leak | `TestTokenIsNeverInTheProcessList`, `TestNoCommandErrorLeaksTheToken`, `TestNoSecretIsEverVisible`, `TestSecretsAreNeverFlags`, `TestTokensCreatePrintsTheTokenOnlyOnce` |
| T3 at rest | `TestLaxPermissionsAreRefused`, `TestLooseCredentialPermissionsStopTheCommand`, `TestCredentialNeverPrintsItsSecret`, `FuzzReadDocument`. Plaintext file remains (keychain Q5 open) |
| T4 host key | `TestHostsTrustNeverFollowsYes`, `TestHostsTrustTypedPrefix`, `TestHostsTrustFingerprint`, `TestHostsTrustReplace`. Limit stays: fingerprint comes from the controller |
| T5 argv | as T2; `run` text warning is help only |
| T6 TLS/HTTP | `TestHTTPClientTrustsAConfiguredCA`, `TestEndpointCAFileErrors`, `TestRedirectsAreNotFollowed`, `TestLoginRemembersInsecureHTTPPerServer`, `TestIsLoopbackHost` |
| T7 impersonation | `TestDecodeFailureSaysWhetherToUpdateOrCheckTheAddress`, `TestSavedCredentialsNeverGoToAnotherServer`. Env/flag server-differs warning not re-verified |
| T8 escapes | `TestErrorTextIsSanitised`, `TestServerMessagesAreSanitised`, `TestRunOutputIsSanitised`, `TestHostNamesInTheRenderedOutputAreSanitised`, `FuzzClean`, `FuzzStream`, `FuzzNewError` |
| T9 expired/forbidden | `TestExpiredAndRevokedCredentials`, `TestOwnerOnlyActionWithAnOperatorToken` |
| T10 malformed responses | `FuzzDecodeResponse`, `FuzzNewError`, client size/redirect tests; install blob is built by the CLI (`TestHostsEnroll*`) |
| T11/T12 production | `TestHostsInProtectedEnvNeedTheEnvName`, `TestRunCancelInAProtectedEnv`, `TestRunRetryInAProtectedEnv`. Protected is local-only (C-02) |
| T13 wrong place | `TestSavedCredentialsNeverGoToAnotherServer`, `TestOwnerCommandsUseTheSessionEvenWithATokenSaved` |
| T14 CI | `TestHostsTrustNeverFollowsYes`, CI tests from 6.2 |
| T15 binary | README checksum/attestation steps (6.4); no self-update. Protocol pin open (no `protocol/v0.1.0`) |
| T16 completion | `TestCompletionNeverFailsLoudlyOrOffersUnsafeNames`; 800 ms budget and no-prompt rule not re-measured |

Not covered by evidence: pty behaviour, Windows permission warning (skipped on Windows), real controller TLS.

**Removal stubs (R+1).** Not prepared: the no-legacy rule removed old commands outright, so there is no R/R+1 window and K-13 text is not implemented; old commands fail as unknown (`TestOldTrustHostCommandIsGone`).

## 6.7 Full acceptance run (2026-11-26)

**Run (all pass):** gofmt (no files), `go vet ./...`, `golangci-lint run ./...` (0 issues), `go test -race -count=1 ./...`, `make smoke` (end to end against the monolith), `make vuln`.

**ID coverage.** 179 test IDs are defined in `09-acceptance-tests.md`. A search of `internal/cli`, `cmd` and `scripts` finds 109 referenced by ID in test code. 70 are **not referenced by ID**; some are covered under other test names (for example S-02/S-04 by the T2/T3 tests listed in 6.6), but that mapping was not verified per ID, so they are reported as **unmapped**, not as passing:

A-01 A-02 A-03 B-01 B-02 B-04 B-06 B-07 B-08 B-11 B-13 B-18 B-21 C-02 C-03 C-04 C-06 C-10 C-12 G-16 G-18 G-20 H-07..H-12 K-01..K-03 K-05..K-09 K-11..K-15 O-01 O-02 O-15 O-20 O-32..O-38 S-02 S-04 S-05 S-10 S-11 S-12 S-15 S-16 S-17 S-18 S-19 S-20 S-21 S-29 S-31 U-09 U-10 U-12.

**Release-blocking set.** G-04, G-05 and K-04 are referenced by tests that pass (K-04 only as the wrong-shape/not-JSON message; the "old commands keep working" part is not implemented by the no-legacy rule). **Unmapped S-series IDs above must be mapped or written before release.** A-01 (five-minute test) is **not run**: it needs a clean machine and a lab server (step 6.8).

**Not run:** Postgres store tests, pty/`script` layers (P: O-15 second Ctrl+C, redrawn progress, hidden-input prompts), SSH lab (L), real agent and NATS (A-02, C-07/Q1), Windows (Q6), CI nightly layers.
