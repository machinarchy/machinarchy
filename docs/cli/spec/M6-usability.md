# M6 usability session (step 6.8)

**Status: NOT the human fresh-user test.** This is a scripted walkthrough by Claude (an AI that knows the repository), run on 2026-11-26. It checks that the printed guidance leads to a working result. It cannot measure confusion, hesitation or time-to-understand, so A-01's "person who has never used it, under 5 minutes, no docs" criterion is **not measured**. A human session is still owed.

## Setup
- CLI built from commit 5a00fe6 (`make build`); clean `HOME` (no config, no `MACHINARCHY_*` variables).
- Fresh throwaway controller on :8081, new empty SQLite database, bootstrap owner `admin`. The existing :8080 controller and its data were not touched.
- Lab SSH server `machinarchy-ssh-test-ssh-key-1` (`make ssh-servers-up`) at `127.0.0.1:2222`, user `testuser`, key `data/.ssh/test_id_ed25519`.
- Driven through a pty, answering only what each prompt asked. The fingerprint was compared with `ssh-keygen -lf` inside the lab container (ED25519 matched).

## Timeline (wall clock of the driver, includes 2.5 s idle detection per prompt)
| Step | Command | Result |
|---|---|---|
| 1 | `machinarchy` | `Not signed in.` `Next: machinarchy login`, exit 3 |
| 2 | `login` (URL, name, 1, `admin`, password, `Y`) | signed in, device token saved, ~17 s, `Next: machinarchy whoami` |
| 3 | `machinarchy` | `You have no machines yet.` `Next: machinarchy hosts add` |
| 4 | `hosts add` (name, address, 1, user, key path, `Y`, 1) | host registered; key prompt reached, ~24 s |
| 5 | `hosts trust web-01` (`Ehd3zpE8`) | `Key trusted.` |
| 6 | `run --on web-01 -- uptime` (`y`) | `OK 1 of 1 hosts succeeded`, uptime line, exit 0 |

The flow reached A-01's expected end state. Exit codes were 0 except step 1 (3, as designed).

## Findings (none hidden)
- **F1 (blocker for a first lab run): loopback and non-22 ports are refused by default, and the message does not say so.** With the default controller settings, `hosts add 127.0.0.1:2222` ends with `WARN web-01 added, but the server cannot connect to it. Check that 127.0.0.1:2222 is reachable from the server and that the login works.` `hosts show` says `cannot connect (ConnectionFailed)`. The real cause is the target policy (`MACHINARCHY_SSH_ALLOW_LOOPBACK`, `MACHINARCHY_SSH_ALLOWED_PORTS`, both default-deny). A plain `ssh` to the same host worked. The controller log shows nothing at info level. `docs/cli/user-full-guide.md` section 2 does not mention these settings. **Fixed after the session** (see ChangeLogAI 2026-11-26 F1): the controller now reports reason `TargetNotAllowed` with a message naming the four `MACHINARCHY_SSH_*` settings, the CLI shows it as `cannot connect`, and the lab guide lists the two settings. Re-checked live on a default-policy controller. Worked after starting the controller with both settings.
- **F2: after the warning the CLI still printed a no-questions repeat command** (`hosts add ... --no-input`) that would fail the same way.
- **F3: A-01 wording.** The key prompt says `It is sent to your server over HTTPS`; the server was `http://localhost:8081`. Inaccurate for loopback HTTP.
- **F4: `login` said `Role: not reported by this server`**, while `whoami` showed `Role: operator`. Inconsistent.
- **F5: `whoami` prints no `Next:` line** after login pointed the user at it, so the printed trail ends there; the user must run bare `machinarchy` to continue. Acceptable only if bare `machinarchy` is the documented home.
- **F6: the host key prompt lists three fingerprints** (ECDSA, ED25519, RSA) without saying which is which; the user must guess which `ssh-keygen` line to compare. Typing 8 characters of any one trusts the set.
- **F7: the `Next:` line after `hosts add`** pointed at `ssh-keygen` on the machine, then the run left the trust step running only because the driver kept the prompt open; when the session was closed at the prompt, the host stayed registered and untrusted. `hosts trust` finished it, but nothing in that output named it as the way back. Worth a `Next: machinarchy hosts trust web-01` line on an unanswered prompt.

- **F8 (fixed 2026-11-26, owner decision): the 8-character prefix does not scale.** Trusting 100 hosts meant 100 typed prefixes. A tester also pasted the whole fingerprint and was refused with "that is not the start of the key". First-key trust now asks for `yes` (Enter or `no` skips); `--yes` still never approves; a changed key (`--replace`) keeps the typed prefix. The risk accepted: `yes` is easier to type without comparing the key. Bulk approval (`--all-pending` in one step) was not added.

## Verdict against A-01
Not measured for time or comprehension. Mechanically: the commands and prompts work in the expected order, the fingerprint matched, and the run succeeded. Two points need follow-up before a human session is worth running: F1 (blocks a fresh lab) and F7.

## Spec changes
None made. F1 may need a note in `03-ux.md` about the failure text for policy-denied targets; left for the owner.

## Owner session, 2026-11-26 (partial record)
Run by the project owner, who knows Machinarchy, on a fresh controller (:8081, new SQLite database, lab policy settings) and the SSH lab. This is **not** a fresh-user test.
- Reached the A-01 end state: `run --on web-01 -- uptime` -> `1 of 1 hosts succeeded`, exit 0, uptime line; `run r-9c1e182c --host web-01` replayed the output.
- Total time, who else (if anyone) tried it, and the exact outputs of login, add and trust were **not recorded**. A-01's under-5-minutes criterion is **unmeasured**.
- Problems met on the way (all setup, not CLI guidance): the controller first ran on the existing database because the SQLite variable is `MACHINARCHY_DATABASE_PATH`, not `MACHINARCHY_DATABASE_URL`, so login failed with `Invalid credentials`; then the controller refused `/tmp/m6-lab` until `chmod 700`; both errors are clear but only found with help.
- **F9:** the run header shows `[localhost-8081 · none]` and `Hosts  no env 1`. No environment was selected, yet the run went ahead. A first-time reader may not know what `no env` means or whether it matters. Needs a decision: either name it ("hosts outside any environment") or require `envs use` first.
- **F10:** the closing line `The machines complied.` is brand voice (`07-brand.md`) and was not checked for whether a new user finds it clear or odd.

## Still owed
A real session with a person who has not used Machinarchy, following the CLI's own guidance on a clean machine, on a controller already allowing the lab targets (or with F1 fixed).
