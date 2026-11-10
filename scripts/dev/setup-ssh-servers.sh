#!/usr/bin/env bash
# ============================================================================
# MACHINARCHY - LOCAL SSH TEST FIXTURE PREPARATION
# ============================================================================
# Prepares local-only SSH test credentials and LinuxServer OpenSSH config
# snippets. Docker Compose lifecycle is deliberately managed by the Makefile.
#
# Generated under data/.ssh/ (must be gitignored):
#   test_id_ed25519                 Test client private key
#   test_id_ed25519.pub             Test client public key
#   <user>.password                 Password consumed via USER_PASSWORD_FILE
#   config/{password,key,hostcheck}/ Persistent per-container /config trees
# ============================================================================

set -Eeuo pipefail

TEST_USER="${SSH_TEST_USER:-testuser}"
SSH_DIR=""
PASSWORD_FILE=""

info()    { printf '→ %s\n' "$1"; }
success() { printf '✓ %s\n' "$1"; }
warn()    { printf '⚠ %s\n' "$1" >&2; }
error()   { printf '✗ %s\n' "$1" >&2; }

on_error() {
    local status=$?
    local line="${BASH_LINENO[0]:-unknown}"
    error "Setup failed near line ${line} (exit code: ${status})."
    exit "$status"
}
trap on_error ERR

