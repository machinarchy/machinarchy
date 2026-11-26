# 05 - Architecture

> Target state. It extends the existing structure (`Factory`, `CommandGroup`, `cli.Run`, `api.Client`, `output.Printer`); it does not replace it. The CLI stays a thin client of the public HTTP API (ADR-035): no business rules, no direct imports of controller packages (today it imports only `internal/buildinfo`, verified with `go list -deps`). No new frameworks.

## 1. Principles

1. **The controller decides, the CLI explains.** Validation that the controller also performs (label syntax, role checks, target caps) is repeated in the CLI only to produce a better message before the request, never as the authority.
2. **One path for everything shown to a person.** All text goes through `msg` (what to say) and `ui` (how to print it), so voice, `--plain`, colour, sanitising and next-step lines cannot be forgotten by one command.
3. **Pure logic is separate from I/O.** Grouping, target parsing, host-range compression, normalisation and sanitising are pure functions with golden tests. Commands only wire them to the API and the terminal.
4. **Everything time-dependent or interactive is injected** (clock, terminal, password reader, API client), so the acceptance tests in `09-acceptance-tests.md` can run without a real server, a real terminal or real time.
5. **Fail closed** (rule R8): an unreadable credential store, a damaged config or an unintelligible response stops the command.

## 2. Package layout

```
cmd/machinarchy/main.go            entry; first interrupt cancels the context, second exits (kept, main.go:20-37)
internal/cli/                      Factory, root, Run (error presentation, exit codes)       [kept, extended]
internal/cli/commands/             one file per noun; registry.go lists CommandGroups          [kept, files renamed]
internal/cli/api/                  HTTP client, typed errors                                   [kept, extended]
internal/cli/config/               non-secret settings: servers, active server/env, CA path    [reworked, v2 file]
internal/cli/cred/                 credential store: file backend, optional keychain backend   [new]
internal/cli/ui/                   terminal: TTY detection, prompts, colour, symbols, progress [new]
internal/cli/msg/                  message catalogue (plain + lore), error builder             [new]
internal/cli/sanitize/             sanitiser (T8) and completion allowlist (T16)               [new]
internal/cli/target/               --on / --except parser, glob expansion, env scope           [new]
internal/cli/group/                grouped-output aggregator                                   [new]
internal/cli/deprecate/            deprecated-alias mechanism                                  [new]
internal/cli/complete/             completion helpers with budget and allowlist               [new]
internal/cli/clierr/               exit codes and classified errors (adds 5, 8, 9)             [kept]
internal/cli/output/               table/json/yaml printer (tables sanitise cells)             [kept, extended]
```

Dependency direction (no cycles, enforced by a test that parses imports):
`commands -> {ui, msg, sanitize, target, group, cred, config, api, deprecate, complete, output, clierr}`; `ui -> {sanitize}`; `msg -> {}`; `group -> {sanitize}`; `api -> {clierr, sanitize}`. `api` stops importing `config`: it takes a small `api.Endpoint{URL, Token, Timeout, CAPool, AllowInsecureHTTP}` built by the Factory, so credential and settings storage can change without touching the HTTP client.

### 2.1 Factory additions
`Factory` keeps its job as the single dependency boundary and gains: `Creds cred.Store`, `UI *ui.Terminal`, `Msg *msg.Catalog`, `Clock func() time.Time`, `Mode` (`Normal`, `Completion`). `Mode == Completion` makes `ui` refuse to prompt and print nothing to stderr (`02-commands.md` 3.13.2). `Factory.Confirm` keeps its contract (never prompts without a terminal) and is joined by `ConfirmTyped(name)` and `ConfirmEnv(env)` for T2 and protected envs. `--yes` stays flag-only (`config.go:173`).

### 2.2 Command registration
Unchanged mechanism: `commands.DefaultGroups()` returns `CommandGroup`s with `CommandBuilder`s. New groups: `start` (login, hosts, run), `everyday` (access, envs, tokens), `account` (whoami, switch, logout), `advanced` (admin). Hidden commands (`config`, `version`, `completion`, and every deprecated alias) are registered with `Hidden: true`. The duplicate-name panic at `root.go:114-119` stays, so an alias can never shadow a command.

## 3. Message and voice layer (`msg`)

