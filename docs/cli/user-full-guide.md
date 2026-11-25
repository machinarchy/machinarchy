# Machinarchy CLI: full user guide (a lab with three SSH servers)

This guide takes you from nothing to running commands on three managed hosts,
using the repository's own SSH test servers
([`deployments/testing`](../../deployments/testing/README.md)). You will:

1. start the three SSH servers with `make ssh-servers-up`;
2. start Machinarchy and log in;
3. point the `machinarchy` CLI at it (a *context*);
4. store the lab's SSH key and password, create Access Profiles, register the hosts;
5. trust the hosts' SSH keys, automatically and by approval;
6. run commands: waiting for the result, and detached with live output;
7. cancel a run, and see what happens when a host restarts or presents other keys;
8. switch to a day-to-day API token, and clean up.

Every command below was run for real on 2026-11-19 (build `ff666e1`), and the
output shown is what it printed. Absolute paths were shortened to
repository-relative ones, and a few long outputs are cut where a line reads `…`. IDs, fingerprints, times and uptimes will differ on
your machine. It takes about 30 minutes.

**Conventions.** Lines starting with `$` are what you type; the rest is
output. Run everything from the repository root. Keep one terminal for the
server (step 2) and one for everything else.

---

## 0. Prerequisites

| Tool | Why |
|---|---|
| Docker with Compose | the three SSH servers |
| Go 1.27 and `make` | builds `machinarchy` (CLI) and `machinarchy-monolith` (server) |
| `curl`, `jq` | the readiness check and a few JSON filters |
| `ssh` | a sanity check of the lab |

Build both binaries and put them on your `PATH`:

```sh
make build
export PATH="$PWD/bin:$PATH"
```

> **Prefer `make`?** Every `machinarchy …` command in this guide can also be
> run from the repository root as `make run-cli ARGS="…"`, for example
> `make run-cli ARGS="get hosts"`. Put all the CLI's arguments in `ARGS`;
> quotes inside it are kept. The guide uses the binaries.

Keep the server's database and a **lab-only CLI config** in a lab directory,
so nothing touches your real CLI configuration:

```sh
export LAB="$HOME/machinarchy-lab"
mkdir -p "$LAB/data" && chmod 700 "$LAB" "$LAB/data"
export MACHINARCHY_CONFIG="$LAB/cli.yaml"
```

---

## 1. Start the three SSH servers

The lab has three OpenSSH servers on your loopback interface, all with the
user `testuser`:

| Service | Endpoint | Login |
|---|---|---|
| `ssh-password` | `127.0.0.1:2221` | password only |
| `ssh-key` | `127.0.0.1:2222` | Ed25519 key only |
| `ssh-hostcheck` | `127.0.0.1:2223` | password only; its host keys survive recreation |

`make ssh-servers-up` generates the lab's credentials under `data/.ssh/`
(gitignored) the first time, and asks for a test password (or reads
`SSH_TEST_PASSWORD`). Later runs reuse them:

```console
$ make ssh-servers-up
→ Machinarchy local SSH fixture preparation
→ Repository: …/machinarchy
✓ data/.ssh/ is ignored by Git.
→ Preparing local SSH fixture directories...
✓ Local SSH directories prepared.
→ Preparing Ed25519 test client key...
✓ Existing test key preserved.
→ Writing SSH authentication policy snippets...
✓ Password-only, key-only, and host-check policies prepared.
→ Preparing local SSH test password...
→ Reusing existing local password file.
✓ Local password file prepared with permissions 600.

Machinarchy SSH test fixtures are ready.

Compose services (managed through Makefile):
  ssh-password   Password-only authentication: localhost:2221
  ssh-key        Ed25519-only authentication:   localhost:2222
  ssh-hostcheck  Persistent host key testing:   localhost:2223

Generated files:
  Client private key: data/.ssh/test_id_ed25519
  Client public key:  data/.ssh/test_id_ed25519.pub
  Test password:      data/.ssh/testuser.password
  Container configs:  data/.ssh/config/{password,key,hostcheck}
…
✓ SSH fixture preparation completed. Docker was not started.
docker compose --project-name "machinarchy-ssh-test" -f "deployments/testing/ssh-servers.docker-compose.yml" config --quiet
…
 Network machinarchy-ssh-test_testing-net  Created
…
 Container machinarchy-ssh-test-ssh-hostcheck-1  Started
 Container machinarchy-ssh-test-ssh-password-1  Started
 Container machinarchy-ssh-test-ssh-key-1  Started

$ make ssh-servers-ps
docker compose --project-name "machinarchy-ssh-test" -f "deployments/testing/ssh-servers.docker-compose.yml" ps
NAME                                   IMAGE                                       COMMAND   SERVICE         CREATED          STATUS                    PORTS
machinarchy-ssh-test-ssh-hostcheck-1   lscr.io/linuxserver/openssh-server:latest   "/init"   ssh-hostcheck   18 seconds ago   Up 15 seconds (healthy)   127.0.0.1:2223->2222/tcp
machinarchy-ssh-test-ssh-key-1         lscr.io/linuxserver/openssh-server:latest   "/init"   ssh-key         18 seconds ago   Up 15 seconds (healthy)   127.0.0.1:2222->2222/tcp
machinarchy-ssh-test-ssh-password-1    lscr.io/linuxserver/openssh-server:latest   "/init"   ssh-password    18 seconds ago   Up 15 seconds (healthy)   127.0.0.1:2221->2222/tcp
```

