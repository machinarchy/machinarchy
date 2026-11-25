# Machinarchy use cases

This guide demonstrates the currently implemented API and CLI workflows for workspace inventory, managed Hosts, SSH Access Profiles, Operations, runtime configuration, and encrypted secrets.

## Prerequisites and access

- A running Machinarchy API. Local development commonly uses `http://localhost:8080`; use HTTPS outside a trusted local environment.
- A human owner JWT for owner-only member, runtime-configuration, Access Profile, and Secret Store administration. Inventory and Run actions also support authorized operator credentials.
- `curl`, `jq`, and the Machinarchy CLI (`go run ./cmd/machinarchy` from the repository, or an installed binary).
- Network access from the API/Control Manager to managed SSH targets and a trusted way to obtain each target's SSH host-key fingerprint.

The current application uses a single workspace. Signup creates reader membership; the configured bootstrap administrator establishes the initial owner. Workspace-scoped managed resources and Access Profiles are isolated using the authenticated principal's workspace. See the [architecture guide](../architecture.md), [security architecture](../archive/phase-9-security-architecture.md), and [CLI configuration guide](../cli/configuration.md) for deployment and credential details.

Set local request variables. Keep real bearer tokens outside source files and shell history:

```sh
export API_URL='http://localhost:8080'
export OWNER_TOKEN='paste-owner-jwt-here'
export OPERATOR_TOKEN='paste-authorized-operator-token-here'
```

## 1. Create a short-lived API token

An owner can create a reader token for inventory queries or an operator token for permitted inventory changes and Run execution. Tokens cannot have the owner role; effective access is constrained by the issuer's workspace membership. The plaintext token is returned at creation and stored server-side as a hash. The default lifetime is 30 days and the maximum is 365 days.

Create a reader token and transfer the returned `token` value directly to your approved credential store:

```sh
curl --silent --show-error --fail-with-body \
  --request POST "$API_URL/api/v1/tokens" \
  --header "Authorization: Bearer $OWNER_TOKEN" \
  --header 'Content-Type: application/json' \
  --data '{"name":"inventory-read","role":"reader","expiresInHours":168}' | jq .
```

`GET /api/v1/tokens` returns token metadata for the caller, and `DELETE /api/v1/tokens/{id}` revokes a token. Do not put bearer tokens in URLs or normal logs.

## 2. Use the CLI

The CLI reads and writes managed Hosts and Operations through `/api/v1/managed`. Output is a table, JSON or YAML. `--env` resolves an environment name or numeric ID.

Configure a named CLI context. `--token-stdin` reads the token without placing it in the command arguments:

```sh
go run ./cmd/machinarchy config set-cluster local --server "$API_URL"
printf '%s' "$OPERATOR_TOKEN" | go run ./cmd/machinarchy config set-credentials local --token-stdin
go run ./cmd/machinarchy config set-context local --cluster local --user local --env production
go run ./cmd/machinarchy config use-context local
```

Then inspect Hosts:

```sh
go run ./cmd/machinarchy get hosts
go run ./cmd/machinarchy get hosts web-01 -o yaml
go run ./cmd/machinarchy get hosts -n production -o json | jq .
go run ./cmd/machinarchy get hosts -l tier=web
go run ./cmd/machinarchy run -l tier=web --wait -- uptime
```

See [the CLI command reference](../cli/commands.md) for host, label, run and operation commands. The API examples below show the same resources over HTTP. The CLI's `config` command manages local API contexts; it is separate from `runtime-config`, which manages server runtime settings.

## 3. Create a managed Host with an Access Profile

Managed Hosts keep endpoint and SSH trust on the Host resource and refer to a workspace-scoped Access Profile. The Profile contains connector/authentication settings and a version-pinned SecretRef; it does not contain plaintext credentials. Profile creation and updates require an owner. Managed Host creation and updates require inventory-write authorization.

