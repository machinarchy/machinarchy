
#!/usr/bin/env bash
# ============================================================================
# MACHINARCHY - LOCAL DEVELOPMENT ENVIRONMENT BOOTSTRAPPER
# ============================================================================
# Responsibilities:
#   1. Validate development prerequisites.
#   2. Initialize local environment configuration.
#   3. Prepare local development directories.
#   4. Download and verify Go dependencies.
#   5. Validate Docker Compose availability and configuration.
#   6. Configure repository Git hooks safely.
#   7. Optionally run initial Go verification.
#
# Usage:
#   ./scripts/dev/setup-local.sh
#   ./scripts/dev/setup-local.sh --verify
#   ./scripts/dev/setup-local.sh --skip-modules
#   ./scripts/dev/setup-local.sh --force-hooks
# ============================================================================

set -Eeuo pipefail

# ----------------------------------------------------------------------------
# Configuration
# ----------------------------------------------------------------------------

# Keep synchronized with the minimum supported version in go.mod and CI.
MIN_GO_MAJOR=1
MIN_GO_MINOR=27

FORCE_HOOKS=0
SKIP_MODULES=0
RUN_VERIFY=0

# ----------------------------------------------------------------------------
# Logging
# ----------------------------------------------------------------------------

info() {
    printf '→ %s\n' "$1"
}

success() {
    printf '✓ %s\n' "$1"
}

warn() {
    printf '⚠ %s\n' "$1" >&2
}

error() {
    printf '✗ %s\n' "$1" >&2
}

on_error() {
    local status=$?
    local line="${BASH_LINENO[0]:-unknown}"

    error "Unexpected failure near line ${line} (exit code: ${status})."
    exit "$status"
}

trap on_error ERR

# ----------------------------------------------------------------------------
# Arguments
# ----------------------------------------------------------------------------

usage() {
    cat <<'EOF'
Machinarchy local development setup

Usage:
  ./scripts/dev/setup-local.sh [options]

Options:
  --help           Display this help message.
  --skip-modules   Skip Go dependency download and verification.
  --verify         Run go test ./... and go vet ./... after setup.
  --force-hooks    Explicitly override an existing custom core.hooksPath.

Examples:
  ./scripts/dev/setup-local.sh
  ./scripts/dev/setup-local.sh --verify
  ./scripts/dev/setup-local.sh --skip-modules
EOF
}

parse_arguments() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --help|-h)
                usage
                exit 0
                ;;
            --skip-modules)
                SKIP_MODULES=1
                ;;
            --verify)
                RUN_VERIFY=1
                ;;
            --force-hooks)
                FORCE_HOOKS=1
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
# Project root discovery
# ----------------------------------------------------------------------------

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

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)

PROJECT_ROOT=$(find_project_root "$SCRIPT_DIR") || {
    error "Unable to locate project root (go.mod not found)."
    exit 1
}

ENV_TEMPLATE="$PROJECT_ROOT/.env.example"
ENV_FILE="$PROJECT_ROOT/.env"
HOOKS_DIR="$PROJECT_ROOT/.githooks"
DATA_DIR="$PROJECT_ROOT/data"
COMPOSE_FILE="$PROJECT_ROOT/docker-compose.yml"

# ----------------------------------------------------------------------------
# Prerequisite validation
# ----------------------------------------------------------------------------

require_command() {
    local command_name="$1"

    if ! command -v "$command_name" >/dev/null 2>&1; then
        error "Required command not found: $command_name"
        return 1
    fi
}

check_required_tools() {
    info "Checking required development tools..."

    require_command go
    require_command git

    success "Required command-line tools are available."
}

check_go() {
    info "Validating Go toolchain..."

    local version major minor

    # GOTOOLCHAIN=local prevents automatic toolchain switching from
    # hiding an outdated locally installed Go executable.
    version=$(GOTOOLCHAIN=local go env GOVERSION)

    if [[ ! "$version" =~ ^go([0-9]+)\.([0-9]+)(\.([0-9]+))?$ ]]; then
        error "Unable to parse a stable Go version: $version"
        return 1
    fi

    major="${BASH_REMATCH[1]}"
    minor="${BASH_REMATCH[2]}"

    if (( major < MIN_GO_MAJOR ||
          (major == MIN_GO_MAJOR && minor < MIN_GO_MINOR) )); then

        error "Go ${MIN_GO_MAJOR}.${MIN_GO_MINOR}+ required; found ${version}."
        return 1
    fi

    success "Go toolchain verified: ${version}"
}

# ----------------------------------------------------------------------------
# Environment initialization
# ----------------------------------------------------------------------------

setup_env() {
    info "Checking local environment configuration..."

    if [[ -e "$ENV_FILE" || -L "$ENV_FILE" ]]; then
        if [[ ! -f "$ENV_FILE" ]]; then
            error ".env exists but is not a regular file. Leaving it untouched."
            return 1
        fi

        success ".env already exists. Existing configuration preserved."
        return 0
    fi

    if [[ ! -f "$ENV_TEMPLATE" ]]; then
        error ".env.example was not found."
        return 1
    fi

    local temporary_env

    temporary_env=$(umask 077 && mktemp "$PROJECT_ROOT/.env.tmp.XXXXXX")

    if ! cp "$ENV_TEMPLATE" "$temporary_env"; then
        rm -f "$temporary_env"
        error "Failed to copy .env.example."
        return 1
    fi

    if ! chmod 600 "$temporary_env"; then
        rm -f "$temporary_env"
        error "Failed to secure temporary environment configuration."
        return 1
    fi

    # Protect against replacing a file created during initialization.
    if [[ -e "$ENV_FILE" || -L "$ENV_FILE" ]]; then
        rm -f "$temporary_env"
        success ".env appeared during initialization. Existing file preserved."
        return 0
    fi

    if ! mv "$temporary_env" "$ENV_FILE"; then
        rm -f "$temporary_env"
        error "Failed to initialize .env."
        return 1
    fi

    success "Created .env with owner-only permissions (600)."

    warn "Review MACHINARCHY_OPERATOR_TOKEN and MACHINARCHY_ENCRYPTION_SECRET."
    warn "Set actual development values before starting the application."
}

