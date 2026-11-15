# POS consumer identity and member transactions

## Status

**Design proposal only. No consumer account, verification, session, sale-linking, or transaction-history functionality is implemented.** The P07 acceptance item for member-linked transaction lookup remains incomplete.

The POS Go service has first-party employee accounts and sessions. Those identities authorize staff operations and are not consumer identities. The `memberships` table associates organization users; it is not a loyalty-member table. `pos_sales.customer_id` is currently an optional opaque value and checkout does not populate or validate it. Do not use an employee session, demo workspace cookie, or caller-supplied customer ID, phone number, or email as proof of a consumer identity.

## Proposed identity model

Use a first-party consumer account model owned by the POS service and kept separate from `users`, `station_accounts`, `station_grants`, `station_sessions`, and employee memberships.

- `pos_consumer_accounts`: opaque account ID, organization ID, normalized email, password hash, email verification timestamp, disabled state, and timestamps. Enforce uniqueness for `(organization_id, normalized_email)` so an email address is not silently linked across organizations. Define one documented normalization routine and apply it consistently at registration, login, verification, and recovery lookup. Treat normalization as an application matching policy; do not assume different address spellings identify the same mailbox beyond the transformations explicitly chosen.
- `pos_consumer_verification_challenges`: organization/account, purpose, hash of a random single-use secret, expiry, attempt count, consumed timestamp, and creation time. Support email verification and password reset. Never store or log raw challenge secrets.
- `pos_consumer_sessions`: hash of a high-entropy random session token, organization/account IDs, expiry, revocation timestamp, and creation time. Resolve this identity only from a separate consumer cookie; do not fall back to or accept the employee session cookie.
- `pos_consumer_pairing_tokens`: organization/account, hash of a random one-time token, short expiry, consumed sale ID, and creation time. This binds an in-person checkout to a consumer account without asking staff to search by personal data or submit an account ID.

The account identity established by verified email means control of that mailbox. It does not establish a legal identity or prove that the mailbox owner is the purchaser in every context. The existing station password hashing code can inform password storage, but employee account and session rows must remain independent.

Consumer registration, verification, login, logout, password recovery, and history should use consumer-specific routes and identity types. Resolve organization membership from the consumer session and a stable public organization/store route. Do not select the tenant from an arbitrary `organizationId` parameter or reuse the demo workspace cookie.

## In-store sale association

1. A consumer signs in on their own device and requests a short-lived pairing token. The server returns the raw token once for display as a QR code and persists only its hash, bound to that consumer and organization.
2. The cashier scans the QR code during checkout. The checkout request carries the pairing token, not a `customerId`, email, or phone number.
3. In the existing checkout transaction, the server validates that the token is unexpired, unused, and for the active organization; resolves the account; records its ID on the sale; and consumes the token with the sale, inventory, audit, and idempotency writes.
4. An anonymous sale remains supported and stores no consumer association.

The existing idempotency lookup happens before checkout business effects. Preserve that ordering: an identical retry after a successful but ambiguous response must replay the saved result even though the pairing token has been consumed. Reusing the key with a changed token must conflict. A failed checkout must not consume the pairing token or leave partial sale/stock effects.

## Consumer transaction history

Provide a bounded, read-only endpoint such as `GET /api/pos/consumer/me/transactions`. Derive organization and account IDs only from the validated consumer session; the request must not accept a target customer ID, email, phone, or receipt number as an authorization selector. Every query must scope by both organization and consumer account. Return only the fields needed for a consumer receipt list, use a bounded cursor and page size, and send `Cache-Control: no-store`.

This endpoint is not a staff lookup tool. Keep the existing back-office exact-receipt refund lookup separate. Staff access to consumer purchase histories, searches by phone/email, customer profile management, and cross-organization account linking are outside this proposal.

## Session and abuse protections

- Generate opaque random session secrets; store only their cryptographic hashes. Use a consumer-specific cookie with `HttpOnly`, `Secure` on HTTPS, `Path=/`, no `Domain`, and an explicitly chosen `SameSite` policy. Determine HTTPS from the direct TLS connection or a forwarded-protocol header only when supplied by a configured trusted proxy; never trust arbitrary client-supplied forwarding headers. Do not put bearer tokens in browser storage.
- Expire and revoke sessions server-side. Rotate sessions after authentication and sensitive account changes. Choose maximum lifetime, idle timeout, and recovery behavior as part of deployment configuration.
- Require same-origin requests for cookie-authenticated mutations and add a CSRF token check. The current global origin check rejects a mismatching nonempty `Origin`, but permits a missing `Origin`; consumer mutations need a stricter rule.
- Make registration, verification, login, and reset responses resistant to account enumeration. Persist rate limits across process restarts, bound challenge attempts, and consume challenges once. Trust forwarded client IP headers only from a configured trusted proxy.
- Do not expose passwords, session hashes, verification challenges, pairing-token values, or unnecessary personal data in API responses, logs, audit payloads, or errors.