First create an encrypted SSH credential. The CLI reads secret values from standard input and the API response contains metadata, not the secret value:

```sh
export SSH_KEY_FILE="$HOME/.ssh/id_ed25519"
SECRET_JSON=$(go run ./cmd/machinarchy secret create managed-web-key \
  --description 'SSH identity for managed web hosts' \
  --value-stdin < "$SSH_KEY_FILE")
printf '%s\n' "$SECRET_JSON" | jq '{id, name, purpose, currentVersion}'
export SECRET_ID=$(printf '%s' "$SECRET_JSON" | jq -r '.id')
export SECRET_VERSION=$(printf '%s' "$SECRET_JSON" | jq -r '.currentVersion')
```

Alternatively, the owner-only Secret API accepts `POST /api/v1/secrets` with `{"name":"managed-web-key","description":"...","purpose":"ssh-credential","value":"..."}`. Prefer the CLI stdin flow or another protected input channel. Secret values are write-only; do not use `GET` expecting plaintext.

Create an Access Profile pinned to that secret version:

```sh
PROFILE_JSON=$(curl --silent --show-error --fail-with-body \
  --request POST "$API_URL/api/v1/managed/access-profiles" \
  --header "Authorization: Bearer $OWNER_TOKEN" \
  --header 'Content-Type: application/json' \
  --data "$(jq -n \
    --arg name 'linux-deploy' \
    --arg secretId "$SECRET_ID" \
    --argjson secretVersion "$SECRET_VERSION" \
    '{name:$name,description:"Pinned SSH profile for Linux deployment hosts",
      spec:{connectorKind:"ssh",username:"deploy",authenticationMethod:"private-key",
        credential:{id:$secretId,version:$secretVersion},enabled:true}}')")
printf '%s\n' "$PROFILE_JSON" | jq .
export PROFILE_ID=$(printf '%s' "$PROFILE_JSON" | jq -r '.id')
export PROFILE_REVISION=$(printf '%s' "$PROFILE_JSON" | jq -r '.currentRevision')
```

Create a managed Host with a network endpoint and the independently verified SHA-256 fingerprint in its trust list:

```sh
HOST_JSON=$(curl --silent --show-error --fail-with-body \
  --request POST "$API_URL/api/v1/managed/hosts" \
  --header "Authorization: Bearer $OPERATOR_TOKEN" \
  --header 'Content-Type: application/json' \
  --data "$(jq -n \
    --arg name 'managed-web-01' \
    --arg profileId "$PROFILE_ID" \
    --arg fingerprint "$SSH_HOST_FINGERPRINT" \
    --arg endpoint "$HOST_IP:$SSH_PORT" \
    '{name:$name,labels:{role:"web",environment:"production"},
      spec:{presence:"Managed",accessProfileId:$profileId,
        accessTarget:{endpoint:$endpoint,trust:[$fingerprint]}}}')")
printf '%s\n' "$HOST_JSON" | jq .
export MANAGED_HOST_ID=$(printf '%s' "$HOST_JSON" | jq -r '.metadata.id')
```

`POST /api/v1/managed/hosts` persists desired state and emits a durable resource-change notification. It does not claim that SSH verification has completed. The Control Manager reconciles the Host, loads the latest resource and current Profile revision, resolves the authorized version-pinned SecretRef, and records observed conditions. Inspect the resource with:

```sh
curl --silent --show-error --fail-with-body \
  "$API_URL/api/v1/managed/hosts/$MANAGED_HOST_ID" \
  --header "Authorization: Bearer $OPERATOR_TOKEN" | jq '{metadata,spec,status}'
```

The reconciler verifies SSH access and updates `status.conditions`/`observedGeneration`; it does not configure the remote machine. It uses the Access Profile's current revision; a Host's optional `profileRevision` field is not used to pin reconciliation to an older revision. An unavailable or disabled Profile, rejected host key, invalid SecretRef, network failure, or SSH authentication failure leaves the resource observable as not ready and eligible for retry. Keep the returned Profile revision and SecretRef version in your change record.

