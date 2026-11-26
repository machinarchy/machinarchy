# 01 - Summary

> Status: **specification of the target state.** Nothing here is implemented. The existing user docs in `docs/cli/` describe the CLI as it is today and are not changed by this spec. Every claim about current code cites `file:line` from the Step 1 audit; every claim about the controller was checked against `docs/api/swagger.json` and the handler code. Where something is not verified it says so.

## 1. Goal

A person who has never seen Machinarchy can install the CLI, sign in, and bring their first machine under management in **under five minutes**, using only the CLI's own prompts and messages. Friendly never means unsafe: no weaker security, no hidden destructive action, no silent trust of a host key.

## 2. Current state (verified)

- The CLI is a thin Cobra client in the controller monorepo (`cmd/machinarchy`, `internal/cli/...`). Dependencies are minimal: cobra, pflag, yaml, `x/term` (`go list -deps`).
- Grammar is kubectl-style: `get|create|delete <resource>`, plus `config` with the kubeconfig trio cluster/user/context (`internal/cli/commands/registry.go:12`, `config.go`).
- Strengths to keep: exit codes (0,1,2,3,4,6,7,130), `Error:`/`Hint:` (`run.go:59`), `run` previews targets and binds the submission to the preview hash (`commands/run.go:79`), no shell, no secrets in argv except the legacy `--token`, config file 0600 with atomic write (`config/config.go:652`), no redirect following (`api/client.go:154`), `--yes` flag-only (`config/config.go:173`), stdout vs stderr split.
- Tests pass in every CLI package (cli 87.1 %, api 75.5 %, clierr 88.2 %, commands 80.9 %, config 68.0 %, output 92.0 %). The Step 1 audit ran them; this spec has run nothing.

## 3. Main usability problems

| # | Problem | Evidence |
|---|---|---|
| U1 | First run needs 9-12 commands in a fixed order the user must know: secret, profile, host, trust, wait, run. | Step 1 journey |
| U2 | Jargon on the beginner path: context, cluster, Access Profile, connector, supervisor, selector, operation, `HostKeyPendingApproval`. | `config.go`, `access.go`, `trust.go:29` |
| U3 | Bare `machinarchy` prints a command list, not a next step. | `root.go:56` |
| U4 | Agent hosts cannot be finished from the CLI: the install needs a NATS URL and credentials no API exposes. | audit F-11 |
| U5 | Wrong-target mistakes are easy: `run` ignores `--env`; `--yes` approves host-key trust; `--server` sends the saved token elsewhere. | audit F-01..F-03 |
| U6 | A session silently expires after 24 h and the 401 hint names the wrong fix. | `api/errors.go:78` |
| U7 | Bulk output is one table row per host; no grouping, no progress, no partial-failure exit code. | `commands/get.go:148`, `run.go:122` |
| U8 | Remote output and host names reach the terminal unsanitised. | audit F-04 |

## 4. Direction

1. **Task-first grammar** with five user concepts: **server, env, host, access, run.**
2. **Guided flows with a complete flag-based twin.** Every wizard question has a flag; with `--no-input` nothing is ever asked.
3. **Safe by construction.** Active server and env on every state-changing command, blast-radius preview, typed confirmation for protected envs, host-key trust that `--yes` cannot bypass, server-bound tokens.
4. **Honest about the controller.** The CLI stays a thin client. Where a decision needs the controller to do something it cannot do today, it is listed as controller work (`06-api-matrix.md`) and the CLI refuses with a clear message instead of faking it.

## 5. Key decisions

Decisions D1-D5 were fixed by the product owner; D6-D17 are made in this spec. "Rejected because" says why each alternative lost.

