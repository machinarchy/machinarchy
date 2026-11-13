# Machinarchy use cases

This guide demonstrates the currently implemented API and CLI workflows for workspace inventory, managed Hosts, SSH Access Profiles, Operations, runtime configuration, and encrypted secrets. It distinguishes the legacy Host/Run API from the newer managed-resource API because they use different resource models and execution paths.

## Prerequisites and access

- A running Machinarchy API. Local development commonly uses `http://localhost:8080`; use HTTPS outside a trusted local environment.
- A human owner JWT for owner-only member, runtime-configuration, Access Profile, and Secret Store administration. Inventory and Run actions also support authorized operator credentials.
- `curl`, `jq`, and the Machinarchy CLI (`go run ./cmd/machinarchy` from the repository, or an installed binary).
- Network access from the API/Control Manager to managed SSH targets and a trusted way to obtain each target's SSH host-key fingerprint.

The current application uses a single workspace. Signup creates reader membership; the configured bootstrap administrator establishes the initial owner. Workspace-scoped managed resources and Access Profiles are isolated using the authenticated principal's workspace. See the [architecture guide](../architecture.md), [security architecture](../architecture/phase-9-security-architecture.md), and [CLI configuration guide](cli/configuration.md) for deployment and credential details.

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

## 2. Use the CLI to inspect legacy inventory

The current `get` command lists legacy Hosts and environments through the HTTP API. It supports table, JSON, and YAML output. The optional environment filter resolves a name or numeric ID by calling the API for environments.

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
```

The CLI currently has no managed Host or Operation commands. Use the API examples below for those resources. The CLI's `config` command manages local API contexts; it is separate from `runtime-config`, which manages server runtime settings.

## 3. Register a legacy SSH Host

The legacy `POST /api/v1/hosts` endpoint accepts SSH credentials in `secretData`, verifies connectivity and the expected host-key fingerprint before persistence, and never returns the credential. This is distinct from the managed Host and Access Profile flow in the next section.

Create an environment and record its numeric ID:

```sh
ENVIRONMENT_JSON=$(curl --silent --show-error --fail-with-body \
  --request POST "$API_URL/api/v1/environments" \
  --header "Authorization: Bearer $OWNER_TOKEN" \
  --header 'Content-Type: application/json' \
  --data '{"name":"production","description":"Production legacy inventory"}')
printf '%s\n' "$ENVIRONMENT_JSON" | jq .
export ENVIRONMENT_ID=$(printf '%s' "$ENVIRONMENT_JSON" | jq -r '.id')
```

Obtain the fingerprint independently from a trusted console or host administrator. `ssh-keyscan` only observes a key presented on the network; it does not authenticate that key:

```sh
export HOST_IP='192.0.2.10'
export SSH_PORT=22
ssh-keyscan -p "$SSH_PORT" "$HOST_IP" 2>/dev/null | ssh-keygen -lf - -E sha256
export SSH_HOST_FINGERPRINT='SHA256:replace-with-independently-verified-value'
export SSH_USERNAME='deploy'
export SSH_KEY_FILE="$HOME/.ssh/id_ed25519"
```

Create the Host. The JSON request is streamed to curl so the private-key content is not printed or put in a command-line argument:

```sh
jq -n \
  --arg name 'legacy-web-01' \
  --arg ip "$HOST_IP" \
  --arg username "$SSH_USERNAME" \
  --arg fingerprint "$SSH_HOST_FINGERPRINT" \
  --argjson environmentId "$ENVIRONMENT_ID" \
  --argjson port "$SSH_PORT" \
  --rawfile privateKey "$SSH_KEY_FILE" \
  '{environmentId:$environmentId,name:$name,ip:$ip,connection:"SSH",port:$port,
    username:$username,authMethod:"PRIVATE_KEY",secretData:$privateKey,
    expectedFingerprint:$fingerprint,labels:{owner:"platform"}}' |
curl --silent --show-error --fail-with-body \
  --request POST "$API_URL/api/v1/hosts" \
  --header "Authorization: Bearer $OPERATOR_TOKEN" \
  --header 'Content-Type: application/json' \
  --data-binary @- | jq .
```

The implemented payload fields are `environmentId`, `name`, optional `hostname` or `ip`, optional `connection`, `labels`, `port`, `username`, `authMethod` (`PASSWORD` or `PRIVATE_KEY`), `secretData`, optional `passphrase`, and `expectedFingerprint`. Port defaults to 22. Password authentication uses the same `secretData` field. The legacy Host service verifies and persists synchronously; it is not the managed Host desired-state reconciler.

List legacy Hosts and optionally filter with the server's selector syntax:

```sh
curl --silent --show-error --fail-with-body \
  "$API_URL/api/v1/hosts?selector=environment%3Dproduction%2Crole%3Dweb" \
  --header "Authorization: Bearer $OPERATOR_TOKEN" | jq .
