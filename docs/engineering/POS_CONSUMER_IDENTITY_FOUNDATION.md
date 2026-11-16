# POS consumer identity foundation

## Status

This change adds an additive SQLite foundation and an isolated Go API for consumer registration, email verification, login, logout, and the current-account endpoint. **Production consumer routes are hard-disabled.** It does not add checkout pairing, sale association, transaction history, public store routing, or a production email provider.

## Account and verification flow

- Consumer accounts, verification challenges, and sessions use their own tables and never reuse employee users, memberships, grants, sessions, or cookies.
- Organization selection is by an existing organization slug resolved server-side. The request cannot select a tenant by organization ID.
- Email matching applies trim and lowercase only. It does not remove plus tags or dots or infer provider-specific mailbox equivalence.
- Registration accepts an email address only. It does not set or store a password before the recipient proves mailbox control. The recipient chooses a password while consuming the single-use verification challenge. This prevents someone who pre-registers another person's email from choosing the password that becomes active after the recipient verifies.
- Passwords use the existing PBKDF2-SHA256 routine. Session and challenge secrets are random and only their SHA-256 hashes are stored.
- The consumer session cookie is distinct from all employee cookies, scoped to `/api/pos/consumer/`, Secure, HttpOnly, and SameSite Strict. Consumer lookups require the organization resolved from the route and never fall back to employee identity.
- Mutating consumer requests require an exact same-origin `Origin`. Forwarded protocol is accepted only when the request comes from an explicitly configured trusted proxy CIDR.

## Production enablement requirements

The Go `main` wiring keeps consumer routes disabled regardless of environment variables. Do not enable the test-only App gate in a deployed service until all of these are implemented and reviewed:

- A durable asynchronous challenge-delivery queue and a configured email provider. The current injected sender is a test seam; it is synchronous and is not wired in production.
- Persistent registration and verification rate limits covering normalized account and trusted network dimensions. Those controls are not implemented in this foundation.
- Stable public tenant/store URL mapping and HTTPS routing to the authoritative Go POS service.
- End-to-end email delivery, session, privacy, retention, and support policies.

Without those parts, this code is not an operational membership feature. No consumer checkout link or history route is present.

The direct injected sender path is synchronous and can take different time for a new or pending account than for an already verified account. Registration and verification also have no persistent throttles yet. These timing and abuse risks remain in the gated test implementation; the hard-disabled production wiring is required until asynchronous queued delivery and persistent limits remove them.
