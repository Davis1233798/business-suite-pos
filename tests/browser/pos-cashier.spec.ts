import { test, expect } from '@playwright/test';

const register = { id: 'register-a', name: 'Register A', storeId: 'store-a' };
const product = { id: 'item-coffee', name: 'Cold brew coffee', barcode: '4710001001', price: 100, stockQuantity: 100 };

async function cashier(page: import('@playwright/test').Page, role: 'operator' | 'readonly' | 'admin' = 'operator', scope: 'front' | 'back' = 'front', language: 'en' | 'zh-TW' = 'en', userId = 'cashier-a') {
  await page.addInitScript(selectedLanguage => {
    if (!localStorage.getItem('suite-language')) localStorage.setItem('suite-language', selectedLanguage);
  }, language);
  await page.route('**/api/auth/me?site=pos', route => route.fulfill({ json: { success: true, data: {
    userId, organizationId: 'org-a', name: 'Cashier A', email: 'cashier@example.test',
    grants: [{ site: 'pos', role, scope, enabled: true }],
  } } }));
  await page.route('**/api/stations/pos/data?view=front', route => route.fulfill({ json: { success: true, data: {
    catalogItems: [], sales: [],
  } } }));
  await page.goto('/pos/front');
  await expect(page.getByRole('heading', { name: language === 'zh-TW' ? '收銀' : 'Cashier' })).toBeVisible();
}

async function posBackOffice(page: import('@playwright/test').Page, role: 'admin' | 'operator' | 'readonly' = 'admin', scope: 'back' | 'front' = 'back', language: 'en' | 'zh-TW' = 'en') {
  await page.addInitScript(selectedLanguage => localStorage.setItem('suite-language', selectedLanguage), language);
  await page.route('**/api/auth/me?site=pos', route => route.fulfill({ json: { success: true, data: {
    userId: 'manager-a', organizationId: 'org-a', name: 'Manager', email: 'manager@example.test',
    grants: [{ site: 'pos', role, scope, enabled: true }],
  } } }));
  await page.route('**/api/stations/pos/data?view=back', route => route.fulfill({ json: { success: true, data: {
    catalogItems: [], sales: [], saleLines: [], stores: [], registers: [], shifts: [], heldCarts: [],
  } } }));
  await page.goto('/pos/refunds');
}

const refundSale = {
  saleId: 'sale-a', receiptNumber: 'POS-REFUND-001', saleStatus: 'completed', paymentMethod: 'cash', shiftStatus: 'open', eligible: true,
  lines: [{ saleLineId: 'line-a', itemId: 'item-a', name: 'Coffee', barcode: '4710001001', quantity: 3, refundedQuantity: 0, remainingQuantity: 3, lineTotal: 101 }],
};

test('cashier opens a shift, scans twice, applies a fixed line discount, and retries checkout with the same key', async ({ page }) => {
  let openShifts: Array<{ id: string; registerId: string; status: string }> = [];
  const openRequests: Record<string, unknown>[] = [];
  const frontDataRequests: string[] = [];
  page.on('request', request => { if (request.url().includes('/api/stations/pos/data?view=front')) frontDataRequests.push(request.url()); });
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [register], openShifts } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: [] } }));
  await page.route('**/api/stations/pos/barcode?**', route => route.fulfill({ json: { success: true, data: product } }));
  await page.route('**/api/stations/pos/business', async route => {
    const body = route.request().postDataJSON();
    expect(body).toMatchObject({ action: 'open', registerId: register.id, openingCash: 1000 });
    openRequests.push(body);
    if (openRequests.length === 1) {
      await route.fulfill({ status: 503, json: { success: false, error: { message: 'Temporary shift failure' } } });
      return;
    }
    openShifts = [{ id: 'shift-a', registerId: register.id, status: 'open' }];
    await route.fulfill({ json: { success: true, data: { shiftId: 'shift-a', status: 'open' } } });
  });
  const checkoutRequests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/cart', async route => {
    checkoutRequests.push(route.request().postDataJSON());
    if (checkoutRequests.length === 1) {
      await route.fulfill({ status: 503, json: { success: false, error: { message: 'Temporary checkout failure' } } });
      return;
    }
    await route.fulfill({ json: { success: true, data: {
      saleId: 'sale-a', receiptNumber: 'POS-RECEIPT-A', subtotal: 200, discountAmount: 10,
      totalAmount: 190, changeAmount: 10,
    } } });
  });

  await cashier(page);
  await expect(page.getByRole('link', { name: 'Back office' })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Labels and documents' })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Users and permissions' })).toHaveCount(0);
  await page.getByLabel('Register').selectOption(register.id);
  await page.getByLabel('Opening cash').fill('1000');
  await page.getByRole('button', { name: 'Open shift' }).click();
  await expect(page.getByRole('alert')).toContainText('Shift opening outcome is unconfirmed');
  await expect(page.getByLabel('Opening cash')).toBeDisabled();
  await page.getByRole('button', { name: 'Retry opening shift' }).click();
  await expect(page.getByText('Shift open · shift-a')).toBeVisible();
  expect(openRequests).toHaveLength(2);
  expect(openRequests[1]).toEqual(openRequests[0]);
  expect(frontDataRequests).toHaveLength(0);

  const barcode = page.getByLabel('Scan barcode');
  await barcode.fill(product.barcode);
  await barcode.press('Enter');
  await barcode.fill(product.barcode);
  await barcode.press('Enter');
  await expect(page.getByText('Cold brew coffee')).toBeVisible();
  await expect(page.getByLabel('Quantity Cold brew coffee')).toHaveValue('2');
  await expect(page.getByText('Subtotal $200')).toBeVisible();
  await page.getByLabel('Discount Cold brew coffee').fill('10');
  await expect(page.getByLabel('Discount Cold brew coffee')).toHaveValue('10');
  await expect(page.locator('.pos-basket li')).toContainText('$190');
  await expect(page.locator('.pos-money-row').nth(1)).toContainText('-$10');

  await page.getByLabel('Cash tendered').fill('200');
  await expect(page.locator('.pos-money-total')).toContainText('$190');
  await expect(page.locator('.pos-money-row').last()).toContainText('$10');
  await page.getByRole('button', { name: 'Complete cash sale' }).click();
  await expect(page.getByRole('alert')).toContainText('outcome is unconfirmed');
  await expect(page.locator('.pos-basket li')).toContainText('Cold brew coffee');
  await expect(page.getByLabel('Cash tendered')).toBeDisabled();
  await page.getByRole('button', { name: 'Retry same sale' }).click();
  await expect(page.getByText('POS-RECEIPT-A')).toBeVisible();
  await expect(page.locator('.pos-receipt').getByText('Change $10')).toBeVisible();
  expect(checkoutRequests).toHaveLength(2);
  expect(checkoutRequests[0]).toMatchObject({ action: 'checkout', shiftId: 'shift-a', tender: 200 });
  expect(checkoutRequests[1]).toEqual(checkoutRequests[0]);
  expect(checkoutRequests[0].lines).toEqual([{ itemId: product.id, quantity: 2, discountAmount: 10 }]);
  expect(checkoutRequests[0]).not.toHaveProperty('total');
});

test('read-only POS identity cannot open a shift or submit a sale', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [register], openShifts: [] } } }));
  await cashier(page, 'readonly');
  await expect(page.getByRole('button', { name: 'Open shift' })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Complete cash sale' })).toBeDisabled();
  await expect(page.getByLabel('Opening cash')).toBeDisabled();
  await expect(page.getByLabel('Cash tendered')).toBeDisabled();
});

test('cashier follows the selected Traditional Chinese language', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [], openShifts: [] } } }));
  await cashier(page, 'operator', 'front', 'zh-TW');
  await expect(page.getByRole('heading', { name: '開班' })).toBeVisible();
  await expect(page.getByLabel('收銀機')).toBeVisible();
  await expect(page.getByLabel('開班現金')).toBeVisible();
  await expect(page.getByLabel('掃描條碼')).toBeVisible();
  await expect(page.getByRole('heading', { name: '產品' })).toBeVisible();
  await expect(page.getByRole('heading', { name: '現金收款' })).toBeVisible();
  await expect(page.getByLabel('收款金額')).toBeVisible();

  await page.evaluate(() => localStorage.setItem('suite-language', 'en'));
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Cashier' })).toBeVisible();
  await expect(page.getByLabel('Scan barcode')).toBeVisible();
});

test('authorized back-office accounts keep POS operations and print navigation from cashier', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [], openShifts: [] } } }));
  await cashier(page, 'operator', 'back');
  await expect(page.getByRole('link', { name: 'Back office' })).toHaveAttribute('href', '/pos/back');
  await expect(page.getByRole('link', { name: 'Labels and documents' })).toHaveAttribute('href', '/pos/print');
  await expect(page.getByRole('link', { name: 'Users and permissions' })).toHaveCount(0);

  await page.unroute('**/api/auth/me?site=pos');
  await page.route('**/api/auth/me?site=pos', route => route.fulfill({ json: { success: true, data: {
    userId: 'admin-a', organizationId: 'org-a', name: 'Admin', email: 'admin@example.test',
    grants: [{ site: 'pos', role: 'admin', scope: 'back', enabled: true }],
  } } }));
  await page.reload();
  await expect(page.getByRole('link', { name: 'Back office' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Labels and documents' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Users and permissions' })).toHaveAttribute('href', '/pos/users');
});

test('receipt shows server-final values when the catalog price changes after barcode lookup', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  await page.route('**/api/stations/pos/barcode?**', route => route.fulfill({ json: { success: true, data: product } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: [] } }));
  await page.route('**/api/stations/pos/cart', async route => {
    expect(route.request().postDataJSON().lines).toEqual([{ itemId: product.id, quantity: 2, discountAmount: 10 }]);
    await route.fulfill({ json: { success: true, data: {
      saleId: 'sale-repriced', receiptNumber: 'POS-REPRICED', subtotal: 220, discountAmount: 10,
      totalAmount: 210, changeAmount: 10,
    } } });
  });
  await cashier(page);
  const barcode = page.getByLabel('Scan barcode');
  await barcode.fill(product.barcode); await barcode.press('Enter');
  await barcode.fill(product.barcode); await barcode.press('Enter');
  await page.getByLabel('Discount Cold brew coffee').fill('10');
  await expect(page.getByLabel('Discount Cold brew coffee')).toHaveValue('10');
  await page.getByLabel('Cash tendered').fill('220');
  await page.getByRole('button', { name: 'Complete cash sale' }).click();
  const receipt = page.locator('.pos-receipt');
  await expect(receipt).toContainText('Subtotal $220');
  await expect(receipt).toContainText('Discount -$10');
  await expect(receipt).toContainText('Total due $210');
  await expect(receipt).toContainText('Cash change $10');
  await expect(receipt).toContainText('Receipt amounts are calculated by the POS server.');
});

