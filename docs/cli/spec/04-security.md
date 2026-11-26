# 04 - Security

> Specification of the target state. Facts about today's CLI cite the Step 1 audit; facts about the controller cite the API contract and handlers. Nothing here was tested. The controller remains the authorization boundary: the CLI reduces mistakes and leaks, it does not replace server-side checks.

## 1. Scope and assumptions

- **In scope:** the `machinarchy` binary, its config and credential storage, what it sends to the controller, what it prints, what it asks the user to confirm.
- **Trusted:** the user's operating system account and the controller the user chose (it is the authority). **Not trusted:** the network, every remote host (its output, its name, its keys until approved), every byte of a response that could have been altered in transit or by a compromised server, other users and processes on the computer, CI logs.
- **Out of scope:** the controller's own hardening (ADR-036 and following), NATS, SSH servers.

## 2. Design rules

| Rule | Statement |
|---|---|
| R1 | No secret (password, token, key, secret value) is ever a flag value or a positional argument. Secrets come from a hidden prompt, stdin, a file given by path, or the environment variable `MACHINARCHY_TOKEN` (documented as the least private option). |
| R2 | Nothing the server or a remote host sends reaches a terminal without passing the sanitiser (section 7). |
| R3 | **A saved credential is only ever sent to the server it was saved for.** `--server URL` or `MACHINARCHY_SERVER` pointing elsewhere does not carry a saved credential along; it needs `MACHINARCHY_TOKEN` or `--with-token` input for that command. |
| R4 | `--yes` answers a `[y/N]` question. It never answers host-key trust, authentication, or a protected-environment confirmation. |
| R5 | The CLI never auto-retries a state-changing request. |
| R6 | The CLI never contacts a managed host itself. Fingerprints come from the controller's observation. |
| R7 | Every state-changing command prints the active server and env first and the exact thing it will do (what, where, how many) before it asks. |
| R8 | Fail closed: if the CLI cannot decide (unknown role, unreadable keychain, response it does not understand) it stops and says what to do. It never falls back to something less safe silently. |

## 3. Threat model

Each threat: scenario, impact, prevention, detection, recovery. IDs are referenced from the other spec files.

### T1 - Password leakage
- **Scenario:** typed into a command line, shown on screen, written to a log, saved by the CLI, sent to the wrong server.
- **Impact:** account takeover, owner-level access.
- **Prevention:** only a hidden prompt or `--password-stdin` (R1); never an environment variable; never stored (the CLI keeps a session or device token, not the password); sent only in the body of `POST /api/auth/login` over TLS; the login HTTP client follows no redirects (already true, `api/client.go:434`); request and response bodies are never logged, also with `--verbose`.
- **Detection:** acceptance tests grep stdout, stderr, config file and `--verbose` output for the test password (`09-acceptance-tests.md` S-05).
- **Recovery:** change the password (`POST /api/v1/me/password` ends every session of the user; there is no CLI command in V1) and revoke tokens with `tokens revoke`.

### T2 - Token leakage (history, process list, environment, logs, screenshots)
- **Scenario:** `--token abc` in shell history or `ps`; `MACHINARCHY_TOKEN` visible to same-user processes or printed by CI `env`; a token shown in a screenshot; copied config file.
- **Impact:** access with the token's role until it expires or is revoked.
- **Prevention:** the `--token` flag of `config set-credentials` is deprecated and removed after one release; tokens are never accepted as arguments (R1); `tokens create` prints a token once and `-q` prints nothing else; `--out FILE` writes mode 0600 and refuses to overwrite; default expiry 90 days (server default 30, max 365); least privilege (`reader` for read-only automation); tokens carry a name so they can be found and revoked; `whoami` and errors print the token **name**, never any part of the value.
- **Detection:** tokens have a prefix `mch_pat_` (`middleware/auth.go:32`) so secret scanners can find them; the audit log records token use on the server (`ADR-040`).
- **Recovery:** `machinarchy tokens revoke NAME`; for a leaked device token, `logout` then revoke from another session.

