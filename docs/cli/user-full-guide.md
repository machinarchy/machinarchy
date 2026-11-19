# Machinarchy CLI: full user guide (a lab with three SSH servers)

This guide takes you from nothing to running commands on three managed hosts.
You will:

1. start three SSH servers in Docker (`web-1`, `web-2`, `db-1`);
2. start Machinarchy and log in;
3. point the `machinarchy` CLI at it (a *context*);
4. store an SSH key, create Access Profiles and register the hosts;
5. trust the hosts' SSH keys, automatically and by approval;
6. run commands: waiting for the result, and detached with live output;
7. cancel a run, see what happens when a host reboots or is reinstalled;
8. switch to a day-to-day API token, and clean up.

Every command below was run for real on 2026-11-19 (build `ff666e1`), and the
output shown is what it printed. IDs, fingerprints, times and uptimes will
differ on your machine. It takes about 30 minutes.

**Conventions.** Lines starting with `$` are what you type; the rest is
output. Keep one terminal for the server (step 2) and one for everything else.

---

## 0. Prerequisites

| Tool | Why |
|---|---|
| Docker | the three SSH servers |
| Go 1.27 and `make` | builds `machinarchy` (CLI) and `machinarchy-monolit` (server) |
| `curl`, `jq` | two steps use the HTTP API directly |
| `ssh`, `ssh-keygen` | the lab's deploy key and a sanity check |

Build both binaries from the repository root and put them on your `PATH`:

```sh
make build
export PATH="$PWD/bin:$PATH"
```

Use a lab directory and a **lab-only CLI config**, so nothing touches your
real CLI configuration:

```sh
export LAB="$HOME/machinarchy-lab"
mkdir -p "$LAB/data" && chmod 700 "$LAB" "$LAB/data"
export MACHINARCHY_CONFIG="$LAB/cli.yaml"
```

---

## 1. Start three SSH servers

Create the key Machinarchy will log in with:

```console
$ ssh-keygen -q -t ed25519 -N "" -C machinarchy-lab -f "$LAB/deploy_key" && ls "$LAB"
data
deploy_key
deploy_key.pub
```

Start three OpenSSH containers. Each accepts that key for the user `deploy`,
on ports 2221–2223 of your loopback interface only:

```console
$ docker run -d --name lab-web-1 --hostname web-1 -p 127.0.0.1:2221:2222 -e PUID=1000 -e PGID=1000 -e USER_NAME=deploy -e PASSWORD_ACCESS=false -e PUBLIC_KEY="$(cat $LAB/deploy_key.pub)" lscr.io/linuxserver/openssh-server:latest >/dev/null && echo started lab-web-1 on port 2221
started lab-web-1 on port 2221

$ docker run -d --name lab-web-2 --hostname web-2 -p 127.0.0.1:2222:2222 -e PUID=1000 -e PGID=1000 -e USER_NAME=deploy -e PASSWORD_ACCESS=false -e PUBLIC_KEY="$(cat $LAB/deploy_key.pub)" lscr.io/linuxserver/openssh-server:latest >/dev/null && echo started lab-web-2 on port 2222
started lab-web-2 on port 2222

$ docker run -d --name lab-db-1 --hostname db-1 -p 127.0.0.1:2223:2222 -e PUID=1000 -e PGID=1000 -e USER_NAME=deploy -e PASSWORD_ACCESS=false -e PUBLIC_KEY="$(cat $LAB/deploy_key.pub)" lscr.io/linuxserver/openssh-server:latest >/dev/null && echo started lab-db-1 on port 2223
started lab-db-1 on port 2223

$ docker ps --filter name=lab- --format "table {{.Names}}\t{{.Ports}}\t{{.Status}}"
NAMES       PORTS                      STATUS
lab-db-1    127.0.0.1:2223->2222/tcp   Up 10 seconds
lab-web-2   127.0.0.1:2222->2222/tcp   Up 13 seconds
lab-web-1   127.0.0.1:2221->2222/tcp   Up 16 seconds
```

Give them a few seconds to generate their host keys, then check that the key
works. The lab keeps its own `known_hosts`, so your `~/.ssh` is not touched:

```console
$ for p in 2221 2222 2223; do ssh -i "$LAB/deploy_key" -p $p -o UserKnownHostsFile="$LAB/known_hosts" -o StrictHostKeyChecking=accept-new deploy@127.0.0.1 "echo \$(hostname) ok" 2>/dev/null; done
web-1 ok
web-2 ok
db-1 ok
```

---

## 2. Start Machinarchy

Write the server's settings to a file. The two secrets are generated, not
typed:

