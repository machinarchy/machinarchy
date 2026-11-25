# 08 - Migration

> Goal: nobody's script breaks on the day the new grammar ships, and everybody is told what to type instead. **Plan:** release **R** is the first release with the new grammar. In R every old command still works, **behaves exactly as before** (same output, same exit codes, same flags) and prints a one-line replacement on stderr. In release **R+1** the old commands are removed. R is not given a number here: it is whatever Release Please produces for the first conventional commit marked as a breaking change (`feat!:` with a `BREAKING CHANGE:` footer); whether that is a minor or major bump depends on the repository's release configuration, which this spec does not change.

## 1. Command mapping

"Same behaviour" means the alias is a thin wrapper that calls the old code path unchanged. The deprecation line is `Deprecated: "OLD" is now "NEW". It will be removed in R+1.` on stderr, once per invocation.

| Old command | New command | Notes |
|---|---|---|
| `login --server URL --context NAME --username U [--password-stdin]` | `login URL --name NAME` (email or username prompt; `--password-stdin`) | Old form keeps working and creates a v2 server entry. `--username` is accepted as the email/username. |
| `config set-credentials NAME --token-stdin` | `login URL --with-token` | Old form still writes a token; prints the replacement. |
| `config set-credentials NAME --token TOKEN` | `login URL --with-token` (stdin) | **`--token` prints a warning** that the value is visible in history and in `ps`, and is removed in R+1 together with the alias. |
| `config set-cluster NAME --server URL [--insecure-http]` | `login URL [--insecure-http]` | Writes the same server entry. |
| `config set-context NAME --cluster C --user U [--env E]` | `login`, then `envs use E` | Maps to a server entry plus its default env. |
| `config use-context NAME` | `switch NAME` | |
| `config get-contexts` | `switch` (no argument) | Old table kept, with the old column names. |
| `config current-context` | `whoami` | Old output kept (one line). |
| `get hosts [NAME] [-l SEL] [-o ...]` | `hosts [NAME] [--on SEL]` | Old table and `-o json\|yaml` shape unchanged. |
| `get profiles` | `access` | |
| `get operations [ID] [--state S] [--limit N]` (aliases `operation`, `ops`, `runs`) | `run [ID] [--state S] [--limit N]` | |
| `get tokens` | `tokens` | |
| `get agent-tokens` | `hosts enroll --list` | Hidden helper that lists enrolment tokens (`GET /api/v1/agents/enrollment-tokens`). |
| `create host NAME --profile P [--endpoint E] [--label k=v] [--trust FP]` | `hosts add NAME E --access P --tag k=v --trust-fingerprint FP` | Old command creates the host exactly as before (no waiting, no wizard). |
| `create profile NAME --connector ssh\|agent --username U --secret S --auth A ...` | `access add NAME --how ssh-key\|ssh-password\|agent --user U ...` | The old `--secret` (a pre-created secret name) is kept in the alias; the new command creates the secret itself. |
| `create token NAME --role R [--expires-hours N]` | `tokens create NAME --role R [--expires Nd]` | `--expires-hours N` maps to `--expires {N}h`. |
| `create agent-token --host H [--hostname N] [--ttl D]` | `hosts enroll H [--ttl D]` | `--hostname` kept as a flag of `hosts enroll`. |
| `delete host NAME` | `hosts remove NAME` | Old command keeps its single `[y/N]`; new command asks to type the name. |
| `delete profile NAME` | `access remove NAME` | |
| `delete token ID` | `tokens revoke NAME` | Old form takes the numeric ID; new form takes the name (or `--id`). |
| `delete agent-token ID` | `hosts enroll H --revoke-old` | Old form by ID kept. |
| `label host NAME k=v k-` | `hosts tag NAME k=v k-` | |
| `trust host NAME [--replace] [--fingerprint FP]` | `hosts trust NAME [--replace] [--fingerprint FP]` | **Safety change applies to the alias too**: `--yes` no longer approves trust; see section 3. |
| `trust host --all-pending [--selector S]` | `hosts trust --all-pending [--on S]` | Same safety change; refused with `--no-input`. |
| `run -l SEL \| --host H... \| --all [--exclude H...] [--wait] [--detach] [--timeout N] [--concurrency N] -- PROG` | `run --on TARGET [--except H] -- PROG [--timeout D]` | **Legacy mode:** when any of `-l`, `--host`, `--all`, `--exclude` is present the command runs with the **old semantics** (detached unless `--wait`, old output, exit 1 on failure). New flags use the new semantics. |
| `run ... --capability NAME [--input JSON]` | `run --on T --do NAME [k=v] \| --input-json JSON` | Old flags keep their old behaviour in legacy mode. |
| `cancel operation ID` | `run ID --cancel` | |
| `logs operation ID [--host H] [--stream S] [--follow]` | `run ID [--host H] [--stream S] [--follow]` | Old output (per-host `==> name <==` blocks) unchanged in the alias. |
| `runtime-config get\|history\|status\|validate\|publish\|rollback` | `admin settings get\|history\|status\|validate\|publish\|rollback` | Same flags. |
| `secret list\|create\|versions\|rotate\|rewrap\|delete` | `admin secrets list\|create\|versions\|rotate\|rewrap\|delete` | Same flags. |
| `version` | `machinarchy --version` | `version` stays as a hidden command (no deprecation needed). |

