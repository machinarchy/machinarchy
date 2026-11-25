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

## The agent repository

machinarchy imports the agent wire contract,
`github.com/machinarchy/machinarchy-agent/protocol`, through
`replace => ../machinarchy-agent/protocol`. Every job therefore checks out
this repository to `machinarchy/` and `<owner>/machinarchy-agent` to
`machinarchy-agent/` side by side.

- Repository variable `MACHINARCHY_AGENT_REF` (optional): agent branch, tag or
  SHA to build against. Defaults to `main`. Pin it for releases.
- Secret `AGENT_REPO_TOKEN` (needed only if the agent repository is private):
  a fine-grained token with Contents: Read on `machinarchy-agent`.

## Workflow behavior

- `ci.yml` runs for pushes to `main`, `beta` and `alpha` and for pull requests:
  - `verify`: tidy, gofmt, vet, build, `make coverage` (race detector,
    coverage artifact, PostgreSQL contract suites, OpenAPI route coverage and
    the real agent end-to-end test, which must not skip), `make smoke`, and
    golangci-lint (v2.15.0, includes gosec rules).
  - `security`: govulncheck v1.1.4 through `make vuln` (fails on called
    vulnerabilities unless a reviewed, unexpired entry in
    `scripts/security/govulncheck-allowlist.txt` accepts one with no fix),
    gosec v2.22.4 and a 15s run of each fuzz target.
  - `images`: builds the api, controlmanager, monolith, CLI and agent images;
    the monolith must turn healthy and exit 0 on `docker stop`.
  - On `main`, `beta` and `alpha`, after all three: Release Please for that
    branch's channel (see "Release channels"), then one published, scanned and
    signed image per server process.
- `release.yml` is reusable and runs Release Please after the CI checks pass.
  `dry_run: true` skips Release Please and all release mutations.
- `template-docker.yml` always builds unless `[docker skip]` is present in the
  commit message or PR title. With `docker_push: false`, it does not push, scan,
  or sign. With `docker_push: true`, it pushes, scans by immutable digest, and
  signs that digest with Cosign keyless signing.

## Release channels

The branch you merge into decides what is released:

| Branch | Channel | Version example | GitHub Release | Docker tags |
|---|---|---|---|---|
| `main` | stable | `v1.3.0` | latest release | `1.3.0`, `1.3`, `latest`, `main` |
| `beta` | beta | `v1.3.0-beta.1`, `-beta.2` … | marked pre-release | `1.3.0-beta.1`, `beta` |
| `alpha` | alpha | `v1.3.0-alpha.1`, `-alpha.2` … | marked pre-release | `1.3.0-alpha.1`, `alpha` |

**How the suffix is added.** Release Please computes the next version from the
Conventional Commits on the branch (`feat:` → minor, `fix:` → patch, `!` or
`BREAKING CHANGE` → major, or minor while below 1.0). On `beta` and `alpha` its
`prerelease` versioning appends `-beta.N` / `-alpha.N`: the first release of
a new version ends in `.1`, and further releases of the same version raise only
`N`. The suffix is never typed by hand and never edited in a file.

**Flow of a change:**

1. Merge feature PRs (squash, Conventional Commit title) into `alpha`.
2. Release Please opens a PR "chore(alpha): release 1.3.0-alpha.1". Merging it
   creates the tag, a pre-release with the four CLI binaries, `checksums.txt`
   and provenance attestations, and the three server images.
3. To promote, merge `alpha` into `beta` with a merge commit (not squash, so
   the commit history Release Please reads stays intact), then `beta` into
   `main`. Each branch then makes its own release PR: `1.3.0-beta.1`, then
   `1.3.0`.
4. Fixes found in a beta are committed to `beta` and merged back to `alpha`.

**What stays separate per channel**, so that merging one branch into the next
never conflicts: `.github/release-please/config.<channel>.json`,
`manifest.<channel>.json` (the last released version) and the changelog
(`CHANGELOG.md` for stable, `CHANGELOG.beta.md`, `CHANGELOG.alpha.md`).

**Rules enforced by the workflows:**

- A pre-release never gets `latest` or a `major.minor` image tag, so installs
  that follow `latest` or `1.3` never receive one.
- The release job refuses a tag whose suffix does not match its branch (a
  `-beta.N` tag from `main`, or a stable tag from `alpha`).
- Pre-release notes start with a warning that the build is not for production.

**Using a pre-release:** download the asset from the pre-release page (the
CLI), or pull the exact tag (`ghcr.io/<owner>/machinarchy-monolith:1.3.0-beta.1`)
or the channel tag (`:beta`). Pin the exact tag anywhere that matters. Stored
data may change between alphas; the README already warns that databases may
need to be recreated during development.

**Set up once:** create the branches `beta` and `alpha` from `main`, protect
all three like `main` (required CI checks, no force pushes). The
`manifest.*.json` files record the last released version of that channel and
start empty: the first release of every channel is `0.0.1` (`initial-version`),
so the first tags are `v0.0.1-alpha.1`, `v0.0.1-beta.1` and `v0.0.1`. After
that the commit types decide (below 1.0, a breaking change raises the minor). Not yet exercised: the workflows have never run on
GitHub (AUDIT/10 Phase 10), so the first release of each channel should be
watched.

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

- Release Please creates `CHANGELOG.md` (and the `.beta` / `.alpha` ones) with
  the first release PR; nothing needs to exist beforehand.
- Release builds are pure Go (`CGO_ENABLED=0`; the SQLite driver is
  `modernc.org/sqlite`) and set the version through
  `internal/buildinfo` (`Version`, `Commit`, `BuildTime`).
- The pinned scanner versions in `ci.yml` (govulncheck, gosec) are older than
  the Go toolchain in `go.mod` and must be aligned before CI is relied on
  (AUDIT/10, F9-03).
- `actions/upload-artifact` and `actions/attest` are pinned by tag, not by SHA
  (AUDIT/10, Phase 10).

## Local hooks

Enable the repository hooks once per clone:

```bash
git config core.hooksPath .githooks
```
