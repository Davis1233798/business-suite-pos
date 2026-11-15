# POS P05 refund architecture

## Existing behavior

`POST /api/stations/pos/business` accepts `action:"refund"` with one `saleLineId`, quantity 1–1000, non-blank `reason` (maximum 200 UTF-8 bytes), and an `idempotencyKey`. The handler restricts refunds to an enabled `admin` grant with `scope:"back"`. It authorizes the sale line through the current organization, requires a completed sale and an **open original shift**, rejects quantities above the remaining sold quantity, computes the refund from the original discounted `line_total`, and checks that the cash refund does not exceed the original shift's current expected drawer balance. The liquidity check runs inside the same transaction as the refund, stock restoration, audit and idempotency writes; an insufficient-drawer rejection is durably reserved with `outcome:"not_applied"` before it is returned. The handler records `pos_refunds` and `pos_refund_lines` atomically. It does not permit a manager to refund a sale after its shift closes or move the refund onto a later shift.

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
- Generate an idempotency key for the exact line/quantity/reason request. Before POST, persist that request in `sessionStorage` under a key scoped by the authenticated organization and user IDs; no session token or credential is stored. If the storage write fails, do not submit the refund. Freeze the payload during an ambiguous network or server error, restore its quantity/reason after a same-tab reload, and allow retry with the same key even if receipt lookup is unavailable. Clear the pending request only after confirmed success or an explicit `error.outcome:"not_applied"` response. The server emits that marker only after atomically reserving the organization/endpoint/key with the rejected request hash and terminal response; same-payload retry replays the rejection, while a changed payload conflicts and cannot later commit under that key. Preserve the pending request on unmarked 4xx responses, including 409, because generic middleware/key conflicts do not prove the earlier attempt had no effect. Browser session storage is tab-scoped and does not recover the attempt in a new browser session.
- An unmarked 409 leaves the request pending and requires manual reconciliation of the receipt and refund records before a replacement attempt; this P05 slice has no control to clear or replace that pending key. Terminal no-effect rejections are stored in `idempotency_keys` without an expiration or cleanup policy in this slice; retention and long-term table growth have not been validated.
- On confirmed success, refresh the exact receipt detail. The returned quantity must increase stock once, and expected cash for the still-open original shift must subtract the refund exactly once. The daily report continues to attribute the refund to its own Taiwan-local refund date.

## Security and transaction invariants

- Only `admin` / `back` can look up or submit a refund. Front-office staff, back-office operators, read-only users, and users from another organization cannot use either path.
- A refund lookup and POST must resolve the sale, line, shift, and product under the caller's organization. A sale line from another organization, an unknown receipt, or a fabricated line ID must not reveal or mutate foreign data.
- Only completed sales on an open original shift are eligible. The original shift may close between lookup and POST; the POST must still reject without changing the line quantity, stock, refunds, audit, or idempotency result.
- Before restoring stock or incrementing refunded quantity, calculate the current cash drawer from opening cash, completed cash sales, cash-in/out movements, and prior cash refunds in the same transaction. If this refund would make expected cash negative, persist a terminal rejection for this exact idempotency key/payload and return 409 with `error.outcome:"not_applied"`; same-payload retries replay that rejection and changed payloads conflict. A different key may succeed only after the drawer has enough cash and the sale remains otherwise eligible.
- The conditional refunded-quantity update must continue to prevent two distinct requests from returning more than sold quantity. Stock, refunded quantity, refund header/line, audit, and idempotency record are one transaction; any failure rolls all of them back.
- Same-key/same-payload replay returns the original response and performs no second stock increment or refund. Same-key/different-payload returns conflict. A different key may proceed only if remaining quantity and the original shift still allow it.
- Refund reason is required, trimmed before storage, and bounded. Product stock is restored to the original item only; this design does not create a separate physical inspection, quarantine, resale-condition, or supplier-return workflow.

## Test-first plan

1. Add Go tests for the lookup projection, exact receipt resolution, 100-line bound, same-organization scope, and `admin`/`back` access. Assert no organization or account fields are returned. Check that a closed-shift sale is returned as ineligible and that an unknown/foreign receipt is not distinguishable.
2. Add Go refund tests before changing the handler: front operator, back operator, read-only, and foreign organization are rejected; non-cash, non-completed, missing-item, closed-original-shift, blank reason, invalid quantity, and over-refund requests leave all business and retry tables unchanged.
3. Verify cumulative discount allocation with a deliberately indivisible line total (101 over 3 units): partial requests return 33, 34, and 34; their sum is exactly 101, the original `line_total`, never gross-before-discount.
4. Verify same-key replay is one refund/stock increment, changed-payload replay conflicts, and two concurrent distinct refund requests cannot exceed the remaining quantity. Inject a transaction failure and assert line quantity, stock, refund header/line, audit, and idempotency all roll back.
5. Verify only the original open shift accepts a refund, closing it causes subsequent refunds to fail, the cash drawer never becomes negative when refunds race with cash-outs or close, and shift close expected cash subtracts each confirmed refund once. Use separate SQLite handles for refund-vs-cash-out and refund-vs-close races. Verify the daily report attributes refunds to the refund's Taipei date.
6. Add browser coverage for admin/back exact-receipt lookup, line selection and quantity bounds, required reason, confirmation, authoritative server receipt, and refresh after success. Cover a closed-shift/non-cash ineligible sale, non-admin hidden controls plus direct API denial, stale remaining quantity, and ambiguous same-payload retry.
7. Run focused Go refund tests, the full Go suite, cashier/refund Playwright specs, TypeScript, web build, targeted lint, and `git diff --check`. Use only a fresh isolated SQLite database for any live API/browser verification.

## Explicit exclusions

This work does not add refunds after the original shift is closed, move a refund to a new shift, process card or gateway reversals, create a generic multi-line refund transaction, inspect returned goods, add manager approval thresholds, or claim full P01–P08 or production acceptance. Those rules need separate operational and settlement decisions.
