# Machinarchy CLI in scripts and CI

Nothing is saved on a CI machine: pass the server and a token in the
environment.

```sh
export MACHINARCHY_SERVER=https://machinarchy.example.com
export MACHINARCHY_TOKEN="$(cat /run/secrets/machinarchy-token)"   # from your CI secret store
export MACHINARCHY_NO_INPUT=1
machinarchy hosts --status attention -o json
machinarchy run --on tier=web --yes -- systemctl is-active nginx
```

- **Token:** create one with `machinarchy tokens create NAME --role operator --expires 90d --out FILE` (an owner session is needed). A token is never a flag; the environment variable or stdin carries it. API tokens are never owners, so owner-only commands (`hosts trust`, `access add`, `tokens`) are not available in CI.
- **No prompts:** `--no-input` (or `MACHINARCHY_NO_INPUT=1`) makes any command that would ask exit 2 and name the flag it needs. `--yes` confirms a run; it never approves host keys or typed-name confirmations. A protected env needs `--confirm-env NAME`.
- **Exit codes:** 0 ok, 1 unexpected failure, 2 usage, 3 not signed in or not allowed, 4 server or network problem, 5 wait deadline elapsed, 6 not found, 7 conflict, 8 some hosts failed, 9 all hosts failed, 130 interrupted. Test `$?` against 8 and 9 to tell partial from total failure.
- **Output:** data goes to stdout, messages to stderr. `-o json|yaml` keep their field names (see [commands](commands.md#output-compatibility)); with `-o json` an error is a JSON object on stderr. `-q` prints only identifiers.
- **Previews:** `run --dry-run` shows the hosts a run would touch and sends nothing.
- **Detached runs:** `machinarchy run --on tier=web --detach -- ./deploy.sh` returns the run ID; later `machinarchy run ID` shows the result.

More in [commands](commands.md#scripts-and-ci) and [configuration](configuration.md).