Check the key works (the lab keeps its own `known_hosts`, so your `~/.ssh` is
not touched):

```console
$ ssh -i data/.ssh/test_id_ed25519 -p 2222 -o UserKnownHostsFile="$LAB/known_hosts" -o StrictHostKeyChecking=accept-new testuser@127.0.0.1 hostname 2>/dev/null
machinarchy-ssh-key
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
MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS='uptime,hostname,sh -c *,sleep {int},df -h {path}'
MACHINARCHY_API_DOCS_ENABLED=true
EOF
chmod 600 "$LAB/server.env"
```

| Setting | Why the lab needs it |
|---|---|
| `MACHINARCHY_ENVIRONMENT=development` | Development defaults; among them, hosts with key login trust their SSH keys on first contact unless a profile says otherwise |
| `MACHINARCHY_ENCRYPTION_SECRET` | Encrypts secrets and command output at rest. **Keep it**: a new one makes the stored data unreadable |
| `MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD` | Creates the first owner, `admin`, on an empty database |
| `MACHINARCHY_SSH_ALLOW_LOOPBACK`, `MACHINARCHY_SSH_ALLOWED_PORTS` | The egress policy refuses loopback and non-22 ports by default; the lab's hosts are both |
| `MACHINARCHY_EXECUTION_ENABLED`, `…_ALLOWED_PROGRAMS` | Running commands is off by default, and only listed commands may run. Each entry is a program and its allowed arguments: `uptime` takes none, `sleep {int}` one number, `df -h {path}` one absolute path, and `sh -c *` lets step 11 run a script (an interpreter runs anything, so a real deployment lists narrow commands; see [execution policy](../operations.md#execution-policy)). The quotes keep the spaces when the file is sourced |
| `MACHINARCHY_API_DOCS_ENABLED` | Serves the API reference at `/scalar` and `/openapi.json` (see [docs/api](../api/README.md)) |

Start the server in **its own terminal** and leave it running (Ctrl-C stops it):

```sh
set -a; . "$LAB/server.env"; set +a
machinarchy-monolith
```

> **With `make`, pass the lab settings explicitly:**
> `make run-monolith ENV_FILE="$LAB/server.env"`. A plain `make run-monolith`
> loads the repository's `.env` instead and ignores what you exported, so the
> server uses another database and another admin password, and the login in
> step 3 fails with `Invalid credentials`.

Back in your main terminal:

```console
$ curl -s http://127.0.0.1:8080/healthz/ready
{"checks":{"control-manager":"ready","database":"ready"},"status":"ready"}
```

---

## 3. Log in

`machinarchy login` asks the server for a session and stores it in the CLI
configuration (`~/.config/machinarchy/config.yaml`, mode 0600). The first
login names the server and the context to create:

```console
$ machinarchy login --server http://127.0.0.1:8080 --context lab --username admin
Password:
Logged in to http://127.0.0.1:8080 as admin; the session is stored for user "lab" of context "lab".
```

The password prompt does not echo. In a script, pass it on stdin instead:
`… login --username admin --password-stdin < password-file`.

A session lasts 24 hours (the default `MACHINARCHY_SESSION_TTL`). When it
expires, run `machinarchy login --username admin` again: it renews the
session of the current context. For day-to-day use, create an API token
instead (step 16).

---

## 4. Contexts

A **context** is a server (*cluster*) plus a credential (*user*), like
kubectl's. The first login created one called `lab` and made it current:

```console
$ machinarchy config get-contexts
CURRENT  NAME  CLUSTER  USER  ENVIRONMENT
*        lab   lab      lab   
```

The CLI refuses to send a token over plain `http://` to anything but
loopback; use `https://` for a real server. Step 16 adds a second context
with an API token.

---

## 5. Store the lab's key and password as secrets

Secrets are write-only: you can create, rotate and delete them, never read
them back. The lab needs two: the client key for `ssh-key`, and the password
for the other two (`tr -d "\n"` drops the file's trailing newline):

```console
$ machinarchy secret create lab-key -o json --value-stdin --description "data/.ssh/test_id_ed25519" < data/.ssh/test_id_ed25519 | jq "{id, name, purpose, currentVersion}"
{
  "id": "ccf383fb-e59d-4ba3-8901-41b56200a018",
  "name": "lab-key",
  "purpose": "ssh-credential",
  "currentVersion": 1
}

$ tr -d "\n" < data/.ssh/testuser.password | machinarchy secret create lab-password -o json --value-stdin --description "data/.ssh/testuser.password" | jq "{id, name, purpose, currentVersion}"
{
  "id": "ef1fadb4-6891-410d-95e8-45b8e6398aa7",
  "name": "lab-password",
  "purpose": "ssh-credential",
  "currentVersion": 1
}

$ machinarchy secret list -o json | jq -r ".[] | [.name, .id, .currentVersion] | @tsv"
lab-key	ccf383fb-e59d-4ba3-8901-41b56200a018	1
lab-password	ef1fadb4-6891-410d-95e8-45b8e6398aa7	1
```

Without `-o json` the secret commands print a table, never a value. They
need an owner's login session; an API token is refused (step 16).

---

## 6. Create Access Profiles

An **Access Profile** says how to log in to a host: the user, the
authentication method, which secret, and how to trust the host's SSH keys.
Hosts point at a profile, so one secret rotation updates them all.

Profiles are owner-only. `--secret` takes the secret's name and uses its
current version:

| Profile | Login | `--host-key-policy` | Effect |
|---|---|---|---|
| `lab-key` | private key | `tofu` | Keys seen on first contact are pinned automatically |
| `lab-password` | password | not set | Needs an owner's approval: Machinarchy never trusts keys on first contact for a password login, because an impostor would receive the password |

```console
$ machinarchy create profile lab-key --connector ssh --auth private-key --username testuser --secret lab-key --host-key-policy tofu --description "key login, keys pinned on first contact"
Access Profile "lab-key" created: 933390bb-6c27-406e-bd6d-e7633309ae77.

$ machinarchy create profile lab-password --connector ssh --username testuser --secret lab-password --description "password login; keys need approval"
Access Profile "lab-password" created: 3bc2e785-f244-4a20-91a6-f07fcee56293.

$ machinarchy get profiles
NAME          ID                                    REVISION  DESCRIPTION
lab-password  3bc2e785-f244-4a20-91a6-f07fcee56293  1         password login; keys need approval
lab-key       933390bb-6c27-406e-bd6d-e7633309ae77  1         key login, keys pinned on first contact
```

Other profile options (`--host-key-policy ca` with host certificates,
`--supervisor`, and `execution.maxOutputFileBytes` through the API) are described in
[operations](../operations.md#ssh-host-keys) and
[detached execution](../operations.md#detached-execution).

---

## 7. Register the hosts

Labels (`--label KEY=VALUE`, repeatable) let you select hosts later. No
fingerprint is needed:

```console
$ machinarchy create host ssh-key --endpoint 127.0.0.1:2222 --profile lab-key --label tier=web --label auth=key
NAME     ID                                    ENVIRONMENT  ENDPOINT        PHASE    READY    REASON
ssh-key  55ad1e17-d9b1-4dd8-8d9f-1d31b3751560  -            127.0.0.1:2222  Pending  Unknown  NotObserved

$ machinarchy create host ssh-password --endpoint 127.0.0.1:2221 --profile lab-password --label tier=web --label auth=password
NAME          ID                                    ENVIRONMENT  ENDPOINT        PHASE    READY    REASON
ssh-password  f6f2d7cb-84be-40a7-af7d-2df9191fb8a9  -            127.0.0.1:2221  Pending  Unknown  NotObserved

$ machinarchy create host ssh-hostcheck --endpoint 127.0.0.1:2223 --profile lab-password --label tier=db --label auth=password
NAME           ID                                    ENVIRONMENT  ENDPOINT        PHASE    READY    REASON
ssh-hostcheck  fcf37d66-7774-4848-9c2a-6d9aef2c9c5b  -            127.0.0.1:2223  Pending  Unknown  NotObserved
```

A second later the controller has checked them:

```console
$ machinarchy get hosts
NAME           ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY  REASON
ssh-key        55ad1e17-d9b1-4dd8-8d9f-1d31b3751560  -            127.0.0.1:2222  Active  True   SSHVerified
ssh-password   f6f2d7cb-84be-40a7-af7d-2df9191fb8a9  -            127.0.0.1:2221  Active  False  HostKeyPendingApproval
ssh-hostcheck  fcf37d66-7774-4848-9c2a-6d9aef2c9c5b  -            127.0.0.1:2223  Active  False  HostKeyPendingApproval
```

`REASON` says why a host is not ready. The JSON (or YAML) view has the whole
condition, for scripts:

```console
$ machinarchy get hosts -o json | jq -r '.[] | [.metadata.name, (.status.conditions[] | select(.type=="Ready") | .reason)] | @tsv'
ssh-key	SSHVerified
ssh-password	HostKeyPendingApproval
ssh-hostcheck	HostKeyPendingApproval
```

`ssh-key` pinned its keys and logged in. The two password hosts are waiting
for you.

---

## 8. Trust host keys

See the keys the controller observed for a waiting host:

```console
$ machinarchy get hosts ssh-password -o yaml | sed -n "/observedHostKeys/,/^[a-z]/p"
  observedHostKeys:
    - algorithm: ssh-ed25519
      fingerprint: SHA256:Tq2Qo6CW1E0cHKD6o2Qb0ekZhnk3Q3/z6o09d+VoXKk
      firstSeenAt: 2026-11-19T13:32:44.780712Z
      lastSeenAt: 2026-11-19T13:32:44.780712Z
    - algorithm: ecdsa-sha2-nistp256
      fingerprint: SHA256:D1Je05PeWOkVrP/z9nMu+Sf98+KjzGn1mkIq8eB+Lc4
      firstSeenAt: 2026-11-19T13:32:44.780712Z
      lastSeenAt: 2026-11-19T13:32:44.780712Z
    - algorithm: ssh-rsa
      fingerprint: SHA256:gISGmJRc/K/ExVEK6rzJm4EYyP2w5tuWuut7vaBCbAM
      firstSeenAt: 2026-11-19T13:32:44.780712Z
      lastSeenAt: 2026-11-19T13:32:44.780712Z
```

On a real network, compare these with the host's own keys (`ssh-keygen -lf
/etc/ssh/ssh_host_*_key.pub` on its console) before approving. Then approve
one host (`machinarchy trust host ssh-password`) or every waiting host at
once:

```console
$ machinarchy trust host --all-pending --yes
SSH host keys to trust:
  ssh-password (127.0.0.1:2221):
    SHA256:Tq2Qo6CW1E0cHKD6o2Qb0ekZhnk3Q3/z6o09d+VoXKk
    SHA256:D1Je05PeWOkVrP/z9nMu+Sf98+KjzGn1mkIq8eB+Lc4
    SHA256:gISGmJRc/K/ExVEK6rzJm4EYyP2w5tuWuut7vaBCbAM
  ssh-hostcheck (127.0.0.1:2223):
    SHA256:EYt0QO2qbu1xJtXr0PXuMKHiXQEPbaW8nERq9io6rCA
    SHA256:+vtzFjVvhN+gu3loD/oZ/Yl+KweOgU066XLNMyHy4oM
    SHA256:nnNU8fHP7Dr2brpoG7YQHxyWIlEHA4Le5yVBjkfTKyM
Host "ssh-password" now trusts 3 host key(s).
Host "ssh-hostcheck" now trusts 3 host key(s).

$ machinarchy get hosts -o json | jq -r '.[] | [.metadata.name, (.status.conditions[] | select(.type=="Ready") | .reason)] | @tsv'
ssh-key	SSHVerified
ssh-password	SSHVerified
ssh-hostcheck	SSHVerified
```

Without `--yes` the CLI shows the same list and asks first.
`--all-pending --selector tier=db` narrows it to matching hosts.

A full host record:

```console
$ machinarchy get hosts ssh-key -o yaml
metadata:
  id: 55ad1e17-d9b1-4dd8-8d9f-1d31b3751560
  name: ssh-key
  labels:
    auth: key
    tier: web
  generation: 2
  revision: 4
  createdAt: 2026-11-19T13:32:43.644469Z
  updatedAt: 2026-11-19T13:32:45.833374Z
spec:
  presence: Managed
  accessProfileId: 933390bb-6c27-406e-bd6d-e7633309ae77
  accessTarget:
    endpoint: 127.0.0.1:2222
    trust:
      - SHA256:Ehd3zpE8ZzPW9qanet+DNIO9adfUFQ2WV5U6Di441e4
      - SHA256:N+XdeAI8ToEzIqHlGm6uQa6OvByAPqR41XSEIUqApEc
      - SHA256:7efWR23MN1hqSRUL49ztROE1RxXc9GS1Tc6QXefw9UA
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
HOST          STATE      EXIT  OUTPUT
ssh-key       Succeeded  0     13:32:47 up 14:14,  0 users,  load average: 1.34, 0.41, 0.27
ssh-password  Succeeded  0     13:32:47 up 14:14,  0 users,  load average: 1.34, 0.41, 0.27
OPERATION                             STATE
506ef18e-d7e7-4d27-8d4c-fa1d36ef9a62  Succeeded
```

The table shows only the **first line** of output; `logs` shows all of it:

```console
$ machinarchy run --host ssh-hostcheck --wait --yes -- df -h /
HOST           STATE      EXIT  OUTPUT
ssh-hostcheck  Succeeded  0     Filesystem      Size  Used Avail Use% Mounted on
OPERATION                             STATE
fb5f6266-e37c-40c3-ae33-dc140d85343e  Succeeded

$ machinarchy logs operation fb5f6266-e37c-40c3-ae33-dc140d85343e
Filesystem      Size  Used Avail Use% Mounted on
overlay          59G   11G   45G  20% /
```

Without `--yes`, `run` lists the hosts it selected and asks first; it then runs
on exactly the hosts it showed you, even if labels change in between.
Selecting hosts: `-l SELECTOR` (`tier=web`, `tier=web,auth in (key,password)`),
`--host NAME` (repeatable), `--all`, and `--exclude NAME`.

---

## 10. Run detached and follow the output

Without `--wait`, a run is **detached**: each host starts the command under a
supervisor and the SSH session ends at once. The command keeps running even if
Machinarchy restarts, and may last up to 24 hours (`--timeout`, in seconds).

```console
$ machinarchy run --all --yes -- sh -c "for i in 1 2 3; do echo step \$i on \$(hostname); sleep 2; done; echo done >&2"
Operation f48d85da-7461-41ba-a730-d87ad3d1d71d submitted (detached).
Follow it with:  machinarchy logs operation f48d85da-7461-41ba-a730-d87ad3d1d71d --follow
Status:          machinarchy get operation f48d85da-7461-41ba-a730-d87ad3d1d71d
```

Follow one host's output while it runs. `--follow` ends when the command does;
new lines appear every few seconds (the controller polls each host every 5 s,
less often for long runs):

```console
$ machinarchy logs operation f48d85da-7461-41ba-a730-d87ad3d1d71d --host ssh-key --follow
step 1 on machinarchy-ssh-key
step 2 on machinarchy-ssh-key
step 3 on machinarchy-ssh-key
```

Without `--host`, `logs` prints every host in turn (without `--follow`):

```console
$ machinarchy logs operation f48d85da-7461-41ba-a730-d87ad3d1d71d
==> ssh-hostcheck (Succeeded) <==
step 1 on machinarchy-ssh-hostcheck
step 2 on machinarchy-ssh-hostcheck
step 3 on machinarchy-ssh-hostcheck

==> ssh-key (Succeeded) <==
step 1 on machinarchy-ssh-key
step 2 on machinarchy-ssh-key
step 3 on machinarchy-ssh-key

==> ssh-password (Succeeded) <==
step 1 on machinarchy-ssh-password
step 2 on machinarchy-ssh-password
step 3 on machinarchy-ssh-password

$ machinarchy logs operation f48d85da-7461-41ba-a730-d87ad3d1d71d --host ssh-hostcheck --stream stderr
done
```

The summary, and the full record:

```console
$ machinarchy get operation f48d85da-7461-41ba-a730-d87ad3d1d71d
HOST           STATE      EXIT  OUTPUT
ssh-hostcheck  Succeeded  0     step 1 on machinarchy-ssh-hostcheck
ssh-key        Succeeded  0     step 1 on machinarchy-ssh-key
ssh-password   Succeeded  0     step 1 on machinarchy-ssh-password
OPERATION                             STATE
f48d85da-7461-41ba-a730-d87ad3d1d71d  Succeeded

$ machinarchy get operation f48d85da-7461-41ba-a730-d87ad3d1d71d -o yaml | sed -n '/targets:/,$p' | head -20
  targets:
    - spec:
        hostId: fcf37d66-7774-4848-9c2a-6d9aef2c9c5b
        displayName: ssh-hostcheck
      state: Succeeded
      result:
        exitCode: 0
        stdout: |
          step 1 on machinarchy-ssh-hostcheck
          step 2 on machinarchy-ssh-hostcheck
          step 3 on machinarchy-ssh-hostcheck
        stderr: |
          done
        stdoutBytes: 108
        stderrBytes: 5
      progress:
        stdoutBytes: 108
        stderrBytes: 5
        lastContactAt: 2026-11-19T13:32:57.272415Z
        polls: 2
```

A long output keeps its first and last 15 KiB per stream; `stdoutBytes` is the
full size and `truncated: true` marks a cut. Output is visible only to users
allowed to run commands; readers see states and exit codes.

---

## 11. Labels and selectors

```console
$ machinarchy label host ssh-hostcheck role=primary
NAME           ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY  REASON
ssh-hostcheck  fcf37d66-7774-4848-9c2a-6d9aef2c9c5b  -            127.0.0.1:2223  Active  True   SSHVerified

$ machinarchy get hosts -l role=primary
NAME           ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY  REASON
ssh-hostcheck  fcf37d66-7774-4848-9c2a-6d9aef2c9c5b  -            127.0.0.1:2223  Active  True   SSHVerified

$ machinarchy get hosts -l "auth in (key)"
NAME     ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY  REASON
ssh-key  55ad1e17-d9b1-4dd8-8d9f-1d31b3751560  -            127.0.0.1:2222  Active  True   SSHVerified

$ machinarchy label host ssh-hostcheck role-
NAME           ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY  REASON
ssh-hostcheck  fcf37d66-7774-4848-9c2a-6d9aef2c9c5b  -            127.0.0.1:2223  Active  True   SSHVerified
```

`KEY=VALUE` sets a label and `KEY-` removes it. The table does not show labels;
`get hosts NAME -o yaml` does.

---

## 12. Cancel a run

```console
$ machinarchy run -l tier=web --yes -- sleep 300
Operation 62409861-bbd0-45b5-a903-255668dad428 submitted (detached).
Follow it with:  machinarchy logs operation 62409861-bbd0-45b5-a903-255668dad428 --follow
Status:          machinarchy get operation 62409861-bbd0-45b5-a903-255668dad428

$ machinarchy cancel operation 62409861-bbd0-45b5-a903-255668dad428
Cancellation requested for operation 62409861-bbd0-45b5-a903-255668dad428 (CancelRequested).

$ machinarchy get operation 62409861-bbd0-45b5-a903-255668dad428
HOST          STATE      EXIT  OUTPUT
ssh-key       Cancelled  143   cancelled
ssh-password  Cancelled  143   cancelled
OPERATION                             STATE
62409861-bbd0-45b5-a903-255668dad428  Cancelled
```

Cancel sends SIGTERM to the command's whole process group, then SIGKILL after
10 seconds; exit 143 is 128 + SIGTERM. Nothing is left on the host:

```console
$ docker exec machinarchy-ssh-test-ssh-key-1 pgrep -fx "sleep 300" || echo "no sleep 300 left on ssh-key"
no sleep 300 left on ssh-key
```

---

## 13. Long runs, timeouts and `Lost`

`--timeout` is per host, in seconds: up to 86400 (24 h) for a detached run,
900 with `--wait`. A command still running at its timeout is stopped on the
host and ends `TimedOut`.

If a host restarts during a run, nobody can know whether the command
finished, so Machinarchy says so instead of guessing:

```console
$ machinarchy run --host ssh-key --timeout 3600 --yes -- sleep 600
Operation 401c6402-6d2a-441c-a0e4-1e657f3b825a submitted (detached).
Follow it with:  machinarchy logs operation 401c6402-6d2a-441c-a0e4-1e657f3b825a --follow
Status:          machinarchy get operation 401c6402-6d2a-441c-a0e4-1e657f3b825a

$ make ssh-servers-restart SSH_SERVICE=ssh-key
docker compose --project-name "machinarchy-ssh-test" -f "deployments/testing/ssh-servers.docker-compose.yml" restart ssh-key
 Container machinarchy-ssh-test-ssh-key-1  Restarting
 Container machinarchy-ssh-test-ssh-key-1  Started

$ machinarchy get operation 401c6402-6d2a-441c-a0e4-1e657f3b825a
HOST     STATE  EXIT  OUTPUT
ssh-key  Lost   -     supervisor_lost
OPERATION                             STATE
401c6402-6d2a-441c-a0e4-1e657f3b825a  Failed
```

`Lost` is never retried automatically: check the host, then run again if
needed. Other causes are listed in the
[runbook](../runbook.md#a-detached-target-is-lost).

---

## 14. When a host presents other keys

A host whose SSH keys differ from the trusted ones has been reinstalled, or
someone is in the middle. The lab's servers keep their host keys, so simulate
it: register a host at `ssh-hostcheck`'s address, but trust `ssh-key`'s keys:

```console
$ KEYS=($(machinarchy get hosts ssh-key -o json | jq -r '.spec.accessTarget.trust[] | "--trust", .')); echo "${KEYS[@]}" | cut -c1-80
--trust SHA256:Ehd3zpE8ZzPW9qanet+DNIO9adfUFQ2WV5U6Di441e4 --trust SHA256:N+XdeA

$ machinarchy create host moved --endpoint 127.0.0.1:2223 --profile lab-password "${KEYS[@]}"
NAME   ID                                    ENVIRONMENT  ENDPOINT        PHASE    READY    REASON
moved  e695c14b-354a-4522-8987-bc7eda4f9eac  -            127.0.0.1:2223  Pending  Unknown  NotObserved

$ machinarchy get hosts moved
NAME   ID                                    ENVIRONMENT  ENDPOINT        PHASE   READY  REASON
moved  e695c14b-354a-4522-8987-bc7eda4f9eac  -            127.0.0.1:2223  Active  False  HostKeyChanged

$ machinarchy get hosts moved -o json | jq -r '.status.conditions[] | select(.type=="Ready") | .reason + ": " + .message'
HostKeyChanged: the host presents SHA256:EYt0QO2qbu1xJtXr0PXuMKHiXQEPbaW8nERq9io6rCA, SHA256:+vtzFjVvhN+gu3loD/oZ/Yl+KweOgU066XLNMyHy4oM, SHA256:nnNU8fHP7Dr2brpoG7YQHxyWIlEHA4Le5yVBjkfTKyM; trusted: SHA256:Ehd3zpE8ZzPW9qanet+DNIO9adfUFQ2WV5U6Di441e4, SHA256:N+XdeAI8ToEzIqHlGm6uQa6OvByAPqR41XSEIUqApEc, SHA256:7efWR23MN1hqSRUL49ztROE1RxXc9GS1Tc6QXefw9UA. Nothing was sent to it. If the change is expected: machinarchy trust host moved --replace
```

`KEYS` is an array so that each `--trust` and fingerprint is its own argument
in both bash and zsh (zsh, the macOS default, does not split an unquoted
`$KEYS` on spaces, and the CLI would get one long unknown flag).

Machinarchy did not log in: the password was never sent. Commands are refused
too:

```console
$ machinarchy run --host moved --wait --yes -- hostname
HOST   STATE   EXIT  OUTPUT
moved  Failed  -     remote_execution_failed
OPERATION                             STATE
66b5b097-08a9-4cfd-808c-c3671aeffc3a  Failed
Error: operation 66b5b097-08a9-4cfd-808c-c3671aeffc3a finished Failed
```

A changed key is never accepted automatically. Once you have checked the new
keys out of band, replace the old ones:

```console
$ machinarchy trust host moved --replace --yes
SSH host keys to trust:
  moved (127.0.0.1:2223):
    SHA256:EYt0QO2qbu1xJtXr0PXuMKHiXQEPbaW8nERq9io6rCA
    SHA256:+vtzFjVvhN+gu3loD/oZ/Yl+KweOgU066XLNMyHy4oM
    SHA256:nnNU8fHP7Dr2brpoG7YQHxyWIlEHA4Le5yVBjkfTKyM
    replacing SHA256:Ehd3zpE8ZzPW9qanet+DNIO9adfUFQ2WV5U6Di441e4, SHA256:N+XdeAI8ToEzIqHlGm6uQa6OvByAPqR41XSEIUqApEc, SHA256:7efWR23MN1hqSRUL49ztROE1RxXc9GS1Tc6QXefw9UA
Host "moved" now trusts 3 host key(s).

$ machinarchy run --host moved --wait --yes -- hostname
HOST   STATE      EXIT  OUTPUT
moved  Succeeded  0     machinarchy-ssh-hostcheck
OPERATION                             STATE
33d4edba-c698-4978-82e1-c7e94539ac69  Succeeded

$ machinarchy delete host moved --yes
Host "moved" deleted.
```

A host that was already trusted is re-checked every 5 minutes, so a real
reinstall shows `HostKeyChanged` within that time (see "Known problems").

---

## 15. History

```console
$ machinarchy get operations --limit 8
ID                                    STATE      TARGETS      CREATED
33d4edba-c698-4978-82e1-c7e94539ac69  Succeeded  Succeeded=1  2026-11-19 15:33:26
66b5b097-08a9-4cfd-808c-c3671aeffc3a  Failed     Failed=1     2026-11-19 15:33:24
401c6402-6d2a-441c-a0e4-1e657f3b825a  Failed     Lost=1       2026-11-19 15:33:10
62409861-bbd0-45b5-a903-255668dad428  Cancelled  Cancelled=2  2026-11-19 15:33:01
f48d85da-7461-41ba-a730-d87ad3d1d71d  Succeeded  Succeeded=3  2026-11-19 15:32:49
fb5f6266-e37c-40c3-ae33-dc140d85343e  Succeeded  Succeeded=1  2026-11-19 15:32:48
506ef18e-d7e7-4d27-8d4c-fa1d36ef9a62  Succeeded  Succeeded=2  2026-11-19 15:32:47
```

`--state Succeeded` (or `Failed`, `Running`, …) filters the list.

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
$ machinarchy create token lab-cli --role operator --expires-hours 720
API token "lab-cli" (id 1, role operator) created. It is shown only now:
mch_pat_seLK…
```

The token is shown **once**. To store it without it ever reaching your
screen, create it with `-q` (prints the token alone) and pipe it into a
second CLI user, then add a context for that user:

```console
$ machinarchy create token lab-cli-2 --role operator --expires-hours 720 -q | machinarchy config set-credentials lab-operator --token-stdin
Credentials "lab-operator" configured.

$ machinarchy config set-context lab-ops --cluster lab --user lab-operator
Context "lab-ops" configured.

$ machinarchy config use-context lab-ops
Switched to context "lab-ops".

$ machinarchy config get-contexts
CURRENT  NAME     CLUSTER  USER          ENVIRONMENT
         lab      lab      lab           
*        lab-ops  lab      lab-operator  

$ machinarchy secret list
Error: API returned HTTP 403: permission denied (request ID IssupaPSPGpSdQQHnNwQiQzDPmCnovif)
Hint: The credential is valid but is not permitted to do this.
Commands that manage runtime configuration or secrets need a human owner session; API tokens (mch_pat_...) are refused for them.

$ machinarchy --context lab secret list -o json | jq -r ".[].name"
lab-key
lab-password
```

`--context NAME` runs one command with another context without switching: the
owner context `lab` lists the two secrets from step 5.

`machinarchy get tokens` lists the tokens (never their values) and
`machinarchy delete token ID` revokes one.

---

## 17. Troubleshooting

Real errors from this lab and what they mean:

| You see | Meaning | Fix |
|---|---|---|
| `Error: unknown hosts: nope` | No host by that name or ID | `machinarchy get hosts` |
| `… [y/N]: Error: confirmation required` | No terminal to ask in (a script) | Add `--yes` |
| `Error: this operation has 3 targets; pick one with --host (ssh-hostcheck, ssh-key, ssh-password)` | `logs --follow` follows one host | Add `--host NAME` |
| `HTTP 403 … API tokens (mch_pat_...) are refused for them` | Owner-only command with an API token | `--context` with the owner's login |
| `READY False` | See the reason: `get hosts NAME -o yaml` | `HostKeyPendingApproval`: step 8. `HostKeyChanged`: step 14. Others: [runbook](../runbook.md#hosts-never-become-ready) |
| `Failed` / `remote_execution_failed` or `launch_host_key_rejected` | SSH failed (host key, login, network) | Check the host's `READY` reason |
| `Lost` / `supervisor_lost` | The host restarted during a detached run | Step 13 |

**Known problems** (recorded in AUDIT/13C, to be fixed in a later phase):

- **The allowed list is read from `server.env` only when the database is
  new.** Afterwards an owner changes it in the runtime configuration; see
  [execution policy](../operations.md#execution-policy). A refused command
  explains itself:

  ```console
  $ machinarchy run --host ssh-key --wait --yes -- whoami
  Error: submit operation: API returned HTTP 403: command execution is not permitted: program "whoami" is not on the allowed list (request ID …)
  Hint: The server's execution policy does not allow this command. An owner can add a matching entry,
  such as "systemctl is-active {unit}", to operations.allowedPrograms with PUT /api/v1/runtime-config.
  ```
- **A trusted host whose keys change keeps `READY True` for up to 5 minutes**,
  until the controller's next periodic check. Commands to it fail safely
  meanwhile (`remote_execution_failed`), and `trust host --replace` reports
  that no new keys were observed yet. Restarting the server checks every host
  at once.

---

## 18. Clean up

```console
$ machinarchy delete host ssh-key --yes && machinarchy delete host ssh-password --yes && machinarchy delete host ssh-hostcheck --yes
Host "ssh-key" deleted.
Host "ssh-password" deleted.
Host "ssh-hostcheck" deleted.
```

Stop the server with Ctrl-C in its terminal, then stop the SSH servers. Their
keys and host identities under `data/.ssh/` are kept for next time:

```console
$ make ssh-servers-down
docker compose --project-name "machinarchy-ssh-test" -f "deployments/testing/ssh-servers.docker-compose.yml" down
…
 Container machinarchy-ssh-test-ssh-key-1  Removed
 Container machinarchy-ssh-test-ssh-hostcheck-1  Removed
 Container machinarchy-ssh-test-ssh-password-1  Removed
 Network machinarchy-ssh-test_testing-net  Removed
```

When you are done with the lab, `rm -rf "$LAB"` removes its database and CLI
config. `delete host` is refused while an operation still targets the host;
cancel it first.

---

## Cheat sheet

| Task | Command |
|---|---|
| Lab SSH servers | `make ssh-servers-up` / `ssh-servers-ps` / `ssh-servers-down` |
| Log in / renew the session | `machinarchy login [--server URL --context NAME] --username USER` |
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
| Profiles / API tokens | `machinarchy create profile …` / `create token NAME --role operator` |

More: [command reference](commands.md), [operations](../operations.md),
[runbook](../runbook.md), [API reference](../api/README.md),
[SSH test servers](../../deployments/testing/README.md).
