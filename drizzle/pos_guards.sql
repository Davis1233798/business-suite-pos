CREATE UNIQUE INDEX IF NOT EXISTS idempotency_scope_unique ON idempotency_keys(organization_id, key, endpoint);
--> statement-breakpoint
CREATE TRIGGER IF NOT EXISTS pos_stock_nonnegative BEFORE UPDATE OF stock_quantity ON pos_catalog_items WHEN NEW.stock_quantity < 0 BEGIN SELECT RAISE(ABORT, 'Insufficient stock'); END;
--> statement-breakpoint
CREATE TRIGGER IF NOT EXISTS pos_refund_limit BEFORE UPDATE OF refunded_quantity ON pos_sale_lines WHEN NEW.refunded_quantity < 0 OR NEW.refunded_quantity > NEW.quantity BEGIN SELECT RAISE(ABORT, 'Refund quantity exceeds original sale'); END;
--> statement-breakpoint
CREATE UNIQUE INDEX IF NOT EXISTS one_open_shift_per_register ON pos_shifts(organization_id, register_id) WHERE status='open';
--> statement-breakpoint
CREATE TABLE IF NOT EXISTS demo_seed_states (organization_id TEXT PRIMARY KEY NOT NULL, ready_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