```sh
cat > "$LAB/server.env" <<EOF
MACHINARCHY_ENVIRONMENT=development
MACHINARCHY_LISTEN_ADDR=127.0.0.1:8080
MACHINARCHY_DATABASE_PATH=$LAB/data/machinarchy.db
MACHINARCHY_OPERATOR_TOKEN=$(openssl rand -hex 32)
MACHINARCHY_ENCRYPTION_SECRET=$(openssl rand -hex 32)
MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD=lab-admin-password-123
MACHINARCHY_SSH_ALLOW_LOOPBACK=true
MACHINARCHY_SSH_ALLOWED_PORTS=2221,2222,2223
MACHINARCHY_EXECUTION_ENABLED=true
MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS=uptime,hostname,sh,sleep,df
MACHINARCHY_API_DOCS_ENABLED=true
EOF
chmod 600 "$LAB/server.env"
```

| Setting | Why the lab needs it |
|---|---|
| `MACHINARCHY_ENVIRONMENT=development` | Development defaults; among them, host keys are trusted on first contact unless a profile says otherwise |
| `MACHINARCHY_ENCRYPTION_SECRET` | Encrypts secrets and command output at rest. **Keep it**: a new one makes the stored data unreadable |
| `MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD` | Creates the first owner, `admin`, on an empty database |
| `MACHINARCHY_SSH_ALLOW_LOOPBACK`, `MACHINARCHY_SSH_ALLOWED_PORTS` | The egress policy refuses loopback and non-22 ports by default; the lab's hosts are both |
| `MACHINARCHY_EXECUTION_ENABLED`, `…_ALLOWED_PROGRAMS` | Running commands is off by default, and only listed programs may run |
| `MACHINARCHY_API_DOCS_ENABLED` | Serves the API reference at `/scalar` and `/openapi.json` (see [docs/api](../api/README.md)) |

Start the server in **its own terminal** and leave it running (Ctrl-C stops it):

```sh
set -a; . "$LAB/server.env"; set +a
machinarchy-monolit
```

Back in your main terminal:

```console
$ curl -s http://127.0.0.1:8080/healthz/ready
{"checks":{"control-manager":"ready","database":"ready"},"status":"ready"}

$ curl -s -o /dev/null -w "%{http_code} %{content_type}\n" http://127.0.0.1:8080/openapi.json
200 application/json; charset=utf-8
```

---

## 3. Log in

The CLI has no `login` command: you log in over HTTP and give the CLI the
session token. Keep it in a shell variable, never in a file:

```console
$ curl -s http://127.0.0.1:8080/api/auth/login -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"lab-admin-password-123\"}" | jq "{token: (.token[0:12] + \"…\"), user}"
{
  "token": "eyJhbGciOiJI…",
  "user": {
    "id": 1,
    "username": "admin"
  }
}

$ TOKEN=$(curl -s http://127.0.0.1:8080/api/auth/login -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"lab-admin-password-123\"}" | jq -r .token)
```

A session lasts 24 hours. For day-to-day use, create an API token instead
(step 16).

---

## 4. Configure the CLI context

A **context** is a server (*cluster*) plus a credential (*user*), like
kubectl's:

```console
$ machinarchy config set-cluster lab --server http://127.0.0.1:8080
Cluster "lab" configured.

$ printf "%s" "$TOKEN" | machinarchy config set-credentials lab-admin --token-stdin
Credentials "lab-admin" configured.

$ machinarchy config set-context lab --cluster lab --user lab-admin
Context "lab" configured.

$ machinarchy config use-context lab
Switched to context "lab".

$ machinarchy config current-context
lab

$ machinarchy config get-contexts
CURRENT  NAME  CLUSTER  USER       ENVIRONMENT
*        lab   lab      lab-admin  

$ machinarchy version
machinarchy ff666e1
commit:     ff666e1
built:      2026-11-19T13:12:52Z
go:         go1.27.4
platform:   darwin/amd64
```

`--token-stdin` keeps the token out of your shell history and the process
list. The CLI refuses to send a token over plain `http://` to anything but
loopback; use `https://` for a real server.

---

## 5. Store the SSH key as a secret

Secrets are write-only: you can create, rotate and delete them, never read
them back.

```console
$ machinarchy secret create deploy-key --value-stdin --description "lab deploy key" < "$LAB/deploy_key"
{
  "createdAt": "2026-11-19T13:21:05.738189Z",
  "currentVersion": 1,
  "description": "lab deploy key",
  "id": "cfd8a8bd-f3b6-4d9d-9ac8-8b887f9c9d4b",
  "name": "deploy-key",
  "purpose": "ssh-credential",
  "updatedAt": "2026-11-19T13:21:05.738189Z"
}

$ machinarchy secret list
[
  {
    "createdAt": "2026-11-19T13:21:05.738189Z",
    "currentVersion": 1,
    "description": "lab deploy key",
    "id": "cfd8a8bd-f3b6-4d9d-9ac8-8b887f9c9d4b",
    "name": "deploy-key",
    "purpose": "ssh-credential",
    "updatedAt": "2026-11-19T13:21:05.738189Z"
  }
]
```

