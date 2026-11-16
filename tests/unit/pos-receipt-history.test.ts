import assert from 'node:assert/strict';
import test from 'node:test';
import { reconcileReceiptHistory, type LocalCheckoutReceipt, type ReceiptHistorySale } from '../../web/src/receiptHistory';

test('latest server page replaces 500 older local checkouts instead of being displaced by them', () => {
  const oldLocal: LocalCheckoutReceipt[] = Array.from({ length: 500 }, (_, index) => ({
    sale: { id: `old-${index}`, receiptNumber: `POS-OLD-${index}`, totalAmount: index },
    version: index + 1,
  }));
  const latestServer: ReceiptHistorySale[] = Array.from({ length: 500 }, (_, index) => ({
    id: `new-${index}`, receiptNumber: `POS-NEW-${index}`, totalAmount: index,
  }));

  const result = reconcileReceiptHistory(latestServer, oldLocal, 500);

  assert.deepEqual(result.history, latestServer);
  assert.deepEqual(result.pendingCheckouts, []);
});

test('only a checkout confirmed after refresh starts is retained ahead of the server page', () => {
  const serverSale = { id: 'server-sale', receiptNumber: 'POS-SERVER', totalAmount: 100 };
  const duringRefresh = { sale: { id: 'local-new', receiptNumber: 'POS-LOCAL', totalAmount: 200 }, version: 2 };

  const result = reconcileReceiptHistory([serverSale], [
    { sale: { id: 'old-local', receiptNumber: 'POS-OLD', totalAmount: 50 }, version: 1 },
    duringRefresh,
  ], 1);

  assert.deepEqual(result.history, [duringRefresh.sale, serverSale]);
  assert.deepEqual(result.pendingCheckouts, [duringRefresh]);
});