# ----------------------------------------------------------------------------
# Local development directories
# ----------------------------------------------------------------------------

setup_directories() {
    info "Initializing local development directories..."

    if [[ -e "$DATA_DIR" && ! -d "$DATA_DIR" ]]; then
        error "Expected a directory but found another file type: $DATA_DIR"
        return 1
    fi

    if [[ ! -d "$DATA_DIR" ]]; then
        (
            umask 077
            mkdir -p "$DATA_DIR"
        )

        success "Created local data directory."
    else
        success "Local data directory already exists."
    fi
}

# ----------------------------------------------------------------------------
# Go dependency preparation
# ----------------------------------------------------------------------------

setup_go_modules() {
    if [[ "$SKIP_MODULES" -eq 1 ]]; then
        warn "Skipping Go module preparation (--skip-modules)."
        return 0
    fi

    info "Downloading Go modules..."

    (
        cd "$PROJECT_ROOT"

        go mod download
        go mod verify
    )

    success "Go dependencies downloaded and verified."
}

# ----------------------------------------------------------------------------
# Docker and local infrastructure
# ----------------------------------------------------------------------------

check_docker_compose() {
    info "Checking local infrastructure tooling..."

    if ! command -v docker >/dev/null 2>&1; then
        warn "Docker is not installed. Container-based development is unavailable."
        return 0
    fi

    if ! docker compose version >/dev/null 2>&1; then
        warn "Docker Compose plugin is unavailable."
        return 0
    fi

    success "Docker Compose is available."

    if [[ -f "$COMPOSE_FILE" ]]; then
        if docker compose -f "$COMPOSE_FILE" config --quiet; then
            success "Docker Compose configuration validated."
        else
            warn "Docker Compose configuration validation failed."
        fi
    else
        warn "Root docker-compose.yml was not found."
    fi
}

# ----------------------------------------------------------------------------
# Git hooks
# ----------------------------------------------------------------------------

setup_git_hooks() {
    info "Configuring repository Git hooks..."

    if ! git -C "$PROJECT_ROOT" rev-parse \
        --is-inside-work-tree >/dev/null 2>&1; then

        warn "Not inside a Git working tree. Skipping hook configuration."
        return 0
    fi

    if [[ ! -d "$HOOKS_DIR" ]]; then
        error ".githooks directory is missing."
        return 1
    fi

    local hook

    for hook in pre-commit commit-msg; do
        if [[ ! -f "$HOOKS_DIR/$hook" ]]; then
            error "Required Git hook is missing: $HOOKS_DIR/$hook"
            return 1
        fi

        if [[ ! -x "$HOOKS_DIR/$hook" ]]; then
            error "Git hook is not executable: $hook"
            error "Run: chmod +x .githooks/$hook"
            error "Commit the executable file mode to Git."
            return 1
        fi
    done

    local current_hooks_path

    current_hooks_path=$(
        git -C "$PROJECT_ROOT" config --get core.hooksPath 2>/dev/null || true
    )

    case "$current_hooks_path" in
        ".githooks"|"./.githooks"|"$HOOKS_DIR")
            success "Git hooks are already configured."
            return 0
            ;;
        "")
            ;;
        *)
            if [[ "$FORCE_HOOKS" -ne 1 ]]; then
                warn "Existing core.hooksPath detected: $current_hooks_path"
                warn "Not overriding custom Git hook configuration."
                info "Use --force-hooks to explicitly replace it."
                return 0
            fi

            warn "Explicitly overriding existing Git hook configuration."
            ;;
    esac

    git -C "$PROJECT_ROOT" config --local core.hooksPath .githooks

    success "Repository Git hooks configured."
}

# ----------------------------------------------------------------------------
# Optional development tools
# ----------------------------------------------------------------------------

check_optional_tools() {
    info "Checking optional development tools..."

    local tool

    for tool in make shellcheck golangci-lint; do
        if command -v "$tool" >/dev/null 2>&1; then
            success "$tool is available."
        else
            warn "$tool is not installed (optional)."
        fi
    done
}

# ----------------------------------------------------------------------------
# Optional initial verification
# ----------------------------------------------------------------------------

run_verification() {
    if [[ "$RUN_VERIFY" -ne 1 ]]; then
        return 0
    fi

    info "Running initial Go verification..."

    (
        cd "$PROJECT_ROOT"

        go test ./...
        go vet ./...
    )

    success "Initial Go verification completed successfully."
}

# ----------------------------------------------------------------------------
# Main
# ----------------------------------------------------------------------------

main() {
    parse_arguments "$@"

    cd "$PROJECT_ROOT"

    info "============================================================"
    info "Machinarchy local development initialization"
    info "Project: $PROJECT_ROOT"
    info "============================================================"

    check_required_tools
    check_go

    setup_env
    setup_directories
    setup_go_modules

    check_optional_tools
    check_docker_compose

    setup_git_hooks
    run_verification

    info "============================================================"
    success "Local development environment preparation completed."
    info "============================================================"

    info "Next steps:"
    info "1. Review .env and configure development secrets."
    info "2. Start your required local infrastructure dependencies."
    info "3. Start the Machinarchy application."
}

main "$@"