Secret commands need an owner's login session; an API token is refused
(step 16).

---

## 6. Create Access Profiles

An **Access Profile** says how to log in to a host: the user, the
authentication method, which secret, and how to trust the host's SSH key.
Hosts point at a profile, so one key rotation updates them all.

Profiles are owner-only and are created through the API (the CLI can list
them). The lab uses two, to show both ways of trusting host keys:

| Profile | `hostKeyPolicy` | Meaning |
|---|---|---|
| `deploy` | `tofu` | Keys seen on first contact are pinned automatically (trust on first use) |
| `deploy-approve` | `approve` | Keys are observed, and an owner approves them with one command |

```console
$ SECRET=$(machinarchy secret list -o json | jq -r ".[] | select(.name==\"deploy-key\") | .id"); echo "$SECRET"
cfd8a8bd-f3b6-4d9d-9ac8-8b887f9c9d4b

$ curl -s -X POST http://127.0.0.1:8080/api/v1/managed/access-profiles -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d "{\"name\":\"deploy\",\"description\":\"lab hosts, keys pinned on first contact\",\"spec\":{\"connectorKind\":\"ssh\",\"authenticationMethod\":\"private-key\",\"username\":\"deploy\",\"enabled\":true,\"hostKeyPolicy\":\"tofu\",\"credential\":{\"id\":\"$SECRET\",\"version\":1}}}" | jq "{id, name, currentRevision}"
{
  "id": "b54e1b5b-2220-460f-8ba1-d9c752109c9e",
  "name": "deploy",
  "currentRevision": 1
}

$ curl -s -X POST http://127.0.0.1:8080/api/v1/managed/access-profiles -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d "{\"name\":\"deploy-approve\",\"description\":\"keys need an owner approval\",\"spec\":{\"connectorKind\":\"ssh\",\"authenticationMethod\":\"private-key\",\"username\":\"deploy\",\"enabled\":true,\"hostKeyPolicy\":\"approve\",\"credential\":{\"id\":\"$SECRET\",\"version\":1}}}" | jq "{id, name, currentRevision}"
{
  "id": "c801d469-01fe-43f8-b0ae-d3eab878162b",
  "name": "deploy-approve",
  "currentRevision": 1
}

$ machinarchy get profiles
NAME            ID                                    REVISION  DESCRIPTION
deploy          b54e1b5b-2220-460f-8ba1-d9c752109c9e  1         lab hosts, keys pinned on first contact
deploy-approve  c801d469-01fe-43f8-b0ae-d3eab878162b  1         keys need an owner approval
```