```

The legacy Host collection currently returns an array rather than a paginated envelope. `GET /api/v1/hosts/{id}` is not implemented; use the collection to inspect a Host. Host list responses omit credential material.

## 4. Create a managed Host with an Access Profile

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

## 5. Submit and track a managed Operation

Managed Operations are separate from Host desired state and from legacy Runs. Submit an explicit host ID list and argv. The application validates execution authorization and the configured program policy, resolves the current Host/Profile binding, and durably creates the Operation before returning `202 Accepted`.

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

Cancellation is a request; the final state is recorded by the worker. A command may already have started remotely, so cancellation does not prove that it had no effect. If a worker loses control after dispatch, the operation is marked interrupted rather than automatically replayed. Operation output and arguments are sensitive operational data; do not put credentials in argv. The CLI currently does not expose managed Operation submit/watch/cancel commands.

The managed SSH connector accepts program argv without invoking a remote shell, bounds connection/execution concurrency and timeouts, enforces pinned host keys and configured egress policy, and bounds captured output. Do not interpret a successful Operation as proof of a broader machine configuration state; use a Host controller for desired-state convergence.

## 6. Use the legacy Run API for selector-based commands

The older `/api/v1/runs` workflow remains implemented for legacy Hosts. It supports target preview, selector or explicit targets, a target hash precondition, dry-run, asynchronous execution, polling and cancellation. It is separate from managed Operations and does not convert legacy Hosts into managed resources.

Execution is disabled by default. An operator must set these variables in the API process environment before starting (or restarting) the API, and configure allowed executable names/paths:

```sh
export MACHINARCHY_EXECUTION_ENABLED=true
export MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS=uptime,/usr/bin/df,systemctl
```

Preview a target rule:

```sh
curl --silent --show-error --fail-with-body \
  --request POST "$API_URL/api/v1/fleet/targets/preview" \
  --header "Authorization: Bearer $OPERATOR_TOKEN" \
  --header 'Content-Type: application/json' \
  --data '{"selector":"environment=production,role=web"}' | jq .
```

Use the returned `targetsHash` when submitting so a changed target set is rejected for review:

```sh
curl --silent --show-error --fail-with-body \
  --request POST "$API_URL/api/v1/runs" \
  --header "Authorization: Bearer $OPERATOR_TOKEN" \
  --header 'Content-Type: application/json' \
  --data '{"argv":["systemctl","status","nginx"],"selector":"environment=production,role=web","expectTargetsHash":"<copy-preview-hash>","timeoutSeconds":30,"concurrency":5}' | jq .
```

The accepted response is `202` with a Run and `Location: /api/v1/runs/{id}`. `dryRun:true` returns a plan with `200` and does not persist or contact a Host. Poll with `GET /api/v1/runs/{id}` and cancel with `POST /api/v1/runs/{id}/cancel`. Commands are argument arrays, not shell strings; there is no shell expansion. Runs are not automatically retried. The API currently returns combined stdout/stderr with a 64 KiB per-host cap and removes terminal escape sequences. Do not put secrets in arguments: arguments may be visible to authorized operators and on the remote process list.

## 7. Publish and inspect runtime configuration

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

## 8. Rotate and retire an SSH Secret

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

## 9. Remove a managed Host

Managed Host deletion is requested by updating desired presence to `Absent` with the current resource revision and generation. The Control Manager then cancels active Operations targeting the Host and removes the record after they reach terminal states. The current managed HTTP surface exposes `PUT /api/v1/managed/hosts/{id}` for desired-state updates; it does not expose a dedicated `DELETE` route.

Read current metadata first, then submit the required concurrency values and an absent desired state while retaining the access binding fields:

```sh
HOST_STATE=$(curl --silent --show-error --fail-with-body \
  "$API_URL/api/v1/managed/hosts/$MANAGED_HOST_ID" \
  --header "Authorization: Bearer $OPERATOR_TOKEN")
printf '%s\n' "$HOST_STATE" | jq '{metadata,spec,status}'
```

The update request requires the full HostSpec:

```sh
jq -n \
  --arg profileId "$PROFILE_ID" \
  --arg fingerprint "$SSH_HOST_FINGERPRINT" \
  --arg endpoint "$HOST_IP:$SSH_PORT" \
  --argjson revision "$(printf '%s' "$HOST_STATE" | jq -r '.metadata.revision')" \
  --argjson generation "$(printf '%s' "$HOST_STATE" | jq -r '.metadata.generation')" \
  '{revision:$revision,generation:$generation,
    spec:{presence:"Absent",accessProfileId:$profileId,
      accessTarget:{endpoint:$endpoint,trust:[$fingerprint]}}}' |
curl --silent --show-error --fail-with-body \
  --request PUT "$API_URL/api/v1/managed/hosts/$MANAGED_HOST_ID" \
  --header "Authorization: Bearer $OPERATOR_TOKEN" \
  --header 'Content-Type: application/json' \
  --data-binary @-
```

The current handler responds with `204 No Content` when the desired-state update is accepted. A subsequent `GET` may return not found after reconciliation deletes the record. Legacy Host deletion is a separate synchronous `DELETE /api/v1/hosts/{id}` workflow.

## Implemented boundaries and limitations

- The API is the external boundary; the CLI uses HTTP and does not access stores, SSH, controllers, or workers directly.
- Legacy Hosts and Runs remain supported alongside managed Hosts, Access Profiles, and Operations. Their routes and resource semantics are distinct.
- Managed Host reconciliation verifies SSH reachability/authentication and records observed status; it does not apply machine configuration.
- The managed API currently has no list endpoint for managed Hosts, Access Profiles, or Operations, and the CLI has no managed resource/Operation commands.
- The legacy Host list is unpaginated; the legacy Run list uses `before_id` and `limit`, not the target uniform cursor envelope.
- Managed operation execution and cross-replica behavior should be deployed with PostgreSQL for distributed Control Manager instances. SQLite targets a single-node deployment.
- PostgreSQL behavior and external secret-key recovery must be verified for the deployment. See [database testing guidance](../../CONTRIBUTING.md) and [secret key operations](../architecture/phase-6-config-secrets.md).

These examples describe implemented contracts as present in the repository; consult the API's `/openapi.json` and command `--help` output on the deployed version before automating against a different release.
