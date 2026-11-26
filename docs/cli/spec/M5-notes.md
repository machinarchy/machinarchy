# M5 implementation notes

Milestone M5 (home screen and brand). Spec files stay the design record.

## Built

| Spec item | Where |
|---|---|
| State-aware home screen, `-o json`, 3 s budgets | `commands/home.go` (5.1) |
| Logotype, ASCII fallback, `--plain` | `ui/logotype.go`, `Factory.Logotype` (5.2) |
| Catalogue applied to status lines, `--tagged` | `msg`, `commands/` (5.3) |
| Help groups, examples, did-you-mean | `help.go`, `suggest.go`, `registry.go` (5.4) |
| Lore guard (U-04), one-time completion hint, plain help audit (U-05) | `msg/msg_test.go`, `commands/login.go`, `commands/vocabulary_test.go` (5.5) |

## Deviations and limits

1. The completion hint points at `machinarchy completion --help`: `completion install` is not implemented (spec 03-ux section 11).
2. The hint goes to stderr, so stdout stays clean for pipes.
3. `machinarchy --version` does not exist (only `version`); left to 6.4.
4. Tables, prompts and `fmt.Errorf` text are not in the catalogue; `docs/cli/user-full-guide.md` is still old (6.4/6.5).
5. Not run: pty look at colours, Postgres, SSH lab.