Other profile options (`ca` with host certificates, `execution.supervisor`,
`execution.maxOutputFileBytes`) are described in
[operations](../operations.md#ssh-host-keys) and
[detached execution](../operations.md#detached-execution).

---

## 7. Register the hosts

Labels (`--label KEY=VALUE`) let you select hosts later. No fingerprint is
needed:

```console
$ machinarchy create host web-1 --endpoint 127.0.0.1:2221 --profile deploy --label tier=web
NAME   ID                                    ENVIRONMENT  ENDPOINT        PHASE    READY
web-1  238069aa-d964-48c4-a077-2e0336d6b7a2  -            127.0.0.1:2221  Pending  Unknown

$ machinarchy create host web-2 --endpoint 127.0.0.1:2222 --profile deploy --label tier=web
NAME   ID                                    ENVIRONMENT  ENDPOINT        PHASE    READY
web-2  214b49d5-7e5d-4460-8149-62e4afbc44ae  -            127.0.0.1:2222  Pending  Unknown

$ machinarchy create host db-1 --endpoint 127.0.0.1:2223 --profile deploy-approve --label tier=db
NAME  ID                                    ENVIRONMENT  ENDPOINT        PHASE    READY
db-1  9315694a-95a0-4bab-90b3-51900f7fe031  -            127.0.0.1:2223  Pending  Unknown
```

A second later the controller has checked them:

```console
$ machinarchy get hosts
NAME   ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY
web-2  214b49d5-7e5d-4460-8149-62e4afbc44ae  -            127.0.0.1:2222  Active  True
web-1  238069aa-d964-48c4-a077-2e0336d6b7a2  -            127.0.0.1:2221  Active  True
db-1   9315694a-95a0-4bab-90b3-51900f7fe031  -            127.0.0.1:2223  Active  False

$ machinarchy get hosts -l tier=web
NAME   ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY
web-2  214b49d5-7e5d-4460-8149-62e4afbc44ae  -            127.0.0.1:2222  Active  True
web-1  238069aa-d964-48c4-a077-2e0336d6b7a2  -            127.0.0.1:2221  Active  True
```

`web-1` and `web-2` (profile `deploy`, `tofu`) pinned their keys and logged
in. `db-1` (profile `deploy-approve`) is waiting for you.

---

## 8. Trust host keys

The table shows only `READY True/False`; the reason and the keys the
controller saw are in the YAML view:

```console
$ machinarchy get hosts db-1 -o yaml | sed -n "/observedHostKeys/,/^[a-z]/p"
  observedHostKeys:
    - algorithm: ssh-ed25519
      fingerprint: SHA256:403pYCFSmX5C4WmUnR1EvYU0Y8kYKcQPZvYGe/RnVLg
      firstSeenAt: 2026-11-19T13:21:06.699866Z
      lastSeenAt: 2026-11-19T13:21:06.699866Z
    - algorithm: ecdsa-sha2-nistp256
      fingerprint: SHA256:kDMrqfbmER2AcCfKVaNZhscyKdP9Xfn1OtZ9zEBtvdY
      firstSeenAt: 2026-11-19T13:21:06.699866Z
      lastSeenAt: 2026-11-19T13:21:06.699866Z
    - algorithm: ssh-rsa
      fingerprint: SHA256:Lbp/AxhSExc+G/eFucDANk98TK84MmMkI5HYMKRN6tY
      firstSeenAt: 2026-11-19T13:21:06.699866Z
      lastSeenAt: 2026-11-19T13:21:06.699866Z
```

On a real network, compare these with the host's own keys (`ssh-keygen -lf
/etc/ssh/ssh_host_*_key.pub` on its console) before approving. Then:

```console
$ machinarchy trust host db-1 --yes
SSH host keys to trust:
  db-1 (127.0.0.1:2223):
    SHA256:403pYCFSmX5C4WmUnR1EvYU0Y8kYKcQPZvYGe/RnVLg
    SHA256:kDMrqfbmER2AcCfKVaNZhscyKdP9Xfn1OtZ9zEBtvdY
    SHA256:Lbp/AxhSExc+G/eFucDANk98TK84MmMkI5HYMKRN6tY
Host "db-1" now trusts 3 host key(s).

$ machinarchy get hosts
NAME   ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY
web-2  214b49d5-7e5d-4460-8149-62e4afbc44ae  -            127.0.0.1:2222  Active  True
web-1  238069aa-d964-48c4-a077-2e0336d6b7a2  -            127.0.0.1:2221  Active  True
db-1   9315694a-95a0-4bab-90b3-51900f7fe031  -            127.0.0.1:2223  Active  True
```

Without `--yes` the CLI shows the same list and asks before trusting. With
many hosts waiting, `machinarchy trust host --all-pending --selector tier=db`
approves them in one go.

A full host record:

```console
$ machinarchy get hosts web-1 -o yaml
metadata:
  id: 238069aa-d964-48c4-a077-2e0336d6b7a2
  name: web-1
  labels:
    tier: web
  generation: 2
  revision: 4
  createdAt: 2026-11-19T13:21:05.996028Z
  updatedAt: 2026-11-19T13:21:07.158134Z
spec:
  presence: Managed
  accessProfileId: b54e1b5b-2220-460f-8ba1-d9c752109c9e
  accessTarget:
    endpoint: 127.0.0.1:2221
    trust:
      - SHA256:d+bxO0LYe/+T0hm8iCmrmODqfBlR9fLLgS2Yhpx+Ons
      - SHA256:EKt15bKdfUzXJLoaQLKCylXbGciQAe+9OqEvUj3FmN8
      - SHA256:ZFzZKU9faVfGYyNaKZxI7yTPDKGd9wqvxSI7gtvw9jc
status:
  phase: Active
  conditions:
    - type: Ready
      status: "True"
      reason: SSHVerified
    - type: Reachable
      status: "True"
      reason: Reachable
    - type: Configured
      status: "True"
      reason: ConfigurationPresent
```

---

## 9. Run a command and wait for the result

`run` takes the program and its arguments after `--`. There is no shell
unless you run one (`sh -c`, next step), and the program must be on the
server's allowed list. `--wait` keeps the SSH session open and prints the
result (at most 15 minutes):

```console
$ machinarchy run -l tier=web --wait --yes -- uptime
HOST   STATE      EXIT  OUTPUT
web-1  Succeeded  0     13:21:08 up 14:03,  0 users,  load average: 0.95, 0.58, 0.31
web-2  Succeeded  0     13:21:08 up 14:03,  0 users,  load average: 0.95, 0.58, 0.31
OPERATION                             STATE
4503230e-e667-4aa4-bc07-6de038bb2612  Succeeded

$ machinarchy run --host db-1 --wait --yes -- df -h /
HOST  STATE      EXIT  OUTPUT
db-1  Succeeded  0     Filesystem      Size  Used Avail Use% Mounted on
OPERATION                             STATE
827c161b-ca9b-44be-82c9-86dd020e0000  Succeeded
```

The table shows only the **first line** of output. For all of it, use
`machinarchy logs operation ID` (next step) or `get operation ID -o yaml`.

Without `--yes`, `run` lists the hosts it selected and asks first; it then runs
on exactly the hosts it showed you, even if labels change in between.

Selecting hosts: `-l SELECTOR` (`tier=web`, `tier=web,region in (eu,us)`),
`--host NAME` (repeatable), `--all`, and `--exclude NAME`.

---

## 10. Run detached and follow the output

Without `--wait`, a run is **detached**: each host starts the command under a
supervisor and the SSH session ends at once. The command keeps running even if
Machinarchy restarts, and may last up to 24 hours (`--timeout`, in seconds).

```console
$ machinarchy run --all --yes -- sh -c "for i in 1 2 3; do echo step \$i on \$(hostname); sleep 2; done; echo done >&2"
Operation e37a8e10-7dcb-4068-bfce-ca1ab73597e5 submitted (detached).
Follow it with:  machinarchy logs operation e37a8e10-7dcb-4068-bfce-ca1ab73597e5 --follow
Status:          machinarchy get operation e37a8e10-7dcb-4068-bfce-ca1ab73597e5
```

Follow one host's output while it runs. `--follow` ends when the command does;
new lines appear every few seconds (the controller polls each host every 5 s,
less often for long runs):

```console
$ machinarchy logs operation e37a8e10-7dcb-4068-bfce-ca1ab73597e5 --host web-1 --follow
step 1 on web-1
step 2 on web-1
step 3 on web-1
```

Without `--host`, `logs` prints every host in turn (without `--follow`):

```console
$ machinarchy logs operation e37a8e10-7dcb-4068-bfce-ca1ab73597e5
==> db-1 (Succeeded) <==
step 1 on db-1
step 2 on db-1
step 3 on db-1

==> web-1 (Succeeded) <==
step 1 on web-1
step 2 on web-1
step 3 on web-1

==> web-2 (Succeeded) <==
step 1 on web-2
step 2 on web-2
step 3 on web-2

$ machinarchy logs operation e37a8e10-7dcb-4068-bfce-ca1ab73597e5 --host db-1 --stream stderr
done
```

The summary, and the full record:

```console
$ machinarchy get operation e37a8e10-7dcb-4068-bfce-ca1ab73597e5
HOST   STATE      EXIT  OUTPUT
db-1   Succeeded  0     step 1 on db-1
web-1  Succeeded  0     step 1 on web-1
web-2  Succeeded  0     step 1 on web-2
OPERATION                             STATE
e37a8e10-7dcb-4068-bfce-ca1ab73597e5  Succeeded

$ machinarchy get operation e37a8e10-7dcb-4068-bfce-ca1ab73597e5 -o yaml | sed -n '/targets:/,$p' | head -20
  targets:
    - spec:
        hostId: 9315694a-95a0-4bab-90b3-51900f7fe031
        displayName: db-1
      state: Succeeded
      result:
        exitCode: 0
        stdout: |
          step 1 on db-1
          step 2 on db-1
          step 3 on db-1
        stderr: |
          done
        stdoutBytes: 45
        stderrBytes: 5
      progress:
        stdoutBytes: 45
        stderrBytes: 5
        lastContactAt: 2026-11-19T13:21:21.632149Z
        polls: 3
```

A long output keeps its first and last 15 KiB per stream; `stdoutBytes` is the
full size and `truncated: true` marks a cut. Output is visible only to users
allowed to run commands; readers see states and exit codes.

---

## 11. Labels and selectors

```console
$ machinarchy label host db-1 role=primary
NAME  ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY
db-1  9315694a-95a0-4bab-90b3-51900f7fe031  -            127.0.0.1:2223  Active  True

$ machinarchy get hosts -l role=primary
NAME  ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY
db-1  9315694a-95a0-4bab-90b3-51900f7fe031  -            127.0.0.1:2223  Active  True

$ machinarchy label host db-1 role-
NAME  ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY
db-1  9315694a-95a0-4bab-90b3-51900f7fe031  -            127.0.0.1:2223  Active  True
```

`KEY=VALUE` sets a label and `KEY-` removes it. The table does not show labels;
`get hosts NAME -o yaml` does.

---

## 12. Cancel a run

```console
$ machinarchy run -l tier=web --yes -- sleep 300
Operation c2f72e21-a449-408c-b5e5-17ad8b311b6a submitted (detached).
Follow it with:  machinarchy logs operation c2f72e21-a449-408c-b5e5-17ad8b311b6a --follow
Status:          machinarchy get operation c2f72e21-a449-408c-b5e5-17ad8b311b6a

$ machinarchy cancel operation c2f72e21-a449-408c-b5e5-17ad8b311b6a
Cancellation requested for operation c2f72e21-a449-408c-b5e5-17ad8b311b6a (CancelRequested).

$ machinarchy get operation c2f72e21-a449-408c-b5e5-17ad8b311b6a
HOST   STATE      EXIT  OUTPUT
web-1  Cancelled  143   cancelled
web-2  Cancelled  143   cancelled
OPERATION                             STATE
c2f72e21-a449-408c-b5e5-17ad8b311b6a  Cancelled
```

Cancel sends SIGTERM to the command's whole process group, then SIGKILL after
10 seconds; exit 143 is 128 + SIGTERM. Nothing is left on the host:

```console
$ docker exec lab-web-1 pgrep -fx "sleep 300" || echo "no sleep 300 left on web-1"
no sleep 300 left on web-1
```

---

## 13. Long runs, timeouts and `Lost`

`--timeout` is per host, in seconds: up to 86400 (24 h) for a detached run,
900 with `--wait`. A command still running at its timeout is stopped on the
host and ends `TimedOut`.

If a host reboots during a run, nobody can know whether the command
finished, so Machinarchy says so instead of guessing:

```console
$ machinarchy run --host web-2 --timeout 3600 --yes -- sleep 600
Operation 735dc57a-34dc-4454-9b17-cd6ec0729422 submitted (detached).
Follow it with:  machinarchy logs operation 735dc57a-34dc-4454-9b17-cd6ec0729422 --follow
Status:          machinarchy get operation 735dc57a-34dc-4454-9b17-cd6ec0729422

$ docker restart lab-web-2
lab-web-2

$ machinarchy get operation 735dc57a-34dc-4454-9b17-cd6ec0729422
HOST   STATE  EXIT  OUTPUT
web-2  Lost   -     supervisor_lost
OPERATION                             STATE
735dc57a-34dc-4454-9b17-cd6ec0729422  Failed
```

`Lost` is never retried automatically: check the host, then run again if
needed. Other causes are listed in the
[runbook](../runbook.md#a-detached-target-is-lost).

---

## 14. When a host's SSH key changes

Reinstall `web-2`, which gives it new host keys:

```console
$ docker rm -f lab-web-2 >/dev/null && docker run -d --name lab-web-2 --hostname web-2 -p 127.0.0.1:2222:2222 -e PUID=1000 -e PGID=1000 -e USER_NAME=deploy -e PASSWORD_ACCESS=false -e PUBLIC_KEY="$(cat $LAB/deploy_key.pub)" lscr.io/linuxserver/openssh-server:latest >/dev/null && echo web-2 reinstalled
web-2 reinstalled
```

Machinarchy refuses to log in to a host whose key it does not trust, so
commands to it fail at once. The host still shows `READY True` until the
controller's next periodic check, every 5 minutes by default (a known gap, see
"Troubleshooting"):

```console
$ machinarchy run --host web-2 --wait --yes -- hostname
HOST   STATE   EXIT  OUTPUT
web-2  Failed  -     remote_execution_failed
OPERATION                             STATE
b3c54423-c178-4690-9ec9-14c451b901b5  Failed
Error: operation b3c54423-c178-4690-9ec9-14c451b901b5 finished Failed

$ machinarchy get hosts web-2
NAME   ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY
web-2  214b49d5-7e5d-4460-8149-62e4afbc44ae  -            127.0.0.1:2222  Active  True
```

To check every host now, restart the server (Ctrl-C in its terminal, then
start it again as in step 2; your token stays valid). After startup:

```console
$ machinarchy get hosts web-2
NAME   ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY
web-2  214b49d5-7e5d-4460-8149-62e4afbc44ae  -            127.0.0.1:2222  Active  False

$ machinarchy get hosts web-2 -o json | jq -r '.status.conditions[] | select(.type=="Ready") | .reason + ": " + .message'
HostKeyChanged: the host presents SHA256:WXa2zgfGKJdjaT5cggrQUAOyiW4J0ZBdtZUjyqHwSDE, SHA256:2RJ1wAJ6J3Gl7gkMI5Bt8HTYrz/ugVRCzA3zOANHlcQ, SHA256:Mt+tJTSmyX8DnCcXYdoctLGMpUn5EgNP/vt0ug4dan4; trusted: SHA256:gAMOJkZfvtFQXbmtQDauNlLDXklQMaygmwJhkGVXbgI, SHA256:h9iy6ns736l6LedpvT5VoycW6UYl1/fqlG5qW4g1HnM, SHA256:rIFaABi6eP3nQZVVSOw8zBzK85g5f438NxSGQlKaz18. Nothing was sent to it. If the change is expected: machinarchy trust host web-2 --replace
```

A changed key is never accepted automatically: it may be a reinstall, or
someone in the middle. Once you have checked the new keys out of band, replace
the old ones:

```console
$ machinarchy trust host web-2 --replace --yes
SSH host keys to trust:
  web-2 (127.0.0.1:2222):
    SHA256:WXa2zgfGKJdjaT5cggrQUAOyiW4J0ZBdtZUjyqHwSDE
    SHA256:2RJ1wAJ6J3Gl7gkMI5Bt8HTYrz/ugVRCzA3zOANHlcQ
    SHA256:Mt+tJTSmyX8DnCcXYdoctLGMpUn5EgNP/vt0ug4dan4
    replacing SHA256:gAMOJkZfvtFQXbmtQDauNlLDXklQMaygmwJhkGVXbgI, SHA256:h9iy6ns736l6LedpvT5VoycW6UYl1/fqlG5qW4g1HnM, SHA256:rIFaABi6eP3nQZVVSOw8zBzK85g5f438NxSGQlKaz18
Host "web-2" now trusts 3 host key(s).

$ machinarchy get hosts web-2
NAME   ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY
web-2  214b49d5-7e5d-4460-8149-62e4afbc44ae  -            127.0.0.1:2222  Active  True

$ machinarchy run --host web-2 --wait --yes -- hostname
HOST   STATE      EXIT  OUTPUT
web-2  Succeeded  0     web-2
OPERATION                             STATE
f1b6c979-0174-439d-9f6a-ea25683439e8  Succeeded
```

---

## 15. History

```console
$ machinarchy get operations
ID                                    STATE      TARGETS      CREATED
f1b6c979-0174-439d-9f6a-ea25683439e8  Succeeded  Succeeded=1  2026-11-19 15:21:51
b3c54423-c178-4690-9ec9-14c451b901b5  Failed     Failed=1     2026-11-19 15:21:49
735dc57a-34dc-4454-9b17-cd6ec0729422  Failed     Lost=1       2026-11-19 15:21:35
c2f72e21-a449-408c-b5e5-17ad8b311b6a  Cancelled  Cancelled=2  2026-11-19 15:21:22
e37a8e10-7dcb-4068-bfce-ca1ab73597e5  Succeeded  Succeeded=3  2026-11-19 15:21:10
52c499a9-6297-405e-bb02-7778f89e3ffe  Succeeded  Succeeded=1  2026-11-19 15:21:09
4503230e-e667-4aa4-bc07-6de038bb2612  Succeeded  Succeeded=2  2026-11-19 15:21:08

$ machinarchy get operations --state Succeeded --limit 3
ID                                    STATE      TARGETS      CREATED
f1b6c979-0174-439d-9f6a-ea25683439e8  Succeeded  Succeeded=1  2026-11-19 15:21:51
e37a8e10-7dcb-4068-bfce-ca1ab73597e5  Succeeded  Succeeded=3  2026-11-19 15:21:10
52c499a9-6297-405e-bb02-7778f89e3ffe  Succeeded  Succeeded=1  2026-11-19 15:21:09
```

---

## 16. Day to day: an API token and several contexts

The owner session from step 3 can do everything, and expires in 24 hours. For
daily work, an owner creates an **API token** with a narrower role:

| Role | Can |
|---|---|
| `operator` | read everything, create and label hosts, run and cancel commands, read output |
| `reader` | read hosts and operations (no output) |

Secrets, Access Profiles, host-key approval and runtime configuration stay
with the owner's own login: API tokens are refused for them.

```console
$ curl -s -X POST http://127.0.0.1:8080/api/v1/tokens -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d "{\"name\":\"lab-cli\",\"role\":\"operator\",\"expiresInHours\":720}" | jq "{name, role, expiresAt, token: (.token[0:12] + \"…\")}"
{
  "name": "lab-cli",
  "role": "operator",
  "expiresAt": "2026-12-19T13:22:48.659863Z",
  "token": "mch_pat_seLK…"
}
```

The token is shown **once**. Capture it straight into a variable (`OPTOKEN=$(curl
… | jq -r .token)`) and store it as a second CLI user and context:

```console
$ printf "%s" "$OPTOKEN" | machinarchy config set-credentials lab-operator --token-stdin
Credentials "lab-operator" configured.

$ machinarchy config set-context lab-ops --cluster lab --user lab-operator
Context "lab-ops" configured.

$ machinarchy config use-context lab-ops
Switched to context "lab-ops".

$ machinarchy config get-contexts
CURRENT  NAME     CLUSTER  USER          ENVIRONMENT
         lab      lab      lab-admin     
*        lab-ops  lab      lab-operator  

$ machinarchy secret list
Error: API returned HTTP 403: permission denied (request ID IssupaPSPGpSdQQHnNwQiQzDPmCnovif)
Hint: The credential is valid but is not permitted to do this.
Commands that manage runtime configuration or secrets need a human owner session; API tokens (mch_pat_...) are refused for them.

$ machinarchy --context lab secret list
[]
```

`--context NAME` runs one command with another context without switching.
(This step ran against a fresh lab server, which is why the secret list is
empty.)

---

## 17. Troubleshooting

Real errors from this lab and what they mean:

| You see | Meaning | Fix |
|---|---|---|
| `Error: unknown hosts: nope` | No host by that name or ID | `machinarchy get hosts` |
| `Run "uptime" on 2 host(s): web-1, web-2? [y/N]: Error: confirmation required` | No terminal to ask in (a script) | Add `--yes` |
| `Error: this operation has 3 targets; pick one with --host (db-1, web-1, web-2)` | `logs --follow` follows one host | Add `--host NAME` |
| `HTTP 403 … API tokens (mch_pat_...) are refused for them` | Owner-only command with an API token | `--context` with the owner's login |
| `READY False` in `get hosts` | See the reason: `get hosts NAME -o yaml` | `HostKeyPendingApproval`: step 8. `HostKeyChanged`: step 14. Others: [runbook](../runbook.md#hosts-never-become-ready) |
| `Failed` / `remote_execution_failed` or `launch_host_key_rejected` | SSH failed (host key, login, network) | Check the host as in step 14 |
| `Lost` / `supervisor_lost` | The host restarted during a detached run | Step 13 |

**Known problems** (recorded in AUDIT/13C, to be fixed in a later phase):

- **A program that is not on the allowed list gives HTTP 500:**

  ```console
  $ machinarchy run --host web-1 --wait --yes -- whoami
  Error: submit operation: API returned HTTP 500: request failed (request ID eOMPxLrcDdSLtRLOFMZaqWxAiPNOedtB)
  Hint: The server reported an internal error. Retry later. Include the request ID when reporting the problem.
  ```

  The cause is the server's `MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS`, not an
  internal failure; add the program there and restart the server.
- **A host with a changed key keeps `READY True` for up to 5 minutes**
  (step 14), until the controller's next periodic check. Commands to it fail
  safely meanwhile; a server restart checks every host at once.

---

## 18. Clean up

```console
$ machinarchy delete host web-1 --yes && machinarchy delete host web-2 --yes && machinarchy delete host db-1 --yes
Host "web-1" deleted.
Host "web-2" deleted.
Host "db-1" deleted.

$ machinarchy get hosts
NAME                 ID  ENVIRONMENT  ENDPOINT  PHASE  READY
No resources found.                                    
```

Stop the server with Ctrl-C in its terminal, then remove the containers and,
if you are done, the lab directory:

```console
$ docker rm -f lab-web-1 lab-web-2 lab-db-1
lab-web-1
lab-web-2
lab-db-1
```

```sh
rm -rf "$LAB"   # deletes the lab's database, key and CLI config
```

`delete host` is refused while an operation still targets the host; cancel it
first.

---

## Cheat sheet

| Task | Command |
|---|---|
| Switch context | `machinarchy config use-context NAME` |
| List hosts / one host in full | `machinarchy get hosts [-l SELECTOR]` / `get hosts NAME -o yaml` |
| Register a host | `machinarchy create host NAME --endpoint HOST:PORT --profile PROFILE --label K=V` |
| Approve keys | `machinarchy trust host NAME` / `--all-pending [--selector S]` / `NAME --replace` |
| Run and wait (≤ 15 min) | `machinarchy run -l S --wait -- PROGRAM ARGS…` |
| Run detached (≤ 24 h) | `machinarchy run -l S [--timeout SECONDS] -- PROGRAM ARGS…` |
| Follow output | `machinarchy logs operation ID --host NAME --follow [--stream stderr]` |
| Status / history | `machinarchy get operation ID` / `get operations [--state S]` |
| Cancel | `machinarchy cancel operation ID` |
| Labels | `machinarchy label host NAME K=V K2-` |

More: [command reference](commands.md), [operations](../operations.md),
[runbook](../runbook.md), [API reference](../api/README.md).
