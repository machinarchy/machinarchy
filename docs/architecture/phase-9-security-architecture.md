# Phase 9 — Security Architecture

**Status:** Accepted as the working design baseline when Phase 10 was authorized
**System classification:** Privileged infrastructure-management control plane. A compromise can expose credentials, alter configuration, and execute work on managed Hosts. This review is an architecture assessment, not a penetration test or certification.

## 1. Executive assessment

Machinarchy already has useful controls: explicit host-key fingerprint checks, context-bounded Go SSH, DNS resolution followed by direct dialing to a validated IP, API token hashing, workspace-scoped query patterns, role middleware, production-disabled signup by default, command argv validation/quoting, SSH bulkheads, bounded output, and append-only/hash-chained audit rows for several mutations.

Those controls do not yet form a complete security architecture. The most important gaps are: private network destinations are allowed by default; the current `operator` role combines inventory write and remote execution; authentication has no MFA/step-up and login throttling is only a per-process IP-wide limiter; cookie-authenticated unsafe methods lack explicit CSRF/Origin protection; Operations output is plaintext and argv/output permissions are bundled; API/Control Manager share broadly privileged storage/broker access; NATS uses no required authenticated TLS configuration in application bootstrap; metrics/docs/health endpoints are unauthenticated; and audit history is not anchored outside the database. Secret encryption currently depends on an installation secret with no rotation protocol and a development fallback to the JWT-signing token.

The strongest architectural correction is to treat host egress, command execution, secret resolution, configuration changes, and message consumption as separate privileged capabilities. User authentication alone must not authorize resource access, secret resolution, or remote execution.

## 2. Trust boundaries

```mermaid
flowchart LR
  U[Users / automation] -->|TLS: bearer or session cookie| EDGE[Ingress / trusted proxy]
  EDGE --> API[API process]
  API --> AUTH[Authentication]
  AUTH --> AUTHZ[Workspace + resource authorization]
  API --> APP[Application commands and queries]
  APP --> DB[(SQLite / PostgreSQL)]
  APP --> OB[(Outbox / durable events)]
  APP --> SEC[Secret resolver capability]
  KEY[External root key / KEK provider] --> SEC
  SEC --> DB
  CM[Control Manager process] --> DB
  CM --> OB
  CM -->|purpose-bound secret resolution| SEC
  CM --> NET[Destination policy + egress firewall]
  NET -->|pinned SSH host key, bounded SSH| RH[Untrusted remote Hosts]
  CM -. future mTLS/task protocol .-> AG[Future Agent]
  DB -. current event adapter .-> NATS[Current NATS JetStream]
  NATS -. notification only in target design .-> CM
  OBS[Logs / metrics / traces / audit export] <-- API
  OBS <-- CM
```

Trust boundaries and required controls:

1. **Client to ingress:** authenticate transport, enforce TLS, request-size/rate limits, and strip untrusted proxy headers. Only explicitly trusted proxies may assert client address or TLS identity.
2. **API transport to application:** request DTOs are untrusted; enforce function, object, and property authorization before commands/queries. Never trust client-provided workspace, actor, permission, Host identity, or target expansion.
3. **Application to persistence:** database rows are authoritative state but not automatically trustworthy after restore/tampering. Use scoped queries, constraints, revision checks, encryption, and auditable transactions.
4. **Application to Secret Store:** plaintext secret resolution is a distinct privileged transition; authorize caller workload, purpose, secret reference, version, and owning resource. Plaintext must not escape into API DTOs, events, errors, or observability.
5. **Control Manager to message transport:** messages are untrusted hints until authenticated/validated and reconciled against durable state. Deduplicate and re-read authoritative state; do not execute directly from payloads.
6. **Control Manager to remote Host:** outbound SSH is privileged egress. Enforce destination and port allowlists, DNS rebinding resistance, host-key pinning, credential least privilege, concurrency, and command policy on every attempt.
7. **Administrator/deployment to bootstrap:** environment, key files, database credentials, TLS roots, egress rules, and broker credentials form a separate root-of-trust boundary. Protect and rotate them independently.
8. **Process to observability:** logs/metrics/traces are separate data stores with their own access boundary. Treat topology, identities, operation metadata, and error details as sensitive.

## 3. Threat model

