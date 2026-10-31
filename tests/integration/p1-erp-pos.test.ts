import test from "node:test";
import assert from "node:assert";
import { getDb } from "../../db/index.ts";
import { seedOrganization } from "../../lib/seed.ts";
import { PosService } from "../../lib/services/pos-service.ts";
import { erpPurchaseOrders, erpPurchaseOrderLines, erpStockLedger, posCatalogItems, posSales } from "../../db/schema.ts";
import { eq, and, sql } from "drizzle-orm";

test("P1 POS Verification: P01 - P08", async () => {
  const orgId = `org_pos_test_${Date.now()}`;
  await seedOrganization(orgId);

  const adminCtx = {
    organizationId: orgId,
    userId: "usr_admin",
    userEmail: "admin@test.local",
    userDisplayName: "Store Manager",
    role: "admin" as const,
    requestId: "req_pos_admin",
  };

  const operatorCtx = {
    ...adminCtx,
    userId: "usr_cashier",
    role: "operator" as const,
    requestId: "req_pos_cashier",
  };

  const db = getDb();
  const storeId = `pos_store_01_${orgId}`;
  const regId = `pos_reg_01_${orgId}`;
  const shiftId = `pos_shift_01_${orgId}`;

  // P01: 掃描條碼取服務端價格
  const item = await PosService.scanBarcode(operatorCtx, "4710001001");
  assert.strictEqual(item.price, 100);

  // P02 & P03: 商品100x2，折10後190；付200找10
  const checkoutRes: any = await PosService.checkout(operatorCtx, {
    storeId,
    registerId: regId,
    shiftId,
    items: [{ barcode: "4710001001", quantity: 2, lineDiscount: 10 }],
    tenderAmount: 200,
    paymentMethod: "cash",
    idempotencyKey: "pos_idem_001",
  });

  assert.strictEqual(checkoutRes.subtotal, 200);
  assert.strictEqual(checkoutRes.discountAmount, 10);
  assert.strictEqual(checkoutRes.totalAmount, 190);
  assert.strictEqual(checkoutRes.changeAmount, 10);

  // Duplicate idempotency test (P03 / C06)
  const dupCheckoutRes: any = await PosService.checkout(operatorCtx, {
    storeId,
    registerId: regId,
    shiftId,
    items: [{ barcode: "4710001001", quantity: 2, lineDiscount: 10 }],
    tenderAmount: 200,
    paymentMethod: "cash",
    idempotencyKey: "pos_idem_001",
  });
  assert.ok(dupCheckoutRes);

  // P04: 掛單與跨工作站取單
  const held = await PosService.holdCart(operatorCtx, {
    storeId,
    cartName: "VIP客戶掛單A",
    items: [{ barcode: "4710001001", quantity: 1 }],
  });
  assert.ok(held.cartId);

  const retrieved = await PosService.retrieveCart(operatorCtx, held.cartId, "REG-02");
  assert.strictEqual(retrieved.cartName, "VIP客戶掛單A");

  // Attempt second retrieve -> must fail (P04)
  await assert.rejects(
    async () => {
      await PosService.retrieveCart(operatorCtx, held.cartId, "REG-03");
    },
    /已被其他工作站取走/
  );

  // P05: 退貨/退款 (需店長權限，分攤退款，超退拒絕)
  const saleList = await db.select().from(posSales)
    .where(and(eq(posSales.organizationId, orgId), eq(posSales.totalAmount, 190)))
    .limit(1);
  assert.strictEqual(saleList.length, 1);
  const sale = saleList[0];

  const lines = await PosService.reprintReceipt(operatorCtx, sale.receiptNumber);
  const saleLine = lines.lines[0];

  // Cashier attempt refund -> must fail (needs manager)
  await assert.rejects(
    async () => {
      await PosService.refundSale(operatorCtx, {
        saleId: sale.id,
        lineReturns: [{ lineId: saleLine.id, quantity: 1 }],
        reason: "客人不需要",
      });
    },
    /退款必須具有店長或管理員權限/
  );

  // Manager refunds 1 piece (original 2 pieces for 190 -> 95 per piece)
  const refundRes = await PosService.refundSale(adminCtx, {
    saleId: sale.id,
    lineReturns: [{ lineId: saleLine.id, quantity: 1 }],
    reason: "客人換貨退款",
  });
  assert.strictEqual(refundRes.totalRefundAmount, 95);

  // Cumulative over-refund attempt -> must reject (P05)
  await assert.rejects(
    async () => {
      await PosService.refundSale(adminCtx, {
        saleId: sale.id,
        lineReturns: [{ lineId: saleLine.id, quantity: 2 }], // only 1 left refundable
        reason: "多退",
      });
    },
    /累計超退拒絕/
  );

  // P06: 交班現金盤點 (開班1000 + 現金銷售190 - 退款95 = 1095)
  // Let's close with 1085 (discrepancy -10), must require notes!
  await assert.rejects(
    async () => {
      await PosService.closeShift(operatorCtx, {
        shiftId,
        closingCash: 1085,
      });
    },
    /結班必須填寫說明/
  );

  const closeRes = await PosService.closeShift(operatorCtx, {
    shiftId,
    closingCash: 1085,
    notes: "找零硬幣微幅短少10元",
  });
  assert.strictEqual(closeRes.discrepancy, -10);

  // P07: 小票重印標記
  const reprint = await PosService.reprintReceipt(operatorCtx, sale.receiptNumber);
  assert.strictEqual(reprint.isReprint, true);

  // P08: 日報 (Net = Sales - Refunds)
  const report = await PosService.getDailyReport(operatorCtx, storeId);
  assert.strictEqual(report.grossSales >= 190, true);
  assert.strictEqual(report.totalRefunds >= 95, true);
  assert.strictEqual(report.netSales, report.grossSales - report.totalRefunds);
});
