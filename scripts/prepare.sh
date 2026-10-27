#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
PROJECT_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd -P)
ENV_TEMPLATE="$PROJECT_ROOT/.env.example"
ENV_FILE="$PROJECT_ROOT/.env"
HOOKS_DIR="$PROJECT_ROOT/.githooks"

info() { printf '→ %s\n' "$1"; }
success() { printf '✓ %s\n' "$1"; }
warn() { printf '⚠ %s\n' "$1" >&2; }
error() { printf '✗ %s\n' "$1" >&2; }

check_go() {
	if ! command -v go >/dev/null 2>&1; then
		error 'Go 1.26 or newer is required. Install it from https://go.dev/dl/.'
		return 1
	fi

	local version major minor
	version=$(go env GOVERSION)
	if [[ ! $version =~ ^go([0-9]+)\.([0-9]+) ]]; then
		error "Unable to parse Go version: $version"
		return 1
	fi
	major=${BASH_REMATCH[1]}
	minor=${BASH_REMATCH[2]}
	if (( major < 1 || (major == 1 && minor < 26) )); then
		error "Go 1.26 or newer is required; found $version."
		return 1
	fi
	success "Go $version"
}

setup_env() {
	if [[ -e $ENV_FILE ]]; then
		success '.env already exists; leaving it unchanged'
		return
	fi
	if [[ ! -f $ENV_TEMPLATE ]]; then
		error "Environment template not found: $ENV_TEMPLATE"
		return 1
	fi
	umask 077
	cp "$ENV_TEMPLATE" "$ENV_FILE"
	chmod 600 "$ENV_FILE"
	success 'Created .env from .env.example with owner-only permissions'
	warn 'Set MACHINARCHY_OPERATOR_TOKEN and MACHINARCHY_ENCRYPTION_SECRET before starting a server.'
}

setup_git_hooks() {
	if ! git -C "$PROJECT_ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
		warn 'Not inside a Git worktree; skipping local hook installation'
		return
	fi

	local hook
	for hook in pre-commit commit-msg; do
		if [[ ! -x $HOOKS_DIR/$hook ]]; then
			error "Git hook is missing or not executable: $HOOKS_DIR/$hook"
			return 1
		fi
	done

	local current_hooks_path
	current_hooks_path=$(git -C "$PROJECT_ROOT" config --local --get core.hooksPath || true)
	if [[ -n $current_hooks_path && $current_hooks_path != .githooks ]]; then
		error "Existing core.hooksPath is '$current_hooks_path'; review it before replacing it with .githooks."
		return 1
	fi
	git -C "$PROJECT_ROOT" config --local core.hooksPath .githooks
	success 'Installed repository hooks at .githooks'
}

main() {
	cd "$PROJECT_ROOT"
	info "Preparing Machinarchy in $PROJECT_ROOT"
	check_go
	setup_env
	setup_git_hooks
	success 'Preparation complete. Review .env before running the server.'
}

main "$@"
