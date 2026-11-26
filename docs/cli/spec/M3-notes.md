# M3 implementation notes

What was built for milestone M3 (envs, access and hosts), and where it differs from the spec. The spec files stay as the design record.

## Built

| Spec item | Where |
|---|---|
| `envs` list/add/use/remove, local protected guard | `internal/cli/commands/envs.go` |
| `access` list/show/add/remove, secret creation, host-key policy wording | `commands/accessentries.go` |
| `hosts` list, `show`, `tag`, `move` (refuses), `remove` | `commands/hostsgroup.go` |
| `hosts trust` (typed prefix, `--replace`, `--fingerprint`, `--all-pending`) | `commands/hoststrust.go` |
| guided `hosts add` (SSH) | `commands/hostsadd.go` |
| agent path: `hosts add --agent`, `hosts enroll`, `hosts revoke-agent` | `commands/hostsagent.go` |
| API client for envs, access, hosts, agents | `internal/cli/api/inventory.go`, `access.go`, `managed.go` |
| Dynamic tab completion (host, access and env names; `--env`, `--access`) | `internal/cli/completion.go`, `commands/completion.go` |
| Old commands removed | step 3.8: `get hosts`, `get profiles`, `create host|profile|agent-token`, `delete host|profile|agent-token`, `label host` (`trust host` went in 3.5) |

## Deviations from the spec

1. **No aliases for the removed commands** (project rule: no legacy). They fail as unknown commands (exit 2).
2. **`get agent-tokens` stays** (owner-only), because `hosts revoke-agent` needs an agent ID and the server does not yet say which agent belongs to a host (C-09). `get operations`, `cancel operation` and `logs operation` stay until M4.
3. **Protected envs are a local guard** (C-02 not accepted by the controller): see `commands.md`.
4. **`hosts move` always refuses** and `--env` on `hosts add` is refused (C-01).
5. **Agent install**: the blob is a base64 agent.env fragment; the blob holds the server URL, controller key and token only (ADR-059, step 8.5; the earlier broker address/credential lines and flags are gone); no hosted `install.sh` exists yet.
6. **Step 3.7b (auto-approve, generated names, `hosts rename`) is deferred** by the owner; nothing of it was built, so `04-security.md` still describes B5/B10 as `--yes never approves a key`.
7. **Completion** asks the server with the saved credential, has a 3 s limit, never prompts and prints no errors. Names with whitespace or control characters are never offered. Positional host arguments after the first are not completed.
8. **`docs/cli/user-full-guide.md` still shows the removed commands.** It has a banner saying so; the rewrite is step 6.4.
9. **`scripts/smoke.sh`** and the Makefile example were updated to the new commands (approved per file).
10. The controller's condition messages still say `machinarchy trust host ...` (`internal/hostaccess/reconciler.go`); a controller change that needs the owner.

## Not done / left

- Tests against a real SSH-container lab and a real agent (`machinarchy-agent`) were not run; the agent path is checked against fakes only.
- Keychain backend (Q5), 200-target cap (Q4) and Windows (Q6) are unchanged.