test('cashier holds a discounted basket, claims and releases it, then checks out with server-final receipt values', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  await page.route('**/api/stations/pos/barcode?**', route => route.fulfill({ json: { success: true, data: product } }));

  let carts = [] as Array<{ cartId: string; cartName: string; heldAt: string; status: 'held' | 'retrieved'; lines: Array<{ itemId: string; quantity: number; discountAmount: number; available: boolean; product: typeof product }> }>;
  let claimed = false;
  let storedDiscount = 10;
  let heldPayload: Record<string, any> | undefined;
  const actions: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: carts } }));
  await page.route('**/api/stations/pos/cart', async route => {
    const body = route.request().postDataJSON();
    actions.push(body);
    if (body.action === 'hold') {
      heldPayload = body;
      carts = [{ cartId: 'cart-a', cartName: body.cartName, heldAt: '2026-11-15T10:00:00Z', status: 'held', lines: body.lines.map((line: any) => ({ ...line, available: true, product })) }];
      await route.fulfill({ json: { success: true, data: { cartId: 'cart-a', status: 'held' } } });
    } else if (body.action === 'claim') {
      claimed = true;
      carts = [{ cartId: 'cart-a', cartName: 'Morning order', heldAt: '2026-11-15T10:00:00Z', status: 'retrieved', lines: [{ itemId: product.id, quantity: 2, discountAmount: storedDiscount, available: true, product }] }];
      await route.fulfill({ json: { success: true, data: { cartId: 'cart-a', status: 'retrieved' } } });
    } else if (body.action === 'update') {
      expect(claimed).toBe(true);
      storedDiscount = body.lines[0].discountAmount;
      await route.fulfill({ json: { success: true, data: { cartId: 'cart-a', status: 'retrieved' } } });
    } else if (body.action === 'release') {
      claimed = false;
      carts = [{ cartId: 'cart-a', cartName: 'Morning order', heldAt: '2026-11-15T10:00:00Z', status: 'held', lines: [{ itemId: product.id, quantity: 2, discountAmount: storedDiscount, available: true, product }] }];
      await route.fulfill({ json: { success: true, data: { cartId: 'cart-a', status: 'held' } } });
    } else if (body.action === 'checkout') {
      expect(body).toMatchObject({ cartId: 'cart-a', shiftId: 'shift-a', tender: 220 });
      expect(body).not.toHaveProperty('lines');
      expect(claimed).toBe(true);
      carts = [];
      await route.fulfill({ json: { success: true, data: {
        saleId: 'sale-held-a', receiptNumber: 'POS-HELD-A', subtotal: 220, discountAmount: 10,
        totalAmount: 220 - storedDiscount, changeAmount: storedDiscount,
      } } });
    } else {
      throw new Error(`Unexpected cart action: ${body.action}`);
    }
  });

  await cashier(page);
  await expect(page.getByRole('heading', { name: 'Held carts' })).toBeVisible();
  const barcode = page.getByLabel('Scan barcode');
  await barcode.fill(product.barcode); await barcode.press('Enter');
  await barcode.fill(product.barcode); await barcode.press('Enter');
  await page.getByLabel('Discount Cold brew coffee').fill('10');
  await page.getByLabel('Cart name').fill('Morning order');
  await page.getByRole('button', { name: 'Hold basket' }).click();
  await expect(page.locator('.pos-basket li')).toHaveCount(0);
  await expect(page.getByText('Morning order', { exact: true })).toBeVisible();
  expect(heldPayload).toMatchObject({ action: 'hold', shiftId: 'shift-a', cartName: 'Morning order' });
  expect(heldPayload?.lines).toEqual([{ itemId: product.id, quantity: 2, discountAmount: 10 }]);

  await page.getByRole('button', { name: 'Claim Morning order' }).click();
  await expect(page.locator('.pos-basket li')).toContainText('Cold brew coffee');
  await expect(page.getByLabel('Quantity Cold brew coffee')).toHaveValue('2');
  await expect(page.getByLabel('Discount Cold brew coffee')).toHaveValue('10');
  await page.getByLabel('Discount Cold brew coffee').fill('15');
  await expect(page.getByLabel('Discount Cold brew coffee')).toHaveValue('15');
  await expect(page.getByRole('button', { name: 'Complete cash sale' })).toBeDisabled();
  await page.getByRole('button', { name: 'Save cart changes' }).click();
  await expect(page.getByRole('button', { name: 'Complete cash sale' })).toBeDisabled();
  await expect(page.getByRole('status').filter({ hasText: 'Held cart changes saved' })).toBeVisible();
  await page.getByRole('button', { name: 'Release held cart' }).click();
  await expect(page.locator('.pos-basket li')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Claim Morning order' })).toBeVisible();

  await page.getByRole('button', { name: 'Claim Morning order' }).click();
  await expect(page.getByLabel('Quantity Cold brew coffee')).toHaveValue('2');
  await expect(page.getByLabel('Discount Cold brew coffee')).toHaveValue('15');
  await page.getByLabel('Cash tendered').fill('220');
  await page.getByRole('button', { name: 'Complete cash sale' }).click();
  const receipt = page.locator('.pos-receipt');
  await expect(receipt).toContainText('POS-HELD-A');
  await expect(receipt).toContainText('Subtotal $220');
  await expect(receipt).toContainText('Total due $205');
  await expect(page.getByText('Morning order')).toHaveCount(0);
  expect(actions.map(action => action.action)).toEqual(['hold', 'claim', 'update', 'release', 'claim', 'checkout']);
  expect(actions.every(action => typeof action.idempotencyKey === 'string' && action.idempotencyKey.length >= 8)).toBe(true);
});

test('claim conflict refreshes the held list and never restores or checks out another cashier cart', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  let reads = 0;
  await page.route('**/api/stations/pos/held-carts?**', route => {
    reads += 1;
    return route.fulfill({ json: { success: true, data: reads === 1 ? [{
      cartId: 'cart-race', cartName: 'Competing basket', heldAt: '2026-11-15T10:00:00Z', status: 'held',
      lines: [{ itemId: product.id, quantity: 1, discountAmount: 0, available: true, product }],
    }] : [] } });
  });
  const actions: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/cart', async route => {
    const body = route.request().postDataJSON(); actions.push(body);
    await route.fulfill({ status: 409, json: { success: false, error: { message: 'Cart has already been claimed' } } });
  });
  await cashier(page);
  await page.getByRole('button', { name: 'Claim Competing basket' }).click();
  await expect(page.getByRole('alert')).toContainText('Another cashier claimed');
  await expect(page.getByText('Competing basket')).toHaveCount(0);
  await expect(page.locator('.pos-basket li')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Complete cash sale' })).toBeDisabled();
  expect(reads).toBeGreaterThanOrEqual(2);
  expect(actions).toHaveLength(1);
  expect(actions[0]).toMatchObject({ action: 'claim', shiftId: 'shift-a', cartId: 'cart-race' });
  await expect(page.getByLabel('Cash tendered')).toBeEmpty();
});

test('ambiguous hold retries the exact named discounted basket with the same idempotency key', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  await page.route('**/api/stations/pos/barcode?**', route => route.fulfill({ json: { success: true, data: product } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: [] } }));
  const requests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/cart', async route => {
    requests.push(route.request().postDataJSON());
    if (requests.length === 1) {
      await route.fulfill({ status: 503, json: { success: false, error: { message: 'Temporary failure' } } });
    } else {
      await route.fulfill({ json: { success: true, data: { cartId: 'cart-retry', status: 'held' } } });
    }
  });
  await cashier(page);
  const barcode = page.getByLabel('Scan barcode');
  await barcode.fill(product.barcode); await barcode.press('Enter');
  await page.getByLabel('Discount Cold brew coffee').fill('10');
  await expect(page.getByLabel('Discount Cold brew coffee')).toHaveValue('10');
  await page.getByLabel('Cart name').fill('Retry basket');
  await page.getByRole('button', { name: 'Hold basket' }).click();
  await expect(page.getByRole('alert')).toContainText('outcome is unconfirmed');
  await expect(page.getByLabel('Cart name')).toBeDisabled();
  await expect(page.locator('.pos-basket li')).toContainText('Cold brew coffee');
  await page.getByRole('button', { name: 'Retry hold' }).click();
  await expect(page.locator('.pos-basket li')).toHaveCount(0);
  expect(requests).toHaveLength(2);
  expect(requests[1]).toEqual(requests[0]);
});

test('independent cashier contexts race to claim one shared cart and only the winner can check it out', async ({ browser }) => {
  const contextA = await browser.newContext();
  const contextB = await browser.newContext();
  const pageA = await contextA.newPage();
  const pageB = await contextB.newPage();
  let lockOwner = '';
  const sharedCart = { cartId: 'cart-shared', cartName: 'Shared basket', heldAt: '2026-11-15T10:00:00Z', status: 'held', lines: [
    { itemId: product.id, quantity: 1, discountAmount: 0, available: true, product },
  ] };
  const setupCashier = async (page: import('@playwright/test').Page, userId: string) => {
    await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
      registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
    } } }));
    await page.route('**/api/stations/pos/held-carts?**', route => {
      const data = !lockOwner ? [sharedCart] : lockOwner === userId ? [{ ...sharedCart, status: 'retrieved' }] : [];
      return route.fulfill({ json: { success: true, data } });
    });
    await page.route('**/api/stations/pos/cart', async route => {
      const body = route.request().postDataJSON();
      if (body.action === 'claim') {
        if (lockOwner) await route.fulfill({ status: 409, json: { success: false, error: { message: 'Cart has already been claimed' } } });
        else {
          lockOwner = userId;
          await route.fulfill({ json: { success: true, data: { cartId: sharedCart.cartId, status: 'retrieved' } } });
        }
        return;
      }
      if (body.action === 'checkout') {
        expect(lockOwner).toBe(userId);
        expect(body).toMatchObject({ cartId: sharedCart.cartId, tender: 100 });
        expect(body).not.toHaveProperty('lines');
        await route.fulfill({ json: { success: true, data: {
          saleId: `sale-${userId}`, receiptNumber: `POS-${userId}`, subtotal: 100, discountAmount: 0,
          totalAmount: 100, changeAmount: 0,
        } } });
        return;
      }
      throw new Error(`Unexpected shared-cart action ${body.action}`);
    });
    await cashier(page, 'operator', 'front', 'en', userId);
  };

  try {
    await Promise.all([setupCashier(pageA, 'cashier-a'), setupCashier(pageB, 'cashier-b')]);
    await Promise.all([
      pageA.getByRole('button', { name: 'Claim Shared basket' }).click(),
      pageB.getByRole('button', { name: 'Claim Shared basket' }).click(),
    ]);
    const winner = lockOwner === 'cashier-a' ? pageA : pageB;
    const loser = lockOwner === 'cashier-a' ? pageB : pageA;
    await expect(winner.locator('.pos-basket li')).toContainText('Cold brew coffee');
    await expect(loser.locator('.pos-basket li')).toHaveCount(0);
    await expect(loser.getByRole('button', { name: 'Complete cash sale' })).toBeDisabled();
    await expect(loser.getByText('Shared basket')).toHaveCount(0);
    await winner.getByLabel('Cash tendered').fill('100');
    await winner.getByRole('button', { name: 'Complete cash sale' }).click();
    await expect(winner.getByText(`POS-${lockOwner}`)).toBeVisible();
  } finally {
    await contextA.close();
    await contextB.close();
  }
});

