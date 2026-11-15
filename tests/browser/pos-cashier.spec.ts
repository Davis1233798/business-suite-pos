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
  let reads = 0;
  await page.route('**/api/stations/pos/held-carts?**', async route => {
    reads += 1;
    if (reads === 2) await route.fulfill({ status: 503, json: { success: false, error: { message: 'Temporary list failure' } } });
    else await route.fulfill({ json: { success: true, data: reads === 1 ? [held] : [{ ...held, status: 'retrieved' }] } });
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
  expect(reads).toBe(4);
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
  let reads = 0;
  let claimed = false;
  await page.route('**/api/stations/pos/held-carts?**', route => {
    reads += 1;
    if (reads === 3) return route.fulfill({ json: { success: true, data: [] } });
    const row = { cartId: 'cart-no-longer-listed', cartName: 'Vanished claim', heldAt: '2026-11-15T10:00:00Z', status: claimed ? 'retrieved' : 'held',
      lines: [{ itemId: product.id, quantity: 1, discountAmount: 0, available: true, product }] };
    return route.fulfill({ json: { success: true, data: [row] } });
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
  expect(reads).toBe(3);
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
