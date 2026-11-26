# 07 - Brand and voice

> Lore is flavour, never vocabulary the user has to learn. The first line of any output is plain and searchable; lore, where allowed, is a short second line. Errors and security text never carry lore. `--plain` removes all of it.

## 1. Voice

1. **Plain first.** A new person must understand every line without knowing the lore.
2. **Calm and exact.** Say what happened, what is true now, what to do. No jokes in failures.
3. **Short.** One sentence per fact. Numbers and names, not adjectives.
4. **Dry, rarely.** The lore is deadpan: the machines are organised and mildly condescending about it. It appears at moments of success only, never on every line, and never more than once per command.
5. **Honest about limits.** "The command may still be running there." beats "Done."

Tagline: *Bringing order to machines, one host at a time.* Principle: *One authority. Distributed execution. Coordinated infrastructure. No machine left without a place in the hierarchy.*

## 2. Glossary

"Plain" is what appears in commands, flags, config keys, JSON fields, error codes and every first line. "Lore" is optional flavour. **Forbidden in user-facing text:** cluster, context, operation, selector, connector, supervisor, profile (for access), label (for tag), `Access Profile`, `HostKeyPendingApproval` and other controller reason strings except in `hosts show` detail and JSON.

| Plain term | Lore term | Lore allowed in | Never in |
|---|---|---|---|
| server (the controller) | the authority | home screen when not signed in; login success second line | errors, security text, JSON |
| agent | operative | agent-host success second line ("Another operative reports for duty.") | errors, install instructions |
| host / machine | territory | host-added success second line ("Another machine joins the hierarchy.") | errors, prompts |
| run | order | run-finished success second line (optional) | progress lines, errors |
| env | sector | never used in V1 (env is plain) | everywhere |
| access | clearance | never used in V1 (access is plain) | everywhere |
| tag | mark | never used in V1 | everywhere |
| trust (a host key) | - | - | **always plain** (security) |
| token | - | - | always plain |
| inventory | the hierarchy | help `Long` text of `hosts` ("hosts brings machines into the hierarchy") | JSON, errors |

Rule of thumb: if a wrong guess about the meaning could make someone lose data, access or money, there is no lore on that line.

### 2.1 Where lore may appear (complete list)
Home screen when not signed in; the success line of `login`, `hosts add` (SSH and agent), `hosts trust` (after the plain confirmation), `run` finishing with all hosts succeeded; the `Long` description of `hosts` and `run` in `--help`. Nowhere else. A test (`msg` package) enforces this list.

## 3. Message catalogue (main flows)

`Next:` lines are real commands. `{x}` are placeholders. Messages marked **E** are errors (exit non-zero), **W** warnings, **S** success. Lore appears only on the lines marked "lore".

