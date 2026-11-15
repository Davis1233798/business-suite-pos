# POS P06 cash movements and shift variance

## Goal

Record cash paid into or taken out of an open POS drawer, then close the original shift against a counted closing amount. Persist movement records, the final cash calculation, audit events, and retry results with organization and shift scope. Keep this as an incremental P06 slice; it does not expand refund eligibility or change P01–P05 rules.

## Current boundary

`pos_cash_movements` already has `organization_id`, `shift_id`, `movement_type`, integer `amount`, `reason`, and `created_at`. The shared POS business handler already supports `open`, `checkout`, `close`, and `refund`. Close currently calculates `opening_cash + completed cash sales + cash movements - refunds`, writes `closing_cash`, `expected_cash`, and `cash_discrepancy`, and uses the existing idempotency table. P06 should reuse those records and the existing transaction and audit conventions; it should not add a parallel ledger or accept a client-supplied expected total.

## API contract

Use the existing `POST /api/stations/pos/business` command envelope and organization-scoped idempotency table.

- Cash in/out: `action:"cash-movement"`, `shiftId`, `movementType` (`"in"` or `"out"`), `amount` (integer minor units, 1–1,000,000,000), trimmed nonblank `reason` (maximum 200 UTF-8 bytes), and `idempotencyKey` (8–128 bytes). The server returns movement ID, type, amount, reason, and shift ID after commit. No client balance or expected total is accepted. The scoped POS terminal read includes the signed-in cashier's current `expectedCash` per open shift for display and early input bounds; POST always recalculates transactionally.
- Close: retain `action:"close"`, `shiftId`, `closingCash` (counted integer minor units, 0–1,000,000,000), optional `notes`, and `idempotencyKey`. If counted cash differs from expected cash, a trimmed explanation of at most 200 UTF-8 bytes is required and stored in `pos_shifts.notes`. The server returns the authoritative `expectedCash`, `closingCash`, and signed `discrepancy = closingCash - expectedCash`. A negative discrepancy means the counted drawer is short; a positive value means over. UI must label the sign and show the server response.
- Successful retries use the same organization + endpoint + idempotency key + request fingerprint semantics as existing POS writes. Same payload replays the committed response; changed payload conflicts. A client must freeze and retry the original payload/key after an ambiguous response, never mint a new key automatically.
- P06-only command fields (`movementType`, `amount`, `notes`) use `omitempty` so old `open`, `checkout`, and `refund` requests retain their pre-P06 JSON fingerprint. Regression coverage seeds idempotency rows with the previous command shape and verifies old refund and checkout retries replay their persisted response after upgrade.
- Deterministic validation, closed/missing shift, and insufficient-cash rejections after idempotency lookup are stored as terminal idempotency responses before returning `error.outcome:"not_applied"`; a changed payload using that key conflicts. If transaction state or commit outcome is uncertain, return an ambiguous service error without the marker; the client retains the original key and payload for reconciliation/retry.

## Authorization and data scope

- Require an enabled POS grant and reject `readonly` for writes.
- Resolve every shift by both `shift_id` and authenticated `organization_id`; foreign-organization and missing shifts must not disclose one another.
- Cash movements require an open shift owned by the cashier or an enabled back-office admin. Preserve the actor ID in audit data. Do not let a front operator modify another cashier's drawer.
- Closing follows the existing rule: the shift owner or an authorized administrator may close an open shift. Recheck both owner/role and open state inside the write transaction.
- Do not close a shift while its cashier has any `retrieved` held cart in that organization and store. The held-cart schema does not associate a cart with one specific shift, so this guard checks the cashier's claimed carts across the same store; shared `held` carts do not block closing. Refuse with a durable same-key `not_applied` result. Claim and close serialize in their write transactions, so either the claim is visible and close is rejected, or close wins and the claim is terminally rejected. Never auto-release a cart.
- P06 records only drawer movements and closing variance. It does not create payouts, sales, refunds, or stock changes.

## Accounting invariants

