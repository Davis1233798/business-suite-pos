# POS P05 refund architecture

## Existing behavior

`POST /api/stations/pos/business` accepts `action:"refund"` with one `saleLineId`, positive `quantity`, non-blank `reason` (maximum 200 characters), and an `idempotencyKey`. The handler already restricts refunds to an enabled `admin` grant with `scope:"back"`. It authorizes the sale line through the current organization, requires a completed sale and an **open original shift**, rejects quantities above the remaining sold quantity, computes the refund from the original discounted `line_total`, restores the original catalog item's stock, and records `pos_refunds`, `pos_refund_lines`, an audit event, and idempotency state in one transaction. This P05 slice preserves the open-original-shift rule. It does not permit a manager to refund a sale after its shift closes or move the refund onto a later shift.

Refund allocation is cumulative integer apportionment:

```text
refund amount for this request
  = floor(line_total * (already_refunded + requested) / sold_quantity)
  - floor(line_total * already_refunded / sold_quantity)
```

This makes a complete return sum exactly to the original line's net amount, including its line discount, regardless of how the quantity is split among requests. The server remains authoritative for the final refund amount. Do not calculate a submitted refund amount in React.

## P05 delivery boundary

- The backend in this worktree now exposes `GET /api/stations/pos/refund-lookup?receiptNumber=...`. It requires an enabled `admin` / `back` grant and uses a purpose-built exact-receipt lookup instead of depending on the generic dataset's 500-row window.
- The lookup response is `{saleId,receiptNumber,saleStatus,paymentMethod,shiftStatus,eligible,lines}` in the standard `data` envelope. Each of at most 100 line entries contains `saleLineId`, `itemId`, `name`, `barcode`, `quantity`, `refundedQuantity`, `remainingQuantity`, and `lineTotal`. No organization IDs, cashier IDs, or raw account fields are returned. The lookup scopes every query by the authenticated organization and uses the receipt number only as a lookup key, never as authority.
- A known completed sale is returned with `eligible:false` if the original shift is closed, its payment is non-cash, or no line has a remaining quantity. This lets the administrator see why it cannot be refunded. Unknown and foreign-organization receipts both return 404. Duplicate receipt numbers and sales with more than 100 lines return 409 instead of choosing an arbitrary or unbounded projection. The POST remains authoritative and rejects a shift that closed after lookup.
- This slice supports cash sales from the Go POS flow. The Go cashier creates cash sales and the refund handler does not perform a card-provider reversal or track a separate payout method. The lookup marks non-cash sales ineligible and the refund POST rejects them. Do not silently treat a card refund as cash or restore stock without a supported refund path.
- One POST refunds one sale line and one quantity. Multi-line returns are outside this slice; the UI may submit a separate, clearly reviewed request for another line after the first has a confirmed result.
- The UI shows remaining refundable quantity and a server-derived estimate based on lookup values, labelled as an estimate. Before submission it confirms the selected line, quantity, reason, and estimated amount. The success result and any receipt show the server's final `refundNumber`, `refundAmount`, and `quantity`.
- Generate an idempotency key for the exact line/quantity/reason request. Before POST, persist that request in `sessionStorage` under a key scoped by the authenticated organization and user IDs; no session token or credential is stored. If the storage write fails, do not submit the refund. Freeze the payload during an ambiguous network or 5xx response, restore its quantity/reason after a same-tab reload, and allow retry with the same key even if receipt lookup is unavailable. Clear the pending request only after confirmed success or a definitive 4xx rejection. On a stale-quantity/closed-shift conflict, reload the exact receipt and explain the current server state; do not retry with a changed payload under the old key. Browser session storage is tab-scoped and does not recover the attempt in a new browser session.
- On confirmed success, refresh the exact receipt detail. The returned quantity must increase stock once, and expected cash for the still-open original shift must subtract the refund exactly once. The daily report continues to attribute the refund to its own Taiwan-local refund date.

## Security and transaction invariants

- Only `admin` / `back` can look up or submit a refund. Front-office staff, back-office operators, read-only users, and users from another organization cannot use either path.
- A refund lookup and POST must resolve the sale, line, shift, and product under the caller's organization. A sale line from another organization, an unknown receipt, or a fabricated line ID must not reveal or mutate foreign data.
- Only completed sales on an open original shift are eligible. The original shift may close between lookup and POST; the POST must still reject without changing the line quantity, stock, refunds, audit, or idempotency result.
- The conditional refunded-quantity update must continue to prevent two distinct requests from returning more than sold quantity. Stock, refunded quantity, refund header/line, audit, and idempotency record are one transaction; any failure rolls all of them back.
- Same-key/same-payload replay returns the original response and performs no second stock increment or refund. Same-key/different-payload returns conflict. A different key may proceed only if remaining quantity and the original shift still allow it.
- Refund reason is required, trimmed before storage, and bounded. Product stock is restored to the original item only; this design does not create a separate physical inspection, quarantine, resale-condition, or supplier-return workflow.

## Test-first plan

1. Add Go tests for the lookup projection, exact receipt resolution, 100-line bound, same-organization scope, and `admin`/`back` access. Assert no organization or account fields are returned. Check that a closed-shift sale is returned as ineligible and that an unknown/foreign receipt is not distinguishable.
2. Add Go refund tests before changing the handler: front operator, back operator, read-only, and foreign organization are rejected; non-cash, non-completed, missing-item, closed-original-shift, blank reason, invalid quantity, and over-refund requests leave all business and retry tables unchanged.
3. Verify cumulative discount allocation with a deliberately indivisible line total (101 over 3 units): partial requests return 33, 34, and 34; their sum is exactly 101, the original `line_total`, never gross-before-discount.
4. Verify same-key replay is one refund/stock increment, changed-payload replay conflicts, and two concurrent distinct refund requests cannot exceed the remaining quantity. Inject a transaction failure and assert line quantity, stock, refund header/line, audit, and idempotency all roll back.
5. Verify only the original open shift accepts a refund, closing it causes subsequent refunds to fail, and shift close expected cash subtracts each confirmed refund once. Verify the daily report attributes refunds to the refund's Taipei date.
6. Add browser coverage for admin/back exact-receipt lookup, line selection and quantity bounds, required reason, confirmation, authoritative server receipt, and refresh after success. Cover a closed-shift/non-cash ineligible sale, non-admin hidden controls plus direct API denial, stale remaining quantity, and ambiguous same-payload retry.
7. Run focused Go refund tests, the full Go suite, cashier/refund Playwright specs, TypeScript, web build, targeted lint, and `git diff --check`. Use only a fresh isolated SQLite database for any live API/browser verification.

## Explicit exclusions

This work does not add refunds after the original shift is closed, move a refund to a new shift, process card or gateway reversals, create a generic multi-line refund transaction, inspect returned goods, add manager approval thresholds, or claim full P01–P08 or production acceptance. Those rules need separate operational and settlement decisions.