| ID | Kind | Primary (plain) | Lore (optional second line) | Next |
|---|---|---|---|---|
| auth.not_signed_in | S/info | `Not signed in.` | `Another machine will not manage itself.` | `machinarchy login` |
| auth.checking_server | info | `Checking the server ...` | | |
| auth.server_ok | S | `✔ Machinarchy server reached.` | | |
| auth.server_wrong | E | `That address did not answer like a Machinarchy server.` + why `Got {what}.` | | `check the address, or: machinarchy login URL` |
| auth.signed_in | S | `✔ Signed in to {server} as {user} ({role}). This session lasts {duration}.` | `The authority acknowledges you.` | `machinarchy hosts add` |
| auth.bad_credentials | E | `Sign-in failed: the email or password is not right.` | | `machinarchy login` |
| auth.throttled | E | `Too many sign-in attempts.` why `Try again in {n} s.` | | `machinarchy login` |
| auth.session_expired | E | `Your session on {server} expired at {time}.` | | `machinarchy login` |
| auth.token_rejected | E | `The server did not accept this token. It may be wrong, expired or revoked.` | | `machinarchy login --with-token` |
| auth.forbidden | E | `{name} ({role}) is not allowed to {action}.` | | `{how to get the right role}` |
| auth.owner_needed | E | `This needs an owner signed in with a password; you are using an API token ({name}, {role}).` | | `machinarchy login` |
| auth.device_token_saved | S | `✔ Device token saved ({role}, {days} days).` | | `machinarchy tokens` to review it |
| auth.logged_out | S | `✔ Signed out of {server}.` | | |
| auth.logged_out_token | W | `The saved copy was removed. The token still works until it expires or is revoked.` | | `machinarchy tokens revoke {name}` |
| auth.insecure_http | W | `{server} uses plain http: your sign-in can be read on the network.` | | `use https://` |
| tls.untrusted | E | `The server's certificate is not trusted ({reason}).` | | `machinarchy login URL --ca-file ca.pem` |
| server.unreachable | E | `Cannot reach {server}: {reason}.` | | `check the address, or: machinarchy switch` |
| server.incompatible | E | `This server speaks API version {a}; this CLI supports {b}.` | | `update the CLI: {url}` |
| server.switched | S | `✔ Active server: {server}.` | | `machinarchy` |
| env.created | S | `✔ Environment "{env}" created{ (protected)}.` | | `machinarchy hosts add --env {env}` |
| env.protected_local | W | `This server cannot enforce protected environments. The guard applies on this computer only.` | | |
| env.active | S | `✔ Active env: {env}.` | | |
| env.protected_prompt | prompt | `Type "{env}" to confirm a change in a protected environment:` | | |
| host.added | S | `✔ {host} added.` | `Another machine joins the hierarchy.` | `machinarchy run --on {host} -- uptime` |
| host.added_agent | S | `✔ {host} added. It stays "waiting for agent" until the agent starts.` | `Another operative reports for duty.` (when ready) | `the install command` |
| host.waiting_key | info | `Waiting for the server to see {host}'s SSH key (up to {n} s) ...` | | |
| host.key_prompt | security | `{host} presents this SSH host key: {fp}` + `Trust this key? Type yes to trust it, or press Enter to skip:` | | |
| host.trusted | S | `✔ Key trusted. {host} will be reachable once it reports ready.` | | `machinarchy hosts show {host}` |
| host.ready | S | `✔ {host} is ready ({n} s).` | | |
| host.key_changed | E/security | `{host} now presents a different SSH key.` why `This is normal after a reinstall. It is also what a machine pretending to be {host} would look like.` | | `check with whoever runs {host}, then: machinarchy hosts trust {host} --replace` |
| host.cannot_connect | E | `{host} cannot be reached: {reason}.` | | `machinarchy hosts show {host}` |
| host.duplicate | E | `A host named {host} already exists.` | | `machinarchy hosts show {host}` |
| host.removed | S | `✔ {host} removed from the inventory.` plus the "not done" list | | |
| host.agent_revoked | S | `✔ The agent on {host} can no longer connect.` | | |
| host.move_unsupported | E | `This server cannot move hosts between environments yet.` | | `ask the server owner to upgrade` |
| access.created | S | `✔ Access "{name}" created.` | | `machinarchy hosts add --access {name}` |
| access.in_use | E | `Access "{name}" is used by {n} hosts.` | | `machinarchy hosts` |
| run.preview | prompt | `[{server} · {env}] Run on {n} hosts: {hosts}` + command, limits | | |
| run.too_many | E | `This would run on {n} hosts; one run can target at most 200.` | | `machinarchy run --on {narrower} -- ...` |
| run.targets_changed | E | `The set of hosts changed while you were deciding. Nothing ran.` | | `run the command again` |
| run.submitted | info | `Run {id}` | | |
| run.all_ok | S | `✔ {n} of {n} hosts succeeded` | `The machines complied.` (optional) | `machinarchy run {id} --host {first}` |
| run.partial | W | `⚠ {k} of {n} hosts succeeded, {f} did not` | | `machinarchy run {id} --retry-failed` |
| run.all_failed | E | `✖ {n} of {n} hosts failed` | | `machinarchy run {id} --host {first}` |
| run.unknown | W | `A command on a "timed out" or "outcome unknown" host may still have run.` | | |
| run.drift | W | `⚠ {k} hosts printed something different from the other {m}.` | | |
| run.stopped_watching | info | `Stopped watching. The run keeps going on the hosts.` | | `machinarchy run {id} --follow` / `--cancel` |
| run.cancel_requested | info | `Cancellation requested. The hosts have not confirmed yet.` | | |
| run.cancelled | S/W | `Run cancelled.` + `{n} hosts could not be reached while cancelling; the command may still be running there.` | | |
| run.submit_unknown | W | `Interrupted while sending the run. It may or may not have been created.` | | `machinarchy run` |
| run.retry | prompt | `Run again on {n} hosts: {hosts}` + skipped list | | |
| token.created | S | `✔ Token created. Copy it now; it is shown only once:` | | `use it in CI: MACHINARCHY_TOKEN=...` |
| token.revoked | S | `✔ Token {name} revoked.` | | |
| completion.installed | S | `✔ Installed. Open a new terminal, then try: machinarchy h<TAB>` | | |
| deprecate.alias | W | `Deprecated: "{old}" is now "{new}". It will be removed in {version}.` | | `{new}` |
| config.migrated | W | `Your saved logins were converted to the new format. The old file is kept at {path} (it contains tokens): delete it when you are happy: rm {path}` | | |
| input.required | E | `{flag} is required when nothing can be asked ({why}).` | | `{full command with the flag}` |
| confirm.required | E | `This needs confirmation, and --yes is not enough here ({what}).` | | `{--confirm-env ENV or --fingerprint ...}` |