## 4. Submit and track a managed Operation

Managed Operations are separate from Host desired state. Submit an explicit host ID list and argv. The application validates execution authorization and the configured program policy, resolves the current Host/Profile binding, and durably creates the Operation before returning `202 Accepted`.

```sh
OPERATION_JSON=$(curl --silent --show-error --fail-with-body \
  --request POST "$API_URL/api/v1/managed/operations" \
  --header "Authorization: Bearer $OPERATOR_TOKEN" \
  --header 'Content-Type: application/json' \
  --data "$(jq -n --arg hostId "$MANAGED_HOST_ID" \
    '{hostIds:[$hostId],run:{argv:["uname","-a"],timeoutSeconds:30,concurrency:1}}')")
printf '%s\n' "$OPERATION_JSON" | jq '{id,state:.status.state,createdAt}'
export OPERATION_ID=$(printf '%s' "$OPERATION_JSON" | jq -r '.id')
```

Poll for status and bounded target results:

```sh
curl --silent --show-error --fail-with-body \
  "$API_URL/api/v1/managed/operations/$OPERATION_ID" \
  --header "Authorization: Bearer $OPERATOR_TOKEN" | jq .
```

Request cancellation when needed:

```sh
curl --silent --show-error --fail-with-body \
  --request POST "$API_URL/api/v1/managed/operations/$OPERATION_ID/cancel" \
  --header "Authorization: Bearer $OPERATOR_TOKEN" | jq .
```

Cancellation is a request; the final state is recorded by the worker. A command may already have started remotely, so cancellation does not prove that it had no effect. If a worker loses control after dispatch, the operation is marked interrupted rather than automatically replayed. Operation output and arguments are sensitive operational data; do not put credentials in argv. The CLI equivalents are `machinarchy run`, `get operation ID` and `cancel operation ID`.

The managed SSH connector accepts program argv without invoking a remote shell, bounds connection/execution concurrency and timeouts, enforces pinned host keys and configured egress policy, and bounds captured output. Do not interpret a successful Operation as proof of a broader machine configuration state; use a Host controller for desired-state convergence.

## 5. Publish and inspect runtime configuration

Runtime configuration is global, persisted as immutable revisions, and managed separately from bootstrap-only settings. The API supports validation, history, status, compare-and-swap publication and rollback. The CLI commands `runtime-config get|history|status|validate|publish|rollback` communicate through the API.

Read the active configuration and process-application status:

```sh
go run ./cmd/machinarchy runtime-config get -o json
go run ./cmd/machinarchy runtime-config status
go run ./cmd/machinarchy runtime-config history --before 10
```

Prepare a configuration JSON document using the schema returned by `runtime-config get`; validate it before publication. Runtime fields evolve with the typed schema, so do not copy an unverified sample into production:

```sh
go run ./cmd/machinarchy runtime-config validate --file runtime-config.json
go run ./cmd/machinarchy runtime-config publish \
  --expected-revision 12 --change-note 'Adjust managed SSH policy' \
  --file runtime-config.json
```

`--expected-revision` is required and prevents overwriting a concurrent update. Publication may return `201` when applied or `202` with application pending. Processes retain their last known good snapshot if applying a new revision fails; check status for per-process applied revisions and failures. Rollback creates a new revision copied from an earlier revision rather than mutating history:

```sh
go run ./cmd/machinarchy runtime-config rollback \
  --expected-revision 13 --target-revision 12 \
  --change-note 'Restore previous configuration'
```

Classifications indicate whether settings are hot-reloadable, trigger reconciliation, or require restart. Bootstrap settings such as database connection and encryption key provisioning are not changed through runtime configuration.

## 6. Rotate and retire an SSH Secret

List secret metadata and version metadata without retrieving secret plaintext:

```sh
go run ./cmd/machinarchy secret list
go run ./cmd/machinarchy secret versions "$SECRET_ID"
```

