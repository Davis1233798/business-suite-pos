import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { tr } from './locale';
import './pos-cashier.css';

type Register = { id: string; name: string; storeId: string };
type Shift = { id: string; registerId: string; storeId: string; openingCash: number; openedAt: string };
type Terminal = { registers: Register[]; openShifts: Shift[] };
type Product = { id: string; name: string; barcode: string; price: number; stockQuantity: number };
type BasketLine = { item: Product; quantity: number; discountAmount: number };
type SaleReceipt = { saleId: string; receiptNumber: string; subtotal: number; discountAmount: number; totalAmount: number; changeAmount: number };

class ApiError extends Error {
  constructor(message: string, readonly status: number) { super(message); }
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, { credentials: 'same-origin', ...init });
  const envelope = await response.json() as { success?: boolean; error?: { message?: string }; data?: T };
  if (!response.ok || !envelope.success) throw new ApiError(tr(envelope.error?.message || `HTTP ${response.status}`), response.status);
  return envelope.data as T;
}

function post<T>(path: string, body: unknown) {
  return api<T>(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
}

export function PosCashier({ role }: { role: string }) {
  const writable = role !== 'readonly';
  const [terminal, setTerminal] = useState<Terminal | null>(null);
  const [activeShiftId, setActiveShiftId] = useState('');
  const [registerId, setRegisterId] = useState('');
  const [openingCash, setOpeningCash] = useState('0');
  const [barcode, setBarcode] = useState('');
  const [basket, setBasket] = useState<BasketLine[]>([]);
  const [tender, setTender] = useState('');
  const [busy, setBusy] = useState(false);
  const [checkoutUncertain, setCheckoutUncertain] = useState(false);
  const [openShiftUncertain, setOpenShiftUncertain] = useState(false);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  const [receipt, setReceipt] = useState<SaleReceipt | null>(null);
  const retryKey = useRef('');
  const openShiftKey = useRef('');
  const scanInput = useRef<HTMLInputElement>(null);

  const refreshTerminal = useCallback(async () => {
    const next = await api<Terminal>('/api/stations/pos/terminal');
    setTerminal(next);
    setRegisterId(current => current || next.registers[0]?.id || '');
    setActiveShiftId(current => next.openShifts.some(shift => shift.id === current)
      ? current
      : next.openShifts[0]?.id || '');
  }, []);

  useEffect(() => {
    let active = true;
    void api<Terminal>('/api/stations/pos/terminal')
      .then(next => {
        if (!active) return;
        setTerminal(next);
        setRegisterId(next.registers[0]?.id || '');
        setActiveShiftId(next.openShifts[0]?.id || '');
      })
      .catch(reason => { if (active) setError((reason as Error).message); });
    return () => { active = false; };
  }, []);

  const subtotal = useMemo(() => basket.reduce((sum, line) => sum + line.item.price * line.quantity, 0), [basket]);
  const discountAmount = useMemo(() => basket.reduce((sum, line) => sum + line.discountAmount, 0), [basket]);
  const total = subtotal - discountAmount;
  const tenderAmount = Number(tender);
  const change = Number.isSafeInteger(tenderAmount) && tenderAmount >= total ? tenderAmount - total : 0;
  const activeShift = terminal?.openShifts.find(shift => shift.id === activeShiftId);

  async function openShift() {
    if (!registerId || !Number.isSafeInteger(Number(openingCash)) || Number(openingCash) < 0) return;
    setBusy(true); setError(''); setMessage('');
    if (!openShiftKey.current) openShiftKey.current = `open-${crypto.randomUUID()}`;
    try {
      const result = await post<{ shiftId: string }>('/api/stations/pos/business', {
        action: 'open', registerId, openingCash: Number(openingCash), idempotencyKey: openShiftKey.current,
      });
      openShiftKey.current = '';
      setOpenShiftUncertain(false);
      setActiveShiftId(result.shiftId);
      setMessage(tr('Shift open'));
      const register = terminal?.registers.find(entry => entry.id === registerId);
      if (register) setTerminal(current => current ? {
        ...current,
        openShifts: [...current.openShifts, { id: result.shiftId, registerId, storeId: register.storeId, openingCash: Number(openingCash), openedAt: new Date().toISOString() }],
      } : current);
      try { await refreshTerminal(); } catch (reason) { setError(`${tr('Shift opened; status refresh failed:')} ${(reason as Error).message}`); }
    } catch (reason) {
      if (!(reason instanceof ApiError) || reason.status >= 500) {
        setOpenShiftUncertain(true);
        setError(tr('Shift opening outcome is unconfirmed. Retry the same request before changing it.'));
      } else {
        openShiftKey.current = '';
        setOpenShiftUncertain(false);
        setError((reason as Error).message);
      }
    } finally {
      setBusy(false);
    }
  }

  async function scan() {
    const code = barcode.trim();
    if (!code || busy || checkoutUncertain) return;
    setBusy(true); setError(''); setMessage('');
    try {
      const item = await api<Product>(`/api/stations/pos/barcode?code=${encodeURIComponent(code)}`);
      if ((basket.find(line => line.item.id === item.id)?.quantity || 0) >= 1000) throw new Error(tr('Maximum quantity per product is 1000.'));
      setBasket(current => {
        const existing = current.find(line => line.item.id === item.id);
        if (existing) return current.map(line => line.item.id === item.id
          ? { ...line, item, quantity: line.quantity + 1, discountAmount: Math.min(line.discountAmount, item.price * (line.quantity + 1)) }
          : line);
        return [...current, { item, quantity: 1, discountAmount: 0 }];
      });
      setTender(''); retryKey.current = ''; setReceipt(null); setBarcode('');
      setCheckoutUncertain(false);
      requestAnimationFrame(() => scanInput.current?.focus());
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(false);
    }
  }

  function updateQuantity(itemId: string, quantity: number) {
    if (checkoutUncertain || !Number.isSafeInteger(quantity) || quantity < 1 || quantity > 1000) return;
    setBasket(current => current.map(line => line.item.id === itemId ? { ...line, quantity, discountAmount: Math.min(line.discountAmount, line.item.price * quantity) } : line));
    setTender(''); retryKey.current = ''; setReceipt(null);
  }

  function updateDiscount(itemId: string, discount: number) {
    const line = basket.find(entry => entry.item.id === itemId);
    if (checkoutUncertain || !line || !Number.isSafeInteger(discount) || discount < 0 || discount > line.item.price * line.quantity) return;
    setBasket(current => current.map(entry => entry.item.id === itemId ? { ...entry, discountAmount: discount } : entry));
    setTender(''); retryKey.current = ''; setReceipt(null);
  }

  function removeItem(itemId: string) {
    setBasket(current => current.filter(line => line.item.id !== itemId));
    setTender(''); retryKey.current = ''; setReceipt(null);
  }

  async function checkout() {
    if (!activeShift || !basket.length || !Number.isSafeInteger(tenderAmount) || tenderAmount < total || busy) return;
    setBusy(true); setError(''); setMessage('');
    if (!retryKey.current) retryKey.current = `sale-${crypto.randomUUID()}`;
    try {
      const result = await post<SaleReceipt>('/api/stations/pos/cart', {
        action: 'checkout', shiftId: activeShift.id,
        lines: basket.map(line => ({ itemId: line.item.id, quantity: line.quantity, discountAmount: line.discountAmount })),
        tender: tenderAmount, idempotencyKey: retryKey.current,
      });
      setReceipt(result); setBasket([]); setTender(''); retryKey.current = '';
      setCheckoutUncertain(false);
      setMessage(tr('Sale complete'));
      try { await refreshTerminal(); } catch (reason) { setError(`${tr('Sale completed; shift status refresh failed:')} ${(reason as Error).message}`); }
      requestAnimationFrame(() => scanInput.current?.focus());
    } catch (reason) {
      // Preserve the exact request after an ambiguous outcome; only replay it.
      if (!(reason instanceof ApiError) || reason.status >= 500) {
        setCheckoutUncertain(true);
        setError(tr('Checkout outcome is unconfirmed. Retry this exact sale before changing it.'));
      } else {
        retryKey.current = '';
        setCheckoutUncertain(false);
        setError((reason as Error).message);
      }
    } finally {
      setBusy(false);
    }
  }

  return <section className="pos-cashier" aria-labelledby="pos-cashier-heading">
    <header className="pos-cashier-header">
      <div><p className="eyebrow">{tr('COUNTERPOINT / FRONT OFFICE')}</p><h1 id="pos-cashier-heading">{tr('Cashier')}</h1><p>{tr('Scan products, take cash, and issue a receipt.')}</p></div>
      <div className="pos-cashier-shift" role="status">{activeShift ? `${tr('Shift open')} · ${activeShift.id}` : tr('No open shift')}</div>
    </header>

    {error && <p className="pos-cashier-error" role="alert">{error}</p>}
    {message && <p className="pos-cashier-message" role="status">{message}</p>}

    {!activeShift && <section className="pos-cashier-panel pos-shift-open" aria-label={tr('Open a cashier shift')}>
      <h2>{tr('Open shift')}</h2>
      <label>{tr('Register')}<select aria-label={tr('Register')} value={registerId} onChange={event => setRegisterId(event.target.value)} disabled={!writable || busy || openShiftUncertain}>
        {!terminal?.registers.length && <option value="">{tr('No active registers')}</option>}
        {terminal?.registers.map(register => <option key={register.id} value={register.id}>{register.name}</option>)}
      </select></label>
      <label>{tr('Opening cash')}<input aria-label={tr('Opening cash')} inputMode="numeric" type="number" min="0" step="1" value={openingCash} onChange={event => setOpeningCash(event.target.value)} disabled={!writable || busy || openShiftUncertain}/></label>
      <button type="button" onClick={() => void openShift()} disabled={!writable || busy || !registerId}>{openShiftUncertain ? tr('Retry opening shift') : tr('Open shift')}</button>
      {!writable && <p>{tr('Read-only access cannot open a shift or complete sales.')}</p>}
    </section>}

    <div className="pos-cashier-grid">
      <section className="pos-cashier-panel pos-scan-panel" aria-label={tr('Scan products')}>
        <h2>{tr('Products')}</h2>
        <form onSubmit={event => { event.preventDefault(); void scan(); }}>
          <label htmlFor="pos-barcode">{tr('Scan barcode')}</label>
          <div className="pos-scan-row"><input id="pos-barcode" ref={scanInput} value={barcode} onChange={event => setBarcode(event.target.value)} autoComplete="off" disabled={busy || checkoutUncertain} placeholder={tr('Scan or enter a barcode')}/><button type="submit" disabled={busy || checkoutUncertain || !barcode.trim()}>{tr('Add item')}</button></div>
        </form>
        <p className="pos-cashier-note">{tr('Prices and availability are checked by the POS server. Discounts are fixed amounts per line and validated again at checkout; the server receipt is final.')}</p>
        {basket.length === 0 && <p className="pos-cashier-empty">{tr('Basket is empty. Scan an active product to begin.')}</p>}
        <ul className="pos-basket">
          {basket.map(line => <li key={line.item.id}>
            <div><strong>{line.item.name}</strong><small>{line.item.barcode} · ${line.item.price} {tr('each')}</small></div>
            <label>{tr('Quantity')}<input aria-label={`${tr('Quantity')} ${line.item.name}`} type="number" min="1" max="1000" step="1" value={line.quantity} onChange={event => updateQuantity(line.item.id, Number(event.target.value))} disabled={!writable || busy || checkoutUncertain}/></label>
            <label>{tr('Line discount')}<input aria-label={`${tr('Discount')} ${line.item.name}`} type="number" min="0" max={line.item.price * line.quantity} step="1" value={line.discountAmount} onChange={event => updateDiscount(line.item.id, Number(event.target.value))} disabled={!writable || busy || checkoutUncertain}/></label>
            <strong>${line.item.price * line.quantity - line.discountAmount}</strong>
            <button type="button" className="pos-remove" onClick={() => removeItem(line.item.id)} disabled={!writable || busy || checkoutUncertain} aria-label={`${tr('Remove')} ${line.item.name}`}>{tr('Remove')}</button>
          </li>)}
        </ul>
      </section>

      <section className="pos-cashier-panel pos-payment-panel" aria-label={tr('Cash payment')}>
        <h2>{tr('Cash payment')}</h2>
        <div className="pos-money-row"><span>{tr('Subtotal')}</span><strong>{tr('Subtotal')} ${subtotal}</strong></div>
        <div className="pos-money-row"><span>{tr('Discount')}</span><strong>-${discountAmount}</strong></div>
        <div className="pos-money-total"><span>{tr('Total due')}</span><strong>${total}</strong></div>
        <label htmlFor="pos-tender">{tr('Cash tendered')}</label>
        <input id="pos-tender" aria-label={tr('Cash tendered')} type="number" min="0" step="1" value={tender} onChange={event => { setTender(event.target.value); retryKey.current = ''; }} disabled={!writable || busy || checkoutUncertain}/>
        <div className="pos-money-row"><span>{tr('Cash change')}</span><strong>{tr('Cash change')} ${change}</strong></div>
        <button type="button" className="pos-checkout" onClick={() => void checkout()} disabled={!writable || busy || !activeShift || !basket.length || !Number.isSafeInteger(tenderAmount) || tenderAmount < total}>{checkoutUncertain ? tr('Retry same sale') : tr('Complete cash sale')}</button>
        {receipt && <div className="pos-receipt" role="status"><strong>{tr('Receipt')} {receipt.receiptNumber}</strong><span>{tr('Subtotal')} ${receipt.subtotal}</span><span>{tr('Discount')} -${receipt.discountAmount}</span><span>{tr('Total due')} ${receipt.totalAmount}</span><span>{tr('Cash change')} ${receipt.changeAmount}</span><span>{tr('Receipt amounts are calculated by the POS server.')}</span></div>}
      </section>
    </div>
  </section>;
}
