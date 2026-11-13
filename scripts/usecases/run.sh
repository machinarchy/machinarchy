#!/usr/bin/env bash
# Run one documented Machinarchy use case against an existing API.
# Safe default: inventory reads only. Mutations require --apply; remote commands
# additionally require --execute. The script never sources .env files.

set -Eeuo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
API_URL="${MACHINARCHY_API_URL:-${API_URL:-http://localhost:8080}}"
BEARER_TOKEN="${MACHINARCHY_API_TOKEN:-${API_TOKEN:-}}"
USE_CASE="inventory"
APPLY=0
EXECUTE=0
ENVIRONMENT_NAME="${MACHINARCHY_ENVIRONMENT_NAME:-development}"
ENVIRONMENT_DESCRIPTION="${MACHINARCHY_ENVIRONMENT_DESCRIPTION:-${ENVIRONMENT_NAME} hosts}"
API_CONNECT_TIMEOUT="${MACHINARCHY_API_CONNECT_TIMEOUT:-5}"
API_REQUEST_TIMEOUT="${MACHINARCHY_API_REQUEST_TIMEOUT:-30}"
POLL_TIMEOUT="${MACHINARCHY_OPERATION_POLL_TIMEOUT:-30}"
POLL_INTERVAL="${MACHINARCHY_OPERATION_POLL_INTERVAL:-2}"
TEMP_DIR=""
AUTH_CONFIG=""

# Do not pass credentials from the environment to curl, jq, Go, or other child
# processes. The bearer token is supplied only through a mode-600 curl config.
unset MACHINARCHY_API_TOKEN API_TOKEN 2>/dev/null || true

info() { printf '→ %s\n' "$*" >&2; }
success() { printf '✓ %s\n' "$*" >&2; }
warn() { printf '⚠ %s\n' "$*" >&2; }
error() { printf '✗ %s\n' "$*" >&2; }

cleanup() {
    local status=$?
    if [[ -n "$TEMP_DIR" && -d "$TEMP_DIR" ]]; then
        rm -rf -- "$TEMP_DIR"
    fi
    unset BEARER_TOKEN
    return "$status"
}
trap cleanup EXIT

on_error() {
    local status=$?
    error "Use case '$USE_CASE' failed near line ${BASH_LINENO[0]:-unknown} (exit ${status})."
    exit "$status"
}
trap on_error ERR

usage() {
    cat <<'USAGE'
Machinarchy documented use-case runner

Usage:
  scripts/usecases/run.sh [--use-case NAME] [--apply] [--execute]

Use cases:
  inventory          Read legacy environments/Hosts through the API (default).
  cli-inventory      Configure a temporary CLI context and list legacy Hosts.
  legacy-host        Inspect or register a legacy SSH Host.
  managed-host       Create an encrypted SSH Secret, Access Profile, and Host.
  managed-operation Submit and track an Operation for a managed Host.
  legacy-run         Preview and optionally execute a selector-based legacy Run.
  runtime-config     Read configuration; optionally validate and publish a file.
  secrets            List secret metadata; optionally create or rotate a secret.
  token              Issue a short-lived reader/operator token and save it safely.

Options:
  --use-case NAME    Select exactly one use case.
  --apply            Allow resource/configuration/secret mutations for the
                     selected use case. Managed-host creates new resources.
  --execute          Submit remote commands (required for managed-operation and
                     legacy-run; still requires --apply).
  --help             Show this help.

Required common environment:
  MACHINARCHY_API_TOKEN       Authorized API bearer credential. Never printed
                              by the runner. Token/Secret/runtime-config
                              administration requires a human owner session.
  MACHINARCHY_API_URL         API base URL (default http://localhost:8080).

See docs/examples/example-usecases.md for payloads, permissions, and workflow
details. This script does not source .env files.
USAGE
}

parse_arguments() {
    while (($#)); do
        case "$1" in
            --use-case)
                (($# >= 2)) || { error '--use-case requires a name'; return 2; }
                USE_CASE="$2"
                shift
                ;;
            --apply) APPLY=1 ;;
            --execute) EXECUTE=1 ;;
            --help|-h) usage; exit 0 ;;
            *) error "Unknown option: $1"; usage; return 2 ;;
        esac
        shift
    done
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || { error "Required command not found: $1"; return 1; }
}

