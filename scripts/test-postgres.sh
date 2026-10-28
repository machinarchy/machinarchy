#!/usr/bin/env bash
# Runs the store contract tests against a throwaway PostgreSQL container.
# Usage: bash scripts/test-postgres.sh [go test packages and flags...]
# Requires Docker. Set POSTGRES_IMAGE to test another server version.
set -euo pipefail

image="${POSTGRES_IMAGE:-postgres:16-alpine}"
password="$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"
container="machinarchy-test-pg-$$"

cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --name "$container" \
  -e POSTGRES_PASSWORD="$password" \
  -p 127.0.0.1::5432 \
  "$image" >/dev/null

port="$(docker port "$container" 5432/tcp | head -n1 | awk -F: '{print $NF}')"

for _ in $(seq 1 60); do
  if docker exec "$container" pg_isready -U postgres >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [ "${ready:-0}" != 1 ]; then
  echo "PostgreSQL did not become ready in time" >&2
  exit 1
fi

export MACHINARCHY_TEST_POSTGRES_URL="postgres://postgres:${password}@127.0.0.1:${port}/postgres?sslmode=disable"
export MACHINARCHY_REQUIRE_POSTGRES_TESTS=1

if [ "$#" -eq 0 ]; then
  set -- ./internal/store/... -count=1
fi
go test "$@"
