import { test, expect } from '@playwright/test';

const register = { id: 'register-a', name: 'Register A', storeId: 'store-a' };
const product = { id: 'item-coffee', name: 'Cold brew coffee', barcode: '4710001001', price: 100, stockQuantity: 100 };

async function cashier(page: import('@playwright/test').Page, role: 'operator' | 'readonly' | 'admin' = 'operator', scope: 'front' | 'back' = 'front', language: 'en' | 'zh-TW' = 'en') {
  await page.addInitScript(selectedLanguage => {
    if (!localStorage.getItem('suite-language')) localStorage.setItem('suite-language', selectedLanguage);
  }, language);
  await page.route('**/api/auth/me?site=pos', route => route.fulfill({ json: { success: true, data: {
    userId: 'cashier-a', organizationId: 'org-a', name: 'Cashier A', email: 'cashier@example.test',
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
  await page.getByLabel('Cash tendered').fill('220');
  await page.getByRole('button', { name: 'Complete cash sale' }).click();
  const receipt = page.locator('.pos-receipt');
  await expect(receipt).toContainText('Subtotal $220');
  await expect(receipt).toContainText('Discount -$10');
  await expect(receipt).toContainText('Total due $210');
  await expect(receipt).toContainText('Cash change $10');
  await expect(receipt).toContainText('Receipt amounts are calculated by the POS server.');
});
