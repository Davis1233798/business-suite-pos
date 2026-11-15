import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { tr } from './locale';
import './pos-cashier.css';

type Register = { id: string; name: string; storeId: string };
type Shift = { id: string; registerId: string; storeId: string; openingCash: number; openedAt: string };
type Terminal = { registers: Register[]; openShifts: Shift[] };
type Product = { id: string; name: string; barcode: string; price: number; stockQuantity: number; isActive?: boolean };
type BasketLine = { item: Product; quantity: number; discountAmount: number };
type SaleReceipt = { saleId: string; receiptNumber: string; subtotal: number; discountAmount: number; totalAmount: number; changeAmount: number };
type HeldCartLine = { itemId: string; quantity: number; discountAmount: number; available: boolean; product: Product | null };
type HeldCart = { cartId: string; cartName: string; heldAt: string; status: 'held' | 'retrieved'; lines: HeldCartLine[] };
type HeldCartPage = { items: HeldCart[]; hasMore: boolean; nextCursor: string };
type HeldOperation = { action: 'hold' | 'claim' | 'update' | 'release' | 'discard'; body: Record<string, unknown>; idempotencyKey: string };
const HELD_CART_PAGE_SIZE = 50;

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
  const [heldCarts, setHeldCarts] = useState<HeldCart[]>([]);
  const [heldCartsShiftId, setHeldCartsShiftId] = useState('');
  const [heldCartsLoading, setHeldCartsLoading] = useState(false);
  const [heldCartsLoadingShiftId, setHeldCartsLoadingShiftId] = useState('');
  const [heldCartsError, setHeldCartsError] = useState('');
  const [heldCartsErrorShiftId, setHeldCartsErrorShiftId] = useState('');
  const [heldCartsHasMore, setHeldCartsHasMore] = useState(false);
  const [heldCartsNextCursor, setHeldCartsNextCursor] = useState('');
  const [cartName, setCartName] = useState('');
  const [claimedCartId, setClaimedCartId] = useState('');
  const [heldCartDirty, setHeldCartDirty] = useState(false);
  const [cartOperationUncertain, setCartOperationUncertain] = useState(false);
  const [cartOperationAction, setCartOperationAction] = useState<HeldOperation['action'] | ''>('');
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  const [receipt, setReceipt] = useState<SaleReceipt | null>(null);
  const retryKey = useRef('');
  const heldOperation = useRef<HeldOperation | null>(null);
  const heldReadSequence = useRef(0);
  const writeLock = useRef(false);
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
  const heldDiscountNeedsCorrection = claimedCartId !== '' && basket.some(line => line.discountAmount > line.item.price * line.quantity);
  const heldStockNeedsCorrection = claimedCartId !== '' && basket.some(line => line.item.isActive !== false && line.quantity > line.item.stockQuantity);
  const tenderAmount = Number(tender);
  const change = Number.isSafeInteger(tenderAmount) && tenderAmount >= total ? tenderAmount - total : 0;
  const activeShift = terminal?.openShifts.find(shift => shift.id === activeShiftId);
  const visibleHeldCarts = activeShift && heldCartsShiftId === activeShift.id ? heldCarts : [];
  const visibleHeldCartsHasMore = !!activeShift && heldCartsShiftId === activeShift.id && heldCartsHasMore;
  const visibleHeldCartsError = activeShift && heldCartsErrorShiftId === activeShift.id ? heldCartsError : '';
  const visibleHeldCartsLoading = activeShift && heldCartsLoadingShiftId === activeShift.id && heldCartsLoading;

  const readHeldCartPage = useCallback(async (shiftId: string, cursor = '', cartId = '') => {
    const query = new URLSearchParams({ shiftId });
    if (cartId) query.set('cartId', cartId);
    else {
      query.set('limit', String(HELD_CART_PAGE_SIZE));
      if (cursor) query.set('cursor', cursor);
    }
    const result = await api<HeldCartPage | HeldCart[]>(`/api/stations/pos/held-carts?${query.toString()}`);
    return Array.isArray(result) ? { items: result, hasMore: false, nextCursor: '' } : result;
  }, []);

  const refreshHeldCarts = useCallback(async (shiftId: string) => {
    const sequence = ++heldReadSequence.current;
    setHeldCartsLoading(true);
    setHeldCartsLoadingShiftId(shiftId);
    setHeldCartsError('');
    setHeldCartsErrorShiftId('');
    try {
      const result = await readHeldCartPage(shiftId);
      if (sequence === heldReadSequence.current) {
        setHeldCarts(result.items);
        setHeldCartsShiftId(shiftId);
        setHeldCartsHasMore(result.hasMore);
        setHeldCartsNextCursor(result.nextCursor);
      }
      return result.items;
    } catch (reason) {
      if (sequence === heldReadSequence.current) {
        setHeldCartsError(tr('Could not refresh held carts. Try again.'));
        setHeldCartsErrorShiftId(shiftId);
      }
      throw reason;
    } finally {
      if (sequence === heldReadSequence.current) setHeldCartsLoading(false);
    }
  }, [readHeldCartPage]);

  const loadMoreHeldCarts = useCallback(async (shiftId: string) => {
    const cursor = heldCartsNextCursor;
    if (!cursor || !heldCartsHasMore || heldCartsLoading || heldCartsShiftId !== shiftId) return;
    const sequence = ++heldReadSequence.current;
    setHeldCartsLoading(true);
    setHeldCartsLoadingShiftId(shiftId);
    setHeldCartsError('');
    setHeldCartsErrorShiftId('');
    try {
      const result = await readHeldCartPage(shiftId, cursor);
      if (sequence === heldReadSequence.current) {
        setHeldCarts(current => {
          const seen = new Set(current.map(cart => cart.cartId));
          return [...current, ...result.items.filter(cart => !seen.has(cart.cartId))];
        });
        setHeldCartsHasMore(result.hasMore);
        setHeldCartsNextCursor(result.nextCursor);
      }
    } catch {
      if (sequence === heldReadSequence.current) {
        setHeldCartsError(tr('Could not refresh held carts. Try again.'));
        setHeldCartsErrorShiftId(shiftId);
      }
    } finally {
      if (sequence === heldReadSequence.current) setHeldCartsLoading(false);
    }
  }, [heldCartsHasMore, heldCartsLoading, heldCartsNextCursor, heldCartsShiftId, readHeldCartPage]);

  const readOwnedHeldCart = useCallback(async (shiftId: string, cartId: string) => {
    const result = await readHeldCartPage(shiftId, '', cartId);
    return result.items.find(cart => cart.cartId === cartId && cart.status === 'retrieved');
  }, [readHeldCartPage]);

  useEffect(() => {
    if (!activeShiftId) {
      ++heldReadSequence.current;
      return;
    }
    const timer = window.setTimeout(() => { void refreshHeldCarts(activeShiftId).catch(() => undefined); }, 0);
    return () => window.clearTimeout(timer);
  }, [activeShiftId, refreshHeldCarts]);

  async function runHeldOperation() {
    const operation = heldOperation.current;
    if (!operation || writeLock.current) return;
    writeLock.current = true;
    setBusy(true); setError(''); setMessage('');
    try {
      await post('/api/stations/pos/cart', { ...operation.body, idempotencyKey: operation.idempotencyKey });
      heldOperation.current = null;
      setCartOperationUncertain(false); setCartOperationAction('');
      if (operation.action === 'hold') {
        setBasket([]); setTender(''); setCartName(''); setReceipt(null); retryKey.current = '';
        setMessage(tr('Basket held'));
      } else if (operation.action === 'claim') {
        const cartId = String(operation.body.cartId || '');
        const shiftId = String(operation.body.shiftId || '');
        let cart: HeldCart | undefined;
        try { cart = await readOwnedHeldCart(shiftId, cartId); }
        catch { /* claim is confirmed; owner can refresh to recover its persisted snapshot */ }
        if (!cart) {
          setClaimedCartId(cartId);
          setHeldCarts(current => current.map(candidate => candidate.cartId === cartId ? { ...candidate, status: 'retrieved' } : candidate));
          setMessage(tr('Claim succeeded but the cart snapshot is unavailable. Refresh before continuing.'));
        } else {
          setHeldCarts(current => current.map(candidate => candidate.cartId === cartId ? cart! : candidate));
          restoreHeldCart(cart);
          setMessage(cart.lines.some(line => !line.available)
            ? tr('Basket retrieved with unavailable items. Remove them before saving or checkout.')
            : cart.lines.some(line => line.discountAmount > (line.product?.price || 0) * line.quantity)
              ? tr('A saved discount exceeds the current price. Adjust it and save the cart before checkout.')
              : tr('Basket retrieved'));
        }
        try { await refreshHeldCarts(shiftId); } catch { /* keep the confirmed claim and restored snapshot available */ }
      } else if (operation.action === 'update') {
        setHeldCartDirty(false);
        setMessage(tr('Held cart changes saved'));
      } else if (operation.action === 'discard') {
        setBasket([]); setTender(''); setClaimedCartId(''); setHeldCartDirty(false); setCartName(''); setReceipt(null); retryKey.current = '';
        setMessage(tr('Held cart discarded'));
      } else {
        setBasket([]); setTender(''); setClaimedCartId(''); setHeldCartDirty(false); setCartName(''); setReceipt(null); retryKey.current = '';
        setMessage(tr('Basket released'));
      }
      const shiftId = String(operation.body.shiftId || '');
      if (operation.action !== 'claim') {
        try { await refreshHeldCarts(shiftId); } catch (reason) { setHeldCartsError((reason as Error).message); }
      }
    } catch (reason) {
      if (!(reason instanceof ApiError) || reason.status >= 500) {
        setCartOperationUncertain(true);
        setCartOperationAction(operation.action);
        setError(tr('Cart operation outcome is unconfirmed. Retry the same request before changing it.'));
      } else {
        heldOperation.current = null;
        setCartOperationUncertain(false); setCartOperationAction('');
        if (operation.action === 'claim' && reason.status === 409) {
          try { await refreshHeldCarts(String(operation.body.shiftId || '')); } catch { /* keep the conflict visible */ }
          setError(tr('Another cashier claimed this cart. The held list was refreshed.'));
        } else {
          setError((reason as Error).message);
        }
      }
    } finally { setBusy(false); writeLock.current = false; }
  }

  function beginHeldOperation(action: HeldOperation['action'], body: Record<string, unknown>) {
    if (writeLock.current || cartOperationUncertain || !activeShift) return;
    heldOperation.current = { action, body: structuredClone(body), idempotencyKey: `cart-${crypto.randomUUID()}` };
    setCartOperationAction(action);
    void runHeldOperation();
  }

  function holdBasket() {
    const name = cartName.trim();
    if (!name || !basket.length || !activeShift) return;
    beginHeldOperation('hold', {
      action: 'hold', shiftId: activeShift.id, cartName: name,
      lines: basket.map(line => ({ itemId: line.item.id, quantity: line.quantity, discountAmount: line.discountAmount })),
    });
  }

  function claimCart(cart: HeldCart) {
    if (cart.status === 'retrieved') { resumeCart(cart); return; }
    if (claimedCartId || basket.length) { setError(tr('Finish or release the current basket before retrieving another cart.')); return; }
    if (!activeShift) return;
    beginHeldOperation('claim', { action: 'claim', shiftId: activeShift.id, cartId: cart.cartId });
  }

  function restoreHeldCart(cart: HeldCart) {
    setBasket(cart.lines.map(line => ({ item: productFromHeldLine(line), quantity: line.quantity, discountAmount: line.discountAmount })));
    setClaimedCartId(cart.cartId); setHeldCartDirty(false); setCartName(cart.cartName);
    setTender(''); setReceipt(null); retryKey.current = '';
  }

  function productFromHeldLine(line: HeldCartLine): Product {
    return line.product || { id: line.itemId, name: tr('Unavailable product'), barcode: '', price: 0, stockQuantity: 0, isActive: false };
  }

  async function resumeCart(cart: HeldCart) {
    if (basket.length || (claimedCartId && claimedCartId !== cart.cartId)) {
      setError(tr('Finish or release the current basket before retrieving another cart.'));
      return;
    }
    if (!activeShift) return;
    try {
      const current = await readOwnedHeldCart(activeShift.id, cart.cartId);
      if (!current) throw new Error(tr('The claimed cart is no longer available to resume. Refresh the held list.'));
      setHeldCarts(carts => carts.map(candidate => candidate.cartId === cart.cartId ? current : candidate));
      restoreHeldCart(current);
      setMessage(current.lines.some(line => !line.available)
        ? tr('Basket retrieved with unavailable items. Remove them before saving or checkout.')
        : current.lines.some(line => line.discountAmount > (line.product?.price || 0) * line.quantity)
          ? tr('A saved discount exceeds the current price. Adjust it and save the cart before checkout.')
          : tr('Basket retrieved'));
    } catch (reason) {
      setError((reason as Error).message);
    }
  }

  async function refreshClaimedCartProducts(shiftId: string, cartId: string) {
    const current = await readOwnedHeldCart(shiftId, cartId);
    if (!current) return false;
    const currentLines = new Map(current.lines.map(line => [line.itemId, line]));
    setBasket(lines => lines.map(line => {
      const latest = currentLines.get(line.item.id);
      return latest ? { ...line, item: productFromHeldLine(latest) } : line;
    }));
    return true;
  }

  function saveHeldCart() {
    if (!claimedCartId || !activeShift || !basket.length || !heldCartDirty) return;
    beginHeldOperation('update', {
      action: 'update', shiftId: activeShift.id, cartId: claimedCartId,
      lines: basket.map(line => ({ itemId: line.item.id, quantity: line.quantity, discountAmount: line.discountAmount })),
    });
  }

  function releaseCart() {
    if (!claimedCartId || !activeShift) return;
    beginHeldOperation('release', { action: 'release', shiftId: activeShift.id, cartId: claimedCartId });
  }

  function discardCart() {
    if (!claimedCartId || !activeShift || (basket.length > 0 && basket.every(line => line.item.isActive !== false))) return;
    if (!window.confirm(tr('Discard this held cart permanently? This will not create a sale or change stock.'))) return;
    beginHeldOperation('discard', { action: 'discard', shiftId: activeShift.id, cartId: claimedCartId });
  }

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
    if (!code || busy || checkoutUncertain || cartOperationUncertain || claimedCartId) return;
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
    const line = basket.find(entry => entry.item.id === itemId);
    if (busy || checkoutUncertain || cartOperationUncertain || line?.item.isActive === false || (claimedCartId && line && quantity > line.item.stockQuantity) || !Number.isSafeInteger(quantity) || quantity < 1 || quantity > 1000) return;
    setBasket(current => current.map(line => line.item.id === itemId ? { ...line, quantity, discountAmount: Math.min(line.discountAmount, line.item.price * quantity) } : line));
    if (claimedCartId) setHeldCartDirty(true);
    setTender(''); retryKey.current = ''; setReceipt(null);
  }

  function updateDiscount(itemId: string, discount: number) {
    const line = basket.find(entry => entry.item.id === itemId);
    if (busy || checkoutUncertain || cartOperationUncertain || !line || line.item.isActive === false || !Number.isSafeInteger(discount) || discount < 0 || discount > line.item.price * line.quantity) return;
    setBasket(current => current.map(entry => entry.item.id === itemId ? { ...entry, discountAmount: discount } : entry));
    if (claimedCartId) setHeldCartDirty(true);
    setTender(''); retryKey.current = ''; setReceipt(null);
  }

  function removeItem(itemId: string) {
    if (busy || checkoutUncertain || cartOperationUncertain) return;
    setBasket(current => current.filter(line => line.item.id !== itemId));
    if (claimedCartId) setHeldCartDirty(true);
    setTender(''); retryKey.current = ''; setReceipt(null);
  }

  async function checkout() {
    if (!activeShift || !basket.length || heldCartDirty || heldDiscountNeedsCorrection || heldStockNeedsCorrection || basket.some(line => line.item.isActive === false) || !Number.isSafeInteger(tenderAmount) || tenderAmount < total || writeLock.current || cartOperationUncertain) return;
    writeLock.current = true;
    setBusy(true); setError(''); setMessage('');
    if (!retryKey.current) retryKey.current = `sale-${crypto.randomUUID()}`;
    try {
      const result = await post<SaleReceipt>('/api/stations/pos/cart', {
        action: 'checkout', shiftId: activeShift.id,
        ...(claimedCartId ? { cartId: claimedCartId } : { lines: basket.map(line => ({ itemId: line.item.id, quantity: line.quantity, discountAmount: line.discountAmount })) }),
        tender: tenderAmount, idempotencyKey: retryKey.current,
      });
      setReceipt(result); setBasket([]); setTender(''); retryKey.current = '';
      const completedHeldCart = claimedCartId;
      setClaimedCartId(''); setHeldCartDirty(false); setCartName('');
      setCheckoutUncertain(false);
      setMessage(tr('Sale complete'));
      if (completedHeldCart) {
        try { await refreshHeldCarts(activeShift.id); } catch (reason) { setHeldCartsError((reason as Error).message); }
      }
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
        let claimedCartRefresh: boolean | null = null;
        if (claimedCartId && reason instanceof ApiError && [400, 404, 409].includes(reason.status)) {
          try {
            claimedCartRefresh = await refreshClaimedCartProducts(activeShift.id, claimedCartId);
          } catch { /* keep the validation error visible */ }
        }
        const refreshFeedback = claimedCartRefresh === true
          ? tr('Current item details were refreshed. Review the basket and save any corrections before retrying.')
          : claimedCartRefresh === false
            ? tr('The claimed cart was not present in the refreshed list. Refresh and verify its status before retrying.')
            : claimedCartId
              ? tr('Current item details could not be refreshed. Refresh the held list and verify the claim before retrying.')
              : '';
        setError(`${(reason as Error).message}${refreshFeedback ? `. ${refreshFeedback}` : ''}`);
      }
    } finally {
      setBusy(false); writeLock.current = false;
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

    {activeShift && <section className="pos-cashier-panel pos-held-carts" aria-label={tr('Held carts')}>
      <div className="pos-held-heading"><h2>{tr('Held carts')}</h2><button type="button" className="pos-secondary" onClick={() => void refreshHeldCarts(activeShift.id).catch(() => undefined)} disabled={busy || cartOperationUncertain || heldCartsLoading}>{heldCartsLoading ? tr('Refreshing') : tr('Refresh held carts')}</button></div>
      {visibleHeldCartsError && <p className="pos-cashier-note" role="status">{tr('Cannot load held carts')}. {visibleHeldCartsError}</p>}
      {cartOperationUncertain && <div className="pos-held-retry" role="group" aria-label={tr('Unconfirmed cart operation')}>
        <p>{tr('Cart operation outcome is unconfirmed. Retry the same request before changing it.')}</p>
        <button type="button" onClick={() => void runHeldOperation()} disabled={busy}>{tr('Retry')} {tr(cartOperationAction || 'cart operation')}</button>
      </div>}
      {visibleHeldCarts.length === 0 && !visibleHeldCartsError && <p className="pos-cashier-empty">{visibleHeldCartsLoading ? tr('Loading held carts') : tr('No held carts for this shift.')}</p>}
      <ul className="pos-held-list">
        {visibleHeldCarts.map(cart => <li key={cart.cartId}>
          <div className="pos-held-summary"><strong>{cart.cartName}</strong><small>{cart.lines.length} {tr('items')} · {cart.lines.reduce((sum, line) => sum + line.quantity, 0)} {tr('units')} · {tr('Held at')} {cart.heldAt}</small>
            {cart.lines.some(line => !line.available) && <small className="pos-held-unavailable">{tr('Contains unavailable items; cannot be claimed.')}</small>}
          </div>
          <button type="button" onClick={() => claimCart(cart)} disabled={!writable || busy || cartOperationUncertain || (!!claimedCartId && (cart.cartId !== claimedCartId || basket.length > 0)) || (!claimedCartId && basket.length > 0)} aria-label={`${tr(cart.status === 'retrieved' ? 'Resume' : 'Claim')} ${cart.cartName}`}>{tr(cart.status === 'retrieved' ? 'Resume' : 'Claim')} {cart.cartName}</button>
        </li>)}
      </ul>
      {visibleHeldCartsHasMore && <button type="button" className="pos-secondary" onClick={() => void loadMoreHeldCarts(activeShift.id)} disabled={busy || cartOperationUncertain || heldCartsLoading}>{heldCartsLoading ? tr('Loading more held carts') : tr('Load more held carts')}</button>}
    </section>}

    <div className="pos-cashier-grid">
      <section className="pos-cashier-panel pos-scan-panel" aria-label={tr('Scan products')}>
        <h2>{tr('Products')}</h2>
        <form onSubmit={event => { event.preventDefault(); void scan(); }}>
          <label htmlFor="pos-barcode">{tr('Scan barcode')}</label>
          <div className="pos-scan-row"><input id="pos-barcode" ref={scanInput} value={barcode} onChange={event => setBarcode(event.target.value)} autoComplete="off" disabled={busy || checkoutUncertain || cartOperationUncertain || !!claimedCartId} placeholder={tr('Scan or enter a barcode')}/><button type="submit" disabled={busy || checkoutUncertain || cartOperationUncertain || !!claimedCartId || !barcode.trim()}>{tr('Add item')}</button></div>
        </form>
        <p className="pos-cashier-note">{tr('Prices and availability are checked by the POS server. Discounts are fixed amounts per line and validated again at checkout; the server receipt is final.')}</p>
        {claimedCartId && <div className="pos-claimed-cart" role="status"><span>{tr('Retrieved cart')}: {cartName}</span><button type="button" className="pos-secondary" onClick={releaseCart} disabled={!writable || busy || cartOperationUncertain}>{tr('Release held cart')}</button></div>}
        {claimedCartId && (!basket.length || basket.some(line => line.item.isActive === false)) && <button type="button" className="pos-discard" onClick={discardCart} disabled={!writable || busy || cartOperationUncertain}>{tr('Discard held cart')}</button>}
        {claimedCartId && (!basket.length || basket.some(line => line.item.isActive === false)) && <p className="pos-cashier-note">{tr('This cart cannot be saved or checked out while required items are unavailable. You can release it unchanged or explicitly discard it.')}</p>}
        {claimedCartId && <p className="pos-cashier-note">{tr('Current prices and basket totals are estimates. Save cart changes before checkout; the server rechecks prices and discounts and its receipt is final.')}</p>}
        {basket.length === 0 && <p className="pos-cashier-empty">{tr('Basket is empty. Scan an active product to begin.')}</p>}
        <ul className="pos-basket">
          {basket.map(line => <li key={line.item.id}>
            <div><strong>{line.item.name}</strong><small>{line.item.barcode} · ${line.item.price} {tr('each')}{claimedCartId ? ` · ${tr('Stock')}: ${line.item.stockQuantity}` : ''}</small></div>
            <label>{tr('Quantity')}<input aria-label={`${tr('Quantity')} ${line.item.name}`} type="number" min="1" max={claimedCartId ? Math.min(1000, line.item.stockQuantity) : 1000} step="1" value={line.quantity} onChange={event => updateQuantity(line.item.id, Number(event.target.value))} disabled={!writable || busy || checkoutUncertain || cartOperationUncertain || line.item.isActive === false}/></label>
            <label>{tr('Line discount')}<input aria-label={`${tr('Discount')} ${line.item.name}`} type="number" min="0" max={line.item.price * line.quantity} step="1" value={line.discountAmount} onChange={event => updateDiscount(line.item.id, Number(event.target.value))} disabled={!writable || busy || checkoutUncertain || cartOperationUncertain || line.item.isActive === false}/></label>
            <strong>${line.item.price * line.quantity - line.discountAmount}</strong>
            <button type="button" className="pos-remove" onClick={() => removeItem(line.item.id)} disabled={!writable || busy || checkoutUncertain || cartOperationUncertain} aria-label={`${tr('Remove')} ${line.item.name}`}>{tr('Remove')}</button>
          </li>)}
        </ul>
        {claimedCartId && <div className="pos-hold-controls">
          <button type="button" onClick={saveHeldCart} disabled={!writable || busy || cartOperationUncertain || !heldCartDirty || !basket.length || basket.some(line => line.item.isActive === false)}>{tr('Save cart changes')}</button>
        </div>}
        {!claimedCartId && <div className="pos-hold-controls">
          <label>{tr('Cart name')}<input aria-label={tr('Cart name')} maxLength={100} value={cartName} onChange={event => setCartName(event.target.value)} disabled={!writable || busy || checkoutUncertain || cartOperationUncertain}/></label>
          <button type="button" onClick={holdBasket} disabled={!writable || busy || checkoutUncertain || cartOperationUncertain || !basket.length || !cartName.trim()}>{tr('Hold basket')}</button>
        </div>}
      </section>

      <section className="pos-cashier-panel pos-payment-panel" aria-label={tr('Cash payment')}>
        <h2>{tr('Cash payment')}</h2>
        <div className="pos-money-row"><span>{tr('Subtotal')}</span><strong>{tr('Subtotal')} ${subtotal}</strong></div>
        <div className="pos-money-row"><span>{tr('Discount')}</span><strong>-${discountAmount}</strong></div>
        <div className="pos-money-total"><span>{tr('Total due')}</span><strong>${total}</strong></div>
        <label htmlFor="pos-tender">{tr('Cash tendered')}</label>
        <input id="pos-tender" aria-label={tr('Cash tendered')} type="number" min="0" step="1" value={tender} onChange={event => { setTender(event.target.value); retryKey.current = ''; }} disabled={!writable || busy || checkoutUncertain || cartOperationUncertain}/>
        <div className="pos-money-row"><span>{tr('Cash change')}</span><strong>{tr('Cash change')} ${change}</strong></div>
        {heldDiscountNeedsCorrection && <p className="pos-cashier-error" role="status">{tr('A saved discount exceeds the current price. Adjust it and save the cart before checkout.')}</p>}
        {heldStockNeedsCorrection && <p className="pos-cashier-error" role="status">{tr('Current stock is below the held quantity. Adjust the quantity and save the cart before checkout.')}</p>}
        <button type="button" className="pos-checkout" onClick={() => void checkout()} disabled={!writable || busy || cartOperationUncertain || !activeShift || !basket.length || heldCartDirty || heldDiscountNeedsCorrection || heldStockNeedsCorrection || basket.some(line => line.item.isActive === false) || !Number.isSafeInteger(tenderAmount) || tenderAmount < total}>{checkoutUncertain ? tr('Retry same sale') : tr('Complete cash sale')}</button>
        {receipt && <div className="pos-receipt" role="status"><strong>{tr('Receipt')} {receipt.receiptNumber}</strong><span>{tr('Subtotal')} ${receipt.subtotal}</span><span>{tr('Discount')} -${receipt.discountAmount}</span><span>{tr('Total due')} ${receipt.totalAmount}</span><span>{tr('Cash change')} ${receipt.changeAmount}</span><span>{tr('Receipt amounts are calculated by the POS server.')}</span></div>}
      </section>
    </div>
  </section>;
}