| ID | Decision | Alternatives considered | Why rejected |
|---|---|---|---|
| D1 | Task-first grammar; old commands stay one release as deprecated aliases. | Keep kubectl grammar and add wizards on top. | The grammar itself is the jargon (`create profile --connector`). Wizards would hide it only while they run. |
| D2 | Hosts and agents are one concept, `hosts`. | Separate `agents` command. | Users want a machine managed, not a transport. |
| D3 | One `run` command for execute, list, show, follow, retry-failed, cancel. A program always follows `--`; otherwise the argument is a run ID. | Keep `logs`, `cancel`, `get operations`. | Four commands and a noun ("operation") for one thing. |
| D4 | Five user concepts; "tags" not labels. | Keep controller vocabulary. | See U2. |
| D5 | Login supports password and API token; device token after password login. | Password only. | CI and shared machines need tokens. |
| D6 | **Bare `machinarchy` is the home screen; there is no `status` command.** `machinarchy -o json` returns the same data. | A separate `status` command. | It would duplicate the home screen and use up the ten-command budget. |
| D7 | **`access` is a top-level command, not `hosts access`.** | Nest under `hosts`. | One access entry serves many hosts; it is one of the five concepts. Nesting would hide it. |
| D8 | **Agent enrolment lives in `hosts`:** `hosts add --agent` does it, `hosts enroll NAME` reissues the one-time token. There is no `agents` command. | Top-level `agents`. | Violates D2. |
| D9 | **`switch [server]` switches servers; `envs use` switches env.** No `servers` command. | `servers use`. | A command for a thing the user changes once a week is a command too many. `login` adds a server; `switch` lists and selects. |
| D10 | **Capabilities use `--do NAME key=value...`** instead of `--capability NAME --input JSON`. `--input-json` stays as the escape hatch. | Keep JSON only; or build per-capability subcommands. | JSON on a command line is the opposite of beginner-friendly. Per-capability subcommands would need a capability catalog the API does not provide. |
| D11 | **Envs are created with `envs add NAME [--protected]`** (needs `POST /api/v1/environments`, verified). Assigning a host to an env needs controller work C-01. | Create envs implicitly from `hosts add --env`. | Typos would silently create environments. |
| D12 | **`--on env=production` is not shipped. Use `--env production`** (or `--on environment=production`). | Rewrite `env=` to `environment=`. | A tag may legally be named `env` (label key pattern, `domain/labels.go:20`); rewriting could silently select a different set of hosts. This challenges fixed decision 7 with evidence. |
| D13 | **Protected envs are a controller attribute** (C-02) with server-side confirmation; until it ships the CLI offers a clearly marked local-only guard. | Local guard only. | A CLI-only flag is not an authorization boundary and other clients would bypass it. |
| D14 | **Run exit codes: 0 all succeeded, 8 partial, 9 every host failed, 1 unexpected error.** | Reuse 1 for all failures. | Fixed decision 9 asks for distinct codes. Both new codes are non-zero, so existing `if ! machinarchy run` scripts keep working. |
| D15 | **A run is limited to 200 hosts in V1** (the controller cap, `domain/resource/operation.go:68`). Larger selections are refused with advice; no client-side batching. | Split into many operations. | Many run IDs and no atomic preview contradict "one run". Raising the cap and adding summary endpoints is controller work C-05, C-06. |
| D16 | **No new runtime dependencies in V1 except an optional, build-tagged OS keychain** (see `05-architecture.md`). Prompts are built on `bufio` + `x/term`. | A prompt library (survey, bubbletea, huh). | Each pulls dozens of transitive packages into a security tool for features we can write in ~300 lines. |
| D18 | **Tab completion is in V1, git-style: `machinarchy h<TAB>` lists matching commands, `machinarchy hosts show we<TAB>` lists real host names.** Built on Cobra's existing completion (no new dependency); dynamic candidates come from the API with a short budget, are sanitised, and never prompt or write. `machinarchy completion install` writes the script to the shell's standard auto-load location and never edits rc files. **This reverses fixed decision 11** ("not in V1: designed shell completion") at the product owner's request. | Static completion only (commands and flags). | Completing host names, envs, access entries, run IDs and tags is what makes the CLI feel like git; static-only would complete the part users already remember. |
| D17 | **`--plain` / `MACHINARCHY_PLAIN=1`** removes lore, colour, symbols and live redraws. Lore is a second line after a plain first line and never appears in errors or security text. | Lore everywhere. | Searchable errors and unambiguous security text matter more than flavour. |

