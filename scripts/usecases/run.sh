#!/usr/bin/env bash
# ============================================================================
# MACHINARCHY - END-TO-END API / HOST / CLI / FLEET USE CASE
# ============================================================================
# Safe by default: this script authenticates, inspects current resources and
# previews the selected fleet. It does NOT create resources or execute remote
# commands unless explicitly requested.
#
# Examples:
#   ./scripts/usecases/run.sh
#   ./scripts/usecases/run.sh --apply
#   ./scripts/usecases/run.sh --apply --configure-cli
#   ./scripts/usecases/run.sh --execute --target-hash <64-char-sha256>
#   ./scripts/usecases/run.sh --apply --configure-cli --execute \
#       --target-hash <64-char-sha256>
#
# Configuration is read from the process environment. This script does not
# source .env automatically because sourcing a file executes shell code.
# For a trusted local .env file, run:
#   set -a; . ./.env; set +a
#   ./scripts/usecases/run.sh --apply --configure-cli
# ============================================================================

set -Eeuo pipefail

# ----------------------------------------------------------------------------
# Defaults and configuration
# ----------------------------------------------------------------------------

API_URL="${MACHINARCHY_API_URL:-${API_URL:-http://localhost:8080}}"
API_TOKEN="${MACHINARCHY_API_TOKEN:-${API_TOKEN:-}}"
ADMIN_USERNAME="${MACHINARCHY_BOOTSTRAP_ADMIN_USERNAME:-admin}"
ADMIN_PASSWORD="${MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD:-}"

ENVIRONMENT_NAME="${MACHINARCHY_ENVIRONMENT_NAME:-development}"
ENVIRONMENT_DESCRIPTION="${MACHINARCHY_ENVIRONMENT_DESCRIPTION:-${ENVIRONMENT_NAME} hosts}"

HOST_NAME="${MACHINARCHY_HOST_NAME:-${HOST_NAME:-}}"
HOST_ADDRESS="${MACHINARCHY_HOST_ADDRESS:-${HOST_IP:-}}"
SSH_USERNAME="${MACHINARCHY_SSH_USERNAME:-${SSH_USERNAME:-}}"
SSH_PORT="${MACHINARCHY_SSH_PORT:-${SSH_PORT:-22}}"
SSH_HOST_FINGERPRINT="${MACHINARCHY_SSH_HOST_FINGERPRINT:-}"
HOST_PASSWORD="${MACHINARCHY_HOST_SSH_PASSWORD:-}"

TOKEN_NAME="${MACHINARCHY_AUTOMATION_TOKEN_NAME:-host-inventory-read}"
TOKEN_FILE="${MACHINARCHY_AUTOMATION_TOKEN_FILE:-data/.secrets/host-inventory-read.token}"
TOKEN_ROLE="${MACHINARCHY_AUTOMATION_TOKEN_ROLE:-reader}"
TOKEN_EXPIRY_HOURS="${MACHINARCHY_AUTOMATION_TOKEN_EXPIRY_HOURS:-168}"
AUTOMATION_TOKEN="${MACHINARCHY_AUTOMATION_TOKEN:-}"
CLI_TOKEN=""

# Keep credentials in this process only; do not pass inherited secret
# environment variables to curl, Go tooling or other child processes.
unset MACHINARCHY_API_TOKEN MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD \
    MACHINARCHY_HOST_SSH_PASSWORD MACHINARCHY_AUTOMATION_TOKEN 2>/dev/null || true
export -n API_TOKEN 2>/dev/null || true

RUN_CONCURRENCY="${MACHINARCHY_RUN_CONCURRENCY:-5}"
TARGETS_HASH="${MACHINARCHY_EXPECT_TARGETS_HASH:-${TARGET_HASH:-}}"
SELECTOR="environment=${ENVIRONMENT_NAME}"

API_CONNECT_TIMEOUT="${MACHINARCHY_API_CONNECT_TIMEOUT:-5}"
API_REQUEST_TIMEOUT="${MACHINARCHY_API_REQUEST_TIMEOUT:-30}"

APPLY=0
CONFIGURE_CLI=0
EXECUTE=0

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
TEMP_FILES=()

# ----------------------------------------------------------------------------
# Logging and cleanup
# ----------------------------------------------------------------------------

