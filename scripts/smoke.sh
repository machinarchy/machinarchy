#!/usr/bin/env bash
# End-to-end smoke test of the managed API through the CLI.
#
# Starts bin/machinarchy-monolit on a throwaway SQLite database, signs in as
# the bootstrap owner, and drives the CLI: secret, Access Profile, host
# create/label/delete, a bound run (expected to fail: the endpoint is closed),
# operation listing, and the legacy-route 404. Build first with `make build`.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
chmod 700 "$work"
port=${SMOKE_PORT:-18080}
api="http://127.0.0.1:${port}"
server_pid=""
cleanup() {
	[[ -n $server_pid ]] && kill "$server_pid" 2>/dev/null || true
	rm -rf "$work"
}
trap cleanup EXIT

fail() { echo "smoke: FAIL: $*" >&2; [[ -f $work/server.log ]] && tail -20 "$work/server.log" >&2; exit 1; }
step() { echo "smoke: $*"; }

MACHINARCHY_ENVIRONMENT=development MACHINARCHY_LISTEN_ADDR="127.0.0.1:${port}" \
	MACHINARCHY_DATABASE_PATH="$work/data/machinarchy.db" \
	MACHINARCHY_OPERATOR_TOKEN=smoke-operator-token-0123456789abcdef0123 \
	MACHINARCHY_ENCRYPTION_SECRET=smoke-encryption-secret-0123456789abcdef \
	MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD=smoke-admin-password \
	MACHINARCHY_EXECUTION_ENABLED=true MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS=uptime \
	"$root/bin/machinarchy-monolit" >"$work/server.log" 2>&1 &
server_pid=$!
for _ in $(seq 1 60); do curl -sf "$api/healthz/ready" >/dev/null && break; sleep 0.5; done
curl -sf "$api/healthz/ready" >/dev/null || fail "server did not become ready"

token=$(curl -sf "$api/api/auth/login" -H 'Content-Type: application/json' \
	-d '{"username":"admin","password":"smoke-admin-password"}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])') ||
	fail "login"
export XDG_CONFIG_HOME="$work"
cli() { "$root/bin/machinarchy" --config "$work/cli.yaml" "$@"; }
cli config set-cluster smoke --server "$api" >/dev/null
printf '%s' "$token" | cli config set-credentials smoke --token-stdin >/dev/null
cli config set-context smoke --cluster smoke --user smoke >/dev/null
cli config use-context smoke >/dev/null

step "secret and Access Profile"
secret=$(printf 'not-a-real-password' | cli secret create smoke --value-stdin)
secret_id=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$secret")
curl -sf "$api/api/v1/managed/access-profiles" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
	-d "{\"name\":\"deploy\",\"spec\":{\"connectorKind\":\"ssh\",\"username\":\"deploy\",\"authenticationMethod\":\"password\",\"credential\":{\"id\":\"$secret_id\",\"version\":1},\"enabled\":true}}" >/dev/null ||
	fail "create Access Profile"
cli get profiles | grep -q deploy || fail "get profiles"

step "hosts"
cli create host web-01 --endpoint 127.0.0.1:1 --profile deploy --label tier=web -q >/dev/null || fail "create host"
cli create host db-01 --endpoint 127.0.0.1:2 --profile deploy --label tier=db -q >/dev/null || fail "create second host"
[[ $(cli get hosts -l tier=web -q | wc -l) -eq 1 ]] || fail "selector"
cli label host web-01 canary=yes -o json | grep -q '"canary": "yes"' || fail "label host"
if cli create host web-01 --endpoint 127.0.0.1:3 --profile deploy >/dev/null 2>&1; then fail "duplicate host accepted"; fi

step "operations"
if cli run -l tier=web --yes --wait --timeout 5 -- uptime >"$work/run.txt" 2>&1; then fail "run against a closed port succeeded"; fi
grep -q 'web-01' "$work/run.txt" || fail "run output: $(cat "$work/run.txt")"
cli get operations | grep -q 'Failed=1' || fail "get operations"

step "delete and legacy routes"
cli delete host db-01 --yes >/dev/null || fail "delete host"
[[ $(cli get hosts -q | wc -l) -eq 1 ]] || fail "host count after delete"
code=$(curl -s -o /dev/null -w '%{http_code}' "$api/api/v1/hosts" -H "Authorization: Bearer $token")
[[ $code == 404 ]] || fail "legacy /api/v1/hosts returned $code"

echo "smoke: OK"
