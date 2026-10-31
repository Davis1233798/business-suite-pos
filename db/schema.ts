import { sql } from "drizzle-orm";
import { integer, sqliteTable, text, uniqueIndex } from "drizzle-orm/sqlite-core";

export const stationAccounts = sqliteTable('station_accounts', {
  userId: text('user_id').primaryKey(), organizationId: text('organization_id').notNull(),
  email: text('email').notNull(), passwordHash: text('password_hash').notNull(), disabled: integer('disabled').notNull().default(0),
}, t=>[uniqueIndex('station_accounts_email').on(t.organizationId,t.email)]);
export const stationGrants = sqliteTable('station_grants', {
  userId: text('user_id').notNull(), site: text('site').notNull(), role: text('role').notNull(),
  scope: text('scope').notNull(), enabled: integer('enabled').notNull().default(1),
},t=>[uniqueIndex('station_grants_user_site').on(t.userId,t.site)]);
export const stationSessions = sqliteTable('station_sessions', {
  tokenHash:text('token_hash').primaryKey(),userId:text('user_id').notNull(),expiresAt:integer('expires_at').notNull(),
});
export const stationLoginLimits = sqliteTable('station_login_limits', {
  key:text('key').primaryKey(),attempts:integer('attempts').notNull(),resetAt:integer('reset_at').notNull(),
});

// ==========================================
// 1. Common & Tenant Tables
// ==========================================