info()    { printf '→ %s\n' "$1"; }
success() { printf '✓ %s\n' "$1"; }
warn()    { printf '⚠ %s\n' "$1" >&2; }
error()   { printf '✗ %s\n' "$1" >&2; }

on_error() {
    local status=$?
    error "Command failed near line ${BASH_LINENO[0]:-unknown} (exit code ${status})."
    exit "$status"
}

make_temp_file() {
    TEMP_FILE="$(mktemp "${TMPDIR:-/tmp}/machinarchy.XXXXXX")"
    chmod 600 "$TEMP_FILE"
    TEMP_FILES[${#TEMP_FILES[@]}]="$TEMP_FILE"
}

cleanup() {
    local file
    for file in "${TEMP_FILES[@]}"; do
        [[ -n "$file" ]] && rm -f "$file"
    done
    unset ADMIN_PASSWORD HOST_PASSWORD API_TOKEN CLI_TOKEN AUTOMATION_TOKEN
}

trap on_error ERR
trap cleanup EXIT

# ----------------------------------------------------------------------------
# Arguments
# ----------------------------------------------------------------------------

usage() {
    cat <<'USAGE'
Machinarchy end-to-end use-case runner

Usage:
  scripts/usecases/run.sh [options]

Options:
  --apply                  Create missing automation token, environment and host.
  --configure-cli          Configure the local Machinarchy CLI context and list hosts.
  --execute                Submit the sample remote commands after fleet preview.
  --target-hash HASH       Expected fleet-target SHA-256 hash for --execute.
  --all                    Equivalent to --apply --configure-cli --execute.
  --help                   Show this help.

Safe default:
  Without options, the script authenticates, inspects resources and previews
  the selected environment. It does not create resources or run remote commands.

Required environment variables when applicable:
  MACHINARCHY_API_TOKEN                         Existing bootstrap/operator API token
  MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD          Used for login if API token is absent
  MACHINARCHY_HOST_NAME                         Host name to register
  MACHINARCHY_HOST_ADDRESS                      Host IP address or DNS name
  MACHINARCHY_SSH_USERNAME                      SSH username
  MACHINARCHY_SSH_HOST_FINGERPRINT              Trusted SHA256 host-key fingerprint
  MACHINARCHY_HOST_SSH_PASSWORD                 SSH password (prompted if omitted)
  MACHINARCHY_EXPECT_TARGETS_HASH               Expected fleet hash for --execute

Common configuration:
  MACHINARCHY_API_URL                            Default: http://localhost:8080
  MACHINARCHY_ENVIRONMENT_NAME                  Default: development
  MACHINARCHY_AUTOMATION_TOKEN_FILE             Default: data/.secrets/host-inventory-read.token

The API paths and JSON field names in this script follow the supplied example.
Verify them against the current Machinarchy API contract before using this
workflow against a shared or production environment.
USAGE
}

parse_arguments() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --apply)
                APPLY=1
                ;;
            --configure-cli)
                CONFIGURE_CLI=1
                ;;
            --execute)
                EXECUTE=1
                ;;
            --target-hash)
                if [[ $# -lt 2 || -z "$2" ]]; then
                    error "--target-hash requires a value."
                    exit 2
                fi
                TARGETS_HASH="$2"
                shift
                ;;
            --all)
                APPLY=1
                CONFIGURE_CLI=1
                EXECUTE=1
                ;;
            --help|-h)
                usage
                exit 0
                ;;
            *)
                error "Unknown option: $1"
                usage
                exit 2
                ;;
        esac
        shift
    done
}

# ----------------------------------------------------------------------------
# Prerequisites and input validation
# ----------------------------------------------------------------------------

require_command() {
    local command_name="$1"
    if ! command -v "$command_name" >/dev/null 2>&1; then
        error "Required command not found: ${command_name}"
        return 1
    fi
}

check_prerequisites() {
    info "Checking local prerequisites..."
    require_command curl
    require_command jq

    if [[ "$CONFIGURE_CLI" -eq 1 ]]; then
        require_command go
    fi

    if [[ "$APPLY" -eq 1 && -n "$HOST_NAME" ]]; then
        require_command ssh-keyscan
        require_command ssh-keygen
    fi

    if ! curl --help all 2>/dev/null | grep -q -- '--write-out'; then
        error "A curl build with --write-out support is required."
        return 1
    fi

    success "Required tools are available."
}

