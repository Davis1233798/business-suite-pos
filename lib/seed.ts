import { eq } from "drizzle-orm";
import { getDb } from "../db/index";
import {
  organizations,
  demoSeedStates,
  users,
  memberships,
  posStores,
  posRegisters,
  posShifts,
  posCatalogItems,
} from "../db/schema";

// Fictional fixtures only: no real brands, personal phone numbers or delivery addresses.
export async function seedOrganization(orgId: string = "org_demo_default") {
  const db = getDb();

  // 1. Organization & Core Users
  const existingOrg = await db
    .select()
    .from(organizations)
    .where(eq(organizations.id, orgId))
    .limit(1);

  if (existingOrg.length === 0) {
    await db.insert(organizations).values({
      id: orgId,
      name: "示範企業 (Business Suite Enterprise)",
      slug: orgId,
    }).onConflictDoNothing();
  }

  // Users
  const adminUser = { id: `usr_${orgId}_admin`, email: "admin@suite.test", displayName: "示範管理員 (Admin)" };
  const operatorUser = { id: `usr_${orgId}_op`, email: "operator@suite.test", displayName: "示範作業員 / 現場主管" };
  const readonlyUser = { id: `usr_${orgId}_ro`, email: "guest@suite.test", displayName: "訪客 (唯讀)" };

  for (const u of [adminUser, operatorUser, readonlyUser]) {
    const exists = await db.select().from(users).where(eq(users.id, u.id)).limit(1);
    if (exists.length === 0) {
      await db.insert(users).values(u).onConflictDoNothing();
    }
  }

  // Memberships
  const memberRoles = [
    { userId: adminUser.id, role: "admin" },
    { userId: operatorUser.id, role: "operator" },
    { userId: readonlyUser.id, role: "readonly" },
  ];
  for (const m of memberRoles) {
    const memId = `mem_${orgId}_${m.userId}`;
    const exists = await db.select().from(memberships).where(eq(memberships.id, memId)).limit(1);
    if (exists.length === 0) {
      await db.insert(memberships).values({
        id: memId,
        organizationId: orgId,
        userId: m.userId,
        role: m.role,
      }).onConflictDoNothing();
    }
  }

  // ==========================================
  // 4. POS「CounterPoint」Seed
  // ==========================================
  const store1 = { id: `pos_store_01_${orgId}`, organizationId: orgId, code: "STORE-XY", name: "示範門市 A（虛構）", address: "示範地址 A（非實際地址）" };
  await db.insert(posStores).values(store1).onConflictDoNothing();

  const reg1 = { id: `pos_reg_01_${orgId}`, organizationId: orgId, storeId: store1.id, registerNumber: "REG-01", name: "1號收銀櫃檯", isActive: true };
  await db.insert(posRegisters).values(reg1).onConflictDoNothing();

  await db.insert(posShifts).values({
    id: `pos_shift_01_${orgId}`,
    organizationId: orgId,
    storeId: store1.id,
    registerId: reg1.id,
    cashierId: operatorUser.id,
    openingCash: 1000, // 1000 cash float
    status: "open",
  }).onConflictDoNothing();

  await db.insert(posCatalogItems).values([
    { id: `pos_item_01_${orgId}`, organizationId: orgId, barcode: "4710001001", name: "精品冷萃美式咖啡 (大杯)", price: 100, stockQuantity: 100, isActive: true },
    { id: `pos_item_02_${orgId}`, organizationId: orgId, barcode: "4710001002", name: "手作黑糖肉桂捲", price: 90, stockQuantity: 50, isActive: true },
    { id: `pos_item_03_${orgId}`, organizationId: orgId, barcode: "4710001003", name: "衣索比亞莊園濾掛禮盒", price: 350, stockQuantity: 20, isActive: true },
  ]).onConflictDoNothing();

  await db.insert(demoSeedStates).values({ organizationId: orgId }).onConflictDoNothing();
  return { success: true, organizationId: orgId };
}