| Threat actor / asset | Attack surface and likely attack | Required control | Residual risk |
|---|---|---|---|
| API user / stolen user session | Guess another Host/Operation ID; change protected fields; use broad target rules; invoke secret/config/admin routes | Function, object, and property authorization on every request and application use case; workspace from principal; strict DTO allowlists; `If-Match`; audit | Current roles are coarse; one shared workspace means operators/readers are not separated by team or resource |
| Automation/API token holder | Steal long-lived token; use operator token for remote execution; exploit inherited role | Short lifetimes, one-time token display, hash storage, immediate revoke, per-token scope, no owner-equivalent tokens, separate execution grants | Current operator token can execute Runs; no service-account lifecycle or token-level permission subset |
| Administrator / deployment operator | Accidental/malicious config widens SSH egress, enables execution, disables signup, replaces key or audit path | Split duties, step-up MFA, staged change review for high-risk settings, immutable/external audit anchor, protected bootstrap/key access | A deployment administrator with process and key access can usually control the system; operational separation is required |
| Remote Host / SSH server | Malicious DNS answers, fake SSH key, protocol/parser exploit, huge output, crafted terminal escapes, remote process survives cancel | Per-dial address validation, pinned key, patched Go SSH library, time/byte limits, output sanitization, no automatic retry | A trusted but compromised Host can return false data and receive authorized commands |
| SSH credential | DB dump, backup leak, debug dump, API echo, argv/log/trace leakage, reused credentials | Envelope encryption with external KEK, separate secret authorization, no plaintext read API, credential rotation, least-privileged remote accounts | Compromise of a running authorized worker can access secrets it is permitted to resolve |
| Control Manager process | RCE or stolen workload identity gains DB access, secret resolution and fleet egress | Hardened isolated runtime; least-privilege DB role; no public API routes; bounded worker and egress policy; signed/pinned software; short-lived credentials | Control Manager is inherently highly privileged and can affect all Hosts covered by its service account |
| Database operator / attacker | Read/alter desired state, ciphertext, outbox, audit history; forge work or remove newest audit events | Network isolation, DB TLS/auth, least-privilege roles, constraints, encrypted secret columns, external audit head, backups/restore checks | A DB superuser can change rows and schema; at-rest encryption does not prevent authorized DB access to other data |
| Configuration Store writer | Enable command execution, expand destinations, weaken worker limits, add attacker-controlled SecretRef | Owner-only config writes, step-up/approval for security-sensitive fields, immutable revisions, two-person rule for widening egress/enabling exec, per-process apply status | A malicious authorized owner can still change policy without independent review if no second approver is required |
| Secret Store caller | Confused deputy resolves another Host’s secret or requests a different purpose/version | Secret reference ACL bound to Host/profile and caller identity; purpose-bound internal API; explicit version; no generic HTTP resolver | Application bugs in access binding can expose decrypted values even if DB ciphertext is sound |
| Message producer/consumer | Forge/replay/drop event; inject subject/payload; exploit poison messages or broad subscription ACL | Authenticated TLS broker access, subject ACLs, signed/validated envelope where broker trust is insufficient, inbox dedupe, bounded payloads, outbox and DB re-read | A compromised authorized producer can publish malicious but valid-looking events; workers must re-authorize state transitions |
| Future Agent | Stolen enrollment token/cert, agent impersonation, replay or Host-ID spoofing, malicious observations/results | Unique short-lived mTLS identity per Agent, one-time enrollment, cert rotation/revocation, identity-to-Host binding, sequence/expiry, capability authorization | A compromised Agent can lie about its own state and perform commands granted to that identity |
| Remote command caller | Abuse allowlisted interpreter, leak secrets through argv/output, exploit shell quoting mismatch, replay side effects | Structured argv only, narrow program policy, isolated low-privilege accounts, target pinning, explicit execute permission, bounded output, no retries | An allowed binary can still be dangerous; shell and remote OS semantics differ; SSH cannot guarantee remote cancellation |

## 4. Authentication architecture

Authentication proves the caller identity only. It does not grant resource access or secret access.

### Current mechanisms

- Human passwords are bcrypt-hashed; login issues an HS256 JWT with issuer, subject, issue/not-before/expiry claims and a 24-hour lifetime. Middleware reloads the caller’s current workspace role on each request.
- Browser sessions are returned in JSON and set an `HttpOnly`, `SameSite=Lax` cookie; `Secure` is tied to production mode. API middleware accepts either bearer JWT or cookie on protected routes.
- API tokens are 256-bit random values displayed once, stored as SHA-256 hashes, expire by default after 30 days (maximum 365), and are resolved against expiry/revocation on each request. Tokens cannot be created as owner role and are bearer-only.
- `MACHINARCHY_OPERATOR_TOKEN` is the HS256 signing key. Configuration enforces 32 characters and recommends random independent material, but length alone does not prove entropy; key IDs/rotation are not implemented.

