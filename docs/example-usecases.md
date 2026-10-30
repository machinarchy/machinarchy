# Machinarchy use cases: workspaces, API tokens, and SSH hosts

This guide walks through creating an environment, registering an SSH host through the API, and inspecting it from the API and CLI.

> **Current execution limit:** Host registration and host listing are implemented. Arbitrary user command execution is not yet exposed through an API endpoint or the Machinarchy CLI. The job endpoints only read job records; they do not submit jobs. To run a command today, connect directly with OpenSSH from your workstation after verifying the host key. That direct SSH session is outside Machinarchy's API, audit trail, and job tracking.

## Prerequisites

- A running Machinarchy API, for example `http://localhost:8080` in local development.
- A human owner JWT from the API's signup or login endpoint for setup and token management. Do not use `MACHINARCHY_OPERATOR_TOKEN`; that is the server's signing secret, not an API user token.
- `curl` and `jq` for the API examples.
- Network access from the API server to the SSH host on the configured port.
- SSH credentials with permission to log in to the host.

All accounts share one workspace. Self-registered accounts are read-only `reader` members; the first owner is created from the `MACHINARCHY_BOOTSTRAP_ADMIN_*` settings and promotes other members with `PUT /api/v1/members/{id}/role`. Signup does not send invitations or provide workspace switching. In production, signup is disabled by default; have an operator bootstrap the owner account deliberately. Set the API endpoint and temporary owner JWT for setup. Supply long-lived automation credentials from a secret manager at runtime rather than saving them in project files or shell startup scripts.

Replace these sample values:

```sh
export API_URL='http://localhost:8080'
export API_TOKEN='paste-a-user-jwt-here'
export HOST_NAME='web-01'
export HOST_IP='192.0.2.10'
export SSH_USERNAME='deploy'
export SSH_KEY_FILE="$HOME/.ssh/id_ed25519"
export SSH_PORT=22
```

Use HTTPS for remote or production deployments. Do not paste real secrets into shared terminals, tickets, or shell scripts committed to source control.

## 1. Issue an API token for automation

Use the human owner JWT to create an `operator` token for inventory updates or a `reader` token for read-only inventory and operational data. Tokens cannot have the owner role, and their effective access is capped by the issuer's current workspace membership. The plaintext is returned once and stored server-side only as a SHA-256 hash. The default lifetime is 30 days; the maximum is 365 days.

Create a short-lived read-only token and copy the `token` value from the response directly into your secret manager:

```sh
curl --silent --show-error --fail-with-body \
  --request POST "$API_URL/api/v1/tokens" \
  --header "Authorization: Bearer $API_TOKEN" \
  --header 'Content-Type: application/json' \
  --data '{"name":"host-inventory-read","role":"reader","expiresInHours":168}' | jq .
```

Use that provisioned token for API/CLI automation, while retaining the human JWT for token administration. `GET /api/v1/tokens` lists the current user's token metadata; `DELETE /api/v1/tokens/{id}` revokes a token. Send secrets only in an HTTPS `Authorization: Bearer` header. Do not put them in URLs, committed files, shell history, or logs.

## 2. Create an environment through the API

Host registration accepts an `environmentId`. Create an environment and capture the ID from its response:

```sh
ENVIRONMENT_JSON=$(curl --silent --show-error --fail-with-body \
  --request POST "$API_URL/api/v1/environments" \
  --header "Authorization: Bearer $API_TOKEN" \
  --header 'Content-Type: application/json' \
  --data '{"name":"production","description":"Production hosts"}')

printf '%s\n' "$ENVIRONMENT_JSON" | jq .
export ENVIRONMENT_ID=$(printf '%s' "$ENVIRONMENT_JSON" | jq -r '.id')
```

If the environment already exists, list environments and set `ENVIRONMENT_ID` to its ID:

```sh
curl --silent --show-error --fail-with-body \
  "$API_URL/api/v1/environments" \
  --header "Authorization: Bearer $API_TOKEN" | jq .
```

## 3. Verify the SSH host key

Registration requires the host's expected SHA-256 SSH host-key fingerprint. Obtain the fingerprint through a trusted channel, such as your cloud provider's console or the host administrator. You can inspect the key presented over the network with:

```sh
ssh-keyscan -p "$SSH_PORT" "$HOST_IP" 2>/dev/null | ssh-keygen -lf - -E sha256
```

`ssh-keyscan` only reports what the network endpoint presented; it does not authenticate that key. Compare the result against the fingerprint obtained through your trusted channel. Do not blindly trust a fingerprint returned by an unverified connection.

Set the verified value, including the `SHA256:` prefix:

```sh
export SSH_HOST_FINGERPRINT='SHA256:replace-with-verified-fingerprint'
```

## 4. Register the SSH host through the API

