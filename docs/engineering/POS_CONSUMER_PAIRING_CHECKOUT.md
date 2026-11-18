# POS consumer pairing, checkout association, and transaction history (P07)

## Context and Purpose

This delivery completes the core backend implementation of P07 (POS consumer identity association and member transaction lookup) defined in `docs/engineering/POS_CONSUMER_IDENTITY.md` and tracked in `DEVELOP_PLAN.md` and `HANDOFF.md`.

Previously:
- PR #17 proposed the consumer identity model.
- PR #19 delivered `0006_pos_consumer_identity.sql` and the foundation for consumer registration, email verification, session authentication, and logout, but explicitly noted that checkout pairing, sale association, and transaction history were outstanding.

## Architecture and Components

### 1. Database Schema (`0007_pos_consumer_pairing.sql`)

- Table `pos_consumer_pairing_tokens`:
  - `token_hash`: SHA-256 digest of high-entropy pairing token (`pair_<hex>`)
  - `account_id`: references `pos_consumer_accounts(id)`
  - `organization_id`: references `organizations(id)`
  - `expires_at`: millisecond timestamp (5-minute TTL)
  - `consumed_sale_id`: populated atomically when consumed by checkout
  - `created_at`: millisecond timestamp
  - Foreign key: `(account_id, organization_id)` references `pos_consumer_accounts(id, organization_id) ON DELETE CASCADE`
  - Index: `pos_consumer_pairing_tokens_account` on `(organization_id, account_id, expires_at)`

- Association with `pos_sales`:
  - Uses the existing `pos_sales.customer_id` column to store the authenticated consumer account ID. Anonymous checkouts continue to leave `customer_id` as `NULL`.

### 2. Pairing Token Generation (`POST /api/pos/consumer/{organization}/pair`)

- Requires an authenticated consumer session (`pos_consumer_session` cookie).
- Validates that the account is active (`verified_at IS NOT NULL` and `disabled == 0`).
- Generates a single-use random token with 5-minute expiry.
- Persists only the SHA-256 hash in `pos_consumer_pairing_tokens`.
- Returns `{ "pairingToken": token, "expiresAt": millis }`.

### 3. In-Store Checkout Association (`/station/pos/checkout`)

- Accepts an optional `pairingToken` in `posCommand`.
- Within the checkout database transaction:
  - Hashes the pairing token with SHA-256.
  - Validates that the token exists, belongs to the current organization, has not been consumed (`consumed_sale_id IS NULL`), has not expired (`expires_at > now`), and belongs to an active, verified consumer account (`verified_at IS NOT NULL` and `disabled == 0`).
  - If invalid, expired, or already consumed, returns HTTP 409 conflict via `terminalPOSBusinessRejection`.
  - Atomically marks the pairing token consumed by setting `consumed_sale_id = sale_id`.
  - Records `customer_id` on the `pos_sales` record.
- Idempotency: replaying an identical checkout request replays the original completed sale and preserves the linked `customer_id` without re-verifying or re-consuming the token. Reusing the key with a different token is rejected with 409.

### 4. Consumer Transaction History (`GET /api/pos/consumer/{organization}/transactions`)

- Requires an authenticated consumer session (`pos_consumer_session` cookie).
- Derives the consumer account ID strictly from the session; does not accept caller-provided customer IDs, emails, or phone numbers.
- Queries `pos_sales` where `organization_id = ? AND customer_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 50`.
- Includes associated line items from `pos_sale_lines` (item name, barcode, quantity, unit price, line total).
- Strictly isolates tenants: consumers cannot view sales from another organization.

## Boundary and Security Rules

- **Gating**: Production consumer routes remain gated behind `consumerIdentityEnabled`.
- **Identity Isolation**: Consumer accounts and sessions remain completely separated from staff/operator `users`, `station_accounts`, and `memberships`.
- **Secret Hygiene**: Raw pairing tokens are never persisted in the database; only their cryptographic hashes are stored.
- **Fail-Closed**: Expired, already-consumed, cross-tenant, or unverified tokens reject checkout and never alter inventory, drawer cash, or audit logs.
