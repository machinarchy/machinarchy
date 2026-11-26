# Repository instructions

## Change log requirement

- For every project change, update the root `ChangeLogAI.md` in the same working change. This includes source code, tests, documentation, configuration, dependencies, migrations, and build or deployment files.
- Add entries newest first using the current date in `YYYY-MM-DD` format. Summarize what changed and why; include affected areas when useful.
- Record checks only after running them. State clearly when relevant tests, benchmarks, or security checks were not run.
- Distinguish implemented behavior from planned work and known limitations. Do not rewrite baseline history to make an existing feature appear newly implemented.
- Keep entries concise, factual, and useful to maintainers. Do not include secrets or sensitive values.

Follow the repository's development and security guidance in `CONTRIBUTING.md` and `docs/architecture.md` when making changes.

<!-- machinarchy-ai-workflow:begin -->
## AI workflow
- Roadmap: root `PLAN.md` (local-only: never stage or commit it, or `.plan/`). `/next` executes steps; `/roadmap` creates or refreshes it.
- One logical step per run. Never start future or unplanned work.
- Read narrowly: the step's files, their tests, direct dependencies. Prefer `rg` and line ranges over whole files; no repo-wide scans. Listed paths are a starting scope, not a limit when correctness needs more.
- Never claim a check passed unless it ran and passed. Report checks not run.
- Never trade correctness, security, or required tests for fewer tokens.
- Be terse: no restating the task, no file dumps, no narration.
<!-- machinarchy-ai-workflow:end -->