test('cashier resumes an owned retrieved cart after reload without issuing a new claim', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  let completed = false;
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: completed ? [] : [{
    cartId: 'cart-owned', cartName: 'Recovered order', heldAt: '2026-11-15T10:00:00Z', status: 'retrieved',
    lines: [{ itemId: product.id, quantity: 1, discountAmount: 10, available: true, product }],
  }] } }));
  const requests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/cart', async route => {
    const body = route.request().postDataJSON(); requests.push(body);
    expect(body.action).toBe('checkout');
    expect(body).toMatchObject({ cartId: 'cart-owned', shiftId: 'shift-a', tender: 100 });
    expect(body).not.toHaveProperty('lines');
    completed = true;
    await route.fulfill({ json: { success: true, data: {
      saleId: 'sale-resumed', receiptNumber: 'POS-RESUMED', subtotal: 100, discountAmount: 10,
      totalAmount: 90, changeAmount: 10,
    } } });
  });
  await cashier(page);
  await page.getByRole('button', { name: 'Resume Recovered order' }).click();
  await expect(page.getByLabel('Quantity Cold brew coffee')).toHaveValue('1');
  await expect(page.getByLabel('Discount Cold brew coffee')).toHaveValue('10');
  await page.getByLabel('Cash tendered').fill('100');
  await page.getByRole('button', { name: 'Complete cash sale' }).click();
  await expect(page.locator('.pos-receipt')).toContainText('POS-RESUMED');
  await expect(page.locator('.pos-receipt')).toContainText('Total due $90');
  expect(requests).toHaveLength(1);
});

test('confirmed claim remains recoverable when the post-claim list read fails', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  const held = { cartId: 'cart-read-retry', cartName: 'Read retry order', heldAt: '2026-11-15T10:00:00Z', status: 'held',
    lines: [{ itemId: product.id, quantity: 1, discountAmount: 0, available: true, product }] };
  let listReads = 0;
  let targetReads = 0;
  await page.route('**/api/stations/pos/held-carts?**', async route => {
    const url = new URL(route.request().url());
    if (url.searchParams.has('cartId')) {
      targetReads += 1;
      if (targetReads === 1) await route.fulfill({ status: 503, json: { success: false, error: { message: 'Temporary target read failure' } } });
      else await route.fulfill({ json: { success: true, data: { items: [{ ...held, status: 'retrieved' }], hasMore: false, nextCursor: '' } } });
    } else {
      listReads += 1;
      await route.fulfill({ json: { success: true, data: { items: listReads === 1 ? [held] : [{ ...held, status: 'retrieved' }], hasMore: false, nextCursor: '' } } });
    }
  });
  const actions: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/cart', async route => {
    const body = route.request().postDataJSON(); actions.push(body);
    expect(body.action).toBe('claim');
    await route.fulfill({ json: { success: true, data: { cartId: held.cartId, status: 'retrieved' } } });
  });
  await cashier(page);
  await page.getByRole('button', { name: 'Claim Read retry order' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Claim succeeded but the cart snapshot is unavailable' })).toBeVisible();
  await expect(page.locator('.pos-basket li')).toHaveCount(0);
  await page.getByRole('button', { name: 'Refresh held carts' }).click();
  await expect(page.getByRole('button', { name: 'Resume Read retry order' })).toBeEnabled();
  await page.getByRole('button', { name: 'Resume Read retry order' }).click();
  await expect(page.getByLabel('Quantity Cold brew coffee')).toHaveValue('1');
  expect(actions).toHaveLength(1);
  expect(listReads).toBe(3);
  expect(targetReads).toBe(2);
});

test('ambiguous claim and release retries replay identical payloads and preserve the claimed basket', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  let status: 'held' | 'retrieved' = 'held';
  const row = () => ({ cartId: 'cart-ambiguous', cartName: 'Ambiguous order', heldAt: '2026-11-15T10:00:00Z', status,
    lines: [{ itemId: product.id, quantity: 1, discountAmount: 5, available: true, product }] });
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: status === 'held' ? [row()] : [{ ...row(), status: 'retrieved' }] } }));
  const claimRequests: Record<string, any>[] = [];
  const releaseRequests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/cart', async route => {
    const body = route.request().postDataJSON();
    if (body.action === 'claim') {
      claimRequests.push(body);
      if (claimRequests.length === 1) await route.fulfill({ status: 503, json: { success: false, error: { message: 'Temporary claim failure' } } });
      else { status = 'retrieved'; await route.fulfill({ json: { success: true, data: { cartId: body.cartId, status } } }); }
    } else if (body.action === 'release') {
      releaseRequests.push(body);
      if (releaseRequests.length === 1) await route.fulfill({ status: 503, json: { success: false, error: { message: 'Temporary release failure' } } });
      else { status = 'held'; await route.fulfill({ json: { success: true, data: { cartId: body.cartId, status } } }); }
    } else throw new Error(`Unexpected cart action ${body.action}`);
  });
  await cashier(page);
  await page.getByRole('button', { name: 'Claim Ambiguous order' }).click();
  await expect(page.getByRole('button', { name: 'Retry claim' })).toBeVisible();
  await expect(page.locator('.pos-basket li')).toHaveCount(0);
  await page.getByRole('button', { name: 'Retry claim' }).click();
  await expect(page.getByLabel('Discount Cold brew coffee')).toHaveValue('5');
  expect(claimRequests).toHaveLength(2);
  expect(claimRequests[1]).toEqual(claimRequests[0]);
  await page.getByRole('button', { name: 'Release held cart' }).click();
  await expect(page.getByRole('button', { name: 'Retry release' })).toBeVisible();
  await expect(page.locator('.pos-basket li')).toContainText('Cold brew coffee');
  await page.getByRole('button', { name: 'Retry release' }).click();
  await expect(page.locator('.pos-basket li')).toHaveCount(0);
  expect(releaseRequests).toHaveLength(2);
  expect(releaseRequests[1]).toEqual(releaseRequests[0]);
});

test('held-cart price decrease blocks checkout until the cashier corrects and saves the discount', async ({ page }) => {
  const repricedProduct = { ...product, price: 5 };
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: [{
    cartId: 'cart-repriced', cartName: 'Repriced order', heldAt: '2026-11-15T10:00:00Z', status: 'retrieved',
    lines: [{ itemId: product.id, quantity: 1, discountAmount: 10, available: true, product: repricedProduct }],
  }] } }));
  const updates: Record<string, any>[] = [];
  let checkedOut = false;
  await page.route('**/api/stations/pos/cart', async route => {
    const body = route.request().postDataJSON();
    if (body.action === 'update') {
      updates.push(body);
      await route.fulfill({ json: { success: true, data: { cartId: body.cartId, status: 'retrieved' } } });
    } else if (body.action === 'checkout') {
      expect(body).toMatchObject({ cartId: 'cart-repriced', tender: 0 });
      expect(body).not.toHaveProperty('lines');
      checkedOut = true;
      await route.fulfill({ json: { success: true, data: {
        saleId: 'sale-repriced-held', receiptNumber: 'POS-REPRICED-HELD', subtotal: 5, discountAmount: 5,
        totalAmount: 0, changeAmount: 0,
      } } });
    } else throw new Error(`Unexpected cart action ${body.action}`);
  });
  await cashier(page);
  await page.getByRole('button', { name: 'Resume Repriced order' }).click();
  await expect(page.getByRole('button', { name: 'Complete cash sale' })).toBeDisabled();
  await expect(page.locator('.pos-payment-panel .pos-cashier-error')).toContainText('A saved discount exceeds the current price');
  await page.getByLabel('Discount Cold brew coffee').fill('5');
  await expect(page.getByRole('button', { name: 'Save cart changes' })).toBeEnabled();
  await page.getByRole('button', { name: 'Save cart changes' }).click();
  await expect(page.getByRole('button', { name: 'Complete cash sale' })).toBeDisabled();
  await page.getByLabel('Cash tendered').fill('0');
  await expect(page.getByRole('button', { name: 'Complete cash sale' })).toBeEnabled();
  await page.getByRole('button', { name: 'Complete cash sale' }).click();
  await expect(page.locator('.pos-receipt')).toContainText('Total due $0');
  expect(checkedOut).toBe(true);
  expect(updates).toHaveLength(1);
  expect(updates[0]).toMatchObject({ action: 'update', cartId: 'cart-repriced', lines: [{ itemId: product.id, quantity: 1, discountAmount: 5 }] });
});