validate_configuration() {
    while [[ "$API_URL" == */ ]]; do
        API_URL="${API_URL%/}"
    done

    case "$API_URL" in
        http://*|https://*) ;;
        *) error "MACHINARCHY_API_URL must start with http:// or https://"; return 1 ;;
    esac

    if [[ "$API_URL" == http://* ]]; then
        warn "API traffic is using plain HTTP. Use HTTPS when connecting beyond localhost or a trusted isolated development network."
    fi

    if [[ ! "$SSH_PORT" =~ ^[0-9]+$ ]] || (( SSH_PORT < 1 || SSH_PORT > 65535 )); then
        error "SSH port must be an integer between 1 and 65535."
        return 1
    fi

    if [[ ! "$TOKEN_EXPIRY_HOURS" =~ ^[0-9]+$ ]] || (( TOKEN_EXPIRY_HOURS < 1 )); then
        error "Automation token expiry must be a positive integer number of hours."
        return 1
    fi

    if [[ ! "$RUN_CONCURRENCY" =~ ^[0-9]+$ ]] || (( RUN_CONCURRENCY < 1 )); then
        error "Run concurrency must be a positive integer."
        return 1
    fi

    if [[ ! "$API_CONNECT_TIMEOUT" =~ ^[0-9]+$ ]] || (( API_CONNECT_TIMEOUT < 1 )); then
        error "API connect timeout must be a positive integer."
        return 1
    fi

    if [[ ! "$API_REQUEST_TIMEOUT" =~ ^[0-9]+$ ]] || (( API_REQUEST_TIMEOUT < 1 )); then
        error "API request timeout must be a positive integer."
        return 1
    fi

    if [[ "$EXECUTE" -eq 1 && ! "$TARGETS_HASH" =~ ^[a-fA-F0-9]{64}$ ]]; then
        error "--execute requires a 64-character SHA-256 target hash. Preview the fleet first, then pass --target-hash or MACHINARCHY_EXPECT_TARGETS_HASH."
        return 1
    fi

    if [[ -z "$API_TOKEN" && -z "$ADMIN_PASSWORD" && ! -t 0 ]]; then
        error "Set MACHINARCHY_API_TOKEN or MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD when running non-interactively."
        return 1
    fi
}

prompt_secret() {
    local prompt="$1"
    local value=""

    if [[ ! -t 0 ]]; then
        error "Cannot prompt for ${prompt} without an interactive terminal. Supply the corresponding environment variable."
        return 1
    fi

    printf '%s' "$prompt" >&2
    IFS= read -r -s value
    printf '\n' >&2
    printf '%s' "$value"
}

# ----------------------------------------------------------------------------
# HTTP helper
# ----------------------------------------------------------------------------

# Usage: api_request METHOD PATH [BEARER_TOKEN] [JSON_BODY]
# Tokens are written to a mode-600 temporary curl config file rather than
# passed directly in curl's command-line arguments.
api_request() {
    local method="$1"
    local path="$2"
    local bearer_token="${3:-}"
    local json_body="${4:-}"
    local response_file auth_file status detail escaped_token
    local -a curl_args

    make_temp_file
    response_file="$TEMP_FILE"

    curl_args=(
        --silent
        --show-error
        --connect-timeout "$API_CONNECT_TIMEOUT"
        --max-time "$API_REQUEST_TIMEOUT"
        --request "$method"
        --output "$response_file"
        --write-out '%{http_code}'
        --header 'Accept: application/json'
        "$API_URL$path"
    )

    if [[ -n "$bearer_token" ]]; then
        if [[ "$bearer_token" == *$'\n'* || "$bearer_token" == *$'\r'* ]]; then
            error "Bearer token contains an invalid newline character."
            return 1
        fi

        make_temp_file
        auth_file="$TEMP_FILE"
        escaped_token="${bearer_token//\\/\\\\}"
        escaped_token="${escaped_token//\"/\\\"}"
        printf 'header = "Authorization: Bearer %s"\n' "$escaped_token" > "$auth_file"
        curl_args=(--config "$auth_file" "${curl_args[@]}")
    fi

    if [[ -n "$json_body" ]]; then
        status="$(printf '%s' "$json_body" | curl "${curl_args[@]}" --header 'Content-Type: application/json' --data-binary @-)" || {
            error "Network request failed: ${method} ${path}"
            return 1
        }
    else
        status="$(curl "${curl_args[@]}")" || {
            error "Network request failed: ${method} ${path}"
            return 1
        }
    fi

    case "$status" in
        2??)
            cat "$response_file"
            ;;
        *)
            detail="$(jq -r '.message // .error // empty' "$response_file" 2>/dev/null || true)"
            if [[ -n "$detail" ]]; then
                error "API request failed: ${method} ${path} (HTTP ${status}): ${detail}"
            else
                error "API request failed: ${method} ${path} (HTTP ${status})."
            fi
            return 1
            ;;
    esac
}

