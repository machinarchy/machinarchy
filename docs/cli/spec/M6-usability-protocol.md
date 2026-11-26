# Beginner test protocol (A-01, release-blocking)

Status: **not run.** This file is the protocol and the recording sheet; no participant has done it yet. Results go in `M6-usability.md` under a new dated heading, one block per participant, with nothing left out.

## Participants
Two people who have never used Machinarchy and are not on the project. Comfortable with a terminal (can run `curl`, `ssh`), not necessarily developers of this product. Record role and terminal experience, not names.

## Setup (done by the facilitator before the person arrives)
- Clean user profile or machine: no `~/.config/machinarchy`, no `MACHINARCHY_*` variables, no `ssh` known_hosts entry for the lab host.
- A controller already running and allowing the lab target (the SSH lab container `web-01`), with one owner account. Give the person only: the server URL, the owner login, and the SSH host address and password. Nothing else.
- The released CLI binary installed as the README describes, or placed on `PATH` if the install step is not under test (say which).
- Screen and terminal recording on; a stopwatch the facilitator runs.

## Task wording (read aloud, verbatim, nothing added)
"Using only this terminal and what the tool tells you, run `uptime` on the machine at `<address>`. You may use `--help`. I will not help or answer questions; say out loud what you are thinking."

## Facilitator rules
- Do not hint, point, or read the screen. Allowed: "What are you thinking?" Never "try ...".
- Stop at success (an uptime line from the host), at 15 minutes, or when the person gives up.
- Intervene only for setup faults (broken lab); record each as a setup fault, not a CLI finding, and pause the clock.

## Record per participant
1. Total time, start (first command) to the uptime line. A-01 passes if under 5 minutes, unaided.
2. A timeline: each command typed, verbatim; the output shown; seconds spent before the next command.
3. Every hesitation of more than 10 seconds, every wrong command, every `--help` use, every time they left the terminal (browser, docs), every message re-read, and every question asked aloud. Write what they said.
4. Whether they followed `Next:` lines, and where a `Next:` line was missing.
5. Whether they understood: which fingerprint they were comparing (F6), what `no env` meant (F9), what happened when the host key was left untrusted (F7).
6. Exit questions (ask after): "What was the most confusing moment?" "What did you think the closing line meant?" (F10) "Would you know what to do tomorrow?"

## Pass / fail
- A-01 passes only if both participants reach the uptime line unaided in under 5 minutes.
- Any failure, or any single point where both people stumble, is a finding: fix the CLI or amend the spec, then re-run with a new participant. A fix is not validated by re-running with the same person.
- Report every finding, including ones that look like the person's fault. Do not edit the test to fit the result.