validate_configuration() {
    while [[ "$API_URL" == */ ]]; do API_URL="${API_URL%/}"; done
    [[ "$API_URL" == http://* || "$API_URL" == https://* ]] || {
        error 'MACHINARCHY_API_URL must be an absolute http:// or https:// URL.'
        return 2
    }
    [[ "$API_URL" != *[[:space:]]* ]] || { error 'API URL cannot contain whitespace.'; return 2; }
    [[ "$ENVIRONMENT_NAME" != '' ]] || { error 'MACHINARCHY_ENVIRONMENT_NAME cannot be empty.'; return 2; }
    for value in "$API_CONNECT_TIMEOUT" "$API_REQUEST_TIMEOUT" "$POLL_TIMEOUT" "$POLL_INTERVAL"; do
        [[ "$value" =~ ^[0-9]+$ ]] && ((value > 0)) || {
            error 'API and polling timeout settings must be positive integers.'
            return 2
        }
    done
    case "$USE_CASE" in
        inventory|cli-inventory|legacy-host|managed-host|managed-operation|legacy-run|runtime-config|secrets|token) ;;
        *) error "Unknown use case '$USE_CASE'."; usage; return 2 ;;
    esac
    if [[ "$EXECUTE" -eq 1 && "$APPLY" -ne 1 ]]; then
        error '--execute requires --apply as a separate mutation confirmation.'
        return 2
    fi
    if [[ "$EXECUTE" -eq 1 && "$USE_CASE" != managed-operation && "$USE_CASE" != legacy-run ]]; then
        error '--execute is supported only by managed-operation and legacy-run.'
        return 2
    fi
    if [[ "$USE_CASE" == managed-operation || "$USE_CASE" == legacy-run ]]; then
        if [[ "$EXECUTE" -ne 1 ]]; then
            warn "Use case '$USE_CASE' will only inspect/preview; pass --apply --execute to submit remote commands."
        fi
    fi
    if [[ -z "$BEARER_TOKEN" ]]; then
        error 'Set MACHINARCHY_API_TOKEN to an authorized API bearer token.'
        return 2
    fi
    if [[ "$API_URL" == http://* ]]; then
        case "$API_URL" in
            http://localhost*|http://127.0.0.1*|http://\[::1\]*) ;;
            *) warn 'Bearer token will travel over plain HTTP; use HTTPS outside isolated local development.' ;;
        esac
    fi
}

initialize() {
    require_command curl
    require_command jq
    if [[ "$USE_CASE" == cli-inventory || "$USE_CASE" == runtime-config || "$USE_CASE" == secrets ]]; then
        require_command go
    fi
    TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/machinarchy-usecases.XXXXXX")"
    chmod 700 "$TEMP_DIR"
    AUTH_CONFIG="$TEMP_DIR/curl-auth.conf"
    local escaped="${BEARER_TOKEN//\\/\\\\}"
    escaped="${escaped//\"/\\\"}"
    [[ "$BEARER_TOKEN" != *$'\n'* && "$BEARER_TOKEN" != *$'\r'* ]] || {
        error 'API token contains an invalid newline.'
        return 2
    }
    printf 'header = "Authorization: Bearer %s"\n' "$escaped" > "$AUTH_CONFIG"
    chmod 600 "$AUTH_CONFIG"

    info "Checking API readiness at $API_URL/healthz/ready..."
    if ! curl --silent --show-error --fail --connect-timeout "$API_CONNECT_TIMEOUT" \
        --max-time "$API_REQUEST_TIMEOUT" "$API_URL/healthz/ready" >/dev/null; then
        error "Machinarchy API is unavailable or not ready at $API_URL. Check the URL and API/database health."
        return 1
    fi
}

