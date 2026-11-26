# ChangeLogAI

## 2026-11-26 — Phase 10 closing check: authorization matrix entry for `/api/v1/version` (branch ai/p10-controller-gaps)

- **Fixed:** `TestRouteAuthorizationMatrix` failed because the new public `GET /api/v1/version` (10.4) had no matrix entry; added it as `public` in `internal/bootstrap/authz_matrix_test.go`.
- **Checks run:** `go test -race ./...` (full): everything passes except `test/e2e` `TestAgentEnrolsAndRunsCapabilitiesThroughMachinarchy` and `TestControllerSigningKeyRotation`; `internal/bootstrap` passes after the fix.
- **Known failure, not fixed:** the two e2e tests fail because the `machinarchy-agent` binary used rejects `MACHINARCHY_AGENT_NATS_URL`, `_NATS_CREDENTIALS_FILE` and `_NAME` ("unsupported settings are set"), i.e. the agent build does not match this controller's transport. Not caused by Phase 10 changes; needs an agent-side or test-harness decision. Not run: Postgres tests.

## 2026-11-26 — C-05/C-06: operation target cap decided as final at 200 (branch ai/p10-controller-gaps)

- **Decision (owner, option A):** `MaxOperationTargets` stays 200; C-06 (`summary` and `targets` endpoints) is not built. No controller behaviour changed. A raise would need C-06 and a 500/5,000-host load test; no such lab exists.
- **Changed:** matrix C-05/C-06, `docs/cli/commands.md` wording, a comment in `runutil.go`; new test `TestOperationTargetCapIsFinal` (200 accepted, 201 refused). The CLI refusal at 201 hosts was already tested (`runs_test.go`).
- **Checks run:** gofmt, `go vet`, `go test -race ./internal/domain/resource/ ./internal/cli/...`. Not run: Postgres tests, smoke, e2e, any load test (no lab).

## 2026-11-26 — C-11: server version and API range at `GET /api/v1/version` (branch ai/p10-controller-gaps)

- **Implemented (controller, additive):** public `GET /api/v1/version` (no auth, registered with the health routes) returns `{"version","apiMin","apiMax"}`; the range constants `buildinfo.APIMin/APIMax` (both 1) are new. No `X-Machinarchy-Version` header (owner-approved shape). `docs/api/swagger.*` regenerated.
- **Implemented (CLI):** only after a response fails to decode, the CLI asks the endpoint without a token; if the server's range does not overlap the CLI's it exits 4 with `incompatible server: this server speaks API version X (release R); this CLI supports Y`. A compatible, missing (404), unreachable or malformed answer keeps the previous wrong-shape / not-JSON messages; the server's release string is stripped to printable ASCII, 32 characters. Docs: `docs/cli/commands.md` ("Supported servers"), `06-api-matrix.md`.
- **Limitations:** compatible-range skew that still decodes is not detected; the check is not run on every call. K-04 "old commands keep working" stays unimplemented (no-legacy rule).
- **Tests:** `TestVersionEndpointIsPublicAndReportsRange`, `/api/v1/version` in the operational-routes table, `TestDecodeFailureNamesIncompatibleServer` (incompatible, compatible, 404, garbled; no token sent; control characters removed).
- **Checks run:** gofmt, `go build ./...`, `go vet`, `golangci-lint` on touched packages (0 issues), `go test -race ./internal/cli/... ./internal/transport/http/... ./cmd/...` pass, `make api-docs`, `make smoke`. Not run: whole-repo suite, Postgres tests, `test/e2e`.

## 2026-11-26 — C-03: `GET /api/v1/me` reports role, auth method and token details (branch ai/p10-controller-gaps)