### T3 - Credentials at rest
- **Today:** tokens (sessions and API tokens) are plaintext in `<UserConfigDir>/.machinarchy/config.yaml`, mode 0600 inside a 0700 directory, written atomically (`config/config.go:652-717`); a permission warning exists but is skipped on Windows (`config.go:257`).
- **Scenario:** other user or backup tool reads the file; malware running as the user reads it.
- **Impact:** same as T2.
- **Prevention:** target storage is described in `05-architecture.md` section 4: the OS keychain when available and enabled, otherwise the 0600 file; credentials are separated from non-secret settings; the file keeps working for anyone who has no keychain; migration is automatic and reversible. A keychain protects against other users and backup leakage; **it does not protect against malware running as the same user** and the docs say so.
- **Detection:** `config show` reports where each credential lives, with values redacted; the existing "readable by other users" warning becomes an error for a file with credentials (fail closed, R8) with `chmod 600` as the next step.
- **Recovery:** revoke; delete the entry with `logout`.

### T4 - Host-key trust and changes
- **Scenario:** first trust of a key an attacker presented (a machine in the middle at registration time); a later key change caused by reinstall or by an impostor; a script approving everything.
- **Impact:** the controller would present SSH credentials to the wrong machine and run commands there.
- **Prevention:** the CLI shows the fingerprint **observed by the controller** and requires the user to type `yes` after comparing it (owner decision 2026-11-26: the 8-character prefix does not scale to many hosts and is kept only for a changed key); the `--yes` flag and `--all-pending` never approve (R4); non-interactive trust needs `--fingerprint` equal to an observed key; a **changed** key is a different, stronger flow (old and new key, two typed confirmations, env name when protected, `03-ux.md` section 8); `tofu` is offered only for key authentication and labelled "lab only" (matching `docs/operations.md`); the whole observed key set is trusted together because the controller refuses an incomplete set.
- **Limit (honest):** the fingerprint comes from the controller's network position, not the user's. A man in the middle between controller and machine at first contact would be shown as legitimate. That is why the wizard prints the command to read the real fingerprint on the machine and why `pinned` (the user supplies `--trust-fingerprint`) exists. The CLI cannot do better without contacting the host itself, which R6 rules out.
- **Detection:** `HostKeyChanged` shows in the home screen, `hosts`, and `hosts show`; the controller audits trust events (`resource_host.host_key_trusted`, `docs/operations.md`).
- **Recovery:** `hosts trust NAME --replace` after checking out of band; or `hosts remove` and re-add.

### T5 - Secrets in argv, history and the process list
Covered by R1 for passwords, tokens and secret values. Non-secret but sensitive values (host names, tags) may appear in history; that is accepted. The `run` program and its arguments appear in history and in the controller (operators and owners can read them back, readers cannot): the help warns "do not put secrets in the command; use an access entry or an environment on the host".