# Request output is returned on stdout. Error responses are sanitized to status
# and a short public error field; response bodies are stored in the private temp dir.
api_request() {
    local method="$1" path="$2" body_file="${3:-}"
    local response_file status message
    response_file="$TEMP_DIR/response.$RANDOM.$RANDOM"
    local -a args=(--silent --show-error --connect-timeout "$API_CONNECT_TIMEOUT"
        --max-time "$API_REQUEST_TIMEOUT" --config "$AUTH_CONFIG" --request "$method"
        --output "$response_file" --write-out '%{http_code}' --header 'Accept: application/json')
    if [[ -n "$body_file" ]]; then
        [[ -f "$body_file" ]] || { error "Request body file is missing: $body_file"; return 1; }
        args+=(--header 'Content-Type: application/json' --data-binary "@$body_file")
    fi
    if ! status="$(curl "${args[@]}" "$API_URL$path")"; then
        error "Network request failed: $method $path"
        return 1
    fi
    case "$status" in
        2??) cat "$response_file" ;;
        *)
            message="$(jq -r '.message // .error // empty' "$response_file" 2>/dev/null || true)"
            if [[ -n "$message" ]]; then error "API request failed: $method $path (HTTP $status): $message"
            else error "API request failed: $method $path (HTTP $status)."
            fi
            return 1
            ;;
    esac
}

write_json_file() {
    local destination="$1"
    shift
    jq -n "$@" > "$destination"
    chmod 600 "$destination"
}

begin_case() { info "Starting use case: $1"; }
finish_case() { success "Use case succeeded: $1"; }

ensure_environment() {
    local response id body
    response="$(api_request GET /api/v1/environments)"
    id="$(printf '%s' "$response" | jq -er --arg name "$ENVIRONMENT_NAME" \
        '[.[] | select(.name == $name) | .id][0] // empty' 2>/dev/null || true)"
    if [[ -n "$id" ]]; then
        ENVIRONMENT_ID="$id"
        info "Environment '$ENVIRONMENT_NAME' exists (ID $ENVIRONMENT_ID)."
        return 0
    fi
    if [[ "$APPLY" -ne 1 ]]; then
        warn "Environment '$ENVIRONMENT_NAME' does not exist; pass --apply to create it."
        return 1
    fi
    body="$TEMP_DIR/environment.json"
    write_json_file "$body" --arg name "$ENVIRONMENT_NAME" --arg description "$ENVIRONMENT_DESCRIPTION" \
        '{name:$name,description:$description}'
    response="$(api_request POST /api/v1/environments "$body")"
    ENVIRONMENT_ID="$(printf '%s' "$response" | jq -er '.id | tostring')"
    success "Created environment '$ENVIRONMENT_NAME' (ID $ENVIRONMENT_ID)."
}

usecase_inventory() {
    local environments hosts
    begin_case 'API inventory read'
    environments="$(api_request GET /api/v1/environments)"
    hosts="$(api_request GET /api/v1/hosts)"
    hosts="$(api_request GET /api/v1/hosts)"
    managed_hosts="$(api_request GET /api/v1/managed/hosts/8dc195e6-775e-42a4-b1c9-3e5f940b5e53)"
    jq -e 'type == "array"' >/dev/null <<<"$environments" || { error 'Environment API did not return an array.'; return 1; }
    jq -e 'type == "array"' >/dev/null <<<"$hosts" || { error 'Host API did not return an array.'; return 1; }
    # jq -e 'type == "array"' >/dev/null <<<"$managed_hosts" || { error 'Host API did not return an array.'; return 1; }
    jq -n --argjson environments "$environments" --argjson hosts "$hosts" --argjson managed_hosts "$managed_hosts" \
        '{environments:$environments,hosts:$hosts,managed_hosts:$managed_hosts}'
    finish_case 'API inventory read'
}

configure_temporary_cli() {
    local cli_config="$TEMP_DIR/cli-config.yaml"
    (
        cd "$PROJECT_ROOT"
        go run ./cmd/machinarchy --config "$cli_config" config set-cluster local --server "$API_URL" >&2
        printf '%s' "$BEARER_TOKEN" | go run ./cmd/machinarchy --config "$cli_config" config set-credentials local --token-stdin >&2
        go run ./cmd/machinarchy --config "$cli_config" config set-context local \
            --cluster local --user local --env "$ENVIRONMENT_NAME" >&2
        go run ./cmd/machinarchy --config "$cli_config" config use-context local >&2
        go run ./cmd/machinarchy --config "$cli_config" get hosts -o json
    )
}

usecase_cli_inventory() {
    begin_case 'temporary CLI context and inventory list'
    configure_temporary_cli | jq .
    finish_case 'temporary CLI context and inventory list'
}