# ----------------------------------------------------------------------------
# Authentication
# ----------------------------------------------------------------------------

authenticate() {
    if [[ -n "$API_TOKEN" ]]; then
        success "Using the supplied Machinarchy API token."
        return 0
    fi

    if [[ -z "$ADMIN_PASSWORD" ]]; then
        ADMIN_PASSWORD="$(prompt_secret "Bootstrap admin password: ")"
    fi

    make_temp_file
    local password_file="$TEMP_FILE"
    printf '%s' "$ADMIN_PASSWORD" > "$password_file"
    unset ADMIN_PASSWORD MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD

    local login_payload login_response
    login_payload="$(jq -n \
        --arg username "$ADMIN_USERNAME" \
        --rawfile password "$password_file" \
        '{username: $username, password: $password}')"

    rm -f "$password_file"

    info "Authenticating with the Machinarchy API..."
    login_response="$(api_request POST /api/auth/login '' "$login_payload")"
    API_TOKEN="$(printf '%s' "$login_response" | jq -er '.token | strings | select(length > 0)')" || {
        error "Login response did not contain a non-empty .token field."
        return 1
    }

    success "API authentication successful."
}

# ----------------------------------------------------------------------------
# JSON helpers
# ----------------------------------------------------------------------------

array_id_by_name() {
    local json="$1"
    local name="$2"

    printf '%s' "$json" | jq -r --arg name "$name" '
        if type != "array" then
            error("Expected an array response")
        else
            ([.[] | select(.name == $name) | .id | select(. != null)][0] // "")
            | if type == "number" or type == "string" then tostring else "" end
        end
    '
}

response_id() {
    printf '%s' "$1" | jq -er '
        if (.id | type) == "string" or (.id | type) == "number" then
            .id | tostring
        else
            error("Response does not contain a string or numeric .id")
        end
    '
}

# ----------------------------------------------------------------------------
# Automation token management
# ----------------------------------------------------------------------------

write_secret_file() {
    local destination="$1"
    local secret="$2"
    local directory temporary_file

    directory="$(dirname "$destination")"
    (umask 077; mkdir -p "$directory")
    chmod 700 "$directory"

    if [[ -L "$destination" ]]; then
        error "Refusing to write automation token through a symbolic link: $destination"
        return 1
    fi

    temporary_file="$(mktemp "$directory/.machinarchy-token.XXXXXX")"
    chmod 600 "$temporary_file"
    printf '%s' "$secret" > "$temporary_file"
    mv -f "$temporary_file" "$destination"
    chmod 600 "$destination"
}

resolve_automation_token() {
    local tokens_json existing_token_id token_response token_payload

    CLI_TOKEN="$AUTOMATION_TOKEN"

    if [[ -z "$CLI_TOKEN" && -f "$TOKEN_FILE" && ! -L "$TOKEN_FILE" ]]; then
        CLI_TOKEN="$(cat "$TOKEN_FILE")"
        if [[ -z "$CLI_TOKEN" ]]; then
            error "Automation token file is empty: $TOKEN_FILE"
            return 1
        fi
    fi

    if [[ -n "$CLI_TOKEN" ]]; then
        success "Automation token supplied or loaded from the protected local token file."
        return 0
    fi

    if [[ "$APPLY" -ne 1 && "$CONFIGURE_CLI" -ne 1 ]]; then
        return 0
    fi

    info "Checking for automation token '$TOKEN_NAME'..."
    tokens_json="$(api_request GET /api/v1/tokens "$API_TOKEN")"
    existing_token_id="$(array_id_by_name "$tokens_json" "$TOKEN_NAME")"

    if [[ -n "$existing_token_id" ]]; then
        error "Token '$TOKEN_NAME' already exists, but its secret is not available locally."
        error "The API normally returns a token secret only at creation time. Set MACHINARCHY_AUTOMATION_TOKEN or provide its protected token file."
        return 1
    fi

    if [[ "$APPLY" -ne 1 ]]; then
        warn "Automation token '$TOKEN_NAME' does not exist. Use --apply to create it."
        return 0
    fi

    token_payload="$(jq -n \
        --arg name "$TOKEN_NAME" \
        --arg role "$TOKEN_ROLE" \
        --argjson expiresInHours "$TOKEN_EXPIRY_HOURS" \
        '{name: $name, role: $role, expiresInHours: $expiresInHours}')"

    info "Creating a least-privilege automation token..."
    token_response="$(api_request POST /api/v1/tokens "$API_TOKEN" "$token_payload")"
    CLI_TOKEN="$(printf '%s' "$token_response" | jq -er '.token | strings | select(length > 0)')" || {
        error "Token creation response did not contain a non-empty .token field."
        return 1
    }

    write_secret_file "$TOKEN_FILE" "$CLI_TOKEN"
    success "Automation token created and stored in $TOKEN_FILE (permissions 600)."
    # Never print token_response: it contains the one-time token secret.
}

# ----------------------------------------------------------------------------
# Environment management
# ----------------------------------------------------------------------------

ensure_environment() {
    local environments_json create_payload create_response

    info "Looking up environment '$ENVIRONMENT_NAME'..."
    environments_json="$(api_request GET /api/v1/environments "$API_TOKEN")"
    ENVIRONMENT_ID="$(array_id_by_name "$environments_json" "$ENVIRONMENT_NAME")"

    if [[ -n "$ENVIRONMENT_ID" ]]; then
        success "Environment exists: $ENVIRONMENT_NAME (ID: $ENVIRONMENT_ID)"
        return 0
    fi

    if [[ "$APPLY" -ne 1 ]]; then
        warn "Environment '$ENVIRONMENT_NAME' does not exist. Re-run with --apply to create it."
        return 0
    fi

    create_payload="$(jq -n \
        --arg name "$ENVIRONMENT_NAME" \
        --arg description "$ENVIRONMENT_DESCRIPTION" \
        '{name: $name, description: $description}')"

    info "Creating environment '$ENVIRONMENT_NAME'..."
    create_response="$(api_request POST /api/v1/environments "$API_TOKEN" "$create_payload")"
    ENVIRONMENT_ID="$(response_id "$create_response")"

    success "Environment created: $ENVIRONMENT_NAME (ID: $ENVIRONMENT_ID)"
}

# ----------------------------------------------------------------------------
# SSH host-key verification and host registration
# ----------------------------------------------------------------------------

verify_ssh_host_fingerprint() {
    local scan_file

    require_command ssh-keyscan
    require_command ssh-keygen

    if [[ -z "$SSH_HOST_FINGERPRINT" ]]; then
        error "MACHINARCHY_SSH_HOST_FINGERPRINT is required to register an SSH host."
        error "Obtain the fingerprint through a trusted channel; do not trust ssh-keyscan output by itself."
        return 1
    fi

    if [[ ! "$SSH_HOST_FINGERPRINT" =~ ^SHA256:[A-Za-z0-9+/=]+$ ]]; then
        error "SSH host fingerprint must use the SHA256:<fingerprint> format."
        return 1
    fi

    if [[ -z "$HOST_ADDRESS" || "$HOST_ADDRESS" == -* || "$HOST_ADDRESS" == *[!A-Za-z0-9._:-]* ]]; then
        error "Set a valid MACHINARCHY_HOST_ADDRESS (IP address or DNS name)."
        return 1
    fi

    make_temp_file
    scan_file="$TEMP_FILE"

    info "Scanning SSH host key for $HOST_ADDRESS:$SSH_PORT..."
    if ! ssh-keyscan -T 5 -p "$SSH_PORT" "$HOST_ADDRESS" > "$scan_file" 2>/dev/null; then
        error "Unable to retrieve an SSH host key from $HOST_ADDRESS:$SSH_PORT."
        return 1
    fi

    if ! ssh-keygen -lf "$scan_file" -E sha256 2>/dev/null \
        | awk '{print $2}' \
        | grep -Fxq -- "$SSH_HOST_FINGERPRINT"; then
        error "SSH host-key fingerprint does not match the trusted fingerprint."
        error "Expected: $SSH_HOST_FINGERPRINT"
        return 1
    fi

    success "SSH host-key fingerprint verified."
}

ensure_host() {
    local hosts_json create_payload create_response environment_id_json
    local host_password password_file

    info "Fetching registered hosts..."
    hosts_json="$(api_request GET /api/v1/hosts "$API_TOKEN")"

    if [[ -z "$HOST_NAME" ]]; then
        printf '%s\n' "$hosts_json" | jq '
            if type == "array" then
                map(del(.secretData, .password, .privateKey, .token))
            else
                .
            end
        '
        if [[ "$APPLY" -eq 1 ]]; then
            warn "Host registration skipped: set MACHINARCHY_HOST_NAME to register a host."
        else
            info "Set MACHINARCHY_HOST_NAME to check or register a specific host."
        fi
        return 0
    fi

    info "Looking up host '$HOST_NAME'..."
    HOST_ID="$(array_id_by_name "$hosts_json" "$HOST_NAME")"

    if [[ -n "$HOST_ID" ]]; then
        success "Host already exists: $HOST_NAME (ID: $HOST_ID)"
        return 0
    fi

    if [[ "$APPLY" -ne 1 ]]; then
        warn "Host '$HOST_NAME' does not exist. Re-run with --apply to register it."
        return 0
    fi

    if [[ -z "$ENVIRONMENT_ID" ]]; then
        error "Cannot register host because environment '$ENVIRONMENT_NAME' is not available."
        return 1
    fi

    if [[ -z "$HOST_ADDRESS" || -z "$SSH_USERNAME" ]]; then
        error "Set MACHINARCHY_HOST_ADDRESS and MACHINARCHY_SSH_USERNAME before registering a host."
        return 1
    fi

    verify_ssh_host_fingerprint

    host_password="$HOST_PASSWORD"
    if [[ -z "$host_password" ]]; then
        host_password="$(prompt_secret "SSH password for ${SSH_USERNAME}@${HOST_ADDRESS}: ")"
    fi
    if [[ -z "$host_password" ]]; then
        error "SSH password cannot be empty for PASSWORD authentication."
        return 1
    fi

    make_temp_file
    password_file="$TEMP_FILE"
    printf '%s' "$host_password" > "$password_file"
    unset host_password HOST_PASSWORD MACHINARCHY_HOST_SSH_PASSWORD

    # Preserve numeric IDs as JSON numbers and UUID/string IDs as JSON strings.
    environment_id_json="$(jq -n --arg id "$ENVIRONMENT_ID" \
        '$id | if test("^[0-9]+$") then tonumber else . end')"

    create_payload="$(jq -n \
        --arg name "$HOST_NAME" \
        --arg ip "$HOST_ADDRESS" \
        --arg username "$SSH_USERNAME" \
        --arg fingerprint "$SSH_HOST_FINGERPRINT" \
        --argjson environmentId "$environment_id_json" \
        --argjson port "$SSH_PORT" \
        --rawfile secretData "$password_file" \
        '{
            environmentId: $environmentId,
            name: $name,
            ip: $ip,
            connection: "SSH",
            port: $port,
            username: $username,
            authMethod: "PASSWORD",
            secretData: $secretData,
            expectedFingerprint: $fingerprint,
            labels: {owner: "platform"}
        }')"

    rm -f "$password_file"

    info "Registering SSH host '$HOST_NAME'..."
    create_response="$(api_request POST /api/v1/hosts "$API_TOKEN" "$create_payload")"
    HOST_ID="$(response_id "$create_response")"

    success "Host registered: $HOST_NAME (ID: $HOST_ID)"
}