- **Implemented (controller, additive):** `/api/v1/me` now also returns `role` (effective role), `authMethod` (`session` or `api_token`) and, for an API token, `tokenId`, `tokenName` and `expiresAt` (looked up in the caller's own tokens; left out if the lookup fails). No secret or hash is returned. A session returns no expiry. `docs/api/swagger.*` regenerated.
- **Implemented (CLI):** `whoami` (table, json, yaml) and `login --with-token` show the server's role, token name and expiry; what the server reports wins over what the CLI saved. An older server still gets "not reported by this server" / "Expires: unknown".
- **Limitations:** a session's expiry is still read from the saved credential or the session list. The `whoami` schema golden did not change (it is generated against an old-style server).
- **Tests:** `TestGetMeReportsRoleAndTokenDetails` (session, token, other tokens and hash absent, no lookup), `TestWhoamiShowsWhatTheServerReports`, `TestWhoamiOnAnOlderServerSaysNotReported`.
- **Checks run:** gofmt, `go vet ./...`, `golangci-lint run ./...` (0 issues), `go test -race` on `internal/cli/...`, `internal/transport/...`, `internal/domain/...`, `cmd/...`, `make smoke` pass. Not run: the whole repo suite, Postgres tests (no store change), `test/e2e`.

## 2026-11-26 — C-02: protected environments held by the server (branch ai/p10-controller-gaps)

- **Implemented (controller, additive API):** migration `000003_environment_protected` (SQLite and PostgreSQL) adds `environments.protected` (default false). `POST/PUT /api/v1/environments` take an optional `protected` (a `PUT` without it keeps the stored value; anyone with inventory write may protect, only the workspace owner may unprotect, else 403); environments are returned with `protected`. A change of the value is audited in the same transaction (`environment.protection_changed`, target `environment`). Guarded calls: run submit (by ids, rule or `--on all`), host update, move (source and target env), trust, and delete refuse with HTTP 409 `protected_environment_confirmation_required` (message lists the env names) unless the request carries `confirmEnvironment` naming every protected env touched, by exact name, for every principal including API tokens and `curl`. The field is a string or a list in the body; for `DELETE /managed/hosts/{id}` it is a repeated query parameter. Enforcement is in `ManagedResourceService` (`internal/service/protected_environments.go`) with the names carried in the context (`domain.WithConfirmedEnvironments`). `docs/api/swagger.*` regenerated.
- **Implemented (CLI):** the CLI reads `protected` from the server; `envs` shows `yes` (server) or `yes (local)` (old server); `envs add --protected` sends it and needs no local-guard consent when the server reports protection (an old server keeps the local guard and its warning); host commands and runs treat a server-protected env like the local guard (typed name or `--confirm-env`); the confirmed names are sent as `confirmEnvironment`; `--confirm-env` is repeatable; the 409 has its own Why/Next. `InventoryClient.CreateEnvironment` takes `protected`. Docs: `docs/cli/commands.md`, `04-security.md`, `06-api-matrix.md`.
- **Limitations:** host label changes (`PATCH .../labels`), environment rename and the agent-side/automatic host writes are not guarded. A host change not covered by the listed calls (for example host creation into a protected env) is not guarded. Existing scripts that mutate hosts of an env after it is protected now get 409. Old-server detection is "some environment reports `protected`".
- **Tests:** `TestProtectedEnvironmentsAreEnforcedByTheServer` (service: no/wrong/inexact name, two envs, bulk `All`, move in and out, trust, delete, reader checked first), `TestProtectedEnvironmentOverHTTP` (409 code, string and list), `TestUnprotectingAnEnvironmentIsOwnerOnly`, store contract test for the column and its audit events, CLI tests (`protectedenv_test.go`, API client confirm fields). Migration count test updated.
- **Checks run:** gofmt, `go vet ./...`, `golangci-lint run ./...` (0 issues), `go test -race ./internal/... ./cmd/... ./pkg/...` pass, `go test ./internal/cli/...` pass, PostgreSQL store/service/transport tests via `scripts/db/test-postgres.sh` pass, `make smoke` pass. Not run: `test/e2e` (known unrelated agent NATS-settings failure, see the C-01 entry), the HA suite.

## 2026-11-26 — C-01: every host belongs to an environment; `hosts add --env`, `hosts move` (branch ai/p10-controller-gaps)

- **Implemented (controller, additive API):** migration `000002_default_environment` (SQLite and PostgreSQL) creates an environment `default` in every workspace and moves hosts without one into it. `POST /api/v1/managed/hosts` and `PUT /api/v1/managed/hosts/{id}` accept an optional `environmentId`: on create, absent means `default`; on update, absent means unchanged, and a changed ID moves the host (inventory write is enough, like labels) as its own revision before the spec write. An unknown ID is 422 `invalid_request`; a stale revision is still 409. There is no way to clear an environment. `DELETE /api/v1/environments/{id}` is 409 while the environment has hosts; `default` cannot be deleted or renamed (409), its description can change. New service methods `CreateHostInEnvironment` and `UpdateHostInEnvironment` (the old ones use `default`/unchanged). `docs/api/swagger.*` regenerated.
- **Implemented (CLI):** `hosts add` (SSH and `--agent`) sends the active env (`--env` or `envs use`; none = server default); `hosts move NAME --env ENV` works, with the local protected-env confirmation for source and target; a server that ignores the env (older) is reported after create/move instead of silently succeeding. New `api.Client.MoveHost`. Docs: `docs/cli/commands.md`, `06-api-matrix.md`.
- **Deviations / limitations:** no "which env?" wizard question (the active env is used; the plan wanted the question restored). The domain still accepts environment 0 (legacy rows, tests); the service always resolves it to `default`, and the column stays nullable. A move plus spec change is two writes: a failure in between leaves the host moved with its old spec. A concurrent host create can still hit the FK when its environment is deleted (the delete check is not in the same transaction). Existing databases pick the change up with the new migration; a workspace created by raw SQL after migration has no `default` until one is added.
- **Tests:** `TestHostEnvironments` (service + SQLite: default, explicit, unknown, move, reader, stale revision, delete/rename guards), `TestHostEnvironmentOverHTTP`, `TestHostsAddEnvironment`, `TestHostsMove` (incl. old server); store contract tests updated for the `default` environment and the second migration.
- **Checks run:** gofmt, `go build ./...`, `go vet ./...`, `golangci-lint run ./...` (0 issues), `go test -race` on every package except `test/e2e` (pass; Postgres contract tests skipped: no `MACHINARCHY_TEST_POSTGRES_URL`). `test/e2e` agent tests FAIL: the installed agent binary rejects the old `MACHINARCHY_AGENT_NATS_*` settings the e2e harness sets (agent/Phase 8 mismatch, unrelated to this change; not fixed here). Not run: `make smoke`, the PostgreSQL migration.

## 2026-11-26 — Owner decisions: scorecard command count and keychain (branch ai/p8-gateway-transport)

- **Decided by the owner (docs only):** the scorecard target "10 top-level commands" counts the commands shown in the default `--help`, not Cobra's own `help`; today 14 are listed, and the plan is to remove `get` and group `secret` and `runtime-config` under one `admin` entry (not implemented yet, see PLAN.md step 9.1). Q5: the keychain backend does not ship in V1; credentials stay in the `0600` file and the limits are documented. Edited `07-brand.md` (scorecard row), `05-architecture.md` 4.4, `10-roadmap.md` Q5.
- **Not done:** the command changes themselves; no code changed. **Checks run:** none (docs only).

## 2026-11-26 — Small CLI findings F7.3-1..3, F4, F2 fixed (branch ai/p8-gateway-transport)

- **Implemented:** F7.3-1..3: new `internal/cli/removed.go` holds a table of the removed old commands and flags (`get|create|delete|label|trust|cancel|logs ...`, `config set-*`, `run -l/--all/--exclude/--selector`, `login --context`, `config set-credentials --token`). Using one is refused with exit 2 and `Error: "X" was removed`, with the replacement as the `Next:` line (for example `logs operation 1` now gives `machinarchy run ID --follow`, not `login`). Still no aliases and nothing is sent; unknown words that are not in the table keep the "closest command" suggestion. F4: when `login` also saves a device token it no longer says `Role: not reported by this server` (the device-token line states the role `whoami` shows); a session-only login still says it. F2: `hosts add` prints the repeat-without-questions command only when the host ended ready or waiting for its key, not after a "cannot connect" or "still checking" warning.
- **Tests:** `TestRemovedFormsNameTheReplacement` (7 forms), `TestOldTrustHostCommandIsGone` updated to expect the replacement. No test for the F4 login wording or the F2 twin suppression (not run).
- **Checks run:** gofmt, `go vet`, `golangci-lint run ./internal/cli/...` (0 issues), `go test -race ./internal/cli/... ./cmd/...` pass. Not run: `go test ./...`, `make smoke`, pty.
- **Limitation:** the removed-form match is by words anywhere in the arguments, so it only runs when Cobra already failed on the line; the table must be kept in step with removals.

## 2026-11-26 — Beginner test protocol added; test NOT run (branch ai/p8-gateway-transport)

- **Implemented:** `docs/cli/spec/M6-usability-protocol.md`: participants, clean setup, verbatim task, facilitator rules, what to record, pass/fail. Documentation only.
- **Not done:** the beginner test itself (A-01, release-blocking). It needs two real people who have never used Machinarchy; it cannot be done or simulated by Claude. A-01's time and comprehension stay unmeasured.
- **Checks run:** none (docs only).

## 2026-11-26 — Usability findings F3, F5, F6, F7, F9 fixed before the fresh-user test (branch ai/p8-gateway-transport)

- **Implemented:** F7: leaving the host-key prompt of `hosts add` (end of input, interrupt, wrong answer) prints `Next:  machinarchy hosts trust NAME`. F9: a run preview that reaches hosts outside any environment adds a line explaining `no env` (decision: explain, not require `envs use`, because hosts cannot be moved into an env until C-01). F3: the credential-send note says "over plain HTTP (not encrypted in transit)" on an `http://` server and "over HTTPS" otherwise. F5: `whoami` (table output only, not `-o json|yaml`) ends with `Next:  machinarchy`. F6: each fingerprint in the trust prompt shows its key type, e.g. `(ssh-ed25519)`.
- **Tests:** whoami Next assertion, `TestHostsTrustLabelsKeyType`, `TestSendNoteMatchesScheme`. No test for the F7 Next line or the F9 note (not run against a real prompt/run).
- **Checks run:** gofmt, `go vet`, `golangci-lint run ./internal/cli/...` (0 issues), `go test -race ./internal/cli/... ./cmd/...` pass. Not run: `go test ./...`, `make smoke`, pty.

## 2026-11-26 — Phase 8 step 8.6: RabbitMQ bus adapter spike (branch ai/p8-gateway-transport)

- **Implemented (spike, not wired):** `internal/agentgateway/bus/rabbitmq` implements `bus.Bus` on AMQP 0-9-1 (`rabbitmq/amqp091-go` v1.17.0, new dependency, confined to that package; `imports_test.go` now enforces it). One durable classic queue per agent with publisher confirms; results arrive on a topic exchange whose routing key carries the agent identity; `Next` uses a channel per delivery, so nack, ack and the ack wait (default 30s) all return unacknowledged commands to the queue in order; a dropped connection is re-established. The `bustest` contract passes on it unchanged. RabbitMQ was chosen over Kafka: a queue per agent with per-message ack and requeue fits the contract, a Kafka topic or partition per agent does not.
- **Not done / known limitations:** not selectable from config or `internal/bootstrap` (no config key; the spike only proves the interface). Idempotent publish is an in-process window, so it is not shared between controller replicas (NATS dedupes in the broker); production would need the broker-side deduplication plugin or a store. Classic queues only (no quorum queues, so no replicated HA); no Register/Renew handlers (the gRPC path carries them).
- **Checks run:** gofmt, `go vet` and `golangci-lint run ./internal/agentgateway/...` (0 issues), `go test -race ./internal/agentgateway/...` pass. The RabbitMQ contract ran against `rabbitmq:4` in Docker (both suites, all subtests pass, including redelivery after ack wait); it was not re-run after the final lint-only edit to the connection-closed branch (the package tests were, and skip without a broker). The test skips unless `MACHINARCHY_TEST_AMQP_URL` is set. Not run: Postgres tests, `make smoke`, e2e/HA.

## 2026-11-26 — Phase 8 step 8.5: CLI prints server URL + token only (branch ai/p8-gateway-transport)

- **Implemented:** `hosts enroll` and `hosts add --agent` build the install blob from the active server URL (`MACHINARCHY_AGENT_SERVER_URL`), the controller public key and the one-time token; no broker value, and the "not reported by this server" broker lines and install step are gone. `--nats-url`, `--nats-creds-file` and `--remember` are removed without aliases: they stay as hidden flags only so that using one is refused (exit 2, nothing sent) with the replacement named. `-o json|yaml` output gains `serverUrl`. Config fields `agent-nats-url`/`agent-bootstrap-creds` and their store methods are deleted (an old config file's unknown keys are ignored as before). Docs: `docs/cli/commands.md`, `M3-notes.md`, `06-api-matrix.md` (C-07 without broker fields).
- **Tests:** replaced the broker-flag tests with a blob-content test (server URL, token, keys, no broker text, token not repeated on stderr) and a refusal test for the three retired flags on both commands.
- **Checks run:** gofmt, `go vet`, `golangci-lint run ./internal/cli/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` pass, `make smoke` pass. Not run: A-02 against a live controller and agent, Postgres tests.
- **Known limitation:** the URL in the blob is the CLI's API server URL; the gateway's gRPC listener is separate in 8.3 (dedicated port) so a deployment must route it behind the same URL.

## 2026-11-26 — Phase 8 step 8.4 (controller side): real-agent compatibility test (branch ai/p8-gateway-transport)

- **Implemented:** `internal/agentgateway/server/compat_test.go`, test only, no production change. `TestRealAgentBinaryAgainstGateway` runs the `machinarchy-agent` binary built from the sibling repository against the real gateway and gRPC service of 8.3 (memory bus): the agent enrols with only a server URL, token and controller key, runs a command and returns its result, then after a restart without a token receives a command that was queued while it was down. It skips unless `MACHINARCHY_AGENT_BIN` names the binary.
- **Agent side** (repo `machinarchy-agent`, same step): gRPC client transport, NATS removed; see its `ChangeLogAI.md`.
- **Checks run:** the compat test passes with `-race` against the agent built at the 8.4 commit; `go vet` and `golangci-lint` on `internal/agentgateway/server` (see the final report). Not run: Postgres, `make smoke`, full suite beyond the gateway packages.
- **Known limitations:** the test uses plaintext loopback gRPC (TLS verification is covered in the agent repository's tests); CI does not yet build the agent binary to run it.

## 2026-11-26 — Phase 8 step 8.3: agent-facing gRPC service with lease credentials (branch ai/p8-gateway-transport)

- **Implemented:** new `internal/agentgateway/server` (gRPC `machinarchy.agent.v1.AgentGateway`: `Register`, `Renew`, `SendResult` unary and a bidirectional `Commands` stream). Bodies are the existing signed `agentv1` envelopes carried as raw bytes (custom codec, no protobuf schema). Stream identity comes from a signed lease-bound open request (renewal-shaped, current lease ID, clock skew checked), so a copied lease ID alone does nothing; results need a live lease and the agent ID in the signed body must verify against that agent's key. Commands are sent one at a time and acknowledged on the stream (unacknowledged or dropped streams redeliver, order kept); a revoked agent's stream is closed and an expired lease is refused. A new stream for the same agent replaces the old one. Limits: 512 KiB messages, 10000 open streams, per-client and per-agent rate limits.
- **Supporting changes:** `bus.Bus` gained `Next` (implemented by the NATS and in-memory adapters; the contract suite also runs through it); gateway gained transport entry points in `transport.go`; config keys `MACHINARCHY_AGENT_GATEWAY_GRPC_ADDR`, `_GRPC_CERT_FILE`, `_GRPC_KEY_FILE` (TLS required in production, off when unset); `internal/bootstrap/agentgrpc.go` serves it as a worker on its own listener; `google.golang.org/grpc` is now a direct dependency (already in the module graph); `docs/operations.md`.
- **Deviations:** (1) served on a dedicated listener, not on the API port, because the API server's write timeout would cut long-lived streams and it terminates no TLS itself; (2) the service definition lives in the controller package, not in `machinarchy-agent/protocol/agentv1` (that repo is not touched; 8.4 mirrors it); (3) a request that gets no answer on NATS gets a generic gRPC `InvalidArgument`/`Unauthenticated` here; (4) the lease credential is the existing lease ID plus the agent-key signature, not a new token type.
- **Known limitations:** the NATS agent path, per-agent NATS JWTs and `MACHINARCHY_AGENT_GATEWAY_NATS_*` remain until the agent client (8.4) and CLI (8.5) land; a captured stream-open request can be replayed within the 2 minute skew window by someone who can also break TLS; no real-agent compatibility run (needs 8.4).
- **Tests added:** `internal/agentgateway/server` (e2e with a fake agent over gRPC; offline queue and redelivery; reused, expired, revoked and wrong-hostname tokens; wrong lease, stale request, copied lease with another key, expired lease; revocation closes an open stream; agent A never receives B's commands or writes B's results; oversize message, flood, result rate, stream bound and replacement; no token or lease in logs), config tests for the gRPC settings, bus contract via `Bus.Next`.
- **Checks run:** gofmt, `go vet ./...`, `golangci-lint run` on agentgateway, bootstrap and config (0 issues), gosec on agentgateway and bootstrap (no findings), `go test -race -count=1 ./...` (all packages pass, including `test/e2e` and `test/ha`). Not run: Postgres store tests, `make smoke`, real agent, multi-node HA over gRPC.

## 2026-11-26 — Phase 8 step 8.2: agent gateway uses a `Bus` interface with a NATS adapter (branch ai/p8-gateway-transport)

- **Refactor, behavior unchanged:** `internal/agentgateway` no longer imports nats-io (except `natsjwt.go`). New `bus` package (`Bus`: Serve, EnsureAgent, Publish, Ready), `bus/nats` (the moved JetStream code: stream, per-agent durable consumer AckWait 30s / MaxDeliver 20, publish with message-ID dedupe, inbox prefix, result subject to agent identity), `bus/memory` (in-process bus) and `bus/bustest` (shared contract). `Config.NATSURL`/`NATSOptions` are replaced by `Config.Bus`; `internal/bootstrap/agents.go` builds the NATS adapter. Register, renew and result handling is now transport-neutral byte handlers in the gateway (the base for the gRPC front in 8.3).
- **Deviations:** the interface lives in `internal/agentgateway/bus` (not `agentgateway/bus.go`) to avoid an import cycle with the adapter; no adapter config key was added because there is only one adapter. The NATS adapter does not test ack-timeout redelivery (fixed 30s); the in-memory bus does.
- **Tests added:** contract suite run against the in-memory bus and the NATS adapter (order, offline queue, isolation, idempotent message ID, nack redelivery, result identity), plus a test that nats-io is imported only in `bus/nats` and `natsjwt.go`.
- **Checks run:** `go build ./...`, `go vet ./...`, `golangci-lint run` on agentgateway and bootstrap (0 issues), `go test -race -count=1 ./internal/agentgateway/...`, `go test -race ./test/e2e/...`, `go test -race ./test/ha/...` and `go test ./internal/...` pass. Not run: Postgres store tests, real agent, multi-node HA beyond the harness.

## 2026-11-26 — Phase 8 step 8.1: ADR-059 gateway-only agent transport (branch ai/p8-gateway-transport)

- **Docs only (planned, not implemented):** added `docs/adr/ADR-059-agent-gateway-transport.md` (status Accepted, owner approved) with the owner's answers to Q7-Q10 (no legacy NATS agents, gRPC, current delivery guarantees kept, owner tags the protocol release), the transport-neutral flow, a kept/changed/dropped table of current guarantees and a threat list. Linked from `docs/adr/README.md` and `docs/architecture.md`.
- **Known limitation:** the agent-repo spec (`agentv1/doc.go`, `transport.md`) is not written; that repo had uncommitted changes that are not part of this work. No code changed.
- **Checks run:** none applicable (docs only; no doc or link checker in the repo was run).

## 2026-11-26 — CLI Phase 7 final review report (branch ai/p7-final-review)

- **Docs only:** added `docs/cli/spec/M7-review.md`: verification, audit re-check, breaking changes, security evidence, A-01 and scorecard status, open findings (F7.3-1..3, F3, F9), controller work (C-01, C-02, C-03, C-05/06, C-07, C-09, C-11), next steps. No code changed; A-01 time and fresh-user comprehension remain unmeasured.
- **Checks run:** in this phase gofmt, build, vet, golangci-lint, `go test -race ./...` (7.1) and the security tests and fuzz seeds under `-race` (7.4) pass. Not run: Postgres tests, pty layers, SSH lab automation, real agent, live HA, Windows.

## 2026-11-26 — CLI M6 step 6.9: scorecard re-measured; stale unreachable-server hint fixed (branch ai/p6-automation-docs-hardening)

- **Behavior changed:** the "cannot reach the API server" hint named `machinarchy config get-contexts`, a command removed in M2. It now says `machinarchy whoami` shows the server and `--server` changes it (`internal/cli/api/client.go`, test in `client_test.go`).
- **Docs:** `docs/cli/spec/M6-notes.md` section 6.9: each scorecard row with command output or test names. Targets missed or unmeasured: 14 top-level commands vs 10, no typo suggestion at depth 2, no `Why:` on the unreachable error, two concepts (server, environment), A-01 time unmeasured. Spec targets were not changed.
- **Checks run:** gofmt, `go vet ./...`, `golangci-lint run ./...` (0 issues), `go test -race -count=1 ./...`, `make smoke` all pass. Not run: Postgres tests, pty layers, SSH lab automation, real agent, Windows.

## 2026-11-26 — CLI M6 step 6.8: owner session recorded (branch ai/p6-automation-docs-hardening)

- **Docs only:** `docs/cli/spec/M6-usability.md` gets a partial record of the owner's lab run (login to `uptime` on web-01 succeeded) and findings F9 (`no env` in the run header with no environment selected) and F10 (brand line unverified). Time and exact outputs were not recorded; A-01 stays unmeasured and the human fresh-user session is still owed. No code changed; no checks run.

## 2026-11-26 — CLI M6 finding F8: first host key is trusted by typing yes (branch ai/p6-automation-docs-hardening)

- **Behavior changed (owner decision):** `hosts trust` and `hosts add` on a terminal now trust a first SSH host key when the person types `yes` or `y` (Enter or `no` skips). Before, the first 8 characters of the fingerprint were required, which does not scale to many hosts. Unchanged: the `--yes` flag never approves a key; without a terminal `--fingerprint` or `--trust-fingerprint` is still needed; a changed key (`--replace`) still needs the typed prefix and the host name. Security trade-off: typing `yes` is easier to do without comparing the key.
- **Areas:** `internal/cli/commands/{hoststrust,hostsadd}.go`, their tests, `docs/cli/{README,commands}.md`, `docs/cli/spec/{02,03,04,07,09,M6-usability}`.
- **Checks run:** gofmt, `go vet`, `go test -race` for `./internal/cli/... ./cmd/...` pass; golangci-lint on `./internal/cli/...` 0 issues. Not run: full suite, smoke, a real-terminal session. `04-security.md` was edited for this decision; the owner should re-read it.

## 2026-11-26 — CLI M6 finding F1: name the network policy when a host target is denied (branch ai/p6-automation-docs-hardening)

- **Behavior changed:** when the controller's SSH target policy denies an address or port, the host's Ready condition now has reason `TargetNotAllowed` and a message naming `MACHINARCHY_SSH_ALLOW_LOOPBACK`, `_ALLOW_PRIVATE`, `_ALLOWED_CIDRS` and `_ALLOWED_PORTS`. Before, it was a bare `ConnectionFailed`. The CLI lists such a host as "cannot connect". Areas: `internal/hostaccess/reconciler.go`, `internal/cli/commands/hostsgroup.go`, `docs/cli/user-full-guide.md`, `docs/cli/spec/{02-commands,06-api-matrix,M6-usability}.md`.
- **Tests:** `failure_reason_test.go` (new), `TestHostsListMapsReadiness` extended. Checks run: gofmt, `go vet` and `go test -race -count=1` for `./internal/hostaccess/... ./internal/cli/...` pass; live check on a default-policy controller shows the new message.
- **Not run:** golangci-lint, full suite, Postgres, API docs/OpenAPI check for the new reason string. Other findings F2-F7 are unchanged.

## 2026-11-26 — CLI M6 step 6.8: scripted walkthrough (branch ai/p6-automation-docs-hardening)

- **Docs only:** `docs/cli/spec/M6-usability.md`. A scripted run by Claude of the A-01 flow on a throwaway controller and the SSH lab: login, hosts add, key trust, run all work. Seven findings listed; F1 (loopback/ports denied by default, message does not say so) blocks a fresh lab run.
- **Limitation:** this is not the human fresh-user test. Time and confusion were not measured; A-01's pass criteria are unmeasured. No code changed, no automated tests run.

## 2026-11-26 — CLI M6 step 6.7 follow-up: S-series mapping (branch ai/p6-automation-docs-hardening)

- **Tests added:** `TestRunJSONOutputIsEscaped` (S-17) in `commands/runsops_test.go`.
- **Docs:** `M6-notes.md` maps S-02/04/05/10/11/12/15/17/18/19/20/21/29 to existing tests by reading them. S-16 needs a pty (not run); S-31 not applicable until the CLI consumes server install info.
- **Limitations:** mapping is by reading, not by per-ID execution; non-S unmapped IDs remain. Checks run: gofmt, golangci-lint 0 issues, `go test -race -count=1 ./internal/cli/... ./cmd/...` pass.

## 2026-11-26 — CLI M6 step 6.7: full acceptance run report (branch ai/p6-automation-docs-hardening)

- **Docs only:** `docs/cli/spec/M6-notes.md` section 6.7. Checks run, all pass: gofmt, `go vet ./...`, golangci-lint (0 issues), `go test -race -count=1 ./...`, `make smoke`, `make vuln`.
- **Findings:** 70 of 179 acceptance IDs are not referenced by ID in test code (listed in the notes), including several S-series IDs; they are reported as unmapped, not passing. A-01 not run (6.8).
- **Not run:** Postgres, pty layers, SSH lab, real agent/NATS, Windows.

## 2026-11-26 — CLI M6 step 6.6 follow-up: CLI fuzz targets in `make fuzz` (branch ai/p6-automation-docs-hardening)

- **Changed** `mk/checks.mk` (owner approved): `FUZZ_TARGETS` now lists the seven `internal/cli` fuzz targets, so `make fuzz` and the CI fuzz smoke run them. Checks run: `make fuzz FUZZTIME=3s` pass. Not run: CI itself.

## 2026-11-26 — CLI M6 step 6.6: hardening (branch ai/p6-automation-docs-hardening)

- **Tests added:** fuzz targets `FuzzReadDocument` (`internal/cli/config/fuzz_test.go`), `FuzzNewError` and `FuzzDecodeResponse` (`internal/cli/api/fuzz_test.go`); the sanitiser, target parser and normaliser fuzz targets already existed. No code under test changed.
- **Docs:** `docs/cli/spec/M6-notes.md` section 6.6: security review against T1..T16 with test evidence, scan results, and why R+1 removal stubs are not prepared (no-legacy rule).
- **Limitations:** the CLI fuzz targets are not in `FUZZ_TARGETS` (`mk/checks.mk`), so `make fuzz` and CI do not run them; wiring needs owner approval. Checks run: each new target 20 s and each existing CLI target 10 s fuzzing, `make vuln` (no unaccepted vulnerabilities), gofmt, golangci-lint, `go test -race -cover ./internal/cli/... ./cmd/...`, `go test ./...` (see result in the step report). Not run: smoke, pty, Postgres.

## 2026-11-26 — CLI M6 step 6.5: documentation (branch ai/p6-automation-docs-hardening)

- **Docs:** README "Five-minute path"; new `docs/cli/ci.md` (CI and scripts), `docs/cli/migration.md` (old to new, no aliases, behaviour changes), `docs/cli/messages.md` (generated from the message catalogue); `docs/cli/user-full-guide.md` rewritten for the current commands (lab hosts, ports and key paths taken from `deployments/testing`); `docs/cli/README.md` links and a pointer that the spec is a design record. One synopsis line in `commands.md` made a real example.
- **Tests added:** `commands/docs_test.go` `TestDocsExamplesParse` (every `machinarchy ...` line in `sh`/`bash` fences of the README and `docs/cli/*.md` is accepted by the command's parser; 20+ lines required); `msg/docs_test.go` `TestMessagesDoc` (fails when `messages.md` differs from the catalogue; `-update` rewrites it).
- **Limitations:** the guide has no recorded output (it was not re-run against the lab, so no output is claimed); the doc test checks parsing, not that the commands succeed. `troubleshooting.md` stays hand-written; the generated page is `messages.md`. Checks run: gofmt, vet, golangci-lint 0 issues, `go test -race -cover ./internal/cli/... ./cmd/...` pass; not run: `go test ./...`, smoke, the lab guide itself.

## 2026-11-26 — CLI M6 step 6.4: install and upgrade docs (branch ai/p6-automation-docs-hardening)

- **Docs only:** `README.md` section "Install the CLI" (checksum and attestation verification, `machinarchy version`, upgrade procedure); pointer in `docs/cli/README.md`. Verified against `.github/workflows/release.yml`: `publish-assets` already builds linux/darwin amd64/arm64, writes `checksums.txt` and attests; no workflow or script was changed.
- **Not done:** Windows build (Q6, owner decision, documented as not published); `--version` flag does not exist (not added); agent protocol pin waits for `protocol/v0.1.0` (agent repo only has `v0.0.1`). Checks: no doc link checker in the repo; none run.

## 2026-11-26 — CLI M6 step 6.3: version-skew messaging and C-11 proposal (branch ai/p6-automation-docs-hardening)

- **Changed** `internal/cli/api/client.go`: a response that is valid JSON of the wrong shape now says "Update the CLI" (exit 4); text that is not JSON keeps the "check the address" hint. Test `TestDecodeFailureSaysWhetherToUpdateOrCheckTheAddress`.
- **Docs:** `docs/cli/commands.md` "Supported servers" (same release); `docs/cli/spec/M6-notes.md` holds the C-11 proposal (`GET /api/v1/version`). **No controller change was made**; it needs owner approval.
- **Limitations:** the CLI cannot compare versions (O-20 not implemented); a wrong-shape answer can also come from a proxy. K-04 (old commands keep working) stays unimplemented by the no-legacy rule.

## 2026-11-26 — CLI M6 step 6.2: CI behaviour tests and docs (branch ai/p6-automation-docs-hardening)

- **Added** `commands/ci_test.go`: N-04/N-05 sweep (every prompting command exits 2 naming a flag, with no terminal and with `--no-input`, sending nothing that changes), N-06 (`MACHINARCHY_TOKEN/SERVER/NO_INPUT/OUTPUT` honoured, config dir stays empty), N-07 (JSON error on stderr, stdout empty), N-08 (`hosts -q`), S-30/N-09 (`--dry-run` on every changing command sends no non-GET request). N-03 already existed (`errors_test.go`). No production code changed: the audit (`.plan/6.2.md`, local) found no gap in behaviour.
- **Docs:** `docs/cli/commands.md` section "Scripts and CI".
- **Limitation:** only `run` previews under `--dry-run`; other changing commands refuse the flag (exit 2), so the S-30 "prints the preview" part is not implemented. `-q` is tested for `hosts` only, not `run -q` or `tokens create -q` here (`run -q` is covered in `runs_test.go`).

## 2026-11-26 — CLI M6 step 6.1: schema goldens for `-o json`/`-o yaml` (branch ai/p6-automation-docs-hardening)

- **Added** `commands/schema_test.go` and goldens `commands/testdata/schema/*.txt` (key path + type per output) for `hosts`, `hosts show`, `envs`, `access`, `access show`, `tokens`, `whoami`, `run`, `run ID`, home and `version`. Tests: N-01 a promised key that is gone or retyped fails (new keys allowed); N-02 the same command twice is byte-identical (JSON and YAML) for outputs that do not carry the clock; the check itself is tested against removed/renamed/retyped keys; YAML paths must exist in JSON. `-update` rewrites the goldens.
- **Docs:** `docs/cli/commands.md` section "Output compatibility". No code behaviour changed.
- **Finding (not changed):** `-o json` prints a zero `lastSeenAt` (`0001-01-01T00:00:00Z`) on observed host keys, `-o yaml` omits it (`omitempty` differs). Left as is: changing it would alter a documented key.
- **Limitations:** run list, tokens, home and version are shape-only (the fakes or the output carry the clock, map order or build data). Not pinned: `config show`, `hosts add`, `hosts enroll`, `tokens create`, `secret`, `runtime-config`.

## 2026-11-26 — CLI M5 step 5.5: lore guard, completion hint, plain audit (branch ai/p5-home-brand)

- **Added** one-time completion hint: the first interactive `login` prints `hint.completion` on stderr (points at `machinarchy completion --help`; `completion install` does not exist). Remembered per computer in the config (`hints:`, `Store.HintSeen/MarkHintSeen`); skipped with `--plain`, `-q`, or no terminal.
- **Added tests:** U-04 `TestLoreTextAppearsOnlyInTheCatalogue` (no lore line in any CLI source outside `msg`), complements the existing allowlist and renderer tests; U-05 `TestPlainHelpIsASCIIForEveryCommand` (help of every visible command under `--plain`); login hint tests (once, stderr, none on pipe or `--plain`). Docs: `docs/cli/commands.md`; `docs/cli/spec/M5-notes.md`.
- **Limitations:** plain audit covers help and catalogue messages, not every command's data output; inline `fmt.Errorf` and prompts are not catalogued, so lore there is guarded only by the source scan. No pty run.
- **Checks run:** gofmt (clean), `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (pass), `go test ./...` (pass), `make smoke` (OK). Not run: pty/colour look, Postgres, SSH lab.

## 2026-11-26 — CLI M5 step 5.4: help layout, examples, "did you mean" (branch ai/p5-home-brand)

- **Changed** help: commands are grouped Get started (login, hosts, run), Everyday (access, envs, tokens), Account (whoami, switch, logout), Advanced (get, runtime-config, secret, version, help) in `commands/registry.go`; a usage template (`internal/cli/help.go`) puts `Examples:` last, adds "Run 'machinarchy COMMAND --help' for details." and a tab-completion line on the root (`machinarchy completion --help`; `completion install` does not exist yet). "Available Commands" is now "Commands". Root got examples.
- **Added** `internal/cli/suggest.go`: an unknown command at any depth names the closest command (`Why:`) and the full corrected command line (`Next:`); nothing close keeps `<path> --help`. Cobra's own distance default only applied at the root, so it is set for the searched command.
- **Added** `Example` to `get`, `get agent-tokens`, every `runtime-config` and `secret` command and `access agent-tokens`.
- **Tests:** U-02 `TestEveryCommandHasParsableExamples` (every visible command incl. root; each example line parsed by the command's own parser), `help_test.go` (suggestions at three depths and after a flag, group order, Examples last), existing group test updated for "Commands:". Docs: `docs/cli/commands.md` ("Help and typos").
- **Limitations:** hidden `config` and `completion` have no examples (exempt). Suggestions cover commands only, not flags (Cobra reports unknown flags without a suggestion). The corrected line replaces the first argument equal to the typo.
- **Checks run:** gofmt (clean), `go vet ./internal/cli/...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass). Not run: `go test ./...`, `make smoke`, pty/colour look, Postgres.

## 2026-11-26 — CLI M5 step 5.3: catalogue applied to plain status lines (branch ai/p5-home-brand)

- **Changed** plain status sentences in `internal/cli/commands/` now come from the catalogue and print through `printMessage`/`printMessageTo` (new, `envs.go`): empty lists, secret created/retired, key trusted and trust summary, "not signed in to SERVER", host registered, the `hosts add` waiting lines and the fingerprint note. New `msg` IDs: `list.no_hosts`, `list.empty`, `secret.*`, `host.registered`, `host.no_pending`, `host.key_trusted`, `host.trust_summary`, `host.waiting_*`, `host.fingerprint_note`, `auth.not_signed_in_server`. Output text is unchanged except that the fingerprint note now puts the `ssh-keygen` command on a `Next:` line.
- **Breaking (CLI, no alias by owner rule):** `hosts` and `hosts trust --all-pending` flag `--selector` is now `--tagged` (`-l` unchanged); `-l SELECTOR` in usage is `-l TAGS`. Help for `secret rewrap` says "run data" instead of "Operation data". Docs updated: `docs/cli/commands.md`, `docs/cli/README.md`, `docs/operations.md`.
- **Added tests:** U-03 `commands/vocabulary_test.go` (no forbidden word in name, help, examples or flag usage of any visible command); U-06 `msg` test that every success message has a `Next:` line or is on an explicit exception list.
- **Not changed (known limitation):** data rows and tables (`hosts show`, `run` preview, home rows, enrolment blob), progress fragments and prompts stay inline; error text built with `fmt.Errorf` in `commands/` is not yet in the catalogue.
- **Checks run:** gofmt (clean), `go vet`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass). Not run: `go test ./...`, `make smoke`, pty/colour look, Postgres.

## 2026-11-26 — CLI M5 step 5.2: logotype (branch ai/p5-home-brand)

- **Added** `ui.Style.Logotype()` (`internal/cli/ui/logotype.go`: ◆ tree with accent/bold/dim roles, ASCII `*` form for non-UTF-8 locales, empty in `--plain`) and `Factory.Logotype(w)`, which returns it only when `w` is a terminal and plain output was not asked for. Shown by `version` (text output), the home screen when not signed in, and the first `login` when no server is saved (on stderr). Never in `-o json/yaml` or on a pipe.
- **Already in place before this step** (verified, not rewritten): the 16-colour palette, symbol set with ASCII words, `NO_COLOR`, `TERM=dumb`, locale detection and plain mode in `ui/style.go`.
- **Deviations:** `machinarchy --version` does not exist (only the `version` command); documenting/adding it stays in step 6.4. Spec 07-brand.md section 4 says plain mode prints the tagline only, section 6 says no logotype; section 6 was followed (nothing printed in plain mode).
- **Tests:** `ui/logotype_test.go` (goldens rich/ASCII/plain, N-11, N-12, U-05, U-07 width, colour roles); `version` output to a pipe has no logotype or escape codes (N-10).
- **Checks run:** gofmt (clean), `go vet ./internal/cli/...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass). Not run: `go test ./...`, `make smoke`, a pty/real-terminal look at the colours.

## 2026-11-26 — CLI M5 step 5.1: the home screen (branch ai/p5-home-brand)

- **Added** the state-aware home screen for bare `machinarchy` (`internal/cli/commands/home.go`, hooked in through the new `cli.Options.Home` in `root.go` and `cmd/machinarchy/main.go`; a mistyped word is still "unknown command" and `--help` still prints help). It shows not signed in, expired or refused credential (exit 3), unreachable server (exit 4), no hosts, attention needed and healthy, with the suggestion priority of `03-ux.md` section 1 (key changed, waiting for key, cannot connect, no hosts, otherwise a `run --on TAG -- uptime` example), and `-o json` with a `state` field. It makes `/me`, the hosts list (first 500, scoped to the active env) and the last two runs in parallel with a 3 s limit each; a failing request degrades one line. Host and server text is sanitised; no credential is printed or sent anywhere else.
- **Added** four `home.*` messages to the catalogue. Docs: `docs/cli/commands.md` ("Home screen").
- **Deviations from the spec transcript:** "Last runs" omits the command text (one request per run would break the 3-request budget); the session end comes from the saved credential instead of `GET /sessions`; at most 500 hosts are counted ("first 500 counted"); the lore allow-list in `msg_test.go` gained `home.not_signed_in` (the spec's own lore line). Not signed in also exits 3 (spec only fixes expired at 3).
- **Tests:** `commands/home_test.go` covers every state, the suggestion priority per fixture (U-10), expired = exit 3 and no request sent (B-04), pipe output without ANSI (N-10), `--plain` and `LANG=C` ASCII only (U-05, N-12), env scoping of the host request, slow-request budget, one-line degradation, sanitised host names, JSON document, typo and `--help` behaviour.
- **Checks run:** gofmt (clean), `go vet ./...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass; commands 82.7 %), `go test ./...` (all pass). Not run: `make smoke`, a pty/real-terminal look at the colours, a real controller, Postgres tests.

## 2026-11-26 — Docs: AI workflow block in AGENTS.md; M3 and M4 approved by the owner

- **Added** an "AI workflow" section to `AGENTS.md` (local-only `PLAN.md`/`.plan/`, one step per run, narrow reads, no unverified check claims). The owner approved M3 (envs, access, hosts) and M4 (runs) in code; M5 is next. Docs only, no code changed.
- **Checks:** none applicable (documentation only); no tests run.

## 2026-11-26 — CLI M4: the `run` command (branch ai/p3-envs-access-hosts)

- **Added** the single `run` command (`internal/cli/commands/run*.go`): `run --on TARGET [--except TARGET] -- PROGRAM`, `--do CAPABILITY [KEY=VALUE...]` / `--input-json`, `run` (list), `run ID` (grouped result), `run ID --host NAME [--stream] [--raw] [--follow]`, `--follow`, `--retry-failed [--include-unknown] [--include-cancelled]`, `--cancel [--no-wait]`, `--detach`, `--timeout`, `--concurrency`, `--wait-timeout` (exit 5), `--no-groups`, `--groups N|all`, `--fail-on-drift`, `--dry-run`, `-q`, `-o json|yaml`. It waits by default and detaches with `--detach` or a timeout above 15 minutes. Exit codes 0 / 8 (some failed) / 9 (all failed) / 5 / 130 come from the M1 aggregator (`group.ExitFor`).
- **Added** `internal/cli/target`: the `--on` / `--except` parser (names, `web-*` globs expanded over the hosts in scope, tag selectors, `all`; names and tags never mixed; the active env joins the selector as `environment=ENV`, a conflicting env or a name outside the env is refused; `env=` is just a tag) with tests and a fuzz target.
- **Preview and safety:** the blast-radius preview (`[server · env]`, hosts, command with quoted arguments, env, limits) is printed on stderr before every run, also with `--yes`; submit is bound to the preview with `expectTargetsHash` (`targets_changed` → exit 7, "Nothing ran"); more than 200 hosts is refused before the preview prompt. The local protected-env guard follows the env of the previewed hosts (typed env name, or `--confirm-env` plus `--yes` without a terminal; `--yes` alone never answers it); `--on all` outside an env asks for the host count. Hosts, env names and remote output are sanitised before they reach the terminal; `--raw` is refused on a terminal.
- **Ctrl+C and failures:** Ctrl+C while watching only stops watching ("Stopped watching. The run keeps going on the hosts." + resume and cancel commands, exit 130, no cancel sent); during a submit, or when the connection drops there, the CLI says the run "may or may not have been created", shows `Next: machinarchy run`, never resubmits (exit 130 / 4); a watch whose polls keep failing ends with "Lost contact ... the run keeps going" (exit 4); brief outages are retried silently. A cancel never says "stopped" before the controller does and flags hosts that were unreachable while cancelling. `--retry-failed` starts a new run on `Failed`/`TimedOut` hosts, lists unknown-outcome and cancelled hosts as skipped, never says "fixed" and leaves the old run unchanged.
- **Added** `group.RenderPerHost` (`--no-groups`), `clierr.Reported` (an exit code with nothing more to print; `internal/cli/run.go` honours it), `api.OperationSpec.Capability` and `OperationStatus.CancelAsked`, 14 `run.*` messages, tab completion for `--on`, `--except`, run IDs, `--host`, `--do` and `--stream`.
- **Removed** (no aliases, per the no-legacy rule): `run -l/--host/--all/--exclude/--wait/--capability/--input`, `get operations`, `cancel operation`, `logs operation` (`commands/logs.go` deleted). Spec items for legacy semantics (08/10 item 9, K-04..K-06) are therefore not implemented.
- **Docs:** `docs/cli/commands.md` (Running programs, exit codes, completion), `docs/cli/README.md`, `docs/operations.md`, `docs/examples/example-usecases.md`, new `docs/cli/spec/M4-notes.md` (decisions and deviations). `docs/cli/user-full-guide.md` still shows the removed commands (banner; rewrite is step 6.4).
- **Tests:** operations acceptance tests with a scripted fake API (`runsfake_test.go`, `runs_test.go`, `runsops_test.go`): partial failure O-06/O-08/O-27, all fail/all ok O-07, targets and env O-23..O-26, 200-host refusal O-22, `targets_changed` O-21, Ctrl+C during submit O-16, dropped submit O-17, Ctrl+C while following O-14, lost stream O-18, `--wait-timeout`, retry-failed O-09..O-11, cancel O-12/O-13, protected env and `--on all` S-22..S-26, listing as reader O-31, capability form O-29/O-30, program on an agent host O-28, malformed answer O-19, sanitisation S-13/S-14, ID prefixes, drift and `--groups`.
- **Known limitations / not run:** a second Ctrl+C ending the process (O-15) and the redrawn progress line need a pty and were not run (the interrupt is tested with a cancelled context); no lab run on SSH hosts, no real controller or Postgres run; `scripts/smoke.sh` now uses `run --on` and `run` (changed with the owner's approval); the `run` list reads each listed run to show its command (heavy until C-06); runs above 200 hosts stay refused (C-05); the `hosts add` success hint still says `hosts show`.
- **Checks run:** `gofmt -l` (clean), `go vet ./...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass; commands 82.2 %, target 96.2 %, group 93.7 %), `go test ./...` (all pass). `make smoke` (OK, against the real monolith). Not run: Postgres tests, lab and pty checks above.

## 2026-11-26 — CLI M3 step 3.8: remove the old resource commands, add dynamic completion (branch ai/p3-envs-access-hosts)

- **Removed** (no aliases) `get hosts`, `get profiles`, `create host|profile|agent-token`, `delete host|profile|agent-token` and `label host` with their code and tests (`commands/hosts.go`, `access.go`, `get.go`, `registry.go`); their replacements are `hosts`, `hosts show|add|tag|remove|enroll|revoke-agent`, `access` and `envs`. `get operations` stays until M4; `get agent-tokens` stays because `hosts revoke-agent` needs the agent ID it shows (C-09). A test asserts the old commands now fail with exit 2 and send nothing.
- **Added** dynamic shell completion (`internal/cli/completion.go`, `commands/completion.go`): host names for `hosts show|tag|move|trust|enroll|revoke-agent|remove`, access names for `access show|remove` and `hosts add --access`, env names for `--env` and `envs use|remove`. It uses the saved credential, has a 3 s limit, prompts for nothing, prints no errors and drops names with whitespace or control characters. Tests in `completion_test.go`.
- **Changed** tests that used `get hosts` now use `hosts` (`TestHostsList` replaces `TestGetHosts`); `scripts/smoke.sh` and the Makefile example use the new commands (approved per file).
- **Docs:** `docs/cli/{commands,README,configuration,troubleshooting,example-cli}.md`, `docs/examples/*`, `docs/operations.md`, `docs/runbook.md`, root `README.md`, new `docs/cli/spec/M3-notes.md`. `docs/cli/user-full-guide.md` still shows the removed commands; it has a banner and is rewritten in step 6.4 (known limitation).
- **Not implemented:** step 3.7b (deferred by the owner). Controller condition messages still say `trust host` (controller change, owner).
- **Checks run:** `gofmt -l` (clean), `go vet ./...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass; commands 80.3 %), `go test ./...` (all pass), `make build` + `make smoke` (OK, against the real monolith). Not run: Postgres tests, real-terminal tab completion in a shell, SSH-container lab, real agent compatibility.

## 2026-11-26 — CLI M3: hide the `completion` command from help and menus (branch ai/p3-envs-access-hosts)

- **Changed** `internal/cli/root.go`: Cobra's `completion` generator is hidden from `--help` and command lists (`HiddenDefaultCmd`). It still works (`machinarchy completion bash`), as the spec (3.13) says: hidden, documented. `completion install` and dynamic completion stay planned for step 3.8; completion that turns itself on without installing a script is not possible, and the CLI never edits shell rc files.
- **Checks run:** `gofmt -l` (clean), `go build`, manual `--help` (no `completion` line) and `completion bash` (still prints). vet, golangci-lint (0 issues) and `go test -race -cover ./internal/cli/... ./cmd/...` pass. Not run: smoke, Postgres tests.

## 2026-11-26 — CLI M3 step 3.7: agent path of `hosts` (branch ai/p3-envs-access-hosts)

- **Added** `hosts add NAME --agent [--access ENTRY] [--tag K=V] [--ttl] [--token-out]`, `hosts enroll NAME [--ttl] [--token-out] [--revoke-old] [--show-controller-key]` and `hosts revoke-agent NAME --agent-id ID` (owner) in `internal/cli/commands/hostsagent.go`. The one-time token and controller key are returned as a base64 install blob on stdout only; `--token-out` writes it to a new 0600 file and never overwrites; a failed write revokes the token again. `revoke-agent` needs the typed host name (`--yes` is not enough) and the protected-env guard. Message `host.agent_revoked`; docs `docs/cli/commands.md`.
- **Added** `--nats-url`, `--nats-creds-file`, `--remember` on `hosts enroll` and `hosts add --agent`; per-server config `agent-nats-url` and `agent-bootstrap-creds` (a path, never the credential). The URL (nats:// or tls://, no userinfo) goes into the blob; the credential file is only referenced and the instructions give the copy command.
- **Limits (planned, not implemented):** the server does not report the broker address or credential (Q1/C-07), so without those flags the instructions name them as "not reported by this server"; no hosted `install.sh` exists yet (planned, agent repo); `revoke-agent` needs `--agent-id` (C-09); `--env` stays refused (C-01); automatic names, `hosts rename` and auto-approve are step 3.7b.
- **Checks run:** `gofmt -l` (clean), `go vet ./internal/cli/...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass). Not run: smoke, Postgres tests, agent-side compatibility with a real agent, `go test ./...`.

## 2026-11-26 — CLI M3 step 3.6: guided `hosts add` for SSH hosts (branch ai/p3-envs-access-hosts)

- **Added** `machinarchy hosts add [NAME [ADDRESS]]` (owner) in `internal/cli/commands/hostsadd.go`: a wizard with `back` navigation that sends nothing until every answer is in (name, address, new key / new password / existing access entry, host-key policy, "Send it?" for a typed credential), plus the full flag twin (`--access`, `--access-name`, `--user`, `--key-file`, `--password-stdin`, `--host-key-policy`, `--tag`, `--trust-fingerprint`, `--wait`). It creates the secret and access entry when needed (rolling the secret back if the entry fails), registers the host, waits for the server to observe the SSH key, shows the key the server saw and asks for the first 8 characters (Enter skips); `tofu` entries ask nothing. `--yes` never approves a key; without a terminal the host is left `waiting for key approval` and the exact `hosts trust` command is printed; `--trust-fingerprint` trusts only a key the server saw. Address (`HOST[:PORT]`, default port 22), name and tags are validated before any request; a duplicate name exits 7; an unreachable host is reported as `cannot connect` with the server's reason (exit 0). The wizard prints the command that repeats it without questions. Messages `host.added`, `host.added_waiting_key`, `host.added_no_connect`, `host.added_checking`.
- **Changed** `accessentries.go`: `createAccess*` split into `makeAccess*` (returns the entry) plus the success line, so the host flow reuses them. Docs `docs/cli/commands.md`.
- **Deviations / limits:** `--env` is refused (gap C-01, no env on host creation); the success line points to `hosts show NAME` instead of the spec's `run --on NAME -- uptime` because `run --on` arrives in M4. The agent path (`--agent`, `hosts enroll`, `revoke-agent`) is step 3.7. Not tested against a real SSH container lab. Old `create host` stays until 3.8.
- **Checks run:** `gofmt -l` (clean), `go vet ./internal/cli/... ./cmd/...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass; commands 79.9 %). New tests cover H-01..H-07, O-03..O-05, S-06/S-07, `--wait`, key/password never printed and the secret rollback. Not run: `make smoke`, Postgres tests, a real-terminal wizard session, SSH-container lab, `go test ./...`.

## 2026-11-26 — CLI M3 step 3.5: `hosts trust` and the host-key safety changes (branch ai/p3-envs-access-hosts)

- **Added** `machinarchy hosts trust NAME [--replace] [--fingerprint SHA256:...]` and `hosts trust --all-pending [-l SELECTOR]` (owner) in `internal/cli/commands/hoststrust.go`. First trust shows the keys the controller saw and asks for the first 8 characters after `SHA256:` (Enter or `back` skips); in a script `--fingerprint` must be one of the seen keys; all seen keys are sent together. `--replace` shows old and new keys, asks for the new key's prefix and then the host's name (`--confirm NAME` in a script); a locally protected env also needs its name. `--all-pending` asks about every waiting host in turn. Keys that are not `SHA256:` fingerprints are refused. Messages `host.trusted`, `host.key_replaced`.
- **Safety (B5, B10):** `--yes` never approves a key; without a terminal and without `--fingerprint` nothing is trusted and the command names the flag to use; `--all-pending` is refused without a terminal or with `--no-input`.
- **Removed** the old `trust host` command (`commands/trust.go`) with no alias, per the project's no-legacy rule; its tests were replaced by `hoststrust_test.go`. Docs updated: `docs/cli/*`, `docs/operations.md`, `docs/runbook.md`.
- **Limits / known:** the controller still writes `machinarchy trust host ...` in the `HostKeyPendingApproval`/`HostKeyChanged` condition messages (`internal/hostaccess/reconciler.go`); not changed (controller is out of scope for this step), so that hint is stale. `scripts/smoke.sh` has no `trust` call (checked with `rg`).
- **Checks run:** `gofmt -l` (clean), `go vet ./internal/cli/...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass; commands 80.8 %). Not run: `make smoke`, Postgres tests, a real-terminal prompt session, `go test ./...`.

## 2026-11-26 — CLI M3 step 3.4: `hosts` list, show, tag, move, remove (branch ai/p3-envs-access-hosts)

- **Added** `machinarchy hosts` (NAME, ENV, ADDRESS, HOW, STATUS, TAGS; `--status ready|attention|all`, `-l` selector, `--env`, `-o json|yaml` keep the API field names, `-q` names), `hosts show NAME` (status with raw reason, env, address, access kind, every condition, observed and trusted host keys, last five runs), `hosts tag NAME KEY=VALUE... KEY-` (tag rules checked before sending; a stale revision is reported as "the host changed"), `hosts move` (checks the names, then refuses: the server cannot move hosts between envs, gap C-01) and `hosts remove NAME` (inventory only; typed name or `--confirm`, `--yes` is not enough; the output lists what was not done) in `internal/cli/commands/hostsgroup.go`. Readiness mapping per `docs/cli/spec/02-commands.md` 3.8.1. In a locally protected env `tag` and `remove` also need the env name (typed or `--confirm-env`). Messages `host.*`; docs `docs/cli/commands.md`.
- **Limits:** `hosts add`, `trust`, `enroll`, `revoke-agent` come in steps 3.5-3.7; the old `get hosts`, `label host`, `delete host` stay until 3.8. Condition reasons other than the documented ones show as `needs attention` (gap C-15).
- **Checks run:** `gofmt -l` (clean), `go vet ./internal/cli/...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass; commands 80.5 %). Not run: `make smoke`, Postgres tests, a real-terminal check of the typed env prompt, `go test ./...`.

## 2026-11-26 — CLI M3 step 3.3: `access` commands (branch ai/p3-envs-access-hosts)

- **Added** `machinarchy access` (list with how, user and host count; `-o json|yaml`, `-q` names), `access show NAME` (how it works, host-key policy in words, hosts using it; no secret reference or value), `access add NAME` (owner; guided on a terminal, flags `--how ssh-key|ssh-password|agent`, `--user`, `--key-file`, `--password-stdin`, `--host-key-policy approve|pinned|tofu`; the credential becomes a secret, then the entry, and the secret is retired again if the entry fails; `tofu` only with a key; a non-private-key file is refused) and `access remove NAME` (owner; typed name or `--confirm`, `--yes` is not enough; says the stored secret is not retired) in `internal/cli/commands/accessentries.go`. Messages `access.*`; docs `docs/cli/commands.md`.
- **Limits:** the old `create profile` / `delete profile` stay until step 3.8. The host-key policy of an existing entry cannot be changed yet. No controller change.
- **Checks run:** `gofmt -l` (clean), `go vet ./internal/cli/...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass; commands 80.5 %). New tests cover that the key and password are never printed, refusals, rollback of the secret and the typed-name removal. Not run: `make smoke`, Postgres tests, a real-terminal wizard session, `go test ./...`.

## 2026-11-26 — CLI M3 step 3.2: `envs` commands (branch feat/cli-m1)

- **Added** `machinarchy envs` (list with host counts, protected and active marks; `-o json|yaml`, `-q` names), `envs add NAME [--description] [--protected]`, `envs use NAME|any`, `envs remove NAME` (`internal/cli/commands/envs.go`). `--protected` is a local guard only (the server cannot enforce it, gap C-02): it asks first (`--yes` answers) and nothing is created if the answer is no. `envs remove` needs the typed name or `--confirm NAME` (`--yes` is not enough) and, for a locally protected env without a terminal, `--confirm-env NAME`; it clears the active env and the local guard and deletes no host.
- **Config:** `servers.<name>.protected-envs` (local guard) and store methods `SetEnv`, `LocalProtected`, `SetLocalProtected`. New messages `env.*` in the catalogue. Docs: `docs/cli/commands.md`.
- **Limits:** the active env is not yet applied by `envs` itself beyond marking it; the guard is not yet consulted by other commands (they arrive in 3.3-3.8 and M4). No controller change.
- **Checks run:** `gofmt -l` (clean), `go vet ./internal/cli/...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass after fixing a non-ASCII character in a new message; commands 81.8 %, config 83.1 %). Not run: `make smoke`, Postgres tests, a real-terminal check of the typed prompt (tested with flags only).

## 2026-11-26 — CLI M3 step 3.1: API client for inventory (branch feat/cli-m1)

- **Added** `internal/cli/api/inventory.go` (`InventoryClient`): create/delete environment, get one Access Profile (spec holds secret references only), list and revoke agents, controller public key. `ListAgents` returns the raw response because its shape is not in the API contract (gap C-09). `HostQuery` gained `Phase` and `Limit`.
- **Already present, unchanged:** host create/get/delete/labels/trust, access profile list/create/delete, enrolment tokens (`managed.go`, `access.go`), secret create (`ManagementClient.CreateSecret`). No controller change; no command uses the new methods yet (steps 3.2-3.8).
- **Checks run:** `gofmt -l` (clean), `go vet ./internal/cli/api/`, `golangci-lint run ./internal/cli/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass; api 84.3 %). Not run: `make smoke`, Postgres store tests.

## 2026-11-26 — CLI M2: servers and authentication (branch feat/cli-m1)

- **New commands:** guided `login` (email and password, or an API token with `--with-token` from a hidden prompt or stdin; the token is checked before it is saved; "stay signed in" creates a device token), `logout` (revokes on the server, `--everywhere`), `whoami`, `switch`, `tokens` (list, create, revoke) and hidden `config path|show`.
- **Credentials:** config v2 holds servers only; secrets are in a separate `0600` file written atomically, shown as `***` everywhere. Order: `MACHINARCHY_TOKEN`, saved token, saved session. A saved credential is only sent to its own server; owner-only commands use the login session and stop with Error/Why/Next when only a token exists. TLS verification cannot be turned off (`--ca-file` per server); plain http to a non-local host needs `--insecure-http`; redirects are not followed.
- **Removed, by the owner's decision (no legacy support):** `config set-*`/`use-context`/`get-contexts`/`current-context`, `--context`, `--token`, `MACHINARCHY_CLI_*`, the token commands under `get/create/delete`, and the M1 deprecation mechanism. An old-format config is refused with the commands to move it aside; its tokens are not copied.
- **Fixed on the way:** `-o yaml` of an issued token panicked (duplicate key); a character device such as `/dev/null` was treated as a terminal.
- **Changed script (approved per file):** `scripts/smoke.sh` signs in with `login`.
- **Not done:** keychain backend (question Q5 unanswered); role and expiry from `/me` (controller work C-03). Deviations: `docs/cli/spec/M2-notes.md`.
- **Checks run:** `gofmt -l` (clean), `go build ./...`, `go vet ./...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass; cli 92.2 %, api 84.3 %, commands 81.6 %, config 91.8 %, cred 92.3 %), `go test ./...` (all pass), `make smoke` (OK), and a manual run against a real `bin/machinarchy-monolith` (login, whoami, tokens, switch, owner-only refusal, logout). Includes the tests that a token is not visible in `ps`, output formats, errors or logs. Not run: the Postgres store tests, a real terminal session by a person.

## 2026-11-26 — CLI M1: foundations (branch feat/cli-m1)

- **New packages under `internal/cli`:** `sanitize` (terminal-safe text, with a stream form for followed output), `msg` (message catalogue: plain primary line, why, next, optional lore), `ui` (style detection, message rendering, `--plain`, progress line), `group` (grouped-output aggregator and renderer), `deprecate` (deprecated-alias mechanism). No new third-party dependency; a test checks that the CLI binary links only cobra, pflag, yaml, `x/term` and `x/sys`.
- **Errors:** every error prints `Error:` / `Why:` / `Next:` (the old `Hint:` label is now `Next:`); with `-o json` the same parts are one JSON object on stderr. Exit codes 5, 8 and 9 exist (wait timeout, some hosts failed, every host failed); no command returns them yet. API errors split their advice into a cause and a next step.
- **Options:** one precedence for global options: flag, `MACHINARCHY_<NAME>`, the deprecated `MACHINARCHY_CLI_<NAME>` (still works, one warning line), saved `defaults:` in the config file, default. Empty variables count as unset. New `--plain` / `MACHINARCHY_PLAIN`. `--yes`, `--confirm`, `--confirm-env`, `--dry-run` stay flag-only; `--dry-run` is refused by commands that do not support it.
- **Security fix:** table cells, followed command output, output headers, the trust prompt and operation messages are sanitised, so remote text cannot send terminal escape sequences (audit F-04).
- **Not changed:** no old command is registered as a deprecated alias yet (their replacements do not exist); no command uses the grouped renderer yet (M4). Deviations from the spec are in `docs/cli/spec/M1-notes.md`.
- **Checks run:** `gofmt -l` (clean), `go build ./...`, `go vet ./...`, `golangci-lint run ./internal/cli/... ./cmd/...` (0 issues), `go test -race -cover ./internal/cli/... ./cmd/...` (all pass; coverage cli 87.7 %, api 75.9 %, clierr 91.4 %, commands 81.1 %, config 72.8 %, output 92.0 %, sanitize 98.3 %, msg 98.0 %, ui 91.3 %, group 93.4 %, deprecate 100 %), and `go test ./...` for the whole repository (all pass, including test/e2e and test/ha). The 5,000-host x 15 KiB memory test runs only in a plain `go test` (measured about 0.5 s and no retained output). Fuzzing of the sanitiser ran for 20 s with no finding. Not run: `make smoke`, the Postgres store tests, a manual check in a real terminal.

## 2026-11-26 — CLI redesign specification (documentation only, branch docs/cli-spec)

- **What:** `docs/cli/spec/01-10` describe the target state of a task-first, beginner-friendly `machinarchy` CLI: summary and decisions, command contracts, UX transcripts, threat model, architecture, API matrix, brand and voice, migration, acceptance tests, and milestones M1-M6. The existing user docs in `docs/cli/` are not changed and still describe the current CLI.
- **Based on:** the Step 1 audit of the current CLI (code, `docs/api/swagger.json`, handlers, tests). Controller facts were read from the contract and the handler code; kubectl comparisons come from running kubectl v1.27.1 locally and are labelled as such.
- **Decisions made in the spec:** bare `machinarchy` is the home screen (no `status`); `access` stays top-level; agent enrolment lives under `hosts`; `switch` selects servers; capabilities use `--do NAME key=value`; envs are created with `envs add`; `--on env=...` is **not** shipped (a tag may be called `env`), use `--env`; run exit codes 8 (partial) and 9 (all failed); runs are limited to the controller's 200 targets in V1. **At the owner's request tab completion is in V1**, which reverses fixed decision 11 for completion only (`doctor`/`fix` stay out).
- **Controller work the design depends on** (listed in `06-api-matrix.md`, none done): environment on host create/update (C-01), protected envs (C-02), role and expiry on `/me` (C-03), agent install information (C-07), and others. Open questions that block implementation are at the end of `10-roadmap.md`.
- **Not implemented:** no Go code, dependency, config or workflow was changed. No test was run; `09-acceptance-tests.md` is a plan.

## 2026-11-25 — Release image scan failed on UNKNOWN advisories (Trivy SARIF severity)

- **First release run (v0.0.1, CI run 45686902170):** "Scan the exact published digest with Trivy" failed for the `api` image, which cancelled the other image jobs, so no image was signed. The step sets `severity: CRITICAL,HIGH` with `exit-code: "1"`, but in SARIF mode trivy-action ignores `severity` unless `limit-severities-for-sarif` is set ("Building SARIF report with all severities"), so any finding failed the job.
- **Findings:** a local Trivy 0.70.0 scan of the `api` image built from the release commit found 0 HIGH/CRITICAL (Go modules, Go binary and distroless packages clean) and 4 UNKNOWN Debian advisories in the distroless base: `tzdata` (DLA-4792-1, DLA-4854-1, DLA-4867-1) and `ca-certificates` (DLA-4845-1). The published GHCR image is private and was not scanned directly.
- **Fix:** `template-docker.yml` sets `limit-severities-for-sarif: true`, so the SARIF report and the exit code cover only HIGH/CRITICAL, as intended. A newer distroless digest will clear the UNKNOWN advisories.
- **Checks run:** the local Trivy scans above only; the workflow was not run. Re-running the failed run would use the old workflow; the images are built by the next release.

## 2026-11-25 — A capability test that failed on the CI runner (branch fix/capability-test-timing)

- **First CI run on `main`:** `TestCapabilityTargetWithoutResultBecomesLost` failed ("timed out waiting for 1 published requests") in `make coverage` (`-race ./...` on a GitHub runner). The test gave each agent command a 200 ms lifetime. The capability relay publishes a command only while it is valid (`now < ExpiresAt`), so when the first relay pass came later than 200 ms the command expired unpublished and the target failed as `agent_command_undelivered`, which the test does not expect. The code is right; the lifetime was too short for a slow runner.
- **Fix:** the lifetime is 2 s, so the target becomes Lost about 3 s after dispatch (lifetime + the Operation's 1 s timeout + 50 ms grace); the test still checks the same behavior (published, no result, Lost with `no_result`).
- **Checks run:** none; per the owner's instruction tests are not run locally during GitHub work, CI runs them. The other tests of the file keep their own short lifetimes on purpose (`TestCapabilityCommandNeverPublishedFailsTheTarget` expects the expiry).

## 2026-11-25 — First CI run: smoke test, a timing test, Node 24 actions (branch release/initial)

- **The smoke test failed in CI** (`scripts/smoke.sh`, "secret and Access Profile": `JSONDecodeError`). The script parsed the output of `secret create` as JSON, but since the CLI work of G-13C-03 that command prints a line for people by default. The script passes `-o json` now (the owner approved editing the script), and starts `machinarchy-monolith` instead of the deprecated alias. `make smoke` passes locally.
- **A timing test could fail on a loaded runner.** `TestAttachedRunFansOutToConcurrency` asserted wall-clock time (300 ms waves plus 1.5 s) and failed under `make coverage` (`-race ./...`) on this laptop: 2.17 s against 0.3 s. It is deterministic now: the commands hold until the test releases them, and it asserts how many run at once (`min(targets, concurrency)`), that no further target starts while they are held, and that everything completes. No data race was involved.
- **Node 20 deprecation warning:** `actions/upload-artifact` is v7.0.2 (was v4.6.2, which runs on Node 20) and `docker/setup-buildx-action` is v4.4.1 (was v3.11.0, Node 20; the agent repo already used it), both pinned by SHA. The upload step's inputs are unchanged.
- **Checks run:** `make coverage` (the CI command: `go test -race -count=1 ./...` with PostgreSQL required and the end-to-end test required): pass, no race, 57.5 % statements; `make tidy-check fmt-check vet`: pass; `make smoke`: pass; the workflow YAML parses. Other actions were not checked for their Node runtime (no warning was reported for them).

## 2026-11-25 — CI before the first push: pinned actions, aligned scanners (branch release/initial)

- **Every action is pinned by commit SHA** (the tag is in a comment): `actions/upload-artifact` v4.6.2, `actions/attest` v4.2.2, `docker/setup-qemu-action` v4.4.0, `docker/build-push-action` v7.4.0, `github/codeql-action/upload-sarif` v4.39.5 (an annotated tag: the SHA is the commit it points to). SHAs were read from the actions' public tags with `git ls-remote`; Dependabot (`github-actions`) proposes bumps.
- **Scanner versions (F9-03):** govulncheck v1.9.0 (was v1.1.4; it is the version the audit verified with and reads the current Go vulnerability database); golangci-lint stays v2.15.0, which is what runs locally. The separate gosec step is removed: golangci-lint already runs the gosec rules (`.golangci.yml`), and the pinned standalone gosec (v2.22.4) could not load Go 1.27 packages, as the locally installed one could not. `make gosec` stays for local use. `.github/WORKFLOWS.md` says so.
- **Checks run:** workflow YAML parses; every `uses:` is pinned; the cross-reference script from the `.github` review still reports nothing. The workflows were not run (they have never run on GitHub).
- **Not done here (needs GitHub):** tagging the agent protocol module and removing the `replace` (D-4); see AUDIT/push-plan.md for the order.

## 2026-11-25 — .github review (branch audit/open-issues)

- **The release binary attestation would have failed.** `release.yml` attests the release binaries with `actions/attest`, which signs with the job's OIDC token and stores the attestation, so it needs `id-token: write` and `attestations: write`. The job had only `contents: read`, and the calling job in `ci.yml` too (a reusable workflow cannot exceed its caller's permissions). Both grant them now. Not run on GitHub; the permission names follow the action's documented requirements.
- **Issue templates:** the bug report's version example is `v0.0.1` / `v0.0.1-beta.1` (the first release and the pre-release form), and says that vulnerabilities do not belong in issues; the issue chooser links to GitHub's private vulnerability report.
- **Cross-checks that passed:** a script checked every workflow for `needs` references, job outputs, reusable-workflow inputs and secrets, permissions the callee needs, and the `make` targets and scripts the workflows (and the PR template) name: no other problem. YAML of all workflows, templates and Dependabot parses.
- **Still open, all Phase 10:** `actions/upload-artifact`, `actions/attest`, `docker/setup-qemu-action`, `docker/build-push-action` and `codeql-action/upload-sarif` are pinned by tag, not SHA; the pinned scanner versions predate Go 1.27; Dependabot's `gomod` job cannot resolve the `replace` to the sibling agent module until the protocol module is tagged; the workflows have never run on GitHub; Trivy fails the image job on any HIGH or CRITICAL finding, including ones in the base image, which may block a release (a policy to decide when the first scan runs).
- **Checks run:** the script above and a YAML parse. `actionlint` is not installed (downloading it was not allowed), and the workflows were not run.

## 2026-11-25 — Open issues (everything open except Phase 10; branch audit/open-issues)

- **S-11, Scalar without integrity.** The API reference page loaded its script from jsDelivr. The bundle (`@scalar/api-reference` 1.75.0, `dist/browser/standalone.js`, 4,620,361 bytes) is now embedded, gzipped (1.3 MB in the repository), and served from the API origin at `/scalar/api-reference-1.75.0.js` (one-year immutable cache; gzip passed through when the client accepts it, decompressed otherwise). The page's CSP is `script-src 'self'`; no third-party host is contacted. The downloaded file is the one published to npm, not jsDelivr's re-minified default, and its SHA-256 equals the one in jsDelivr's package index; a test checks the embedded bytes against that hash. Checked in a browser: the reference renders and the console shows no CSP error. `internal/transport/http/scalar/README.md` records source, hash, license (MIT) and how to update.
- **F16-05 and H-13B-01, the pre-commit hook (owner approved editing it).** The test timeout was 30 s, which does not fit `test/e2e` or the worker packages on a loaded machine; it is 120 s now (`PRECOMMIT_TEST_TIMEOUT` overrides it). Under macOS's `/bin/bash` 3.2 the hook failed with "unbound variable" on an empty array; fixed with `${arr[@]+"${arr[@]}"}` and checked under `/bin/bash` 3.2.57.
- **S-20, container hardening.** The dev `docker-compose.yml` app service has `read_only: true`, a `/tmp` tmpfs, `cap_drop: [ALL]` and `no-new-privileges`. I built the image and ran it with those flags (SQLite on a volume): healthy, clean exit 0 on `docker stop`. The PostgreSQL and NATS images in `deployments/infra.docker-compose.yml` are pinned by digest (the digests of the images already on this machine), and Dependabot covers Compose files.
- **F16-06, an unused signing-key secret.** When recording a signing key failed after its seed was stored (a replica that lost the first-start race, or a database error), the encrypted seed stayed behind. It is retired now (`Service.RetireSystem`, which only touches system-purpose secrets). Tests: the concurrent-start test expects exactly one stored secret; a failed key record leaves none; an owner's secret cannot be retired through that path.
- **Already fixed, now marked in the tracker:** F13A-05 (reconcile status is not audited since F20-05) and T-13B-01 (every PostgreSQL fixture sets idle connections).
- **A flaky test.** `TestTimeoutWithoutProcessGroupKillsTheTree` (`runner`) failed three times when the whole suite ran at once: the supervisor needed more than its 20 s wait on a saturated machine. The wait is 90 s now; it still fails if the supervisor never starts.
- **Left, by decision or because it needs something else:** Phase 10 items (CI never run on GitHub, scanner versions, the protocol `replace`, the actions pinned by tag); `deadcode` is not installed (downloading it was declined); load figures come from a laptop. **Noted, not changed:** every reconcile bumps a Host's `revision` (the status write shares the spec revision), so a revision-checked update that races a reconcile gets a 409; separating the status revision would remove it.
- **Checks run:** `go test ./...` with PostgreSQL required: pass (44 packages); `golangci-lint run ./...`: 0 issues; the image test above; compose files validated with `docker compose config`.

## 2026-11-25 — SSH review: handshake timeout and pinned keys on certified hosts (branch audit/ssh)

Reviewed `pkg/sshclient` (client, host keys), `pkg/netpolicy`, and `internal/hostaccess` (connector, reconciler), and the argument quoting that builds the remote command.

- **A silent host held its slot for the whole call (Medium).** `dial` set the handshake deadline to the call's deadline whenever there was one, so a host that accepts TCP and never speaks SSH (a firewall that drops, a tarpit, a wrong service) kept a connector slot until the call ended: up to 15 minutes for an attached command, 60 s for a launch. A handful of such hosts exhausted `ssh.maxConcurrent` and blocked every other run. The key scan already used the shorter of the two. The handshake now has its own 10 s limit, capped by the call deadline. The regression test hung for the full 5-minute call deadline before the fix and returns in 0.3 s with a shortened limit after it.
- **A host pinned by its plain key was refused if it had a host certificate (Low).** A server with a host certificate lets the client negotiate the certificate form of its key; the pin (the plain key's fingerprint, from the scan) did not match the certificate's fingerprint, so a trusted host failed with `HostKeyRejected` and then showed `HostKeyChanged`. A pin now also matches the key inside a certificate (the server has just proved it holds that key); a certificate from an untrusted CA does not make a pinned key fail. Test: a certified host, pinned plain key, no CA.
- **Cleanup:** the empty `pkg/sshclient/connection_pool.go` is removed.
- **Looked at, left as is:** the egress policy (order of rules, DNS pinned to the dialed address, NAT64/6to4/Teredo, metadata addresses, ports denied unless listed) is sound; reserved IPv4 ranges such as 240.0.0.0/4 and 198.18.0.0/15 count as public (they are not routable on the public internet, so this is not an exposure); `QuoteArgv` single-quotes every argument correctly for a POSIX login shell (documented assumption); the credential is resolved before a connector slot is taken, so a queued call holds a decrypted credential in memory while it waits (bounded by the call deadline; the resolver audit is per attempt).
- **Checks run:** `go test -race` for `pkg/sshclient`, `pkg/netpolicy`, `internal/hostaccess/...`: pass; `go test ./...` with PostgreSQL required: pass (44 packages); `golangci-lint run ./...`: 0 issues. A real SSH server with a host certificate and OpenSSH `MaxStartups` behaviour were not tried (the test SSH server is Go's).

## 2026-11-25 — Operation worker review: panics no longer end the process (branch audit/operationworker)

Reviewed `internal/operationworker` (worker, detached launcher, poller, capability relay).

- **A panic in a target ended the whole process (High).** AUDIT Q-04 isolated a panic in the goroutine that runs an Operation (`safeExecute`), but the goroutines that run its targets had no recovery: the attached target (`runAttachedTarget`, one goroutine per target), the detached launch (`launchTarget`) and the per-host status poll. A bug in a connector or an SSH library, triggered by one odd host, would exit the API too in monolith mode. The test that reproduced it killed the test binary with `panic: connector bug`. Now:
  - an attached run or a detached launch that panics fails its target as `worker_panic` (never retried: the command may have run), the sibling targets and the Operation carry on, and the panic is logged with its stack and counted in `panics_total`;
  - a panic while polling a host is logged and counted; its targets are polled again at their normal interval, not on the next tick;
  - a panic in one pass of the capability relay (the gateway's publish, the store) is logged and the next tick tries again.
- **Host-key failures found while polling (G-13C-01, completed).** A detached command whose host changed keys while it ran failed every poll as "unreachable" without queueing a recheck. Poll failures on a host key now queue it (still at most once a minute per host).
- **A silent failure.** When the scan for overdue capability targets failed, nothing was logged, so targets would never become Lost without a trace; it logs a warning now.
- **Looked at, left as is:** a result from an agent other than the one a command went to is refused without acknowledgement, so a buggy agent would republish it; `markSettled` reads with an unbounded context; `recoverRunning` lists running Operations every 500 ms (cheap, bounded to 500).
- **Tests (each failed or crashed before its fix):** attached target panic, detached launch panic, poll panic, relay panic, poll host-key recheck.
- **Checks run:** `go test -race ./internal/operationworker/` three times: pass; `go test ./...` with PostgreSQL required: pass (44 packages) on the second run. The first full run had one failure, `TestTwoWorkersOnPostgresRunEachCommandOnce`, which hit its 130 s limit while the whole suite ran at once; it passes in 9 s alone and in 3 of 3 runs of the package, so it is load-sensitive (like the `runner` test noted earlier) and not caused by this change. `golangci-lint run ./...`: 0 issues. The HA harness was not run.

## 2026-11-25 — F14-01, F14-02, G-13C-01 (branch audit/small-fixes)

- **F14-01, timestamps compared wrongly within a second.** `sqlx.Time` wrote RFC 3339 text without trailing zeros, so a whole second (`…:00Z`) sorted after the half second following it (`…:00.5Z`, since `Z` sorts after `.`). A SQL comparison such as `expires_at > now` was wrong for up to a second: an API token stayed valid up to 1 s past its expiry (224 ms in the regression test). Times are now fixed-width (`2006-01-02T15:04:05.000000000Z`); `ParseTime` still reads both forms. Tests: text order equals time order; a token that expired at a whole second is refused on both engines (it was accepted before the fix).
- **F14-02, the health check ignored flags and the bootstrap file.** `healthcheck` read only `MACHINARCHY_LISTEN_ADDR`, so a server started with `--listen-addr` or `--config` was probed on `:8080`. It now resolves the address as the server does (flag, environment, bootstrap file, default; `config.ListenAddr`, sharing the argument parser with `LoadCommandLine`), taking the flags after `healthcheck`. With only the environment variable nothing changes. `Run` takes the arguments now.
- **G-13C-01, a changed host key stayed Ready.** A run that failed on a host key (attached, or a detached launch) did not trigger a re-check, so the host showed `READY True` for up to a resync interval while every command to it failed. The worker now asks the Control Manager to reconcile that host (`Store.RequestHostRecheck` writes the same event a spec change does, changes no host field), at most once a minute per host so a bulk run writes one event, never one per target. Tests: the worker queues a recheck for a host-key failure and not for other errors (attached and detached), the throttle, and the store writes one event and ignores a missing host (both engines).
- **Docs:** the guide's note on the five-minute delay, and the health-check flags in `docs/operations.md`.
- **Checks run:** `go test ./...` with PostgreSQL required: pass (44 packages); `go test -race ./internal/operationworker/`: pass; `golangci-lint run ./...`: 0 issues.
- **Not run:** a live check with a real host whose keys change (the lab SSH servers were left alone) and the HA harness. Existing development databases keep working: old-format times are still read, but mix with the new format until rewritten, so on a long-lived development database the first-second comparison error can persist for old rows; new databases are exact.

## 2026-11-25 — Control Manager review: queue and resync fixes (branch audit/controlmanager)

Reviewed `internal/controlmanager` (engine, queue, host controller, metrics) and how bootstrap runs it. Five defects, each found by a failing test first:

- **Delayed retries used the queue capacity (Medium).** `ResourceQueue` counted keys waiting for their next check (every not-Ready host waits `managedHostResyncSeconds`) against `capacity` (default 1024). A fleet with more not-Ready hosts than that could not admit new events: the startup scan waited on a full queue, so the Control Manager never became ready, and outbox events were retried again and again. The capacity now bounds only the keys ready to run; a delayed retry is never refused (before, a refused one was dropped silently and waited for the next scan).
- **A key could be lost for good (Medium).** A key added while a worker processed it was marked dirty and queued when the worker finished, but only if the queue had room. When it was full the key stayed dirty and in neither place, so every later event for it was coalesced into nothing and the host was never reconciled again until a restart. It now goes to the scheduler as an immediate retry and runs when there is room.
- **Shutdown ran every pending retry (Low).** `Shutdown` moved all delayed keys into the queue, so a stop with many not-Ready hosts started an SSH check for each of them and used the whole drain timeout. They are dropped now; the next start scans everything anyway.
- **The scan could loop forever (Low).** The resync advanced its cursor only for hosts it queued, so a full page of hosts without a registered controller was listed again and again. It advances for every host.
- **A panic left no trace (Low).** A controller panic was recovered into the error "controller panic" with no value and no stack. It is logged now (controller, resource, panic value, stack); the retry behaviour is the same.
- **Looked at, left as is:** a lease contention is counted as a successful reconcile in `reconciles_total` (rare, because only one replica receives an event); `queue_depth` includes delayed keys; `Get` leaves a cosmetic `waiting` counter off by one when its context ends. The host controller, metrics and `cmd/controlmanager` were fine.
- **Tests:** five new tests (queue capacity with delayed keys, requeue after Done on a full queue, shutdown with delayed keys, scan past hosts without a controller, panic log). Each failed before its fix.
- **Checks run:** `go test -race` for the package, the new tests 15 times: pass; `go test ./...` with PostgreSQL required: pass (44 packages); `golangci-lint run ./...`: 0 issues. The HA harness was not run.

## 2026-11-25 — Restart-required runtime settings can be changed (F21-01; branch audit/restart-settings)

- **Problem:** `PUT /runtime-config` answered 422 for any change to a restart-required setting (worker counts, resync interval, queue limits, SSH timeout and concurrency, Operation workers and timeout), and nothing else set them, so they stayed at their defaults.
- **Now:** the revision is stored like any other. Hot-reloadable changes in it apply at once; the response (publish and rollback) lists the restart-required settings it changed in `restartRequired`; each process uses the new values from its next start. The 422 and `ErrRestartRequired` are gone.
- **Visibility:** each process records the revision it started with (`startedRevision`, new column `started_revision` in `runtime_config_process_status`). `GET /runtime-config/status` sets `restartPending` for a process whose startup revision differs from the current one in a restart-required setting. A rollback to the startup settings clears it.
- **Docs:** `docs/operations.md` has a "Restart-required settings" section (the settings, the rolling restart, the status fields) and the resync note no longer says it is fixed; API descriptions say the same. The HA scenario comment was updated; the scenario itself is unchanged.
- **Breaking for development databases:** the baseline gained a column, so a database created from the previous baseline must be recreated (same as the baseline merge on the same day).
- **Checks run:**
  - `go test ./...` with PostgreSQL required: pass, except `TestTimeoutWithoutProcessGroupKillsTheTree` (`runner`), which failed again only when run with the whole suite on a loaded machine and passed alone earlier; it is not related to this change;
  - a real monolith (SQLite): publishing a changed `controlManager.resyncSeconds` returned 201 with `restartRequired`, the running process showed `restartPending: true`, and after a restart the new process showed `startedRevision: 2` and `restartPending: false` (the stopped process's row stays for two minutes);
  - `golangci-lint run ./...`: 0 issues; `make api-docs`: only the new descriptions changed.
- **Not run:** the HA harness; the PostgreSQL path is covered by the contract tests only (the live check used SQLite).

## 2026-11-25 — HTTP API v1: one shape for packages, routes and errors (branch audit/v1-handlers)

- **Routes:** every package under `internal/transport/http/v1` now has a `routes.go` with `RegisterRoutes(v1 *echo.Group, h *Handler)` that mounts its own prefix and states its own access rule (permission, owner, human session). Before, five packages had a routes file, `agents` and `managedresource` registered in `handler.go` with other signatures (two groups; different parameter names), `environments` took a read and a write group, and `router.go` built the groups and their middleware itself. `router.go` is now a list: build the handler, mount the package. `auth` has two functions in `routes.go` (`RegisterRoutes` public, `RegisterSessionRoutes` including `/me`).
- **Router signature:** `RegisterRoutes` no longer takes `sessionKeys` and `sessionTTL` as separate parameters; they were also in `Repositories`, so the two could disagree.
- **Handlers:** the `agents` and `managedresource` methods were unexported (`list`, `createHost`, …) while every other package exports (`List`, `Create`); they are exported now.
- **Errors:** seven packages each had their own hand-written `switch` (`tokenError`, `memberError`, `auditError`, `configError`, `secretError`, `sessionError`, `mapError`) and `environments` and `auth` had inline ones. They all use `httperr.FromError` with a per-package `errorMappings` list and one `respondError`, and `httperr.Map` builds a mapping. **Bug fixed on the way:** the old mappers answered 500 without logging the cause, so the F20-06 fix ("log the cause of a 500 with the request ID") covered only the managed-resource routes; now every unexpected error is logged. Status codes and codes are unchanged; the 500 message is now always `request failed` (it was a different sentence per package).
- **Middleware:** `RequirePermission` and `RequireHuman` answered 403 when there was no principal at all; `RequireOwner` answered 401. All three answer 401 now. The JWT middleware already runs first in production, so only tests could reach this.
- **Principal access:** `auth` read the principal with `c.Get("principal")`; it uses `middleware.Principal` like the others.
- **Not changed:** request type names differ by package (`EnvSavePayload`, `CreateRequest`, `SetRoleRequest`, …); renaming them changes the OpenAPI schema names, so it needs its own change and an API docs regeneration. Package names are plural except `secret`, and `configuration` serves `/runtime-config`.
- **Tests:** the handler tests mount through the new `RegisterRoutes` on a `/api/v1` group; new test that the three middlewares answer 401 without a principal. The route authorization matrix and the end-to-end test pass unchanged, which is the evidence that access rules did not move.
- **Checks run:** `go test ./...` with PostgreSQL required: pass, except `TestTimeoutWithoutProcessGroupKillsTheTree` (`runner`), which failed once at a machine load average of 16 and passes alone and is not related to this change; `golangci-lint run ./...`: 0 issues; `make api-docs`: only enum lines reordered, not committed.

## 2026-11-25 — Migrations merged into one baseline (before the first release; branch audit/baseline)

- **Schema:** migrations `000002`-`000010` (agent enrolment tokens, host key observations, detached targets, user sessions, login failures, agent signing keys, capability Operations, host label index, host keyset index) are folded into `000001_baseline.up.sql` for SQLite and PostgreSQL. Columns and constraints that were `ALTER`ed in are now part of their `CREATE TABLE`; the SQLite table rebuilds and the label backfill are gone (a fresh database has nothing to backfill).
- **Breaking for development databases:** a database that recorded versions 2-10 is refused with `ErrIncompatibleSchema` and must be recreated (delete the SQLite file; drop and recreate the PostgreSQL database). From the first release (0.0.1) on, schema changes are additive migrations. `docs/operations.md` "Upgrades and schema" says so.
- **Tests:** the two upgrade-path tests (`migration8_test.go`, the migration 9 backfill test) are removed, since the migrations they upgraded through no longer exist. Their lasting checks are kept: `TestSQLiteBaselineOperations` (Capability kind and `Lost` state accepted, deleting an Operation cascades to its targets) and the existing label-selector contract test. The idempotence test now expects one applied migration. Comments naming old migration numbers were updated.
- **Verification that nothing changed in the schema:**
  - PostgreSQL: the old chain and the new baseline, built in two scratch databases, give identical columns, indexes and constraints (411 rows compared);
  - SQLite: identical tables (columns, foreign keys), indexes and CHECK clauses;
  - column order inside a table was not compared on PostgreSQL.
- **Checks run:** `go test ./...` with PostgreSQL required: pass; `golangci-lint run ./...`: 0 issues.

## 2026-11-25 — Release channels: alpha and beta pre-releases (CI; branch audit/ci)

- **First version:** `initial-version` is `0.0.1` and the manifests start empty, so the first tags are `v0.0.1-alpha.1`, `v0.0.1-beta.1` and `v0.0.1`. Unverified until Release Please runs.
- **Channels by branch:** `main` releases stable versions (`v1.3.0`), `beta` releases `v1.3.0-beta.N`, `alpha` releases `v1.3.0-alpha.N`. Release Please adds the suffix itself (prerelease versioning); nobody types it. How it works and how to promote alpha → beta → main is in `.github/WORKFLOWS.md`, "Release channels".
- **`release.yml`:** selects the channel from the branch, passes per-channel config and manifest files to Release Please (`.github/release-please/config.*.json`, `manifest.*.json`, with separate changelogs so branch merges do not conflict), refuses a tag whose suffix does not match its branch, and starts pre-release notes with a "not for production" warning.
- **`ci.yml`:** runs on pushes to `main`, `beta` and `alpha`, and creates releases from all three.
- **`template-docker.yml`:** a pre-release image gets no `latest` and no `major.minor` tag (it keeps its exact tag and the branch tag `beta`/`alpha`).
- **Docs:** `WORKFLOWS.md` rewritten where it was stale (the "Before the first release" list described a review archive); `.github/SECURITY.md` says pre-releases get no security fixes.
- **Review of `.github`, not changed here:** scanner versions in `ci.yml` (govulncheck v1.1.4, gosec v2.22.4) predate Go 1.27; `actions/upload-artifact` and `actions/attest` are pinned by tag, not SHA; the workflows have never run on GitHub (all Phase 10).
- **Checks run:** all workflow YAML and release-please JSON parse; the tag-versus-branch guard and the tag regex were tested for stable, beta and alpha tags. **Not run:** the workflows themselves and Release Please (needs GitHub), actionlint (not installed). The prerelease version numbering follows Release Please's documented behavior and is unverified until the first run.

## 2026-11-25 — Security status, dev-only Compose defaults, CLI setup docs (Phase 21: S-20, docs; branch audit/final)

- **`docs/SECURITY.md` "Validation status":** rewritten. The old text said PostgreSQL, endpoint authorization and SSH policy were unproven; it now lists what the suite shows (the route authorization matrix, store contracts on both engines, SSH policy and host-key tests, the `test/ha` harness, govulncheck and gosec) and what is still not shown (production-like load, a penetration test, most alert paths).
- **Compose (S-20):** `docker-compose.yml` and `deployments/infra.docker-compose.yml` start with a "development only" header naming the plain HTTP, the default database password, PostgreSQL without TLS and unauthenticated NATS. No setting changed; container hardening and digest pinning remain open (S-20).
- **CLI docs:** `docs/cli/README.md` and `docs/cli/configuration.md` lead with `machinarchy login`; the token-by-hand setup stays for API tokens.
- **Checks run:**
  - `make vuln`: no unaccepted vulnerabilities in called code;
  - `docker compose -f docker-compose.yml -f deployments/infra.docker-compose.yml config -q`: valid;
  - `make gosec` could not run: the installed gosec binary was built with Go 1.26 and cannot load Go 1.27 packages. `make lint` runs gosec through golangci-lint (0 issues).

## 2026-11-25 — HA harness: the agent gateways share agent traffic (Phase 21: F20-09; branch audit/final)

- `test/ha`: a new scenario, run right after the baseline while both Control Managers are fresh, counts the lease renewals each gateway answers over 2 minutes and fails if either answers less than a fifth. It settles F20-09 (the Phase 20 counts were skewed by restarts).
- Result of the run: cm-1 36, cm-2 36 renewals; the NATS queue group shares agent traffic evenly.
- CONTRIBUTING lists the scenario; the harness now takes about 9 minutes.
- **Checks run:**
  - `MACHINARCHY_HA=1 go test -run 'TestHighAvailability/(baseline|gateways)' ./test/ha/`: pass, no process left running;
  - `golangci-lint run ./test/ha/...`: 0 issues.
  - The full harness was not re-run for this change.

## 2026-11-25 — CLI login, Access Profile and token commands (Phase 21: G-13C-03, part 2; branch audit/final)

- **`machinarchy login`:** prompts for the password without echo (`--password-stdin` for scripts; without a terminal it refuses to prompt) and stores the session token for the current context's user. A first login with `--server URL --context NAME` creates the cluster, user and context and makes it current when none is. The password is never stored or printed. The "no context" hint now starts with `login`.
- **Owner commands that needed `curl` before:**
  - `create profile NAME --connector ssh|agent` (`--username`, `--secret` by name or ID at its current version, `--auth`, `--host-key-policy`, `--supervisor`, `--description`) and `delete profile`;
  - `create token NAME --role reader|operator [--expires-hours N]`, `get tokens`, `delete token ID`;
  - `create agent-token --host NAME [--hostname H] [--ttl D]`, `get agent-tokens`, `delete agent-token ID`.
  - A new token is printed once; `-q` prints it alone so it can be piped into `config set-credentials --token-stdin`. The `get` lists never show token values.
- **Server:** a duplicate API token or environment name now returns code `already_exists` (it was `conflict`), so the CLI hint says to pick another name.
- **Docs:** the full user guide's steps 3, 4, 6 and 16 use `login`, `create profile` and `create token` instead of `curl`; README quick start and `docs/cli/commands.md` updated.
- **Checks run:**
  - `go test ./internal/cli/... ./internal/transport/...`: pass, with new tests for login (stored token, wrong password, no terminal, first login creating the context) and for every new command;
  - the guide's new commands, run against a scratch monolith (SQLite): login on an empty config, two profiles, token creation and the `-q` pipe, `get tokens`, re-login;
  - `golangci-lint run ./...`: 0 issues.

## 2026-11-25 — CLI rough edges (Phase 21: G-13C-03, part 1; branch audit/final)

- **Usage errors:** the hint now points at the help of the command that failed (`Run 'machinarchy logs operation --help' for its usage.`), not the list of all commands. Root-level errors keep the old hint.
- **`get hosts`:**
  - a new `REASON` column shows why a host is or isn't ready (`SSHVerified`, `HostKeyPendingApproval`, `AgentNotEnrolled`…), so the guide no longer needs `-o json | jq` for it;
  - an empty list prints `No resources found.` alone, not inside an empty table.
- **Breaking default output:**
  - `secret list` prints a table by default (`NAME ID PURPOSE VERSION UPDATED`);
  - `secret create` prints a line naming the new secret;
  - `-o json`/`-o yaml` give the JSON as before, and `-q` prints only IDs.
  - The guide's `jq` pipelines now pass `-o json`.
- **409 for a name that is taken:** the server's code is now `already_exists` (it was `conflict`), and the CLI hint says to choose another name instead of "retry with an up-to-date revision". ADR-034's code list is updated.
- **Docs:** the CLI guide's host tables now show the `REASON` column.
- **Checks run:**
  - `go test ./internal/cli/... ./internal/transport/...`: pass, with new tests for the command-specific hint, the secret table and JSON, the plain empty list, the reason column and the `already_exists` hint;
  - `golangci-lint run ./...`: 0 issues;
  - `make api-docs`: no change.

## 2026-11-25 — Agent host Ready follows enrolment and revocation at once (Phase 21: F17-04; branch audit/final)

- `store/agents`: binding a host (enrolment with a host-bound token) and revoking an agent each write a host event and its Control Manager delivery, in the same transaction, for every affected host. The host is reconciled within seconds, and its `Ready` condition (`AgentConnected`, `AgentNotEnrolled`) follows. Before, it waited for the next host resync, up to 5 minutes.
- `store/resources`: the outbox insert is shared as `InsertHostEvent` instead of being copied.
- A lease lapse is still noticed at the next resync; documented in `docs/operations.md`.
- **Test:** the store contract "capability operations" now checks one queued reconcile per binding and per revocation, on both engines. Before the fix: 0.
- **Checks run:**
  - `go test` for store, agentgateway, controlmanager and e2e: pass;
  - `golangci-lint run ./...`: 0 issues.

## 2026-11-25 — No accepted vulnerabilities left (Phase 21: D-14; branch audit/final)

- `scripts/security/govulncheck-allowlist.txt`: the entries for GO-2026-6875 and GO-2026-6872 are removed (owner approved), since Echo v4.16.6 fixes both. The file keeps its header; the script reads it.
- `docs/SECURITY.md`: "Accepted today: none".
- **Checks run:** `make vuln`: no unaccepted vulnerabilities in called code.

## 2026-11-25 — Faster resync takeover after a crash; recovery times documented (Phase 21: F20-08, F20-10; branch audit/final)

- **`controlmanager.Engine`:**
  - The cluster-wide resync lease lasts 1.25 × the resync interval, down from 2 × with a 2-minute floor. That still keeps one scan per interval cluster-wide.
  - A replica that finds the lease held now retries every quarter interval, at startup as well, instead of a full interval later. It is no longer logged as a failure.
  - After the holder crashes, the others resync within about 1.5 intervals (7.5 minutes by default), down from up to about 3.
- **Test:** `TestSurvivorTakesTheResyncLeaseSoonAfterItExpires`. Before the fix: the lease was taken after 1.03 s with a 1 s interval, and the TTL was 2 minutes. After: well under an interval and a TTL of 1.25 intervals; passes 5 of 5.
- **HA harness:** a new "SIGKILL during an outbox burst" scenario, sharing the burst code with the `SIGTERM` one. It passes, with all 120 hosts reconciled 5 s after the burst. In this run the killed replica had nothing queued, so it shows no host is lost; the resync takeover itself is covered by the unit test.
- **Docs:** `docs/operations.md` "High availability" lists each failure with its recovery time (F20-10).
- **Found on the way (F21-01, open):** the runtime-configuration API refuses any change to a restart-required setting (`controlManager.resyncSeconds`, worker counts, database settings), and nothing else sets them. They keep their defaults. Documented; recorded in AUDIT/21.
- **Checks run:**
  - `go test ./internal/controlmanager/ ./internal/bootstrap/`: pass;
  - `MACHINARCHY_HA=1` baseline and both burst scenarios: pass;
  - `golangci-lint run ./...`: 0 issues.

## 2026-11-25 — Lint 0, duplication cut, dead interface removed (Phase 21: D-15, Q-22, Q-23; branch audit/final)

- **Full `golangci-lint run ./...`: 15 issues → 0.**
  - 13 test uses of the deprecated `secrets.StaticKeyProvider` now use `LocalProvider`, and the alias is removed (no production user).
  - `api.APIError` is renamed `api.Error` (revive stutter). The package is internal, so no alias is needed.
  - One unused test parameter fixed.
- **Duplication** (the Phase 5 method: golangci-lint with `dupl` threshold 60): production fragments 26 → 20. The real duplications are extracted:
  - the gateway's two registration refusals → one `refuse` closure;
  - the poller's two live-output writes → `storeLive`;
  - the expired-Operation check in the run and capability paths → `Worker.expireIfPastDeadline`;
  - the agent store's row-scanning loops → generic `collect`.
- **The 20 remaining fragments are justified:**
  - Swagger annotation blocks around one-line handlers that already share `call`/`bind` (managed-resource handler 8, secret handler 2);
  - Cobra command declarations over the existing `managementRead` helper (CLI 6);
  - two typed `Submit…ForRule` entry points (2);
  - two 8-line queries of different tables (2).
- **Q-23:** `controlmanager.Queue` was declared and never used (the engine uses `*ResourceQueue`) and is removed.
- **Not run:** the `deadcode` tool (not installed, and downloading it was not approved). The `unused` linter reports nothing. `unparam` reports 6 test-helper parameters only.
- **Checks run:**
  - `golangci-lint run ./...`: 0 issues;
  - `go test` for secrets, cli, controlmanager, agentgateway, operationworker, store, hostaccess, service and transport: 31 packages pass.

## 2026-11-25 — ADR-034 amended to the implemented API design (Phase 21: D-15; branch audit/final)

- **Owner decision: amend rather than implement.** ADR-034 now records what the API does:
  - keyset pagination (`limit`, `cursor`, `{items, nextCursor}`);
  - the expected revision in the request (`revision`, `expectedRevision`, `expectTargetsHash`), with `409 conflict` or `targets_changed`;
  - `{error, code}` errors with the request ID in `X-Request-Id`, and unexpected errors logged (F20-06).
- RFC 9457 problem details and ETag/`If-Match` are deferred as an additive option. The ADR index row is updated.
- Docs only; no checks run.

## 2026-11-25 — `monolit` renamed to `monolith` (Phase 21: D-15; branch audit/final)

- **Renamed (owner decision):**
  - `cmd/monolit` → `cmd/monolith`;
  - binary `machinarchy-monolith`;
  - make targets `build-monolith`, `run-monolith`, `image-monolith`;
  - Compose service `monolith`;
  - telemetry service name `machinarchy-monolith`;
  - CI image and container names.
- **The old names keep working for one release:**
  - `make build` writes `bin/machinarchy-monolit` as a symlink, which `scripts/smoke.sh`, left unchanged, uses;
  - `build-monolit`, `run-monolit` and `image-monolit` print a deprecation note;
  - the image build and the Docker workflow template accept `MACHINARCHY_COMMAND=monolit`.
- **Docs:** README, architecture, operations (with a deprecation note), the CLI guide and troubleshooting, the development docs and examples.
- **Checks run:**
  - `go build ./...` and `make build`;
  - `make smoke`: OK, through the old binary name;
  - `go test ./internal/bootstrap/ ./test/e2e/`: pass;
  - `docker buildx build --check` with `MACHINARCHY_COMMAND=monolit`: no warnings.

## 2026-11-25 — Echo v4.16.6; Echo is kept (Phase 21: D-14; branch audit/final)

- `github.com/labstack/echo/v4` v4.16.4 → v4.16.6, which fixes GO-2026-6875 and GO-2026-6872. Both had been allowlisted as not exploitable here.
- ADR-058 (owner decision): keep Echo and upgrade when fixes appear; migrate to `net/http` only if Echo stops shipping security fixes.
- `make vuln` reports both allowlist entries as no longer reported. They are not removed yet: the file is under `scripts/` and needs the owner's go-ahead.
- **Checks run:**
  - `govulncheck ./...`: no vulnerabilities found;
  - `make vuln`: no unaccepted vulnerabilities;
  - `go test` for transport, bootstrap and e2e: pass.

## 2026-11-25 — Runner test waits long enough under load (Phase 20; branch audit/ha)

- `TestTimeoutWithoutProcessGroupKillsTheTree` waited 5 s for the supervisor to record its branch. In the full suite, with every package running in parallel, that wasn't enough, and the test failed once. It now waits up to 20 s, like the file's other bounded waits.
- **Checks run:**
  - the runner F20-07 tests with `-count=3`: pass;
  - full `go test ./...` with PostgreSQL required: pass, with no process left behind.

## 2026-11-25 — CONTRIBUTING: audit rule and HA harness (Phase 20; branch audit/ha)

- The audit rule now matches F20-05: intents and Operation outcomes are audited; system-driven progress is not.
- How to run `test/ha` (`MACHINARCHY_HA=1`, Docker, about 7 minutes, `MACHINARCHY_HA_LOG_DIR`).
- Docs only; no checks run.

## 2026-11-25 — HA verification harness (Phase 20: D-16; branch audit/ha)

- **`test/ha`:** real binaries in a multi-replica topology, all started at once.
  - PostgreSQL in its own Docker container on a named volume, so a restart keeps the data;
  - NATS in operator mode, with a cuttable TCP proxy in front of each Control Manager;
  - 2 API replicas behind a load balancer with failover;
  - 2 Control Managers;
  - 3 agents with host-bound tokens;
  - 6 SSH hosts on 3 test servers.
  
  It runs only with `MACHINARCHY_HA=1` and needs Docker. `MACHINARCHY_HA_LOG_DIR` keeps the process logs of a failed run. `MACHINARCHY_HA_LOAD_KINDS` limits the load mix.
- **Scenarios:** baseline; `SIGKILL` a Control Manager mid-Operation (detached, attached, capability burst); restart PostgreSQL; partition one Control Manager from NATS for 30 s; `SIGTERM` during a 120-host burst; a one-minute load profile. The load profile takes latency from the server's timestamps and samples `pg_stat_activity` and the audit-lock holders.
- **Findings fixed in separate commits:** F20-01 to F20-07 (see their entries).
- **Checks run:** `MACHINARCHY_HA=1 go test -v ./test/ha/` on a laptop (i7-9750H, Docker Desktop): every scenario passes, in 414 s.
  - Kill: detached `Succeeded` with one launch per target; attached `Interrupted`; capability burst `Succeeded` with one request per target.
  - PostgreSQL restart: ready again in 3.2 s.
  - NATS partition: capability Operations in 2.2 s.
  - Burst: all 120 hosts reconciled 5 s after it.
  - Load: 82 Operations per minute.

    | Kind | p50 | p95 |
    |---|---|---|
    | Attached `uptime` (6 hosts) | 690 ms | 910 ms |
    | Capability (3 agents) | 1.0 s | 1.13 s |
    | Detached `sleep 1` (6 hosts) | 7.3 s | 7.5 s (5 s first poll interval) |

  - Without `MACHINARCHY_HA` the package skips (`go test ./test/ha/`: ok).

## 2026-11-25 — Lease owner IDs are logged at startup (Phase 20; branch audit/ha)

- The Operation worker logs `operation worker started` with `owner_id`, and the Control Manager engine logs `control manager engine started` with `instance_id`. These are the `owner_id` values written to `resource_leases` and to outbox claims, so an operator, or the HA harness, can tell which process holds a lease. Before, they were random IDs that appeared nowhere else.
- Checks run: `go build ./...`; the HA harness uses both lines to find the lease holder before killing it.

## 2026-11-25 — Detached-run supervisor always ends and enforces its timeout (Phase 20: F20-07; branch audit/ha)

- **Found during the HA load profile:** the laptop's load average was about 860, with 75 % system CPU. Nineteen detached-run supervisors (`wrapper`) left by earlier test runs had been looping on `ps` and `awk` for up to 4 hours. On a real host, the same supervisor would burn CPU forever.
- **Two defects in `internal/hostaccess/runner/runner.sh` (owner approved editing it):**
  1. **Tree walk never ended.** Without a process group, `signal()` walks the process tree. Its work list always carried a separator, so `[ -n "$todo" ]` never became false, and the loop ran `ps` and `awk` forever. It now stops when `set -- $todo` leaves no pid.
  2. **Timeout not enforced once the directory was gone.** The watchdog marked the timeout with `: >"$d/timedout"`. A failed redirection on the special built-in `:` ends a POSIX shell, so with the state directory removed, the watchdog exited and never signalled the command.
     - The marker is now written with `printf`.
     - Whether the command has its own process group is now kept in a variable instead of re-read from the directory, so the whole group is still signalled.
- **Tests (both fail against the old script):**
  - `TestSupervisorEndsWhenItsDirectoryIsGone`: the command and supervisor still ran 25 s after the timeout.
  - `TestTimeoutWithoutProcessGroupKillsTheTree`: a fake `ps` forces the tree walk; the supervisor never ended.
  - Both run on `sh` and `dash`.
- **Test hygiene:** the runner and detached-worker tests launched commands with a 600 s timeout and left them running when the test ended. Their cleanup now kills every process under the test's temporary home. A full run of both packages leaves no process behind (checked).
- **Checks run:**
  - `go test ./internal/hostaccess/... ./internal/operationworker/`: pass;
  - lint: 0 issues.

## 2026-11-25 — The cause of an unexpected 500 is logged (Phase 20: F20-06; branch audit/ha)

- **Found by the HA load profile:** 62 `POST /api/v1/managed/operations` requests failed with `500 request failed`, each after exactly 25 s (the request deadline). The API logs had only the status line: `httperr.FromError` turned unknown errors into a generic 500 without recording the cause anywhere.
- **Fix:** `FromError` logs an error with the request ID, method, route and the cause. The client still gets no detail; the request ID in the response leads to the log line.
- **Test:** `TestUnexpectedErrorsAreLogged`. Before the fix nothing was logged.
- **Checks run:** `go test ./internal/transport/http/...`: pass. Lint: 0 issues.

## 2026-11-25 — The audit trail records intents and outcomes, not system progress (Phase 20: F20-05; branch audit/ha)

- **Found by the HA load profile:** at about one Operation per second plus the periodic host resync, both Control Managers timed out on `lock audit chain`. Statements hit the statement timeout, agent results were "not stored; redelivered" (adding load), and submissions failed with 500. Each audited write takes the workspace's audit-chain lock until it commits, and system-driven progress was audited:
  - every host reconcile (`resource_host.updated`, every 5 minutes for every host, contrary to SECURITY.md);
  - an Operation starting;
  - every target transition;
  - every detached launch;
  - every capability dispatch.
  
  A larger connection pool made it worse.
- **Owner decision:** audit intents and outcomes only.
- **No longer audited:** a reconcile's status update (`ObserveHost`), Operation start, `UpdateOperationTarget`, `RecordTargetLaunch`, `DispatchCapabilityTarget`. Their state is in the rows.
- **Still audited:**
  - every API or human action;
  - an Operation's creation, cancel request and final outcome;
  - a host presenting a changed key (`resource_host.host_key_changed`);
  - TOFU and owner key trust;
  - host creation, update and deletion.
- **Docs:** `docs/SECURITY.md` audit section now matches the code.
- **Test:** store contract "audit records intents and outcomes" (both engines). Before the change it failed with `[resource_host.created resource_host.updated]` after one reconcile.
- **Checks run:**
  - `go test` with PostgreSQL required for store, service, operationworker, controlmanager and hostaccess: pass;
  - lint: 0 issues.
- **Not yet re-measured:** the load profile after this change is recorded in AUDIT/20.

## 2026-11-25 — A stopping Control Manager waits for its components (Phase 20: F20-04; branch audit/ha)

- **Found by the HA harness:** after a graceful `SIGTERM` to one of two Control Managers during a burst of 120 host creations, 5–8 hosts were never reconciled, not even after 7 minutes.
  - Their outbox events were acknowledged, some still had host leases from the stopped replica, and the stopped replica still held the cluster-wide resync lease (10-minute TTL). The surviving replica therefore skipped its periodic resync.
  - The stopped replica's log showed no drain and no lease release.
- **Root cause:** `bootstrap.combinedWorkers.Start` (the engine, the Operation worker and the agent gateway) returned as soon as the context ended. `ctx.Done()` was ready on every pass of its wait loop, so it counted out the components without waiting for any of them.
  - The process then closed the database and exited while the engine was still draining its queue and before it released its leases.
  - Every graceful stop of a Control Manager or monolith was affected.
- **Fix:** the loop waits for every component's result; the end of the context only triggers the cancellation, once. `runComponents` still bounds the whole wait with the shutdown budget and leaves the database open if a component overruns it.
- **Test:** `TestCombinedWorkersWaitForEveryComponent`, with a component that takes 300 ms to stop. Before the fix, `Start` returned with that component still running.
- **Checks run:** `go test ./internal/bootstrap/`: pass. Lint: 0 issues.

## 2026-11-24 — The first owner is created in one transaction (Phase 20: F20-03; branch audit/ha)

- **Found by the HA harness (intermittent):** replicas starting together on an empty database all run `BootstrapOwner`. The winner created the user as a reader and promoted it to owner in a second transaction. A replica that checked in between saw an existing account that was not an owner, and stopped with `bootstrap username "admin" already exists but is not a workspace owner`. A winner crashing in that window would have left every later start failing the same way.
- **Fix:**
  - `accounts.Store.CreateOwner` writes the user, the owner membership and the audit event in one transaction.
  - `BootstrapOwner` uses it when the store provides it, and otherwise keeps the two-step path (test fakes).
  - `CreateUser` shares the code.
- **Test:** `TestConcurrentBootstrapOwner`: 8 concurrent bootstraps per round, 3 rounds, on PostgreSQL. Before the fix every run had failing replicas; after it, 3 runs (9 rounds) produced exactly one owner and no errors.
- **Checks run:**
  - `go test ./internal/service/` and the store test with PostgreSQL: pass;
  - lint: 0 issues on the changed packages.

## 2026-11-24 — API replicas serve the agent signing keys (Phase 20: F20-02; branch audit/ha)

- **Found by the HA harness:** in a split deployment (API replicas plus Control Managers), `GET /api/v1/agents/controller-key` and every signing-key rotation endpoint (Phase 16) returned 503 from the API replicas. Only processes that run the agent gateway had a key manager, so agents could not be set up and keys could not be rotated through the API. It worked only in the monolith.
- **Fix:**
  - `agentkeys.NewPublicOnly` reads the key list, follows changes (5 s refresh), and stages, activates and discards keys. It never resolves a private seed.
  - Bootstrap builds one in every process without a gateway.
  - Until a gateway has created a key, the endpoints answer 503 and the refresh loop logs nothing.
- **Test:** `TestPublicOnlyManagerServesTheAPI`: an API-side manager stages and activates; the Control Manager follows; the API side made zero seed reads.
- **Checks run:** `go test ./internal/agentkeys/ ./internal/bootstrap/`: pass. Lint: 0 issues.

## 2026-11-24 — Replicas starting together migrate in turn (Phase 20: F20-01; branch audit/ha)

- **Found by the HA harness:** with two API replicas and two Control Managers starting at once on an empty PostgreSQL database, one replica crashed. The error was `migrate postgres database: duplicate key value violates unique constraint "pg_type_typname_nsp_index"`: two processes created the same table at the same moment. Deployments start replicas together on every rollout.
- **Fix:** `store.Migrator.Up` takes the session advisory lock `pg_advisory_lock` on a dedicated connection on PostgreSQL before it reads anything.
  - Each replica waits its turn, then finds the migrations already applied.
  - The lock is released explicitly; the database also releases it if the process crashes and its connection closes.
  - SQLite is unchanged: one process per database.
- **Test:** `TestConcurrentPostgresStartup` starts 4 stores at once on a fresh schema. It failed on every run before the fix; it passed 5 of 5 after.
- **Checks run:**
  - `go test ./internal/store/` with PostgreSQL required: pass;
  - lint: 0 issues.

## 2026-11-24 — Egress policy checks IPv4 inside NAT64, 6to4 and Teredo (Phase 19: S-19; branch audit/data-path)

- `pkg/netpolicy`: `CheckAddr` now decodes the IPv4 address that an IPv6 address carries and checks it under the same policy (metadata addresses, Deny ranges, loopback, link-local, private). The IPv6 address is refused if that IPv4 address would be.
  - NAT64 `64:ff9b::/96` (RFC 6052);
  - 6to4 `2002::/16` (RFC 3056);
  - Teredo `2001::/32` (RFC 4380): both the server and the inverted client address.
- **Before:** for example, `64:ff9b::a9fe:a9fe` (the metadata address 169.254.169.254) and `2002:c0a8:101::1` (192.168.1.1) counted as public IPv6 addresses and were allowed.
- **Tests:**
  - `TestEmbeddedIPv4IsChecked`: 11 denied cases (all allowed before the fix) and 4 allowed ones, including `2001:db8::1`, which is outside the Teredo range.
  - New `FuzzEmbeddedIPv4`: 20 s, no failures.
  - `FuzzParsePrefixes` corpus extended with the NAT64, 6to4 and Teredo prefixes: 10 s, no failures.
- **Checks run:** `go test ./pkg/... ./internal/hostaccess/... ./internal/config/`: pass. Lint: 0 issues.

## 2026-11-24 — Database statement timeout below the request deadline (Phase 19: Q-20; branch audit/data-path)

- **`store.Config.StatementTimeout`:**
  - PostgreSQL: set as the `statement_timeout` runtime parameter of every connection. The DSN is now parsed with `pgx.ParseConfig` and opened with `stdlib.OpenDB`.
  - SQLite: the value becomes the busy timeout.
  - Migrations run `SET LOCAL statement_timeout = 0`; `MACHINARCHY_DATABASE_MIGRATION_TIMEOUT` still bounds them.
- **Bootstrap:** derives the value with `HTTPConfig.StatementTimeout()`: the request deadline minus 5 s, and at least half of it. That is 20 s for the default 25 s, so a slow statement ends in the database before the response is abandoned.
- **Tests:**
  - `TestPostgresStatementTimeout`: every new connection reports `300ms`, and `pg_sleep(3)` fails with SQLSTATE 57014 within 2 s.
  - `TestSQLiteBusyTimeoutFollowsStatementTimeout`.
  - `TestStatementTimeoutStaysBelowTheRequestDeadline`.
  - Before the fix: `statement_timeout = "0"` and busy timeout 5000.
- **Docs:** the `docs/operations.md` Timeouts table gains the statement row. It also replaces the stale "Synchronous agent call" row (`invoke` was removed in Phase 17) with the capability timeout.
- **Checks run:**
  - `go test` with PostgreSQL required for store, bootstrap, config, operationworker and e2e: pass;
  - lint: 0 issues.

## 2026-11-24 — Outbox claims are released within a bound on stop (Phase 19: Q-18; branch audit/data-path)

- **`controlmanager.Engine.dispatchBatch`:** when the dispatcher stops with deliveries claimed, it hands them back through one shared context. That context is detached from the cancelled stop context and cancelled `stopReleaseTimeout` (2 s) after the first release.
  - Before, one path used `context.Background()` with no timeout, so a hung database blocked shutdown forever. The `QueueShuttingDown` path passed the cancelled context, so its release always failed at once.
  - A failed release is logged; the delivery is reclaimed after its lease.
- **Test:** `TestStoppingDispatcherReleasesClaimsWithinABound`, with an outbox whose `Retry` hangs.
  - Before the fix: `dispatchBatch` never returned (test timeout after 3 s).
  - After: 3 claims released within the shared bound, and the first release didn't start cancelled.
  - Also run with `-race -count=3`.
- **Checks run:** `go test ./internal/controlmanager/`: pass. Lint: 0 issues.

## 2026-11-24 — Runtime-config apply rolls back only what it applied (Phase 19: Q-16; branch audit/data-path)

- `runtimeconfig.PreparedApply` documents its contract and gains an optional `Abort`:
  - `Rollback` undoes a `Commit`;
  - `Abort` releases a prepared change that will never be committed.
- **`SnapshotManager.Reload`:**
  - A failed Commit at participant i rolls back i down to 0, newest first, and aborts the participants after i. Before, it rolled back every prepared participant, including ones never committed.
  - A failed Prepare aborts the prepared participants. Before, it rolled them back.
  - The leftover `_ = i` is gone.
- **Tests:**
  - `TestCommitFailureRollsBackOnlyWhatWasApplied` and `TestPrepareFailureAbortsWithoutRollback` check the exact call order. Both failed before the fix (`c.rollback` on a never-committed change; rollbacks after a failed Prepare).
  - `TestApplyIsAllOrNothing` now expects no rollback when nothing was committed.
- **Checks run:** `go test ./internal/runtimeconfig/ ./internal/bootstrap/`: pass. Lint: 0 issues.

## 2026-11-24 — Host resync pages through an index (Phase 19: Q-08; branch audit/data-path)

- **Migration 000010 (both engines):** `idx_managed_hosts_keyset ON managed_hosts(workspace_id, id)`.
- **`ListHostKeys`:** uses the row-value predicate `(workspace_id, id) > (?, ?)` instead of `workspace_id > ? OR (workspace_id = ? AND id > ?)`. The query is exported as `resources.HostKeysQuery` so a test can check its plan.
- **Before:**
  - SQLite: `MULTI-INDEX OR` plus `USE TEMP B-TREE FOR ORDER BY`;
  - PostgreSQL: `Incremental Sort` over a scan that filtered every row.
- **After:**
  - SQLite: `SEARCH managed_hosts USING COVERING INDEX idx_managed_hosts_keyset ((workspace_id,id)>(?,?))`;
  - PostgreSQL: `Index Only Scan using idx_managed_hosts_keyset`, `Index Cond: (ROW(workspace_id, id) > ROW(...))`.
- **Test:** store contract "host resync keyset uses an index" on both engines (PostgreSQL with sorts and sequential scans disabled). It failed before the fix on both.
- **Checks run:**
  - `go test ./internal/store/... ./internal/controlmanager/...` with PostgreSQL required: pass;
  - lint: 0 issues.

## 2026-11-24 — SQLite connection settings survive a reconnect (Phase 19: Q-10; branch audit/data-path)

- `internal/store/sqlite`:
  - `foreign_keys(1)` and `busy_timeout` are now DSN `_pragma` parameters, so the driver applies them to every connection it opens. Before, they were set with `Exec` on whichever connection ran it, and a reopened connection had foreign keys off and no busy timeout.
  - `Config.BusyTimeout`, default 5 s.
  - `Configure` keeps enabling WAL (stored in the file) and now verifies that foreign keys are on.
  - A database path containing `?` or `#` is refused, since the path becomes a DSN.
- **Tests:**
  - `TestPragmasSurviveReconnect` forces a new connection for every query. Without the fix it fails with `foreign_keys=0 busy_timeout=0`.
  - `TestOpenDBRefusesAQueryInThePath`.
- **Checks run:** `go test ./internal/store/sqlite/ ./internal/store/`: pass. `golangci-lint run --new-from-rev=HEAD ./...`: 0 issues.

## 2026-11-24 — Label selectors run in the database (Phase 18: A-12; branch audit/scale)

- **Migration 000009 (SQLite and PostgreSQL):** `managed_host_labels(host_id → managed_hosts ON DELETE CASCADE, workspace_id, label_key, label_value)`.
  - Primary key `(host_id, label_key)`; lookup index `(workspace_id, label_key, label_value, host_id)`.
  - Backfilled from `managed_hosts.labels` (`json_each` / `jsonb_each_text`).
  - The store rewrites the rows only in the transaction of a label change (`UpdateHostMetadata`) or a host insert; status updates don't touch them.
- **Domain:**
  - `Selector.LabelConstraints` turns label requirements into conditions every match satisfies: `=`/`in` become `LabelIn` (`LabelExists` with wildcards), `!=`/`notin` become `LabelNotIn` (nothing with wildcards), plus existence and non-existence.
  - `resource.IsHostBuiltinField`.
  - `HostListFilter` gains `Labels` and `Lookup` (ID or name, ignoring case).
- **Store:** positive conditions use `id IN (SELECT host_id …)` through the lookup index; negative ones use `id NOT IN (…)`.
- **Service:**
  - Host listings with a selector page through the database's candidates and apply the selector exactly. The 5,000-row scan cap is gone.
  - Previews and submissions load only the candidates: every host for `all`, label-filtered hosts that match the selector, and the hosts named in include and exclude. The 10,000-host limit now counts matched hosts, not the whole inventory.
- **Tests:**
  - store contract on both engines: 14 selector shapes produce the same result through SQL candidates as the selector alone; label changes and deletes update the index; workspace isolation; ID or name lookup; paging;
  - migration 9 backfill test (SQLite).
- **Benchmark** `BenchmarkHostSelectors`, laptop (i7-9750H), 10 iterations each:

  | Engine, hosts | Selective list page | Selective preview | `canary,tier!=batch` preview | No label requirement (full scan) |
  |---|---|---|---|---|
  | SQLite, 10k | 16 ms | 13 ms | 10 ms | 0.30 s |
  | SQLite, 50k | 81 ms | 135 ms | 55 ms | 1.31 s |
  | PostgreSQL, 10k | 14 ms | 9 ms | 38 ms | 0.38 s |
  | PostgreSQL, 50k | 54 ms | 57 ms | 150 ms | 1.89 s |

  The selective selector is `tier=db,zone=z9`, 1 % of hosts. Every case finishes well within the 25 s request deadline. Before this change, a preview over an inventory larger than 10,000 hosts failed.
- **Docs:** `docs/operations.md` "Upgrades and schema" (migration 9, selectors at scale).
- **Checks run:**
  - `go test` with PostgreSQL required for store, service, transport, domain, hostaccess and bootstrap: pass;
  - `golangci-lint run --new-from-rev=HEAD ./...`: 0 issues.

## 2026-11-24 — Attached runs fan out to RunSpec.Concurrency (Phase 18: D-10; branch audit/scale)

- **Operation worker:** attached (non-detached) runs used to execute their targets one after another, whatever `RunSpec.Concurrency` said. They now run `Concurrency` targets at a time (1–20); the SSH capacity limiter still caps the total.
  - Before each launch, the worker checks the deadline, cancellation and its own shutdown.
  - After a stop, no new target starts. Running targets finish (cancel closes their sessions), and the Operation is finished only after all of them have returned (`Cancelled`, `TimedOut` or `Interrupted`, as before).
  - The per-target body moved into `runAttachedTarget`, unchanged.
- **Capability targets:** already dispatched all at once (each agent runs on its own host); documented.
- **CLI:**
  - `run --concurrency N` (1–20);
  - the default is 5 with `--wait` (was 1, so attached CLI runs never fanned out) and 20 for detached launches;
  - both are capped by the host count.
- **Tests:**
  - with a fake connector at 300 ms per target: 12 targets at 4 took 930 ms (ideal 900 ms, sequential 3.6 s); 5 at 1 took 1.53 s; 3 at 20 took 310 ms; peak concurrency equalled the limit;
  - cancel mid-run starts no further target and ends `Cancelled`;
  - a worker stop leaves no target `Running` and ends `Interrupted`;
  - CLI `--concurrency` defaults and limits.
- **Checks run:**
  - `go test -race -count=3` for the fan-out and capability worker tests: pass;
  - `go test` for operationworker, hostaccess (real SSH), service, bootstrap, cli and e2e: pass;
  - `golangci-lint run --new-from-rev=HEAD ./...`: 0 issues.

## 2026-11-24 — Execution policy checks arguments (Phase 18: D-11/S-12; branch audit/scale)

- **Policy model (owner decision):** argument templates. Each policy entry is a program followed by one pattern per argument:
  - literals;
  - placeholders `{unit}`, `{word}`, `{int}`, `{path}` and `{any}`, none of which matches an argument starting with `-`;
  - an optional final `*` for any remaining arguments.

  A command must match one of its program's entries. `ExecutionPolicy.Check` takes the whole argv, and the service and the worker (at submit and at run time) call it that way.
- **Breaking:** a bare program name used to allow any arguments; it now allows none. `PROGRAM *` restores the old behaviour for one program. `docs/operations.md` "Execution policy" has the upgrade note and the runtime-config command to change the policy.
- **Refusals:** a refused command now gets HTTP 403 `execution_not_allowed` instead of 500 (guide known problem, G-13C). The message names the program and its allowed entries, never the arguments. The CLI hint explains how an owner adds an entry.
- **Runtime configuration:** entries may contain spaces, and validation errors say what is wrong with an entry.
- **Bootstrap:** seeds the runtime configuration from the normalized entries.
- **Quoting:** `domain.QuoteArgv` remains the only quoting implementation (unified in Phase 5). A new test sends hostile arguments through a real `/bin/sh`.
- **Tests:**
  - template matching with 20 argument-abuse cases (AUDIT/02 S-12);
  - parsing errors;
  - a new fuzz target, `FuzzArgumentTemplates`: about 950k runs in 20 s, no failures;
  - `FuzzQuoteArgv`: 10 s, no failures;
  - service refusal of allowed-program-but-wrong-arguments;
  - the HTTP mapping and the CLI hint.
- **Docs:**
  - operations guide "Execution policy";
  - SECURITY.md;
  - `.env.example`;
  - the guide's lab policy, now quoted because entries contain spaces, plus its known-problem note.
  - The guide's lab policy was checked by sourcing it and matching every command the guide runs.
- **Checks run:**
  - `go build ./...` and `go vet ./...`: clean;
  - `golangci-lint run --new-from-rev=HEAD ./...`: 0 issues;
  - `go test ./internal/... ./cmd/...`: pass.

## 2026-11-24 — Docs for capability Operations (Phase 17; branch audit/capability-ops)

- `docs/operations.md`:
  - "Enrolling a host" covers agent Access Profiles, agent hosts without an endpoint, and `hostId` tokens.
  - New "Capability Operations" section: submission, status mapping, `Lost`, the dispatch error classes, and agent-host `Ready` reasons.
  - The `invoke` note is gone.
- `docs/cli/commands.md`: `create host` without `--endpoint` for agent hosts; `run --capability`.
- `README.md`: the API table drops `/{id}/invoke`.
- Known limitation (documented): an agent host's `Ready` condition updates at the next host resync (5 minutes by default) after its agent enrols. Dispatch does not depend on it.
- Docs only; no checks run for this entry.

## 2026-11-24 — E2E tests run capability Operations (Phase 17; branch audit/capability-ops)

- `test/e2e`: both tests use capability Operations instead of the removed `POST /api/v1/agents/{id}/invoke`. New helpers are in `test/e2e/capability_test.go`.
- `TestAgentEnrolsAndRunsCapabilitiesThroughMachinarchy`:
  - two agent hosts, each enrolled with its own host-bound token;
  - `runtime.info` over both, with per-target output checked;
  - `shell.exec` fails with the agent's code;
  - after revocation, the host's target fails `agent_not_bound`;
  - the second agent stops cleanly.
- `TestControllerSigningKeyRotation`: an Operation submitted just before a controller restart finishes `Succeeded` after it.
- Checks run: `go test -count=1 ./test/e2e/` passes in 15.7 s when run alone. It took 40–80 s when other packages ran at the same time, which is why it is committed separately from the code.

## 2026-11-24 — Agent capabilities run as Operations (Phase 17: A-05, M-7, M-8, M-9; branch audit/capability-ops)

Implements ADR-057.

- **Domain:**
  - `OperationKind=Capability` with `CapabilitySpec{name, input, timeoutSeconds}`: the name is lowercase words joined by dots, the input is a JSON object of at most 16 KiB, and the timeout is 1–3600 s.
  - `Run` stays SSH-only.
  - Access Profile connector `agent`, which takes no username, credential, host-key or execution settings.
- **Migration 000008 (SQLite and PostgreSQL):**
  - the `Capability` operation kind;
  - `agent_enrollment_tokens.host_id`;
  - `agent_host_bindings`;
  - `operation_agent_commands`.
  - SQLite rebuilds `operations` and `operation_targets` together (copy both, then drop): rebuilding only the parent would cascade-delete targets. `TestSQLiteMigration8KeepsOperations` covers it.
- **Host binding:**
  - an enrolment token with `hostId` binds the enrolling agent to that managed host;
  - re-enrolment replaces the binding;
  - revoked agents serve no host;
  - tokens for unknown hosts are refused;
  - binding is audited as `agent.host_bound`.
- **Dispatch (M-9):** the Operation worker routes Capability Operations to agents. One durable command is written per target, in the same transaction that marks the target Running.
- **Relay (M-7):**
  - publishes with `Nats-Msg-Id` = command ID;
  - marks a command published only after the PubAck;
  - retries with backoff;
  - fails a target `agent_command_undelivered` when its command expires unsent.
- **Results (M-8):**
  - stored before the gateway acks them;
  - status mapping: `unknown` → `Interrupted` (never retried), no result by the due time → `Lost` (`no_result`);
  - duplicates are idempotent;
  - a result from the wrong agent is refused.
- **Cancel and restart:** cancelling sends `operation.cancel` to running targets. Capability Operations resume after a controller restart.
- **Ready:** agent hosts report `Ready` from their agent's lease. They are never contacted over SSH.
- **Removed:** `POST /api/v1/agents/{id}/invoke` and its synchronous waiters (A-05).
- **API:** `POST /api/v1/managed/operations` takes exactly one of `run` or `capability`. Each kind requires targets on a matching connector. Readers don't see a capability's input. API docs are regenerated.
- **CLI:**
  - `run --capability NAME [--input JSON]`;
  - `create host` needs `--endpoint` only for SSH hosts.
- **Fix found by the new tests:** the service checked a credential SecretRef even for agent profiles, so agent Access Profiles could not be created.
- **Tests:**
  - store contract (both engines);
  - migration 8;
  - worker flow on a real store with a fake transport: dispatch and relay with a refused first publish, results and duplicates, controller restart, status mapping, `Lost`, undelivered command, cancel, undispatchable targets;
  - domain validation, the reconciler's agent-host conditions, the HTTP handler, and the CLI.
  - The e2e tests move to capability Operations in the next commit; at this commit they still call the removed `invoke`.
- **Checks run:**
  - `go build ./...` and `go vet ./...`: clean;
  - `golangci-lint run --new-from-rev=HEAD ./...`: 0 issues;
  - `go test -race -count=3` on the worker capability tests: pass;
  - `go test ./...` with PostgreSQL required (`MACHINARCHY_REQUIRE_POSTGRES_TESTS=1`), together with the next commit's e2e tests: pass.

## 2026-11-24 — CLI guide step 16 shows the owner's secrets (owner report; branch audit/capability-ops)

- `docs/cli/user-full-guide.md` step 16: `machinarchy --context lab secret list` printed `[]`, which came from a separate fresh lab. A reader following the guide sees `lab-key` and `lab-password` from step 5. The example now prints their names through `jq`.
- Docs only; no checks run.

## 2026-11-24 — Audit Phase 17: ADR-057, agent capabilities as Operations (branch audit/capability-ops)

- `docs/adr/ADR-057-capability-operations.md` records the design and the owner's three decisions:
  - capability work extends `/api/v1/managed/operations` (`OperationKind=Capability`);
  - `POST /api/v1/agents/{id}/invoke` is removed;
  - hosts are bound to agents through a per-host enrolment token.
- The ADR also covers the `agent` Access Profile connector, the PubAck-gated command relay with `Nats-Msg-Id` (M-7), persist-then-ack results with a status mapping where `unknown` is never retried (M-8), `Lost` targets on no result, and restart behaviour.
- Docs only; implementation follows in later commits.

## 2026-11-24 — CLI guide step 15 works in zsh (owner report; branch audit/keys)

- **Bug:** `docs/cli/user-full-guide.md` built `KEYS` as one space-separated string and passed it unquoted (`… $KEYS`). zsh, the macOS default shell, does not split unquoted parameters, so the CLI received a single argument and failed with `unknown flag: --trust SHA256:… --trust …`.
- **Fix:** an array, `KEYS=($(… | jq -r '.spec.accessTarget.trust[] | "--trust", .'))`, passed as `"${KEYS[@]}"`, plus a short note on why. The printed output in the guide is unchanged.

### Verification

- With sample host JSON, bash 3.2 and zsh both pass 4 separate arguments (`--trust`, fingerprint, `--trust`, fingerprint), and `echo "${KEYS[@]}" | cut -c1-80` prints the line the guide shows.

## 2026-11-24 — Docs check after Phases 14–16 (owner request; branch audit/keys)

- **Links:** a script resolved every relative link and `#anchor` in `README.md`, `CONTRIBUTING.md` and `docs/` (77 files, `docs/archive` excluded). Two links pointed at a removed `#quick-start` heading; one was also relative to the wrong README. Both now point at the project README's "Run locally" (`docs/cli/README.md`, `docs/cli/example-cli.md`).
- **Settings:** all 56 `MACHINARCHY_*` settings read by `internal/config` appear in `README.md`, `.env.example`, `docs/operations.md` or `docs/runbook.md`. The reverse check found only two placeholder shell variables in `docs/SECURITY.md` curl examples (not settings).
- **Stale references:** none left for `NEXT_PUBLIC_KEY`, `make run-cli --`, `StaticKeyProvider` or a required signing-key file.
- **`machinarchy secret rewrap` help** (`internal/cli/commands/management.go`) still described secrets only ("Rewrap stored DEKs under the active external key"). It now says it moves secrets and Operation data, repeats until `done`, and keeps the old key until older backups expire; the flag texts and the confirmation prompt are updated too. The OpenAPI summary of `POST /api/v1/secrets/rewrap` is updated likewise; spec regenerated.
- `docs/cli/user-full-guide.md`: "A session lasts 24 hours" now names the setting (the default `MACHINARCHY_SESSION_TTL`).

### Verification

- The link check reported 0 problems after the fix. `go build ./...`; `go test ./internal/cli/... ./internal/transport/http/v1/...` passed; `secret rewrap --help` shows the new text; golangci-lint `--new-from-rev` 0 issues.

## 2026-11-24 — Back to `make run-cli ARGS="…"` only (owner decision; branch audit/keys)

- `mk/build.mk`: removed the `make run-cli -- …` support (`2b0050f`, `2f10e27`). `run-cli` is again `$(call run_with_env,./cmd/machinarchy)` with `ARGS`, byte-identical to before. Why: the `--` form relied on reading make's own command line through `ps`, lost quoting, ran make targets named like CLI words, and let `NAME=…` words change make's own settings. `ARGS` has none of these problems.
- Docs (`README.md`, `docs/cli/README.md`, `docs/cli/user-full-guide.md`, `docs/development/getting-started.md`, `docs/development/local-environment.md`, `docs/examples/example.md`) use `ARGS="…"` again. The notes on `.env`/`ENV_FILE` (the cause of the earlier lab login failure) and the troubleshooting entry stay.

### Verification

- `git diff 2b0050f~1 -- mk/build.mk` is empty: the Makefile is as before the `--` change.
- Make 3.81, scratch CLI context on a closed port: `ARGS="create host … --label tier=web --label auth=key"` and `ARGS='run --host ssh-key --yes -- sh -c "echo hi"'` both parsed and reached the API (connection refused); `make help` shows the `ARGS` usage.

## 2026-11-24 — `make run-cli -- …` passes arguments containing `=` (owner report; branch audit/keys)

- **Bug:** `make run-cli -- create host … --label tier=web --label auth=key` failed with `label "--label" must be KEY=VALUE`. Make takes any word containing `=` as a make variable, so those words never reached the CLI.
- **`mk/build.mk` fix:** `run-cli` now has its own recipe. With arguments after `--` (and no `ARGS` on the command line), the recipe reads make's own command line (`ps -o command= -p $PPID`) and passes everything after the first ` -- `, in order. Words containing `=` arrive, and a later `--` (`run … -- uptime`) is kept. Globbing is off (`set -f`) and nothing is `eval`ed. Without `ps` it falls back to the goals, as before. `ARGS="…"` is unchanged and still the way to pass quoted arguments.
- **Docs:** the `=` caveat is removed from `README.md`, `docs/cli/README.md`, `docs/cli/user-full-guide.md`, `docs/development/getting-started.md` and `docs/development/local-environment.md`. What remains: quoting is lost after `--`, and a word that is also a make target runs it.

### Verification

- Make 3.81, scratch CLI context on a closed port, so reaching the network proves the arguments parsed:
  - the reported command → "list access profiles: cannot reach the API server" (before: the label error);
  - `get hosts -l tier=web` → reached the API;
  - `run --host ssh-key --yes -- uptime` → reached target preview;
  - `ARGS="config current-context"` → `t`;
  - `ARGS=` together with `--` words is refused.

## 2026-11-24 — Docs: `make run-cli -- …` and the `.env` behaviour of `make run-*` (owner request; branch audit/keys)

- `docs/cli/user-full-guide.md`:
  - step 1 note: every `machinarchy …` command also runs as `make run-cli -- …`, with the `=`/quoting limits;
  - step 2 note: use `make run-monolit ENV_FILE="$LAB/server.env"`, because plain `make run-monolit` loads the repository's `.env`, and step 3's login then fails with `Invalid credentials`.
- `docs/cli/troubleshooting.md`: new "Login fails with `Invalid credentials`": the wrong `.env`; the bootstrap password applying only to an empty database; 429 backoff.
- `docs/cli/README.md`, `README.md`, `docs/development/local-environment.md`, `docs/examples/example.md`: the `--` form, `ARGS` for arguments with `=` or quotes, and that the `run-*` targets override exported settings (variables `.env` does not set, such as `MACHINARCHY_CONFIG`, are inherited).

### Verification

- With `MACHINARCHY_CONFIG` exported, `make run-cli -- config set-cluster lab --server http://127.0.0.1:8080` wrote to that file, so the guide's lab-only CLI config works through make. Neither `.env` nor `.env.example` sets `MACHINARCHY_CONFIG`.

## 2026-11-24 — `make run-cli -- <arguments>` (owner request; branch audit/keys)

- `mk/build.mk`: when `run-cli` is the first goal, the words after it become `ARGS`, and make treats them as goals that do nothing (`.DEFAULT`, silent, so an argument that is an existing file prints nothing). Giving both `ARGS=` and words is refused.
- Make limits, documented in `mk/build.mk` and `docs/development/getting-started.md`: a word containing `=` becomes a make variable, quoting is lost, and a word that is also a make target runs it. `ARGS="..."` still works for those.
- `docs/development/getting-started.md` also notes that the `run-*` targets load `.env` and ignore exported shell settings (use `ENV_FILE=` or `bin/`). This is what caused a failed lab login: `make run-monolit` used the repo's `.env` database instead of `$LAB/server.env`.

### Verification

- Make 3.81 (macOS):
  - `make run-cli -- --config <scratch> config set-cluster lab --server http://127.0.0.1:8080` → `Cluster "lab" configured.`, and `config get-contexts` lists it;
  - the `ARGS="…"` form works;
  - both forms together is refused with a clear error;
  - a CLI error passes through (exit status 2);
  - `make -n build-cli` is unchanged, and `make help` shows the new usage.

## 2026-11-24 — Audit Phase 16.7: ADR-056 and the root-key rotation drill (branch audit/keys)

- `docs/adr/ADR-056-key-providers-built-in-default.md` and its README row: one root of trust behind the provider interface; `local` by default; KMS providers optional (AUDIT/10 K1–K4); signing key and Operation data in the same envelope.
- **Drill run once**, real binaries and the repository's SSH lab:
  - **v1:** secret, profile and host created (the host verified over SSH); a command ran; everything stored as `local:v1`.
  - **v2 with v1 in the keyring:** old output read; a new command ran; one rewrap round answered `done: true` with 1 secret and 2 Operation values moved.
  - **v2 only:** old output read and a new command ran (the SSH key decrypted).
  - 0 server ERROR lines. Details in AUDIT/16 §3.

## 2026-11-24 — Audit Phase 16.5: Operation data carries key IDs and AAD; one rewrap covers secrets and Operation data (S-07) (branch audit/keys)

- **Gap closed:** Operation specs, per-host output and detached live progress were `pkg/crypto.Encrypt` under one key derived from the encryption secret, with no key ID and no AAD. Rotating the secret made every old Operation unreadable, and a value could be copied between rows.
- **`internal/store/resources/sealed.go` (`DataCipher`):** each value gets a fresh data key, wrapped by the key provider; stored as `{"v":2,"k":"<key ID>","w":…,"c":…}`. The AAD binds column, workspace, Operation and host. Values written before keep opening through legacy keys derived from the current secret and every keyring secret.
- **The store and the Secret Store share one provider:** bootstrap builds it with `secrets.NewLocalProvider` (new helper) before `store.Open`; `store.Config` gains `Keys` and `PreviousSecrets`; tests that pass only `EncryptionSecret` get the same local provider.
- **`RewrapOperationData`:** batches per column; updates only if the value is unchanged since read; also reseals legacy values; counts what remains. `POST /api/v1/secrets/rewrap` (and the CLI) now returns `operationData {rewrapped, remaining}` and `done` (all kinds finished); a failure answers 503 and the old key must stay. OpenAPI regenerated.
- Docs: `docs/operations.md` "Key management" (what is sealed, the root-key rotation drill, backups, the future KMS switch), runbook.

### Verification

- Store contract "operation data keys" on **SQLite and PostgreSQL 16**:
  - spec, output and progress are stored with `local:bootstrap-v1` and no plaintext;
  - a spec copied to another Operation does not open;
  - a legacy spec and output (old format) read;
  - after a root-key rotation, rewrap in batches of 1 moves all 6 values (including the legacy ones) to `local:v2`;
  - with only the new secret, the Operation, the legacy output and the live progress all read.
- `TestRewrapCoversOperationData`: `done` stays false until Operation data is finished; a failure is 503.
- `TestKeyPrefixesMatchSealedText`: the rewrap's prefix match equals the stored text for key IDs JSON escapes (`<`, `>`, `&`, quotes, `%`, `_`). A lint fix had briefly switched to `strconv.Quote`, which escapes differently and would have reported "done" with values left on the old key; caught before commit.
- Existing store, worker, host-access, control-manager, service and bootstrap tests pass on both engines with the new format.

## 2026-11-19 — Audit Phase 16.3/16.4: controller signing key in the Secret Store; rotation as owner actions (branch audit/keys)

- **Migration `000007_agent_signing_keys`** (SQLite and PostgreSQL): public key, state (`next`/`active`/`retired`), origin (`generated`/`imported`), who and when; the private seed is referenced by Secret Store ID and version. Partial unique indexes allow one active and one next key, also across replicas.
- **Secret Store system secrets**: new purpose `agent-signing-key`, created and read only through `CreateSystem`/`ResolveSystem`. Owner rotate and delete of such a secret, owner create with that purpose, and workload resolution are all refused. Same envelope and key provider as every secret.
- **`internal/agentkeys.Manager`**:
  - `Ensure` imports `MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE` at the first start, so enrolled agents keep working, or generates a key. Later a different file is ignored with a warning.
  - `Stage`/`Activate`/`Discard` are the owner actions.
  - `Refresh` every 5 s, so every replica follows an activation.
  - `Signer` hands the gateway a fixed snapshot per message (`SealWith` reads `Public` then `Sign`).
  - A seed that does not match its recorded public key is refused.
- **Owner API** (owner session, audited `agent.signing_key_created/_activated/_discarded`): `GET /api/v1/agents/signing-keys`, `POST …/next`, `POST …/next/activate`, `DELETE …/next`. Responses carry public data only. `/controller-key` reads the manager live. Authorization matrix and OpenAPI updated.
- **Config**: the key file is optional (import only); `MACHINARCHY_AGENT_GATEWAY_NEXT_PUBLIC_KEY` (added earlier today, never released) is removed in favour of the owner actions.
- Docs: `docs/operations.md` "Agent gateway" and "Rotating the controller signing key" (4 steps), `docs/runbook.md`, `.env.example`.

### Verification

- `test/e2e` `TestControllerSigningKeyRotation`, real binaries in NATS operator mode:
  - the first start imports the key file (`/controller-key` equals the file's key);
  - stage → both announced → the agent pins both → 3 commands;
  - activate with no restart → 3 commands and a lease renewal;
  - a restart **without** the key file keeps the new key, and a command succeeds;
  - the key list shows active + retired/imported;
  - the agent stays up and its log has no discard, signature or ERROR line.
- `internal/agentkeys` (race detector): import once then ignore a different file; generate without a file; 4 concurrent replicas end with one key; stage/activate/discard with another replica switching only after its refresh; a snapshot keeps its key; a tampered seed is refused.
- Store contract "agent signing keys" on **SQLite and PostgreSQL 16**: one active and one next at most, activation retires, discard, `created_by` from the actor, 5 audit events.
- `TestSystemSecretsAreOnlyForTheController`; `TestSigningKeyEndpoints` (409s, 201/200, no secret reference in the list, 503 without a gateway); authorization matrix with the 4 routes as owner-only; migration count 7.
- `test/e2e`: the monolith and agent binaries are built once per run and shared; the two tests run in parallel (each has its own NATS, ports, database and work directory); child processes are started with a context that ends 2 s before the test deadline. Before this, a test killed by `-timeout` left its monolith and agent running: 11 orphans from earlier runs were found and stopped.
- Pre-commit hook: it runs `go test -timeout=30s` per staged package. The e2e package needs about 37 s at load average 70–100 (each test about 30 s), so the hook fails on any commit that stages `test/e2e`. The hook was not changed (owner rule). With normal timeouts every check passes: gofmt, `go vet ./...`, `go test ./internal/... -count=1`, and e2e in 37.3 s.
- Known gap: if replicas race at the very first start, or a key row fails after its secret was written, the losing seed stays as an unused system secret (encrypted, never resolved).

## 2026-11-19 — Audit Phase 16.1/16.2/16.6: key providers wrap and unwrap; built-in local provider; provider contract suite (branch audit/keys)

- **Interface** (`internal/secrets/keyprovider.go`): `KeyProvider` no longer returns root-key bytes, which no KMS can do. It has `Name`, `CurrentKeyID`, `Owns`, `Wrap(keyID, dataKey, aad)`, `Unwrap`, `Capabilities` (optional `Ed25519Sign`). The Secret Store builds the same `dek:` AAD as before and passes it in, so stored data is byte-compatible.
- **`LocalProvider`** (the former `StaticKeyProvider`, kept as a deprecated alias): today's AES-256-GCM wrapping under root keys derived from `MACHINARCHY_ENCRYPTION_SECRET[_FILE]` and the keyring. New key IDs are `local:<id>`; bare IDs written before keep working. `Owns` means "holds this key", so two instances can share a keyring.
- **`Keyring`**: the current provider plus earlier ones; Unwrap routes by owner. This is what a later provider switch (local → a KMS) uses.
- **Rewrap** accepts the bare key ID operators know and processes both stored spellings (`kek-a` and `local:kek-a`); `Remaining` counts both. An unconfigured old key still returns `ErrKeyUnavailable` (HTTP 503), as before.
- **Contract suite** `internal/secrets/providertest`: key IDs name the provider, wrap/unwrap, changed AAD/ciphertext/key ID fail, wrap only under the current key, rotation keeps old data until the old key is retired, concurrent use, keyring routing. `LocalProvider` passes it; every future provider must.
- **`MACHINARCHY_KMS_PROVIDER`** (`--kms-provider`): `local` (default); any other value fails startup with "not available in this build".
- `MACHINARCHY_ENCRYPTION_KEY_ID` is now at most 122 characters and may not contain `:`: it is stored as `local:<id>` in a `VARCHAR(128)` column on PostgreSQL, and `:` separates provider from key. A deployment with a longer ID or a `:` gets a clear startup error. Keyring (previous) IDs are unchanged.
- Docs: operations settings, runbook, `.env.example`.

### Verification

- `TestLocalProviderContract` (7 cases); `TestLocalProviderReadsBareKeyIDs`.
- `TestSecretsStoredBeforeProvidersStillWork`: a secret written in the old format (bare key ID in the stored row and the AAD) resolves with the local provider, rewraps by the bare ID to `local:kek-b`, and resolves after the old key is removed.
- Existing `TestSecretLifecycle` (rotation, rewrap, wrong key) and `TestSecretAPIIsWriteOnly` (rewrap from an unknown key is 503) pass unchanged.
- `TestKMSProvider` (default, explicit, unavailable provider, key-ID bounds); bootstrap tests.
- Store contract "secret store key providers" on **SQLite and PostgreSQL 16**: create under a 128-character `local:` key ID, resolve, rewrap to the next key, resolve with only the next key. No earlier test ran the Secret Store on PostgreSQL.
- `TestSecretKEKRewrapResumesInBoundedBatches` (store): the rewrapped row's key ID is now `local:kek-v2` (expected change); its custom raw-key provider was replaced by `LocalProvider`.
- Cached `go test ./...` passes; golangci-lint `--new-from-rev`: 0 issues.
- The contract first failed on keyring routing: `Owns` matched any `local:` ID, so two local instances claimed each other's keys. Fixed before commit.

## 2026-11-19 — Audit Phase 16: gateway signs through crypto.Signer and announces the next key (branch audit/keys)

- `internal/agentgateway`: `Config.Signer crypto.Signer` replaces the raw `SigningKey`; messages are sealed with `agentv1.SealWith`, which checks each signature before sending. A KMS-held key can plug in through the same interface (K-phases).
- `GET /api/v1/agents/controller-key` returns `publicKey`, `nextPublicKey` (during a rotation) and `publicKeys`, the exact value agents pin. Existing clients reading `publicKey` keep working. OpenAPI regenerated.
- New `MACHINARCHY_AGENT_GATEWAY_NEXT_PUBLIC_KEY`; startup refuses a next key equal to the current one and logs both.
- `docs/operations.md` "Rotating the controller signing key", `.env.example`.
- Phase 16 re-planned with the owner (AUDIT/10): built-in default provider first, KMS providers as optional K-phases. A Vault Transit signer written earlier the same day is parked on the local branch `wip/vault-signer` (`4e3ed7f`) for K1; nothing from it is on `audit/keys`.

### Verification

- `TestControllerSigningKeyRotation` (`test/e2e`, real binaries, NATS operator mode): the controller signs with A and announces B; the agent pins both; 3 commands succeed; the controller restarts signing with B; the same agent process renews its lease and runs 4 more commands; its log has no discard, signature or ERROR line.
- `TestControllerKeyListsTheNextKeyDuringARotation`, `TestAgentGatewayNextPublicKey`; gateway tests with the signer.
- Cached `go test` of touched packages; e2e package passes (one hook run failed in the e2e package under load and passed on the next two runs).

## 2026-11-19 — Audit Phase 14: SSH call timeouts follow their settings (branch audit/sessions)

- The Control Manager's SSH connector used a fixed 30 s per call, so raising runtime configuration `reconciliation.timeoutSeconds` above 30 had no effect on host checks. The connector now uses that setting (default 30 s, unchanged).
- The Operation worker's connector used a literal 15 min; it now uses `domain.MaxRunTimeout` (900 s, the same value), so the two cannot drift apart.

### Verification

- `go build ./...`, `go vet`, and the cached `go test ./...` pass; golangci-lint `--new-from-rev`: 0 issues. No dedicated test: the value is passed straight through to the existing connector constructor.

## 2026-11-19 — Audit Phase 14: client address behind proxies, token-bucket rate limits, HTTP settings from config (S-10) (branch audit/sessions)

- **Trusted proxies.** New `MACHINARCHY_TRUSTED_PROXIES` (IPs or CIDRs; `0.0.0.0/0`-style "trust everyone" is refused).
  - Empty (default): the TCP peer is the client, as before.
  - Set: `X-Forwarded-For` is read only from a listed proxy, from the right, stopping at the first address that is not a listed proxy. Echo's default trust of loopback, link-local and private ranges is turned off. A client therefore cannot choose its address by sending the header.
  - Before, every client behind a load balancer shared the balancer's address, so one client's burst or failed logins (S-09 network limit) blocked everyone.
- **Rate limiter is a plain token bucket per client network** (`domain.ClientKey`: IPv4 address, IPv6 /64).
  - Removed the escalating bans (1 min doubling to 2 h, extended by every request while banned). A refused request gets 429 with `Retry-After` = seconds until the next token and costs no token, so a client that slows down is served at once.
  - Limits are per replica; documented.
- **HTTP settings come from configuration** (env, flags, JSON file), in a new `config.HTTPConfig`:
  - `MACHINARCHY_CORS_ALLOWED_ORIGINS` (the server read `CORS_ALLOWED_ORIGINS` itself with `os.Getenv`, so it was missing from flags and the config file; the old name still works, and production still requires origins where the API is served);
  - `MACHINARCHY_RATE_LIMIT_RPS`, `_BURST` (were hardcoded 20 and 30);
  - `MACHINARCHY_HTTP_MAX_BODY_BYTES` (was hardcoded 1 MiB);
  - `MACHINARCHY_HTTP_REQUEST_TIMEOUT` (was the constant 25 s); the listener's write timeout is now derived from it (+5 s), so the deadline always ends first (Q-20).
- Docs: `docs/operations.md` ("Behind a reverse proxy", "Rate limits", settings, timeouts), README, `.env.example`, `docs/SECURITY.md` and the govulncheck allowlist comment (GO-2026-6872 still does not apply: Echo's limiter is unused and our extractor trusts only listed proxies).

### Verification

- `TestClientIPExtractor`: no proxies ignores the header; a forged header from an untrusted peer is ignored; a client cannot prepend its own address; chains of proxies; private and loopback ranges not trusted unless listed; IPv6.
- `TestRateLimiterIsATokenBucketPerClientNetwork`: burst, 429 with `Retry-After: 2` that does not grow while hammering, other clients unaffected, one IPv6 /64 shares a bucket and another /64 does not.
- `TestLimitsUseTheClientBehindATrustedProxy` (wired server): through one proxy, client A's network login limit and client B's rate limit do not affect other clients; changing `X-Forwarded-For` from an untrusted peer does not escape the limit.
- `TestLoadHTTPSettings` (defaults, the old CORS name, every setting, bounds) and `TestAllowedOrigins`.
- `go test ./...` (cached) passes; golangci-lint `--new-from-rev`: 0 issues. govulncheck traces for GO-2026-6872/6875 are unchanged in kind (package-wide symbols).

## 2026-11-19 — Audit Phase 14: failed-login throttling and constant-time login (S-09) (branch audit/sessions)

- **Throttling shared in the database.** Migration `000006_login_failures` (SQLite and PostgreSQL) counts failures per account and per client network with one atomic upsert each, so every replica enforces the same limits and concurrent failures are never lost.
  - Per account: after `MACHINARCHY_LOGIN_FREE_FAILURES` (5) failures in `MACHINARCHY_LOGIN_FAILURE_WINDOW` (15m), each attempt waits 1 s, doubling up to `MACHINARCHY_LOGIN_MAX_BACKOFF` (15m). No lockout, so an attacker cannot lock an owner out.
  - Per network: `MACHINARCHY_LOGIN_IP_MAX_FAILURES` (20) failures across accounts block the network until the window ends; IPv6 clients count per /64 (`domain.ClientKey`).
  - Refused attempts get 429 with `Retry-After`. A success resets the account. The start of backoff is audited once per window (`auth.login_throttled`, outcome `denied`).
  - A wrong current password on `POST /api/v1/me/password` counts as a failed login.
- **Login no longer reveals which accounts exist.** An unknown user is compared against a dummy bcrypt hash, so it costs the same time, and is throttled under its name like a real account. Before: an unknown username answered in about 0.5 µs, a real one in about 470 ms.
- Docs: `docs/operations.md` (Login sessions, settings), `docs/runbook.md` ("A user cannot log in (429)"), README, `.env.example`.

### Verification

- `internal/domain`: policy bounds, the backoff curve, `ClientKey` (IPv4, IPv4-mapped, IPv6 /64, junk).
- `TestLoginTimingDoesNotRevealAccounts` fails on the previous code and passes now.
- Store contract "login throttling" on **SQLite and PostgreSQL 16**: counting, the window reset, account and network blocks, the audit once per window, reset, and 20 concurrent failures all counted.
- End to end (`TestLoginThrottlingEndToEnd`): 429 with `Retry-After: 1` for the right password during backoff, success after it, the network limit across accounts including unknown ones, another network unaffected, one audit event.
- Config: `TestLoadSessionAndLoginSettings`.
- `go test ./...` (cached) passes; golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 14: revocable sessions, key rotation, password change (S-08) (branch audit/sessions)

- **Server-side sessions.** Migration `000005_user_sessions` (SQLite and PostgreSQL; times stored as Unix nanoseconds).
  - Every signup and login records a session; the JWT carries its ID (`sid`).
  - `JWTAuth` accepts a session token only while its session is active. The check is one query that also returns the user's current role, replacing the membership lookup each request already made.
  - A user keeps at most 50 active sessions (the oldest is revoked past that), and long-ended rows are pruned on the next login.
- **Logout really logs out.** `POST /api/auth/logout` revokes the session of the token sent (bearer or cookie), so a copied token stops working at once. Before this, logout only cleared the cookie.
- **New self-service endpoints** (login sessions only; API tokens get 403; listed in the authorization matrix as `humans`):
  - `GET /api/v1/sessions`;
  - `DELETE /api/v1/sessions/{id}`;
  - `DELETE /api/v1/sessions` (log out everywhere);
  - `POST /api/v1/me/password`, which checks the current password, enforces 12–72 bytes, requires the new password to differ, and revokes every session atomically with the change.
  - Audited: `auth.session_revoked`, `auth.sessions_revoked`, `user.password_changed`.
- **Signing keyring with `kid`** (new `internal/sessionkey`).
  - Tokens are signed with `MACHINARCHY_OPERATOR_TOKEN` and name it by a derived key ID; `MACHINARCHY_SESSION_PREVIOUS_KEYS` lists earlier values, still accepted during a rotation.
  - Token validation lives in one place (`Keyring.Parse`) and is shared by the middleware and logout.
- New `MACHINARCHY_SESSION_TTL` (default 24h, 5m–168h) replaces the hardcoded 24 h in both the service and the middleware. Bootstrap resolves the effective value once for both: a zero-value config used to give the middleware a lifetime of 0, which rejected every token.
- **Upgrade note:** tokens issued before this release have no session ID, so users log in again once.
- Docs: `docs/operations.md` "Login sessions" (actions, rotation procedure, upgrade note) and the settings; README; `.env.example`; OpenAPI regenerated.

### Verification

- Unit tests:
  - `internal/service`: logout, revoke, log out everywhere, the current flag, API tokens refused, and every password-change rule;
  - `internal/transport/http/middleware`: no `sid`, a revoked session, no `kid`, an unknown `kid`, the previous key accepted, and the previous key's `kid` with the current secret refused;
  - the auth handler: logout passes the bearer or cookie token, and never an API token.
- Store contract "login sessions" on **SQLite and PostgreSQL 16**. It covers:
  - the role from an active session;
  - another user's, unknown, expired and malformed IDs;
  - revoking once, audited;
  - the 50-session cap;
  - log out everywhere;
  - password change atomic with revocation;
  - a multi-byte user agent cut without breaking UTF-8.
- End to end through the wired server (`internal/bootstrap/sessions_test.go`):
  - a copied token is refused after logout;
  - revoke by ID, and log out everywhere;
  - password change (wrong current gives 403, a short new one 400, every session ends, the old password fails and the new one works);
  - **key rotation across restarts** (the previous key is accepted while listed and refused once dropped).
- The authorization matrix covers the 4 new routes.
- `go test ./...` (cached) passes; golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13C: the guide uses the repository's SSH test servers (branch audit/docs-guide)

- `docs/cli/user-full-guide.md` rewritten on the existing fixtures, at the owner's request: `make ssh-servers-up` and `deployments/testing/ssh-servers.docker-compose.yml`, instead of ad-hoc `docker run` containers.
  - Hosts `ssh-key` (2222, key login, `tofu`), `ssh-password` (2221) and `ssh-hostcheck` (2223), both password login.
  - The password hosts show that Machinarchy never trusts keys on first contact for a password login: they wait for `trust host --all-pending`.
  - `Lost` is shown with `make ssh-servers-restart SSH_SERVICE=ssh-key`.
  - The changed-key case is a host pinned to another server's keys, so the fixtures' persisted host keys in `data/.ssh/` are never touched.
  - Cleanup is `make ssh-servers-down`, which keeps the lab's keys.
- Every command was run again for real against the fixtures, and the outputs were replaced. Absolute paths are shortened and cut lines are marked `…`. The lab's credentials are piped from `data/.ssh/` into write-only secrets, never printed.
- The run left only empty runner directories (`.local/state/machinarchy/ops`) in the fixtures' bind-mounted homes; those were removed with `rmdir`.

### Verification

- The full lab script ran end to end on the fixtures with no timeouts; the server log had no warnings or errors.

## 2026-11-19 — Audit Phase 13C: full CLI user guide (branch audit/docs-guide)

- DOC-GUIDE: new `docs/cli/user-full-guide.md`, a step-by-step lab with three SSH servers (OpenSSH containers `web-1`, `web-2`, `db-1` on ports 2221–2223). It covers:
  1. prerequisites and a lab-only CLI config;
  2. the SSH lab;
  3. server settings, each explained;
  4. login;
  5. CLI contexts;
  6. a write-only secret;
  7. Access Profiles with `tofu` and `approve`;
  8. hosts with labels;
  9. host-key trust (automatic, and `trust host`);
  10. attached runs;
  11. detached runs with `logs --follow` and `--stream stderr`;
  12. labels and selectors;
  13. cancel;
  14. long runs and `Lost`;
  15. a reinstalled host (`HostKeyChanged`, `trust host --replace`);
  16. history;
  17. an operator API token and a second context;
  18. troubleshooting, cleanup, and a cheat sheet.
- **Every command was run for real** on 2026-11-19 against that lab, with the make-built monolith and CLI (`ff666e1`), and the outputs are pasted from the run. The run surfaced three product problems; the guide documents them as known problems and AUDIT/13C records them.
- Linked from `docs/cli/README.md`, the main README (which also points at `docs/api`) and `docs/api/README.md`.

### Verification

- The lab script ran every step twice end to end (the second run is the guide's source); the server log had no warnings or errors.
- The API-token step ran against a separate fresh server.
- Link targets in the guide checked: `operations.md` and `runbook.md` anchors, `commands.md`, `docs/api/README.md`.

## 2026-11-19 — Audit Phase 13C: API reference moved to docs/api (branch audit/docs-guide)

- DOC-API: the generated OpenAPI files moved from `docs/swagger.{json,yaml}` to `docs/api/swagger.{json,yaml}` (with `git mv`). Updated everything that refers to them:
  - `make api-docs` now writes to `docs/api`;
  - `docs/embed.go` embeds `api/swagger.json`; the server still serves it at `/openapi.json`, and the reference UI at `/scalar`, when `MACHINARCHY_API_DOCS_ENABLED` is on;
  - the Dockerfile copies the new path, and `.dockerignore` allows it;
  - the spec test messages, and `docs/architecture.md`.
- New `docs/api/README.md`: how the files are generated and served.
- Regenerating also picked up one comment changed in Phase 13B and not yet in the spec (`TargetOutput.live`).

### Verification

- `make api-docs` writes only to `docs/api/`; no file is left at the old path.
- `go build ./...`: pass.
- `go test ./internal/transport/http/v1/` (`TestOpenAPISpecCoversEveryRoute` against the embedded spec) and `./internal/bootstrap/` (the authorization matrix, which reads every route from `/openapi.json`): pass.
- The container image build was not run (no Docker build in this phase); the changed `COPY` lines were checked by reading them.

## 2026-11-19 — Audit Phase 13B: detached tests poll at a sane pace (branch audit/detached-ssh)

- Each detached status poll runs the runner script on the "host", which in these tests is the machine itself: about 20 short processes per target. At 150 ms over 50 separate hosts, the restart test launched thousands of processes per second, and under `-race -count=3` it ran 157–199 s instead of about 15 s.
- The tests now poll at 300 ms (`fastPolls`), and the 50-target restart test at 1–2 s, like the PostgreSQL test. Production polls every 5–30 s, and each host runs only its own targets' scripts.

### Verification

- `go test -timeout 30s ./internal/operationworker/`: pass in 21.8 s (it was about 35 s).
- The full race suite could not be judged here: with no test running, this machine showed 471 runnable processes and a load average above 800 (Spotlight indexing, Chrome, macOS services). Timeouts across unrelated packages in the last full runs match that.

## 2026-11-19 — Audit Phase 13B: consistent Operation reads; full-suite stability (branch audit/detached-ssh)

- **Pre-existing bug, fixed:** `Store.GetOperation` read the Operation row and its target rows with two separate queries. A reader could combine an Operation still `Queued` with a target a worker had already started, and fail validation ("queued Operation targets must be pending"). That is a spurious error for any API reader. Detached runs move targets right after the start, which made it visible in the full suite. It now reads both in one read-only transaction (`REPEATABLE READ` on PostgreSQL; a deferred transaction, a WAL snapshot, on SQLite).
- **Older test race, fixed:** `TestManagedHostAndOperationEndToEnd` cancelled the `hold` command as soon as its target was `Running`. The attached worker marks it Running before the SSH session opens, so under load the cancel could win and the exec never happened ("remote exec count=2, want 3"). The test now also waits for the exec to reach the server.
- The contract fixture now keeps idle PostgreSQL connections (`MaxIdleConns: 8`, T-13B-01 for the fixture). The PostgreSQL contract run of the new read test went from 86–299 s to about 30 s.
- Detached integration tests: at most 4 environments run at once, and the wait limits rise to 90 s. Under the full shuffled race suite, ten parallel tests spawning real shells starved other packages; the authorization matrix's bootstrap hit its deadline.

### Verification

- New contract subtest "operation reads are consistent" (SQLite and PostgreSQL): 4 readers call `GetOperation` while a writer starts 40 Operations and moves their targets.
  - It **fails on the old code** on SQLite in 2 of 2 runs and on PostgreSQL in 1 of 2 runs;
  - it passes with the fix on both engines in 2 of 2 runs.
- `TestManagedHostAndOperationEndToEnd`: `-race -count=3` pass.
- `go test -timeout 30s ./internal/operationworker/` (the hook's limit): pass twice.
- golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13B: migrations get their own deadline (branch audit/detached-ssh)

- `store.Open` ran the ping, the SQLite pragmas **and every migration** under the connect timeout (5 s by default). Migration `000004` rewrites `operation_targets` on SQLite, which takes time proportional to the stored output: an 834 MB database with 100,000 targets took **3 minutes** on a laptop. **The first start after an upgrade would have failed.**
  - It showed up as SQLite stores timing out during migrations in the full shuffled race run.
  - Fix: migrations now run under `store.Config.MigrationTimeout` (default 10 min), set by the new `MACHINARCHY_DATABASE_MIGRATION_TIMEOUT` (`--database-migration-timeout`). The connect timeout still covers connecting and session setup.
- `docs/operations.md` (Upgrades): the new setting, and the rewrite and its cost on SQLite. `.env.example`: the setting.
- Removed the unused `resource.SanitizeOutput` (deadcode).

### Verification

- `TestLoadDatabaseMigrationTimeout`: default, override, and invalid values.
- Migration 000004 timed by hand with `sqlite3` on an 834 MB version-3 database: 179.5 s, and all 100,000 rows kept.
- `go test ./internal/config/ ./internal/store/ ./internal/bootstrap/`: pass. golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13B: benchmark, live run, docs (branch audit/detached-ssh)

- **Benchmark** (`TestBenchmark200TargetsFiveMinutes`, opt-in with `MACHINARCHY_BENCH_DETACHED=1`): 200 targets × a 5-minute command, 4 workers, production poll intervals.
  - All 200 launched in 27 s; the Operation succeeded after **5 min 49 s**. Run attached and one at a time, the same work would take about 16.7 h.
  - 4,657 polls, 0 unreachable, 0 lost.
- **Live run** against real OpenSSH (`linuxserver/openssh-server`: setsid available, no systemd), with the make-built monolith and CLI:
  - `run` detaches and `logs --follow` streams the output as it arrives;
  - a monolith restart mid-run: the command survived, finished `Succeeded` and ran exactly once;
  - cancel gives `Cancelled` with exit 143 and no process left;
  - a host restart gives `Lost` / `supervisor_lost`;
  - no state directories are left on the host;
  - the server log has no warnings or errors.
- Fixes found by the live run:
  - `logs --follow` returned at once while the target was still Pending: the output endpoint now reports `live` until the target is final;
  - `sshclient` no longer logs `WARN failed to close SSH session: EOF`. After `Run`, the server has normally closed the channel already, and that warning would have appeared on every detached poll.
- Docs:
  - `docs/operations.md`: new "Detached execution" section (how it runs, supervisors, timeouts and cancel, Lost, restarts, output, limitations), the metrics and alerts, and the timeouts table;
  - `docs/runbook.md`: "A detached target is Lost" and the launch failure classes.

### Verification

- `go test ./pkg/sshclient/ ./internal/domain/resource/`: pass, including a new check that a Pending target reads as live.
- The live run above.

## 2026-11-19 — Audit Phase 13B: runner on a real systemd host (branch audit/detached-ssh)

- New `test/systemd/`:
  - a Debian image with systemd as PID 1 and OpenSSH (`Dockerfile`; the user `deploy` has lingering, `nolinger` does not);
  - `systemd_test.go` (build tag `systemd`), which drives the runner over real SSH in a privileged container;
  - `run.sh`, which builds the image and runs it.
  - It is not part of `go test ./...`.
- What it proves:
  - root runs under the system manager: the command's cgroup is `machinarchy-op-1-t-1.service`;
  - a lingering user runs under `user@1000.service` (`scope=user`);
  - a user without lingering falls back to setsid;
  - **cancel under systemd also kills a process that escaped with `setsid`** (KillMode=control-group); in setsid mode that process survives, the documented limitation;
  - the host-side timeout works under systemd;
  - a stopped unit (what a reboot does) is reported `lost`;
  - no unit is left active.

### Verification

- `test/systemd/run.sh`: all 8 subtests pass (Debian bookworm with systemd 252, Docker Desktop, cgroup v2). golangci-lint `--build-tags systemd`: 0 issues. `shellcheck`: clean.

## 2026-11-19 — Audit Phase 13B: two workers on PostgreSQL; single-row credential authorization (branch audit/detached-ssh)

- New `TestTwoWorkersOnPostgresRunEachCommandOnce`: two workers with production-style leases share 50 detached targets, one is stopped mid-way, and every command still runs exactly once. It exercises the Operation lease for launches and the per-host lease for polls.
- New `internal/testutil/pgtest`: a fresh schema per test on `MACHINARCHY_TEST_POSTGRES_URL`; the test is skipped without it, or fails when `MACHINARCHY_REQUIRE_POSTGRES_TESTS` is set.
- `secrets.ProfileAuthorizer`: when the reader implements `resource.OperationTargetReader` (the store and `bootstrap.runtimeProfileBindings` do), operation-worker credential checks read one target row (`GetOperationTarget`) instead of loading and decrypting the whole Operation. Every detached poll resolves a credential, so this matters at scale. The rules are unchanged: active Operation, same Host generation, same captured profile revision.
- Detached integration tests can show worker logs with `MACHINARCHY_TEST_WORKER_LOGS=1`.

### Found while doing it

- `store.Config` with `MaxIdleConns` 0 keeps no idle PostgreSQL connections, so every query opens and authenticates a new connection: about 56 ms each here, against 8 ms pooled. Production sets 5 idle by default (`MACHINARCHY_DATABASE_MAX_IDLE_CONNS`), but tests that build `store.Config` directly (the contract fixture among them) pay it. This is why the PostgreSQL contract suite takes about 87 s. Recorded in AUDIT/13B; not changed here.
- Polling cost on PostgreSQL is roughly 10 queries plus one SSH session per host per poll (host lease, credential resolution, progress). Fine at the 5–30 s production intervals; the 150 ms test interval saturated a laptop, so the PostgreSQL test polls at 1–2 s.

### Verification

- `TestTwoWorkersOnPostgresRunEachCommandOnce`: pass in 33 s on PostgreSQL 16, running in a tmpfs container with `fsync=off`. Docker Desktop's disk made checkpoints take 50–74 s.
- `TestProfileAuthorizerSingleRowTargetRead`: a changed Host, an uncaptured revision and a finished Operation are all refused, and the full Operation is never read.
- `go test ./internal/secrets/ ./internal/operationworker/`: pass. golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13B: follow output by offset; CLI run --detach and logs (branch audit/detached-ssh)

- The output endpoint takes `from` (a stream byte offset) and returns `from` and `next`, computed from the capture's real byte positions (`StreamCapture.Slice`). A part of the stream that was not kept is reported as one `[… N bytes omitted …]` line, so a follower never repeats or misses bytes.
- A detached target now keeps its final capture after it ends (encrypted, at most 15 KiB head plus 15 KiB tail per stream), so output stays readable by offset afterwards; the poller stores the last chunk before recording the result. Domain validation allows a capture on any launched target.
- CLI:
  - `run` without `--wait` is detached, with launch concurrency up to 20; `--wait` stays attached unless `--timeout` is over 900 s or `--detach` is given;
  - new `logs operation ID [--host] [--stream stdout|stderr] [--follow]`, which prints every target with a header when no host is given;
  - `api.TargetResult` gains `stdout`, `stderr`, byte counts and `truncated`, and `get operation` shows the first line of stdout, stderr or output. Before this, every result stored with separate streams showed an empty OUTPUT column.
- Docs: `docs/cli/README.md`. OpenAPI regenerated.

### Verification

- `TestOutputOfByOffset`: follow reads, a read across the omitted part, a read at the end, attached and legacy results.
- `TestDetachedRunReportsOutputAndCleansUp` now also reads the final output from an offset.
- CLI tests:
  - `run` detaches without `--wait`, stays attached with `--wait`, and detaches with `--timeout 3600` or `--detach`;
  - `logs --follow` prints the whole stream exactly once;
  - stderr by host ID, all-target headers, and usage and not-found errors;
  - `TestTargetOutputRequest` (path escaping and query);
  - the command-tree test lists `logs operation`.
- `go test ./internal/cli/... ./internal/domain/... ./internal/service/ ./internal/operationworker/ ./internal/store/`: pass. golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13B: target output endpoint (branch audit/detached-ssh)

- `GET /api/v1/managed/operations/{id}/targets/{hostId}/output?stream=stdout|stderr` returns `resource.TargetOutput`:
  - live while a detached command runs, final afterwards;
  - `text`, `bytes` (the full size), `truncated` (a 15 KiB head and tail are kept) and `live`;
  - escape sequences are stripped.
  - Access: only callers allowed to execute runs (AUDIT S-04); readers get 403. Listed in the authorization matrix as `writers`.
- The live capture is never part of the Operation JSON (`OperationTarget.Live` is `json:"-"`; the store encodes it separately). Readers instead see `progress` (stream sizes, last contact, poll count, cancel reason, no output). `redactOperation` also drops the capture.
- `RunSpec.detach` is accepted by the submit endpoint, with timeouts up to 24 h when set.
- OpenAPI regenerated (`make api-docs`).

### Verification

- New tests:
  - `TestDetachedSubmissionAndOutputAccess`: an attached 2 h run is refused and a detached one accepted; an operator reads output, a reader gets 403; a bad stream gives 400 and an unknown target 404;
  - `TestOperationJSONOmitsLiveOutput`;
  - `TestOutputOfRunningAndFinishedTargets`.
- `go test ./internal/bootstrap/` (authorization matrix, including the new route), `./internal/service/` and `./internal/transport/...`: pass. golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13B: detached launcher and poller (branch audit/detached-ssh)

- Launcher (`internal/operationworker/detached.go`):
  - a detached Operation launches its targets `RunSpec.Concurrency` at a time; a worker slot is held only for the launch;
  - each target gets a launch handle, with its deadline set to the earlier of launch time plus the timeout and the Operation deadline;
  - a session that failed before anything could start fails the target with a `launch_*` class;
  - a connection lost during the launch records an unconfirmed handle for the poller to resolve;
  - a launch already sent is not cut off by worker shutdown or cancel; it finishes and the poller cancels the command if needed;
  - restart: a detached Operation is never marked Interrupted; Pending targets are resumed.
- Poller (`internal/operationworker/poller.go`), started with the worker when the store and connector support detached runs:
  - one status session per host (per profile revision and state root, up to 4 targets each); on PostgreSQL a per-host lease keeps two workers from polling the same host;
  - the interval grows from 5 s to 30 s with the target's age;
  - output is captured as head and tail; an exited target is finished only after all its output has been read;
  - cancel and timeout are sent by the poller; a timeout is enforced past the deadline plus a grace if the host watchdog did not act;
  - `Lost` when the supervisor is gone, the state directory vanished, or the host stays unreachable past the deadline (or 5 min after a cancel);
  - an unconfirmed launch the host knows about is adopted (`mode: adopted`); one proven never to have started is relaunched, up to 3 attempts;
  - a finished target's directory is cleaned up before its result is recorded, because the credential authorizer only serves active Operations; the Operation finishes once every target is terminal.
- Runner: a launch now records the launching shell's pid. A directory without `meta` whose launcher died is reclaimed (`busy` if the launcher is still alive), and status reports it `missing` instead of `lost`.
- Metrics: `operationworker_detached_launches_total{outcome}`, `_running_targets`, `_polls_total{outcome}`, `_lost_targets_total`, `_poll_duration_seconds`.

### Verification

- New `internal/operationworker/detached_test.go`: real SQLite store, secret resolution and SSH connector, against an SSH server running commands in a real shell. Tests:
  - live output while running, the final result with newlines and byte counts, and no state left on the host;
  - **a restart with 50 targets loses none, and each command runs exactly once**, even when the worker stops mid-launch (39 Pending and 11 Running at the stop);
  - cancel stops whole process trees;
  - timeout enforced on the host, and by the poller;
  - a killed supervisor gives `Lost`;
  - an unreachable host gives `Lost`;
  - an unconfirmed launch is adopted, or retried and then failed;
  - a profile with `supervisor: attached` refuses detached runs.
- `go test -race -count=2 -shuffle=on` on `operationworker`, `hostaccess/...` and `domain/...`: pass. golangci-lint `--new-from-rev`: 0 issues. `shellcheck`: clean.
- Test duration: the integration tests run in parallel; `operationworker` takes about 22 s and `hostaccess/runner` about 7.5 s, within the pre-commit hook's 30 s per package. The runner tests' fake `systemd-run`, `systemctl` and `loginctl` are now one shared script reached through symlinks, because macOS scans every new executable on its first run (about 3 s each).
- Problems found and fixed while writing these tests:
  - a stopping worker failed targets as `host_snapshot_stale`;
  - a cleanup after the last result was refused by the authorizer;
  - a half-made launch directory was adopted as a running command.

## 2026-11-19 — Audit Phase 13B: detached-execution model and persistence (branch audit/detached-ssh)

- Domain (`internal/domain/resource/detached.go`, `operation.go`, `access_profile.go`):
  - `RunSpec.Detach`: timeouts up to 24 h when detached, 900 s otherwise;
  - the terminal target state `Lost`;
  - `LaunchHandle` (mode, unit, pid, state root, endpoint, launch time, deadline; an empty mode means an unconfirmed launch);
  - `LiveOutput` with a `StreamCapture` per stream, which keeps a 15 KiB head and a 15 KiB tail, handles repeated chunks and gaps, and renders with an omission marker;
  - `TargetResult` gains `stdoutBytes`, `stderrBytes` and `truncated`;
  - Access Profiles gain `execution` (`supervisor` auto/systemd/setsid/attached, `stateRoot`, `maxOutputFileBytes`).
- Output keeps newlines and tabs: `sanitizeOutput` still removes other control characters (so no terminal escapes), replaces invalid UTF-8 and never splits a rune. Before this, stored stdout and stderr had every newline replaced by a space.
- Migration `000004_detached_targets`:
  - SQLite rebuilds `operation_targets` to allow `Lost`; PostgreSQL swaps the CHECK constraint;
  - both add `launch` (plain JSON) and `live` (encrypted) plus a partial index.
  - Checked by hand that the SQLite rebuild keeps existing rows (`integrity_check` ok).
- Store (`operation_detached.go`), all single-row writes:
  - `RecordTargetLaunch` (audited, fenced like target updates);
  - `UpdateTargetLive` (encrypted, not audited, because it is progress);
  - `ListDetachedTargets`.
  - `GetOperation` returns the launch and live data; a terminal write clears the live data.

### Verification

- New domain tests: capture fits, gap, unread bytes, output budget, sanitizer, timeout bounds, live output only while running, execution settings.
- New contract subtest "detached targets" passes on SQLite and PostgreSQL 16. It covers launch only on an active Operation, re-recording, encrypted progress, listing, the `Lost` round-trip with byte counts, and terminal targets refusing launch and progress.
- The migration-count contract now expects 4.
- `go test ./internal/...`: pass. golangci-lint `--new-from-rev`: 0 issues.

## 2026-11-19 — Audit Phase 13B: detached-execution runner (branch audit/detached-ssh)

- New `internal/hostaccess/runner`: the POSIX `sh` runner of AUDIT/10b §1.2 (`runner.sh`, embedded), plus Go command builders and strict reply parsers.
- Actions:
  - `launch`: an idempotent `mkdir` lock; a retry adopts the running process;
  - `status`: running, exited, lost, missing; output deltas with head and skip-ahead chunks;
  - `cancel`: TERM to the process group or tree, KILL after 10 s;
  - `cleanup`;
  - a `sweep` of directories older than the retention period, run on launch.
- Supervisor: `systemd-run` (system, or `--user` with lingering) when usable, otherwise `setsid`/`nohup`. A host-side watchdog enforces the timeout in both modes, and systemd adds `RuntimeMaxSec` and `KillMode=control-group`.
- Safety:
  - the command reaches the host only through `domain.QuoteArgv`;
  - identifiers and the state root are checked in Go and again by the script;
  - state is owner-only (`umask 077`);
  - protocol lines are prefixed, so output from shell start-up files is ignored.
- Not wired into execution yet.

### Verification

- `go test -race -count=3 ./internal/hostaccess/runner/`: pass with `/bin/sh` (bash 3.2) and `dash`. The tests cover:
  - idempotent launch;
  - verbatim arguments;
  - incremental output and skip-ahead;
  - cancel kills the whole tree;
  - host-side timeout;
  - a dead supervisor gives `lost`;
  - cleanup leaves running targets;
  - a fake `systemd-run` (arguments, fallback, and a forced `systemd` mode that is refused);
  - crafted IDs and roots refused by Go and by the script;
  - parser rejection cases.
- `shellcheck -s sh -S warning`: clean for the runner and the wrapper. golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13A: host-key change audit and metric (branch audit/host-keys)

- AUDIT/10b §2.2 asks for an audit event and a metric on a key change. Both were missing.
- Audit:
  - `internal/store/resources/store.go`: `ObserveHost` audits the transition into `HostKeyChanged` as `resource_host.host_key_changed` (`domain.AuditResourceHostKeyChanged`), with `presented` and `trusted` fingerprints, once per transition; other observations stay `resource_host.updated`.
  - `resource.ReadyReason` helper.
- Metric:
  - `internal/controlmanager/host_controller.go`: `HostController.RegisterMetrics` exports `machinarchy_controlmanager_host_key_events_total{event}` (`pending_approval`, `changed`, `tofu_pinned`), counted per transition;
  - registered in `bootstrap.Runtime`.
- Docs: `docs/operations.md` (metric and suggested alert), `docs/runbook.md` (`HostKeyChanged`).

### Verification

- `TestChangedHostKeyIsNeverAccepted` waits for two further controller retries, then expects exactly one `host_key_changed` audit row naming both keys and a counter of 1. It fails when the transition guard is removed (mutation run; file restored).
- `TestTOFUPinsEveryHostKeyThenVerifies` checks `tofu_pinned` = 1.
- `go test -race -count=2` on the host-key tests: pass.

## 2026-11-18 — Audit Phase 13A: host-key store contract on both engines (branch audit/host-keys)

- `internal/store/resource_contract_test.go`: new contract subtest "host-key observations and trust". It runs on SQLite and PostgreSQL and covers:
  - migration 000003: `observed_host_keys` round-trips including `lastSeenAt`, and an observation without keys keeps the stored ones;
  - `TrustHostKeys`: a stale revision and a change without replace are refused; trust bumps the generation; every trust writes one `resource_host.host_key_trusted` audit row and one `host.spec-changed` outbox event with change `host-keys-trusted`.

### Verification

- `go test -race -run TestManagedResourceContract ./internal/store/` on SQLite, and with `MACHINARCHY_TEST_POSTGRES_URL` set (postgres:16-alpine container, `MACHINARCHY_REQUIRE_POSTGRES_TESTS=1`): pass on both engines.
- `./internal/store/...`, `./internal/service/...` and `./internal/hostaccess/...` on PostgreSQL: pass.

## 2026-11-18 — Audit Phase 13A: complete host-key scans, rate-limited rescans (branch audit/host-keys)

- `pkg/sshclient/hostkeys.go`: `ScanHostKeys` used to skip a key type whose connection failed and return the rest. Seen live: an `approve` scan offered 2 of 3 keys. It now retries a failed type once, after 500 ms, and if it still fails the whole scan fails. A refused or policy-denied target is not retried.
- `internal/hostaccess/reconciler.go`: the reconciler reuses a Host's last scan for `DefaultHostKeyRescanInterval` (10 min, `WithHostKeyRescanInterval`) while the Host is pending approval or `HostKeyChanged`. A new generation, or a cached scan that still shows a trusted key (it predates the change), forces a rescan.
  - Why: seen live, OpenSSH 9.8+ `PerSourcePenalties` ("noauth") blocked the controller's address after it rescanned a pending Host on every retry. The block then broke the verify after approval.
- `ObservedHostKey.LastSeenAt` (domain and CLI): only a fresh scan sets it, so reuse cannot extend its own window.
- `docs/operations.md`: the scan is all or nothing; why scans are rate limited and what to do on `srclimit_penalise`.

### Verification

- New tests:
  - `TestScanHostKeysRetriesADroppedConnection` and `TestScanHostKeysNeverReturnsAPartialSet` (a dialer drops scan connections);
  - `TestPendingHostReusesARecentScan` (5 reconciles give 1 scan; after the interval there is a 2nd, after a spec change a 3rd);
  - `TestChangedKeyRescansOnceThenReuses`.
- `go test -race` on `internal/hostaccess`, `internal/domain/...`, `pkg/sshclient`, `internal/controlmanager` and `internal/cli/...`: pass. `golangci-lint --new-from-rev=HEAD`: 0 issues.

## 2026-11-18 — Audit Phase 13A: an event no longer waits behind a delayed retry (branch audit/host-keys)

- `internal/controlmanager/queue.go`: `Add` for a key that already had a delayed requeue (`AddAfter`, e.g. the Host controller's connector retry) was coalesced into the delayed entry. The outbox event was acknowledged, but the reconcile waited out the whole delay. An immediate `Add` now replaces the pending delay. Found live: an owner's host-key approval sat at `HostKeyPendingApproval` until the retry interval; this affected any spec change to a Host that was not Ready.

### Verification

- `TestResourceQueueAddOverridesDelayedRetry`: fails without the fix and passes with it. `go test -race -count=3 ./internal/controlmanager/`: pass.

## 2026-11-18 — Audit Phase 13A: endpoint changes clear trust; host-key docs (branch audit/host-keys)

- `internal/service/managed_resources.go`: changing a bound Host's endpoint (owner only, AUDIT S-02) clears its trusted keys unless new trust is sent with the change, so the new endpoint is scanned again under the profile's policy.
- `docs/operations.md`: new "SSH host keys" section (policies, the deployment default, the safety rules) and the setting. `docs/runbook.md`: `HostKeyPendingApproval`, `HostKeyChanged`, `HostKeyNotTrusted`. `README.md` and `.env.example`: the setting.

### Verification

- `TestEndpointChangeClearsHostKeyTrust` (an operator move is refused; an owner move clears trust; explicit new trust is kept), `TestOperatorCannotRedirectOwnerCredential` and `TestManagedResourceFlow`: pass. golangci-lint on `internal/service`: 0 issues.

## 2026-11-18 — Audit Phase 13A: host-key approval API and CLI (branch audit/host-keys)

- `POST /api/v1/managed/hosts/{id}/trust` `{generation, fingerprints, replace}` (owner only, through the service). It is in the OpenAPI spec and in the authorization matrix, which flagged the route as missing before its entry was added.
- CLI:
  - `machinarchy trust host NAME-OR-ID [--replace] [--fingerprint …]` and `--all-pending [--selector …]`;
  - it always prints the hosts and fingerprints to be trusted before asking for confirmation (also with `--yes`), and sends exactly those fingerprints with the generation it displayed;
  - it refuses to touch a host that already trusts keys without `--replace`, and refuses a host with no observed keys.
  - The CLI's `HostStatus` gains `observedHostKeys`.
- `docs/cli/README.md`: the commands.

### Verification

- `go test ./internal/cli/...`: pass. New subtests:
  - no change without confirmation, with the keys shown anyway;
  - trust sends the observed keys at the displayed generation;
  - `--replace` is required to change keys, and `--fingerprint` overrides them;
  - a host with no observed keys is refused;
  - `--all-pending` with a selector only approves pending matching hosts;
  - 3 usage errors;
  - the command tree test lists `trust host`.
- `TestRouteAuthorizationMatrix` and the route-coverage test pass. golangci-lint: only the 2 pre-existing issues.

## 2026-11-18 — Audit Phase 13A: automatic SSH host-key trust (branch audit/host-keys)

**Behaviour change:** a Host bound to an Access Profile no longer needs a hand-entered fingerprint. Its keys are trusted according to the profile's `hostKeyPolicy`: `tofu` pins them on first contact; `approve` (the production default) waits for an owner; `ca` trusts the profile's host certificate authorities; `pinned` is the old behaviour.

- **Domain** (`internal/domain/resource/hostkeys.go`, new):
  - `HostKeyPolicy`, and `AccessProfileSpec.HostKeyPolicy` plus `HostCertificateAuthorities` (validated: `tofu` requires private-key authentication; `ca` needs 1–8 keys). `EffectiveHostKeyPolicy` turns `tofu` into `approve` for password profiles, so an impostor on first contact never receives a password.
  - `HostStatus.ObservedHostKeys`.
  - `Host.TrustHostKeys` (1–8 distinct `SHA256:` fingerprints; it refuses to replace existing trust unless asked).
  - `HostObservation`.
- **Store:**
  - migration `000003` adds `managed_hosts.observed_host_keys`;
  - `ObserveHost` records observed keys;
  - new `TrustHostKeys`, which bumps the generation, emits a spec-changed event and audits `resource_host.host_key_trusted` with the fingerprints, the source (`tofu` or `owner`) and any replaced keys.
- **Reconciler** (`internal/hostaccess`):
  - no trust: scan before authenticating, then pin (`tofu`) or report `HostKeyPendingApproval` with the fingerprints (`approve`); `pinned` never scans;
  - a trusted key no longer presented gives `HostKeyChanged`, naming the presented and trusted keys, with no authentication attempted and nothing replaced.
  - The connector verifies against the whole fingerprint list and the profile's CAs, and refuses to authenticate with no trust at all. `WithDefaultHostKeyPolicy` sets the default.
- **Controller:** under TOFU it pins first; the spec-changed event then reconciles again with the pinned keys.
- **Service:** owner-only `TrustHostKeys`. It requires the exact fingerprints the owner reviewed (approving "whatever is observed now" could trust a key that changed in between) and the spec generation the owner saw (status writes do not conflict with it). CA keys are parsed when a profile is created or updated.
- **Config:** `MACHINARCHY_SSH_HOST_KEY_POLICY` (`--ssh-host-key-policy`): `tofu` in development and `approve` in production by default; `pinned` is allowed; `ca` is per profile only.

### Verification

- `internal/hostaccess/host_keys_test.go` (new), the real controller loop over SQLite, the Secret Store and SSH test servers:
  - TOFU pins every presented key (Ed25519 and ECDSA), then verifies, and audits `source=tofu`;
  - approve: a password profile under a `tofu` default waits with **0 authentication attempts**; operators are refused; approval needs the named fingerprints and the current spec generation, and succeeds after a status write lands in between;
  - a changed key gives `HostKeyChanged` with **0 authentication attempts**; trust is unchanged, approval without replace is refused, and an explicit replace works;
  - `pinned` makes **0 connections**;
  - `ca` verifies a certified host with no fingerprint;
  - profile validation.
- `TestLoadSSHHostKeyPolicy` (5 cases).
- `go test -race -count=3 -shuffle=on` on `hostaccess`, `controlmanager`, `service`, `store` and `sshclient`: pass. golangci-lint: only the 2 pre-existing issues.

## 2026-11-18 — Audit Phase 13A: SSH host-key scanning and multi-key verification (branch audit/host-keys)

- `pkg/sshclient/hostkeys.go` (new):
  - `ScanHostKeys` returns a host's keys, one per algorithm family (Ed25519, ECDSA, RSA). Each handshake stops right after the server proves its host key, **before authentication**, and the egress policy applies.
  - The host-key check accepts any fingerprint in `DialParams.TrustedFingerprints` (plus the old `ExpectedFingerprint`). When `HostCertAuthorities` is set, it also accepts host certificates signed by those CAs for the dialed host name. Any rejection is reported as `ErrHostKeyUnknown` with the presented fingerprint.
  - `ParseHostCertificateAuthorities` reads CA keys in authorized_keys format and refuses certificates.
- `pkg/sshclient/client.go`: uses the new check. The connection is now closed when setting the handshake deadline fails (it leaked before).
- `internal/testutil/sshtest`: options for an extra ECDSA host key and for a host certificate signed by a test CA; `Server.Fingerprints`.

### Verification

- `go test -race -count=3 ./pkg/sshclient/`: pass. New tests:
  - the scan returns every key and makes 0 authentication attempts;
  - the scan of a denied target is refused and never reaches the server;
  - a fingerprint list is accepted, and an unrelated one is refused with no authentication attempt;
  - a certificate from the trusted CA is accepted, while one from another CA and an uncertified host are refused; a pinned fingerprint still works next to a CA;
  - CA parsing.
- golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13: stable SSH and policy tests (Q-25, Q-15) (branch audit/tests-2)

- **Q-25, corrected diagnosis.** AUDIT/03 said encrypted SSH keys are decrypted "on every production connection". That is wrong: the connector never sets `DialParams.Passphrase`, so production keys are never passphrase-decrypted (the Secret Store already encrypts them at rest). The intermittent failure came only from the passphrase subtests in `pkg/sshclient`. There, bcrypt-pbkdf takes about 0.1 s normally and 1.3 s under `-race` (measured), far more under shuffled parallel runs, against a 5 s budget. Those subtests now get a 60 s budget, documented in the test. No signer cache was added: it would keep decrypted private keys in memory for no production benefit.
- **Q-15:** four sleeps that stood in for synchronisation now check the real condition:
  - `controlmanager/engine_test.go`: instead of sleeping 50 ms, it asserts that a key re-added while processing is not runnable (`queue.Len() == 0`), which is the guarantee itself;
  - `sshclient/dial_policy_test.go` and `netpolicy/policy_test.go`: instead of sleeping 50 ms, a sentinel connection proves no denied dial reached the listener (accepts are ordered);
  - `sshclient/client_integration_test.go`: cancellation now waits until the server holds the connection, and the time is measured from cancellation.
  The remaining sleeps in tests are poll intervals of deadline-bounded waits, or simulated durations.

### Verification

- `go test -race -count=8 -shuffle=on ./pkg/sshclient/`: pass. `go test -race -count=5 -shuffle=on` on `pkg/sshclient`, `pkg/netpolicy` and `internal/controlmanager`: pass. golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13: managed-resource use cases end to end (branch audit/tests-2)

- `internal/service/managed_flow_test.go` (new): the managed-resource service on real SQLite stores and a real Secret Store, with owner, operator and reader members. It covers:
  - Access Profiles: owner-only create, update and delete; a profile pointing at a missing secret is refused; readers see only the secret reference;
  - Hosts: create needs `inventory:write`; selector listing and an invalid selector; labels need the right revision and `inventory:write`;
  - Operations:
    - preview, then submit refused while execution is disabled, for readers, for a stale reviewed-target hash, for a rule matching nothing, for a rule that includes an unbound host, and for a program outside the allowlist;
    - a successful submit;
    - listing;
    - cancel (readers refused, a repeat cancel is idempotent);
  - deletion: revision checks; deleting a profile still used by a host gives `ErrConflict`, and it works once unused.

### Verification

- `go test -race -cover ./internal/service/`: **81.2%** (28.9% at the start of the phase). golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13: runtime configuration tests (branch audit/tests-2)

- `internal/runtimeconfig/service_test.go` (new, on a real SQLite repository):
  - publish and rollback by an owner, applied to the live snapshot;
  - refused for operators, readers and owner API tokens, and for stale revisions, restart-only changes and invalid configurations;
  - get, list, status and checksum;
  - **apply is all-or-nothing on prepare**: a participant that refuses leaves the snapshot unchanged, rolls back the others, and records `apply_failed`; the revision applies once every participant accepts it;
  - snapshots are copies;
  - the watcher applies a revision published by another process and stops cleanly on cancellation.
- `internal/transport/http/v1/configuration/handler_test.go` (new): every endpoint and status code against the real service, including strict decoding (unknown fields and trailing JSON are refused).
- Not covered on purpose: a commit failure rolls back every participant, including ones never committed (AUDIT/03 Q-16). That is a known bug scheduled for Phase 19, so no test locks it in.

### Verification

- `go test -race -cover`: `runtimeconfig` 13.3% → **78.2%**; `v1/configuration` 0% → **91.6%**. golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13: API token, secret and environment tests (branch audit/tests-2)

- `internal/service/api_token_service_test.go` (new):
  - tokens are `mch_pat_` plus a random value, and only their SHA-256 reaches the store;
  - the name is trimmed, the default lifetime is 30 days, and an explicit lifetime is honoured;
  - only owners can issue, list or revoke, and only `operator` or `reader` roles are accepted;
  - name and lifetime bounds are enforced;
  - list and revoke are scoped to the caller's user and workspace.
- `internal/transport/http/v1/secret/handler_test.go` (new), on a real SQLite secrets service:
  - **no response from any endpoint carries secret material**;
  - rotation needs `expectedVersion` (409 when stale);
  - delete retires the secret, keeping its metadata;
  - rewrap from an unknown key gives 503;
  - the service itself refuses operators, readers and owner API tokens (403), not only the router.
- `internal/transport/http/v1/tokens/handler_test.go` (new): the role is normalised; the stored hash is never returned; every error maps to its status, and causes are not leaked.
- `internal/transport/http/v1/environments/handler_test.go` (new): **every call uses the caller's workspace even when the body names another one**; every error status.

### Verification

- `go test -cover`: `v1/secret` 0% → **93.4%**; `v1/tokens` 0% → **100%**; `v1/environments` 0% → **100%**. golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13: login and session tests (branch audit/tests-2)

- `internal/service/auth_service_test.go` (new):
  - signup normalises the username and email, stores only a bcrypt hash, enforces the 12–72 byte password bounds, and refuses duplicates in any case;
  - login works by username or email in any case;
  - a wrong password and an unknown account give the same `ErrInvalidCredentials`, while a store failure does not;
  - session JWTs are HS256 with issuer `machinarchy`, carry the user and workspace claims, use the configured lifetime (24h when 0), and fail to verify with another secret;
  - `VerifySession`, and no session for a user without a workspace.
- `internal/transport/http/v1/auth/handler_test.go`:
  - the session cookie is HttpOnly, SameSite=Lax and `Path=/`, with `Secure` following production mode;
  - no cookie on any failure, and a store error is not leaked;
  - signup follows the runtime setting over the static one, with 409, 400 and 403 cases;
  - logout expires the cookie; `GET /me` cases.

### Verification

- `go test -cover`: `v1/auth` 12.1% → **93.1%**; `service` 28.9% → 38.5%. golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 13: route authorization matrix (branch audit/tests-2)

- `internal/bootstrap/authz_matrix_test.go` (new): `TestRouteAuthorizationMatrix` builds the fully wired API server (`Runtime.NewAPIServer`: real SQLite stores, services, JWT and role middleware).
  - It creates an owner, an operator and a reader, plus reader and operator API tokens, through the API, and real resources so path IDs exist.
  - It calls every route documented in `/openapi.json` as each of the 6 principals: 401 for anonymous, 403 for a forbidden role, and anything else for an allowed one.
  - A documented route missing from the matrix, or a matrix entry that is not documented, fails the test.
- `internal/transport/http/server.go`: `Server.Handler()`, a test seam that returns the wired handler.

### Verification

- The test passes (59 routes × 6 principals).
- Mutation checks, reverted afterwards:
  - letting readers through the environments write group fails the test with 6 errors;
  - removing the owner check from `ManagedResourceService.CreateAccessProfile` fails it with 4 errors.
  - Removing `RequireHuman` from `/tokens` was **not** detected, because API tokens cannot hold the owner role that `tokens:manage` requires, so that check is redundant there. This is recorded rather than changed.
- golangci-lint on the changed packages: 0 issues.

## 2026-11-18 — Audit Phase 12: end-to-end test in NATS operator mode (branch audit/agent-authz)

- `test/e2e/agent_test.go`: the real monolith and the real agent now run against NATS in operator mode (`natstest`).
  - The gateway uses a controller user with `ControllerPermissions`, plus the account signing key; there is no shared enrolment token.
  - The owner issues a per-host token through `POST /api/v1/agents/enrollment-tokens`, bound to the machine's hostname.
  - The agent starts with the bootstrap credential.
  - New checks: a second agent with the same token is refused and exits 78; the token list shows the agent that used it and never the secret; the agent stored its NATS user JWT.
  - The earlier checks (signed calls, renewal, revocation with exit 78, metrics, SIGTERM) all now run over per-agent credentials.

### Verification

- `MACHINARCHY_E2E_REQUIRED=1 go test -race ./test/e2e/`: pass (20.3 s), against agent `996e8a3`.

## 2026-11-18 — Audit Phase 12: NATS operator-mode setup generator (branch audit/agent-authz)

- `deployments/nats/generate` (new, `go run ./deployments/nats/generate -out DIR`): creates the operator, the system account, the `MACHINARCHY` account with JetStream and a signing key, and two users:
  - `controller`, with `agentgateway.ControllerPermissions` and allow-responses;
  - `bootstrap`, with `agentv1.BootstrapPermissions`.
  It also writes `nats-server.conf` (memory resolver, TLS block to enable) and env snippets. All files are 0600, and it refuses to overwrite. Written in Go instead of an `nsc` script so it is tested; `nsc` was not available here (AUDIT M-10).
- `internal/agentgateway/natsjwt.go`: `ControllerPermissions()`, the single definition of the gateway's NATS reach. `natstest.ControllerCredentials` now takes permissions, so the gateway tests run with exactly those, not an unrestricted user.
- `docs/operations.md`: "NATS in operator mode", with the generated files and a permission table for `nsc` users.

### Verification

- `go test -race ./deployments/nats/generate/`: `TestGeneratedSetup` loads the generated config with `server.ProcessConfigFile`, starts it, and checks:
  - the controller can create the command stream and answer registrations;
  - bootstrap can send a registration request, but cannot read registrations or use the JetStream API;
  - the controller cannot read the default inbox;
  - the files are 0600, and a second run refuses to overwrite.
- `TestGatewayIssuesScopedNATSCredentials` passes with the restricted controller user.
- golangci-lint: only the 2 pre-existing issues. The first run found 4 unchecked `PublicKey` errors and a G302 on the directory chmod (now 0700 with a justified `#nosec`); both were fixed before committing.

## 2026-11-18 — Audit Phase 12: protocol contract test (branch audit/agent-authz)

- `internal/agentgateway/contract_test.go` (new): `TestProtocolGoldenFixtures` finds the protocol module (`go list -m`), and verifies every golden fixture in `agentv1/testdata` with the published test keys (controller seed 0x01, agent seed 0x02). Each fixture must also fail with the other party's key. It fails on a fixture it does not know, and on an expected fixture that is missing (AUDIT M-11).

### Verification

- `go test ./internal/agentgateway/ -run TestProtocolGoldenFixtures`: pass, against agent `fb5f243` (4 fixtures). golangci-lint: 0 issues.

## 2026-11-18 — Audit Phase 12: per-host, single-use agent enrolment tokens (branch audit/agent-authz)

**Behaviour change:** the shared `MACHINARCHY_AGENT_GATEWAY_ENROLLMENT_TOKEN_FILE` is now optional and refused in production. Hosts enrol with owner-issued tokens.

- Migration `000002_agent_enrollment_tokens` (both dialects): stores the token hash, an optional hostname, the issuer, the expiry, and the usage and revocation fields.
- `internal/agentgateway/enrollment.go` (new):
  - `NewEnrollmentToken`: `mae_` plus 32 random bytes, SHA-256 hash only, 1h default lifetime (1m–24h), optional hostname normalised to lower case without a trailing dot;
  - the `Enrollment` and `TokenStore` types;
  - `ErrEnrollmentRejected`.
- `Store.RegisterAgent` now takes the enrolment (AUDIT M-4):
  - agents are looked up by public key only (before: `request_id OR public_key`);
  - a retry of the same request needs no new token;
  - anything else consumes a valid token in the same transaction (the UPDATE re-checks `used_at`, so two registrations cannot share one token);
  - the `agent.registered` audit event records `enrollment` and `token_id`.
- The gateway hashes the presented token for the store, and accepts the shared token only if configured.
- `internal/store/agents`: the token store (create, list, revoke unused), with audit events `agent.enrollment_token_issued` and `agent.enrollment_token_revoked`.
- API (owner and human only), with views that never include the secret or its hash:
  - `POST /api/v1/agents/enrollment-tokens` (returns the secret once);
  - `GET /api/v1/agents/enrollment-tokens`;
  - `DELETE /api/v1/agents/enrollment-tokens/{id}`.
- OpenAPI regenerated. Docs: "Enrolling a host" in `docs/operations.md`, the runbook row, the README route list.

### Verification

- `go test -race ./internal/agentgateway/` (`TestPerHostEnrollmentTokens`, `TestNoSharedTokenAcceptsOnlyIssuedTokens`, `TestNewEnrollmentToken`), `./internal/transport/http/v1/agents/` (new `TestEnrollmentTokenEndpoints`) and `./internal/config/`: pass.
- Store contract `agents` (rewritten: single use, retry, hostname binding, expiry, revocation, no enrolment, shared token, audit details): pass on SQLite and on **PostgreSQL 16**. `migrations are idempotent` now expects 2 migrations.
- `make api-docs`; the route-coverage test passes. golangci-lint `./internal/...`: only the 2 pre-existing issues. gosec flagged the name `tokenColumns` as a hardcoded credential (false positive); it was renamed.

## 2026-11-18 — Audit Phase 12: per-agent NATS credentials from the gateway (branch audit/agent-authz)

**Behaviour change:** in production, the agent gateway now requires `MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT` and `_NATS_ACCOUNT_SIGNING_KEY_FILE` (NATS operator mode).

- `internal/agentgateway/natsjwt.go` (new): `NATSIssuer`. Each agent gets a NATS user JWT whose subject is its Ed25519 identity key as a user nkey and whose permissions are `agentv1.AgentPermissions(agentID)`. It is signed with an account signing key, with a lifetime of 1h by default (5m–24h, at least twice the heartbeat) (AUDIT M-10).
- `internal/agentgateway/gateway.go`: register and renew responses carry the JWT when an issuer is configured. A revoked or unknown agent gets none. The gateway uses the `_INBOX_CONTROLLER` inbox prefix, so enrolling agents cannot read its replies.
- `internal/config/config.go`: `MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT`, `_NATS_ACCOUNT_SIGNING_KEY_FILE` (0600, must be an account seed) and `_AGENT_JWT_TTL`. Both keys go together, and production requires them. `internal/bootstrap/agents.go` wires the issuer and logs whether it is on.
- `internal/testutil/natstest` (new): embedded NATS in operator mode (operator, system account, JetStream account with a signing key) for tests.
- `go.mod`: `nats-io/jwt/v2` and `nkeys` become direct requirements (they were already in the graph; same versions).
- `docs/operations.md`, `.env.example`: the three settings.

### Verification

- `go test -race ./internal/agentgateway/ ./internal/config/`: pass.
  - New `TestGatewayIssuesScopedNATSCredentials` (operator-mode NATS). The JWT subject is the agent key, the issuer account and permissions are exactly as specified, and the expiry is about the lifetime. With the JWT, the agent renews over its own connection and binds its own consumer, but cannot subscribe to registrations. A revoked agent gets no JWT.
  - New `TestNATSIssuerValidation` (6 cases), plus 9 new config cases and a production case.
- `MACHINARCHY_E2E_REQUIRED=1 go test -race ./test/e2e/`: pass, in development mode without JWT issuing, against agent `996e8a3`.
- golangci-lint on the changed packages: 0 issues.

## 2026-11-18 — Audit Phase 11: document invoke results (branch audit/http-hardening)

- `internal/transport/http/v1/agents/handler.go`: the OpenAPI description of `POST /api/v1/agents/{id}/invoke` now says that 200 means the agent answered and the body's `status` must be checked. `rejected` and `failed` are agent answers and return 200; 503 and 504 mean there was no answer. 504 is now listed. Behaviour is unchanged (decision for AUDIT/09 F9-05: keep 200, document it).
- `docs/swagger.json` and `docs/swagger.yaml` regenerated with swag v1.16.4; only the invoke entry changed.

### Verification

- `make api-docs`; `go test ./internal/transport/http/v1/` (route-coverage test): pass. golangci-lint on the agents handler: 0 issues.

## 2026-11-18 — Audit Phase 11: remove unreachable code (branch audit/http-hardening)

Removed everything `deadcode -test ./...` reported as unreachable from any program or test (AUDIT/09 F9-04 and §1.3):
- `Runtime.Metrics` (`internal/bootstrap/runtime.go`), never called;
- the `Gateway.nc` field (`internal/agentgateway/gateway.go`), written but never read;
- `SnapshotManager.Check` (`internal/runtimeconfig/config.go`), never called;
- `eventing.Command`, `CommandMetadata` and `HostSpecUpdateCommand` (`internal/eventing/contracts.go`), used only by their own test, which is removed too. Host spec changes go through `ManagedResourceService` and the outbox events.

### Verification

- `deadcode -test ./...` (x/tools v0.52.0): no findings in machinarchy (before: 4).
- `go test -race` for `eventing`, `bootstrap`, `agentgateway` and `runtimeconfig`: pass. golangci-lint on those packages: 0 issues.

## 2026-11-18 — Audit Phase 11: CLI refuses tokens over plain HTTP (branch audit/http-hardening)

**Behaviour change:** a CLI context whose server is `http://` on a non-loopback host now fails with a usage error until the cluster is re-added with `--insecure-http`.

- `internal/cli/config/config.go`: `checkTransport` and `IsLoopbackHost`. `Resolve` and `SetCluster` refuse plain `http://` to anything but `localhost`, `*.localhost` or a loopback IP. `Cluster.InsecureHTTP` (`insecure-http` in the config file) is the per-cluster opt-in. It does not apply to a server overridden by flag or environment (AUDIT S-18).
- `internal/cli/commands/config.go`: `config set-cluster --insecure-http`.
- Tests that used `http://*.example` servers for precedence checks now use `https://`; `docs/cli/README.md` documents the rule.

### Verification

- `go test ./internal/cli/...`: pass. New tests: `TestPlainHTTPTokenTransport` (10 cases, including an override bypass attempt and a `localhost.example.test` look-alike), `TestSetClusterRequiresOptInForRemoteHTTP`, and two command-table cases. With the guard disabled, both new config tests fail.
- golangci-lint `./internal/cli/...`: only the 2 pre-existing issues (`client_test.go` unused parameter, `APIError` stutter).

## 2026-11-18 — Audit Phase 11: API docs and metrics off the public listener (branch audit/http-hardening)

**Behaviour change:** in production, `/openapi.json`, `/scalar` and `/scalar/init.js` are no longer served unless `MACHINARCHY_API_DOCS_ENABLED=true`.

- `internal/config/config.go`: new `MACHINARCHY_API_DOCS_ENABLED` (`--api-docs-enabled`; default `true` in development, `false` in production) and `MACHINARCHY_METRICS_LISTEN_ADDR` (`--metrics-listen-addr`; must be host:port on a different port from the API).
- `internal/transport/http/server.go`: `registerOperationalRoutes` serves health always, `/metrics` only when there is no separate metrics listener, and the docs only when enabled. `useMetricsListener` binds a second listener that serves `/metrics` and nothing else. It is bound before readiness is signalled and shut down within the same budget as the API listener (AUDIT S-17).
- `docs/operations.md`, `README.md`, `.env.example`: the two settings.

### Verification

- `go test -race ./internal/transport/http/ ./internal/config/`: pass. New tests: `TestOperationalRoutesFollowConfiguration` (4 combinations), `TestMetricsListenerServesOnlyMetrics` (serves metrics, 404 for anything else, closed after shutdown) and `TestLoadOperationalEndpoints` (8 cases).
- golangci-lint on the changed packages: 0 issues. The first commit attempt had 2 (an unclosed response body in a test, and an unchecked `SplitHostPort`); both were fixed before amending.
- Manual: a production monolith on SQLite with `MACHINARCHY_METRICS_LISTEN_ADDR=127.0.0.1:19090`:
  - the API port answered `/healthz/ready` 200, and `/metrics`, `/openapi.json` and `/scalar` 404;
  - `/api/v1/me` returned 401 with HSTS, `no-store` and the deny-all CSP;
  - the metrics port served `/metrics` 200 and `/healthz/live` 404;
  - SIGTERM: exit 0, and both ports closed.

## 2026-11-18 — Audit Phase 11: security headers and API reference policy (branch audit/http-hardening)

- `internal/transport/http/middleware/headers.go` (new): `SecurityHeaders` sets `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, same-origin COOP and CORP, and a deny-all CSP on every response. HSTS is added in production only. `/api` responses add `Cache-Control: no-store`. It is used by the API server and by the operations server (AUDIT S-16).
- `internal/transport/http/scalar.go`: the reference page has its own CSP (scripts only from this origin and the pinned `@scalar/api-reference@1.75.0` file, no inline scripts). The init code moved to `/scalar/init.js`, and Scalar's telemetry and web fonts (`fonts.scalar.com`) are off (AUDIT S-11). SRI is not added: computing the hash needs the file, and fetching it was out of scope.
- `docs/operations.md`: new "Security headers" section.

### Verification

- `go test ./internal/transport/http/ ./internal/transport/http/middleware/`: pass (new `TestSecurityHeaders`, `TestSecurityHeadersCanBeOverriddenByHandler`, `TestScalarReferencePolicy`).
- golangci-lint on `internal/transport/http/...`: 0 issues.
- Manual: a development monolith served `/scalar` in the browser pane. The reference rendered the spec, and the console showed no CSP violations once the web fonts were disabled. Before that, only the 14 font loads from `fonts.scalar.com` were blocked.

## 2026-11-18 — Audit follow-up: e2e requires exit 78 from a revoked agent (branch audit/followup)

- `test/e2e/agent_test.go`: after revocation the test now waits for the agent to exit by itself and requires exit status 78 (machinarchy-agent `5ec5beb`, AUDIT/09 F9-02). Before, it accepted any exit, and usually stopped the agent with SIGTERM before it noticed. **CI's `MACHINARCHY_AGENT_REF` must point at an agent revision that includes `5ec5beb`.**
- `docs/runbook.md`: the revoked-agent row mentions exit 78 and the restart policy.

Verification: `MACHINARCHY_E2E_REQUIRED=1 go test -race -count=2 ./test/e2e/` passes with the current agent; against agent `a7aa03c` it fails with "revoked agent exited with exit status 1, want exit status 78".

## 2026-11-18 — Audit follow-up: govulncheck gate with reviewed allowlist (branch audit/followup)

- `scripts/security/govulncheck.sh` (new): runs `govulncheck -format json` and fails (exit 3) on any vulnerability the code calls, unless `scripts/security/govulncheck-allowlist.txt` has an unexpired entry for it and no fixed version exists. It notes allowlist entries that no longer match.
- `scripts/security/govulncheck-allowlist.txt` (new): accepts GO-2026-6875 and GO-2026-6872 (Echo v4.16.4, no fix; not exploitable here, AUDIT/02 S-22) until 2027-02-16.
- `make vuln` and the CI `security` job use the script. Before, CI ran plain `govulncheck ./...`, which exits 3 on these two advisories, and `orchestrate-release` needs `security`, so every release was blocked (AUDIT/09 F9-01).
- `docs/SECURITY.md` explains how to accept an advisory; `.github/WORKFLOWS.md` and `README.md` updated.

### Verification

- `scripts/security/govulncheck.sh` against the real module: exit 0, both advisories reported as accepted.
- Failure paths: empty allowlist → exit 3 for both; an expired entry → exit 3, and a stale entry is noted; a fake report with a fixed version → exit 3 ("upgrade the module").
- `shellcheck` clean. `make vuln`: exit 0; with `GOVULNCHECK_ALLOWLIST=/dev/null` it fails. `ci.yml` parses as YAML.
- Not verified: the workflow on GitHub (nothing pushed).

## 2026-11-18 — Audit Phase 8: tests, CI/CD and operations (branch audit/ops; recorded late)

This entry was missing when Phase 8 was committed; it is added here and summarizes those commits (`99b32db`..`d632af4`).

- **Agent gateway** (`internal/agentgateway`, `internal/store/agents`, `agents` table in both baselines, `/api/v1/agents` routes, `MACHINARCHY_AGENT_GATEWAY_*` settings): controller side of `protocol/agentv1`, imported from `../machinarchy-agent/protocol` through `replace`. Runs in the Control Manager and monolith roles. Synchronous capability calls only; no Operation integration.
- **Fixes found by new tests:** `globMatch` with `*` in values (fuzz); per-host timeouts recorded as `TimedOut`/`timeout`; secret rotation keeps the stored name; a revoked agent re-enrolling gets `enrollment_rejected`; the image's `/data` is `0700`.
- **Tests:** agent end-to-end test (`test/e2e`), 7 fuzz targets, operation worker, secrets, JWT middleware and gateway tests.
- **Runtime:** Control Manager, operation worker and gateway Prometheus metrics; 25 s API request deadline; `healthcheck` subcommand in the server binaries.
- **Build and CI:** digest-pinned multi-stage images with healthchecks and allowlist `.dockerignore`; reproducible ldflags builds; `ci.yml` rewritten (verify, security, images, release) with the agent repository checked out alongside.
- **Docs:** README, `docs/operations.md`, new `docs/runbook.md`, `.env.example`.

Verification (run at the time and repeated in AUDIT/09 on a clean clone): build, vet, `go test -race ./...` (33 packages, including the end-to-end test) and PostgreSQL 16 contract suites pass; lint has the 2 pre-existing issues; gosec 0; coverage 48.4%. CI never ran on GitHub.

## 2026-11-18 — Audit Phase 7: cleanup

**Recreate existing databases.** The schema is now one baseline per dialect; older databases are refused at startup.

- The legacy host, run, fleet, job and metrics-history stack is deleted (API, services, stores, tables). The managed API is complete (list/delete/labels, Access Profile reads, operation lists, target preview bound by a hash) and the CLI uses it: `get hosts|profiles|operations`, `create|delete|label host`, `run`, `cancel operation`.
- One store implementation per area for SQLite and PostgreSQL (`store/accounts`, `store/resources`, `store/management`, helpers in `store/sqlx`); PostgreSQL uses pgx.
- One error format, `{"error", "code"}`, and one list format, `{"items", "nextCursor"}`. The OpenAPI spec covers every route and a test keeps it that way.
- Destructive CLI commands ask for confirmation (`--yes`, `--no-input`).
- `make smoke` drives the CLI against a fresh monolith. ADRs are merged into `docs/adr` (ADR-055 records the managed-only model); phase documents moved to `docs/archive`.

## 2026-11-17 — Makefile refactor

### Changes in this update

- Restructured the developer Makefile into five topic files: `mk/config.mk`, `mk/build.mk`, `mk/checks.mk`, `mk/containers.mk` and `mk/help.mk`. These replace `go.mk`, `quality.mk`, `test.mk`, `security.mk`, `compose.mk` and `ssh.mk`. Removed the stale `mk/README.md`.
- **Fixed `make build` and `build-release`.** They no longer build the deleted `cmd/agent`. Removed `build-agent` and `run-agent`.
- Version metadata (`VERSION`, `COMMIT`, `BUILD_TIME`) is computed once per invocation and can be overridden.
- Go linker flags moved to `GO_LDFLAGS`, so an exported C `LDFLAGS` can no longer leak into Go builds.
- `build-release` uses target-specific variables instead of a recursive `make`.
- Shared helpers (`go_build`, `run_with_env`, `require_tool`) replace repeated recipes.
- **`run-*` targets take extra arguments through `ARGS="..."`.** The previous trick that turned extra command-line words into no-op targets is gone: it could silently trigger real targets such as `build`. The env file is now sourced by absolute path.
- Added `tidy-check` (`go mod verify` + `go mod tidy -diff`) and included it in `verify` and `verify-full`.
- `lint` no longer re-runs `fmt-check`; golangci-lint already enforces gofmt.
- `verify-full` uses `test-race` instead of running the suite twice.
- `help` now shows usage and common variables.
- Updated `docs/development/getting-started.md`.

### Verification

- All 57 targets shared with the previous Makefile were dry-run (`make -n`) and diffed against it. 34 are identical. The differences in the rest are the intended ones listed above.
- Ran successfully: `make -j4 build`; `make build-release VERSION=1.0.0` (binary reports 1.0.0 and was built with `-trimpath`); `make build-cli VERSION=9.9.9-test`; `make run-cli ARGS=version`; `make vet tidy-check`; `make test` (29 packages ok); `make clean`.
- `make verify`, `make shellcheck` and `make vuln` run correctly but exit non-zero on findings that predate this change: 4 golangci-lint issues, shellcheck errors in `scripts/dev/setup-local.sh` and `scripts/usecases/run.sh`, and the Echo advisories GO-2026-6875/6872. These are tracked in `AUDIT/00` and `AUDIT/02`.
- Tested with GNU Make 3.81, the macOS default.

## 2026-11-14 — CLI foundation and API client (Phase 2)

### Changes in this update

- Added `internal/cli/clierr`: classified CLI errors with stable exit codes (0 ok, 1 failure, 2 usage/config/input, 3 auth, 4 API/network/timeout, 6 not found, 7 conflict, 130 interrupted; 5 reserved for wait deadlines) and remediation hints.
- Reworked `internal/cli/api/client.go` around one request path: bearer authentication, `Accept`/`User-Agent` headers applied in a single place; configurable per-request timeout (`--request-timeout`, default 30s); caller-context cancellation; response bodies always closed; bounded success and error bodies; escaped path segments preserved (an identifier containing `/` can no longer change the route; dot and empty segments are rejected); redirects are no longer followed so the token cannot be replayed elsewhere; 202/204 empty bodies handled; token validated before use. Resource methods and the `Client`/`ManagementClient` interfaces are unchanged.
- Added `api.APIError` (status, sanitised message, request ID, `Retry-After`, structured fields) with status-to-exit-code mapping and hints; transport failures now report the cause without repeating the request URL.
- `internal/cli/config`: the YAML document is parsed directly (Viper now only resolves flag/environment overrides), so context, cluster and user names keep their exact case and dots; config errors are classified and carry hints; added request-timeout, quiet, no-headers and no-input settings, an explicit-output accessor, `LoadSettings` (no file read), `ValidateToken`, size-bounded reads, and a stderr warning (never fatal) when a config file containing tokens is readable by other users. `--yes` is never read from the environment or config file. Undocumented top-level preference keys in the YAML file (`output`, `server`, ...) are no longer honoured; the documented format is unchanged.
- `internal/cli/output`: added a shared `Printer` (table, JSON, YAML, `--no-headers`, `--quiet`); `PrintHosts`/`PrintHost` remain and produce identical output.
- `internal/cli/factory.go`: `Printer`, `OutputFormat` (per-command default), `Confirm`/`RequireConfirmation` (never prompts without a terminal or with `--no-input`), config warnings on stderr.
- `internal/cli/root.go`, `run.go`, `cmd/machinarchy/main.go`: added global flags `--server`, `--request-timeout`, `--quiet/-q`, `--no-headers`, `--no-input`, `--yes/-y`; `--output` now defaults to "per command" instead of a fixed `table` (existing behaviour preserved); global settings are validated before any command runs; duplicate or reserved command names panic at registration; `help`, `completion`, `version` and group help no longer read the config file; command groups are strict: an unknown subcommand (for example `secret lst`) now exits 2 instead of printing help and exiting 0; `cli.Run` owns error presentation (`Error:` / `Hint:`), maps errors to exit codes, and Ctrl-C now exits 130 (previously 0).
- Commands: `get hosts` uses the shared printer and classified errors (host/environment not found = 6, ambiguous = 2); `config` and `runtime-config`/`secret` input errors are usage errors (2); `writeJSON` tolerates an empty body; added `machinarchy version`.
- Tests: new unit tests for clierr, the API client (headers, serialization, error classification, redirects, cancellation, timeouts, body closure, size limits, path escaping), config (names, precedence, permissions, errors), output, factory/confirmation, root/exit codes, command registration/help/compatibility, `get hosts`, `config` end to end, and management commands against a real HTTP test server; plus an ADR-035 guard test that fails if CLI code imports application, database or transport packages or uses `net/http` outside `internal/cli/api`.
- Docs: `docs/cli/configuration.md`, `commands.md`, `troubleshooting.md` updated (global flags, precedence, environment variables, exit codes, command groups, `version`, config-file security). Corrected the documented default config path to `.machinarchy/config.yaml` to match the code.

### Validation

- **Not compiled and not run.** The authoring environment had no Go toolchain and no network access, so `go build`, `go vet`, `gofmt`, `golangci-lint` and `go test` were NOT executed. All code and tests were checked by reading and by a delimiter-balance script only; compile and test failures are possible and must be checked with `go build ./... && go test ./internal/cli/... ./cmd/...` before merging.
- Behaviour that depends on Cobra or Viper internals (group typo handling, flag/environment precedence, `help` skipping the config file) was reasoned from their documented behaviour, not observed.

## 2026-11-12 — Refresh implemented API and CLI use cases

### Changes in this update

- Rewrote `docs/examples/example-usecases.md` against the current API, CLI, managed Host/Access Profile/Operation, legacy Host/Run, runtime configuration, and Secret Store implementations. Clarified the distinct legacy and managed workflows, corrected endpoint and response semantics, and documented current limitations instead of planned functionality.

### Validation

- Cross-checked documented routes, payload fields, CLI commands, authorization requirements, and lifecycle behavior against handlers, services, runtime composition, and CLI command registrations.
- `bash -n` over all 26 shell examples and `git diff --check` — passed.
- `GOCACHE=/tmp/machinarchy-doc-gocache go test -count=1 ./internal/cli/...` and `GOCACHE=/tmp/machinarchy-doc-gocache go run ./cmd/machinarchy --help` — passed. No live API calls or full application tests were run; the examples require a configured Machinarchy service and credentials.

## 2026-11-12 — Phase H managed Host access and Operations

### Changes in this update

- Completed the managed Host Access Profile path from approved ADR-054: workspace-bound immutable profile revisions, SQLite/PostgreSQL migrations, binding-aware Secret Store authorization, Host-owned endpoint/trust, and durable profile-change notifications that fan out reconciliation.
- Added the connector-neutral HostConnector and Go SSH adapter with pinned host-key verification, version-pinned SecretRef resolution, bounded connections, context deadlines, argv quoting, separate bounded stdout/stderr capture, safe error classes, and cancellation cleanup. Wired managed Host reconciliation into Control Manager and canonical managed Host/Profile/Operation HTTP application routes.
- Added a separate bounded Operation worker with durable outbox scheduling and level scans, authorization/policy checks, cancellation, encrypted persisted results, PostgreSQL lease fencing, and restart recovery that marks uncertain executions Interrupted rather than replaying commands. Optimistic conflicts during cancellation, target updates, and finalization reload and retry; Host deletion cancels active Operations and waits for terminal state before removal.
- Changed SSH egress defaults to deny private/loopback ranges and all TCP ports unless explicitly configured. Added bootstrap and hot-reloadable runtime port allowlists; updated legacy test fixtures to opt in where they intentionally use private LAN addresses and ephemeral test ports. Updated architecture, security, environment-example, ADR-054, and Phase H documentation.

### Validation

- `GOCACHE=/tmp/machinarchy-go-cache go test -count=1 -timeout 90s ./internal/domain/resource ./internal/secrets ./internal/hostaccess ./internal/operationworker ./internal/controlmanager ./internal/store ./internal/service ./internal/bootstrap ./internal/transport/http/v1/managedresource ./pkg/netpolicy ./pkg/sshclient` — passed.
- `GOCACHE=/tmp/machinarchy-go-cache go test -count=1 -run '^TestManagedHostAndOperationEndToEnd$' -v -timeout 30s ./internal/hostaccess` with local-listener permission — passed, covering registration, persistence/outbox, SSH reconciliation, Operation execution/result, cancellation, restart recovery, and Host deletion.
- `GOCACHE=/tmp/machinarchy-go-cache go test -race -count=3 -run '^TestManagedHostAndOperationEndToEnd$' -timeout 2m ./internal/hostaccess` with local-listener permission — passed, including long-running command and cancellation conflict coverage.
- `GOCACHE=/tmp/machinarchy-go-cache go test -count=1 -timeout 2m ./...` with local-listener permission — passed. The same command without that permission failed only at the existing HTTP listener bind test because this sandbox disallows socket binding.
- `git diff --check` — passed. PostgreSQL integration was not run: `MACHINARCHY_TEST_POSTGRES_URL` is unset and Docker daemon access is denied. Lint checks were not run, per the current instruction to stop focusing on linters.

### Known limitations

- PostgreSQL migration, transaction, and multi-manager lease behavior still require integration validation against a live PostgreSQL service. Deployments must mirror application CIDR/port policy in an independent egress firewall. Encrypted private-key passphrases and automatic retry of uncertain remote commands are unsupported. Existing legacy Host and Run records are preserved and are not automatically migrated into managed Host resources.

## 2026-11-12 — Phase H.2 Access Profile domain and persistence foundation

### Changes in this update

- Added workspace-owned Access Profile domain/revision types, optimistic head update and repository contract; added SQLite/PostgreSQL migration 000011 for profile heads/revisions and Host lookup indexing.
- Added transactional profile create/revision, audit and outbox event persistence, workspace-bound Host profile validation, tombstone deletion checks, and a binding-aware Secret Store authorizer for Control Manager and Operation worker contexts.
- Added SQLite resource contract coverage for immutable revisions, stale update conflict, Host reference lookup, cross-workspace assignment rejection, referenced-profile deletion conflict, and profile outbox events.

### Validation

- `GOCACHE=/tmp/machinarchy-go-cache go test -count=1 -timeout 60s ./internal/store -run '^TestManagedResourceContract/sqlite/Access_Profile_revision_isolation_and_outbox$'` — passed.
- Initial broader focused run exposed the outbox schema requirement that subject generation be positive; profile events now use their revision as generation. PostgreSQL integration was skipped because `MACHINARCHY_TEST_POSTGRES_URL` is unset. Full H.2 validation remains incomplete.

## 2026-11-12 — Phase H scoped Secret Store resolution boundary

### Changes in this update

- Replaced purpose-only secret resolution with a typed workload/resource request and required an injected authorization port before any secret lookup or decryption.
- Configured bootstrap with a deny-all resolver until the subsequent Access Profile milestone supplies a binding-aware authorizer; updated Secret Store tests to use explicit authorization fixtures.
- Added a narrow `.gitignore` exception so the `internal/secrets` source package and this change appear in repository status/reviews.

### Validation

- `gofmt`, `git diff --check`, `GOCACHE=/tmp/machinarchy-go-cache go test -count=1 -timeout 60s ./internal/store/management ./internal/bootstrap` — passed. Lint checks were not run, consistent with the current work-session instruction to stop focusing on linters.

## 2026-11-12 — Proposed Phase H Access Profile ADR

### Changes in this update

- Added proposed ADR-054 covering workspace-scoped reusable Access Profiles, immutable revisions, Host/Operation binding, scoped Secret Store authorization required by ADR-038, deletion/disable behavior, outbox fan-out, and SQLite/PostgreSQL persistence implications.
- The ADR is explicitly proposed and requires owner approval; no implementation or migrations were changed.

### Validation

- Reviewed existing Host/Operation models, Secret Store contracts, ADR-028, ADR-038, and Phase 2/7 architecture documents. No tests were run because this update contains no code changes.

## 2026-11-12 — Phase G bounded KEK rewrapping

### Changes in this update

- Added migration 000010 to version secret value AAD while retaining the original AAD key ID for legacy ciphertext.
- Added resumable, owner-authorized batch rewrapping of stored DEKs to the active external KEK. Rewrapping leaves secret ciphertext and immutable version identifiers unchanged; each successful update is audited without key material or plaintext.
- Exposed rewrapping through the Secret API and CLI and documented the staged keyring/restart/rewrap/removal procedure.

### Validation

- `gofmt` and focused `go test -count=1 -timeout 60s ./internal/store/management ./internal/runtimeconfig ./internal/transport/http/v1/secret ./internal/cli/api ./internal/cli/commands` — passed, including bounded-batch resume, authorization, and legacy ciphertext compatibility.
- `go test -count=1 -timeout 2m ./...` — failed at `internal/transport/http/TestOperationsServerStartsAndShutsDown` waiting for its listener; other reported packages passed. PostgreSQL integration remains unavailable because no test URL is configured and Docker is unavailable. Lint checks were not run as directed.

## 2026-11-12 — Phase G runtime retry and key handling hardening

### Changes in this update

- Bounded configuration notification polling to one second and added capped exponential retry backoff after database or apply failures, avoiding rapid repeated reload attempts while preserving periodic recovery checks.
- Wipe temporary KEK copies after Secret Store use without modifying key-provider-owned buffers; added regression coverage for key ownership and retry backoff bounds.

### Validation

- `gofmt`, `git diff --check`, `go test -count=1 -timeout 60s ./internal/runtimeconfig ./internal/store/management`, focused API/CLI/bootstrap/config package tests — passed.
- `go test -count=1 -timeout 2m ./...` — failed at the existing `internal/transport/http/TestOperationsServerStartsAndShutsDown` listener test; the other reported packages passed. No HTTP code was changed in this update.
- PostgreSQL integration validation — blocked: `MACHINARCHY_TEST_POSTGRES_URL` is unset and the Docker daemon is unavailable. Lint checks were not run as directed for this work session.

## 2026-11-12 — Phase G configuration/secrets and bootstrap lifecycle verification

### Changes in this update

- Implemented global typed runtime configuration revisions, compare-and-swap updates, history/rollback, SQL outbox hints, process application status, atomic snapshots, periodic checks, and safe hot-reload of logging, SSH target policy, operation admission policy, and signup behavior. Restart-only worker/pool settings are reported as restart-required and rejected when changed at runtime.
- Added external bootstrap key/keyring file loading, encrypted versioned Secret Store records using per-version DEKs and AES-GCM authenticated data, owner/human authorization, metadata/audit endpoints, rotation, retirement, and CLI management through the API. Plaintext is write-only at the API and CLI.
- Fixed bootstrap lifecycle validation so non-cancellable contexts are rejected before component startup; lifecycle tests use explicit cancellation contexts.

### Validation

- Bootstrap regression test and full bootstrap package tests — passed.
- `go test -count=1 -timeout 2m ./...` — failed only at `internal/transport/http/TestOperationsServerStartsAndShutsDown` because its listener did not bind in this environment; all other listed packages passed.
- PostgreSQL test script — blocked because Docker cannot connect to the local daemon. PostgreSQL behavior remains unverified.
- `golangci-lint run` — not completed successfully; Go module cache writes under `/Users/mleus/go/pkg/mod/cache` are denied and package loading fails. No dependency files or lint rules were changed.
- Full Phase G docs/changelog completion is recorded here; configuration examples and user-facing documentation updated separately in this change.

Project change history maintained alongside the code. Entries are newest first. Each entry distinguishes changes made in that update from the implementation baseline and records only validation that was actually performed.

## 2026-11-11 — Phase F event infrastructure and control loop

### Changes in this update

- Added versioned command/event envelopes, transactional Host outbox records, per-consumer delivery claims, retry/quarantine state, retention, and PostgreSQL resource fencing/resync leases. SQLite remains the single-Control-Manager profile; PostgreSQL coordination uses database-backed claims and leases.
- Replaced the Control Manager's new resource path with an outbox dispatcher, deduplicating bounded resource-key queue, bounded workers, retries with jitter and global rate limiting, startup/periodic resynchronization, latest-state controller reads, and graceful drain/cancellation. Imperative Operation execution remains separate.
- Added a default Host controller that handles logical deletion and records `ConnectorUnavailable` for managed Hosts until a connector is wired. Existing legacy API/NATS Host flows are not dual-written to the canonical resource store and therefore are not reconciled by this path.
- Added migration 000008 for SQLite and PostgreSQL, cross-engine outbox/coordination contracts, lifecycle and failure tests, and Phase F execution/sequence documentation. No NATS or Kafka dependency was introduced.

### Validation

- `gofmt`, `git diff --check`, focused Phase F package tests, `go test -race ./internal/controlmanager ./internal/eventing ./internal/store ./internal/bootstrap ./internal/config`, and `go vet ./...` — passed.
- `go test ./...` — all listed packages passed except `internal/transport/http/TestOperationsServerStartsAndShutsDown`, which failed waiting for its listener to bind; no HTTP code was changed in this phase.
- PostgreSQL integration tests were skipped because `MACHINARCHY_TEST_POSTGRES_URL` was not configured. Benchmarks and security checks were not run.

### Known limitations

- The initial Host controller does not perform remote convergence; it reports unavailable connector capability and retries. The legacy API Host model is not migrated to or synchronized with canonical resource records. Multi-replica PostgreSQL coordination is implemented but was not integration-tested against a live PostgreSQL instance.

## 2026-11-11 — Phase E domain and persistence foundation

### Changes in this update

- Added connector-neutral managed Host and Operation domain types with UUID identity, desired/observed state, generation/revision semantics, conditions, validation, and lifecycle transitions. Kept the existing SSH-shaped Host and Run APIs intact.
- Added shared resource repository ports and SQLite/PostgreSQL persistence adapters, transactional audit writes, optimistic revision checks, encrypted Operation specifications and target output, target snapshots/results, workspace-safe environment references, indexes, and migration 000007 for each engine.
- Added domain and cross-engine repository contract tests for resource creation/read/update/deletion, generation and status updates, concurrent stale-write rejection, Operation lifecycle/encryption, rollback, and migrations. The canonical tables are additive; legacy `hosts` and `runs` data are not migrated or redirected, and event/outbox publishing remains out of scope.

### Validation

- `gofmt` — passed for changed Go files.
- `GOCACHE=/tmp/machinarchy-go-cache go test ./internal/domain/resource ./internal/store` — passed. SQLite migration and repository contracts ran; PostgreSQL contract tests were skipped because `MACHINARCHY_TEST_POSTGRES_URL` was not configured.
- `GOCACHE=/tmp/machinarchy-go-cache go test -race ./internal/domain/resource ./internal/store`, `go vet ./...`, and `git diff --check` — passed.
- `GOCACHE=/tmp/machinarchy-go-cache go test ./...` — all other packages passed, but `internal/transport/http/TestOperationsServerStartsAndShutsDown` failed waiting for its listener to bind. The same isolated test failed; no HTTP code was changed. Benchmarks and security checks were not run.

## 2026-11-11 — Phase D repository foundation and runtime composition

### Changes in this update

- Centralized API, Control Manager, and all-in-one process construction in `internal/bootstrap`; command entry points now load config, create signal contexts, and select a runtime role. All-in-one composes the existing API and worker components over one runtime.
- Added shared structured logger/telemetry initialization, listener-ready startup, bounded HTTP/worker shutdown, startup-context propagation, and propagation of runtime cleanup failures. Added broker URL validation with a setting-specific error.
- Updated runtime architecture documentation and added lifecycle/configuration tests. The Agent remains an explicit placeholder; no Agent runtime was added. No domain, API behavior, persistence schema, broker behavior, or security policy was changed.

### Validation

- `gofmt` and `git diff --check` — passed.
- `go test ./...` — passed.
- `go test -race ./internal/bootstrap ./internal/config ./internal/transport/http` — passed.
- `go vet ./...` — passed.
- `go build ./cmd/api ./cmd/controlmanager ./cmd/monolit ./cmd/machinarchy ./cmd/agent` — passed.
- `go run ./cmd/machinarchy --help` — passed. Runtime lifecycle tests cover API, Control Manager, and all-in-one component topologies; an external DB/NATS-backed process launch was not run.
- No PostgreSQL-specific integration startup, benchmark, or security test was run.

## 2026-11-11 — Phase C engineering roadmap and backlog

### Changes in this update

- Added `docs/architecture/phase-c-implementation-roadmap.md` with ten milestone outcomes, a 40-item GitHub-ready backlog, dependencies, acceptance/testing/documentation criteria, priority matrix, critical paths, deferred capabilities, and Definition of Done.
- Recorded approval-metadata reconciliation and product/security decision gates without introducing new architecture decisions. No source code, tests, migrations, or deployment files were changed.
- Phase D has not started and remains gated on owner approval.

### Validation

- `git diff --check` — passed. No tests, builds, benchmarks, or security checks were run because this update is documentation only.

## 2026-11-11 — Phase B repository gap analysis

### Changes in this update

- Added `docs/architecture/phase-b-repository-gap-analysis.md`, mapping the current repository to the approved architecture baseline, classifying implementation gaps and actions, recording risks and potential breaking changes, preserving existing test contracts, and ordering future implementation work.
- No source code, tests, configuration, or package layout were changed. No tests, builds, benchmarks, or security checks were run. The Go package listing could not be completed because the sandbox denied writes to the Go cache; dependency-cycle status remains unverified.
- Phase C is not started and remains gated on owner approval.

### Validation

- `git diff --check` — passed. No tests, builds, benchmarks, or security checks were run.

## 2026-11-11 — Implementation architecture baseline proposal

### Changes in this update

- Consolidated approved Phase 1–11 architecture decisions, ADR precedence, Phase 12 integration status, initial implementation scope, mandatory engineering/security/database/testing requirements, production-only conditions, deferred/rejected capabilities, known implementation gaps, and remaining owner decisions into `docs/architecture/implementation-baseline.md`.
- Read the supplied implementation execution prompt and repository guidance. No source code, migrations, deployment files, or tests were changed.
- The baseline is proposed for owner approval; Phase B remains gated on that approval.

### Validation

- `git diff --check` and whitespace checks of the baseline and changelog passed. No tests or implementation checks were run because this update is documentation only.

## 2026-11-11 — Phase 12 architecture validation and final blueprint

### Changes in this update

- Recorded approval to proceed from Phase 11 on its proposed deployment/scaling baseline.
- Added the integrated architecture review, package/dependency model, 16 end-to-end failure/recovery traces, cross-phase conflict register, architecture classifications, staged implementation roadmap, complete ADR index, acceptance checklist, and ADR-053. Amended ADR-007 and older Phase 1/3 text to align broker timing with the accepted ADR-018/Phase 4 and Phase 11 decisions. No source code, schema, manifests, or runtime behavior changed.
- The blueprint remains owner-approval pending. It recommends persist-first Host registration with asynchronous reachability, Control Manager ownership of durable Operation workers, and optional/deferred JetStream; these are target contracts, not implemented behavior.

### Validation

- `git diff --check` and documentation whitespace checks passed. No tests, architecture automation, load tests, or recovery exercises were run because this update is documentation only.

## 2026-11-11 — Phase 11 deployment, scalability and future evolution proposal

### Changes in this update

- Recorded approval to proceed from Phase 10 on its observability, reliability, and SRE design baseline.
- Added all-in-one, standalone API/Control Manager, and PostgreSQL multi-replica topologies; SQLite/PostgreSQL deployment boundaries; DB-backed lease/fencing requirements; JetStream adoption criteria; future Agent enrollment/heartbeat lifecycle; optional container/Kubernetes guidance; upgrade compatibility; and explicit 100/1,000/10,000 Host planning assumptions with no benchmark claims. Added ADRs 047–052. No code, deployment manifests, migrations, Agents, or coordination mechanisms were implemented.

### Validation

- `git diff --check` and documentation whitespace checks passed. No tests, load tests, benchmarks, or restore exercises were run because this update is documentation only.

## 2026-11-11 — Phase 10 observability, reliability and SRE proposal

### Changes in this update

- Recorded approval to proceed from Phase 9 on its security architecture baseline, including production deny-by-default SSH egress.
- Added observability architecture, current telemetry assessment, metrics catalog, logging/tracing conventions, health/readiness model, proposed SLOs, failure behavior, shutdown/drain sequence, backup/recovery procedures, operational requirements, and ADRs 042–046. No source code, dashboards, alerts, telemetry instruments, or operational procedures were implemented.

### Validation

- `git diff --check` and documentation whitespace checks passed. No tests, benchmarks, restore exercises, or security checks were run because this update is documentation only.

## 2026-11-11 — Phase 9 security architecture review

### Changes in this update

- Recorded approval to proceed from Phase 8 on its proposed API, CLI, and application interaction baseline.
- Added the privileged-control-plane threat model, trust boundaries, authentication and resource authorization requirements, secret/key handling, SSH and egress controls, API and configuration protections, audit model, attack scenarios, risk priorities, and security acceptance criteria. Added ADRs 036–041. No source code, runtime security controls, or tests changed.
- Challenged the earlier private-network-allowed default: production SSH egress should deny by default and require explicit destination and port policy plus independent network enforcement. Existing Phase 6 choices (shared SQLite/PostgreSQL database, global configuration, database-backed encrypted secrets, API/CLI management) remain intact.

### Validation

- `git diff --check` and documentation whitespace checks passed. No tests, penetration tests, or implementation checks were run because this update is documentation only.

## 2026-11-11 — Phase 8 API, CLI and application interaction proposal

### Changes in this update

- Recorded approval to proceed from Phase 7 on the recommended Host connector and execution defaults.
- Added API/runtime boundaries, endpoint inventory, resource and Operation lifecycle semantics, configuration/secret interfaces, validation/error/pagination contracts, authorization, CLI hierarchy and exit codes, interaction diagrams, and four draft ADRs. No HTTP handler, CLI command, or API schema was implemented.

### Validation

- `git diff --check` and whitespace checks of the new design documents passed. No tests or implementation checks were run because this update contains documentation only.

## 2026-11-11 — Phase 7 host access and execution architecture proposal

### Changes in this update

- Recorded approval to proceed from Phase 6 on the confirmed configuration/secrets constraints and recommended defaults.
- Added capability-oriented Host connector boundaries, Go SSH lifecycle and trust model, Secret Store resolution, bounded connections and command execution, network restrictions, Agent integration boundary, failure taxonomy, current implementation assessment, and four ADRs. These defaults were accepted when Phase 8 was authorized. No implementation or package changes were made.

### Validation

- `git diff --check` and whitespace checks of the new design documents passed. No tests or implementation checks were run because this update contains documentation only.

## 2026-11-11 — Phase 6 configuration and secrets management proposal

### Changes in this update

- Recorded approval to proceed from Phase 5 on its recommended persistence defaults.
- Added bootstrap/runtime configuration ownership, revision and snapshot propagation, hot-reload classifications, a database-backed envelope-encrypted Secret Store, API/CLI contracts, failure/security handling, Go package boundaries, and five ADRs. The user-confirmed global scope, shared database, external key, event-plus-poll propagation, and API/CLI constraints are preserved. These constraints and recommended defaults were accepted when Phase 7 was authorized. No source code, runtime config subsystem, or migration changed.

### Validation

- `git diff --check` and whitespace checks of the new design documents passed. No tests or schema validation were run because this update contains documentation only.

## 2026-11-11 — Phase 5 persistence and data architecture proposal

### Changes in this update

- Recorded approval to proceed from Phase 4 on its recommended event defaults, including the transactional outbox, durable consumer deduplication where needed, and deferral of NATS/Kafka until justified. Phase 5’s recommended persistence defaults were accepted when Phase 6 was authorized.
- Added the persistence boundaries, logical schema, transaction and concurrency contracts, migration/startup strategy, SQLite/PostgreSQL comparison, recovery/backup guidance, current-state observations, and four draft ADRs. No source code, package layout, repository implementation, or migration changed.

### Validation

- `git diff --check` passed. No tests or schema validation were run because this update contains documentation only.

## 2026-11-11 — Phase 4 event-driven architecture proposal

### Changes in this update

- Recorded approval to proceed from Phase 3 on its recommended initial topology, resync interval, and bounded Host worker count.
- Added the event taxonomy, envelope/versioning contract, transactional outbox and consumer strategy, delivery guarantees, retry/retention policy, database behavior, and future broker criteria. Added three ADRs, including deferral of NATS adoption. These defaults were approved when Phase 5 was authorized. No code or package layout changed.

### Validation

- Not run: this documentation-only messaging proposal was not checked with automated tooling.

## 2026-11-11 — Phase 3 hybrid control loop proposal

### Changes in this update

- Recorded approval to proceed from Phase 2 on the recommended domain baseline.
- Added a level-triggered, edge-driven controller manager design covering resource-key workqueues, deduplication, bounded workers, retries, recovery, resynchronization, shutdown, replica coordination, failure handling, metrics, and Kubernetes pattern comparison. Added three ADRs. These defaults are approved design assumptions, not implementation. No code or package layout changed.

### Validation

- Not run: this documentation-only controller proposal was not checked with automated tooling.

## 2026-11-11 — Phase 2 domain model proposal

### Changes in this update

- Recorded approval to proceed from Phase 1 on its recommended modular-monolith baseline; the Phase 1 defaults remain the working assumptions for this design.
- Added a domain/resource lifecycle model for Host, Operation, conditions, generations, revisions, concurrency, deletion, and domain events, plus four ADRs. These are approved design decisions, not implementation changes.

### Validation

- Not run: this documentation-only domain proposal was not checked with automated tooling.

## 2026-11-11 — Phase 1 target architecture proposal

### Changes in this update

- Added a proposed target architecture covering component ownership, runtime composition, deployment modes, package boundaries, dependency direction, and a distributed evolution path.
- Added ADRs for modular-monolith boundaries, Control Manager and operation-worker ownership, persistence/messaging, and configuration/secrets. These Phase 1 decisions were accepted when approval to proceed to Phase 2 was given; no implementation or package reorganization was performed.

### Validation

- Not run: this documentation-only architecture proposal was not checked with automated tooling.

## 2026-10-29 — Command execution foundation: structured, pinned, audited runs

### Changes in this update

- New permission `runs:execute` (owners and operators; readers cannot start or cancel runs). Execution is disabled by default: `MACHINARCHY_EXECUTION_ENABLED` and a required `MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS` list (`*` must be explicit; enabling without a list stops startup).
- A command is an argument list (`domain.ValidateArgv`, `QuoteArgv`): the program must be a plain name or path, arguments are bounded and free of control characters, and every element is single-quoted when sent, so the remote shell sees one word per argument. There is no shell string.
- `POST /api/v1/runs` validates policy and shape before touching the inventory, resolves the target rule once (at most 200 hosts), pins it as host IDs, optionally verifies a preview's `targetsHash` (409 with the new hash if the hosts differ), supports `dryRun`, and returns 202 with the run. Previews now include `targetsHash`.
- `GET /api/v1/runs`, `GET /api/v1/runs/{id}`, `POST /api/v1/runs/{id}/cancel`. Arguments and per-host output are returned only to callers with `runs:execute`.
- Migration 6 (`runs`, `run_hosts`); the arguments are stored encrypted with the installation key; run-store methods write `run.created`, `run.cancel_requested`, `run.finished`, and `run.interrupted` audit events transactionally (arguments and output never included).
- `service.RunManager`: background executor with a per-run worker pool, per-host timeouts, SSH capacity waiting, sanitized and truncated output (terminal escapes and control characters removed, 64 KiB per host), exit-code reporting (`sshclient.ExitStatus`), refusal to contact a host whose address or port changed since submission, heartbeats, cancellation through the heartbeat, recovery of abandoned runs at startup and periodically, a limit on concurrently executing runs, and orderly shutdown (runs in flight are interrupted and recorded before the database closes). Commands are never retried.
- Removed the unwired fleet-execution placeholder and its result types.
- Tests: argv validation and quoting (including a POSIX-style round-trip of hostile arguments), execution policy, targets hash, status aggregation, output sanitization; run service behavior with in-memory fakes (policy before inventory, validation, dry run, preview binding, results and attribution, mixed outcomes and timeouts, concurrency bound, cancellation, shutdown, moved or deleted hosts, active-run limit, SSH capacity queueing, redaction, authorization, recovery); real-SSH-server runs (quoting reaches the remote shell intact, exit codes, wrong host key never sees credentials or the command, timeouts, forbidden addresses, twelve hosts); store contract tests on SQLite and PostgreSQL (lifecycle, encryption at rest, listing, recovery, audit contents); handler guards and error mapping; configuration parsing.
- Docs: new command-execution section in the security assessment, ADR 0004, architecture, ROADMAP, usage guide (preview, run, watch, cancel), operations guide, README, and `.env.example`.

### Known limitations

- No CLI `exec` command, scripts, file transfer, approval step, agent execution, submission idempotency keys, or retention and export of output. Output is stored unencrypted. Runs execute in the API process, so a crash interrupts them (the next recovery sweep records it). The program allow list cannot judge arguments; do not allow interpreters or `ssh`.
- Swagger files were not updated for the run endpoints; run `make api-docs`.

### Validation

- Not run: Go and Docker were unavailable. Static checks only. Run `gofmt -w`, `make vet test`, and `make test-postgres`. The most timing-sensitive tests are the cancellation, shutdown, recovery, and real-SSH timeout cases; watch them under `-race`.

## 2026-10-29 — Append-only, hash-chained audit trail

### Changes in this update

- Added migration 5 (`audit_events`) for SQLite and PostgreSQL: one row per audited change with actor (user, username, API token, request ID, entry point), action, target, outcome, a small set of non-secret details, and the previous and own SHA-256 hash. Triggers abort UPDATE and DELETE.
- The stores now write the event in the same transaction as the change: account creation (self-registration is attributed to the new account, bootstrap to `bootstrap`), member role changes (not when the role is unchanged), host create, update, and delete, label patches, and API token create and revoke. Failed or no-op operations write nothing. `DeleteHost`, `CreateAPIToken`, `RevokeAPIToken`, and `UpdateWorkspaceMemberRole` are now transactional so they can read what they record and write it atomically.
- Events of a workspace form a hash chain (length-prefixed fields plus the exact timestamp text). Appends are serialized per workspace (single SQLite connection; transaction-scoped advisory lock in PostgreSQL) so the chain cannot fork. `domain.AuditChainVerifier` walks it and reports the first modified, removed, or reordered event.
- New `AuditActor` middleware attaches the authenticated principal and request ID to the request context after authentication; stores read it with `domain.AuditActorFrom`.
- New owner-only endpoints (new `audit:read` permission; human sessions only, API tokens refused): `GET /api/v1/audit/events` (newest first; `action`, `before_id`, `limit` up to 500) and `GET /api/v1/audit/verify` (valid flag, event count, head hash, first invalid event and reason).
- Tests: hash coverage of every field, unambiguous encoding, detail sanitization, filter normalization, actor context, and the verifier (intact, modified, removed, reordered, forged-with-recomputed-hash, truncated, empty); store contract cases on SQLite and PostgreSQL for the full trail (every audited action, actors, request IDs, details, no secrets in any event, failures leaving nothing, paging and filtering, workspace isolation), append-only enforcement, tamper detection by modification and by removal (triggers disabled to simulate a privileged attacker), and concurrent appends keeping one valid chain; service permission matrix; handler guards and error mapping; actor middleware; bootstrap attribution.
- Docs: new audit sections in the security assessment, architecture, ADR 0003, ROADMAP, and an upgrade note in the operations guide (including recording the head hash outside the database).

### Known limitations

- Denied requests, failed logins, reads, bulk-import summaries, and automatic host status changes are not recorded. No retention, archival, or export. A database administrator can disable the triggers and rewrite the chain; removal of the newest events is only detectable by comparing a head hash recorded elsewhere. New mutating store methods must write an event themselves.
- Swagger files were not updated for the two audit endpoints; run `make api-docs` (the handler annotations are in place).

### Validation

- Not run: Go and Docker were unavailable. Static checks only (identifier resolution, interface and fake signatures, call arity, unused variables and imports, printf formats, missing imports). Run `gofmt -w`, `make vet test`, and `make test-postgres`; the PostgreSQL triggers, advisory lock, and `ALTER TABLE ... DISABLE TRIGGER` in the tamper tests are the most likely places to need adjustment.

## 2026-10-29 — Verification pass (static review without a Go toolchain)

### Problems found and fixed

- Two test fakes (`targetFakeEnvs`, `listFakeEnvs`) implemented `ListEnvironments` with the wrong return type (`[]domain.Environment` instead of `[]*domain.Environment`), which would not compile.
- `internal/transport/http/v1/router.go` had an unsorted import group (gofmt reorders imports, so the CI formatting check would have failed), and `pkg/netpolicy/policy.go` ended with extra blank lines.
- CORS did not allow `PATCH`, so browser clients could not call `PATCH /api/v1/hosts/{id}/labels` (preflight would be refused). Added `PATCH` and a preflight regression test (`middleware/cors_test.go`).
- The embedded OpenAPI spec (`docs/swagger.json`, served at runtime) lacked the members, bulk registration, fleet preview, and label patch endpoints, the `selector` query parameter, and the 409 responses. Added them by hand and regenerated `swagger.yaml` from the same data; every `$ref` resolves. Run `make api-docs` when the Swaggo CLI is available so the generator becomes the source of truth again (the handler annotations are in place).
- Stale documentation: roadmap, architecture, and usage docs still described a private workspace per account. Corrected them, added ADR 0003 (shared workspace, bootstrap owner, owner-managed roles, last-owner protection, pre-flight migrations) and marked the superseded parts of ADR 0002, and added an "Upgrading" section to `docs/operations.md` covering the first-owner setting, the duplicate-host migration check, the SSH target policy, and label rules.

### Checks performed (static only)

- Qualified identifiers resolve to real declarations across all module packages; no duplicate top-level declarations; struct-literal field names exist; store and service interfaces are satisfied by both stores and by every fake, and fakes that embed an interface override its methods with matching signatures; call arity for module functions; every method called on the store interfaces exists; field access through `SSH`/`AuthType` chains; capitalized and lowercase unqualified identifiers resolve; no unused imports or unused variables (scope-aware); import groups sorted; indentation and end-of-file newlines; 923 printf-style calls have matching verb and argument counts and `%w` appears only in `Errorf`; no non-constant format strings; the shell script and workflow YAML parse; the OpenAPI JSON parses and all references resolve; environment variable names agree across code, `.env.example`, and docs. The checkers were validated by injecting known errors into a scratch copy and confirming each was reported.
- Assertions in the integration tests were compared against the store and client implementations (not-found and conflict error types, status guards, token semantics, DSN handling for the PostgreSQL schema isolation, literal status/connection values in raw INSERTs, column widths).

### Not verified

- Nothing was compiled, vetted, formatted, or executed: Go and Docker were unavailable. Heuristic checks cannot find type errors in expressions, missing returns, or gofmt alignment differences (column alignment of struct and map literals and comments); expect `gofmt -w` to touch the new files. Run `make fmt`, then `make vet test` and `make test-postgres` before merging.

## 2026-10-29 — Fleet targeting: labels, selectors, include/exclude rules, and target preview

### Changes in this update

- Labels are now validated: keys of 1-63 characters from letters, digits, `.`, `_`, `-`, `/` (starting and ending alphanumeric), values up to 63 characters (may be empty), at most 50 labels per host, and the built-in field names `id`, `name`, `hostname`, `address`, `status`, `environment`, and `connection` are reserved. Validation applies in `Host.SSHValidate` and at the API boundary for create, update, and each bulk record. Existing stored labels are not rewritten.
- Replaced the old `domain.HostSelector` (which matched every host when empty) with a parsed `domain.Selector`: comma-separated AND requirements `key=value`, `key!=value`, `key in (a,b)`, `key notin (a,b)`, `key`, and `!key`, with `*` and `?` wildcards, case-insensitive built-in fields (`name`, `hostname`, `address`, `status`, `connection`, `environment`, `id`), case-sensitive labels, and caps on length (2048), requirements (20), set size (50), and value length. There are no regular expressions; wildcard matching is non-backtracking. An empty or malformed selector is a parse error.
- Added `domain.TargetRule` and `domain.ResolveTargets`: a selector or an include list or an explicit `all`, minus an exclude list that always wins; entries are host IDs or names; unknown include/exclude entries are reported; results are sorted deterministically. A rule with nothing to select, `all` combined with a selector or include list, oversized lists, and blank entries are rejected.
- Added `POST /api/v1/fleet/targets/preview` (inventory read permission): returns the exact total, up to 500 credential-free host views, the hosts removed by the exclude list, and unknown entries. Nothing is contacted.
- Added `GET /api/v1/hosts?selector=...` filtering (an empty or invalid selector returns 400, never everything).
- Added `PATCH /api/v1/hosts/{id}/labels` (`{"set": {...}, "remove": [...]}`) backed by the new `HostStore.PatchHostLabels`, which merges atomically (a single-connection transaction in SQLite, `SELECT ... FOR UPDATE` in PostgreSQL), leaves credentials and connection settings untouched, enforces the label limit on the merged result, and is workspace-scoped.
- The unwired fleet placeholder now takes a `TargetRule`, resolves it with the shared resolver, and still reports `ErrExecutionNotImplemented`; `NewFleetService` also takes the environment store.
- Tests: selector parsing (accepted and rejected forms), matching semantics, wildcard matching (including a pathological pattern), rule resolution (selector/include/exclude/all, IDs vs names, unknown entries, determinism, never-default-to-all), label validation and patch rules, target service (workspace scoping, authorization, no credentials in output, truncation), fleet and host handler behavior, and store contract cases for label patches (merge, remove, limits, isolation, concurrent patches) on SQLite and PostgreSQL.
- Docs: ROADMAP.md (Phase 4 status), architecture.md, SECURITY.md (new fleet-targeting section).

### Known limitations

- No execution yet; selectors are evaluated at request time, so execution must re-resolve or pin the previewed host IDs. No OR or grouping in selectors. No bulk relabeling by selector. Label changes are not audited. Swagger files were not regenerated; run `make api-docs`.
- Existing databases may hold labels that no longer satisfy the new rules; they keep working for reads and selectors and can be removed with a patch, but a full host update (`PUT`) that resends them is rejected until they are fixed.

### Validation

- Not run: Go was unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `make fmt vet test` and `make test-postgres` before merging.

## 2026-10-28 — SSH integration tests against an in-process server

### Changes in this update

- Added `internal/testutil/sshtest`, a loopback-only SSH server for tests: generated ed25519 host key, password and public-key authentication, `exec` requests with configurable output and exit status, counters (accepted and open connections, credential attempts, execs), and two deliberately broken modes (accepts TCP but never answers; answers with HTTP instead of SSH).
- Added `pkg/sshclient/client_integration_test.go` covering the real client: success with a pinned key; mismatched and missing fingerprints (the server's actual key is reported and, crucially, no credential is ever sent to the unverified host); wrong password, wrong user, and a server that offers no password auth; unencrypted and passphrase-protected key auth, a wrong passphrase failing before any connection, and an untrusted key; connection refused; a hung host honoring the deadline; cancellation during the handshake; a non-SSH service; connection release after 20 sequential and 8 concurrent connections; command output, non-zero exit status, the 1 MiB output cap, no command execution on a host-key mismatch, blank-command rejection, and a deadline stopping a running command.
- Added `internal/service/host_ssh_integration_test.go`: `RegisterHost` and bulk registration against real servers (success stored as active; wrong key, wrong password, refused, and hung hosts stored nothing and were isolated from each other; the default policy never dials loopback).
- Fix found while writing the tests: when the handshake read timed out a moment before the context reported its deadline, `pkg/sshclient` returned an opaque "negotiate SSH connection" error. It now returns `context.DeadlineExceeded` when the context deadline has passed and `domain.ErrConnectionTimeout` otherwise.
- Updated `docs/ROADMAP.md`.

### Known limitations

- The new tests have not been run. Timing-based cases (hung host, deadline during a command) use generous bounds but should be watched for flakiness under `-race` on slow CI runners.
- The server does not implement shells, ptys, port forwarding, or SFTP.

### Validation

- Not run: Go was unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `make fmt vet test` (and `go test -race ./pkg/sshclient/... ./internal/service/...`) before merging.

## 2026-10-28 — Store contract tests for SQLite and PostgreSQL, and CI

### Changes in this update

- Replaced the SQLite-only member and host-uniqueness tests with one shared contract suite (`internal/store/contract_test.go`) that runs every case against each available engine: signup defaults and uniqueness (including accounts without an email), member roles and last-owner protection, concurrent owner demotion, host lifecycle and workspace isolation (including credential decryption round-trip and status guards), host uniqueness and update collisions, environments, API tokens (expiry, revocation, role capping), migration idempotence, and the migration-4 pre-flight.
- SQLite always runs. PostgreSQL runs when `MACHINARCHY_TEST_POSTGRES_URL` is set; each test gets its own schema (via `search_path`) that is dropped afterwards. `MACHINARCHY_REQUIRE_POSTGRES_TESTS=1` turns a missing URL into a failure instead of a skip.
- Added `.github/workflows/ci.yml`: gofmt check, `go build`, `go vet`, and `go test -race ./...` with a PostgreSQL 16 service (PostgreSQL tests required), plus a Docker image build.
- Added `make test-postgres` and `scripts/db/test-postgres.sh` to run the suite against a throwaway Docker container, and documented both in `CONTRIBUTING.md`.
- Updated `docs/ROADMAP.md`, `docs/SECURITY.md`, and `README.md` to say the PostgreSQL suite exists but has not yet been observed passing.

### Known limitations

- Not run: the PostgreSQL half of the suite has never been executed, so it may expose real PostgreSQL bugs or incorrect test assumptions; fix what it reports rather than loosening tests.
- CI does not run `golangci-lint` (no configuration exists in the repository) and does not regenerate Swagger files.
- Email uniqueness is case-insensitive in SQLite but case-sensitive in PostgreSQL; the suite does not assert either, so confirm the service normalizes emails before relying on it.

### Validation

- Not run: Go and Docker were unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `make fmt vet test` and `make test-postgres` (or push and let CI run) before merging.

## 2026-10-28 — Host uniqueness enforced by the database

### Changes in this update

- Added migration `000004_host_uniqueness` for SQLite and PostgreSQL: unique indexes on `(workspace_id, name)` (case-insensitive) and `(workspace_id, ip_address, port)` (case-insensitive address). The previous schema had no uniqueness constraint, so duplicate detection in bulk import was a read-then-write check that concurrent requests could beat.
- Added a migrator pre-flight hook (`internal/store/preflight.go`). Before applying migration 4 it scans for existing conflicts and, if any exist, refuses to start with a message listing each workspace, the conflicting name or address:port, and the host IDs. Nothing is renamed, merged, or deleted automatically; resolve the duplicates and restart. A fresh database or one without duplicates migrates normally.
- Stores now map unique violations on create and update to the new `domain.ErrHostAlreadyExists`. `POST /api/v1/hosts` and `PUT /api/v1/hosts/{id}` return 409 Conflict; bulk registration reports a record that loses a race as `duplicate` (with no database error text) and leaves the others unaffected.
- Tests: database-enforced duplicates (name, address:port, case, per-workspace independence, no partial rows), update collisions and self-update, the pre-flight conflict report and recovery after resolving duplicates, bulk insert-time duplicates, and handler status mapping.
- Updated `docs/ROADMAP.md` and `docs/architecture.md`.

### Upgrade note

- Databases that already contain two hosts with the same name (ignoring ASCII case) or the same address:port in one workspace will fail to start until the duplicates are renamed or deleted. The error lists the host IDs.
- Case-insensitivity is ASCII-only in SQLite; the application-level duplicate check folds Unicode case, so it can be slightly stricter than the SQLite index.

### Known limitations

- The migration pre-flight and PostgreSQL indexes have no PostgreSQL integration test. Host deletion is unaffected.

### Validation

- Not run: Go was unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `make fmt vet test` (ideally with `-race`) before merging.

## 2026-10-28 — Owner-only member role administration

### Changes in this update

- Added `GET /api/v1/members` and `PUT /api/v1/members/{id}/role` (body `{"role": "owner|operator|reader"}`). Both require a human owner session (new `members:manage` permission plus the existing human-session guard, repeated in `service.MemberService`), so API tokens cannot use them. The workspace comes from the authenticated principal.
- `UserStore.UpdateWorkspaceMemberRole` now refuses to demote the last owner with the new `domain.ErrLastOwner`, atomically with the update: one guarded statement in SQLite, and a transaction that locks owner rows in PostgreSQL. Changing a role within a workspace the member does not belong to returns `ErrUserNotFound`.
- Added `UserStore.ListWorkspaceMembers` (bounded to 1000, ordered by user ID) for both engines.
- Role changes are written to the structured log as `audit` events (actor, target, previous and new role); unchanged roles are a no-op.
- Tests: member service permission matrix and behavior, handler error mapping and route guards (operator, reader, API token), SQLite listing, last-owner protection, cross-workspace refusal, and a concurrent-demotion test.
- Docs: SECURITY.md (new member-administration section and corrected workspace statements left stale by the single-workspace change), ROADMAP.md, architecture.md, and README.md.

### Known limitations

- No invitations, member removal, or workspace switching. Audit records are log-only. No confirmation or step-up check when promoting an owner. The PostgreSQL implementation has no integration test. Swagger files were not regenerated; run `make api-docs`.

### Validation

- Not run: Go was unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `make fmt vet test` (ideally with `-race`) before merging.

## 2026-10-28 — SSH target address policy and DNS-rebinding-safe dialing

### Changes in this update

- Added `pkg/netpolicy`: a destination policy (always-denied unspecified/multicast/broadcast/zoned/cloud-metadata addresses; denied-by-default loopback and link-local; configurable private ranges; explicit allow and deny CIDRs) and a `DialContext` that resolves a hostname once, requires every answer to be permitted, and dials the validated IP.
- `pkg/sshclient` now dials through the policy; with no dialer supplied it uses `netpolicy.Default()`, so the policy cannot be skipped. Policy denials surface as `domain.ErrTargetNotAllowed`.
- Host registration (single and bulk) rejects forbidden literal IPs before spending SSH capacity; the host and metrics services accept `service.WithTargetPolicy`, wired from configuration in the API runtime. Single and bulk responses report a fixed, non-revealing denial message.
- New settings: `MACHINARCHY_SSH_ALLOW_PRIVATE` (default true), `MACHINARCHY_SSH_ALLOW_LOOPBACK` (default false), `MACHINARCHY_SSH_ALLOWED_CIDRS`, `MACHINARCHY_SSH_DENIED_CIDRS`.
- Behavior change: loopback and link-local targets are now refused by default. Development setups that register hosts on 127.0.0.1 must set `MACHINARCHY_SSH_ALLOW_LOOPBACK=true`.
- Updated `.env.example`, `docs/SECURITY.md`, `docs/architecture.md`, and `docs/ROADMAP.md`.

### Known limitations

- Not a substitute for network-layer egress controls; no port restrictions; `PUT /api/v1/hosts/{id}` does not re-check the address until the next dial.

### Validation

- Not run: Go was unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `make fmt vet test` (ideally with `-race`) before merging.

## 2026-10-28 — Single shared workspace, bootstrap owner, and hardened bulk host registration

### Changes in this update

- Replaced the per-signup workspace logic with one shared workspace: `CreateUser` (SQLite and PostgreSQL) now inserts the user and a `reader` membership in `domain.DefaultWorkspaceID` and creates no workspace rows. This also fixes the PostgreSQL path that stored the `owner` role while returning `reader`.
- Removed the hardcoded `admin`/default-password seeding and the user-label table/queries. The first owner is now created only from `MACHINARCHY_BOOTSTRAP_ADMIN_*` configuration via `service.BootstrapOwner`, only while the workspace has no owner, with an explicit error if the username already exists as a non-owner. A startup warning is logged when no owner exists.
- Added `UserStore.UpdateWorkspaceMemberRole` and `CountWorkspaceOwners` for both engines. No migration changes: the earlier in-place edit of SQLite migration `000002` was reverted.
- Reworked `POST /api/v1/hosts/bulk`: it now accepts `Payload` records (credentials and fingerprint can be supplied), sets the workspace from the authenticated principal, validates environments per record, supports `validate_only`, detects duplicates (name or address:port, in the inventory and in the batch), uses a bounded worker pool, preserves request order, reports every record, and returns sanitized errors.
- Made the unwired fleet placeholder safe: it now reports `ErrExecutionNotImplemented` instead of success, takes the caller's workspace, and no longer records the command in trace attributes.
- Restored the workspace-isolation and API-token integration test (adapted to a second workspace created directly) and added tests for bulk registration, bootstrap owner, shared-workspace signups, and bootstrap config.
- Updated `.env.example` (bootstrap variables and `MACHINARCHY_BROKER_URL`), `docs/architecture.md`, and `docs/ROADMAP.md`.

### Known limitations

- Existing databases keep their per-account workspaces; only new signups join workspace 1. There is still no API to promote members.
- Duplicate detection is read-then-write; the schema has no uniqueness constraint for hosts.
- `docs/swagger.json` and `docs/swagger.yaml` were not regenerated; run `make api-docs`.

### Validation

- Not run: Go was unavailable in the authoring environment, so nothing was compiled, vetted, formatted, or tested. Run `gofmt`/`make fmt`, `make vet`, `make test`, and `make api-docs` before merging.

## 2026-10-27 — Roadmap: build artifact checksums

### Changes in this update

- Added planned SHA-256 checksum generation after successful Go binary builds, with unambiguous artifact names and consumer verification instructions as acceptance criteria.

### Validation

- `git diff --check` — passed.
- No tests run; this update changes roadmap documentation only.

## 2026-10-27 — Roadmap: shared installation RBAC and future workspaces

### Changes in this update

- Recorded the near-term direction of one shared tenant per installation, with environments for grouping and installation-wide reader, operator/writer, and owner/admin roles.
- Kept the currently shipped private-workspace-per-account behavior clearly marked as current, and added a planned follow-up for trusted account provisioning, role assignment, and an explicit migration that never silently merges existing workspaces.
- Preserved organization workspaces, membership management, and possible environment-level access policies as a later multi-tenant feature.

### Validation

- `git diff --check` — passed.
- No tests run; this update changes roadmap documentation only.

## 2026-10-26 — Git hook and setup script alignment

### Changes in this update

- Updated `scripts/dev/setup-local.sh` to match Machinarchy: require Go 1.27+, create `.env` securely from the repository template, and configure the checked-in hooks. Removed unrelated frontend/assets setup and automatic `go mod tidy`/dependency download behavior.
- Made setup preserve an existing non-default `core.hooksPath` by stopping for review instead of silently replacing it.
- Updated the commit-message hook to validate conventional breaking-change syntax and reject invalid invocation; refreshed the Git-hook documentation to reference current paths and behavior.

### Validation

- `bash -n scripts/dev/setup-local.sh .githooks/pre-commit .githooks/commit-msg` — passed.
- `git diff --check` — passed.
- `shellcheck` — not run; executable is not installed.
- Setup and pre-commit hooks were not executed; setup changes clone-local Git configuration and creates `.env`, while pre-commit runs the repository test suite.

## 2026-10-26 — Workspace and API-token use-case examples

### Changes in this update

- Expanded the use-case and development examples with workspace limitations, owner-managed API-token issuance, reader/operator guidance, token lifecycle and CLI credential use.
- Marked all `Principal` fields as excluded from JSON serialization and added a regression test to prevent accidental disclosure of request identity metadata.
- Reviewed the new authorization and workspace-related structs: API-token hashes and workspace ownership fields are excluded from JSON; issued tokens expose plaintext only in the one-time create response. No other serialization changes were needed.

### Validation

- `gofmt` — run on changed Go files.
- Focused `go test` for domain, middleware, API-token service, token, host, and environment packages — passed.
- `git diff --check` — passed.
- Full repository tests, benchmarks, race detection, and lint — not run.

## 2026-10-26 — Workspace RBAC and scoped API tokens

### Changes in this update

- Added workspace ownership and owner/operator/reader permission contracts. JWT requests reload current membership; API-token requests resolve a workspace-bound identity whose effective role cannot exceed its current membership.
- Added migration 000003 for SQLite and PostgreSQL. Legacy hosts, environments, jobs, and metrics history move to workspace 1; the earliest existing account owns it, and later accounts receive empty workspaces. Environment-name uniqueness is now workspace-local.
- Scoped host, environment, job, and metrics-history storage and API paths. Host-to-environment and job-to-host relationships are checked within the caller's workspace.
- Added API token create/list/revoke endpoints. Tokens are high-entropy bearer secrets, returned once, stored as SHA-256 hashes, default to 30 days, cap at 365 days, and are checked for membership, expiry, and revocation on each request. Token administration requires a human owner session.
- Updated the roadmap, security and architecture documentation, README, ADR 0002, and generated Swagger/Scalar contract with role semantics, token usage, legacy migration behavior, and remaining member-management limitations.
- Added role tests, middleware permission/bearer tests, and SQLite fresh-install plus legacy-upgrade isolation tests.

### Validation

- `gofmt` — run on changed Go files.
- `make api-docs` — passed.
- `go test ./...` — passed, including SQLite migration and workspace-isolation tests.
- `go vet ./...` — passed.
- `git diff --check` — passed after the final documentation update.
- PostgreSQL integration tests, benchmarks, race detection, and `golangci-lint` — not run. No PostgreSQL test server was configured.

## 2026-10-26 — Security baseline and production signup guard

### Changes in this update

- Added the authoritative product roadmap, a threat assessment, and an architecture decision record based on the inspected repository and official Salt, Ansible, and Terraform references.
- Added `MACHINARCHY_SIGNUP_ENABLED`; it defaults on in development and off in production. The signup handler returns HTTP 403 when disabled. Documented that this limits account creation but does not provide resource-level authorization.
- Regenerated the OpenAPI contract to include the signup-disabled response.
- Replaced the obsolete history handler test that imported a removed metrics package with table-driven tests using the current metrics service boundary, unblocking repository-wide tests.
- Recorded the next security milestone: workspace membership and authorization enforced across inventory, job, and history paths, with a deterministic migration plan for existing rows.

### Validation

- `gofmt` — run on changed Go files.
- `make api-docs` — passed.
- `go test ./...` — passed.
- `go vet ./...` — passed.
- `git diff --check` — passed after the final documentation update.
- `golangci-lint` — not run; executable is not installed.
- No benchmark, database integration, or end-to-end authorization/security test was run.

## 2026-10-26 — Initialize project change log

### Repository baseline at `f3bb16e`

The initial tracked project snapshot includes:

- Go API, control-manager, combined monolith, and Machinarchy CLI entry points.
- Echo HTTP API, authentication, environment and host management, SSH host registration with expected host-key verification, and SQLite/PostgreSQL storage adapters.
- NATS JetStream event plumbing, health and metrics endpoints, OpenTelemetry setup, and structured server logging.
- CLI context configuration and host listing. The CLI does not currently register hosts or submit arbitrary commands.
- Job read endpoints and an agent placeholder; there is no public job-submission or arbitrary command-execution endpoint.
- README, operations and architecture notes, CLI guides, use-case examples, and the Scalar-rendered OpenAPI contract.

This section records the existing baseline; it does not claim these capabilities were added by the changelog update.

### Changes in this update

- Added `ChangeLogAI.md` and this initial baseline entry.
- Added repository instructions and a contributor workflow requirement to update this log with every future project change.

### Validation

- `git diff --check` — passed.
- No tests or build checks were run; this update changes documentation and repository instructions only.