## 4. Logotype

Shown at most once per command, only on a terminal that is not `--plain`, and only by: `machinarchy --version`, the first screen of `login` (when no server is saved), and the home screen when not signed in. Never in `-o json`, never on stderr in scripts.

```
   ◆
  ◆ ◆     machinarchy
 ◆ ◆ ◆    one authority. distributed execution.
```
A tree of hosts under one point: the hierarchy. ASCII fallback (non-UTF-8 locale or `--plain`):
```
   *
  * *     machinarchy
 * * *    one authority. distributed execution.
```
In `--plain` mode the mark is omitted and only the text line is printed. The wordmark is lowercase, one word.

## 5. Palette and symbols

The CLI uses the terminal's own 16 colours, so it follows the user's theme (light or dark) and `NO_COLOR`. No 256-colour or true-colour codes.

| Role | Colour | Used for |
|---|---|---|
| Accent | cyan | commands in `Next:` lines, server and env names in the context line |
| OK | green | `✔`, `ready`, `succeeded` |
| Error | red | `✖`, `Error:`, `failed`, `protected` |
| Warning | yellow (not bold) | `⚠`, `waiting for key approval`, drift |
| Unknown | magenta | `?`, `outcome unknown` |
| Secondary | dim | explanations, elapsed time, hints |
| Heading | bold | section names, table headers |

| Meaning | Symbol | ASCII (`--plain`, non-UTF-8, `TERM=dumb`) |
|---|---|---|
| success | ✔ | `OK` |
| failure | ✖ | `FAILED` |
| warning | ⚠ | `WARN` |
| running | ● | `RUN` |
| pending | ○ | `WAIT` |
| unknown | ? | `UNKNOWN` |
| cancelled | ⏹ | `CANCELLED` |
| next step | `Next:` word | `Next:` |

Rules: a symbol is always followed by words (never meaning by symbol or colour alone); columns are aligned on text, not on symbols (some terminals draw `⚠` double-width); colour is dropped when `NO_COLOR` is set, stdout is not a terminal, or `--plain`; `protected` is shown as the word with red, plus a bracket marker in the context line (`[company · production (protected)]`).

## 6. `--plain` rules

`--plain` or `MACHINARCHY_PLAIN=1`:
1. No lore lines, no logotype.
2. No colour, no bold, no dim, no symbols: ASCII markers from section 5.
3. No cursor movement or line redraw; progress is a new line every 10 seconds and at each 10 %.
4. No box drawing or alignment padding beyond single spaces and tabs in tables.
5. Same words, same order, same exit codes, same `Next:` lines.
6. Prompts are still asked on a terminal; `--no-input` governs prompting, `--plain` governs appearance.
7. `-o json` and `-o yaml` are never affected.
`NO_COLOR` alone removes colour only; non-UTF-8 locale alone switches to ASCII symbols only.

## 7. Accessibility