### Target requirements

- Require TLS 1.2+ at the public ingress; prefer TLS 1.3 where supported. HSTS belongs at the public TLS terminator. Internal TLS links protect API-to-database, API/Control Manager-to-broker, and future Agent traffic.
- Replace manually memorable signing secrets with generated high-entropy key material from a protected key provider. Introduce key IDs, overlapping verification during rotation, bounded token lifetimes, and emergency revocation. Consider asymmetric signing if API and worker components should verify without having token-minting authority.
- Add MFA or WebAuthn for owner/admin sessions and step-up reauthentication for secret lifecycle, member/role changes, runtime security configuration, token issuance, host-key trust changes, and command policy changes.
- Keep account enumeration-resistant login errors. Add bounded throttling by both source and account identifier for login/signup/token creation, with alerting and a lockout design that avoids attacker-triggered permanent account denial.
- Session logout must invalidate/revoke the server-side session or use a documented short-lived-token compromise window. Current cookie clearing alone does not revoke a copied signed JWT before expiry.
- API tokens remain machine credentials: high entropy, scoped permissions, expiration, owner, last-used metadata, revocation, rotation, and audit. Never accept them in cookies, query parameters, URLs, or CLI positional args.
- **CSRF:** because protected routes accept browser cookies and CORS allows credentials, require strict `Origin`/`Referer` validation and a CSRF token or remove cookie authentication from unsafe API methods. `SameSite=Lax` is defense in depth, not the authorization model. Bearer-only CLI requests remain unaffected.
- **Future Agent identity:** authenticate each Agent with unique mTLS certificate or equivalent workload identity, bind certificate identity to one Host ID and tenant, use short-lived/rotatable certificates, revocation and replay protection, and authorize each capability independently. Enrollment must be one-time or operator-approved; no fleet-wide shared secret.

### Mutual TLS evaluation

