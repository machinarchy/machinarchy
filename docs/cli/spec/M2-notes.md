# M2 implementation notes

What was built for milestone M2 (servers and authentication), and where it differs from the spec. The spec files stay as the design record.

## Built

| Spec item | Where |
|---|---|
| `login` (guided; email and password, API token; `--with-token`; token checked before saving; "stay signed in") | `internal/cli/commands/login.go` |
| `logout`, `logout --everywhere`, `whoami`, `switch` | `internal/cli/commands/account.go` |
| `tokens`, `tokens create`, `tokens revoke` | `internal/cli/commands/tokens.go` |
| hidden `config path`, `config show` | `internal/cli/commands/config.go` |
| Credential store | `internal/cli/cred` (file backend), `internal/cli/fsutil` |
| Servers (config v2) and credential resolution | `internal/cli/config/config.go`, `resolve.go` |
| Prompts | `internal/cli/ui/prompt.go` |
| Anonymous endpoint, sign-in, CA trust, `/me`, `/sessions`, logout | `internal/cli/api/auth.go` |
| Owner-only commands | `cli.AnnotationOwner`, `commands/registry.go` |

## Deviations from the spec

1. **No legacy support, at the product owner's decision.** The spec planned deprecated aliases and a migration from today's plaintext config (`08-migration.md`). None of it was built, and the M1 deprecation mechanism was deleted:
   - `config set-cluster|set-credentials|set-context|use-context|get-contexts|current-context`, `--context`, `MACHINARCHY_CONTEXT`, `login --server --context --username`, `--token` and the token commands under `get`/`create`/`delete` are gone.
   - A config file in the old format is **refused**, never converted: the error names the file, says its tokens were not copied, and prints the commands to move it aside and sign in again. Nothing is overwritten and no token is carried over.
   - The `MACHINARCHY_CLI_*` variable names no longer work either (that was already done before M2's first commit).
2. **No keychain backend.** Open question Q5 (a new third-party dependency) was not answered, so only the file backend exists. `cred.Store` is the interface a keychain backend would implement; the 0600 file, the refusal of a file others can read, and the redacting `Credential` type are in place.
3. **The role is often "not reported".** `GET /api/v1/me` returns only the account (controller work C-03), so `login` and `whoami` print "not reported by this server" for a session and for a token the person supplied. The role and expiry are shown when the CLI itself created the credential (a device token) and the session's expiry comes from `GET /api/v1/sessions`.
4. **"Server reached" is any HTTP answer from `GET /healthz/ready`.** There is no unauthenticated endpoint that proves the server is Machinarchy; the sign-in that follows does ("that address did not answer like a Machinarchy server").
5. **`logout` revokes the device token the CLI created** before it ends the session, so a logged-out laptop keeps no working token. The spec only said the local copy is removed.
6. **`back` ends the sign-in** ("aborted; nothing was changed") instead of returning to the previous question; the step-back flow of `03-ux.md` is not implemented for `login`.
7. **Owner commands are marked by an annotation** (`cli.AnnotationOwner`) on `tokens`, `secret`, `runtime-config`, `trust host`, `create|delete profile`, `create|delete agent-token` and `get agent-tokens`. The spec named the rule, not the list.
8. **`--insecure-http` is saved per server** and is a `login` flag only; `--ca-file` copies the CA to `ca/NAME.pem` next to the config.
9. **`switch` on a terminal asks "Make which server active?"** as a numbered choice; without a terminal it prints the list.
10. **Terminal detection uses isatty.** The earlier check treated any character device, including `/dev/null`, as a terminal, so a script with stdin from `/dev/null` was asked questions.
11. **`scripts/smoke.sh` changed** (approved per file): it signs in with `login` instead of the removed `config set-*` commands.

## Found by running it against a real controller

A fresh `bin/machinarchy-monolith` was driven with the built CLI (login with a device token, `whoami`, `tokens` list/create, an owner command with only a device token, a token in the environment, logout, `make smoke`). It found three problems that the fake server did not: the terminal check above, a repeated path in `config show`, and that `-o yaml` on an issued token panicked (a duplicated `token` key; it affected `create token -o yaml` before M2 too).
