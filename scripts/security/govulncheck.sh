#!/usr/bin/env bash
# Runs govulncheck and fails on any vulnerability the code calls, unless a
# reviewed, unexpired entry in the allowlist accepts it. An accepted entry
# stops counting as soon as a fixed version exists: upgrade instead.
#
#   GOVULNCHECK            govulncheck binary (default: govulncheck)
#   GOVULNCHECK_ALLOWLIST  allowlist file (default: next to this script)
#
# Allowlist lines: <OSV ID> <expires YYYY-MM-DD> <reason>; '#' starts a comment.
set -euo pipefail

govulncheck=${GOVULNCHECK:-govulncheck}
allowlist=${GOVULNCHECK_ALLOWLIST:-"$(dirname "$0")/govulncheck-allowlist.txt"}
command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
[ -r "$allowlist" ] || { echo "allowlist $allowlist is not readable" >&2; exit 2; }

report=$(mktemp)
trap 'rm -f "$report"' EXIT
"$govulncheck" -format json ./... >"$report"

# Called vulnerabilities: a trace frame names a function (what govulncheck's
# text mode reports as "Your code is affected"). One line: ID and fixed version.
called=$(jq -r 'select(.finding) | .finding
  | select(any(.trace[]; (.function // "") != ""))
  | "\(.osv) \(.fixed_version // "")"' "$report" | sort -u)

today=$(date -u +%Y-%m-%d)
failed=0
while read -r id fixed; do
  [ -n "$id" ] || continue
  entry=$(awk -v id="$id" '$1 == id { print $2; exit }' "$allowlist")
  if [ -n "$fixed" ]; then
    echo "FAIL $id: called, fixed in $fixed; upgrade the module" >&2
    failed=1
  elif [ -z "$entry" ]; then
    echo "FAIL $id: called and not in $allowlist; see https://pkg.go.dev/vuln/$id" >&2
    failed=1
  elif [[ ! "$entry" > "$today" && "$entry" != "$today" ]]; then
    echo "FAIL $id: allowlist entry expired on $entry; review it again" >&2
    failed=1
  else
    echo "accepted $id: no fix available; reviewed until $entry" >&2
  fi
done <<<"$called"

# Entries for advisories that no longer apply should be removed.
while read -r id _; do
  case "$id" in '' | '#'*) continue ;; esac
  grep -q "^$id " <<<"$called" || echo "note: $id is allowlisted but no longer reported; remove the entry" >&2
done <"$allowlist"

if [ "$failed" -ne 0 ]; then
  exit 3
fi
echo "govulncheck: no unaccepted vulnerabilities in called code"