## 6. Excluded from V1

| Item | Why |
|---|---|
| `doctor`, `fix` | Decision 11 (still in force). Diagnostics come from errors that carry a next step. |
| Completion for PowerShell and for aliases of deprecated commands | bash, zsh and fish are specified and tested; PowerShell works through Cobra's generator but is not tested in V1. Deprecated aliases are hidden from completion. |
| Client-side batching above 200 hosts | D15. |
| Plugins, custom output templates (`jsonpath`, `go-template`) | Not needed for the five-minute goal. kubectl wins here (`07-brand.md`). |
| Declarative `apply`/YAML authoring | The beginner path must not need YAML or JSON. |
| Signup | `POST /api/auth/signup` exists but is disabled by default (`MACHINARCHY_SIGNUP_ENABLED`, README). The first owner comes from the server's bootstrap setting. |
| Member management, audit, signing-key rotation | Reachable through `admin` later; not on the path to a first managed machine. |
| Windows binaries | The release builds linux and darwin only (`release.yml`). Decision needed (open question Q6). |

## 7. Step 1 audit findings referenced in this spec

The audit was delivered in the working conversation, not as a file; the findings the spec cites are repeated here so the spec stands alone. Evidence lines are as of commit `2edd1c5`.

| ID | Finding | Evidence |
|---|---|---|
| F-01 | `--yes` approves host-key trust, including `--all-pending` | `internal/cli/factory.go:158`, `commands/trust.go:97` |
| F-02 | `run` accepts `--env` but the target rule has no env, so the filter is ignored | `root.go:72`, `commands/run.go:107` |
| F-03 | `--server`/`MACHINARCHY_CLI_SERVER` sends the saved token elsewhere; `login --server` on an existing context stores another server's session under it | `config/config.go:309-324`, `:401-417` |
| F-04 | Remote output and host names printed without stripping terminal escapes | `commands/logs.go:101`, `output/output.go:229`, `commands/trust.go:84` |
| F-05 | 401 hint names the wrong fix; session expiry invisible | `api/errors.go:78`, `api/client.go:446` |
| F-06 | Env var prefix `MACHINARCHY_CLI_*`; `--token` on the command line | `config/config.go:43`, `commands/config.go:144` |
| F-07, F-08 | No way to assign a host to an env; no `protected` attribute | `managedresource/handler.go:28`, swagger `domain.Environment` |
| F-09, F-10 | 200-target cap not pre-checked; `--wait` re-downloads the whole operation each second | `domain/resource/operation.go:68`, `commands/run.go:207` |
| F-11 | Agent path needs NATS details no API exposes | agent README, `agents/handler.go:224` |
| F-14 | No installer or install docs; no Windows build | `release.yml`, `docs/cli/README.md:7` |
| F-18 | No CA-file option for private TLS | `api/client.go:139` |
| F-22 | CLI release builds against the agent repository's `main` | `release.yml` |

## 8. How to read the rest

| File | Content |
|---|---|
| `02-commands.md` | Command tree and per-command contracts. |
| `03-ux.md` | Home screen, onboarding transcripts, runs, Ctrl+C. |
| `04-security.md` | Threat model and destructive-operation standard. |
| `05-architecture.md` | Packages, credential store, dependencies. |
| `06-api-matrix.md` | CLI to API mapping and controller work. |
| `07-brand.md` | Glossary, messages, palette, scorecard. |
| `08-migration.md` | Old to new commands. |
| `09-acceptance-tests.md` | Test cases. Written, not run. |
| `10-roadmap.md` | Milestones M1-M6. |