Adopt mTLS for future Agent-to-control-plane traffic and consider it for API-to-Control Manager/broker/database service identities where the deployment can operate a CA and rotation workflow. mTLS is machine authentication and channel protection; it does not replace application authorization, tenant checks, secret authorization, or audit. Certificate identity must map to a stable process/Agent principal and be checked for revocation/expiry. RFC 8705 describes certificate-based client authentication and certificate-bound tokens as distinct controls, reinforcing that certificate proof is not the full authorization decision. [RFC 8705](https://www.rfc-editor.org/rfc/rfc8705.html)

## 5. Authorization and RBAC

### Mandatory authorization layers

1. **Function-level:** may this principal invoke host write, operation execute, configuration write, secret rotate, audit verify, or member administration?
2. **Object-level:** does this principal have access to this Host, Operation, Environment, Configuration revision, Secret, or member inside the authorized workspace/resource relationship?
3. **Property-level:** may this caller read or mutate this field? Secret references, credentials, argv, output, trust pins, and policy fields require separate checks.
4. **Workload-level:** may this API/Control Manager/Agent process resolve a particular secret or perform a particular connector capability?

Never rely on unguessable IDs, hidden CLI commands, or route middleware alone. Derive tenant/workspace from the authenticated principal or bound workload identity; do not accept it from the request body. Every persistence query and mutation must include the relevant workspace/resource constraints. This is consistent with OWASP’s broken object-level authorization guidance: every operation using a client-supplied object ID needs an object-level check. [OWASP API1:2023](https://api-security.owasp.org/editions/2023/en/0xa1-broken-object-level-authorization/)

### Target role model

| Principal / capability | Recommended permissions |
|---|---|
| Reader | Inventory read; Operation metadata read; no raw output by default; no mutation, secret, or configuration access |
| Operator | Inventory mutation only when granted; Operation submit/read/cancel only when separately granted; no secret management, config security changes, member/token administration |
| Owner | Workspace administration and ordinary resource management; sensitive security changes still require MFA/step-up and audit |
| Automation identity | Explicit per-token scopes and resource/operation kind constraints; no owner-equivalent token; default read-only |
| Control Manager workload | Reconcile allowed Host/resource kinds, claim Operations, resolve only referenced secrets for approved connector purpose, write observed/Operation status; no member/token/config-management access |
| Agent identity | Report/perform only against its bound Host and negotiated capabilities; cannot list fleet, resolve arbitrary secrets, or change desired state |

The current model gives every `operator` both `inventory:write` and `runs:execute`. For privileged fleet software, split those grants and allow resource/environment/pool restrictions before production execution is enabled. Current installation puts all accounts in one shared workspace; workspace membership therefore does not provide team-level isolation. Environment is organizational metadata, not a security boundary.

### Secret authorization

Secret metadata management, secret plaintext resolution, and secret reference assignment are three separate permissions. Proposed enforcement:

- Humans can create/rotate/delete through owner-only, step-up authenticated API actions; normal read APIs return metadata/references only, never plaintext.
- A runtime worker resolves a version only after proving workload identity, caller use case, resource ownership, secret ID/version, and allowed purpose. Scope references to access profile/Host/operation as applicable.
- Secret assignment to a Host/access profile is separately authorized and audited. Secret name/reference alone is not permission to resolve.
- Do not expose a generic `/resolve` HTTP endpoint or pass decrypted `[]byte` into domain, transport, event, logger, metrics, trace, panic, or error interfaces.
- Track resolution metadata (identity, purpose, secret/version, resource, outcome) in a protected audit stream, with safe sampling/aggregation only for high-volume machine paths; never record value or ciphertext.

## 6. Secrets, encryption, and key lifecycle

### Encryption model

- Use the Phase 6 envelope design: a random DEK for each immutable secret version; AEAD encryption of the value; KEK wrapping of each DEK; authenticated associated data binds secret ID, version, purpose, algorithm, and schema.
- Keep all KEKs/root keys external to SQLite/PostgreSQL. Provision through a protected file/secret provider, never source control, database rows, command-line arguments, or the same value used to sign JWTs. Production and development must not silently fall back to a weaker/different key.
- Keep secret ciphertext, wrapped DEKs, nonce, algorithm/key IDs in the database. Enforce nonce uniqueness/limits per key. Never expose encrypted payload fields through normal query APIs.
- At-rest protection layers are independent: application-level AEAD for secrets; OS/disk encryption and restrictive filesystem/DB roles for DB files; encrypted backups and WAL/snapshot handling; TLS in transit. Current SQLite permissions and PostgreSQL TLS protect access paths but do not encrypt all database contents at the application layer.
- KEK rotation: new key ID for new writes, rewrap DEKs in idempotent batches, retain old keys through backup/restore horizon, verify no active ciphertext needs old key, then retire. Suspected KEK compromise also requires assessment of secret replacement; rewrapping does not protect copies of already-exposed ciphertext/DEKs.
- Secret rotation creates a new immutable version and updates a reference transactionally. Old versions remain while referenced and through the backup recovery horizon. Delete wrapped DEK for cryptographic erasure only after retention/reference clearance; do not claim physical overwrite in SQLite WAL, PostgreSQL MVCC, or backups.
- Recovery must restore the database and compatible external keyring together. Key loss is unrecoverable for ciphertext protected by that key; test restore and key rotation as an operational procedure.

### Current-state risks

Store adapters currently derive per-engine AES-256 keys via HKDF from `MACHINARCHY_ENCRYPTION_SECRET` and encrypt Host SSH fields with AES-GCM. `internal/config.Load` allows development fallback to `MACHINARCHY_OPERATOR_TOKEN`; production requires a separate secret. No generic secret references, per-secret DEKs, KEK IDs, rotation workflow, or secret-resolution ACL exists. The fallback couples authentication and confidentiality domains and should be removed before using encrypted database secrets beyond development.

Run argv is encrypted at rest, but can be returned to principals with `runs:execute`; `run_hosts.output` and `error` are plaintext. Output can contain credentials or PII, and terminal sanitization is not secret redaction. Target design must separate `operations:output:read`, encrypt sensitive results, minimize returned fields, bound retention, and document that the remote host and process list can observe arguments.

## 7. SSH, remote command, and network security

### SSH requirements

- Use the Go SSH library; do not shell out to an external SSH executable.
- Require an explicit pinned SHA-256 host-key fingerprint before sending credentials. Fail closed on absent/mismatched keys; no TOFU or `InsecureIgnoreHostKey`. Trust pin update is an audited, authorized workflow with overlap/expiry for planned rotations.
- Resolve credentials through Secret Store by immutable version and purpose. Avoid copying secrets into broad service DTOs; clear owned byte buffers best-effort after signer/auth material creation.
- Set connect, handshake, command, and overall Operation deadlines. Cancellation closes local channels/connections but must report uncertainty when exec may have reached the remote host.
- Restrict remote accounts, sudo rights, allowed commands, and file access on Hosts. A control plane cannot compensate for root SSH credentials on every managed machine.
- Limit algorithms to currently supported secure algorithms from maintained Go library defaults; track library/security advisories and upgrade promptly. Do not permit downgrade on host-key failure.

### Network/SSRF controls

Current `pkg/netpolicy` resolves a hostname once, checks every address, dials the validated IP, denies metadata and unsafe special addresses, and supports CIDR allow/deny. This is a strong DNS rebinding mitigation. However, it allows public and (by default) private destinations and does not restrict ports. An inventory writer can therefore use registration/metrics/Operation paths as an outbound SSH scanner into reachable networks.

**Security decision that challenges the earlier default:** production must use deny-by-default destination policy for private, loopback, link-local, and public ranges, with explicit approved CIDRs and ports. Private networks should not be implicitly trusted just because Machinarchy manages infrastructure. Apply independent egress firewall/security-group rules, deny metadata at network layer too, cap DNS answer count/time, re-check policy at each dial and Operation execution, and use separate permission for changing egress policy. Any decision to allow broad private ranges requires documented owner approval and compensating network segmentation.

### Command execution

- Keep execution disabled by default and require a narrow executable allowlist; prohibit `*` for production unless a deliberate exception is documented.
- Accept structured argv only, validate each element, construct the SSH `exec` request with correct remote-shell quoting, and never accept an arbitrary shell string. Do not concatenate selector, Host name, config value, or output into a command.
- Require a distinct operation execution capability and per-operation policy. Revalidate authorization, Host/profile/trust revision, allowlist, and destination policy at execution time.
- Treat command programs as privileged: allowing `sh`, `bash`, interpreters, `ssh`, package managers, or broad utilities can bypass the spirit of the allowlist. Prefer dedicated wrappers on managed Hosts.
- Pin the target set and endpoint/access-profile revision at submit. Do not silently execute on a changed endpoint. Require idempotency-aware explicit retry; default to no retry after possible dispatch.
- Bound targets, concurrency, process-wide SSH, time, bytes, stored result retention, and per-principal admission. Drain output after cap while discarding excess; sanitize terminal/control sequences but do not claim this redacts secrets.
- Restrict output/argv read separately from Operation status. Encrypt sensitive command data and output at rest; exclude from logs/traces/audit. Make command submission auditable with actor, target IDs, program identity and policy version, while excluding raw args and output.

## 8. API and configuration security

- TLS at trusted ingress; PostgreSQL uses `sslmode=verify-full` in production today. Authenticate and encrypt broker traffic and internal service links. Do not trust forwarded proto/IP/client-cert headers from arbitrary peers.
- Use strict request DTOs, size/depth limits, unknown-field rejection, field-level property allowlists, optimistic concurrency, idempotency keys, cursor pagination and safe RFC 9457 errors. Do not serialize domain structs wholesale.
- Apply rate limits by source, identity, route and cost class: login/signup, token issuance, Host registration, target preview, Operation submit, secret/config mutations. Per-process IP throttling alone is not sufficient across replicas or for distributed credential attacks.
- Protect `/metrics`, `/scalar`, `/openapi.json`, and readiness endpoints with private network binding/ingress ACL or separate management listener. Current endpoints are unauthenticated and may disclose topology, dependency status, API shape, process/database/SSH capacity. Liveness may remain public only if it returns minimal state.
- CORS allowlists must be exact and production-only. CORS is not authorization and does not prevent direct API clients. For cookie auth, add CSRF/Origin checks or require bearer auth for unsafe endpoints.
- Keep the request body cap but add endpoint-specific lower limits for secrets/argv and CPU-heavy endpoints. Return generic errors with request ID; do not echo secrets, DB errors, connector diagnostics, or internal addresses unnecessarily.
- Configuration changes that enable remote execution, expand allowed egress, change trust pins, reduce authentication requirements, or modify key providers are security-sensitive. Require owner + step-up MFA, immutable revision/audit, process apply status, and preferably two-person approval for production widening of egress/exec policy. Rollback must be audited and validated.
- Bootstrap secrets and runtime settings are separate. Changing environment variables does not hot-mutate a running process; do not add `os.Setenv` reload behavior. Configuration revisions cannot change DB connectivity, listener/TLS identity, or the external encryption root key.

## 9. Database, Control Manager, and message-delivery security

- Use separate DB principals for API and Control Manager where practical. Grant each only required tables/operations; avoid broad schema ownership and DDL privileges at runtime. Current `Stores` uses one configured connection identity and exposes `*sql.DB` publicly through the store container.
- Protect PostgreSQL with network ACLs, TLS verification, least-privileged roles, encrypted backups, audit/connection logging policy. Protect SQLite DB/WAL/SHM and parent directory with OS permissions, local filesystem and encrypted volume/backups. A database connection compromise is a major control-plane compromise even when secret columns are encrypted.
- Current NATS connection is built from one URL with reconnect enabled; code does not enforce TLS, credentials, subject ACLs, or producer identity. Current stream accepts `machinarchy.>` and retention limits; message payloads are JSON. Require TLS/authentication, per-component publish/subscribe subject ACLs, size/schema validation, event IDs, bounded retries, and DB re-read before control action before any untrusted network deployment. Phase 4’s outbox remains a design, not current code.
- Do not treat NATS acknowledgement as authorization or successful reconciliation. Outbox/inbox dedupe is delivery safety; controller policy and durable state checks remain required. Event payloads carry IDs/revisions only, never secret material, raw argv, or full Host configs.
- Control Manager should have no general public API routes; expose only management health on a private listener. Run workers under a dedicated OS identity, restrict file access and egress, cap CPU/memory/FDs, and separate API-serving process from remote execution worker in production.
- Configuration Store and Secret Store share the database per approved decision, so logical separation, table ACLs, application ports, event boundaries, and key separation become especially important. A DB superuser remains a high-trust administrator and can alter config/state/audit.

## 10. Audit model

Separate four records:

1. **Security audit:** attributable action/decision facts, append-only, tamper-evident, externally anchored; not application debug logs.
2. **Operational logs:** safe event/error classes and request/trace IDs, no credentials, payloads, argv, output, ciphertext, tokens, or secret-derived hashes.
3. **Metrics/traces:** bounded-cardinality operational timing/counters; no Host secrets, raw addresses unless approved, operation output, usernames/tokens, or secret IDs with sensitive names.
4. **Database audit/infra logs:** access to secret/config/audit tables and privileged schema/role operations, protected independently from application audit.

Record successful and denied authentication, token issue/revoke/use metadata, authorization denials, resource changes, host-key trust changes, Secret Store create/update/resolve/retire metadata, config validate/apply/rollback and process apply failures, Operation submit/cancel/start/finish/interruption, policy decisions, and administrative key rotation. Include actor/workload, action, target IDs, request/correlation ID, timestamp, outcome, policy/revision version; never include plaintext secret or command output.

Current audit uses same-transaction append, triggers, and per-workspace hash chains for selected mutations. This does not protect against a DBA rewriting the chain or truncating its newest tail. Export/anchor sequence + head hash to a separate restricted/WORM system, alert on gaps, define retention, and make audit-write failures fail closed for security-sensitive mutations (or clearly indicate when only best-effort events can be emitted).

## 11. Attack scenarios

| Scenario | Likely path | Required prevention/detection |
|---|---|---|
| Operator pivots into internal network | Registers private IP or hostname; SSH listener is reachable; chooses/pins its presented host key | Production CIDR/port allowlist and egress firewall; separate host registration from egress grant; audit target/policy; rate limit dials |
| Stolen operator API token runs fleet commands | Token inherits operator role that includes `runs:execute`; attacker targets all hosts | Separate operation grants, token-specific scopes, limited target selectors, expiry/revocation, MFA for privilege grant, anomaly alerts |
| Cross-tenant object ID probing | User changes Host/Operation ID in path or nested payload | Scoped lookup/update in application and store, per-object policy, consistent 404, adversarial authorization test suite |
| Secret exfiltration via authorized API field | Host/secret detail or Operation response includes credentials/argv/output | DTO/property authorization, no plaintext reads, separate output permission, response schema allowlist, serialization tests |
| CSRF changes Hosts/config via browser cookie | Victim visits attacker page; browser sends auth cookie to unsafe endpoint | CSRF token and strict Origin checks, SameSite/Secure/HttpOnly, bearer-only automation routes |
| Fake SSH server harvests credentials | Network attacker redirects DNS/address to impostor | Verify pinned fingerprint in handshake before auth; no TOFU; reject mismatch and never retry insecurely |
| Remote output causes DoS or terminal/log injection | Malicious Host streams huge bytes or ANSI sequences | Drain/discard beyond cap; deadline and concurrency limits; sanitize before persistence/display; do not log raw output |
| Allowed command becomes shell escape | Operator selects `sh` or passes arguments that alter an allowed interpreter | Narrow executable allowlist and dedicated remote wrappers; explicit warnings; server-side argv validation and quoting; per-operation authorization |
| Database theft exposes fleet secrets | DB/backup dump includes encrypted SSH creds/configured secrets | Envelope AEAD, external KEK, encrypted backup/volume, least-privileged DB, access alerts, rotation and restore drills |
| Database owner rewrites audit | DBA disables triggers, recomputes chain, removes tail | External WORM/hash anchor, independent infrastructure audit, privileged DBA access separation |
| Forged or replayed broker event triggers action | Producer publishes plausible Host/Operation event | Authenticated broker ACLs, schema/event ID, inbox/dedup, re-read DB, controller authz/policy, no payload-driven commands |
| Compromised Control Manager executes fleet-wide work | Process identity has DB, secrets and egress access | Process isolation, scoped DB and secret resolver identity, allowlisted egress, bounded connector capability, workload identity, incident revocation |
| Agent credential stolen | Attacker reports Host facts or requests tasks as Agent | Per-agent mTLS identity bound to Host, short certificates, revoke, sequence/expiry, least capability, alert on duplicate identity |

## 12. Risk classification and priorities

| Priority | Risk | Current evidence / location | Required disposition |
|---|---|---|---|
| **Critical** | Production egress allows all public and private SSH destinations by default; no port allowlist | `pkg/netpolicy/policy.go` `Default`, `internal/config/config.go` default `AllowPrivate=true`; no port filter | Change secure production default to deny-by-default; allow explicit approved CIDR/port rules and enforce network egress firewall before enabling multi-user execution |
| **Critical** | Control Manager/runtime can use database and broker as broadly privileged shared identities; event path has no enforced TLS/auth/subject ACL in app config | `internal/bootstrap/runtime.go`, `internal/broker/nats.go`, `internal/store/store.go` | Separate workload credentials/DB roles, secure broker, least-privilege secret resolver; prevent untrusted publishers |
| **High** | `operator` combines inventory write and command execution; one workspace is shared by all current accounts | `internal/domain/authorization.go`, `internal/transport/http/v1/router.go`, `docs/SECURITY.md` | Split grants; add resource/environment scopes; do not enable broad execution for routine inventory editors |
| **High** | Cookie-authenticated state-changing API has no explicit CSRF/Origin check | `internal/transport/http/middleware/auth.go`, `internal/transport/http/v1/auth/handler.go` | Add CSRF protections or remove cookie auth for unsafe routes; require strict Origin policy |
| **High** | Operations expose argv/output under `runs:execute`; output/error are plaintext in DB and can contain secrets | `internal/transport/http/v1/runs/handler.go`, `internal/store/migrations/*/000006_runs.up.sql`, `internal/domain/run.go` | Separate result access, encrypt sensitive results, retention, and no-log policies; warn about remote process visibility |
| **High** | Signing key accepts any 32-character token string; shared HS256 secret can mint and verify; no key ID/rotation/session revocation | `internal/config/config.go`, `internal/service/auth_service.go`, `internal/transport/http/middleware/auth.go` | Require generated high entropy key material, key IDs/rotation, MFA/step-up, revocation strategy |
| **High** | Login has no dedicated account-aware throttling/MFA; general rate limiter is process-local and IP-based | `internal/transport/http/server.go`, `internal/transport/http/middleware/ratelimit.go`, auth handlers | Add identity+source throttling, distributed limits, MFA/step-up for owners |
| **High** | Metrics, OpenAPI/Scalar, and health endpoints are unauthenticated | `internal/transport/http/server.go` routes `/metrics`, `/openapi.json`, `/scalar`, `/healthz/*` | Bind management endpoints privately or apply ingress ACL/auth; minimize health details |
| **High** | Encryption key fallback couples dev encryption to JWT signing key; no key rotation or separate secret resolver ACL | `internal/config/config.go`, `internal/store/sqlite/sqlite.go`, `internal/store/postgres/store.go`, `pkg/crypto/crypto.go` | Remove fallback; separate external KEK; implement envelope/key rotation before general Secret Store use |
| **High** | Current migration/audit security validation has unverified PostgreSQL and incomplete denied/auth event coverage | `docs/SECURITY.md`, `internal/store/migrator.go`, audit stores | Run required engine contract/security tests in implementation phase; export audit anchor and define coverage |
| **Medium** | Rate limit/per-SSH concurrency caps multiply by replica count; host/network targets can cause expensive probes | `internal/transport/http/middleware/ratelimit.go`, `internal/service/ssh_capacity.go` | Global/distributed quotas and per-principal admission; budget operation cost and DNS results |
| **Medium** | CORS credentials and proxy/TLS trust depend on deployment; direct listener defaults to `:8080` | `internal/transport/http/server.go`, `.env.example` | Document/enforce private bind or trusted TLS ingress, exact origin, proxy header policy |
| **Medium** | Audit chain resides in same DB and newest-tail removal is not detectable internally | `internal/store/migrations/*/000005_audit_events.up.sql`, `internal/store/*/audit.go` | External hash anchoring/WORM export, retention and alerts |
| **Medium** | Host registration currently synchronously probes SSH before persist, which couples write API to network risk and resource exhaustion | `internal/service/host_service.go`, Host POST handler | Move to desired-resource persistence + controlled async reconciliation/validation flow |
| **Medium** | Future Agent protocol/identity/capability model is not implemented | `cmd/agent/main.go`, `internal/domain/host.go` | Do not accept Agent traffic until unique identity, enrollment, revocation, replay and capability controls exist |
| **Low** | Error/response formats and list behavior are inconsistent; some validation detail can expose operational input | `internal/transport/http/v1/*` | Standardize DTOs/errors and review property-level disclosure |

## 13. Secure defaults

- Signup disabled; owner access provisioned deliberately; owner MFA/step-up mandatory before production execution or secret management.
- No implicit private network access; deny all egress until explicit CIDR/port policy; metadata ranges denied both in app and network layer.
- Remote execution disabled; narrow per-operation executable policy; no raw shell; no retry on uncertain dispatch.
- All SSH host keys pinned; all credentials resolved by versioned SecretRef; no trust-on-first-use.
- API tokens read-only, short-lived, scoped, displayed once, revocable; execution privileges are separately granted and audited.
- HTTPS for users; PostgreSQL TLS with certificate verification; authenticated TLS for broker and Agent; private management endpoints.
- Secret encryption keys external and independent from JWT signing key; database/backup encryption; no key fallback.
- Fail closed on missing/invalid policy, secret, trust pin, authorization, or configuration revision; never substitute empty credentials or default-allow.
- Bound requests, lists, target sets, workers, connections, execution time, output, retries, and storage retention.
- No plaintext secrets, tokens, argv, or remote output in ordinary logs, metrics, traces, audit, events, API responses, or support bundles.
- Security-sensitive config changes require explicit authorization, step-up, audit, revision history, apply status, and second-person review for production egress/execution widening.

## 14. Security acceptance criteria before production remote execution

These are future verification criteria; none were run as part of this documentation review.

1. Every route and application use case has a documented function permission, object-scope check, property-level response/write policy, and negative tests for cross-workspace/resource IDs.
2. Cookie-based unsafe requests reject missing/invalid CSRF/Origin proof; bearer API-token requests remain stateless and never accept tokens in cookies/URLs.
3. Login, signup, token issuance, config/secret writes, target preview, and Operation submission have identity-aware and source-aware rate limits with cross-replica behavior documented.
4. Production SSH policy defaults to no destination access; tests cover mixed DNS answers, rebinding, IPv4-mapped IPv6, metadata, denied ports, and all actual connector dial paths; network firewall rules independently enforce expected egress.
5. Host-key mismatch/unknown pin proves that no credential or command is sent; key rotation and revocation are audited.
6. Secret ciphertext cannot be decrypted from database/backup without the external KEK; test key rotation, restore with old/new key IDs, denied secret resolution, and plaintext non-disclosure in every output surface.
7. Command injection/property tests cover hostile argv, quoting, allowlist bypass programs, output floods, ANSI/control output, cancellation before/after dispatch, and uncertain retry prevention.
8. API/Control Manager DB roles and broker credentials are distinct and least privileged; broker TLS, authentication, subject ACLs, payload bounds, replay/dedupe, and DB re-read are verified.
9. `/metrics`, `/scalar`, `/openapi.json`, readiness and management listeners are unreachable from untrusted networks or protected by explicit policy; no credential/config/Host secrets appear in metrics or traces.
10. Security-sensitive changes, secret lifecycle and remote Operations generate complete audit records; external audit anchoring detects truncation; audit outages have explicit fail behavior.
11. Agent traffic remains disabled until unique mTLS identity, enrollment, rotation/revocation, Host binding, replay handling and per-capability authorization are verified.
12. PostgreSQL and SQLite security contracts run in CI; PostgreSQL migration/authz/concurrency test results are required before claiming distributed deployment readiness.

## 15. ADRs and review boundary

See ADR-036 through ADR-041 for separate authorization layers, deny-by-default egress, secret resolution/encryption boundaries, service/Agent identity, audit anchoring, and high-risk remote execution. This review challenges and recommends changing the previously proposed private-network-allowed default for production. Other approved product constraints (database-backed secrets, global config scope, API/CLI management) remain intact; the security controls above constrain their use rather than replacing them.