The API accepts either an IP address or hostname, an SSH username, an authentication method, its secret, and the expected host-key fingerprint. This example reads a private key from a file and streams the generated JSON request to `curl`, so the private-key contents are not placed in command arguments or printed in the terminal:

```sh
jq -n \
  --arg name "$HOST_NAME" \
  --arg ip "$HOST_IP" \
  --arg username "$SSH_USERNAME" \
  --arg fingerprint "$SSH_HOST_FINGERPRINT" \
  --argjson environmentId "$ENVIRONMENT_ID" \
  --argjson port "$SSH_PORT" \
  --rawfile privateKey "$SSH_KEY_FILE" \
  '{
    environmentId: $environmentId,
    name: $name,
    ip: $ip,
    connection: "SSH",
    port: $port,
    username: $username,
    authMethod: "PRIVATE_KEY",
    secretData: $privateKey,
    expectedFingerprint: $fingerprint,
    labels: {"owner": "platform"}
  }' |
curl --silent --show-error --fail-with-body \
  --request POST "$API_URL/api/v1/hosts" \
  --header "Authorization: Bearer $API_TOKEN" \
  --header 'Content-Type: application/json' \
  --data-binary @- | jq .
```

The example uses `SSH_PORT` in the payload; an omitted port defaults to 22. For password authentication, set `authMethod` to `PASSWORD` and provide the SSH password as `secretData`. For an encrypted private key, include its `passphrase` field as well. Manage those secrets with a secret manager or another protected input mechanism; avoid putting them directly in a shell command.

The registration service attempts an SSH connection, verifies the supplied fingerprint, and authenticates before saving the host. A successful response has HTTP `201` and normally reports SSH status `ACTIVE`. The SSH credential is not returned in the host response.

Common failures:

- `412 Precondition Failed`: the presented host key does not match the supplied fingerprint. Verify the fingerprint independently, then retry with the verified value.
- `401 Unauthorized`: the API JWT is invalid, or the SSH login was rejected. Check the response body to distinguish API authentication from SSH authentication failure.
- `503 Service Unavailable`: the bounded SSH verification capacity is exhausted or unavailable; retry after the service recovers.

## 5. Inspect the registered host through the API

List registered hosts. The response omits SSH passwords and private keys:

```sh
curl --silent --show-error --fail-with-body \
  "$API_URL/api/v1/hosts" \
  --header "Authorization: Bearer $API_TOKEN" | jq .
```

The current API does not implement `GET /api/v1/hosts/{id}`. Use the collection response to find a host by `id` or `name`.

## 6. Configure the CLI and list hosts

The current CLI can list hosts, but it does not create hosts or run remote commands. Configure a context with the API base URL and a `reader` or `operator` API token. Keep the owner JWT for token administration rather than routine CLI use:

```sh
go run ./cmd/machinarchy config set-cluster local --server "$API_URL"
printf '%s' "$API_TOKEN" | go run ./cmd/machinarchy config set-credentials local --token-stdin
go run ./cmd/machinarchy config set-context local --cluster local --user local --env production
go run ./cmd/machinarchy config use-context local
```

List or find the host from the CLI:

```sh
go run ./cmd/machinarchy get hosts
go run ./cmd/machinarchy get hosts "$HOST_NAME"
go run ./cmd/machinarchy get hosts -n production -o json | jq .
```

See [CLI configuration](cli/configuration.md) for named contexts and credential handling.

## 7. Run a command directly with OpenSSH

Until Machinarchy adds a command-submission feature, use the local `ssh` client directly. Before connecting, ensure the host's verified key is in your OpenSSH `known_hosts` file. Keep strict host-key checking enabled:

```sh
ssh -i "$SSH_KEY_FILE" \
  -p "$SSH_PORT" \
  -o StrictHostKeyChecking=yes \
  "$SSH_USERNAME@$HOST_IP" \
  'uname -a'
```

You can replace `uname -a` with a command you are authorized to run. The command executes from your workstation over a separate SSH connection; Machinarchy does not receive, authorize, record, or return its output. Do not disable host-key checking to work around a mismatch.

## What is needed for managed command execution

The repository currently has job read endpoints (`GET /api/v1/jobs`, `GET /api/v1/jobs/{id}`, and `GET /api/v1/hosts/{id}/jobs`) but no job submission route. The CLI has no `exec`, `run`, `apply`, or host creation command. The agent runtime is also a placeholder. An internal SSH helper executes a fixed Linux diagnostics command, but there is no public route wired to it for arbitrary commands.

Managed execution will require an authenticated submission API, an authorization policy for allowed hosts and commands, durable job state, a worker/agent execution path, bounded output and timeouts, audit logging, and a CLI command that submits and watches a job. The existing job read endpoints can then report those persisted results.