usecase_legacy_host() {
    local environments hosts existing body response password_file auth_method
    begin_case 'legacy Host inventory and registration'
    environments="$(api_request GET /api/v1/environments)"
    hosts="$(api_request GET /api/v1/hosts)"
    printf '%s\n' "$hosts" | jq .
    if [[ -z "${MACHINARCHY_HOST_NAME:-}" ]]; then
        info 'Set MACHINARCHY_HOST_NAME to select a Host for registration.'
        finish_case 'legacy Host inventory read'
        return 0
    fi
    existing="$(printf '%s' "$hosts" | jq -r --arg name "$MACHINARCHY_HOST_NAME" '[.[] | select(.name == $name)][0].id // empty')"
    if [[ -n "$existing" ]]; then
        info "Legacy Host '$MACHINARCHY_HOST_NAME' already exists (ID $existing); no change made."
        finish_case 'legacy Host inventory and registration'
        return 0
    fi
    [[ "$APPLY" -eq 1 ]] || { warn 'Host is absent; pass --apply to register it.'; return 1; }
    [[ -n "${MACHINARCHY_HOST_ADDRESS:-}" && -n "${MACHINARCHY_SSH_USERNAME:-}" ]] || {
        error 'Set MACHINARCHY_HOST_ADDRESS and MACHINARCHY_SSH_USERNAME to register a legacy Host.'; return 2;
    }
    [[ "${MACHINARCHY_SSH_HOST_FINGERPRINT:-}" =~ ^SHA256:[A-Za-z0-9+/=]+$ ]] || {
        error 'Set MACHINARCHY_SSH_HOST_FINGERPRINT to a trusted SHA256 fingerprint.'; return 2;
    }
    ensure_environment
    password_file=""
    if [[ -n "${MACHINARCHY_HOST_SSH_KEY_FILE:-}" ]]; then
        [[ -r "$MACHINARCHY_HOST_SSH_KEY_FILE" ]] || { error 'SSH key file is unreadable.'; return 2; }
        password_file="$MACHINARCHY_HOST_SSH_KEY_FILE"
        auth_method=PRIVATE_KEY
    elif [[ -n "${MACHINARCHY_HOST_SSH_PASSWORD:-}" ]]; then
        password_file="$TEMP_DIR/ssh-password"
        printf '%s' "$MACHINARCHY_HOST_SSH_PASSWORD" > "$password_file"
        chmod 600 "$password_file"
        unset MACHINARCHY_HOST_SSH_PASSWORD
        auth_method=PASSWORD
    else
        error 'Set MACHINARCHY_HOST_SSH_KEY_FILE or MACHINARCHY_HOST_SSH_PASSWORD.'; return 2
    fi
    body="$TEMP_DIR/legacy-host.json"
    write_json_file "$body" --arg name "$MACHINARCHY_HOST_NAME" \
        --arg ip "$MACHINARCHY_HOST_ADDRESS" --arg username "$MACHINARCHY_SSH_USERNAME" \
        --arg fingerprint "$MACHINARCHY_SSH_HOST_FINGERPRINT" --arg method "$auth_method" \
        --argjson env "$ENVIRONMENT_ID" --argjson port "${MACHINARCHY_SSH_PORT:-22}" \
        --rawfile secretData "$password_file" \
        '{environmentId:$env,name:$name,ip:$ip,connection:"SSH",port:$port,
          username:$username,authMethod:$method,secretData:$secretData,
          expectedFingerprint:$fingerprint,labels:{owner:"platform"}}'
    response="$(api_request POST /api/v1/hosts "$body")"
    printf '%s\n' "$response" | jq '{id,name,ip_address,connection,ssh}'
    finish_case 'legacy Host registration'
}