## Schema and storage boundary

The Go backend uses POS SQLite. Its startup migration list is explicitly assembled in `backend/main.go`, and the repository marker remains version 1. Additive consumer tables must be registered in that migration path and tested against both a fresh database and an existing marked POS database. Preserve existing sales and retry rows; do not rewrite the base schema file for an already initialized database.

The README documents that the Go POS SQLite database and the Vinext Worker D1 database are separate and do not share data. Consumer transaction history must read from the same authoritative store that receives Go POS sales. Do not implement a D1 history endpoint or imply synchronization until a verified data-sharing design exists.

## Test-first plan

Implement backend contracts and tests before consumer screens or pairing UI.

### Database and migration

- Fresh database creates consumer tables and indices.
- Existing marked POS database upgrades without losing staff, sales, refunds, or idempotency rows; repeated startup is safe.
- Uniqueness is organization-scoped; cross-organization records cannot be linked by ID.
- Session, challenge, and pairing secrets are stored only as hashes.

### Consumer authentication

- Unverified accounts cannot create consumer sessions; invalid, expired, reused, or over-attempt challenges fail safely.
- Registration and recovery do not disclose whether an email is already registered. Rate limits persist and cover account and network dimensions.
- Consumer cookie is distinct from the employee cookie; employee-only sessions cannot call consumer history and consumer sessions cannot call employee operations.
- Test session expiry, logout/revocation, sensitive-change rotation, cookie flags, missing/mismatched Origin, CSRF failure, and disabled accounts.

### Checkout pairing and idempotency

- Anonymous checkout retains current behavior and has no consumer ID.
- A valid same-organization pairing token links exactly one sale; expired, consumed, malformed, and cross-organization tokens do not create a sale or change stock.
- Concurrent uses of one pairing token produce at most one linked sale.
- A failed checkout, including a stock race, leaves the token usable and leaves no partial sale, stock, audit, or idempotency effects.
- Identical checkout retry replays the original linked result after token consumption; changed-payload key reuse conflicts.

### Transaction history

- Unauthenticated requests fail. Authenticated consumers see only their own account's sales in their organization, across that organization's stores.
- Requests cannot select another account by ID, email, phone, or receipt number. Cross-organization IDs and altered cursors cannot disclose rows.
- Page size is bounded; response and cache headers contain no unnecessary personal or credential data.

### Browser flow

After backend tests pass, cover registration, email verification, login/logout, pairing QR creation and expiry, cashier scan and checkout, retry after an ambiguous response, own-history display, and attempts to alter the target identity. Use synthetic accounts and sales only.

## Rollout sequence

1. Register and test the additive Go SQLite migration, then add consumer auth handlers and security-focused backend tests behind a disabled feature flag.
2. Configure and verify an email sender for verification and recovery. Keep accounts unverified and consumer routes unavailable until delivery works end to end.
3. Provide a stable public organization/store route and a same-origin HTTPS consumer entry point that reaches the Go service and its authoritative SQLite database. The documented OCI setup currently binds Go to loopback port 8095 and describes private VPN/SSH-tunnel reachability; this is not a public consumer access path.
4. Enable consumer registration and account sessions in a staging environment with synthetic data. Verify migration recovery, rate limits, email delivery, cookie behavior, and session revocation.
5. Enable pairing and sale association; verify retry and concurrency behavior with synthetic checkout flows.
6. Enable the self-only transaction-history page after verifying that the endpoint derives the owner from the consumer session and reads the Go POS database.

## Missing deployment and product inputs

- Email delivery provider, sender domain, secret configuration, and delivery monitoring for verification and recovery.
- Public tenant/store URL mapping and consumer enrollment rules. The demo workspace cookie is not a production tenant selector.
- Public HTTPS routing to the Go POS service and the authoritative POS database; current OCI documentation describes private loopback service access only.
- Whether verified mailbox control is sufficient for the product's meaning of “member identity”; it is not legal identity verification.
- Session lifetime, idle timeout, recovery, account lockout/support, privacy notice, retention, export/deletion, and same-email multi-organization policy.

Until those components exist, the repository has no functional consumer registration, verified identity, sale association, or self-service transaction history. Local tests can prove code paths once implemented, but cannot establish public reachability, email delivery, or production identity assurance.
