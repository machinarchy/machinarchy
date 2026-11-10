# GitHub Actions setup

## Required repository secret

Create an Actions secret named `MY_GITHUB_TOKEN`. Use a fine-grained PAT or a
GitHub App installation token with access to this repository and these
repository permissions:

- Contents: Read and write
- Issues: Read and write
- Pull requests: Read and write

The secret token is used by Release Please to open/update release PRs and
publish a tag/release, and by `gh` to upload release assets. The job-scoped
`GITHUB_TOKEN` remains read-only. Do not substitute `${{ github.token }}` here
if you expect the release-created tag or PR events to start other workflows:
events created by `GITHUB_TOKEN` generally do not trigger subsequent workflow
runs.

The production Docker pipeline uses `GITHUB_TOKEN` for GHCR. Ensure the
repository has `packages: write` permission available and, if the GHCR package
already exists, grant this repository Actions write access to that package.

## Workflow behavior

- `ci.yml` runs for pushes to `main` and pull requests.
- `template-lint.yml` runs formatting and golangci-lint v2 (including gosec).
- `template-db-validate.yml` calls `make test-postgres`. The repository must
  include `scripts/db/test-postgres.sh`, and it must consume `DATABASE_URL` or
  `MACHINARCHY_TEST_POSTGRES_URL`.
- `release.yml` is reusable and runs Release Please after the CI checks pass.
  `dry_run: true` skips Release Please and all release mutations.
- `template-docker.yml` always builds unless `[docker skip]` is present in the
  commit message or PR title. With `docker_push: false`, it does not push, scan,
  or sign. With `docker_push: true`, it pushes, scans by immutable digest, and
  signs that digest with Cosign keyless signing.
- `template-notifications.yml` is optional and requires only the secrets for
  the notification providers enabled by the caller.

## Release Please expectations

Use Conventional Commit subjects (`feat:`, `fix:`, `perf:`, etc.) and squash
merge PRs with a Conventional Commit title. Release Please's Go strategy uses
the Go module and the repository changelog/release configuration. Do not run a
second tag-triggered release workflow alongside `release.yml`.

## Docker image verification

The image signing job uses GitHub OIDC. No long-lived Cosign private key is
required. Verify a published image against the identity of the calling
workflow, not the reusable workflow file; the expected certificate identity
should be reviewed against the actual workflow run before enforcement.

## Before the first release

- Ensure a root `CHANGELOG.md` exists; the Release Please Go strategy expects it.
- Confirm the Go toolchain in `Dockerfile` is compatible with the `go` directive
  in the root `go.mod`. Those files were not both present in the review archive.
- Confirm both `./cmd/machinarchy` and `./cmd/agent` define the linker variables
  `main.Version`, `main.Commit`, and `main.BuildTime`.
- Confirm the SQLite driver supports `CGO_ENABLED=0`. If it is
  `github.com/mattn/go-sqlite3`, these release builds need a CGO-enabled native
  toolchain instead of the current pure-Go cross-compilation approach.
- Ensure `scripts/db/test-postgres.sh` and `scripts/dev/setup-local.sh` exist. They are
  referenced by the Makefile but were not present in the review archive.
- Verify the Compose environment variable names for the database URL and NATS
  endpoint against the actual config loader; the Go configuration source was
  not included.

## Local hooks

Enable the repository hooks once per clone:

```bash
git config core.hooksPath .githooks
```
