# M7 - Final independent review (2026-11-26)

Read-only review of the CLI redesign (M1-M6) on branch `ai/p7-final-review`. No code was changed in this phase. Evidence is from the checks named below; nothing here is claimed beyond them.

## 1. Verification (7.1)
gofmt clean, build, `go vet`, `golangci-lint` (0 issues) and `go test -race ./...` pass. Security tests and fuzz seeds re-run under `-race` (7.4): pass.
**Not run:** Postgres-backed store tests, pty layers (O-15, S-16), SSH lab automation, real agent, live HA, Windows, nightly CI layers.

## 2. Original audit (7.2)
28 Critical/High findings re-checked: fixed, except residuals: R-10 coverage numbers, A-08 (environments handler still takes `domain.EnvironmentStore`), A-12 (200-target cap, no scale re-test), A-07 (`go.mod` still uses a `replace` to the agent protocol; protocol untagged). HA, real agent and Postgres contracts not re-verified live.

## 3. Breaking changes
Owner rule "no legacy support" supersedes the alias plan in `08-migration.md`. Users must change:
- **Commands removed (exit 2, nothing sent):** `get hosts|profiles|operations|tokens`, `create host|profile|token|agent-token`, `delete ...`, `label host`, `trust host`, `cancel operation`, `logs operation`, `config set-cluster|set-context|use-context|get-contexts|current-context|set-credentials`. Replacements: `hosts`, `access`, `run`, `tokens`, `hosts add|remove|tag|trust|enroll`, `switch`, `whoami`, `login`.
- **Flags removed:** `run -l`, `--all`, `--exclude`, `--wait` legacy mode (use `--on`, `--except`); `--token` (use `login --with-token`).
- **Config:** old config is refused, not migrated; sign in again. Credentials are stored separately; a credentials file with loose permissions stops every command.
- **Environment:** `MACHINARCHY_CLI_*` variables gone; `MACHINARCHY_TOKEN` is the CI path.
- **Behaviour:** `--yes` never approves SSH host-key trust; protected envs need `--confirm-env`; destructive commands need the typed name; first host-key trust is done by typing `yes` (owner decision, F8); `--on all` asks for the host count.
- **Kept by decision:** `get agent-tokens`, `runtime-config`, `secret`, `version`, global `-n`.

## 4. Security (7.4)
All tracked S and T items with tests pass (table in the working notes). Gaps: S-16 needs a pty and is untested; `access show -o json` redaction is not in the secret-leak test; S-19..S-21 wording partly asserted; completion 800 ms budget not re-measured. Credentials remain a plaintext 0600 file (keychain Q5 open). The trust fingerprint comes from the controller's network position (stated limit, T4). Protected envs are local-only until C-02.

## 5. Usability and scorecard (7.5)
- **A-01:** works end to end (scripted run and owner lab session). **Time under 5 minutes and a fresh-human run are unmeasured.** A-01 is release-blocking per `09-acceptance-tests.md` Z.
- **Targets missed:** 14 top-level commands (target 10); unreachable-server error has no `Why:`; beginners meet 2 concepts (server, environment); no typo suggestion at depth 2.
- **Partly met:** multi-host run proven on 1 host live; F9 (a run proceeds with `no env` selected) open.
- **Unmeasured:** time to first managed thing, completion budget.

## 6. Open findings
- F7.3-1: removed commands do not name the replacement (generic `unknown command`).
- F7.3-2: `logs operation 1` suggests `login operation 1` (`suggest.go`); should point to `run ID --follow`.
- F7.3-3: `config set-credentials --token` does not name `login --with-token`.
- F3 (HTTPS wording on loopback HTTP), F9 (`no env`).

## 7. Controller work still needed
| ID | Need | Effect until done |
|---|---|---|
| C-01 | `environmentId` on host create/update | `hosts move` refuses; env questions skipped |
| C-02 | server-held `protected` envs + `confirmEnvironment` | local-only guard |
| C-03 | role/expiry for tokens on `GET /me` | "not reported by this server" |
| C-05/C-06 | target cap decision; operation summary/targets endpoints | no runs over 200 hosts; large runs download everything |
| C-07 | structured agent install info | CLI prints the four-value fallback |
| C-09 | document `GET /agents` | `revoke-agent` needs `--agent-id` |
| C-11 | server version/API range | decode-failure message only |
Also: `internal/hostaccess/reconciler.go` still says `machinarchy trust host` (should be `hosts trust`); gRPC/gateway-only agent transport is Phase 8 (needs owner approval); agent-repo `install.sh` not written; Windows (Q6) undecided.

## 8. Recommended next steps
1. Run A-01 with two people who never used the product; record time (blocks release).
2. Fix F7.3-1..3 and F9 (CLI-only, small).
3. Decide the scorecard targets: change the spec (14 commands, 2 concepts) or the CLI.
4. Map or write the unmapped S-series tests; add a pty test for S-16.
5. Land C-02 and C-01 in the controller, then C-07 with Phase 8.
6. Run Postgres, SSH-lab and real-agent suites in CI before release.