### 1.1 Global flags and variables

| Old | New | Notes |
|---|---|---|
| `--context NAME` | `--server NAME` | `--context` kept as hidden alias, deprecation line. |
| `-n`, `--env` | `--env` | `-n` removed from new commands; legacy aliases keep it. |
| `--server URL` | `--server NAME-OR-URL` | **Behaviour change (security):** a saved credential is not sent to a URL that is not saved (R3). Scripts that pass `--server URL` with a saved token must pass `MACHINARCHY_TOKEN` or save the server first. This also applies to legacy aliases and is the one place where an old command's behaviour changes. |
| `--config FILE`, `MACHINARCHY_CONFIG` | unchanged | |
| `-o`, `-v`, `-q`, `--no-headers`, `--no-input`, `-y/--yes`, `--request-timeout` | unchanged | `--yes` semantics narrowed for trust and protected envs. |
| `MACHINARCHY_CLI_SERVER` | `MACHINARCHY_SERVER` | old name read with a warning |
| `MACHINARCHY_CLI_CONTEXT` | `MACHINARCHY_SERVER` (a saved server name) | |
| `MACHINARCHY_CLI_TOKEN` | `MACHINARCHY_TOKEN` | |
| `MACHINARCHY_CLI_ENV` | `MACHINARCHY_ENV` | |
| `MACHINARCHY_CLI_OUTPUT` | `MACHINARCHY_OUTPUT` | |
| `MACHINARCHY_CLI_NO_INPUT` | `MACHINARCHY_NO_INPUT` | |
| `MACHINARCHY_CLI_REQUEST_TIMEOUT` | `MACHINARCHY_REQUEST_TIMEOUT` | |
| `MACHINARCHY_CLI_VERBOSE`, `..._QUIET`, `..._NO_HEADERS` | same names without `_CLI` | |
| (none) | `MACHINARCHY_PLAIN`, `MACHINARCHY_NO_DEPRECATION_WARNINGS` | new |
| `--yes` as an environment variable | still **not** supported | unchanged |

When both an old and a new variable are set the new one wins; using an old one prints one warning line naming the replacement.

## 2. Breaking changes (what is not "the same")

