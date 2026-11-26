# Machinarchy CLI: full user guide (a lab with three SSH servers)

This guide takes you from nothing to running commands on managed hosts, using
the repository's SSH test servers ([`deployments/testing`](../../deployments/testing/README.md)).
It lists commands and what to expect; it contains **no recorded output**, because
output carries IDs, fingerprints and times that differ on every machine. Each
command prints its own next step: when in doubt, follow it. About 30 minutes.

Lines are commands to type. Run them from the repository root, and keep one
terminal for the server (step 2) and one for everything else. Replace
`machinarchy` with `go run ./cmd/machinarchy` if you have not installed it
([install](../../README.md#install-the-cli)).

## 1. Start the lab servers

```sh
make ssh-servers-up
```

## 2. Start Machinarchy

```sh
cp .env.example .env
make run-monolith
```

Set `MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD` in `.env` before the first start; it
creates the owner account (`admin`). The server listens on
`http://localhost:8080`.

## 3. Sign in and create an environment

```sh
machinarchy login http://localhost:8080 --name local
machinarchy whoami
machinarchy envs add lab
machinarchy envs use lab
```

`login` asks for the password without echo and offers a 90-day device token so
you do not type it every day. Owner-only commands (`access add`, `hosts trust`,
`tokens`) use the password session; everything else uses the saved token.

## 4. Say how Machinarchy reaches the machines

The lab user is `testuser`; the setup script generates a key at `data/.ssh/test_id_ed25519` and a password file at `data/.ssh/testuser.password`:

```sh
machinarchy access add lab-key --how ssh-key --user testuser --key-file data/.ssh/test_id_ed25519
machinarchy access
machinarchy access show lab-key
```

The key is stored encrypted on the server and is never printed again. For the
password login, pipe the password in:

```sh
tr -d '\n' < data/.ssh/testuser.password | machinarchy access add lab-pw --how ssh-password --user testuser --password-stdin
```

## 5. Add the hosts and approve their keys

```sh
machinarchy hosts add ssh-key 127.0.0.1:2222 --access lab-key --tag tier=web
machinarchy hosts add ssh-password 127.0.0.1:2221 --access lab-pw --tag tier=web
machinarchy hosts add ssh-hostcheck 127.0.0.1:2223 --access lab-pw --tag tier=db
machinarchy hosts
machinarchy hosts show ssh-key
machinarchy hosts trust ssh-key
```

`hosts add` registers the host and waits for the server to see its SSH key.
`hosts trust` shows that key; you type the first 8 characters of the
fingerprint after `SHA256:`, compared with the key on the machine. `--yes`
never answers this. Repeat `hosts trust` for the other two hosts. For a lab only, an access entry created with
`--host-key-policy tofu` trusts the first key it sees.

## 6. Run commands

```sh
machinarchy run --on ssh-key -- uptime
machinarchy run --on all -- hostname
machinarchy run --on tier=web -- cat /etc/os-release
```

You see the hosts first and confirm; hosts that print the same are one line in
the result. The exit code is 0 (all succeeded), 8 (some failed) or 9 (all
failed). A program is an argument list, not a shell string.

## 7. Long runs, cancel, retry

```sh
machinarchy run --on all --detach -- sleep 120
machinarchy run
machinarchy run r-1a2b3c4d --follow
machinarchy run r-1a2b3c4d --cancel
machinarchy run r-1a2b3c4d --retry-failed
machinarchy run r-1a2b3c4d --host ssh-key --follow
```

Use the run ID printed by the first command (`r-` and 8 characters). Ctrl+C
while watching only stops the watching; the run keeps going.

## 8. A key changes

If a host presents a different key, `hosts` shows `key changed` and runs
refuse it. Check the new key on the machine, then:

```sh
machinarchy hosts trust ssh-key --replace
```

You type the first 8 characters of the new key and the host's name.

## 9. Day-to-day token and clean up

```sh
machinarchy tokens create daily --role operator --expires 30d --out daily.token
machinarchy tokens
machinarchy tokens revoke daily
machinarchy hosts remove ssh-key
machinarchy hosts remove ssh-password
machinarchy hosts remove ssh-hostcheck
machinarchy access remove lab-key
machinarchy access remove lab-pw
machinarchy envs remove lab
machinarchy logout
make ssh-servers-down
```

`hosts remove` takes the host out of the inventory only; it does not touch the
machine. Removing prompts you to type the name.

More: [commands](commands.md), [CI and scripts](ci.md),
[troubleshooting](troubleshooting.md), [error messages](messages.md).