- **Meaning never depends on colour or symbols alone** (section 5).
- **No redrawing in `--plain` or on a non-terminal**, so screen readers and logs read linear text.
- **No arrow-key menus.** Choices are numbered and typed; every prompt can be answered by typing and Enter, with the default shown in brackets.
- **Typed confirmations** are exact strings and are read out as plain text; no hidden keystrokes except password and token entry.
- **Line width:** wrap at the terminal width (`COLUMNS` or the `x/term` size), default 80; never wrap inside a command, ID or fingerprint.
- **Language:** short sentences, common words, no idioms; the lore lines are decoration and removable.
- **Times** are shown as absolute local time plus a relative hint ("09:14, in 21 h").
- **Keyboard only**, no mouse, no timing-dependent prompts; waits show the elapsed seconds and can be interrupted.
- **Machine-readable output** (`-o json`) is always available for tools that read for the user.

## 8. "Better than kubectl" scorecard

Method: I ran `kubectl` on this machine and read its own help. **Client version tested: v1.27.1 (2023); newer versions may behave differently.** Claims about Machinarchy describe the **target** in this spec and are not yet true or tested. Where kubectl simply has a feature we do not, the verdict says so.

| Criterion | kubectl 1.27.1 (what I observed) | Machinarchy target | Verdict |
|---|---|---|---|
| Top-level commands in `--help` | 42 listed (counted from `kubectl --help`) | 10 listed (decision 2026-11-26: counts the commands shown in the default `--help`; `help` is Cobra's own and not counted; the rarely used admin commands are grouped under one `admin` entry, see PLAN step 9.1) | Machinarchy simpler; scopes are not comparable (kubectl manages far more) |
| Output with no cluster reachable | `kubectl get podz` printed five repeated lines like `E1125 ... memcache.go:265] couldn't get current server API group list: Get "http://localhost:8080/api?...": dial tcp [::1]:8080: connect: connection refused` | One four-part error with the next step | Machinarchy clearer, if built as specified |
| Typo help | `kubectl gett` prints `Did you mean this? set get` | `Did you mean:` with the full corrected command, at any depth | Parity at top level; Machinarchy should extend it |
| Missing argument | `kubectl get` explains how to list resource types (`kubectl api-resources`) | Every error ends with a runnable command | Parity |
| First-time sign-in | No `login` command in `kubectl --help`; access comes from a kubeconfig file and external plugins | `login` with a guided prompt, device token, expiry shown | Machinarchy easier for a newcomer; kubectl more flexible for enterprise auth |
| Concepts a beginner meets | `kubectl config` exposes clusters, contexts, users (`get-clusters`, `get-contexts`, `get-users`, `set-cluster`, `set-context`, `set-credentials`) | One concept: server | Machinarchy simpler; less flexible |
| Destructive command safety | `kubectl delete --help` lists `--dry-run`, `--wait`, `--now`, `--force`; no confirmation prompt option appears in the 1.27.1 help | Typed confirmation for destructive actions, protected envs, `--dry-run` | Machinarchy safer by default (kubectl assumes the user knows); kubectl is faster for experts |
| Multi-target execution | `kubectl exec` runs in one container; `kubectl logs -l SELECTOR` handles several pods with `--prefix` and a cap `--max-log-requests=5`; no grouped identical-result view | Preview-bound run on many hosts, grouped result, partial-failure exit codes | Not a like-for-like comparison; grouped bulk output has no kubectl counterpart in what I read |
| Output formats | `-o` accepts json, yaml, name, go-template, jsonpath, custom-columns, wide | table, json, yaml | **kubectl wins** |
| Shell completion | `completion` for bash, zsh, fish, powershell | bash, zsh, fish with dynamic host and env names (not tested by me for kubectl's dynamic parts) | Parity at best; kubectl is more mature |
| Extensibility | `plugin` command, `apply`, `diff`, `explain`, `wait`, `auth`, `debug`, `rollout`, `top` | none of these | **kubectl wins** by a wide margin |
| Watching | `get -w`, `logs -f` | `run ID --follow` for runs only | kubectl wins on breadth |
| Safety defaults for execution | exec runs what you type, in the namespace of your context | preview-bound runs, env scope on every run, typed confirmation in protected envs | Machinarchy safer by default |
| Time to first managed thing | not measured (kubectl needs a cluster and credentials from elsewhere) | five-minute test (`09-acceptance-tests.md` A-01), not yet run | unmeasured on both sides |

**Where Machinarchy will not win:** ecosystem (plugins, third-party tooling), output templating, declarative management, breadth of resource types, and expert speed. The claim this spec makes is narrower: for a person who has never used it, getting a first machine managed and running a first safe command is easier, and the CLI makes the wrong target hard to hit.