### T6 - TLS and plain HTTP
- **Today:** `https://` uses Go's default verification; `http://` to a non-loopback host is refused unless `--insecure-http` was saved for the cluster (`config.go:611`); redirects are not followed (`client.go:154`); there is **no way** to trust a private CA (audit F-18), which pushes users to plain HTTP.
- **Scenario:** self-signed lab server; expired certificate; HTTP downgrade; a proxy answering.
- **Prevention:** per-server `--ca-file FILE` (added to the trust pool for that server only; the file is copied into the CLI's config directory and shown by `whoami`); **no `--insecure-skip-verify`**: it would let a user disable verification for every future command, and R8 forbids it; invalid certificate error: `Error: the server's certificate is not trusted (certificate signed by unknown authority).` `Next: if this is your own server: machinarchy login URL --ca-file ca.pem`; `--insecure-http` stays per server, loopback and lab only, and `whoami` and the context line show `http (insecure)`.
- **Detection:** the context line and `whoami` show the transport; the home screen shows a warning for an insecure server.
- **Recovery:** switch to https, remove the server (`switch`, `logout`).

### T7 - Controller impersonation
- **Scenario:** a typo-squatted or hijacked URL; DNS spoofing; a proxy returning plausible JSON.
- **Impact:** credentials are sent to an attacker; false results are displayed.
- **Prevention:** TLS verification; saved servers are keyed by URL and credentials never move to another URL (R3); on first `login` the CLI prints the exact host and, for a new server, asks the user to check it ("Is this the right server: machinarchy.example.com?"); a response that is not valid Machinarchy JSON stops the command with "that address did not answer like a Machinarchy server" (existing `invalidResponseHint`, `api/client.go:30`); `MACHINARCHY_SERVER` that differs from the active server prints a warning with both URLs.
- **Detection:** the server name in every context line; unexpected changes in the displayed server host.
- **Recovery:** `logout`, revoke tokens from a trusted session, change the password.

### T8 - Escape sequences in remote output and host names
- **Scenario:** a remote command (or a compromised host) prints `ESC[2J`, cursor movement, OSC 52 (clipboard write), OSC title changes, bidi controls or fake prompts; a host is registered with a name containing control characters (the controller only checks name length, `domain/resource/host.go:207`); output is "cleaned" inconsistently.
- **Impact:** a spoofed screen (hiding a failed host), clipboard overwrite, a command typed by the user's terminal in some emulators, log forging.
- **Prevention:** every server- or host-originated string printed in text form passes the sanitiser (R2, section 7) - host names, tags, run output, error messages, fingerprints, access names, env names, descriptions; `-o json` and `-o yaml` escape control characters by construction and are passed through; `--raw` exists only when stdout is not a terminal. Defence in depth on the server: C-14 asks the controller to validate host names.
- **Detection:** the sanitiser counts removals; `run ID --host NAME` says `[output contained N control characters; shown sanitised]`.
- **Recovery:** none needed; the content cannot reach the terminal.

### T9 - Expired, revoked and insufficient credentials
- **Scenario:** a 24 h session ends mid-task; a token is revoked; a reader token tries to write.
- **Prevention:** show the expiry that the server reports (`GET /api/v1/sessions` for a session; C-03 for a token); on 401 say "expired or revoked" with `machinarchy login`; on 403 say what the role is and what is needed (never "expired"); a command that needs an owner is rejected locally when the credential is known to be a token (`02-commands.md` 2.6) to avoid sending a doomed request; long operations (a follow) that hit 401 stop and print how to resume.
- **Detection:** `whoami`; the home screen's expiry line.
- **Recovery:** `login`.

### T10 - Untrusted or malformed responses
- **Scenario:** a hostile or buggy server returns huge bodies, deep JSON, redirects, strings with control characters, URLs or shell snippets intended to be pasted.
- **Prevention:** existing bounds stay: 16 MiB per response, 64 KiB for error bodies, no redirects, request timeouts, escaped path segments, dot and empty segments refused (`api/client.go:28,154,273`); strings are sanitised (T8); **the CLI never prints a server-supplied shell command for the user to paste.** The agent install command (C-07) is built by the CLI from structured fields (version, URLs, checksum, addresses), each validated (https URL, hex checksum, host syntax), and the URL and checksum are shown for the user to check; unknown fields are ignored; a server answer that fails validation produces the "four values" fallback of `03-ux.md` 2.2.
- **Detection:** decoding errors name the endpoint and the HTTP status.
- **Recovery:** none needed.

### T11 - Accidental production change
- **Scenario:** wrong server, wrong env, an `--on` that matches more than intended, a stale selection, a run sent from the wrong terminal.
- **Prevention:** the context line `[server · env]` on every state-changing command (R7); env scope applies to `run` (today it is ignored, audit F-02) by adding `environment=ENV` to the selector; blast-radius preview with count and names; the preview hash binds the submission so the set cannot change silently (already true, `run.go:107`); protected envs require typing the env name (section 6); `--on all` without an env asks to type the host count; `--dry-run` prints the preview and exits without submitting; a different color of the context line for protected envs (and the word `protected`, never colour alone).
- **Detection:** the controller audits operations and actors.
- **Recovery:** `run ID --cancel` (see the guarantees in `03-ux.md` section 9); there is no undo.

### T12 - Destructive commands
- **Scenario:** `rm -rf`, `reboot`, `systemctl stop` on many hosts; removing a host, access entry or env; revoking identities.
- **Prevention:** the destructive-operation standard (section 6). The CLI does **not** try to detect dangerous programs by looking at the command text: any list would give false confidence. Every `run` gets the same preview and confirmation; the protected env raises it.
- **Detection and recovery:** as T11.

### T13 - Credential sent to the wrong place
- **Scenario:** `MACHINARCHY_SERVER` set by a script or a `--server` typo while a saved token exists (today the token is sent, `config.go:309-324`); `login --server B` stores B's session under server A's entry (audit F-03).
- **Prevention:** R3; credentials are stored per server entry together with the URL they belong to; `login` to a new URL always creates a new entry; `--insecure-http` and `--ca-file` are per server and are not inherited by an override URL.
- **Detection:** the context line shows the server actually used.

### T14 - CI misuse
- **Scenario:** `--yes` in a pipeline against production; a token in CI with owner-like reach; `MACHINARCHY_TOKEN` echoed.
- **Prevention:** a protected env requires `--confirm-env ENV` even with `--yes`; trust requires `--fingerprint`; tokens created for CI are `reader` or `operator`, never owner (an API token cannot be owner, `tokens.CreateRequest` enum); `MACHINARCHY_TOKEN` is never written to disk or printed; CI mode is automatic when stdin is not a terminal and prompts never block (R5-style fail with the missing flag named).
- **Recovery:** revoke the token.

### T15 - The CLI binary itself
- **Today:** releases publish linux and darwin binaries with `checksums.txt` and an artifact attestation (`release.yml`, `publish-assets`). Binaries are built against the agent repository's `main` unless pinned (audit F-22).
- **Prevention:** the README quick start verifies the checksum (and the attestation with `gh attestation verify` where available); builds are reproducible (`-trimpath -buildvcs=false`, already so); the protocol dependency is pinned once D-4 is done. No self-update in V1 (a self-updater is a high-value target).

### T16 - Shell completion as an injection or latency channel
- **Scenario:** a host name, tag or run description containing spaces, quotes, `$(...)`, backticks, `;`, newlines or escape sequences is offered as a candidate and inserted on the user's command line; or completion hangs the prompt; or completion prints an error into the prompt line.
- **Impact:** command execution on the user's computer when they press Enter; a frozen terminal; credential use without the user noticing.
- **Prevention:** candidates and descriptions are sanitised (section 7) and must match a conservative allowlist, anything else is dropped (`02-commands.md` 3.13.2 rule 4); Cobra's completion directives quote what remains; read-only requests only, never a prompt (rule 1); an 800 ms total budget and silence on any failure (rule 2); no on-disk cache (rule 7); `completion install` writes one file in a user-owned auto-load directory after showing the path, and never edits rc files or uses `sudo`.
- **Detection:** acceptance tests C-01..C-08 call the hidden `__complete` entry with hostile host names.
- **Recovery:** delete the installed completion file (its path was printed).

## 4. Credential handling summary

| Credential | Created by | Stored | Sent | Lifetime |
|---|---|---|---|---|
| Password | user | never | body of `POST /api/auth/login` | - |
| Session token (JWT) | `login` | credential store | `Authorization: Bearer` to its own server only | 24 h by default; revoked server-side by `logout` |
| Device token | `login` ("stay signed in") | credential store | same | 90 days, role operator |
| API token | `tokens create` | by `login --with-token`, or nowhere (CI env) | same | 90 days default, max 365 |
| Enrolment token | `hosts add --agent`, `hosts enroll` | never | shown once | 1 h default, 24 h max |
| SSH key / password for access | `access add` | server side only, encrypted | once, to `POST /api/v1/secrets` | until rotated |

## 5. Confirmation tiers

| Tier | When | How | Non-interactive |
|---|---|---|---|
| T1 | create, tag, run, enrol | `[y/N]`, default no | `--yes` |
| T2 | remove or revoke anything; **any change in a protected env**; `--on all` | type the exact name (or host count) | `--confirm NAME`; protected: `--confirm-env ENV` |
| T3 | host-key trust, changed key, sign-in | a purpose-built flow (typed fingerprint prefix; password) | `--fingerprint`; never `--yes` |

In every tier the prompt repeats the server, env, action and count, and says what is **not** being done (`03-ux.md` section 6). With `--no-input` and a missing answer the command exits 2 and names the flag.

## 6. Destructive-operation standard and protected envs

### 6.1 Classes

| Class | Examples | Rule |
|---|---|---|
| D0 read | `hosts`, `run ID`, `whoami` | no confirmation |
| D1 reversible change | `hosts tag`, `envs use`, `switch` | no confirmation, message says how to undo |
| D2 state change | `hosts add`, `access add`, `hosts enroll`, `tokens create`, **every `run`**, `run --retry-failed` | T1 with preview |
| D3 destructive or hard to undo | `hosts remove`, `hosts move`, `hosts revoke-agent`, `access remove`, `envs remove`, `tokens revoke`, `admin secrets delete`, `admin settings rollback`, `run ID --cancel`, `logout --everywhere`, key replacement | T2, plus a line that says what is not undone |

### 6.2 The standard
1. Say **what, where, how many** before asking, using the names the user knows.
2. Default is no. A bare Enter never confirms D2 or D3.
3. D3 asks for the exact name typed; a pasted `yes` is not accepted.
4. Every destructive output ends with what was **not** done and the command for it (removal, section 6 of `03-ux.md`).
5. No pattern matching on program text; every run gets the same treatment.
6. `--yes` is flag-only (never an environment variable; `config.go:173`), applies to T1 only.
7. `--dry-run` is accepted by every D2 and D3 command: it prints the preview and exits 0 without a mutating request.
8. The CLI never batches D3 across servers or across envs in one invocation.

### 6.3 Protected envs
- **Controller side (C-02):** an environment has `protected: true|false`. A request that changes hosts in a protected env (submit a run, update, delete, trust, move) must carry `confirmEnvironment` equal to the env name or is refused with HTTP 409 `protected_environment_confirmation_required` naming the env. This makes the guard hold for every client, including scripts that bypass the CLI's prompts by mistake.
- **CLI side:** on that refusal, or before it when the env is known to be protected, the CLI asks the user to type the env name (interactive) or requires `--confirm-env ENV` (non-interactive; `--yes` is not enough) and sends it as `confirmEnvironment`.
- **Implemented (Phase 10, step 10.2):** `protected` is stored per environment; submit, host update/move/delete/trust need `confirmEnvironment` (a name or a list of names; for `DELETE` a repeated query parameter) naming every protected env touched, exact match, for every principal; unprotecting is owner-only and audited (`environment.protection_changed`); `PUT` without `protected` keeps it. Not guarded: host label changes and environment rename/delete (an env with hosts cannot be deleted).
- **Against an older server (no C-02):** a *local guard* may be set (`envs add --protected`, answer yes to the warning). It is stored in the CLI config for that server and enforced by the CLI only. It is labelled `protected (local)` everywhere and is described as protection against typing mistakes, not against other people, other computers or other tools. It never appears as a server property.
- **Multi-env targets:** a run whose hosts span several envs lists them and requires one typed confirmation per protected env.

## 7. Sanitiser specification

Applied to any string from the server or a host before it is written to a terminal in table or text form.

1. Invalid UTF-8 -> U+FFFD.
2. Remove complete escape sequences: CSI (`ESC [ ... final`), OSC (`ESC ] ... BEL|ESC \`), DCS, SOS, PM, APC, and two-byte `ESC x` forms; an unterminated sequence is removed to the end of the string.
3. Remove C1 controls (U+0080-U+009F) and DEL.
4. Replace remaining C0 controls except newline and tab with U+FFFD (a bare carriage return becomes U+FFFD, not a line overwrite); tabs become four spaces in tables; newlines in a table cell become a space.
5. Remove bidirectional controls (U+202A-U+202E, U+2066-U+2069, U+200E, U+200F) and zero-width characters (U+200B-U+200D, U+2060, U+FEFF) so text cannot be visually reordered or disguised.
6. Truncate to a bound (host names 255, errors 500 runes as today in `api/errors.go:176`).
7. Count removals; the caller may report "N control characters removed".

Not applied to `-o json` / `-o yaml` (their encoders escape control characters) or to `--raw` with a non-terminal stdout.

## 8. Residual risks (accepted, stated)

- Malware running as the user can read a keychain entry once it is unlocked, or the 0600 file, and can run `machinarchy` itself.
- The controller is trusted; a compromised controller can show false results and false fingerprints.
- First-contact host-key trust relies on the user checking the fingerprint out of band.
- The CLI cannot stop a remote command that ignores TERM/KILL or double-forks under `setsid` (`docs/operations.md`).
- A local protected-env guard is not an authorization boundary.
