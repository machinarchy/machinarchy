# ADR-060: Member onboarding: email invitations and owner-added users, no temporary passwords

**Status:** Proposed (owner chose email delivery on 2026-11-27; API shape and mailer settings need owner approval before steps 2.3 and 2.4)
**Date:** 2026-11-27
**Builds on:** ADR 0001 (production self-registration off), ADR 0003 (shared workspace, owner-managed roles), step 2.1 (member removal)

## Context

With production signup disabled (ADR 0001), the only way to get a second person into the workspace is for an owner to create an account by some out-of-band means. Today there is no supported path: `GET /members`, `PUT /members/{id}/role` and `DELETE /members/{id}` manage existing accounts only. Two needs follow:

1. **Invite:** an owner names an email address and role; the person joins and chooses their own password.
2. **Add user:** an owner creates an account directly, for people without a reachable mailbox or for a deployment with no mail server.

The usual shortcut for both is a temporary password chosen or shown by the owner. That password is known to a second person, travels through chat or tickets, and often stays valid. This project already rejected a built-in default credential (ADR 0003).

## Decision

1. **Two flows, one token mechanism.** Both create a single-use *onboarding token*; neither involves a password chosen by the owner. No temporary or initial passwords exist anywhere: not generated, not stored, not displayed.
2. **Token properties.** 256 bits from `crypto/rand`, shown once, stored only as a SHA-256 hash, expiry 7 days for invitations and 24 hours for added users, single use (consumed in the same transaction that sets the password), constant-time comparison by hash lookup. Redeeming a token is rate limited per client network and per token hash, and a wrong, expired or used token gives one generic error.
3. **Invitation (`members invite`).** An owner (human session only, like role changes) submits email and role. The role is `reader` or `operator`; inviting an `owner` is not allowed in this step (ADR 0003 leaves owner promotion to a later step-up confirmation). The controller emails a link to the invitee, who opens it, picks a username and a password (same 12..72 byte rule as signup), and gets a membership with exactly the invited role. One pending invitation per email and workspace; a second one replaces the first. An existing account's email is refused with the same response as a new one, so the endpoint does not reveal which addresses have accounts. Owners can list pending invitations and revoke them; revoking, expiry and acceptance all invalidate the token.
4. **Add user (`members add`).** An owner submits username and role (and optional email). The controller creates the account with a password hash that cannot match any password, plus the membership, and returns a one-time set-password link **once** for the owner to hand over; it is not retrievable afterwards (a new link can be issued, which invalidates the old one). Until the link is used the account cannot log in. If the owner gave an email and mail is configured, the link may be sent instead of returned.
5. **Link, not credential, in transit.** Links point at the controller's configured public URL and carry the token in the URL fragment, so it does not appear in server logs or `Referer`. The page or CLI call that redeems it sends the token in a POST body.
6. **Mail is optional configuration.** A `mailer` section in runtime configuration (SMTP host, port, STARTTLS/TLS required by default, username, from address) with the SMTP password in the secret store (ADR 026/027), never in plain settings and never read back. Without mail configured, `members invite` returns a clear error naming the missing setting, and `members add` remains available. The mailer is a small interface in the service layer so tests use a fake; the SMTP client uses the standard library (`net/smtp`) to avoid a new dependency in the controller, and the CLI allowlist is unaffected.
7. **Audit.** Hash-chained events in the same transaction as the state change: `member.invited`, `member.invite_revoked`, `member.joined` (invitation or add-user redemption), `member.added`, `member.link_reissued`. Events record actor, target email or username, role and expiry; never the token or its hash.
8. **Authorization.** Issuing, listing, revoking and reissuing require a human owner session; API tokens are refused. Redeeming requires only possession of the token. The role is fixed at issue time and cannot be changed by the redeemer, so there is no self-promotion.
9. **Storage.** One new table `member_onboarding` (id, workspace, kind `invite|add`, token hash, email, username, role, created by, created at, expires at, consumed at), added as a forward-only migration for SQLite and PostgreSQL with contract tests on both (ADR-020). The existing `users.reset_token` columns are not reused; they belong to password reset.

## Alternatives considered

- **Owner sets a temporary password, user must change it on first login:** simple, but the owner and any channel carrying it learn a live credential, and a forgotten change leaves it valid. Rejected.
- **Generated temporary password shown once to the owner:** same exposure and needs a forced-change state in login. The one-time link gives the same operator experience with no credential at all.
- **Email only, no add user:** leaves deployments without mail unable to onboard anyone.
- **Open self-registration with approval:** reopens the signup surface ADR 0001 closes.
- **Third-party mail API (HTTP) instead of SMTP:** adds a vendor dependency and a credential class; can be added later behind the same interface.

## Consequences

- Operators who want email invitations must configure SMTP; everyone else uses `members add` and passes the link on themselves.
- A link handed over insecurely is a bearer secret until it expires or is used; the short expiry, single use and reissue-invalidates-old rules bound that, but do not remove it.
- Account enumeration through the invite endpoint is closed by uniform responses; the add-user endpoint reveals username conflicts to owners, who can already list members.
- Inviting owners, multi-workspace invitations and invitation-by-API-token stay out of scope.
- Implementation is split: 2.3 invitations (mailer, token table, API, CLI), 2.4 add user (reuses the token table and redeem endpoint). Both need negative and security tests: expired, reused, revoked, wrong-workspace and role-tamper tokens, rate limiting, no token in logs or audit.
