# M1 implementation notes

What was built for milestone M1 (Foundations) and where it differs from the spec. The spec files stay as the design record; this file records what the first implementation did.

## Built

| Spec item | Where |
|---|---|
| Sanitiser (`04-security.md` section 7) | `internal/cli/sanitize` (plus `Stream` for followed output) |
| Message catalogue and voice (`07-brand.md`) | `internal/cli/msg` |
| Style, message rendering, progress line | `internal/cli/ui` |
| Grouped-output aggregator and renderer | `internal/cli/group` |
| Unified error format, exit codes 5, 8, 9, JSON errors | `internal/cli/clierr`, `internal/cli/run.go`, `internal/cli/api/errors.go` |
| Global options and precedence | `internal/cli/config/settings.go`, `internal/cli/root.go`, `internal/cli/factory.go` |
| Deprecated-alias mechanism | `internal/cli/deprecate`, `cli.Options.Aliases` |

## Deviations from the spec

1. **Tab completion, prompts and the `clitest` helper are not in M1.** The milestone list given for M1 named the voice layer, the error format, global options, the aggregator and renderer, and the alias mechanism. Static completion, the line and hidden-input prompts of `ui`, and `completion install` move to the milestones that need them (M2 for completion, M2/M3 for prompts).
2. **No old command is registered as a deprecated alias yet.** An alias must name a replacement that exists; none does until M2-M4. The mechanism, its table validation and its tests are in place.
3. **Drift needs a majority.** Spec 03-ux.md 7.2 rule 8 called the smaller groups drift whenever one group has two or more hosts. A 25-host group against 4,764 other hosts is not "the others", so the implementation requires the dominant group to hold more than half of the succeeded hosts (`DriftDominant`). Without a majority the result is `DriftMixed` and the note reads "N different outputs among M hosts; none is the majority." Every other case follows the spec.
4. **One-host groups collapse.** When all succeeded hosts printed something different and there are more than three of them, they are shown as one line (`each printed different output`) instead of a group each. The JSON keeps every group.
5. **`group` depends on `ui` and `msg`, and `ui` depends on `msg`** (the spec allowed `group -> sanitize` only). Rendering a message needs both; an import-graph test (`TestPackageDependencyRules`) fixes the order.
6. **Group lines always carry a marker** (`✔`, `✖`, ...), including when everything succeeded, and the count `(N hosts)` is shown whenever there is more than one group. The spec transcripts drop the marker in the all-success case.
7. **Empty environment variables count as unset.** The previous behaviour treated `VAR=` as "set to empty", which would have hidden saved defaults. This is documented in `docs/cli/configuration.md`.
8. **`--dry-run`, `--confirm` and `--confirm-env` are hidden flags.** No command supports them yet; `--dry-run` is refused by every command that does not declare `cli.AnnotationDryRun`, so it can never be ignored.
9. **The 15 KiB-per-host memory test runs only in a plain `go test`.** Under `-race` and `-cover` the byte scanner is two orders of magnitude slower (130 s measured), so `TestG05FiveThousandHostsMemoryAndTime` skips itself there; `TestG05FiveThousandHostsRendering` (5,000 hosts, short output, golden) always runs.
10. **`--no-groups` (G-16) is not implemented.** It needs per-host samples, which the run command (M4) fetches; the aggregator keeps one sample per group on purpose.
11. **Existing hint text still says `config set-credentials`.** The 401 error now has a `Why:` and a `Next:` line, but the `Next:` is the old command until `login` exists (M2).
12. **`trust host --yes` still approves host keys.** The safety change B5 of `08-migration.md` belongs to M3 with `hosts trust`.