export const organizations = sqliteTable("organizations", {
  id: text("id").primaryKey(),
  name: text("name").notNull(),
  slug: text("slug").notNull().unique(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const users = sqliteTable("users", {
  id: text("id").primaryKey(),
  email: text("email").notNull(),
  displayName: text("display_name").notNull(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const memberships = sqliteTable("memberships", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  userId: text("user_id").notNull(),
  role: text("role").notNull().default("operator"), // admin, operator, readonly
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const auditEvents = sqliteTable("audit_events", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  actorId: text("actor_id").notNull(),
  actorRole: text("actor_role").notNull(),
  action: text("action").notNull(), // UPDATE_AMOUNT, UPDATE_ROLE, DEDUCT_STOCK, REFUND, etc.
  entityType: text("entity_type").notNull(),
  entityId: text("entity_id").notNull(),
  beforeValue: text("before_value"),
  afterValue: text("after_value"),
  reason: text("reason"),
  requestId: text("request_id"),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const idempotencyKeys = sqliteTable("idempotency_keys", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  key: text("key").notNull(),
  endpoint: text("endpoint").notNull(),
  requestHash: text("request_hash").notNull(),
  responseStatus: integer("response_status").notNull(),
  responseBody: text("response_body").notNull(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const outboxEvents = sqliteTable("outbox_events", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  eventType: text("event_type").notNull(),
  payload: text("payload").notNull(),
  status: text("status").notNull().default("pending"), // pending, processed, failed
  retryCount: integer("retry_count").notNull().default(0),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

// ==========================================
// 2. MES「ForgeFlow」Tables
// ==========================================

export const mesWorkOrders = sqliteTable("mes_work_orders", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  orderNo: text("order_no").notNull(),
  productId: text("product_id").notNull(),
  bomVersionId: text("bom_version_id").notNull(),
  targetQuantity: integer("target_quantity").notNull(),
  completedQuantity: integer("completed_quantity").notNull().default(0),
  scrappedQuantity: integer("scrapped_quantity").notNull().default(0),
  status: text("status").notNull().default("draft"), // draft, released, in_progress, completed, closed
  materialsStaged: integer("materials_staged", { mode: "boolean" }).notNull().default(false),
  scheduledMinutes: integer("scheduled_minutes").notNull().default(480),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const mesOperations = sqliteTable("mes_operations", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  workOrderId: text("work_order_id").notNull(),
  sequence: integer("sequence").notNull(),
  workCenterId: text("work_center_id").notNull(),
  name: text("name").notNull(),
  standardMinutesPerUnit: integer("standard_minutes_per_unit").notNull().default(1),
  status: text("status").notNull().default("pending"), // pending, running, completed
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const mesQualityChecks = sqliteTable("mes_quality_checks", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  workOrderId: text("work_order_id").notNull(),
  reportId: text("report_id"),
  inspectorId: text("inspector_id").notNull(),
  sampleQuantity: integer("sample_quantity").notNull(),
  passedQuantity: integer("passed_quantity").notNull(),
  failedQuantity: integer("failed_quantity").notNull(),
  status: text("status").notNull().default("passed"), // passed, quarantined, reworked
  disposition: text("disposition"),
  notes: text("notes"),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const erpItems = sqliteTable("erp_items", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  sku: text("sku").notNull(),
  name: text("name").notNull(),
  unit: text("unit").notNull().default("PCS"),
  boxConversionRatio: integer("box_conversion_ratio").notNull().default(1),
  category: text("category").notNull().default("general"),
  isActive: integer("is_active", { mode: "boolean" }).notNull().default(true),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const erpPurchaseOrders = sqliteTable("erp_purchase_orders", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  poNumber: text("po_number").notNull(),
  supplierId: text("supplier_id").notNull(),
  status: text("status").notNull().default("draft"), // draft, approved, rejected, fulfilled
  totalAmount: integer("total_amount").notNull().default(0), // cents
  rejectedReason: text("rejected_reason"),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const erpPurchaseOrderLines = sqliteTable("erp_purchase_order_lines", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  poId: text("po_id").notNull(),
  itemId: text("item_id").notNull(),
  quantity: integer("quantity").notNull(),
  unitPrice: integer("unit_price").notNull(), // cents
  receivedQuantity: integer("received_quantity").notNull().default(0),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const erpStockLedger = sqliteTable("erp_stock_ledger", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  itemId: text("item_id").notNull(),
  warehouseId: text("warehouse_id").notNull(),
  changeType: text("change_type").notNull(), // purchase_receipt, purchase_return, sales_shipment, transfer_in, transfer_out, adjustment
  quantityChange: integer("quantity_change").notNull(),
  balanceAfter: integer("balance_after").notNull(),
  unitCost: integer("unit_cost").notNull().default(0),
  totalValue: integer("total_value").notNull().default(0),
  referenceDoc: text("reference_doc").notNull(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const posStores = sqliteTable("pos_stores", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  code: text("code").notNull(),
  name: text("name").notNull(),
  address: text("address"),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const posRegisters = sqliteTable("pos_registers", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  storeId: text("store_id").notNull(),
  registerNumber: text("register_number").notNull(),
  name: text("name").notNull(),
  isActive: integer("is_active", { mode: "boolean" }).notNull().default(true),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const posShifts = sqliteTable("pos_shifts", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  storeId: text("store_id").notNull(),
  registerId: text("register_id").notNull(),
  cashierId: text("cashier_id").notNull(),
  openingCash: integer("opening_cash").notNull(),
  closingCash: integer("closing_cash"),
  expectedCash: integer("expected_cash"),
  cashDiscrepancy: integer("cash_discrepancy"),
  status: text("status").notNull().default("open"), // open, closed
  openedAt: text("opened_at").notNull().default(sql`CURRENT_TIMESTAMP`),
  closedAt: text("closed_at"),
  notes: text("notes"),
});

export const posCatalogItems = sqliteTable("pos_catalog_items", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  barcode: text("barcode").notNull(),
  name: text("name").notNull(),
  price: integer("price").notNull(),
  stockQuantity: integer("stock_quantity").notNull().default(0),
  isActive: integer("is_active", { mode: "boolean" }).notNull().default(true),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const posHeldCarts = sqliteTable("pos_held_carts", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  storeId: text("store_id").notNull(),
  cartName: text("cart_name").notNull(),
  itemsJson: text("items_json").notNull(),
  status: text("status").notNull().default("held"), // held, retrieved, cancelled
  lockedBy: text("locked_by"),
  heldAt: text("held_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const posSales = sqliteTable("pos_sales", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  receiptNumber: text("receipt_number").notNull(),
  storeId: text("store_id").notNull(),
  registerId: text("register_id").notNull(),
  shiftId: text("shift_id").notNull(),
  cashierId: text("cashier_id").notNull(),
  customerId: text("customer_id"),
  subtotal: integer("subtotal").notNull(),
  discountAmount: integer("discount_amount").notNull().default(0),
  taxAmount: integer("tax_amount").notNull().default(0),
  totalAmount: integer("total_amount").notNull(),
  tenderAmount: integer("tender_amount").notNull(),
  changeAmount: integer("change_amount").notNull().default(0),
  paymentMethod: text("payment_method").notNull().default("cash"),
  status: text("status").notNull().default("completed"), // completed, refunded, partially_refunded
  isReprint: integer("is_reprint", { mode: "boolean" }).notNull().default(false),
  idempotencyKey: text("idempotency_key"),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const posSaleLines = sqliteTable("pos_sale_lines", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  saleId: text("sale_id").notNull(),
  itemId: text("item_id").notNull(),
  barcode: text("barcode").notNull(),
  name: text("name").notNull(),
  unitPrice: integer("unit_price").notNull(),
  quantity: integer("quantity").notNull(),
  discountAmount: integer("discount_amount").notNull().default(0),
  lineTotal: integer("line_total").notNull(),
  refundedQuantity: integer("refunded_quantity").notNull().default(0),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const posRefunds = sqliteTable("pos_refunds", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  refundNumber: text("refund_number").notNull(),
  originalSaleId: text("original_sale_id").notNull(),
  managerId: text("manager_id").notNull(),
  reason: text("reason").notNull(),
  refundAmount: integer("refund_amount").notNull(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const posRefundLines = sqliteTable("pos_refund_lines", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  refundId: text("refund_id").notNull(),
  saleLineId: text("sale_line_id").notNull(),
  quantity: integer("quantity").notNull(),
  amount: integer("amount").notNull(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const posCashMovements = sqliteTable("pos_cash_movements", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  shiftId: text("shift_id").notNull(),
  movementType: text("movement_type").notNull(), // pay_in, pay_out
  amount: integer("amount").notNull(),
  reason: text("reason").notNull(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

// ==========================================
// 5. CRM「RelationDesk」Tables
// ==========================================

export const shopOrders = sqliteTable("shop_orders", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  orderNumber: text("order_number").notNull(),
  userId: text("user_id"),
  customerEmail: text("customer_email").notNull(),
  shippingAddressJson: text("shipping_address_json").notNull(),
  subtotal: integer("subtotal").notNull(),
  discountAmount: integer("discount_amount").notNull().default(0),
  shippingFee: integer("shipping_fee").notNull().default(0),
  totalAmount: integer("total_amount").notNull(),
  paymentStatus: text("payment_status").notNull().default("pending"), // pending, paid, failed, refunded
  fulfillmentStatus: text("fulfillment_status").notNull().default("unfulfilled"), // unfulfilled, partially_shipped, shipped, delivered
  trackingNumber: text("tracking_number"),
  idempotencyKey: text("idempotency_key"),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const demoSeedStates = sqliteTable("demo_seed_states", { organizationId: text("organization_id").primaryKey(), readyAt: text("ready_at").notNull().default(sql`CURRENT_TIMESTAMP`) });
