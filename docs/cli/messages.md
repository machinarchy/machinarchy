# CLI messages

<!-- Generated from internal/cli/msg by `go test ./internal/cli/msg -run TestMessagesDoc -update`. Do not edit. -->

Every error and warning the CLI prints, with the reason and the next step. `{name}` is replaced by a value when the message is shown.

## Errors

### `auth.logout_unconfirmed`

Signed out locally, but the server could not be told: {why}.

*Why:* The session may still be active on the server.

*Next:*

```text
machinarchy login
machinarchy logout
```

### `confirm.required`

This needs confirmation, and --yes is not enough here ({what}).

*Next:*

```text
{next}
```

### `input.required`

{flag} is required when nothing can be asked.

*Why:* {why}

*Next:*

```text
{next}
```

## Warnings

### `auth.device_token_failed`

Could not create a device token, so you stay signed in for this session only.

*Why:* {why}

*Next:*

```text
machinarchy login
```

### `auth.logged_out_token`

The saved token was removed from this computer. It still works until it expires or is revoked.

*Next:*

```text
{next}
```

### `host.added_no_connect`

{host} added, but the server cannot connect to it{reason}.

*Why:* Check that {endpoint} is reachable from the server and that the login works.

*Next:*

```text
machinarchy hosts show {host}
```

### `host.added_waiting_key`

{host} added. It is waiting for key approval: nothing was trusted.

*Why:* Trust the key only after you compared it with the one on the machine.

*Next:*

```text
{next}
```

### `run.cancel_unreached`

{hosts} could not be reached while cancelling; the command may still be running there.

### `run.drift`

{deviant} hosts printed something different from the other {majority}.

### `run.lost_contact`

Lost contact with the server; the run keeps going.

*Next:*

```text
machinarchy run {id} --follow
```

### `run.partial`

{ok} of {total} hosts succeeded, {failed} did not

*Next:*

```text
machinarchy run {id} --retry-failed
```

### `run.submit_interrupted`

Interrupted while sending the run. It may or may not have been created.

*Next:*

```text
machinarchy run
```

### `run.submit_lost`

The connection ended while sending the run. It may or may not have been created.

*Why:* {why}

*Next:*

```text
machinarchy run
```

### `run.unknown`

A command on a "timed out" or "outcome unknown" host may still have run.
