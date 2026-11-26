# 10 - Roadmap

> Six milestones. Each is written so another agent can implement it without reinterpreting the product: it names the scope, the files and packages, the dependencies (including controller work from `06-api-matrix.md`), the acceptance tests from `09-acceptance-tests.md` that must pass, and what is explicitly **not** in it. Order is M1 -> M6; M2 and M3 can overlap after M1. Nothing here is estimated in days because the controller work has not been scheduled.

**Working rules for every milestone**
- Conventional commits; the milestone that introduces the new grammar (M2's first user-visible command) uses `feat!:` with the `BREAKING CHANGE:` footer of `08-migration.md`.
- Update `ChangeLogAI.md` in the same change (repository rule); record only checks that were actually run.
- No new dependency except as allowed by `05-architecture.md` section 10.
- Every new user-facing string goes through `msg`; every printed server/host string through `sanitize`.
- A milestone is done when its listed tests pass **in CI** and the existing CLI tests still pass at or above today's coverage.
- Old commands keep working through `deprecate` from the moment their replacement exists.

## Controller work and where it blocks

| ID | Needed by | Blocks |
|---|---|---|
| C-03 role and expiry on `/me` | M2 (display only) | nothing; shows `unknown` |
| C-01 host environment | M3 | env assignment in `hosts add` and `hosts move` |
| C-02 protected envs | M3 | server enforcement (local guard ships instead) |
| C-07 agent install info (+ Q1) | M3 | **the agent path of the five-minute test** |
| C-09 agent list shape | M3 | name-based `hosts revoke-agent` |
| C-05, C-06 | M4 | runs above 200 hosts |
| C-10, C-11, C-12, C-14, C-15 | M4/M6 | exactness, not function |
| C-08 capability catalog | M4 | nothing |

---

## M1 - Foundations

**Objective.** The shared machinery every later milestone relies on, with no visible change in what the CLI does yet except better errors.

**Scope**
1. `internal/cli/msg`: message catalogue, `Message`/`Error` builders, plain and lore rendering, lint tests (U-01, U-04, U-06).
2. `internal/cli/ui`: `Terminal` interface (TTY detection via `x/term`, width, colour, symbols, `NO_COLOR`, `--plain`), line prompt, hidden prompt, numbered choice, typed confirmation, progress line; injected reader and writer.
3. `internal/cli/sanitize`: the sanitiser of `04-security.md` section 7 and the completion allowlist; applied in `output.Printer.Table`, `logs`, `trust` output and `api` error handling (extending `api/errors.go:176`).
4. Error format: `cli.reportError` prints the four-part form from `clierr` + `msg`; `clierr` gains codes 5, 8, 9; `-o json` error shape on stderr.
5. Global options: `--plain`, `--dry-run`, `--confirm`, `--confirm-env`, `MACHINARCHY_*` variable names with `MACHINARCHY_CLI_*` fallback and warning; `--yes` still flag-only.
6. `internal/cli/group`: the aggregator, `CompressHosts`, `Normalise`, drift logic; golden infrastructure and the 200/500/5,000 generators.
7. `internal/cli/deprecate`: alias mechanism and the alias table skeleton; hidden-from-help and hidden-from-completion handling; `MACHINARCHY_NO_DEPRECATION_WARNINGS`.
8. `internal/cli/complete` skeleton and static completion for commands and flags; the completion directive that prints nothing on error.
9. `clitest` helper (fake streams, terminal, clock, API) and the import-graph test.

**Dependencies.** None on the controller.

**Deliverables.** The packages above with tests; `docs/cli/spec` unchanged; no command renamed yet.

**Acceptance.** G-01..G-19 (aggregator level), S-13, S-14, S-15, S-16, S-17 (sanitiser level), U-01, U-04, U-05, U-07, U-08, U-11, N-03 (new codes), C-03, C-04, C-05 (static, empty-on-error), `go vet`, existing tests unchanged.

**Tests.** Table tests and golden files; fuzz test for the sanitiser (arbitrary bytes in, no ESC/C1/bidi out, valid UTF-8 out); fuzz test for `Normalise`; property test that grouping is order-independent (G-12).

**Risks.** Over-design of the aggregator (keep it pure and small); the `ui` abstraction growing a framework (limit to what `03-ux.md` uses); width calculation for non-ASCII host names (accepted limitation, `05-architecture.md` section 10).

**Excluded.** Any new command, any change to credential storage, any API client change beyond what the sanitiser needs.

---

## M2 - Servers and authentication

**Objective.** Sign in with a password or a token, stay signed in safely, see who you are, switch servers, sign out, with credentials stored behind an interface and old configs converted.

**Scope**
1. `internal/cli/cred`: `Store` interface, file backend (`credentials.yaml`, 0600, atomic, refuses lax permissions), `Credential` with redacting `String()`.
2. `internal/cli/config` v2 (`config.yaml` with servers, active server/env, per-server CA and `insecureHTTP`, local protected-env guards) and the v1 reader plus automatic conversion and backup (`05-architecture.md` 4.5); `config path`, `config show` (hidden).
3. API client: `Endpoint` struct, per-server CA pool, never skip-verify, new methods `Me`, `Sessions`, `Logout`, `RevokeSessions`, token list/create/revoke; typed errors with the `code` field.
4. Commands: `login` (password, `--with-token`, `--stay-signed-in`, `--ca-file`, `--insecure-http`, `--name`), `logout [--everywhere]`, `whoami`, `switch`, `tokens`, `tokens create`, `tokens revoke`.
5. Credential resolution with R3 (server-bound credentials) and the precedence of `02-commands.md` 2.6; owner-needed local refusal.
6. Aliases for the old commands of this area (`login --server ... --context ...`, `config set-*`, `config use-context`, `config get-contexts`, `config current-context`, `get tokens`, `create token`, `delete token`) with legacy behaviour unchanged; `--token` warning.
7. Optional: keychain backend behind a build tag **only if** the checklist of `05-architecture.md` 4.4 passes; otherwise recorded as not done with the reason.
8. Hidden `completion` command wiring (`completion bash|zsh|fish`), `completion install`, and server-name/token-name completion.

**Dependencies.** None required. C-03 improves the token display; C-04 is optional.

**Deliverables.** Working new auth flow against a live lab controller; migration of a real v1 config.

**Acceptance.** B-01..B-24 (B-21 after C-03), S-01..S-05, S-18, S-27, S-28, S-29, K-01..K-03, K-08, K-09, K-11, K-12, C-08, C-09, C-12, N-06, N-10..N-12, U-09, A-03 (login part).

**Tests.** `clitest` flows with fake terminal for prompts; a process-listing test for B-23; credential file permission tests; config migration golden tests (a corpus of v1 files).

**Risks.** Migration bugs that lose a credential (mitigated by the byte-exact backup and K-01..K-03); keychain library failing the checklist (then not shipped); platform differences in `UserConfigDir`; the owner-session-versus-device-token interplay (`03-ux.md` 3.3) confusing users: needs the usability session.

**Excluded.** Hosts, envs, runs, brand screens; renaming `logs`/`get`/`create` (their aliases arrive with their milestones); changing server-side auth.

---

## M3 - Envs, access and hosts

**Objective.** A person can add their first machine through a guided flow (SSH and agent), see readiness, tag, move, trust, and remove it safely, in protected or unprotected envs.

**Scope**
1. API client: environments create/delete, access profile create/delete/get, secrets create (for credentials), host create/get/delete/labels/trust, agents (`ControllerKey`, enrolment tokens, revoke, list).
2. `envs` (list, add, use, remove) with local protected guard and typed confirmation; server enforcement when C-02 exists (`confirmEnvironment`).
3. `access` (list, add wizard, show, remove) including the credential secret creation, plain-words host-key policy, and the "not done" lines.
4. `hosts` (list with the readiness mapping of `02-commands.md` 3.8.1, show, tag, move, trust, enroll, revoke-agent, remove) and the **guided `hosts add`** for SSH and agent with its complete flag twin, back-navigation, fingerprint flow, wait loop with elapsed time, and the printed non-interactive command.
5. Install command built by the CLI from C-07's structured fields (validation per `04-security.md` T10), or the "four values" fallback.
6. Host-key flows: first trust (typed prefix), changed key (two confirmations), non-interactive `--fingerprint`.
7. Host removal semantics of `03-ux.md` section 6 (four separate things) and the "not done" output.
8. Aliases for the old commands of this area (`get hosts`, `get profiles`, `create host`, `create profile`, `create agent-token`, `delete host|profile|agent-token`, `label host`, `trust host`) with legacy output unchanged and the safety changes B5/B10.
9. Dynamic completion: hosts, envs, access names.

**Dependencies.** C-01 (env assignment; without it the "which env?" question is skipped and `hosts move` refuses), C-02 (server-side protection; without it the local guard), **C-07 and Q1 (agent install; without it the fallback)**, C-09 (name-based revoke), C-14, C-15 (nice).

**Deliverables.** `hosts add` reaching `ready` in the lab for an SSH host (and an agent host once C-07 exists).

**Acceptance.** A-01, A-02 (C-07), H-01..H-12, S-06..S-12, S-19..S-26 (host/env parts), S-31, O-03..O-05, O-32..O-38, K-04 (rows for this area), K-10, C-01, C-02, C-06 (names part), U-02, U-03.

**Tests.** Fake-controller flow tests including the changed-key path; lab tests with the three SSH containers; hostile-name fleet; usability session for A-01.

**Risks.** The fingerprint's provenance (controller vantage) and user education: wording must stay honest (`04-security.md` T4); wizard complexity (keep every question backed by a flag); reading `status.conditions` reasons that are not enumerated in the contract (C-15); an operator who cannot create hosts gets an owner-needed message.

**Excluded.** Bulk import of hosts; editing an access entry; agent upgrade; connectivity probe (C-13); any change to how the controller stores credentials.

---

## M4 - Runs

**Objective.** One `run` command for targeting, preview, grouped results, following, retry-failed, cancel and capabilities, with exact semantics about what the controller guarantees.

**Scope**
1. `internal/cli/target`: `--on`/`--except` parser, glob expansion, env scope, conflict errors (`02-commands.md` 2.4, O-23..O-26).
2. `run` execution flow (`03-ux.md` 7.1): preview, blast-radius confirm (T1/T2, protected env, `--on all` count), submit with `expectTargetsHash`, wait by default, attached/detached rule (900 s), `--detach`, `--wait-timeout` (exit 5).
3. Watch loop with progress line (TTY) / periodic lines (non-TTY), backoff, resume messages.
4. Grouped result via `group` for runs up to 200 hosts; `--no-groups`, `--groups`, `--fail-on-drift`; exit codes 0/8/9.
5. `run` (list), `run ID`, `run ID --host`, `--stream`, `--raw` rule, `--follow`.
6. `--retry-failed` with unknown-outcome and cancelled exclusions; `--cancel` with the guarantees table; ambiguous-submit message.
7. Capability form `--do NAME key=value...`, `--input-json`; static capability help text; no value rewriting.
8. Prefix resolution of run IDs (`r-` + prefix), `C-10` if present.
9. Aliases: legacy `run -l/--host/--all/--exclude` keeps legacy semantics; `get operations`, `cancel operation`, `logs operation`.
10. Completion: `--on`, `--except`, run IDs, `--host` for a run, `--do`.
11. Refusal above 200 hosts (O-22).

**Dependencies.** None required for up to 200 hosts. C-05 + C-06 for larger runs; C-12 for an exact "did it go through"; C-10, C-11, C-08 improve results.

**Deliverables.** All transcripts of `03-ux.md` sections 7 and 9 reproduced by golden tests; a lab run on three SSH hosts with one down.

**Acceptance.** O-06..O-31, G-01..G-20 (end-to-end with the fake API; G-04/G-05 through the aggregator at the library level and through the CLI with a fake of C-06), S-22..S-26 (run parts), S-30, N-01..N-05, N-07, N-08, K-04 (run rows), K-05..K-07, C-06, C-07, U-10.

**Tests.** Fake API with time control; signal tests under a pty (O-14..O-16); golden files; a memory test at 5,000 synthetic hosts for the aggregator (G-05); a large-run test against the fake of the proposed C-06 contract, re-run live when it ships.

**Risks.** Overstating cancel (`ADR-030`) - mitigated by the guarantees table and O-12; poll cost for 200 targets with 64 KiB outputs against the 16 MiB response cap (audit F-10) - mitigated by C-06, and until then by fetching only needed output; an operator choosing `--retry-failed` for a non-idempotent command - mitigated by always showing the command and the unknown-outcome exclusion; drift heuristics producing noise (G-08).

**Excluded.** Client-side batching above 200; scheduling; run history export; running programs on agent-only hosts; any change to how the controller executes.

---

## M5 - Home screen and brand

**Objective.** The first screen after install tells a newcomer the one next step, and the CLI has a consistent voice.

**Scope**
1. Home screen (`03-ux.md` section 1): variants for not signed in, expired, fine, attention needed, no hosts, unreachable; suggestion priority; `-o json`; 3 s budgets and degradation.
2. Logotype and palette (`07-brand.md`), symbols with ASCII fallbacks, colour by role, `NO_COLOR`, non-UTF-8 detection.
3. Voice: the catalogue entries for all flows; lore placement rules; `--plain` fully implemented across all commands.
4. Help: group layout (Get started / Everyday / Account / Advanced), `Examples:` in every command, "did you mean" with full corrected command at any depth, the completion hint in the help footer.
5. First-run hints: one-time completion line after the first login; next-step lines on every success.

**Dependencies.** None. Uses `/me`, `/sessions`, hosts and operations lists.

**Deliverables.** Screens matching the transcripts; `--plain` audit of every command.

**Acceptance.** U-02..U-10, N-10..N-12, home fixtures of U-10, A-03, plus a manual review of the transcripts in `03-ux.md` against real output.

**Tests.** Golden files for every home variant in rich and plain modes; the lore allowlist test; width tests.

**Risks.** Lore leaking into serious text (U-04 guards it); width of symbols in some fonts (alignment on text only); the home screen's extra requests slowing startup (budget and parallel requests).

**Excluded.** Localisation; themes; animated output; a TUI.

---

## M6 - Automation, docs and hardening

**Objective.** The CLI is safe and predictable in CI, documented for a newcomer, released with checksums, and the whole acceptance suite is green.

**Scope**
1. **JSON/YAML stability:** schema goldens for every `-o` output (N-01, N-02); document the compatibility promise (fields are added, never renamed or removed) in `docs/cli`.
2. **CI behaviour:** `--no-input`, `MACHINARCHY_TOKEN`, stdout/stderr discipline, error JSON on stderr, quiet mode, dry-run everywhere (N-04..N-09, S-30).
3. **Release:** the `publish-assets` job keeps linux/darwin amd64/arm64 with checksums and attestation; decision on a Windows build (Q6); install steps in the README with checksum and attestation verification; pin the agent protocol dependency once `protocol/v0.1.0` exists (audit F-22, D-4); `machinarchy --version` documented.
4. **Version compatibility:** use C-11 if present; otherwise decode-failure messaging; document the supported server range.
5. **Docs:** rewrite `docs/cli/README.md` quick start; mark the old guides legacy with replacements; migration notes; troubleshooting entries generated from the message catalogue; the `docs/cli/spec` files stay as design record with a pointer to the implemented behaviour.
6. **Removal stubs** for R+1 prepared (`08-migration.md` section 4) but not enabled.
7. **Hardening:** fuzz targets (sanitiser, target parser, normaliser, v1 config reader, API error decoder); `govulncheck`, `gosec` through the existing linter; a security review pass against `04-security.md` T1..T16.
8. **Full acceptance run** (`09-acceptance-tests.md` Z) and two recorded usability sessions.

**Dependencies.** Everything above. C-11, C-12, C-14, C-15 improve results but do not block.

**Deliverables.** A release candidate with the green suite, the docs, and the usability results.

**Acceptance.** Section Z of `09-acceptance-tests.md`: all of A, B, S, O, G, N, K, C, H, U; the release-blocking set (S-series, K-04, G-04, G-05, A-01) must pass.

**Tests.** The union of the previous milestones plus fuzzing for a fixed budget in CI and nightly lab runs (P, L layers).

**Risks.** Late discovery of compatibility breaks (K-04 is run on every pull request from M2 on, not only at the end); docs drifting from behaviour (examples in docs are executed by U-02); usability results contradicting the spec (the spec is then amended, not the test).

**Excluded.** Self-update; package-manager distribution (Homebrew, apt); telemetry; any feature not in this spec.

---

## Open questions that block implementation

Only questions that stop work are listed.

| # | Question | Blocks | Needed from |
|---|---|---|---|
| Q1 | How does a new agent obtain its **NATS address and bootstrap credential**, and may the controller expose them (or a way to obtain them) to an owner through the API (C-07)? | the agent path of the five-minute test and A-02; the install command | controller and agent owners |
| Q2 | Is C-01 (environment on host create/update) accepted, and is it acceptable that a host without an env stays `none`? | env assignment in M3 | controller owner |
| Q3 | Is C-02 (protected envs with server-side `confirmEnvironment`) accepted, or should the protected guard remain local-only for V1? | the meaning of "protected" in M3 | product owner and controller owner |
| Q4 | The 200-target cap: is it final (V1 refuses larger runs) or will it be raised (C-05, C-06)? | the 500/5,000-host claims of decision 9 | controller owner |
| Q5 | May the keychain backend ship if the M2 checklist passes, given a new third-party dependency? | M2 item 7 | product owner: **decided 2026-11-26, not in V1** (see `05-architecture.md` 4.4) |
| Q6 | Is a Windows CLI build in scope for V1 (the release builds none; the code has Windows branches)? | M6 release and the credential store design | product owner |
