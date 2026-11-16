export type ReceiptHistorySale = { id: string; receiptNumber: string; totalAmount: number; createdAt?: string };
export type LocalCheckoutReceipt = { sale: ReceiptHistorySale; version: number };

export function reconcileReceiptHistory(
  latest: ReceiptHistorySale[],
  localCheckouts: LocalCheckoutReceipt[],
  requestVersion: number,
): { history: ReceiptHistorySale[]; pendingCheckouts: LocalCheckoutReceipt[] } {
  const returnedIDs = new Set(latest.map(sale => sale.id));
  const pendingCheckouts = localCheckouts.filter(entry => entry.version > requestVersion && !returnedIDs.has(entry.sale.id));
  return {
    history: [...pendingCheckouts.map(entry => entry.sale), ...latest].slice(0, 500),
    pendingCheckouts,
  };
}