# ----------------------------------------------------------------------------
# CLI configuration
# ----------------------------------------------------------------------------

configure_cli() {
    if [[ "$CONFIGURE_CLI" -ne 1 ]]; then
        return 0
    fi

    if [[ -z "$CLI_TOKEN" ]]; then
        error "No automation token is available for CLI configuration."
        error "Supply MACHINARCHY_AUTOMATION_TOKEN, provide the token file, or run with --apply."
        return 1
    fi

    info "Configuring the local Machinarchy CLI..."

    (
        cd "$PROJECT_ROOT"
        "$GO" run ./cmd/machinarchy config set-cluster local --server "$API_URL"
        printf '%s' "$CLI_TOKEN" | "$GO" run ./cmd/machinarchy config set-credentials local --token-stdin
        "$GO" run ./cmd/machinarchy config set-context local \
            --cluster local --user local --env "$ENVIRONMENT_NAME"
        "$GO" run ./cmd/machinarchy config use-context local

        info "Fetching hosts through the CLI..."
        "$GO" run ./cmd/machinarchy get hosts
        "$GO" run ./cmd/machinarchy get hosts -n "$ENVIRONMENT_NAME" -o json | jq .
    )

    success "Machinarchy CLI context configured."
}

# ----------------------------------------------------------------------------
# Fleet preview and remote runs
# ----------------------------------------------------------------------------