usage() {
    cat <<'USAGE'
Machinarchy local SSH fixture preparation

Usage:
  ./scripts/dev/setup-ssh-servers.sh [options]

Options:
  --help, -h       Show this help.

Environment:
  SSH_TEST_USER       Test account name (default: testuser).
  SSH_TEST_PASSWORD   Test password. If unset, reuse the local password file
                      or prompt securely and confirm it.

This script does not run Docker Compose. Use:
  make ssh-servers-up
  make ssh-servers-ps
  make ssh-servers-logs
  make ssh-servers-down

All generated credentials and persistent SSH host keys live under data/.ssh/.
USAGE
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    usage
    exit 0
elif [[ $# -gt 0 ]]; then
    error "Unknown argument: $1"
    usage
    exit 2
fi

require_command() {
    if ! command -v "$1" >/dev/null 2>&1; then
        error "Required command not found: $1"
        return 1
    fi
}

find_project_root() {
    local directory="$1"

    while :; do
        if [[ -f "$directory/go.mod" ]]; then
            printf '%s\n' "$directory"
            return 0
        fi
        [[ "$directory" == "/" ]] && break
        directory="${directory%/*}"
        [[ -n "$directory" ]] || directory="/"
    done
    return 1
}

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
PROJECT_ROOT=$(find_project_root "$SCRIPT_DIR") || {
    error "Unable to locate repository root (go.mod not found)."
    exit 1
}

DATA_DIR="$PROJECT_ROOT/data"
SSH_DIR="$DATA_DIR/.ssh"
CLIENT_KEY="$SSH_DIR/test_id_ed25519"
CLIENT_PUBLIC_KEY="${CLIENT_KEY}.pub"
PASSWORD_FILE="$SSH_DIR/${TEST_USER}.password"
CONFIG_ROOT="$SSH_DIR/config"

validate_inputs() {
    if [[ ! "$TEST_USER" =~ ^[a-z_][a-z0-9_-]*\$?$ ]]; then
        error "SSH_TEST_USER contains unsupported characters: '$TEST_USER'."
        return 1
    fi

    # Refuse symlinked paths so generated secrets cannot be redirected outside
    # the repository's ignored local-data directory.
    local path
    for path in \
        "$DATA_DIR" "$SSH_DIR" "$CLIENT_KEY" "$CLIENT_PUBLIC_KEY" \
        "$PASSWORD_FILE" "$CONFIG_ROOT" \
        "$CONFIG_ROOT/password" \
        "$CONFIG_ROOT/password/sshd" \
        "$CONFIG_ROOT/password/sshd/sshd_config.d" \
        "$CONFIG_ROOT/password/sshd/sshd_config.d/10-machinarchy-auth.conf" \
        "$CONFIG_ROOT/key" \
        "$CONFIG_ROOT/key/sshd" \
        "$CONFIG_ROOT/key/sshd/sshd_config.d" \
        "$CONFIG_ROOT/key/sshd/sshd_config.d/10-machinarchy-auth.conf" \
        "$CONFIG_ROOT/hostcheck" \
        "$CONFIG_ROOT/hostcheck/sshd" \
        "$CONFIG_ROOT/hostcheck/sshd/sshd_config.d" \
        "$CONFIG_ROOT/hostcheck/sshd/sshd_config.d/10-machinarchy-auth.conf"; do
        if [[ -L "$path" ]]; then
            error "Refusing to write through symlink: $path"
            return 1
        fi
    done
}

prepare_directories() {
    info "Preparing local SSH fixture directories..."

    umask 077
    mkdir -p \
        "$SSH_DIR" \
        "$CONFIG_ROOT/password/sshd/sshd_config.d" \
        "$CONFIG_ROOT/key/sshd/sshd_config.d" \
        "$CONFIG_ROOT/hostcheck/sshd/sshd_config.d"

    chmod 700 "$SSH_DIR" "$CONFIG_ROOT" \
        "$CONFIG_ROOT/password" "$CONFIG_ROOT/key" "$CONFIG_ROOT/hostcheck"

    chmod 755 \
        "$CONFIG_ROOT/password/sshd" \
        "$CONFIG_ROOT/password/sshd/sshd_config.d" \
        "$CONFIG_ROOT/key/sshd" \
        "$CONFIG_ROOT/key/sshd/sshd_config.d" \
        "$CONFIG_ROOT/hostcheck/sshd" \
        "$CONFIG_ROOT/hostcheck/sshd/sshd_config.d"

    success "Local SSH directories prepared."
}

prepare_client_key() {
    info "Preparing Ed25519 test client key..."

    if [[ -e "$CLIENT_KEY" ]]; then
        [[ -f "$CLIENT_KEY" && ! -L "$CLIENT_KEY" ]] || {
            error "Private-key path is not a regular file: $CLIENT_KEY"
            return 1
        }
        if [[ ! -s "$CLIENT_PUBLIC_KEY" ]]; then
            ssh-keygen -y -f "$CLIENT_KEY" > "$CLIENT_PUBLIC_KEY"
        fi
        success "Existing test key preserved."
    else
        [[ ! -e "$CLIENT_PUBLIC_KEY" ]] || {
            error "Public key exists without its private key; refusing to overwrite it."
            return 1
        }
        ssh-keygen -q -t ed25519 -N '' \
            -C 'machinarchy-local-ssh-test' \
            -f "$CLIENT_KEY"
        success "Generated test Ed25519 key pair."
    fi

    chmod 600 "$CLIENT_KEY"
    chmod 644 "$CLIENT_PUBLIC_KEY"
}

write_authentication_policies() {
    info "Writing SSH authentication policy snippets..."

    cat > "$CONFIG_ROOT/password/sshd/sshd_config.d/10-machinarchy-auth.conf" <<'CONF'
# Generated local-test policy: password authentication only.
PubkeyAuthentication no
KbdInteractiveAuthentication no
AuthenticationMethods password
CONF

    cat > "$CONFIG_ROOT/key/sshd/sshd_config.d/10-machinarchy-auth.conf" <<'CONF'
# Generated local-test policy: public-key authentication only.
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AuthenticationMethods publickey
CONF

    cat > "$CONFIG_ROOT/hostcheck/sshd/sshd_config.d/10-machinarchy-auth.conf" <<'CONF'
# Generated local-test policy: password authentication for host-key tests.
PubkeyAuthentication no
KbdInteractiveAuthentication no
AuthenticationMethods password
CONF

    chmod 644 \
        "$CONFIG_ROOT/password/sshd/sshd_config.d/10-machinarchy-auth.conf" \
        "$CONFIG_ROOT/key/sshd/sshd_config.d/10-machinarchy-auth.conf" \
        "$CONFIG_ROOT/hostcheck/sshd/sshd_config.d/10-machinarchy-auth.conf"

    success "Password-only, key-only, and host-check policies prepared."
}

write_password_file() {
    local password="$1"
    local temporary_file

    temporary_file=$(umask 077 && mktemp "$SSH_DIR/.password.XXXXXX")
    if ! printf '%s\n' "$password" > "$temporary_file"; then
        rm -f "$temporary_file"
        error "Unable to write local SSH test password."
        return 1
    fi
    chmod 600 "$temporary_file"
    mv -f "$temporary_file" "$PASSWORD_FILE"
}

prepare_password() {
    info "Preparing local SSH test password..."

    local password="${SSH_TEST_PASSWORD:-}"
    local confirmation=""

    if [[ -z "$password" && -f "$PASSWORD_FILE" ]]; then
        password=$(cat "$PASSWORD_FILE")
        info "Reusing existing local password file."
    fi

    if [[ -z "$password" ]]; then
        if [[ ! -t 0 ]]; then
            error "Set SSH_TEST_PASSWORD or run interactively to create a test password."
            return 1
        fi

        printf 'Password for SSH test user %s (minimum 12 characters): ' "$TEST_USER"
        IFS= read -r -s password
        printf '\nConfirm password: '
        IFS= read -r -s confirmation
        printf '\n'

        if [[ "$password" != "$confirmation" ]]; then
            error "Passwords do not match."
            return 1
        fi
    fi

    if (( ${#password} < 12 )); then
        error "SSH test password must contain at least 12 characters."
        return 1
    fi
    if [[ "$password" == *$'\n'* || "$password" == *$'\r'* ]]; then
        error "SSH test password must be a single line."
        return 1
    fi

    write_password_file "$password"
    unset password confirmation
    success "Local password file prepared with permissions 600."
}

print_summary() {
    cat <<SUMMARY

Machinarchy SSH test fixtures are ready.

Compose services (managed through Makefile):
  ssh-password   Password-only authentication: localhost:2221
  ssh-key        Ed25519-only authentication:   localhost:2222
  ssh-hostcheck  Persistent host key testing:   localhost:2223

Generated files:
  Client private key: $CLIENT_KEY
  Client public key:  $CLIENT_PUBLIC_KEY
  Test password:      $PASSWORD_FILE
  Container configs:  $CONFIG_ROOT/{password,key,hostcheck}

Next commands:
  make ssh-servers-config
  make ssh-servers-up
  make ssh-servers-ps
  make ssh-servers-down

Persistent host keys for ssh-hostcheck are stored in:
  $CONFIG_ROOT/hostcheck/ssh_host_keys

All credentials and keys are local test material. Never reuse them on
production systems and never commit data/.ssh/.
SUMMARY
}

main() {
    info "Machinarchy local SSH fixture preparation"
    info "Repository: $PROJECT_ROOT"

    require_command ssh-keygen
    validate_inputs

    if command -v git >/dev/null 2>&1 && git -C "$PROJECT_ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
        if git -C "$PROJECT_ROOT" check-ignore -q -- data/.ssh/test_id_ed25519; then
            success "data/.ssh/ is ignored by Git."
        else
            warn "data/.ssh/ is not ignored by Git. Add this rule before generating credentials:"
            warn "  /data/.ssh/"
        fi
    fi

    prepare_directories
    prepare_client_key
    write_authentication_policies
    prepare_password
    print_summary
    success "SSH fixture preparation completed. Docker was not started."
}

main "$@"