- A message is a value, not a string: `Message{ID, Primary, Why, Next, Lore}`. `Primary` is plain, searchable text. `Lore` is optional, printed on its own second line only for success messages, and dropped by `--plain`.
- Errors are built with `msg.Error(ID).With(args...)`, which returns a `clierr.Error` carrying code, hint (the `Why` and `Next` lines) and an ID. `cli.reportError` prints them in the four-part form. A unit test fails the build if an error ID lacks a `Next`, if lore appears in a message of kind error or security, or if any message contains a token-shaped string.
- The catalogue is Go code in one package (not a template language, not a translation system; localisation is out of scope).
- `ui` renders a message in one of three styles: rich (UTF-8 symbols, colour), plain (`--plain`: ASCII markers `OK`, `FAILED`, `WARN`), json (not used for messages; errors in `-o json` mode print `{"error":{"code":...,"message":...,"next":...}}` on stderr, so a script gets structure without scraping).

## 4. Credential store

### 4.1 Files
| File | Content | Mode |
|---|---|---|
| `config.yaml` (v2) | `version: 2`, saved servers (name, URL, `insecureHTTP`, CA file path, default env, protected-env local guards, completion hint shown), active server | 0600 in a 0700 directory (it has no secrets, but it names every server) |
| `credentials.yaml` | per server: kind (`session`, `device-token`, `token`), the secret, `expiresAt` when known | 0600, created with `O_EXCL`, written atomically (same routine as `config.go:652`) |
| `ca/NAME.pem` | CA certificate files copied by `--ca-file` | 0600 |

Location stays `<UserConfigDir>/.machinarchy/` (`config.go:122`) and `MACHINARCHY_CONFIG` still overrides it; `config path` prints it. On macOS this is `~/Library/Application Support/.machinarchy`; the leading dot is kept for compatibility.

### 4.2 Interface
```go
type Store interface {
    Get(server string) (Credential, error)   // ErrNotFound when none
    Put(server string, c Credential) error
    Delete(server string) error
    Where() string                           // "file", "system keychain" - for config show
}
```
`Credential` has a `String()` that returns `***` and never exposes the secret in formatting, so a `%v` in a log line cannot leak it.

### 4.3 Backends
1. **File backend (default).** `credentials.yaml` as above. Refuses a file that is group- or world-readable (R8; the current warning at `config.go:254` becomes an error on Unix) and explains `chmod 600`.
2. **System keychain backend (optional).** macOS Keychain, Linux Secret Service, Windows Credential Manager. Enabled only after the user says yes ("Move saved tokens into your system keychain?") and only when the backend works; when it stops working the CLI says so and does **not** silently fall back to writing a token to disk (R8): it asks.
3. **Environment.** `MACHINARCHY_TOKEN` is read by the Factory, never stored.

### 4.4 Keychain dependency: conditions, not a decision
**Owner decision 2026-11-26 (Q5): the keychain does not ship in V1.** Credentials stay in the separate `0600` file; the docs state what that does and does not defend against (T3). The `cred.Store` interface stays so a backend can be added later; revisit if a security review or a real user asks (macOS Keychain first, opt-in after login, never a silent fallback to disk, R8). The checklist below applies if it is revisited.

The keychain is optional and ships only if the checklist below passes during M2. If it does not, V1 ships with the file backend only and the `cred.Store` interface stays.