usecase_managed_host() {
    local name address username port fingerprint auth key_file secret_file auth_method
    local secret_body secret_response profile_body profile_response host_body host_response
    begin_case 'managed Host, Secret, and Access Profile creation'
    [[ "$APPLY" -eq 1 ]] || { error 'Managed Host creation mutates state; pass --apply.'; return 2; }
    name="${MACHINARCHY_HOST_NAME:-}"
    address="${MACHINARCHY_HOST_ADDRESS:-}"
    username="${MACHINARCHY_SSH_USERNAME:-}"
    port="${MACHINARCHY_SSH_PORT:-22}"
    fingerprint="${MACHINARCHY_SSH_HOST_FINGERPRINT:-}"
    key_file="${MACHINARCHY_HOST_SSH_KEY_FILE:-}"
    auth="${MACHINARCHY_SSH_AUTH_METHOD:-private-key}"
    [[ -n "$name" && -n "$address" && -n "$username" ]] || {
        error 'Set MACHINARCHY_HOST_NAME, MACHINARCHY_HOST_ADDRESS, and MACHINARCHY_SSH_USERNAME.'; return 2;
    }
    [[ "$port" =~ ^[0-9]+$ ]] && ((port > 0 && port < 65536)) || { error 'Invalid SSH port.'; return 2; }
    [[ "$fingerprint" =~ ^SHA256:[A-Za-z0-9+/=]+$ ]] || { error 'Set a trusted SHA256 SSH fingerprint.'; return 2; }
    [[ "$auth" == private-key || "$auth" == password ]] || { error 'MACHINARCHY_SSH_AUTH_METHOD must be private-key or password.'; return 2; }
    if [[ "$auth" == private-key ]]; then
        [[ -n "$key_file" && -r "$key_file" ]] || { error 'Set readable MACHINARCHY_HOST_SSH_KEY_FILE.'; return 2; }
        secret_file="$key_file"
    else
        [[ -n "${MACHINARCHY_HOST_SSH_PASSWORD:-}" ]] || { error 'Set MACHINARCHY_HOST_SSH_PASSWORD.'; return 2; }
        secret_file="$TEMP_DIR/ssh-password"
        printf '%s' "$MACHINARCHY_HOST_SSH_PASSWORD" > "$secret_file"
        chmod 600 "$secret_file"
        unset MACHINARCHY_HOST_SSH_PASSWORD
    fi
    secret_body="$TEMP_DIR/secret.json"
    write_json_file "$secret_body" --arg name "${MACHINARCHY_SECRET_NAME:-managed-host-ssh}" \
        --arg purpose ssh-credential --rawfile value "$secret_file" \
        '{name:$name,purpose:$purpose,value:$value}'
    secret_response="$(api_request POST /api/v1/secrets "$secret_body")"
    local secret_id secret_version profile_name
    secret_id="$(printf '%s' "$secret_response" | jq -er '.id')"
    secret_version="$(printf '%s' "$secret_response" | jq -er '.currentVersion')"
    profile_name="${MACHINARCHY_ACCESS_PROFILE_NAME:-${name}-ssh}"
    auth_method=private-key
    [[ "$auth" == password ]] && auth_method=password
    profile_body="$TEMP_DIR/profile.json"
    write_json_file "$profile_body" --arg name "$profile_name" --arg username "$username" \
        --arg method "$auth_method" --arg secretId "$secret_id" --argjson version "$secret_version" \
        '{name:$name,spec:{connectorKind:"ssh",username:$username,authenticationMethod:$method,
          credential:{id:$secretId,version:$version},enabled:true}}'
    profile_response="$(api_request POST /api/v1/managed/access-profiles "$profile_body")"
    local profile_id profile_revision
    profile_id="$(printf '%s' "$profile_response" | jq -er '.id')"
    profile_revision="$(printf '%s' "$profile_response" | jq -er '.currentRevision')"
    host_body="$TEMP_DIR/managed-host.json"
    write_json_file "$host_body" --arg name "$name" --arg profileId "$profile_id" \
        --arg endpoint "$address:$port" --arg fingerprint "$fingerprint" \
        '{name:$name,labels:{owner:"platform"},spec:{presence:"Managed",accessProfileId:$profileId,
          accessTarget:{endpoint:$endpoint,trust:[$fingerprint]}}}'
    host_response="$(api_request POST /api/v1/managed/hosts "$host_body")"
    printf '%s\n' "$host_response" | jq '{metadata,spec,status}'
    info "Created Secret $secret_id version $secret_version; Access Profile $profile_id revision $profile_revision."
    warn 'Managed Host and Access Profile create routes have no list/get-or-create workflow; rerunning --apply creates additional resources.'
    finish_case 'managed Host, Secret, and Access Profile creation'
}