preview_targets() {
    local preview_payload preview_response

    preview_payload="$(jq -n --arg selector "$SELECTOR" '{selector: $selector}')"

    info "Previewing fleet targets for selector: $SELECTOR"
    preview_response="$(api_request POST /api/v1/fleet/targets/preview "$API_TOKEN" "$preview_payload")"
    printf '%s\n' "$preview_response" | jq .
}

run_remote_command() {
    local timeout_seconds="$1"
    shift

    local run_payload run_response run_id run_status

    run_payload="$(jq -n \
        --arg selector "$SELECTOR" \
        --arg expectedTargetsHash "$TARGETS_HASH" \
        --argjson timeoutSeconds "$timeout_seconds" \
        --argjson concurrency "$RUN_CONCURRENCY" \
        --args \
        '{
            argv: $ARGS.positional,
            selector: $selector,
            expectTargetsHash: $expectedTargetsHash,
            timeoutSeconds: $timeoutSeconds,
            concurrency: $concurrency
        }' -- "$@")"

    info "Submitting remote command: $*"
    run_response="$(api_request POST /api/v1/runs "$API_TOKEN" "$run_payload")"
    printf '%s\n' "$run_response" | jq .

    run_id="$(printf '%s' "$run_response" | jq -r 'if .id == null then "" else (.id | tostring) end')"

    if [[ -n "$run_id" && "$run_id" != *[!A-Za-z0-9._~-]* ]]; then
        info "Fetching run details for ID $run_id..."
        run_status="$(api_request GET "/api/v1/runs/$run_id" "$API_TOKEN")"
        printf '%s\n' "$run_status" | jq .
    else
        warn "Run response did not contain a usable .id; skipping run-detail lookup."
    fi
}