For a shift, calculate expected cash from committed source rows only:

`opening_cash + SUM(completed cash sale total_amount) + SUM(cash-in amounts) - SUM(cash-out amounts) - SUM(refund_amount for original cash sales on this shift)`.

- Cash movement types are an allowlist (`in`, `out`); amount is positive and integer minor units. Reject zero, negatives, overflow, invalid UTF-8 byte length, blank reason, and unknown types.
- Prevent a cash-out from making the expected drawer balance negative, using a transactional calculation. Recheck under the same write transaction that inserts the movement and idempotency/audit rows; insufficient balance is a durable terminal rejection so the UI can safely release that exact attempt and accept a corrected amount with a new key.
- Permit an explicit cash-in even when a legacy open shift already has a negative expected drawer balance. This is the cashier-recorded recovery path: do not silently change opening cash, delete legacy movements, or auto-adjust the ledger. The terminal balance remains signed until recorded cash-in restores it; cash-out and close remain guarded by the current server balance.
- Closing persists one final counted amount, expected amount, signed discrepancy, closed state, and timestamp. A repeated close with the same key replays the original result; another key sees a closed-shift conflict and cannot overwrite the count.
- Refund arithmetic remains authoritative in P05. Cash refunds are subtracted from the original cash shift exactly once. Non-cash sales remain excluded from expected cash.
- A cash refund must also fit within the original shift's current expected drawer balance inside the same transaction. Reject and durably reserve an insufficient-cash refund before changing refunded quantity, stock, refund rows, or audit; this prevents prior cash-outs from turning a valid sale refund into a negative drawer.
- Any failure to persist the movement/close, idempotency result, or audit row rolls back the business write and returns no success response.

## Concurrency and retry boundary

The database write transaction is the serialization boundary for movement, close, sales, and refunds. Do not compute a drawer amount from a UI snapshot. Conditional writes must include organization and open status. A movement racing a close may either commit before close and be included, or lose the race and receive a retryable/closed result; it must never commit after a closed shift or be silently omitted from a successful close total. A refund racing a cash-out must not allow both operations to commit if their combined amounts exceed the available drawer; a refund racing close must either be included in the final expected amount or be rejected after close. If SQLite reports a busy/snapshot or commit error, preserve ambiguity and require same-key retry. Add tests using separate `App`/SQLite handles, because a single `*sql.DB` pool does not represent independent processes.

## Tests first

1. Valid cash-in and cash-out persist one scoped movement, one audit event, and one idempotency record; close totals include them with correct signs.
2. Amount/type/reason limits reject invalid input; cash-out above current expected drawer balance is rejected without movement, audit, or idempotency success record.
3. Same-key same-payload retry replays the movement/close response and creates no duplicate rows; same-key changed payload conflicts and cannot change the movement or closing count.
4. A second organization cannot read or mutate the shift. A front operator cannot move cash on another cashier's shift; read-only roles cannot write. Admin exception, if chosen, is explicitly tested.
5. Closing with no sales/movements, with cash sales, with refunds, and with cash in/out produces the expected total and signed over/short variance. Non-cash sales do not affect expected cash.
6. A close retry after an ambiguous commit resolves to the original stored variance. A fresh key cannot overwrite a closed shift.
7. Race cash-in/out against close, race two cash-outs, and race refunds against cash-out and close through separate database handles. Assert one valid serial outcome: either committed movement included in close or a non-success movement; no success can omit an already committed movement, no movement may land after close, a refund fits current drawer liquidity, and the drawer cannot go below zero.
8. Inject failures at movement/close write, audit insert, idempotency insert, and commit boundaries where feasible; assert atomic rollback and retry behavior.

## Delivery boundary

Backend/API and tests come first. Add UI only after the exact auth, validation, response, and retry contracts pass backend tests. UI should expose cash in/out and counted close only to authorized roles, show a variance preview as advisory, and render the server's final expected cash and variance after close. Full close workflow, multi-register operations, offline operation, and production accounting reconciliation remain outside this slice until separately validated.