usecase_managed_operation() {
    local host_id argv_json body response operation_id result state elapsed
    begin_case 'managed Operation submission and tracking'
    host_id="${MACHINARCHY_MANAGED_HOST_ID:-}"
    [[ -n "$host_id" ]] || { error 'Set MACHINARCHY_MANAGED_HOST_ID.'; return 2; }
    if [[ "$EXECUTE" -ne 1 ]]; then
        info "Ready to submit a managed Operation for Host $host_id; remote execution was not requested."
        finish_case 'managed Operation preflight'
        return 0
    fi
    argv_json="${MACHINARCHY_OPERATION_ARGV_JSON:-[\"uname\",\"-a\"]}"
    jq -e 'type == "array" and length > 0 and all(.[]; type == "string")' <<<"$argv_json" >/dev/null || {
        error 'MACHINARCHY_OPERATION_ARGV_JSON must be a non-empty JSON string array.'; return 2;
    }
    body="$TEMP_DIR/operation.json"
    write_json_file "$body" --arg hostId "$host_id" --argjson argv "$argv_json" \
        --argjson timeout "${MACHINARCHY_OPERATION_TIMEOUT_SECONDS:-30}" \
        --argjson concurrency "${MACHINARCHY_OPERATION_CONCURRENCY:-1}" \
        '{hostIds:[$hostId],run:{argv:$argv,timeoutSeconds:$timeout,concurrency:$concurrency}}'
    response="$(api_request POST /api/v1/managed/operations "$body")"
    operation_id="$(printf '%s' "$response" | jq -er '.id')"
    printf '%s\n' "$response" | jq '{id,status,createdAt}'
    info "Operation $operation_id accepted; polling for up to ${POLL_TIMEOUT}s."
    elapsed=0
    while ((elapsed < POLL_TIMEOUT)); do
        result="$(api_request GET "/api/v1/managed/operations/$operation_id")"
        state="$(printf '%s' "$result" | jq -r '.status.state // empty')"
        case "$state" in
            Succeeded|PartiallySucceeded|Failed|Cancelled|TimedOut|Interrupted)
                printf '%s\n' "$result" | jq .
                [[ "$state" == Succeeded ]] || { error "Operation reached terminal state $state (ID $operation_id)."; return 1; }
                finish_case "managed Operation $operation_id"
                return 0
                ;;
        esac
        sleep "$POLL_INTERVAL"
        elapsed=$((elapsed + POLL_INTERVAL))
    done
    warn "Operation remains $state after ${POLL_TIMEOUT}s; it is durable and can be queried at /api/v1/managed/operations/$operation_id."
    finish_case "managed Operation accepted (ID $operation_id)"
}

usecase_legacy_run() {
    local selector preview_body preview preview_hash expected run_body run_response run_id
    begin_case 'legacy Run preview and optional execution'
    selector="environment=${ENVIRONMENT_NAME}"
    preview_body="$TEMP_DIR/preview.json"
    write_json_file "$preview_body" --arg selector "$selector" '{selector:$selector}'
    preview="$(api_request POST /api/v1/fleet/targets/preview "$preview_body")"
    printf '%s\n' "$preview" | jq .
    preview_hash="$(printf '%s' "$preview" | jq -er '.targetsHash')"
    [[ "$EXECUTE" -eq 1 ]] || { finish_case 'legacy Run target preview'; return 0; }
    expected="${MACHINARCHY_EXPECT_TARGETS_HASH:-}"
    [[ "$expected" =~ ^[a-fA-F0-9]{64}$ ]] || { error 'Set MACHINARCHY_EXPECT_TARGETS_HASH to the reviewed preview hash before execution.'; return 2; }
    [[ "$expected" == "$preview_hash" ]] || {
        error 'Fresh target preview differs from MACHINARCHY_EXPECT_TARGETS_HASH; review the new target set and retry.'; return 1;
    }
    local argv_json
    argv_json="${MACHINARCHY_RUN_ARGV_JSON:-[\"uptime\"]}"
    jq -e 'type == "array" and length > 0 and all(.[]; type == "string")' <<<"$argv_json" >/dev/null || {
        error 'MACHINARCHY_RUN_ARGV_JSON must be a non-empty JSON string array.'; return 2;
    }
    run_body="$TEMP_DIR/run.json"
    write_json_file "$run_body" --arg selector "$selector" --arg hash "$expected" \
        --argjson argv "$argv_json" --argjson timeout "${MACHINARCHY_RUN_TIMEOUT_SECONDS:-30}" \
        --argjson concurrency "${MACHINARCHY_RUN_CONCURRENCY:-5}" \
        '{selector:$selector,expectTargetsHash:$hash,argv:$argv,timeoutSeconds:$timeout,concurrency:$concurrency}'
    run_response="$(api_request POST /api/v1/runs "$run_body")"
    run_id="$(printf '%s' "$run_response" | jq -er '.id | tostring')"
    printf '%s\n' "$run_response" | jq .
    info "Run $run_id accepted; checking current result."
    api_request GET "/api/v1/runs/$run_id" | jq .
    finish_case "legacy Run $run_id accepted"
}

