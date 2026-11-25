#!/bin/sh
# Runs the detached runner against a real systemd host: builds the test
# image (Debian with systemd and OpenSSH) and runs the systemd-tagged tests,
# which start a privileged container. Needs Docker.
set -eu
cd "$(dirname "$0")/../.."
image=${MACHINARCHY_SYSTEMD_IMAGE:-machinarchy-systemd-test:local}
docker build -q -t "$image" test/systemd >/dev/null
MACHINARCHY_SYSTEMD_IMAGE=$image go test -tags systemd -count=1 -timeout 600s -v ./test/systemd/
