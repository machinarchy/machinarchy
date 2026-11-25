# Local SSH test fixtures

This Compose file provides three isolated SSH endpoints for local Machinarchy
SSH integration and host-key tests. It is separate from the application
`docker-compose.yml` stack.

## Endpoints

| Service | Host endpoint | Authentication | Purpose |
|---|---|---|---|
| `ssh-password` | `127.0.0.1:2221` | Password only | Password-auth integration |
| `ssh-key` | `127.0.0.1:2222` | Ed25519 public key only | Private-key integration |
| `ssh-hostcheck` | `127.0.0.1:2223` | Password only | Persistent host-key/fingerprint tests |

## Start and stop

Run commands from the repository root:

```bash
make ssh-servers-up
make ssh-servers-ps
make ssh-servers-logs
make ssh-servers-down
```

`make ssh-servers-up` first runs `scripts/dev/setup-ssh-servers.sh` to generate
local-only credentials and OpenSSH policy snippets, validates this Compose
file, and then starts the services. The setup script does not invoke Docker.
The Makefile owns the Compose lifecycle.

The test password is prompted for on first setup and saved with mode `0600` at
`data/.ssh/<SSH_TEST_USER>.password`. You may instead provide
`SSH_TEST_PASSWORD` in the environment. Do not put test credentials in the
Compose YAML or commit them.

## Local files

All generated keys, passwords, OpenSSH configuration snippets, and each
container's `/config` directory are under `data/.ssh/`. Add this to `.gitignore`:

```gitignore
/data/.ssh/
```

Each server has its own persistent `/config` directory. The LinuxServer image
stores generated SSH host keys under `/config/ssh_host_keys`, so the
`ssh-hostcheck` host identity survives container recreation as long as its
host directory is preserved. `make ssh-servers-down` does not delete this data.

## Configuration

Useful environment overrides:

```bash
SSH_TEST_USER=testuser make ssh-servers-up
SSH_PASSWORD_HOST_PORT=3221 make ssh-servers-up
SSH_KEY_HOST_PORT=3222 make ssh-servers-up
SSH_HOSTCHECK_HOST_PORT=3223 make ssh-servers-up
SSH_SERVER_IMAGE=lscr.io/linuxserver/openssh-server:<tested-tag> make ssh-servers-up
```

The published ports bind to loopback only; these services are intended for
local development and should not be exposed to a LAN or the public internet.

The Compose file currently defaults to the image's `latest` tag for convenient
local experimentation. For reproducible CI, pin `SSH_SERVER_IMAGE` to a tested
version tag or immutable digest.