usecase_runtime_config() {
    local config_file="${MACHINARCHY_RUNTIME_CONFIG_FILE:-}" expected="${MACHINARCHY_EXPECTED_CONFIG_REVISION:-}"
    begin_case 'runtime configuration inspection and optional publication'
    configure_temporary_cli >/dev/null
    local cli_config="$TEMP_DIR/cli-config.yaml"
    (
        cd "$PROJECT_ROOT"
        go run ./cmd/machinarchy --config "$cli_config" runtime-config get -o json
        go run ./cmd/machinarchy --config "$cli_config" runtime-config status
        go run ./cmd/machinarchy --config "$cli_config" runtime-config history
    )
    if [[ -n "$config_file" ]]; then
        [[ -r "$config_file" ]] || { error 'MACHINARCHY_RUNTIME_CONFIG_FILE is not readable.'; return 2; }
        (
            cd "$PROJECT_ROOT"
            go run ./cmd/machinarchy --config "$cli_config" runtime-config validate --file "$config_file"
        )
        if [[ "$APPLY" -eq 1 ]]; then
            [[ "$expected" =~ ^[0-9]+$ ]] && ((expected > 0)) || {
                error 'Set MACHINARCHY_EXPECTED_CONFIG_REVISION to publish safely.'; return 2;
            }
            (
                cd "$PROJECT_ROOT"
                go run ./cmd/machinarchy --config "$cli_config" runtime-config publish \
                    --expected-revision "$expected" --file "$config_file" \
                    --change-note "Published by use-case runner"
            )
        fi
    elif [[ "$APPLY" -eq 1 ]]; then
        local rollback_target="${MACHINARCHY_ROLLBACK_TARGET_REVISION:-}"
        [[ "$expected" =~ ^[0-9]+$ && "$rollback_target" =~ ^[0-9]+$ ]] && ((expected > 0 && rollback_target > 0)) || {
            error 'For --apply without a config file, set MACHINARCHY_EXPECTED_CONFIG_REVISION and MACHINARCHY_ROLLBACK_TARGET_REVISION to roll back.'
            return 2
        }
        (
            cd "$PROJECT_ROOT"
            go run ./cmd/machinarchy --config "$cli_config" runtime-config rollback \
                --expected-revision "$expected" --target-revision "$rollback_target" \
                --change-note "Rollback by use-case runner"
        )
    fi
    finish_case 'runtime configuration inspection'
}

usecase_secrets() {
    local cli_config="$TEMP_DIR/cli-config.yaml" secret_id="${MACHINARCHY_SECRET_ID:-}"
    local expected_version="${MACHINARCHY_EXPECTED_SECRET_VERSION:-}"
    begin_case 'Secret Store metadata and optional mutation'
    configure_temporary_cli >/dev/null
    (
        cd "$PROJECT_ROOT"
        go run ./cmd/machinarchy --config "$cli_config" secret list
        if [[ -n "$secret_id" ]]; then
            go run ./cmd/machinarchy --config "$cli_config" secret versions "$secret_id"
        fi
        if [[ "$APPLY" -eq 1 ]]; then
            if [[ -n "${MACHINARCHY_SECRET_VALUE_FILE:-}" ]]; then
                [[ -r "$MACHINARCHY_SECRET_VALUE_FILE" ]] || { error 'Secret value file is unreadable.'; exit 2; }
                if [[ -n "$secret_id" ]]; then
                    [[ "$expected_version" =~ ^[0-9]+$ ]] && ((expected_version > 0)) || {
                        error 'Set MACHINARCHY_EXPECTED_SECRET_VERSION to rotate an existing secret.'; exit 2;
                    }
                    go run ./cmd/machinarchy --config "$cli_config" secret rotate "$secret_id" \
                        --expected-version "$expected_version" --value-stdin \
                        < "$MACHINARCHY_SECRET_VALUE_FILE"
                else
                    go run ./cmd/machinarchy --config "$cli_config" secret create \
                        "${MACHINARCHY_SECRET_NAME:-usecase-secret}" --value-stdin \
                        < "$MACHINARCHY_SECRET_VALUE_FILE"
                fi
            else
                if [[ "${MACHINARCHY_RETIRE_SECRET:-false}" == true && -n "$secret_id" ]]; then
                    go run ./cmd/machinarchy --config "$cli_config" secret delete "$secret_id"
                else
                    error 'For --apply, set MACHINARCHY_SECRET_VALUE_FILE to create, or SECRET_ID + EXPECTED_SECRET_VERSION + VALUE_FILE to rotate; set MACHINARCHY_RETIRE_SECRET=true to retire a secret.'
                    exit 2
                fi
            fi
        fi
    )
    finish_case 'Secret Store metadata and optional creation'
}

