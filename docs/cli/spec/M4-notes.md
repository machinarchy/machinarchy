# M4 implementation notes

What was built for milestone M4 (runs), and where it differs from the spec. The spec files stay as the design record.

## Built

| Spec item | Where |
|---|---|
| `--on` / `--except` parser, globs, env scope, conflict errors (O-23..O-26) | `internal/cli/target` |
| `run` execution flow: preview, blast-radius confirm, submit with `expectTargetsHash`, wait, detach | `commands/run.go`, `runexec.go` |
| Watch loop, progress, backoff, resume messages, `--wait-timeout` | `commands/runwatch.go` |
| Grouped result, `--no-groups`, `--groups`, `--fail-on-drift`, exit 0/8/9 | `commands/runresult.go`, `group.RenderPerHost` |
| `run` (list), `run ID`, `--host`, `--follow`, short IDs and prefix lookup | `commands/runshow.go`, `runutil.go` |
| `--retry-failed`, `--cancel` | `commands/runactions.go` |
| Capability form `--do NAME KEY=VALUE...`, `--input-json` | `commands/run.go`, `runactions.go` |
| Completion of `--on`, `--except`, run IDs, `--host`, `--do`, `--stream` | `commands/runcomplete.go` |
| Messages `run.*` | `internal/cli/msg/msg.go` |
| `clierr.Reported`: an exit code with nothing more to print | `internal/cli/clierr`, `internal/cli/run.go` |
| Old forms removed: `run -l/--host/--all/--exclude/--wait/--capability/--input`, `get operations`, `cancel operation`, `logs operation` | `commands/run.go`, `get.go`, `registry.go` |

## Decisions and deviations from the spec

1. **No aliases or legacy semantics** (project rule). `10-roadmap.md` M4 item 9 and tests K-04..K-06 (old `run -l` keeps its semantics, exit 1) are not implemented: the old forms fail as unknown flags or commands (exit 2). Test `TestRunOldFormsAreGone`.
2. **Protected envs follow the hosts, not only the active env.** The guard looks at the env of every previewed host (and of every host of a run being cancelled), so `run --on web-01` with no active env still asks for the env's name when web-01 is in a guarded env. The guard is local-only (C-02 not accepted). `--yes` never answers the typed env name; without a terminal a protected run needs both `--confirm-env ENV` and `--yes`.
3. **`--on all` outside any env** asks to type the host count on a terminal; `--yes` also accepts it (so a script can run on everything). The spec table lists `--yes` for T1 and says nothing for this count.
4. **Argument quoting in the preview**: an argument is shown as typed when it has only shell-safe characters and in single quotes otherwise (`'/tmp/my dir'`), a Go string when it holds control characters. The spec sentence quotes every argument; its own example (`systemctl restart nginx`) does not.
5. **Auto-detach** applies to capabilities too: `--timeout` above 15 minutes or `--detach` returns after the submit; the controller has no detach mode for capabilities.
6. **`run` list "WHAT"** needs the command, which the list API does not carry, so each listed run is read (8 at a time). For 20 runs with large outputs that is heavy until the controller offers a summary (C-06). A run that cannot be read shows `(hidden)`.
7. **Result output** comes from the run itself; only a finished detached host with output but no stored text is read from the output endpoint (8 at a time, 64 KiB per stream). A failed read leaves that host without output, so two hosts whose reads failed can look alike.
8. **Run ID prefixes** are looked up in the 500 most recent runs; IDs are the controller's UUIDs and `r-xxxxxxxx` is their first 8 characters (C-10 not shipped).
9. **Ambiguous submit**: no HTTP answer, an interrupt, or HTTP 500/502/504 during the submit print the "may or may not have been created" message and exit 130 (interrupt) or 4; the CLI never resubmits.
10. **`run ID --host`** prints stdout on stdout and stderr on stderr (or one stream with `--stream`), then a status line on stderr. `--raw` is refused when stdout is a terminal.
11. **Retry of a removed host**: hosts of the old run that no longer exist are left out and listed ("no longer managed"); the retry uses host IDs, not names.
12. The success hint of `hosts add` still points at `hosts show` (the spec's `run --on NAME -- uptime` is a later wording change).

## Not done / left

- Real terminal checks: a second Ctrl+C ending the process (done by `cmd/machinarchy/main.go`, not testable without a pty), the redrawn progress line, and the typed prompts on a real terminal were not run; the signal behaviour is tested through a cancelled context.
- Lab run on three SSH hosts with one down, and any run against a real controller, were not run. `scripts/smoke.sh` still uses the removed `run -l` and `get operations` and needs the owner's approval to change (see the report).
- Memory test at 5,000 hosts through the CLI and the fake of the proposed C-06 contract (G-04/G-05 are covered at library level by the M1 tests); runs above 200 hosts stay refused (C-05).
- Timing against a real pty for O-14..O-16.