test('held checkout validation refreshes current item details without changing the persisted line snapshot', async ({ page }) => {
  const repricedProduct = { ...product, price: 5 };
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  let claimed = false;
  let failedCheckout = false;
  let updated = false;
  let completed = false;
  await page.route('**/api/stations/pos/held-carts?**', route => {
    if (completed) return route.fulfill({ json: { success: true, data: [] } });
    const currentProduct = failedCheckout ? repricedProduct : product;
    const row = { cartId: 'cart-price-race', cartName: 'Price race order', heldAt: '2026-11-15T10:00:00Z', status: claimed ? 'retrieved' : 'held',
      lines: [{ itemId: product.id, quantity: 1, discountAmount: updated ? 5 : 10, available: true, product: currentProduct }] };
    return route.fulfill({ json: { success: true, data: [row] } });
  });
  const updates: Record<string, any>[] = [];
  const checkouts: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/cart', async route => {
    const body = route.request().postDataJSON();
    if (body.action === 'claim') {
      claimed = true;
      await route.fulfill({ json: { success: true, data: { cartId: body.cartId, status: 'retrieved' } } });
    } else if (body.action === 'update') {
      updates.push(body); updated = true;
      await route.fulfill({ json: { success: true, data: { cartId: body.cartId, status: 'retrieved' } } });
    } else if (body.action === 'checkout') {
      checkouts.push(body);
      expect(body).toMatchObject({ cartId: 'cart-price-race', tender: 100 });
      expect(body).not.toHaveProperty('lines');
      if (!updated) {
        failedCheckout = true;
        await route.fulfill({ status: 400, json: { success: false, error: { message: 'Invalid line discount' } } });
      } else {
        completed = true;
        await route.fulfill({ json: { success: true, data: {
          saleId: 'sale-price-race', receiptNumber: 'POS-PRICE-RACE', subtotal: 5, discountAmount: 5,
          totalAmount: 0, changeAmount: 100,
        } } });
      }
    } else throw new Error(`Unexpected cart action ${body.action}`);
  });
  await cashier(page);
  await page.getByRole('button', { name: 'Claim Price race order' }).click();
  await expect(page.getByLabel('Discount Cold brew coffee')).toHaveValue('10');
  await page.getByLabel('Cash tendered').fill('100');
  await page.getByRole('button', { name: 'Complete cash sale' }).click();
  await expect(page.getByRole('alert')).toContainText('Invalid line discount');
  await expect(page.getByRole('alert')).toContainText('Current item details were refreshed');
  await expect(page.getByText('Cold brew coffee')).toBeVisible();
  await expect(page.locator('.pos-basket li').locator('small')).toContainText('$5 each');
  await expect(page.getByLabel('Quantity Cold brew coffee')).toHaveValue('1');
  await expect(page.getByLabel('Discount Cold brew coffee')).toHaveValue('10');
  await expect(page.getByRole('button', { name: 'Complete cash sale' })).toBeDisabled();
  await page.getByLabel('Discount Cold brew coffee').fill('5');
  await expect(page.getByRole('button', { name: 'Save cart changes' })).toBeEnabled();
  await page.getByRole('button', { name: 'Save cart changes' }).click();
  await page.getByLabel('Cash tendered').fill('100');
  await page.getByRole('button', { name: 'Complete cash sale' }).click();
  await expect(page.locator('.pos-receipt')).toContainText('POS-PRICE-RACE');
  expect(checkouts).toHaveLength(2);
  expect(updates).toHaveLength(1);
  expect(updates[0].lines).toEqual([{ itemId: product.id, quantity: 1, discountAmount: 5 }]);
});

test('held checkout rejection reports when the refreshed list no longer contains the claimed cart', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  let listReads = 0;
  let targetReads = 0;
  let claimed = false;
  await page.route('**/api/stations/pos/held-carts?**', async route => {
    const url = new URL(route.request().url());
    if (url.searchParams.has('cartId')) {
      targetReads += 1;
      const row = { cartId: 'cart-no-longer-listed', cartName: 'Vanished claim', heldAt: '2026-11-15T10:00:00Z', status: 'retrieved',
        lines: [{ itemId: product.id, quantity: 1, discountAmount: 0, available: true, product }] };
      await route.fulfill({ json: { success: true, data: { items: targetReads === 1 ? [row] : [], hasMore: false, nextCursor: '' } } });
      return;
    }
    listReads += 1;
    const row = { cartId: 'cart-no-longer-listed', cartName: 'Vanished claim', heldAt: '2026-11-15T10:00:00Z', status: claimed ? 'retrieved' : 'held',
      lines: [{ itemId: product.id, quantity: 1, discountAmount: 0, available: true, product }] };
    await route.fulfill({ json: { success: true, data: { items: [row], hasMore: false, nextCursor: '' } } });
  });
  await page.route('**/api/stations/pos/cart', async route => {
    const body = route.request().postDataJSON();
    if (body.action === 'claim') {
      claimed = true;
      await route.fulfill({ json: { success: true, data: { cartId: body.cartId, status: 'retrieved' } } });
    } else if (body.action === 'checkout') {
      expect(body).toMatchObject({ cartId: 'cart-no-longer-listed', tender: 100 });
      expect(body).not.toHaveProperty('lines');
      await route.fulfill({ status: 409, json: { success: false, error: { message: 'Cart has already been claimed' } } });
    } else throw new Error(`Unexpected cart action ${body.action}`);
  });
  await cashier(page);
  await page.getByRole('button', { name: 'Claim Vanished claim' }).click();
  await page.getByLabel('Cash tendered').fill('100');
  await page.getByRole('button', { name: 'Complete cash sale' }).click();
  const alert = page.getByRole('alert');
  await expect(alert).toContainText('Cart has already been claimed');
  await expect(alert).toContainText('The claimed cart was not present in the refreshed list');
  await expect(alert).not.toContainText('Current item details were refreshed');
  await expect(page.getByLabel('Quantity Cold brew coffee')).toHaveValue('1');
  await expect(page.getByLabel('Discount Cold brew coffee')).toHaveValue('0');
  expect(listReads).toBe(2);
  expect(targetReads).toBe(2);
});

test('held checkout stock conflict refreshes stock and preserves quantity until corrected and saved', async ({ page }) => {
  const limitedProduct = { ...product, stockQuantity: 1 };
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  let claimed = false;
  let failedCheckout = false;
  let updated = false;
  let completed = false;
  await page.route('**/api/stations/pos/held-carts?**', route => {
    if (completed) return route.fulfill({ json: { success: true, data: [] } });
    const currentProduct = failedCheckout ? limitedProduct : product;
    const row = { cartId: 'cart-stock-race', cartName: 'Stock race order', heldAt: '2026-11-15T10:00:00Z', status: claimed ? 'retrieved' : 'held',
      lines: [{ itemId: product.id, quantity: updated ? 1 : 2, discountAmount: 10, available: true, product: currentProduct }] };
    return route.fulfill({ json: { success: true, data: [row] } });
  });
  const updates: Record<string, any>[] = [];
  const checkouts: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/cart', async route => {
    const body = route.request().postDataJSON();
    if (body.action === 'claim') {
      claimed = true;
      await route.fulfill({ json: { success: true, data: { cartId: body.cartId, status: 'retrieved' } } });
    } else if (body.action === 'update') {
      updates.push(body); updated = true;
      await route.fulfill({ json: { success: true, data: { cartId: body.cartId, status: 'retrieved' } } });
    } else if (body.action === 'checkout') {
      checkouts.push(body);
      expect(body).toMatchObject({ cartId: 'cart-stock-race', tender: 200 });
      expect(body).not.toHaveProperty('lines');
      if (!updated) {
        failedCheckout = true;
        await route.fulfill({ status: 409, json: { success: false, error: { message: 'Insufficient stock' } } });
      } else {
        completed = true;
        await route.fulfill({ json: { success: true, data: {
          saleId: 'sale-stock-race', receiptNumber: 'POS-STOCK-RACE', subtotal: 100, discountAmount: 10,
          totalAmount: 90, changeAmount: 110,
        } } });
      }
    } else throw new Error(`Unexpected cart action ${body.action}`);
  });
  await cashier(page);
  await page.getByRole('button', { name: 'Claim Stock race order' }).click();
  await expect(page.getByLabel('Quantity Cold brew coffee')).toHaveValue('2');
  await page.getByLabel('Cash tendered').fill('200');
  await page.getByRole('button', { name: 'Complete cash sale' }).click();
  await expect(page.getByRole('alert')).toContainText('Insufficient stock');
  await expect(page.getByText('Cold brew coffee')).toBeVisible();
  await expect(page.locator('.pos-basket li').locator('small')).toContainText('Stock: 1');
  await expect(page.getByLabel('Quantity Cold brew coffee')).toHaveValue('2');
  await expect(page.getByLabel('Discount Cold brew coffee')).toHaveValue('10');
  await expect(page.getByRole('button', { name: 'Complete cash sale' })).toBeDisabled();
  await page.getByLabel('Quantity Cold brew coffee').fill('1');
  await expect(page.getByRole('button', { name: 'Save cart changes' })).toBeEnabled();
  await page.getByRole('button', { name: 'Save cart changes' }).click();
  await page.getByLabel('Cash tendered').fill('200');
  await page.getByRole('button', { name: 'Complete cash sale' }).click();
  await expect(page.locator('.pos-receipt')).toContainText('POS-STOCK-RACE');
  expect(checkouts).toHaveLength(2);
  expect(updates[0].lines).toEqual([{ itemId: product.id, quantity: 1, discountAmount: 10 }]);
});

test('held-cart controls follow selected Traditional Chinese and English on reload', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: [] } }));
  await page.route('**/api/stations/pos/barcode?**', route => route.fulfill({ json: { success: true, data: product } }));
  await page.route('**/api/stations/pos/cart', async route => route.fulfill({ json: { success: true, data: { cartId: 'cart-localized', status: 'held' } } }));
  await cashier(page, 'operator', 'front', 'zh-TW');
  await expect(page.getByRole('heading', { name: '掛單' })).toBeVisible();
  await expect(page.getByLabel('掛單名稱')).toBeVisible();
  await expect(page.getByRole('button', { name: '暫存購物籃' })).toBeDisabled();
  const barcode = page.getByLabel('掃描條碼');
  await barcode.fill(product.barcode); await barcode.press('Enter');
  await page.getByLabel('掛單名稱').fill('中文掛單');
  await page.getByRole('button', { name: '暫存購物籃' }).click();
  await expect(page.getByRole('status').filter({ hasText: '購物籃已暫存' })).toBeVisible();
  await page.evaluate(() => localStorage.setItem('suite-language', 'en'));
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Held carts' })).toBeVisible();
  await expect(page.getByLabel('Cart name')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Refresh held carts' })).toBeVisible();
});