execute_sample_runs() {
    if [[ "$EXECUTE" -ne 1 ]]; then
        return 0
    fi

    warn "Remote execution is enabled for selector: $SELECTOR"
    warn "Commands will be sent as argv arrays (not through a local shell)."

    # Read-only diagnostic examples. These still execute on the selected hosts.
    run_remote_command 30 systemctl status nginx
    run_remote_command 10 date
    run_remote_command 10 df -h /

    success "Sample remote commands submitted."
}

# ----------------------------------------------------------------------------
# Main
# ----------------------------------------------------------------------------

main() {
    cd "$PROJECT_ROOT"
    parse_arguments "$@"

    info "============================================================"
    info "Machinarchy end-to-end use case"
    info "Project:     $PROJECT_ROOT"
    info "API:         $API_URL"
    info "Environment: $ENVIRONMENT_NAME"
    info "Mode:        $([[ "$APPLY" -eq 1 ]] && printf 'apply' || printf 'inspect')"
    info "============================================================"

    check_prerequisites
    validate_configuration
    authenticate

    if [[ "$APPLY" -eq 1 || "$CONFIGURE_CLI" -eq 1 ]]; then
        resolve_automation_token
    elif [[ -n "$AUTOMATION_TOKEN" || -f "$TOKEN_FILE" ]]; then
        resolve_automation_token
    fi

    ensure_environment
    ensure_host
    configure_cli
    preview_targets
    execute_sample_runs

    info "============================================================"
    success "Use-case workflow completed."

    if [[ "$APPLY" -ne 1 ]]; then
        info "No resources were created. Use --apply to create missing resources."
    fi
    if [[ "$EXECUTE" -ne 1 ]]; then
        info "No remote commands were executed. Use --execute with a fresh --target-hash to run the sample diagnostics."
    fi
    info "============================================================"
}

main "$@"
