# POS cart line discount boundary

Cart checkout accepts an optional integer `discountAmount` on each line. It is a requested absolute discount for the entire line, in the same currency minor units as the catalog price. The server remains authoritative for product prices and computes each line's gross amount from the current catalog price and requested quantity.

The server rejects negative discounts and any line discount greater than that line's gross amount. Sale subtotal is the sum of gross line amounts, sale discount is the sum of line discounts, and sale total is subtotal minus discount. The stored `line_total` is the net line amount after discount; `discount_amount` records the line discount. Tender must cover the net sale total, and change is calculated from that total. Existing requests that omit discounts continue to produce zero discount and unchanged totals.

For a held cart, checkout uses the line data stored when the cart was held; discount fields supplied separately at checkout are not authoritative. Held-cart line discounts are persisted with the cart and checked again against current catalog prices at checkout. The existing transaction continues to cover stock deduction, sale and line creation, cart completion, audit, and idempotency recording. The request fingerprint includes the requested line discounts, so retrying a key with changed discounts conflicts.

This boundary validates arithmetic and preserves the existing POS authorization rules. It does not add manager approval, discount reasons, campaign eligibility, or a configurable discount ceiling; those require a separate policy and API design.