test('one-line cart with a missing item cannot be checked out and requires explicit discard confirmation', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  let status: 'held' | 'retrieved' | 'cancelled' = 'retrieved';
  const unavailableCart = { cartId: 'cart-missing', cartName: 'Missing item order', heldAt: '2026-11-15T10:00:00Z', status,
    lines: [{ itemId: 'item-deleted', quantity: 1, discountAmount: 0, available: false, product: null }] };
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: status === 'cancelled' ? [] : status === 'held' ? [{ ...unavailableCart, status: 'held' }] : [unavailableCart] } }));
  const discardRequests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/cart', async route => {
    const body = route.request().postDataJSON();
    expect(body.action).toBe('discard');
    expect(body.cartId).toBe('cart-missing');
    discardRequests.push(body);
    if (discardRequests.length === 1) await route.fulfill({ status: 503, json: { success: false, error: { message: 'Temporary failure' } } });
    else { status = 'cancelled'; await route.fulfill({ json: { success: true, data: { cartId: body.cartId, status } } }); }
  });
  await cashier(page);
  await page.getByRole('button', { name: 'Resume Missing item order' }).click();
  await expect(page.getByRole('button', { name: 'Complete cash sale' })).toBeDisabled();
  await page.getByRole('button', { name: 'Remove Unavailable product' }).click();
  await expect(page.locator('.pos-basket li')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Save cart changes' })).toBeDisabled();
  page.on('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Discard held cart' }).click();
  await expect(page.getByRole('button', { name: 'Retry discard' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Discard held cart' })).toBeDisabled();
  await page.getByRole('button', { name: 'Retry discard' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Held cart discarded' })).toBeVisible();
  await expect(page.getByText('Missing item order')).toHaveCount(0);
  expect(discardRequests).toHaveLength(2);
  expect(discardRequests[1]).toEqual(discardRequests[0]);
});

test('held-cart pages expose older owned claims and resume them through a scoped cart read', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: {
    registers: [register], openShifts: [{ id: 'shift-a', registerId: register.id, storeId: register.storeId, openingCash: 1000 }],
  } } }));
  const cart = (index: number) => ({
    cartId: `cart-retrieved-${index}`, cartName: `Recovered order ${index}`, heldAt: `2026-11-15T10:${String(index % 60).padStart(2, '0')}:00Z`, status: 'retrieved',
    lines: [{ itemId: product.id, quantity: 1, discountAmount: 0, available: true, product }],
  });
  const firstPage = Array.from({ length: 50 }, (_, index) => cart(index));
  const olderClaim = cart(50);
  const cursors: string[] = [];
  let targetedRead = false;
  await page.route('**/api/stations/pos/held-carts?**', async route => {
    const url = new URL(route.request().url());
    const cartId = url.searchParams.get('cartId');
    if (cartId) {
      expect(cartId).toBe(olderClaim.cartId);
      targetedRead = true;
      await route.fulfill({ json: { success: true, data: { items: [olderClaim], hasMore: false, nextCursor: '' } } });
      return;
    }
    const cursor = url.searchParams.get('cursor') || '';
    cursors.push(cursor);
    if (!cursor) {
      expect(url.searchParams.get('limit')).toBe('50');
      await route.fulfill({ json: { success: true, data: { items: firstPage, hasMore: true, nextCursor: 'next-page-token' } } });
      return;
    }
    expect(cursor).toBe('next-page-token');
    await route.fulfill({ json: { success: true, data: { items: [olderClaim], hasMore: false, nextCursor: '' } } });
  });
  await cashier(page);
  await expect(page.getByRole('button', { name: 'Resume Recovered order 50' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Load more held carts' })).toBeVisible();
  await page.getByRole('button', { name: 'Load more held carts' }).click();
  await expect(page.getByRole('button', { name: 'Resume Recovered order 50' })).toBeVisible();
  await page.getByRole('button', { name: 'Resume Recovered order 50' }).click();
  await expect(page.getByLabel('Quantity Cold brew coffee')).toHaveValue('1');
  expect(cursors).toEqual(['', 'next-page-token']);
  expect(targetedRead).toBe(true);
});

test('admin refund lookup submits a reviewed line quantity and shows the authoritative amount', async ({ page }) => {
  let currentRefunded = 0;
  await page.route('**/api/stations/pos/refund-lookup?**', route => route.fulfill({ json: { success: true, data: {
    ...refundSale,
    lines: refundSale.lines.map(line => ({ ...line, refundedQuantity: currentRefunded, remainingQuantity: line.quantity - currentRefunded })),
  } } }));
  const refundRequests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', async route => {
    const body = route.request().postDataJSON(); refundRequests.push(body);
    expect(body).toMatchObject({ action: 'refund', saleLineId: 'line-a', quantity: 1, reason: 'Customer return' });
    expect(body.idempotencyKey).toEqual(expect.any(String));
    currentRefunded += 1;
    await route.fulfill({ json: { success: true, data: { refundId: 'refund-a', refundNumber: 'REF-001', refundAmount: 33, quantity: 1 } } });
  });
  await posBackOffice(page);
  await expect(page.getByRole('heading', { name: 'Refunds' })).toBeVisible();
  await page.getByLabel('Receipt number').fill('POS-REFUND-001');
  await page.getByRole('button', { name: 'Find receipt' }).click();
  await expect(page.getByLabel('Estimated refund')).toHaveText('NT$33');
  await page.getByLabel('Refund quantity').fill('1');
  await page.getByLabel('Refund reason').fill('Customer return');
  let confirmation = '';
  page.once('dialog', async dialog => { confirmation = dialog.message(); await dialog.accept(); });
  await page.getByRole('button', { name: 'Review refund' }).click();
  expect(confirmation).toContain('Coffee');
  expect(confirmation).toContain('NT$33');
  await expect(page.getByRole('status').filter({ hasText: 'REF-001' })).toContainText('NT$33');
  await expect(page.getByText('2 remaining', { exact: true })).toBeVisible();
  expect(refundRequests).toHaveLength(1);
  expect(refundRequests[0]).not.toHaveProperty('refundAmount');
});

test('cash refund lookup shows closed-shift and non-cash receipts as ineligible', async ({ page }) => {
  await page.route('**/api/stations/pos/refund-lookup?**', route => {
    const receiptNumber = new URL(route.request().url()).searchParams.get('receiptNumber');
    const closed = receiptNumber === 'POS-CLOSED';
    const sale = { ...refundSale, receiptNumber, paymentMethod: closed ? 'cash' : 'card', shiftStatus: closed ? 'closed' : 'open', eligible: false };
    return route.fulfill({ json: { success: true, data: sale } });
  });
  const requests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', route => { requests.push(route.request().postDataJSON()); return route.fulfill({ status: 409, json: { success: false, error: { message: 'Ineligible' } } }); });
  await posBackOffice(page);
  await expect(page.getByRole('heading', { name: 'Refunds' })).toBeVisible();
  await page.getByLabel('Receipt number').fill('POS-CLOSED');
  await page.getByRole('button', { name: 'Find receipt' }).click();
  await expect(page.getByText(/original shift is closed/i)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Review refund' })).toBeDisabled();
  await page.getByLabel('Receipt number').fill('POS-CARD');
  await page.getByRole('button', { name: 'Find receipt' }).click();
  await expect(page.getByText(/cash sales only/i)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Review refund' })).toBeDisabled();
  expect(requests).toHaveLength(0);
});

test('cash refund liquidity rejection is localized and releases the corrected refund form', async ({ page }) => {
  let lookupCount = 0;
  await page.route('**/api/stations/pos/refund-lookup?**', route => { lookupCount++; return route.fulfill({ json: { success: true, data: refundSale } }); });
  let submitted: Record<string, any> | undefined;
  await page.route('**/api/stations/pos/business', route => {
    submitted = route.request().postDataJSON();
    return route.fulfill({ status: 409, json: { success: false, error: { message: 'Cash refund exceeds the current expected drawer balance', outcome: 'not_applied' } } });
  });
  await posBackOffice(page, 'admin', 'back', 'zh-TW');
  await page.getByLabel('收據號碼').fill('POS-REFUND-001');
  await page.getByRole('button', { name: '查詢收據' }).click();
  await page.getByLabel('退款原因').fill('現金不足');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: '確認退款內容' }).click();
  await expect(page.getByRole('alert')).toContainText('現金退款金額超過收銀抽屜目前的預期現金餘額');
  await expect(page.getByRole('button', { name: '重試退款' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: '確認退款內容' })).toBeEnabled();
  await expect(page.getByLabel('退款原因')).toHaveValue('現金不足');
  expect(submitted).toMatchObject({ action: 'refund', saleLineId: 'line-a', quantity: 1, reason: '現金不足' });
  expect(lookupCount).toBe(2);
  expect(await page.evaluate(() => Object.keys(sessionStorage).filter(key => key.startsWith('pos-refund-pending:')))).toEqual([]);
});

test('ambiguous refund retries the exact payload and idempotency key', async ({ page }) => {
  await page.route('**/api/stations/pos/refund-lookup?**', route => route.fulfill({ json: { success: true, data: refundSale } }));
  const refundRequests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', async route => {
    refundRequests.push(route.request().postDataJSON());
    if (refundRequests.length === 1) {
      await route.fulfill({ status: 503, json: { success: false, error: { message: 'Temporary refund failure' } } });
      return;
    }
    await route.fulfill({ json: { success: true, data: { refundId: 'refund-retry', refundNumber: 'REF-RETRY', refundAmount: 33, quantity: 1 } } });
  });
  await posBackOffice(page);
  await page.getByLabel('Receipt number').fill('POS-REFUND-001');
  await page.getByRole('button', { name: 'Find receipt' }).click();
  await page.getByLabel('Refund reason').fill('Customer return');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Review refund' }).click();
  await expect(page.getByRole('button', { name: 'Retry refund' })).toBeVisible();
  await expect(page.getByLabel('Refund reason')).toBeDisabled();
  await page.getByRole('button', { name: 'Retry refund' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'REF-RETRY' })).toBeVisible();
  expect(refundRequests).toHaveLength(2);
  expect(refundRequests[1]).toEqual(refundRequests[0]);
});

