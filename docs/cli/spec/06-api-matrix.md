# 06 - API matrix

> Every endpoint below was read from `docs/api/swagger.json` (the generated contract, 65 operations) and checked against the route registration and handlers in `internal/transport/http/v1/**` and the middleware in `internal/transport/http/middleware/auth.go`. Nothing was called against a running server. **"Today" means present in that code at commit `2edd1c5`.** Anything not in the contract is listed under controller work, never assumed.

## 1. CLI command to API operation

Roles: **any** = reader, operator or owner; **runs** = may execute runs (operator, owner); **owner** = workspace owner with a human session (an API token can never be an owner: `middleware.RequireOwner`, `RequireHuman`).

| CLI command | API operation | Method and path | Role | Today | Missing controller work |
|---|---|---|---|---|---|
| `login` (password) | log in | `POST /api/auth/login` | none | yes (body `email` or `username`, `password`; returns `token`, `user`) | - (expiry comes from `/sessions`) |
| `login --with-token`, `whoami` | current user | `GET /api/v1/me` | any (session or API token) | yes; returns `id`, `username`, `email` and, since Phase 10.3 (C-03), `role`, `authMethod`, token `tokenId`, `tokenName`, `expiresAt` |
| `login`, `whoami`, home | my sessions | `GET /api/v1/sessions` | session only | yes (`id`, `current`, `createdAt`, `expiresAt`, `clientIp`, `userAgent`) | - |
| `login` ("stay signed in") | create API token | `POST /api/v1/tokens` | owner session | yes (`name`, `role` operator or reader, `expiresInHours`; token shown once) | - |
| `logout` | log out | `POST /api/auth/logout` | none (revokes the session whose token is sent) | yes | - |
| `logout --everywhere` | log out everywhere | `DELETE /api/v1/sessions` | session only | yes (API tokens unaffected) | - |
| `tokens` | list tokens | `GET /api/v1/tokens` | owner session | yes | - |
| `tokens create` | create token | `POST /api/v1/tokens` | owner session | yes | - |
| `tokens revoke` | revoke token | `DELETE /api/v1/tokens/{id}` | owner session | yes (by numeric id; the CLI finds it by name in the list) | - |
| `envs` | list environments | `GET /api/v1/environments` | any (`inventory:read`) | yes (`id`, `name`, `description`, `createdAt`) | - |
| `envs add` | create environment | `POST /api/v1/environments` | operator, owner (`inventory:write`) | yes (`name`, `description`) | C-02 for `--protected` |
| `envs remove` | delete environment | `DELETE /api/v1/environments/{id}` | operator, owner | yes; behaviour when hosts exist **not verified** | - |
| `envs use`, `switch` | - | local | - | n/a | - |
| `hosts` | list hosts | `GET /api/v1/managed/hosts` (`selector`, `environment`, `phase`, `cursor`, `limit` <= 500) | any | yes | - |
| `hosts show` | get host | `GET /api/v1/managed/hosts/{id}` | any | yes (metadata, spec, status, conditions, `observedHostKeys`) | - |
| `hosts show` (recent runs) | list operations by host | `GET /api/v1/managed/operations?host=ID` | any | yes | - |
| `hosts add` (SSH) | create host | `POST /api/v1/managed/hosts` (`name`, `labels`, `spec.presence`, `spec.accessProfileId`, `spec.accessTarget.endpoint`, `.trust`) | **owner** when binding an access profile (`service/managed_resources.go:50-53`) | yes | C-01 done: optional `environmentId`, default env when absent |
| `hosts add` (credential) | create secret | `POST /api/v1/secrets` | owner session | yes (`purpose` is fixed to `ssh-credential` by the CLI) | - |
| `hosts add`, `access add` | create access profile | `POST /api/v1/managed/access-profiles` | owner | yes | - |
| `hosts add` (wait) | get host (poll) | `GET /api/v1/managed/hosts/{id}` | any | yes | - |
| `hosts tag` | set labels | `PATCH /api/v1/managed/hosts/{id}/labels` (`revision`, `labels`) | per service rules | yes | - |
| `hosts move` | replace host spec/metadata | `PUT /api/v1/managed/hosts/{id}` | owner for profile/endpoint/trust changes | C-01 done: optional `environmentId` (absent = unchanged; unknown = 422); a move is a second revision | none |
| `hosts trust` | trust host keys | `POST /api/v1/managed/hosts/{id}/trust` (`generation`, `fingerprints`, `replace`) | owner | yes | - |
| `hosts add --agent`, `hosts enroll` | issue enrolment token | `POST /api/v1/agents/enrollment-tokens` (`hostId`, `hostname`, `ttlSeconds`) | owner session | yes (token shown once) | C-07 for the install command |
| `hosts enroll --revoke-old` | list / revoke enrolment tokens | `GET /api/v1/agents/enrollment-tokens`, `DELETE /api/v1/agents/enrollment-tokens/{id}` | owner session | yes | - |
| `hosts add --agent`, `hosts enroll` | controller public key | `GET /api/v1/agents/controller-key` | any (`inventory:read`) | yes (`publicKey`, `publicKeys`, `nextPublicKey`; 503 when no gateway) | - |
| `hosts revoke-agent` | revoke agent | `POST /api/v1/agents/{id}/revoke` | owner session | yes | C-09 (map host to agent id) |
| `hosts revoke-agent --agent-id` | list agents | `GET /api/v1/agents` | any | yes, **response shape not in the contract** | C-09 |
| `hosts remove` | delete host | `DELETE /api/v1/managed/hosts/{id}?revision=N` | per service rules | yes; 409 while a run targets it | - |
| `access` | list profiles | `GET /api/v1/managed/access-profiles` | any | yes | - |
| `access show` | get profile | `GET /api/v1/managed/access-profiles/{id}` | any | yes | - |
| `access remove` | delete profile | `DELETE /api/v1/managed/access-profiles/{id}` | owner | yes (refused while hosts use it) | - |
| `run` (preview) | preview targets | `POST /api/v1/managed/operations/preview` (`targets`) | runs | yes (returns hosts, excluded, unknown includes/excludes, `targetsHash`) | - |
| `run -- PROG` | submit operation | `POST /api/v1/managed/operations` (`targets`, `expectTargetsHash`, `run{argv,timeoutSeconds,concurrency,detach}`) | runs | yes; max 200 targets, concurrency 1-20 | C-05, C-12 |
| `run --do CAP` | submit operation | same, with `capability{name,input,timeoutSeconds}` | runs | yes | C-08 (catalog) |
| `run` (list) | list operations | `GET /api/v1/managed/operations` (`state`, `host`, `createdAfter`, `createdBefore`, `cursor`, `limit`) | any (`operations:read`) | yes (summaries: `id`, `kind`, `state`, `targets` counts, times; no argv or output) | C-10 |
| `run ID` | get operation | `GET /api/v1/managed/operations/{id}` | any; readers do not see argv/output | yes (all targets inline) | C-06 for large runs |
| `run ID --host` | target output | `GET /api/v1/managed/operations/{id}/targets/{hostId}/output` (`stream`, `from`) | runs | yes (live while detached; first/last 15 KiB kept) | - |
| `run ID --cancel` | cancel | `POST /api/v1/managed/operations/{id}/cancel` | runs | yes (records a request; terminal state comes later) | - |
| `run ID --retry-failed` | submit operation | as submit, with `include` = failed host names | runs | yes | C-12 |
| `admin settings *` | runtime config | `GET/PUT /api/v1/runtime-config`, `GET .../revisions`, `GET .../status`, `POST .../validate`, `POST .../rollback` | owner session | yes (unchanged from today's CLI) | - |
| `admin secrets *` | secrets | `GET/POST /api/v1/secrets`, `GET .../{id}/versions`, `POST .../{id}/rotate`, `POST .../rewrap`, `DELETE .../{id}` | owner session | yes (unchanged) | - |
| home screen | counts and recents | `GET /api/v1/me`, `GET /api/v1/managed/hosts`, `GET /api/v1/managed/operations?limit=5` | any | yes | - |
| completion | as in `02-commands.md` 3.13.1 | read-only GETs above | per command | yes | - |
| readiness check | host health | `GET /healthz/ready` (public, no auth) | none | yes; **not used by the CLI** (it reports the process, not the API contract) | C-11 for the version |

### Endpoints in the contract the CLI does not use in V1
`POST /api/auth/signup`, `GET/PUT /api/v1/members*`, `GET /api/v1/audit/*`, `/api/v1/agents/signing-keys*`, `GET /api/v1/runtime-config/revisions/{revision}`, `GET /api/v1/secrets/{id}`, `POST /api/v1/me/password`, `DELETE /api/v1/sessions/{id}`, `PUT /api/v1/managed/access-profiles/{id}`, `PUT /api/v1/environments/{id}`, `/metrics`, `/healthz*`. They are reachable through other clients; adding them is a later decision.

### Facts the CLI relies on, and where they were verified

| Fact | Source |
|---|---|
| `GET /api/v1/me` accepts both a session token and an API token (`mch_pat_...`) | `middleware/auth.go:32-36,64-80`; `auth/handler.go` `GetMe` reads `username` set for both |
| `/me` returns only id, username, email | swagger `domain.UserResponse` |
| API tokens cannot create tokens, revoke tokens, manage sessions or be owner | `tokens/routes.go:13` (`RequireHuman`), `agents/routes.go:16`, `auth` handler errors |
| Token roles are only `operator` and `reader`; default 30 d, max 365 d | swagger `tokens.CreateRequest`; `docs/operations.md` |
| Session expiry is reported by `GET /api/v1/sessions` | swagger `domain.Session.expiresAt` |
| A host cannot be given an environment through the API | `managedresource/handler.go:28-37` (`hostRequest`, `hostUpdateRequest` have no environment); `service/managed_resources.go:57` passes 0 |
| No `protected` attribute exists on environments | swagger `domain.Environment` has `id`, `name`, `description`, `createdAt` only |
| An operation holds at most 200 targets; concurrency 1-20 | `domain/resource/operation.go:68,294` |
| No idempotency key is accepted on submit | no `Idempotency-Key` in swagger or handlers (ADR-033 mentions the idea) |
| Host-key policies `tofu`, `approve`, `pinned`, `ca`; `tofu` only with key auth | `domain/resource/hostkeys.go`, `docs/operations.md` |
| Host conditions `Ready`, `Reachable`, `Configured`; reasons seen in code: `NotObserved`, `ConnectionFailed`, `TargetNotAllowed`, `AccessNotVerified`, `ConnectorUnavailable`, `HostKeyPendingApproval` | `domain/resource/host.go:116-118`, `hostaccess`, `commands/trust.go:51`. `HostKeyChanged` appears in `docs/operations.md`. **The complete reason list is not documented in the contract** (C-15). |
| Cancel records a request and the final state arrives later; remote work is not asserted stopped | `ADR-033`, `ADR-030`, `docs/operations.md` (cancel, `unreachable_during_cancel`) |

## 2. Controller work required

Separate from CLI work. "Blocks" lists the CLI features that must refuse or degrade until the change exists. Priority: **M** = needed for the milestone named; **S** = should have; **N** = nice to have.

| ID | Change | Why (CLI need) | Blocks until done | Fallback the CLI uses | Pri |
|---|---|---|---|---|---|
| C-01 | Accept `environmentId` on host create and on host update (`hostRequest`, `hostUpdateRequest`); the domain method `UpdateMetadata` already takes an environment | `hosts add --env`, `hosts move`, the "which env?" question | env assignment | The questions are skipped and `hosts move` refuses with "this server cannot move hosts between envs yet" | M3 |
| C-02 | `protected` on environments; mutations of hosts in a protected env (run submit, host update/delete/trust/move) require `confirmEnvironment` equal to the env name, else HTTP 409 `protected_environment_confirmation_required` | server-held protected envs (`04-security.md` 6.3) | server-side protection | Local guard, labelled `protected (local)` | M3. **Implemented 10.2** (`confirmEnvironment` string or list; 409 message lists the env names) |
| C-03 | `GET /api/v1/me` also returns `role`, `authMethod` (`session`, `api_token`), and for tokens `tokenName`, `tokenId`, `expiresAt` | decision 5 ("show user, role and expiry") for token logins | role and expiry for tokens | CLI prints "not reported by this server" | M2. **Implemented 10.3** |
| C-04 | Login response includes `expiresAt` | saves one request; optional because `/sessions` has it | - | `GET /api/v1/sessions` | N |
| C-05 | **Decided (10.5): final at 200** (`MaxOperationTargets`); a raise needs C-06 and a load lab | runs on more than 200 hosts | >200-host runs | Refuse with advice | S |
| C-06 | **Not needed while C-05 stays 200.** `GET /api/v1/managed/operations/{id}/summary` (state counts, per-state host lists as ids, groups by result hash) and `GET .../targets?state=&cursor=&limit=`; the operation document stops embedding every target for large runs | 500 and 5,000 host runs; polling cost | large runs (needs C-05) | The CLI downloads the whole operation (<= 200 targets) | S |
| C-07 | `GET /api/v1/agents/install` returning **structured** data: `agentVersion`, `downloadUrl`, `checksumSha256`, `controllerPublicKeys`. **No pre-built shell command, no broker field** (ADR-059 supersedes Q1: agents use the gateway with server URL + enrolment token) | `hosts add --agent` printing an install command | agent path of the five-minute test | "Four values" fallback (`03-ux.md` 2.2) | **M3 blocking for the agent path** |
| C-08 | `GET /api/v1/agents/capabilities` (names, one-line descriptions, input fields and types) | `--do` help, completion, early validation | discoverability | static list of the four known capabilities | N |
| C-09 | Document the `GET /api/v1/agents` response (agent id, bound host id, state, last seen) in swagger | `hosts revoke-agent NAME` without `--agent-id` | name-based revoke | `--agent-id` | S |
| C-10 | Short, unique, typeable run IDs (`shortId`) or `?idPrefix=` lookup | `r-4k2p` style IDs | typeable IDs | first 8 characters of the ID, unique prefix resolved in the 500 most recent runs | S |
| C-11 | **Done (10.4):** `GET /api/v1/version`, public, `{"version","apiMin","apiMax"}`; no header | "incompatible version" handling, support | version message | decode failure message | S |
| C-12 | `Idempotency-Key` on `POST /api/v1/managed/operations` (ADR-033 already specifies it) | safe handling of "did the submit go through?" | exact answer after a dropped connection | the user is told to check `machinarchy run` | S |
| C-13 | Connectivity probe for a registered host (`POST .../hosts/{id}/probe`) | "test connectivity" before trusting | an explicit test step | the wizard waits for the controller's own observation | N |
| C-14 | Validate host names (letters, digits, `.`, `_`, `-`, 1-255) like labels | defence in depth for T8 and T16 | - | the CLI sanitises everything | S |
| C-15 | Document the host condition reasons and phases as an enumeration in swagger | stable readiness mapping | complete mapping | the mapping in `02-commands.md` 3.8.1, unknown reasons -> `needs attention` | S |

### Controller work **not** required by this spec
Anything not listed. In particular the CLI does not need server-side batching, a new auth scheme, a protocol change, or a CLI-specific endpoint other than C-07's structured install information.

## 3. CLI work that needs no controller change

Everything in `10-roadmap.md` M1, M2 (except C-03's display), M4 up to 200 hosts, M5 and M6, plus the SSH half of M3 without environment assignment.