Candidate: `github.com/zalando/go-keyring` (MIT). **Not verified in this spec:** its current maintenance status, transitive dependencies, and how it passes secrets on macOS (a design that puts the secret in a child process's argument list would be visible in `ps` and is **rejected**). Checklist:
- [ ] no secret in any child process argv or environment, on macOS and Linux (checked by a test that lists processes while `Put` runs);
- [ ] release within the last 12 months and no open unfixed security advisory;
- [ ] `go mod graph` adds at most two modules and none with cgo;
- [ ] builds with `CGO_ENABLED=0` (the release builds with it, `release.yml`), or the feature is behind a build tag and only some release binaries carry it;
- [ ] behaviour on a headless Linux machine without a Secret Service is a clear error that offers the file backend.

Alternative if the library fails: implement macOS via the Security framework is not possible without cgo; implement Linux via `secret-tool` on stdin. Both add an external-tool dependency and are not worth it for V1.

### 4.5 Migration from the v1 file
The v1 file (`apiVersion: machinarchy.io/v1`, `clusters`, `users`, `contexts`; tokens in `users`) is read once by a small reader kept in `config/v1.go`.

1. On the first command that needs config, if a v1 file is found and there is no v2 file: back it up byte-for-byte to `config.yaml.v1.bak` (0600), write `config.yaml` and `credentials.yaml` v2 atomically, then continue.
2. Mapping: each v1 **context** becomes a saved server named like the context, with the cluster's URL and `insecure-http`, and the context's env as its default env; the context's user token becomes its credential. Kind: `token` if the value starts with `mch_pat_`, otherwise `session` (expiry unknown until the first request; a 401 then explains).
3. If two contexts share one cluster with different users, both become servers with the same URL and different names; `switch` lists both.
4. The CLI prints once to stderr: `Your saved logins were converted to the new format. The old file is kept at PATH (it contains tokens): delete it when you are happy: rm PATH`.
5. A v1 file that cannot be parsed, a v2 file that is newer than the CLI, or a conflict between v1 and v2: stop (R8) and name the file.
6. **Downgrade:** older CLIs ignore a v2 file; the `.v1.bak` is the way back (copy it over `config.yaml`). The spec does not promise two-way compatibility.
7. The old commands that wrote the v1 file (`config set-cluster`, `set-credentials`, `set-context`, `use-context`) now write v2 through the same store and print their replacement (`08-migration.md`); `--token` still works for one release, prints a warning about history, and is removed after.

## 5. Grouped-output aggregator (`group`)

Pure package, no I/O, no terminal, no clock.

```go
type Result struct {               // one host's outcome, as the CLI learned it
    Host       string
    State      string              // Succeeded, Failed, TimedOut, Cancelled, Interrupted, Lost, Running, Pending
    ExitCode   *int
    ErrorClass string
    Stdout     string              // already cut to the controller's 15 KiB window
    Stderr     string
}
type Aggregator struct{ ... }
func New(opts Options) *Aggregator          // Options: MaxGroups, SampleLines, SampleChars
func (a *Aggregator) Add(Result)            // O(len(output)) once; keeps hash + bounded sample
func (a *Aggregator) Summary() Summary      // classes, groups ordered per 03-ux.md 7.2, counts, drift
func CompressHosts(names []string, width int) string   // "web-[01-12,14] (+37 more)"
func Normalise(s string) string             // timestamps, UUIDs, PIDs, 0x addresses
```
- **Memory** is O(groups + hosts): per host a small integer index into one name table, per group a hash, a bounded sample, and the sorted index list. Output text is dropped after hashing and sampling. 5,000 hosts with 15 KiB each would be 75 MB of text; the aggregator holds a few hundred kilobytes (`09-acceptance-tests.md` G-05 measures it).
- **Determinism:** the same input in any order yields the same summary (hosts are sorted naturally: `web-2` before `web-10`). Golden files make this a test.
- **Natural host ordering and range compression** are specified by examples in `09-acceptance-tests.md` G-10.
- The aggregator does not know how to talk to the API. The run command feeds it from the operation document (up to 200 targets today) or, with C-06, from a summary plus paged targets.

## 6. Run orchestration

```
resolve target -> preview -> confirm -> submit -> watch -> aggregate -> print -> exit code
```
- `target.Parse(onFlag, exceptFlag, env) -> api.TargetRule` (pure).
- Preview and submit use the existing `PreviewTargets` and `SubmitOperation` with `ExpectTargetsHash` (already in `api/managed.go`).
- **Watch:** poll `GET /operations/{id}` at 1 s, backing off to 5 s after 30 s; cancel via context; after C-06 the poll reads the summary only. A poll failure that is not 401 is retried up to 3 times with backoff because it is a read; **a state-changing request is never retried** (R5).
- **Output fetch:** after the run reaches a terminal state, fetch `output` for hosts needed by the displayed samples (bounded concurrency 8); the full text of any one host is `run ID --host NAME`.
- **Signals:** `main.go` keeps the two-stage interrupt. The context passed to `Run` is cancelled by the first interrupt; the watch loop treats that as "stop watching" and prints the resume lines (`03-ux.md` section 9). Submission code distinguishes "cancelled before the request was sent" from "cancelled after it may have been sent".
- **Exit code:** a pure function `exitFor(summary)` returns 0, 8 or 9.

## 7. API client changes (`api`)

| Change | Why |
|---|---|
| `Endpoint` struct replaces `config.Target` in `NewHTTPClient` | decouple from settings; carry CA pool and transport policy |
| TLS: `tls.Config{RootCAs: pool}` per server when `--ca-file` was saved; **never** `InsecureSkipVerify` | T6 |
| New methods: `Me`, `Sessions`, `RevokeSession(s)`, `Logout`, environments CRUD, `DeleteAgentToken`, `RevokeAgent`, `Agents`, `ControllerKey` | verified endpoints, `06-api-matrix.md` |
| Version check: read `X-Machinarchy-Version` / API range when C-11 exists; otherwise rely on decode errors | "incompatible version" tests |
| Typed errors carry the API `code` field (`already_exists`, `targets_changed`, `protected_environment_confirmation_required`, `execution_not_allowed`) | `msg` selects the right "Next" |
| Request IDs from `X-Request-Id` kept in errors (already done, `errors.go:126`) | support |
| No retries for non-GET; GET retried at most 3 times on network error or 502/503/504 | R5 |

Everything else about the client (16 MiB bound, redirect refusal, escaped paths, header decoration in one place, `api/client.go:258`) is kept.

## 8. Deprecated aliases (`deprecate`)

`deprecate.Alias{Old: "get hosts", New: "hosts", RemovedIn: "0.2.0"}` registers a hidden command that runs the same code path as the new command (a thin wrapper that rewrites flags, e.g. `-l` -> `--on`), prints on stderr `Deprecated: "machinarchy get hosts" is now "machinarchy hosts". It will be removed in 0.2.0.`, and exits with the new command's exit code. One message per invocation. The table of aliases is the data behind `08-migration.md` and is tested to be complete against the list of old commands. Deprecated commands are hidden from `--help` and from completion. `MACHINARCHY_NO_DEPRECATION_WARNINGS=1` silences the line for scripts that cannot change yet; there is no way to silence it in interactive use except migrating.

## 9. Completion (`complete`)

Uses Cobra's `ValidArgsFunction` and `RegisterFlagCompletionFunc`; no new dependency. A helper `complete.Hosts(factory)` builds a context with an 800 ms budget, calls the API read-only, applies the sanitiser and the allowlist, caps the list at 200, and returns `cobra.ShellCompDirectiveNoFileComp`. On any error it returns an empty list and the same directive without printing anything. Tests call Cobra's hidden `__complete` command with a fake API (`09-acceptance-tests.md` C-series).

## 10. Dependencies

Existing (kept): `github.com/spf13/cobra`, `github.com/spf13/pflag`, `go.yaml.in/yaml/v3`, `golang.org/x/term` (and its `x/sys`).

| Candidate | Decision | Security and maintenance reasoning |
|---|---|---|
| Prompt library (`survey`, `promptui`, `huh`, `bubbletea`) | **Rejected** | Each pulls tens of transitive modules and takes over the terminal in ways that make `--plain`, `--no-input` and testing harder. The needs are: line prompt, hidden prompt (`x/term`), numbered choice, typed confirmation: about 300 lines on `bufio`. Some of these libraries have had maintenance gaps; none is needed. |
| Colour / styling (`lipgloss`, `fatih/color`) | **Rejected** | The palette is the 16 ANSI colours and bold/dim; ~40 lines. `NO_COLOR` is honoured by our own code. |
| Table library | **Rejected** | `text/tabwriter` is already used (`output/output.go:101`). |
| Spinner / progress | **Rejected** | One status line redrawn with `\r` and a clear-to-end code. |
| `go-isatty` | **Not needed** | `x/term.IsTerminal` is already a dependency. |
| `go-runewidth` / `x/text/width` | **Deferred** | Needed only to align East-Asian wide characters in tables. V1 aligns by rune count; host names are normally ASCII. Revisit if a user reports misalignment. |
| PTY test library (`creack/pty`) | **Rejected** | Prompts read through an injected `Terminal` interface; tests fake it. One shell-level smoke test uses the system `script` tool in CI, not a module. |
| Keychain (`zalando/go-keyring` or equivalent) | **Conditional** | Section 4.4 checklist; optional, build-tagged. |
| Viper | **Stays removed** | Already replaced by `settings` (`config.go:148-162`). |

Rule: any later dependency needs an entry in this table with the same three questions (what attack surface it adds, how well it is maintained, what is the smallest alternative).

## 11. Testability and test layout

- `api.Client` is already an interface (`api/client.go:43`) with a fake used in `commands/commands_test.go`; fakes grow with the new methods.
- New pure packages (`sanitize`, `target`, `group`, `msg`) get table tests and **golden files** under `testdata/` (the golden files are the executable form of `03-ux.md` section 7).
- A single `clitest` helper runs `cli.Run` with fake streams, a fake terminal, a fake clock and a fake API, and asserts stdout, stderr and exit code together.
- An import-graph test enforces section 2's dependency rules; a lint test enforces "every error has a Next".

## 12. Build and compatibility

- Version stamping stays (`buildinfo`, `-ldflags`, `release.yml`). `machinarchy --version` prints it.
- The CLI declares the API generation it speaks. With C-11 the server answers with its version and supported range; without it the CLI treats a response that fails to decode as "incompatible" and says so (`03-ux.md` section 10).
- Release artefacts: linux and darwin, amd64 and arm64 (today). A Windows build is an open question (Q6); the code already contains Windows-aware branches (`config.go:257`).