test('ambiguous refund remains frozen and retryable after a page reload', async ({ page }) => {
  await page.route('**/api/stations/pos/refund-lookup?**', route => route.fulfill({ json: { success: true, data: refundSale } }));
  const refundRequests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', async route => {
    refundRequests.push(route.request().postDataJSON());
    if (refundRequests.length === 1) {
      await route.fulfill({ status: 503, json: { success: false, error: { message: 'Temporary refund failure' } } });
      return;
    }
    await route.fulfill({ json: { success: true, data: { refundId: 'refund-recovered', refundNumber: 'REF-RECOVERED', refundAmount: 33, quantity: 1 } } });
  });
  await posBackOffice(page);
  await page.getByLabel('Receipt number').fill('POS-REFUND-001');
  await page.getByRole('button', { name: 'Find receipt' }).click();
  await page.getByLabel('Refund reason').fill('Customer return');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Review refund' }).click();
  await expect(page.getByRole('button', { name: 'Retry refund' })).toBeVisible();
  const originalRequest = refundRequests[0];
  await page.reload();
  await expect(page.getByRole('button', { name: 'Retry refund' })).toBeVisible();
  await expect(page.getByLabel('Refund quantity')).toHaveValue('1');
  await expect(page.getByLabel('Refund reason')).toBeDisabled();
  await expect(page.getByLabel('Refund reason')).toHaveValue('Customer return');
  await page.getByRole('button', { name: 'Retry refund' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'REF-RECOVERED' })).toBeVisible();
  expect(refundRequests).toHaveLength(2);
  expect(refundRequests[1]).toEqual(originalRequest);
});

test('saved ambiguous refund can retry when receipt recovery lookup fails', async ({ page }) => {
  let lookupCount = 0;
  await page.route('**/api/stations/pos/refund-lookup?**', async route => {
    lookupCount += 1;
    if (lookupCount === 2) {
      await route.fulfill({ status: 503, json: { success: false, error: { message: 'Lookup temporarily unavailable' } } });
      return;
    }
    await route.fulfill({ json: { success: true, data: refundSale } });
  });
  const refundRequests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', async route => {
    refundRequests.push(route.request().postDataJSON());
    if (refundRequests.length === 1) {
      await route.fulfill({ status: 503, json: { success: false, error: { message: 'Temporary refund failure' } } });
      return;
    }
    await route.fulfill({ json: { success: true, data: { refundId: 'refund-recovered', refundNumber: 'REF-RECOVERED', refundAmount: 33, quantity: 1 } } });
  });
  await posBackOffice(page);
  await page.getByLabel('Receipt number').fill('POS-REFUND-001');
  await page.getByRole('button', { name: 'Find receipt' }).click();
  await page.getByLabel('Refund reason').fill('Customer return');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Review refund' }).click();
  await expect(page.getByRole('button', { name: 'Retry refund' })).toBeVisible();
  const originalRequest = refundRequests[0];
  await page.reload();
  await expect(page.getByRole('button', { name: 'Retry refund' })).toBeVisible();
  await expect(page.getByRole('alert')).toContainText('Lookup temporarily unavailable');
  await expect(page.getByText('Refund quantity: 1')).toBeVisible();
  await expect(page.getByText('Refund reason: Customer return')).toBeVisible();
  await page.getByRole('button', { name: 'Retry refund' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'REF-RECOVERED' })).toBeVisible();
  expect(refundRequests).toHaveLength(2);
  expect(refundRequests[1]).toEqual(originalRequest);
});

test('back-office operators do not see the admin refund workflow', async ({ page }) => {
  await posBackOffice(page, 'operator', 'back');
  await expect(page.getByRole('heading', { name: 'Refunds' })).toHaveCount(0);
  await expect(page.getByLabel('Receipt number')).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Refunds' })).toHaveCount(0);
});

test('admin refund workflow follows the active Traditional Chinese language', async ({ page }) => {
  await posBackOffice(page, 'admin', 'back', 'zh-TW');
  await expect(page.getByRole('heading', { name: '退款' })).toBeVisible();
  await expect(page.getByLabel('收據號碼')).toBeVisible();
  await expect(page.getByRole('link', { name: '退款' })).toBeVisible();
});