Add a new version from protected stdin. The command uses the expected current version for optimistic concurrency:

```sh
export NEW_SSH_KEY_FILE="$HOME/.ssh/id_ed25519_rotation"
ROTATED_SECRET=$(go run ./cmd/machinarchy secret rotate "$SECRET_ID" \
  --expected-version "$SECRET_VERSION" --value-stdin < "$NEW_SSH_KEY_FILE"
)
printf '%s\n' "$ROTATED_SECRET" | jq '{id, currentVersion}'
export NEW_SECRET_VERSION=$(printf '%s' "$ROTATED_SECRET" | jq -r '.currentVersion')
```

Rotation does not rewrite an Access Profile's pinned SecretRef. To adopt the new credential, publish a new Access Profile revision referring to the new secret version; the Host reconciler then checks the latest profile. Profile revision updates create durable notifications for Hosts that reference the Profile.

Update the profile with its current revision as the optimistic concurrency value:

```sh
PROFILE_UPDATE=$(curl --silent --show-error --fail-with-body \
  --request PUT "$API_URL/api/v1/managed/access-profiles/$PROFILE_ID" \
  --header "Authorization: Bearer $OWNER_TOKEN" \
  --header 'Content-Type: application/json' \
  --data "$(jq -n \
    --arg secretId "$SECRET_ID" \
    --argjson expectedRevision "$PROFILE_REVISION" \
    --argjson secretVersion "$NEW_SECRET_VERSION" \
    '{expectedRevision:$expectedRevision,
      spec:{connectorKind:"ssh",username:"deploy",authenticationMethod:"private-key",
        credential:{id:$secretId,version:$secretVersion},enabled:true}}')")
printf '%s\n' "$PROFILE_UPDATE" | jq .
export PROFILE_REVISION=$(printf '%s' "$PROFILE_UPDATE" | jq -r '.revision')
```

The current Profile API exposes create and update, but not list, get, history, or delete routes. An update returns the newly created immutable revision. To disable a Profile, update it with `enabled:false` and the current expected revision; the current revision is then unavailable for new authorized use. `machinarchy secret delete ID` retires the entire Secret, including all its versions. Use it only when no Profile or operation can still require that Secret. Retired ciphertext follows the configured retention behavior. Secret values never appear in normal API responses, events, configuration history, logs, or CLI output.

## 7. Remove a managed Host

`DELETE /api/v1/managed/hosts/{id}?revision=N` deletes a host at its current revision. It is refused with `409` while an Operation targets the host. The CLI asks for confirmation:

```sh
go run ./cmd/machinarchy delete host web-01
curl --silent --show-error --fail-with-body --request DELETE \
  "$API_URL/api/v1/managed/hosts/$MANAGED_HOST_ID?revision=$(printf '%s' "$HOST_STATE" | jq -r '.metadata.revision')" \
  --header "Authorization: Bearer $OPERATOR_TOKEN"
```

Setting `spec.presence` to `Absent` with `PUT` instead lets the Control Manager cancel active Operations first and then remove the record.

## Implemented boundaries and limitations

- The API is the external boundary; the CLI uses HTTP and does not access stores, SSH, controllers, or workers directly.
- Managed Host reconciliation verifies SSH reachability/authentication and records observed status; it does not apply machine configuration.
- Lists use keyset pagination (`{"items", "nextCursor"}`); errors use `{"error", "code"}`.
- Managed operation execution and cross-replica behavior should be deployed with PostgreSQL for distributed Control Manager instances. SQLite targets a single-node deployment.
- PostgreSQL behavior and external secret-key recovery must be verified for the deployment. See [database testing guidance](../../CONTRIBUTING.md) and [secret key operations](../archive/phase-6-config-secrets.md).

These examples describe implemented contracts as present in the repository; consult the API's `/openapi.json` and command `--help` output on the deployed version before automating against a different release.
