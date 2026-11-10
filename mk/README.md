# Machinarchy modular Makefiles

This package contains a root `Makefile` and eight included Makefiles under `mk/`.

## Install

Copy the `Makefile` and the complete `mk/` directory into the Machinarchy
repository root, replacing the existing root `Makefile` if appropriate.

The Makefiles assume the repository paths shown in the project tree, including:

- `deployments/infra.docker-compose.yml`
- `deployments/testing/ssh-servers.docker-compose.yml`
- `scripts/dev/setup-local.sh`
- `scripts/dev/setup-ssh-servers.sh`
- `scripts/db/test-postgres.sh`

## SSH Compose filename

The generated configuration standardizes the SSH fixture Compose filename to
`deployments/testing/ssh-servers.docker-compose.yml`. Rename your existing
`deployments/testing/sshServers.docker-compose.yml` to this name and remove the
duplicate `scripts/dev/sshServers.docker-compose.yml` after confirming it is no
longer referenced.

## Commands

Run `make help` to list the documented targets. Existing command names from the
previous Makefile are retained.

## Notes

- `make dev-up` starts infrastructure first and applications second.
- `make dev-down` stops applications first and infrastructure second.
- Compose commands use `ENV_FILE` (default: `.env`).
- `run-*` targets source `ENV_FILE` as a trusted shell-compatible file. Do not
  source an untrusted `.env` file.
- The build metadata is read from `go.mod` and Git. Run these commands from the
  repository root.