test('stale refund quantity rejection refreshes the exact receipt before another attempt', async ({ page }) => {
  let lookupCount = 0;
  await page.route('**/api/stations/pos/refund-lookup?**', route => {
    lookupCount += 1;
    const currentRemaining = lookupCount === 1 ? 3 : 1;
    return route.fulfill({ json: { success: true, data: {
      ...refundSale,
      lines: refundSale.lines.map(line => ({ ...line, refundedQuantity: line.quantity - currentRemaining, remainingQuantity: currentRemaining })),
    } } });
  });
  let postCount = 0;
  await page.route('**/api/stations/pos/business', async route => {
    postCount += 1;
    await route.fulfill({ status: 409, json: { success: false, error: { message: 'Refund quantity changed; retry', outcome: 'not_applied' } } });
  });
  await posBackOffice(page);
  await page.getByLabel('Receipt number').fill('POS-REFUND-001');
  await page.getByRole('button', { name: 'Find receipt' }).click();
  await page.getByLabel('Refund quantity').fill('3');
  await page.getByLabel('Refund reason').fill('Customer return');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Review refund' }).click();
  await expect(page.getByRole('alert')).toContainText('Refund quantity changed');
  await expect(page.getByText('1 remaining', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Retry refund' })).toHaveCount(0);
  expect(lookupCount).toBe(2);
  expect(postCount).toBe(1);
});

test('completed refund selects the next refundable line after refresh', async ({ page }) => {
  let refundedFirstLine = false;
  await page.route('**/api/stations/pos/refund-lookup?**', route => route.fulfill({ json: { success: true, data: {
    ...refundSale,
    lines: [
      { ...refundSale.lines[0], saleLineId: 'line-a', name: 'Coffee', quantity: 1, refundedQuantity: refundedFirstLine ? 1 : 0, remainingQuantity: refundedFirstLine ? 0 : 1, lineTotal: 100 },
      { ...refundSale.lines[0], saleLineId: 'line-b', name: 'Tea', quantity: 1, refundedQuantity: 0, remainingQuantity: 1, lineTotal: 75 },
    ],
  } } }));
  await page.route('**/api/stations/pos/business', async route => {
    refundedFirstLine = true;
    await route.fulfill({ json: { success: true, data: { refundId: 'refund-a', refundNumber: 'REF-NEXT', refundAmount: 100, quantity: 1 } } });
  });
  await posBackOffice(page);
  await page.getByLabel('Receipt number').fill('POS-REFUND-001');
  await page.getByRole('button', { name: 'Find receipt' }).click();
  await page.getByLabel('Refund reason').fill('Customer return');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Review refund' }).click();
  await expect(page.getByLabel('Refund line')).toHaveValue('line-b');
  await expect(page.getByRole('button', { name: 'Review refund' })).toBeEnabled();
});

test('rejected refund with unavailable receipt refresh stays clearly rejected', async ({ page }) => {
  let lookupCount = 0;
  await page.route('**/api/stations/pos/refund-lookup?**', async route => {
    lookupCount += 1;
    if (lookupCount > 1) return route.fulfill({ status: 503, json: { success: false, error: { message: 'Lookup unavailable' } } });
    return route.fulfill({ json: { success: true, data: refundSale } });
  });
  await page.route('**/api/stations/pos/business', route => route.fulfill({ status: 409, json: { success: false, error: { message: 'Refund quantity changed; retry', outcome: 'not_applied' } } }));
  await posBackOffice(page);
  await page.getByLabel('Receipt number').fill('POS-REFUND-001');
  await page.getByRole('button', { name: 'Find receipt' }).click();
  await page.getByLabel('Refund reason').fill('Customer return');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Review refund' }).click();
  await expect(page.getByRole('alert')).toContainText('Refund was rejected, but receipt refresh failed');
  await expect(page.getByRole('alert')).not.toContainText('Refund completed');
});

test('ambiguous refund keeps its idempotency key across pre-handler errors and reload', async ({ page }) => {
  await page.route('**/api/stations/pos/refund-lookup?**', route => route.fulfill({ json: { success: true, data: refundSale } }));
  const refundRequests: Record<string, any>[] = [];
  const retryStatuses = [503, 401, 429, 403, 400, 409];
  await page.route('**/api/stations/pos/business', async route => {
    refundRequests.push(route.request().postDataJSON());
    if (refundRequests.length <= retryStatuses.length) {
      await route.fulfill({ status: retryStatuses[refundRequests.length - 1], json: { success: false, error: { message: 'Temporary request failure' } } });
      return;
    }
    await route.fulfill({ json: { success: true, data: { refundId: 'refund-auth-recovered', refundNumber: 'REF-AUTH-RECOVERED', refundAmount: 33, quantity: 1 } } });
  });
  await posBackOffice(page);
  await page.getByLabel('Receipt number').fill('POS-REFUND-001');
  await page.getByRole('button', { name: 'Find receipt' }).click();
  await page.getByLabel('Refund reason').fill('Customer return');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Review refund' }).click();
  await expect(page.getByRole('button', { name: 'Retry refund' })).toBeVisible();
  await page.getByRole('button', { name: 'Retry refund' }).click();
  await expect(page.getByRole('alert')).toContainText('Resolve the access or request issue');
  await expect(page.getByRole('button', { name: 'Retry refund' })).toBeVisible();
  await page.reload();
  await expect(page.getByRole('button', { name: 'Retry refund' })).toBeVisible();
  for (const expectedStatus of [429, 403]) {
    await page.getByRole('button', { name: 'Retry refund' }).click();
    await expect(page.getByRole('alert')).toContainText('Resolve the access or request issue');
    expect(retryStatuses[refundRequests.length - 1]).toBe(expectedStatus);
  }
  await page.getByRole('button', { name: 'Retry refund' }).click();
  await expect(page.getByRole('alert')).toContainText('Resolve the access or request issue');
  expect(retryStatuses[refundRequests.length - 1]).toBe(400);
  await expect(page.getByRole('button', { name: 'Retry refund' })).toBeVisible();
  await page.getByRole('button', { name: 'Retry refund' }).click();
  await expect(page.getByRole('alert')).toContainText('Refund retry key conflicts with a different request');
  expect(retryStatuses[refundRequests.length - 1]).toBe(409);
  await expect(page.getByRole('button', { name: 'Retry refund' })).toBeVisible();
  await page.getByRole('button', { name: 'Retry refund' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'REF-AUTH-RECOVERED' })).toBeVisible();
  expect(refundRequests).toHaveLength(7);
  expect(refundRequests.every(request => request.idempotencyKey === refundRequests[0].idempotencyKey)).toBe(true);
  expect(refundRequests.every(request => JSON.stringify(request) === JSON.stringify(refundRequests[0]))).toBe(true);
});

test('refund editor enforces the backend quantity and UTF-8 reason limits before saving', async ({ page }) => {
  const sale = { ...refundSale, lines: [{ ...refundSale.lines[0], quantity: 1500, remainingQuantity: 1500 }] };
  await page.route('**/api/stations/pos/refund-lookup?**', route => route.fulfill({ json: { success: true, data: sale } }));
  const requests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', route => { requests.push(route.request().postDataJSON()); return route.fulfill({ status: 500, json: { success: false, error: { message: 'Should not submit invalid values' } } }); });
  await posBackOffice(page);
  await page.getByLabel('Receipt number').fill('POS-REFUND-001');
  await page.getByRole('button', { name: 'Find receipt' }).click();
  const quantity = page.getByLabel('Refund quantity');
  await expect(quantity).toHaveAttribute('max', '1000');
  await quantity.fill('1001');
  await expect(page.getByRole('button', { name: 'Review refund' })).toBeDisabled();
  await quantity.fill('1000');
  await page.getByLabel('Refund reason').fill('中'.repeat(67));
  await expect(page.getByText('Refund reason must be 200 UTF-8 bytes or fewer.')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Review refund' })).toBeDisabled();
  await page.getByLabel('Refund reason').fill('中'.repeat(66));
  await expect(page.getByText('Refund reason must be 200 UTF-8 bytes or fewer.')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Review refund' })).toBeEnabled();
  expect(requests).toHaveLength(0);
  expect(await page.evaluate(() => Object.keys(sessionStorage).filter(key => key.startsWith('pos-refund-pending:')))).toEqual([]);
});

test('definitive no-effect validation clears an older impossible refund and restores an editable form', async ({ page }) => {
  const ownerKey = 'org-a:manager-a';
  const attempt = { receiptNumber: 'POS-REFUND-001', saleLineId: 'line-a', quantity: 1001, reason: 'Customer return', idempotencyKey: 'refund-old-invalid-0001' };
  await page.addInitScript(({ key, value }) => sessionStorage.setItem(key, JSON.stringify(value)), { key: `pos-refund-pending:${encodeURIComponent(ownerKey)}`, value: attempt });
  const sale = { ...refundSale, lines: [{ ...refundSale.lines[0], quantity: 1500, remainingQuantity: 1500 }] };
  let lookups = 0;
  await page.route('**/api/stations/pos/refund-lookup?**', route => { lookups++; return route.fulfill({ json: { success: true, data: sale } }); });
  let sent: Record<string, any> | undefined;
  await page.route('**/api/stations/pos/business', route => {
    sent = route.request().postDataJSON();
    return route.fulfill({ status: 400, json: { success: false, error: { message: 'Invalid refund quantity', outcome: 'not_applied' } } });
  });
  await posBackOffice(page);
  await expect(page.getByRole('button', { name: 'Retry refund' })).toBeVisible();
  await page.getByRole('button', { name: 'Retry refund' }).click();
  await expect(page.getByRole('alert')).toContainText('Refund request was rejected before processing');
  await expect(page.getByRole('button', { name: 'Retry refund' })).toHaveCount(0);
  await expect(page.getByLabel('Refund quantity')).toHaveValue('1000');
  await expect(page.getByLabel('Refund reason')).toHaveValue('Customer return');
  await expect(page.getByRole('button', { name: 'Review refund' })).toBeEnabled();
  expect(sent).toMatchObject({ saleLineId: 'line-a', quantity: 1001, idempotencyKey: attempt.idempotencyKey });
  expect(lookups).toBe(2);
  expect(await page.evaluate(key => sessionStorage.getItem(key), `pos-refund-pending:${encodeURIComponent(ownerKey)}`)).toBeNull();
});

test('definitive no-effect validation releases a persisted UTF-8-overlong reason for correction', async ({ page }) => {
  const ownerKey = 'org-a:manager-a';
  const longReason = '中'.repeat(67);
  const attempt = { receiptNumber: 'POS-REFUND-001', saleLineId: 'line-a', quantity: 1, reason: longReason, idempotencyKey: 'refund-old-long-reason-01' };
  await page.addInitScript(({ key, value }) => sessionStorage.setItem(key, JSON.stringify(value)), { key: `pos-refund-pending:${encodeURIComponent(ownerKey)}`, value: attempt });
  await page.route('**/api/stations/pos/refund-lookup?**', route => route.fulfill({ json: { success: true, data: refundSale } }));
  let sent: Record<string, any> | undefined;
  await page.route('**/api/stations/pos/business', route => {
    sent = route.request().postDataJSON();
    return route.fulfill({ status: 400, json: { success: false, error: { message: 'Invalid point-of-sale request', outcome: 'not_applied' } } });
  });
  await posBackOffice(page);
  await expect(page.getByRole('button', { name: 'Retry refund' })).toBeVisible();
  await page.getByRole('button', { name: 'Retry refund' }).click();
  await expect(page.getByRole('alert')).toContainText('Refund request was rejected before processing');
  await expect(page.getByRole('button', { name: 'Retry refund' })).toHaveCount(0);
  const reason = page.getByLabel('Refund reason');
  await expect(reason).toHaveValue(longReason);
  await expect(page.getByText('Refund reason must be 200 UTF-8 bytes or fewer.')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Review refund' })).toBeDisabled();
  await reason.fill('中'.repeat(66));
  await expect(page.getByRole('button', { name: 'Review refund' })).toBeEnabled();
  expect(sent).toMatchObject({ saleLineId: 'line-a', quantity: 1, reason: longReason, idempotencyKey: attempt.idempotencyKey });
  expect(await page.evaluate(key => sessionStorage.getItem(key), `pos-refund-pending:${encodeURIComponent(ownerKey)}`)).toBeNull();
});

const openCashShift = { id: 'shift-cash-a', registerId: register.id, storeId: register.storeId, openingCash: 1000, expectedCash: 1000, openedAt: '2026-11-15T09:00:00Z' };

test('cashier records cash in and out then closes against the server counted variance', async ({ page }) => {
  let closed = false, expectedCash = 1000;
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [register], openShifts: closed ? [] : [{ ...openCashShift, expectedCash }] } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: { items: [], hasMore: false, nextCursor: '' } } }));
  const requests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', async route => {
    const body = route.request().postDataJSON(); requests.push(body);
    if (body.action === 'cash-movement') {
      expect(body).toMatchObject({ shiftId: openCashShift.id, action: 'cash-movement' });
      expect(body.amount).toBeGreaterThan(0);
      expect(body.idempotencyKey).toEqual(expect.any(String));
      expectedCash += body.movementType === 'in' ? body.amount : -body.amount;
      await route.fulfill({ json: { success: true, data: { movementId: `movement-${requests.length}`, shiftId: openCashShift.id, movementType: body.movementType, amount: body.amount, reason: body.reason } } });
      return;
    }
    expect(body).toMatchObject({ action: 'close', shiftId: openCashShift.id, closingCash: 1135, notes: 'Cash short after drawer count' });
    expect(body).not.toHaveProperty('expectedCash');
    closed = true;
    await route.fulfill({ json: { success: true, data: { shiftId: openCashShift.id, status: 'closed', expectedCash: 1145, closingCash: 1135, discrepancy: -10 } } });
  });
  await cashier(page);
  await expect(page.getByRole('region', { name: 'Cash movement' })).toBeVisible();
  await page.getByLabel('Cash movement type').selectOption('in');
  await page.getByLabel('Movement amount').fill('20');
  await page.getByLabel('Movement reason').fill('Float top-up');
  await page.getByRole('button', { name: 'Record cash movement' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Cash movement recorded' })).toBeVisible();
  await page.getByLabel('Cash movement type').selectOption('out');
  await page.getByLabel('Movement amount').fill('15');
  await page.getByLabel('Movement reason').fill('Safe drop');
  await page.getByRole('button', { name: 'Record cash movement' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Cash movement recorded' })).toBeVisible();
  await page.getByLabel('Counted closing cash').fill('1135');
  await page.getByLabel('Variance explanation').fill('Cash short after drawer count');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Review and close shift' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Shift closed' })).toContainText('1,145');
  await expect(page.getByRole('status').filter({ hasText: 'Shift closed' })).toContainText('-10');
  expect(requests.map(request => request.action)).toEqual(['cash-movement', 'cash-movement', 'close']);
  expect(requests[0]).toMatchObject({ movementType: 'in', amount: 20, reason: 'Float top-up' });
  expect(requests[1]).toMatchObject({ movementType: 'out', amount: 15, reason: 'Safe drop' });
});

test('ambiguous cash movement keeps and retries the exact request after reload', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [register], openShifts: [openCashShift] } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: { items: [], hasMore: false, nextCursor: '' } } }));
  const requests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', async route => {
    requests.push(route.request().postDataJSON());
    if (requests.length === 1) {
      await route.fulfill({ status: 503, json: { success: false, error: { message: 'Cash movement outcome is unconfirmed' } } });
      return;
    }
    await route.fulfill({ json: { success: true, data: { movementId: 'movement-recovered', shiftId: openCashShift.id, movementType: 'in', amount: 25, reason: 'Float top-up' } } });
  });
  await cashier(page);
  await page.getByLabel('Cash movement type').selectOption('in');
  await page.getByLabel('Movement amount').fill('25');
  await page.getByLabel('Movement reason').fill('Float top-up');
  await page.getByRole('button', { name: 'Record cash movement' }).click();
  await expect(page.getByRole('button', { name: 'Retry cash movement' })).toBeVisible();
  const original = requests[0];
  await expect(page.getByLabel('Movement reason')).toBeDisabled();
  await page.reload();
  await expect(page.getByRole('button', { name: 'Retry cash movement' })).toBeVisible();
  await expect(page.getByLabel('Movement amount')).toHaveValue('25');
  await expect(page.getByLabel('Movement reason')).toHaveValue('Float top-up');
  await page.getByRole('button', { name: 'Retry cash movement' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Cash movement recorded' })).toContainText('25');
  expect(requests).toHaveLength(2);
  expect(requests[1]).toEqual(original);
});

