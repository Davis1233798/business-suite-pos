# POS cashier UI slice

## Architecture

The Go API remains authoritative for product state, prices, shift ownership, cash totals, inventory, audit records, and idempotency. The React POS front-office route uses the authenticated same-origin API: a limited terminal-context read, barcode lookup, shift open, and the existing cart checkout operation. It skips the generic station dataset request. The browser keeps only a temporary basket and displays the server's receipt result. This slice does not use the old Vinext POS API or introduce new product pricing behavior.

## Boundary and failure behavior

- The terminal read returns active registers for the current organization and only open shifts owned by the current user. It does not expose another cashier's open shift or back-office dataset.
- The cashier shell preserves existing role-gated navigation: back-office-scoped accounts retain operations and print-center links, administrators retain account/permission management, and front-office-only accounts do not see those links.
- Product lookup resolves an active barcode and current stock/price from Go. Repeated scans increment that item in the in-memory basket.
- Opening a shift and checkout retain the existing Go permission checks. Checkout accepts item IDs, quantities, and line discounts; Go re-reads product details and stock inside its transaction.
- The basket displays listed-price subtotal, per-line fixed-amount discounts, net due, cash tender, and estimated change. Discount values are submitted as `discountAmount`; Go validates them against its current server-side price. There are no campaign rules or manager approval in this slice.
- Cashier controls, status messages, errors, and receipts use the existing English/Traditional Chinese `tr()` catalog and honor the saved `suite-language` selection when the route opens.
- Shift opening and checkout use stable idempotency keys. On an ambiguous network/server result, the UI freezes the request inputs and allows replay with the same key; confirmed success or a definitive client error resolves the pending operation.
- Checkout posts the existing `pos/cart` contract with item IDs, quantities, fixed line discounts, cash tender, and a stable idempotency key for retries. The backend persists sale, lines, stock deductions, audit, and idempotency atomically. A successful sale clears the basket and shows server-returned receipt totals.
- A changed catalog price can make the displayed estimate stale. The server revalidates before committing; definitive validation errors keep the basket for correction, and successful receipts show only server-returned totals and change.

## Test-first acceptance

1. Go terminal-context tests prove same-organization register listing, own-open-shift visibility, and no cross-user shift disclosure.
2. Browser cashier test retries an ambiguous shift-open result with the same key, then scans one barcode twice, applies a 10-unit line discount, tenders 200 for a 190 total, and checks the server receipt and change of 10.
3. Browser tests verify the saved locale, role-gated navigation, and that read-only users cannot write.
4. Run `npm run go:test`, `npm test`, `npx tsc --noEmit`, the focused Playwright cashier spec, and `npm run web:build`.

This slice does not claim all P01-P08 acceptance, production deployment, or legacy data migration.
