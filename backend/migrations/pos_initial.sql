CREATE TABLE `audit_events` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`actor_id` text NOT NULL,
	`actor_role` text NOT NULL,
	`action` text NOT NULL,
	`entity_type` text NOT NULL,
	`entity_id` text NOT NULL,
	`before_value` text,
	`after_value` text,
	`reason` text,
	`request_id` text,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `idempotency_keys` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`key` text NOT NULL,
	`endpoint` text NOT NULL,
	`request_hash` text NOT NULL,
	`response_status` integer NOT NULL,
	`response_body` text NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `memberships` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`user_id` text NOT NULL,
	`role` text DEFAULT 'operator' NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `organizations` (
	`id` text PRIMARY KEY NOT NULL,
	`name` text NOT NULL,
	`slug` text NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE UNIQUE INDEX `organizations_slug_unique` ON `organizations` (`slug`);
--> statement-breakpoint
CREATE TABLE `outbox_events` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`event_type` text NOT NULL,
	`payload` text NOT NULL,
	`status` text DEFAULT 'pending' NOT NULL,
	`retry_count` integer DEFAULT 0 NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `pos_cash_movements` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`shift_id` text NOT NULL,
	`movement_type` text NOT NULL,
	`amount` integer NOT NULL,
	`reason` text NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `pos_catalog_items` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`barcode` text NOT NULL,
	`name` text NOT NULL,
	`price` integer NOT NULL,
	`stock_quantity` integer DEFAULT 0 NOT NULL,
	`is_active` integer DEFAULT true NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `pos_held_carts` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`store_id` text NOT NULL,
	`cart_name` text NOT NULL,
	`items_json` text NOT NULL,
	`status` text DEFAULT 'held' NOT NULL,
	`locked_by` text,
	`held_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `pos_refund_lines` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`refund_id` text NOT NULL,
	`sale_line_id` text NOT NULL,
	`quantity` integer NOT NULL,
	`amount` integer NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `pos_refunds` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`refund_number` text NOT NULL,
	`original_sale_id` text NOT NULL,
	`manager_id` text NOT NULL,
	`reason` text NOT NULL,
	`refund_amount` integer NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `pos_registers` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`store_id` text NOT NULL,
	`register_number` text NOT NULL,
	`name` text NOT NULL,
	`is_active` integer DEFAULT true NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `pos_sale_lines` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`sale_id` text NOT NULL,
	`item_id` text NOT NULL,
	`barcode` text NOT NULL,
	`name` text NOT NULL,
	`unit_price` integer NOT NULL,
	`quantity` integer NOT NULL,
	`discount_amount` integer DEFAULT 0 NOT NULL,
	`line_total` integer NOT NULL,
	`refunded_quantity` integer DEFAULT 0 NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `pos_sales` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`receipt_number` text NOT NULL,
	`store_id` text NOT NULL,
	`register_id` text NOT NULL,
	`shift_id` text NOT NULL,
	`cashier_id` text NOT NULL,
	`customer_id` text,
	`subtotal` integer NOT NULL,
	`discount_amount` integer DEFAULT 0 NOT NULL,
	`tax_amount` integer DEFAULT 0 NOT NULL,
	`total_amount` integer NOT NULL,
	`tender_amount` integer NOT NULL,
	`change_amount` integer DEFAULT 0 NOT NULL,
	`payment_method` text DEFAULT 'cash' NOT NULL,
	`status` text DEFAULT 'completed' NOT NULL,
	`is_reprint` integer DEFAULT false NOT NULL,
	`idempotency_key` text,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `pos_shifts` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`store_id` text NOT NULL,
	`register_id` text NOT NULL,
	`cashier_id` text NOT NULL,
	`opening_cash` integer NOT NULL,
	`closing_cash` integer,
	`expected_cash` integer,
	`cash_discrepancy` integer,
	`status` text DEFAULT 'open' NOT NULL,
	`opened_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL,
	`closed_at` text,
	`notes` text
);
--> statement-breakpoint
CREATE TABLE `pos_stores` (
	`id` text PRIMARY KEY NOT NULL,
	`organization_id` text NOT NULL,
	`code` text NOT NULL,
	`name` text NOT NULL,
	`address` text,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
--> statement-breakpoint
CREATE TABLE `users` (
	`id` text PRIMARY KEY NOT NULL,
	`email` text NOT NULL,
	`display_name` text NOT NULL,
	`created_at` text DEFAULT CURRENT_TIMESTAMP NOT NULL
);