| # | Change | Who is affected | Why it is acceptable |
|---|---|---|---|
| B1 | `run` in the new form **waits by default** (old: detached unless `--wait`). | Anyone who switches to the new form and relied on a quick return. | Fixed decision 8. Legacy flags (`-l`, `--host`, `--all`) keep the old behaviour in R. Use `--detach` for the old return-at-once behaviour. |
| B2 | New `run` exit codes 8 and 9 for partial and total failure. | Scripts testing `$? -eq 1` after the **new** form. | Both are non-zero; legacy form still exits 1. |
| B3 | Config file format v2, credentials in a separate file (or keychain). | Anyone who reads the old YAML by script, or downgrades. | Automatic conversion with a byte-exact backup (`05-architecture.md` 4.5); old CLIs ignore the new files. |
| B4 | `--server URL` does not carry a saved credential to an unsaved URL. | Scripts that rely on that. | Security fix (audit F-03). Use `MACHINARCHY_TOKEN`. |
| B5 | `--yes` does not approve host-key trust. | Provisioning scripts that ran `trust host --all-pending --yes`. | Security fix (audit F-01). Use `hosts trust NAME --fingerprint FP` or set the access policy to `pinned` or `tofu` (lab). |
| B6 | `--token` removed after R. | Scripts using `config set-credentials --token`. | Replaced by `login --with-token` over stdin. |
| B7 | `-n` removed from new commands. | Muscle memory. | `--env` stays; legacy aliases keep `-n`. |
| B8 | `hosts` default table has new columns. | Scripts parsing the old table. | Parse `-o json` (field names unchanged); the old table is still produced by the legacy `get hosts`. |
| B9 | A protected env (when configured) requires `--confirm-env` in non-interactive runs. | New feature; affects only envs marked protected. | Opt-in by an owner. |
| B10 | `trust --all-pending` is refused with `--no-input`. | Provisioning scripts. | Same as B5. |

The `-o json` and `-o yaml` outputs of resource commands (hosts, access, tokens, runs) keep the API's own field names and are **not** reshaped; new fields may be added, none removed or renamed (`09-acceptance-tests.md`, automation).

## 3. Behaviour of the safety changes inside the aliases

The security changes B4, B5 and B10 apply to the old commands too. They are the only deliberate differences between an alias and the original behaviour. Each prints its own explanation:

```
$ machinarchy trust host web-01 --yes
Deprecated: "machinarchy trust host" is now "machinarchy hosts trust". It will be removed in R+1.
Error: --yes cannot approve an SSH host key.
Why:   approving a key decides which machine receives your credentials; it needs to be a deliberate act.
Next:  machinarchy hosts trust web-01          (asks you to check the fingerprint)
       machinarchy hosts trust web-01 --fingerprint SHA256:...      (for scripts)
```
Exit code 2.

## 4. The one-release plan

| When | State |
|---|---|
| Before R (today) | Old grammar only. |
| **R** | New grammar. Every old command works via `deprecate.Alias` (`05-architecture.md` 8) with the deprecation line. Old variables work with a warning. Config converts automatically. Old commands are hidden from `--help` and from completion. Docs: new quick start; the old user guides move under "legacy" headings and note the replacements. Release notes contain the table of section 1. |
| R+1 | Old commands, `--context`, `MACHINARCHY_CLI_*` variables and `--token` are removed. `config.yaml.v1.bak` is never deleted by the CLI. Running an old command prints `"X" was removed in R+1. Use "Y".` and exits 2, so the failure is understandable instead of "unknown command". That stub stays one more release (R+2) and is then deleted. |

Release Please: the commit that introduces the grammar is `feat!: ...` with a `BREAKING CHANGE:` footer listing B1-B10 so the release notes carry them. The removal commit in R+1 is `feat!:` as well.

## 5. Communication checklist (part of M6)
- [ ] Release notes: the section 1 table, section 2 list, and the date or release of removal.
- [ ] `docs/cli/README.md` quick start rewritten for the new grammar; old pages labelled legacy.
- [ ] A line in `machinarchy --version` output? **No**: version output stays machine-friendly.
- [ ] `MACHINARCHY_NO_DEPRECATION_WARNINGS=1` documented as a stop-gap for scripts.
- [ ] Every deprecation line is covered by an acceptance test (`09-acceptance-tests.md` K-series).