usecase_token() {
    local name role expiry body response token destination directory temporary
    begin_case 'one-time API token issuance'
    [[ "$APPLY" -eq 1 ]] || { error 'Token issuance mutates access state; pass --apply.'; return 2; }
    name="${MACHINARCHY_AUTOMATION_TOKEN_NAME:-usecase-reader}"
    role="${MACHINARCHY_AUTOMATION_TOKEN_ROLE:-reader}"
    expiry="${MACHINARCHY_AUTOMATION_TOKEN_EXPIRY_HOURS:-168}"
    [[ "$role" == reader || "$role" == operator ]] || { error 'Token role must be reader or operator.'; return 2; }
    [[ "$expiry" =~ ^[0-9]+$ ]] && ((expiry > 0 && expiry <= 8760)) || {
        error 'Token lifetime must be between 1 and 8760 hours.'; return 2;
    }
    destination="${MACHINARCHY_AUTOMATION_TOKEN_FILE:-$HOME/.config/machinarchy/usecase-token}"
    directory="$(dirname "$destination")"
    mkdir -p "$directory"
    chmod 700 "$directory"
    [[ ! -L "$destination" && ! -e "$destination" ]] || {
        error "Refusing to overwrite an existing or symlink token file: $destination"; return 2;
    }
    body="$TEMP_DIR/token.json"
    write_json_file "$body" --arg name "$name" --arg role "$role" --argjson expires "$expiry" \
        '{name:$name,role:$role,expiresInHours:$expires}'
    response="$(api_request POST /api/v1/tokens "$body")"
    token="$(printf '%s' "$response" | jq -er '.token | strings | select(length > 0)')"
    temporary="$(mktemp "$directory/.machinarchy-token.XXXXXX")"
    chmod 600 "$temporary"
    printf '%s' "$token" > "$temporary"
    mv -- "$temporary" "$destination"
    chmod 600 "$destination"
    unset token response
    success "Issued $role token '$name' and saved it to $destination (mode 600); the token value was not printed."
    finish_case 'one-time API token issuance'
}

main() {
    parse_arguments "$@"
    require_command curl
    require_command jq
    [[ "$APPLY" -ne 1 || "$USE_CASE" != inventory && "$USE_CASE" != cli-inventory ]] || {
        error "--apply has no mutation for use case '$USE_CASE'."; return 2;
    }
    [[ "$EXECUTE" -ne 1 || "$APPLY" -eq 1 ]] || { error '--execute requires --apply.'; return 2; }
    validate_configuration
    case "$USE_CASE" in
        cli-inventory|runtime-config|secrets) require_command go ;;
    esac
    initialize
    info "Machinarchy use-case runner (project $PROJECT_ROOT)"
    info "API: $API_URL | environment: $ENVIRONMENT_NAME"
    case "$USE_CASE" in
        inventory) usecase_inventory ;;
        cli-inventory) usecase_cli_inventory ;;
        legacy-host) usecase_legacy_host ;;
        managed-host) usecase_managed_host ;;
        managed-operation) usecase_managed_operation ;;
        legacy-run) usecase_legacy_run ;;
        runtime-config) usecase_runtime_config ;;
        secrets) usecase_secrets ;;
        token) usecase_token ;;
    esac
}

main "$@"
