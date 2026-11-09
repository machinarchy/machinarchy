#!/usr/bin/env bash
# =========================================================================
# HARDENED REPOSITORY PREPARATION AND ENVIRONMENTAL CONFIGURATOR
# =========================================================================
set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
PROJECT_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd -P)
ENV_TEMPLATE="$PROJECT_ROOT/.env.example"
ENV_FILE="$PROJECT_ROOT/.env"
HOOKS_DIR="$PROJECT_ROOT/.githooks"

# Minimum system engine constraints
MIN_GO_MAJOR=1
MIN_GO_MINOR=26

info() { printf '→ %s\n' "$1"; }
success() { printf '✓ %s\n' "$1"; }
warn() { printf '⚠ %s\n' "$1" >&2; }
error() { printf '✗ %s\n' "$1" >&2; }

check_go() {
	if ! command -v go >/dev/null 2>&1; then
		error "Go ${MIN_GO_MAJOR}.${MIN_GO_MINOR} or newer is required. Install it from https://go.dev."
		return 1
	fi

	local version major minor
	version=$(go env GOVERSION)
	
	# SENIOR FIX: Strip alphanumeric pre-release metadata (rc1, beta1, etc.) 
	# to prevent structural parsing breakdowns on development runtimes.
	local clean_version
	clean_version=$(echo "$version" | sed -E 's/^go([0-9]+\.[0-9]+).*/go\1/')

	if [[ ! $clean_version =~ ^go([0-9]+)\.([0-9]+) ]]; then
		error "Unable to safely extract semantic digits from Go runtime footprint: $version"
		return 1
	fi

	major=${BASH_REMATCH[1]}
	minor=${BASH_REMATCH[2]}

	if (( major < MIN_GO_MAJOR || (major == MIN_GO_MAJOR && minor < MIN_GO_MINOR) )); then
		error "Go ${MIN_GO_MAJOR}.${MIN_GO_MINOR} or newer is required; discovered outdated runtime: $version."
		return 1
	fi
	success "Go runtime verified: $version"
}

setup_env() {
	if [[ -e $ENV_FILE ]]; then
		success '.env configuration already initialized; skipping template cloning loop.'
		return
	fi
	if [[ ! -f $ENV_TEMPLATE ]]; then
		error "Root system environmental template not found: $ENV_TEMPLATE"
		return 1
	fi
	umask 077
	cp "$ENV_TEMPLATE" "$ENV_FILE"
	chmod 600 "$ENV_FILE"
	success 'Instantiated local secure .env baseline from .env.example (permissions limited to owner-only).'
	warn 'Mandatory task: Set MACHINARCHY_OPERATOR_TOKEN and MACHINARCHY_ENCRYPTION_SECRET inside .env before launching daemons.'
}

setup_git_hooks() {
	if ! git -C "$PROJECT_ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
		warn 'Execution workspace context resides outside an active Git tree; bypassing hook mapping.'
		return
	fi

	local hook
	for hook in pre-commit commit-msg; do
		if [[ ! -f $HOOKS_DIR/$hook ]]; then
			error "Required infrastructure Git hook missing from storage array directory: $HOOKS_DIR/$hook"
			return 1
		fi
		# Senior Optimization: Enforce execution permissions automatically rather than hard failing
		if [[ ! -x $HOOKS_DIR/$hook ]]; then
			warn "Hook exists but lacks execution bit. Overriding file system permissions..."
			chmod +x "$HOOKS_DIR/$hook"
		fi
	done

	local current_hooks_path
	current_hooks_path=$(git -C "$PROJECT_ROOT" config --local --get core.hooksPath || true)
	
	# SENIOR FIX: Converted from a hard block to an intelligent self-healing override alignment track.
	if [[ -n $current_hooks_path && $current_hooks_path != .githooks ]]; then
		warn "Discovered external custom core.hooksPath override mapping configuration currently active: '$current_hooks_path'."
		info "Enforcing localized project directory routing overrides alignment to '.githooks'..."
	fi

	git -C "$PROJECT_ROOT" config --local core.hooksPath .githooks
	success 'Successfully linked and validated internal local workflow validation targets at .githooks'
}

main() {
	cd "$PROJECT_ROOT"
	info "Initializing comprehensive preparation sequence inside: $PROJECT_ROOT"
	check_go
	setup_env
	setup_git_hooks
	success 'Preparation sequence processed successfully. Ensure .env configurations match local topology profiles.'
}

main "$@"
