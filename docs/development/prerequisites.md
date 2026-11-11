# Development Prerequisites

Install the required tools before following [Getting Started](getting-started.md).

## Required tools

### Go

Use the Go version required by the repository. The `go` directive in the root `go.mod` is the source of truth; if the file also contains a `toolchain` directive, follow that toolchain requirement as well.

Check the declared version and the installed toolchain:

```bash
grep -E '^(go|toolchain) ' go.mod
go version
```

Do not independently guess or hard-code a Go version in this document. Update this guide when the repository's `go.mod` requirement changes.

### Git

Git is required to clone the repository and is used by the build metadata logic to determine the version and commit. It is also required for the repository's optional local Git hooks.

```bash
git --version
git status
```

### GNU Make

GNU Make is required to run the repository's developer commands:

```bash
make --version
make help
```

The Makefile uses standard Unix shell utilities such as `awk`, `find`, and `date`.

### Docker Engine

Docker is required for the local PostgreSQL/NATS infrastructure, application containers, and SSH test fixtures.

```bash
docker --version
docker info
```

The Docker daemon must be running and the current user must have permission to access it.

### Docker Compose v2

The repository uses the Compose v2 plugin syntax (`docker compose`), not the legacy standalone `docker-compose` command:

```bash
docker compose version
```

Confirm that the installed Compose version is compatible with the Compose file syntax in the repository.

## Optional tools

These tools are needed only for the corresponding Make targets or workflows.

| Tool | Used by | Required for |
|---|---|---|
| `golangci-lint` | `make lint`, `make verify` | Go linting and standard verification |
| `gosec` | `make gosec`, `make security`, `make verify-full` | Go security analysis |
| `govulncheck` | `make vuln`, `make security`, `make verify-full` | Go vulnerability analysis |
| `shellcheck` | `make shellcheck`, `make verify-full` | Shell script linting |
| `swag` | `make api-docs` | Swagger/OpenAPI documentation generation |
| C compiler and CGO toolchain | `make test-race` | Go race detector support |

Install these tools using their official installation instructions and choose versions compatible with the Go version declared by the repository. Avoid relying on an unpinned globally installed tool in CI; use the repository's documented CI/tool version policy when one is defined.

## Verify the installation

Run the basic checks:

```bash
go version
git --version
make --version
docker --version
docker compose version
```

Then, from the repository root:

```bash
make help
make deps
```

The following commands additionally require the relevant optional tools and services:

```bash
make verify        # Requires golangci-lint
make security      # Requires gosec and govulncheck
make shellcheck    # Requires ShellCheck
make api-docs      # Requires swag
make test-race     # Requires CGO_ENABLED=1 and a working C toolchain
make test-postgres # Requires a configured reachable PostgreSQL test database
```

See [Troubleshooting](troubleshooting.md) for common installation and connectivity problems.