test('definitive cash-out rejection clears the saved attempt before a corrected request', async ({ page }) => {
  // Terminal showed 30, but a concurrent drawer change leaves only 20 by the time the server validates.
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [register], openShifts: [{ ...openCashShift, openingCash: 30, expectedCash: 30 }] } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: { items: [], hasMore: false, nextCursor: '' } } }));
  const requests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', async route => {
    const body = route.request().postDataJSON(); requests.push(body);
    if (requests.length === 1) {
      await route.fulfill({ status: 409, json: { success: false, error: { message: 'Cash out exceeds the available drawer balance', outcome: 'not_applied' } } });
      return;
    }
    await route.fulfill({ json: { success: true, data: { movementId: 'movement-corrected', shiftId: openCashShift.id, movementType: 'out', amount: 20, reason: 'Safe drop' } } });
  });
  await cashier(page);
  await page.getByLabel('Cash movement type').selectOption('out');
  await page.getByLabel('Movement amount').fill('30');
  await page.getByLabel('Movement reason').fill('Safe drop');
  await page.getByRole('button', { name: 'Record cash movement' }).click();
  await expect(page.getByRole('alert')).toContainText('exceeds the available drawer balance');
  await expect(page.getByRole('button', { name: 'Retry cash movement' })).toHaveCount(0);
  await expect(page.getByLabel('Movement amount')).toBeEnabled();
  await expect(page.getByLabel('Movement amount')).toHaveValue('30');
  await page.getByLabel('Movement amount').fill('20');
  await page.getByRole('button', { name: 'Record cash movement' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Cash movement recorded' })).toBeVisible();
  expect(requests).toHaveLength(2);
  expect(requests[0].idempotencyKey).not.toBe(requests[1].idempotencyKey);
  expect(requests[0]).toMatchObject({ movementType: 'out', amount: 30, reason: 'Safe drop' });
  expect(requests[1]).toMatchObject({ movementType: 'out', amount: 20, reason: 'Safe drop' });
});

test('negative legacy drawer balance can be repaired with cash in while cash out is blocked', async ({ page }) => {
  let expectedCash = -20;
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [register], openShifts: [{ ...openCashShift, openingCash: 0, expectedCash }] } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: { items: [], hasMore: false, nextCursor: '' } } }));
  const requests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', async route => {
    const body = route.request().postDataJSON(); requests.push(body);
    expectedCash += body.amount;
    await route.fulfill({ json: { success: true, data: { movementId: 'movement-repair', shiftId: openCashShift.id, movementType: body.movementType, amount: body.amount, reason: body.reason } } });
  });
  await cashier(page);
  await expect(page.getByRole('region', { name: 'Cash drawer' })).toContainText('-NT$20');
  await page.getByLabel('Cash movement type').selectOption('out');
  await page.getByLabel('Movement amount').fill('1');
  await page.getByLabel('Movement reason').fill('Correct negative balance');
  await expect(page.getByRole('button', { name: 'Record cash movement' })).toBeDisabled();
  await page.getByLabel('Cash movement type').selectOption('in');
  await page.getByLabel('Movement amount').fill('20');
  await expect(page.getByRole('button', { name: 'Record cash movement' })).toBeEnabled();
  await page.getByRole('button', { name: 'Record cash movement' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Cash movement recorded' })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Cash drawer' })).toContainText('NT$0');
  expect(requests).toHaveLength(1);
  expect(requests[0]).toMatchObject({ action: 'cash-movement', movementType: 'in', amount: 20, reason: 'Correct negative balance' });
});

test('ambiguous shift close replays the same counted cash and key after reload', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [register], openShifts: [openCashShift] } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: { items: [], hasMore: false, nextCursor: '' } } }));
  const requests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', async route => {
    requests.push(route.request().postDataJSON());
    if (requests.length === 1) {
      await route.fulfill({ status: 503, json: { success: false, error: { message: 'Close outcome is unconfirmed' } } });
      return;
    }
    await route.fulfill({ json: { success: true, data: { shiftId: openCashShift.id, status: 'closed', expectedCash: 1000, closingCash: 990, discrepancy: -10 } } });
  });
  await cashier(page);
  await page.getByLabel('Counted closing cash').fill('990');
  await page.getByLabel('Variance explanation').fill('Counted drawer is short');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Review and close shift' }).click();
  await expect(page.getByRole('button', { name: 'Retry close shift' })).toBeVisible();
  const original = requests[0];
  await expect(page.getByLabel('Counted closing cash')).toBeDisabled();
  await page.reload();
  await expect(page.getByRole('button', { name: 'Retry close shift' })).toBeVisible();
  await expect(page.getByLabel('Counted closing cash')).toHaveValue('990');
  await page.getByRole('button', { name: 'Retry close shift' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Shift closed' })).toContainText('-10');
  expect(requests).toHaveLength(2);
  expect(requests[1]).toEqual(original);
});

test('persisted invalid close retry is released by a definitive rejection before corrected close', async ({ page }) => {
  const ownerKey = 'org-a:cashier-a';
  const oldAttempt = { action: 'close', shiftId: openCashShift.id, closingCash: 1000000001, notes: 'Old invalid count', idempotencyKey: 'close-invalid-count-key' };
  await page.addInitScript(({ key, value }) => sessionStorage.setItem(key, JSON.stringify(value)), { key: `pos-shift-operation:${encodeURIComponent(ownerKey)}`, value: oldAttempt });
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [register], openShifts: [openCashShift] } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: { items: [], hasMore: false, nextCursor: '' } } }));
  const requests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', async route => {
    const body = route.request().postDataJSON(); requests.push(body);
    if (requests.length === 1) {
      await route.fulfill({ status: 400, json: { success: false, error: { message: 'Closing cash must be within allowed bounds', outcome: 'not_applied' } } });
      return;
    }
    await route.fulfill({ json: { success: true, data: { shiftId: openCashShift.id, status: 'closed', expectedCash: 1000, closingCash: 1000, discrepancy: 0 } } });
  });
  await cashier(page);
  await expect(page.getByRole('button', { name: 'Retry close shift' })).toBeVisible();
  await expect(page.getByLabel('Counted closing cash')).toHaveValue('1000000001');
  await page.getByRole('button', { name: 'Retry close shift' }).click();
  await expect(page.getByRole('alert')).toContainText('Closing cash must be within allowed bounds');
  await expect(page.getByRole('button', { name: 'Retry close shift' })).toHaveCount(0);
  expect(await page.evaluate(key => sessionStorage.getItem(key), `pos-shift-operation:${encodeURIComponent(ownerKey)}`)).toBeNull();
  await expect(page.getByLabel('Counted closing cash')).toBeEnabled();
  await page.getByLabel('Counted closing cash').fill('1000');
  await page.getByLabel('Variance explanation').fill('');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Review and close shift' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Shift closed' })).toContainText('Variance 0');
  expect(requests).toHaveLength(2);
  expect(requests[0].idempotencyKey).toBe(oldAttempt.idempotencyKey);
  expect(requests[1].idempotencyKey).not.toBe(requests[0].idempotencyKey);
  expect(requests[1]).toMatchObject({ action: 'close', closingCash: 1000 });
});

test('read-only cashier cannot record cash movements or close an open shift', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [register], openShifts: [openCashShift] } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: { items: [], hasMore: false, nextCursor: '' } } }));
  await cashier(page, 'readonly');
  await expect(page.getByRole('button', { name: 'Record cash movement' })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Review and close shift' })).toBeDisabled();
  await expect(page.getByLabel('Movement amount')).toBeDisabled();
  await expect(page.getByLabel('Counted closing cash')).toBeDisabled();
});

test('zero-balance shift still requires an explicit counted cash value', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [register], openShifts: [{ ...openCashShift, openingCash: 0, expectedCash: 0 }] } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: { items: [], hasMore: false, nextCursor: '' } } }));
  const requests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', route => { requests.push(route.request().postDataJSON()); return route.fulfill({ status: 503, json: { success: false, error: { message: 'Unexpected request' } } }); });
  await cashier(page);
  await expect(page.getByLabel('Counted closing cash')).toHaveValue('');
  await expect(page.getByRole('button', { name: 'Review and close shift' })).toBeDisabled();
  expect(requests).toHaveLength(0);
});

test('cash movement rejects zero, over-limit, blank, and UTF-8-overlong values before submit', async ({ page }) => {
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [register], openShifts: [openCashShift] } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: { items: [], hasMore: false, nextCursor: '' } } }));
  const requests: Record<string, any>[] = [];
  await page.route('**/api/stations/pos/business', route => { requests.push(route.request().postDataJSON()); return route.fulfill({ status: 503, json: { success: false, error: { message: 'Invalid request' } } }); });
  await cashier(page);
  const amount = page.getByLabel('Movement amount');
  await amount.fill('0');
  await page.getByLabel('Movement reason').fill('reason');
  await expect(page.getByRole('button', { name: 'Record cash movement' })).toBeDisabled();
  await amount.fill('1000000001');
  await expect(page.getByRole('button', { name: 'Record cash movement' })).toBeDisabled();
  await amount.fill('10');
  await page.getByLabel('Movement reason').fill('中'.repeat(67));
  await expect(page.getByText('Reason must be 200 UTF-8 bytes or fewer.')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Record cash movement' })).toBeDisabled();
  await page.getByLabel('Movement reason').fill('中'.repeat(66));
  await expect(page.getByRole('button', { name: 'Record cash movement' })).toBeEnabled();
  expect(requests).toHaveLength(0);
});

test('shift close and drawer controls show localized labels and server-final short variance', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('suite-language', 'zh-TW'));
  await page.route('**/api/stations/pos/terminal', route => route.fulfill({ json: { success: true, data: { registers: [register], openShifts: [openCashShift] } } }));
  await page.route('**/api/stations/pos/held-carts?**', route => route.fulfill({ json: { success: true, data: { items: [], hasMore: false, nextCursor: '' } } }));
  await page.route('**/api/stations/pos/business', route => route.fulfill({ json: { success: true, data: { shiftId: openCashShift.id, status: 'closed', expectedCash: 1000, closingCash: 990, discrepancy: -10 } } }));
  await cashier(page, 'operator', 'front', 'zh-TW');
  await expect(page.getByLabel('現金異動')).toBeVisible();
  await expect(page.getByLabel('交班實點現金')).toBeVisible();
  await page.getByLabel('交班實點現金').fill('990');
  await page.getByLabel('差異說明').fill('點鈔短少');
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: '確認交班' }).click();
  await expect(page.getByRole('status').filter({ hasText: '班次已結束' })).toContainText('-10');
});
