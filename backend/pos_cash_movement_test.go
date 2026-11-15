package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type posCashFixture struct {
	a                                     *App
	dbPath, org, shift                    string
	admin, front, office, viewer, foreign []*http.Cookie
}

func newPOSCashFixture(t *testing.T) posCashFixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "cash.sqlite")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := &App{db: db}
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "pos")
	front := loginSite(t, a, "front@suite.test", space, "pos")
	office := loginSite(t, a, "office@suite.test", space, "pos")
	viewer := loginSite(t, a, "viewer@suite.test", space, "pos")
	foreign := loginSite(t, a, "admin@suite.test", token(), "pos")
	var shift string
	if err := a.db.QueryRow(`SELECT id FROM pos_shifts WHERE organization_id=? AND status='open' ORDER BY id LIMIT 1`, org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE pos_shifts SET cashier_id=?,opening_cash=100 WHERE organization_id=? AND id=?`, org+"_front", org, shift); err != nil {
		t.Fatal(err)
	}
	return posCashFixture{a: a, dbPath: dbPath, org: org, shift: shift, admin: admin, front: front, office: office, viewer: viewer, foreign: foreign}
}

func cashMovement(shift, movementType string, amount int64, reason, key string) map[string]any {
	return map[string]any{"action": "cash-movement", "shiftId": shift, "movementType": movementType, "amount": amount, "reason": reason, "idempotencyKey": key}
}

func seedCashRefundSale(t *testing.T, f posCashFixture) (string, string) {
	t.Helper()
	var store, register, cashier, item string
	if err := f.a.db.QueryRow(`SELECT store_id,register_id,cashier_id FROM pos_shifts WHERE organization_id=? AND id=?`, f.org, f.shift).Scan(&store, &register, &cashier); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT id FROM pos_catalog_items WHERE organization_id=? ORDER BY id LIMIT 1`, f.org).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.db.Exec(`UPDATE pos_catalog_items SET stock_quantity=10 WHERE organization_id=? AND id=?`, f.org, item); err != nil {
		t.Fatal(err)
	}
	sale, line := "sale_cash_refund_"+token(), "line_cash_refund_"+token()
	if _, err := f.a.db.Exec(`INSERT INTO pos_sales(id,organization_id,receipt_number,store_id,register_id,shift_id,cashier_id,subtotal,discount_amount,tax_amount,total_amount,tender_amount,change_amount,payment_method,status,idempotency_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sale, f.org, "POS-"+token()[:12], store, register, f.shift, cashier, 101, 0, 0, 101, 101, 0, "cash", "completed", "fixture-"+token()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.db.Exec(`INSERT INTO pos_sale_lines(id,organization_id,sale_id,item_id,barcode,name,unit_price,quantity,discount_amount,line_total,refunded_quantity) VALUES(?,?,?,?,?,?,?,?,?,?,0)`, line, f.org, sale, item, "CASH-REFUND", "Cash drawer refund item", 34, 3, 1, 101); err != nil {
		t.Fatal(err)
	}
	return sale, line
}

func cashRefund(line, reason, key string) map[string]any {
	return map[string]any{"action": "refund", "saleLineId": line, "quantity": 1, "reason": reason, "idempotencyKey": key}
}

func TestPOSCashMovementsAndCloseUseAuthoritativeDrawerTotals(t *testing.T) {
	f := newPOSCashFixture(t)
	path := "/api/stations/pos/business"
	in := cashMovement(f.shift, "in", 50, "Float top-up", "cash-in-key-0001")
	w := request(f.a, http.MethodPost, path, in, f.front...)
	if w.Code != http.StatusOK {
		t.Fatalf("cash in: %d %s", w.Code, w.Body.String())
	}
	first := w.Body.String()
	if replay := request(f.a, http.MethodPost, path, in, f.front...); replay.Code != http.StatusOK || replay.Body.String() != first {
		t.Fatalf("cash-in replay: %d %s; original %s", replay.Code, replay.Body.String(), first)
	}
	changed := cashMovement(f.shift, "in", 51, "Float top-up", "cash-in-key-0001")
	if w := request(f.a, http.MethodPost, path, changed, f.front...); w.Code != http.StatusConflict {
		t.Fatalf("changed movement retry should conflict: %d %s", w.Code, w.Body.String())
	}
	out := cashMovement(f.shift, "out", 20, "Petty cash", "cash-out-key-0001")
	if w := request(f.a, http.MethodPost, path, out, f.front...); w.Code != http.StatusOK {
		t.Fatalf("cash out: %d %s", w.Code, w.Body.String())
	}
	terminal := request(f.a, http.MethodGet, "/api/stations/pos/terminal", nil, f.front...)
	var terminalData struct {
		Data struct {
			OpenShifts []struct {
				ID       string `json:"id"`
				Expected int64  `json:"expectedCash"`
			} `json:"openShifts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(terminal.Body.Bytes(), &terminalData); err != nil {
		t.Fatal(err)
	}
	if terminal.Code != http.StatusOK || len(terminalData.Data.OpenShifts) != 1 || terminalData.Data.OpenShifts[0].ID != f.shift || terminalData.Data.OpenShifts[0].Expected != 130 {
		t.Fatalf("terminal current drawer did not include movements: %d %#v", terminal.Code, terminalData.Data.OpenShifts)
	}
	close := map[string]any{"action": "close", "shiftId": f.shift, "closingCash": 125, "notes": "Drawer was short by five after count", "idempotencyKey": "cash-close-key-01"}
	w = request(f.a, http.MethodPost, path, close, f.front...)
	if w.Code != http.StatusOK {
		t.Fatalf("close: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Expected int64 `json:"expectedCash"`
			Counted  int64 `json:"closingCash"`
			Variance int64 `json:"discrepancy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Expected != 130 || response.Data.Counted != 125 || response.Data.Variance != -5 {
		t.Fatalf("unexpected authoritative close result: %#v", response.Data)
	}
	if replay := request(f.a, http.MethodPost, path, close, f.front...); replay.Code != http.StatusOK || replay.Body.String() != w.Body.String() {
		t.Fatalf("close replay: %d %s", replay.Code, replay.Body.String())
	}
	changedClose := map[string]any{"action": "close", "shiftId": f.shift, "closingCash": 130, "notes": "Corrected count", "idempotencyKey": "cash-close-key-01"}
	if w := request(f.a, http.MethodPost, path, changedClose, f.front...); w.Code != http.StatusConflict {
		t.Fatalf("changed close retry should conflict: %d %s", w.Code, w.Body.String())
	}
	var movements, audits, keys int
	f.a.db.QueryRow(`SELECT count(*) FROM pos_cash_movements WHERE organization_id=? AND shift_id=?`, f.org, f.shift).Scan(&movements)
	f.a.db.QueryRow(`SELECT count(*) FROM audit_events WHERE organization_id=? AND action='pos.cash-movement'`, f.org).Scan(&audits)
	f.a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND endpoint IN('/station/pos/cash-movement','/station/pos/close')`, f.org).Scan(&keys)
	if movements != 2 || audits != 2 || keys != 3 {
		t.Fatalf("movement/audit/idempotency rows %d/%d/%d", movements, audits, keys)
	}
}

func TestPOSCashMovementValidatesAmountTypeReasonAndDrawerBalance(t *testing.T) {
	f := newPOSCashFixture(t)
	cases := []struct {
		name, typ string
		amount    int64
		reason    string
	}{
		{name: "zero", typ: "in", amount: 0, reason: "valid"}, {name: "negative", typ: "out", amount: -1, reason: "valid"},
		{name: "over limit", typ: "in", amount: 1_000_000_001, reason: "valid"}, {name: "unknown type", typ: "transfer", amount: 1, reason: "valid"},
		{name: "blank reason", typ: "in", amount: 1, reason: "  "}, {name: "reason UTF-8 bytes", typ: "in", amount: 1, reason: strings.Repeat("中", 67)},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := cashMovement(f.shift, tc.typ, tc.amount, tc.reason, fmt.Sprintf("cash-invalid-%02d", index))
			w := request(f.a, http.MethodPost, "/api/stations/pos/business", in, f.front...)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"outcome":"not_applied"`) {
				t.Fatalf("invalid input should be terminal 400: %d %s", w.Code, w.Body.String())
			}
			if replay := request(f.a, http.MethodPost, "/api/stations/pos/business", in, f.front...); replay.Code != w.Code || replay.Body.String() != w.Body.String() {
				t.Fatalf("invalid movement replay: %d %s; first %s", replay.Code, replay.Body.String(), w.Body.String())
			}
		})
	}
	w := request(f.a, http.MethodPost, "/api/stations/pos/business", cashMovement(f.shift, "out", 101, "too much", "cash-overdraft-01"), f.front...)
	if w.Code != http.StatusConflict {
		t.Fatalf("overdraft should conflict: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"outcome":"not_applied"`) {
		t.Fatalf("overdraft rejection must be definitive: %s", w.Body.String())
	}
	if replay := request(f.a, http.MethodPost, "/api/stations/pos/business", cashMovement(f.shift, "out", 101, "too much", "cash-overdraft-01"), f.front...); replay.Code != w.Code || replay.Body.String() != w.Body.String() {
		t.Fatalf("overdraft replay: %d %s; first %s", replay.Code, replay.Body.String(), w.Body.String())
	}
	var rows int
	f.a.db.QueryRow(`SELECT count(*) FROM pos_cash_movements WHERE organization_id=? AND shift_id=?`, f.org, f.shift).Scan(&rows)
	if rows != 0 {
		t.Fatalf("rejected movements were persisted: %d", rows)
	}
}

func TestPOSCashInCanRepairLegacyNegativeExpectedCash(t *testing.T) {
	f := newPOSCashFixture(t)
	if _, err := f.a.db.Exec(`UPDATE pos_shifts SET opening_cash=0 WHERE organization_id=? AND id=?`, f.org, f.shift); err != nil {
		t.Fatal(err)
	}
	// Model a legacy P05 state with a cash-out larger than the source cash. P06
	// does not rewrite it; the cashier must record an explicit cash-in.
	if _, err := f.a.db.Exec(`INSERT INTO pos_cash_movements(id,organization_id,shift_id,movement_type,amount,reason) VALUES(?,?,?,?,?,?)`, "legacy-negative-cashout", f.org, f.shift, "out", 20, "Legacy movement"); err != nil {
		t.Fatal(err)
	}
	before, err := posShiftExpectedCash(f.a.db, f.org, f.shift, 0)
	if err != nil {
		t.Fatal(err)
	}
	if before != -20 {
		t.Fatalf("legacy expected cash=%d want -20", before)
	}
	input := cashMovement(f.shift, "in", 20, "Cash deposit to reconcile legacy drawer", "legacy-recovery-cashin")
	w := request(f.a, http.MethodPost, "/api/stations/pos/business", input, f.front...)
	if w.Code != http.StatusOK {
		t.Fatalf("cash-in should repair a negative legacy drawer: %d %s", w.Code, w.Body.String())
	}
	after, err := posShiftExpectedCash(f.a.db, f.org, f.shift, 0)
	if err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Fatalf("cash-in recovery expected cash=%d want 0", after)
	}
	var movements int
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_cash_movements WHERE organization_id=? AND shift_id=?`, f.org, f.shift).Scan(&movements); err != nil {
		t.Fatal(err)
	}
	if movements != 2 {
		t.Fatalf("legacy state should be preserved and one explicit cash-in appended; movement count=%d", movements)
	}
}

func TestPOSCashMovementScopesShiftAndRoles(t *testing.T) {
	f := newPOSCashFixture(t)
	input := cashMovement(f.shift, "in", 10, "audit float", "cash-scope-key-01")
	if w := request(f.a, http.MethodPost, "/api/stations/pos/business", input, f.office...); w.Code != http.StatusForbidden {
		t.Fatalf("other operator should not alter cashier shift: %d %s", w.Code, w.Body.String())
	}
	if w := request(f.a, http.MethodPost, "/api/stations/pos/business", input, f.viewer...); w.Code != http.StatusForbidden {
		t.Fatalf("readonly should not alter drawer: %d %s", w.Code, w.Body.String())
	}
	if w := request(f.a, http.MethodPost, "/api/stations/pos/business", input, f.foreign...); w.Code != http.StatusNotFound {
		t.Fatalf("foreign shift should be scoped: %d %s", w.Code, w.Body.String())
	}
	if w := request(f.a, http.MethodPost, "/api/stations/pos/business", input, f.admin...); w.Code != http.StatusOK {
		t.Fatalf("back-office admin exception: %d %s", w.Code, w.Body.String())
	}
}

func TestPOSRefundCannotMakeDrawerNegativeAfterCashOut(t *testing.T) {
	f := newPOSCashFixture(t)
	_, line := seedCashRefundSale(t, f)
	if _, err := f.a.db.Exec(`UPDATE pos_shifts SET opening_cash=0 WHERE organization_id=? AND id=?`, f.org, f.shift); err != nil {
		t.Fatal(err)
	}
	path := "/api/stations/pos/business"
	out := cashMovement(f.shift, "out", 100, "Cash withdrawn before return", "refund-liquidity-out")
	if w := request(f.a, http.MethodPost, path, out, f.front...); w.Code != http.StatusOK {
		t.Fatalf("cash out should leave one dollar: %d %s", w.Code, w.Body.String())
	}
	refund := cashRefund(line, "Partial cash return", "refund-liquidity-001")
	denied := request(f.a, http.MethodPost, path, refund, f.admin...)
	if denied.Code != http.StatusConflict || !strings.Contains(denied.Body.String(), `"outcome":"not_applied"`) || !strings.Contains(denied.Body.String(), "Cash refund exceeds the current expected drawer balance") {
		t.Fatalf("illiquid refund should be a definitive conflict: %d %s", denied.Code, denied.Body.String())
	}
	if replay := request(f.a, http.MethodPost, path, refund, f.admin...); replay.Code != denied.Code || replay.Body.String() != denied.Body.String() {
		t.Fatalf("liquidity rejection should replay: %d %s; first %s", replay.Code, replay.Body.String(), denied.Body.String())
	}
	var refunded, stock, refunds int
	f.a.db.QueryRow(`SELECT refunded_quantity FROM pos_sale_lines WHERE organization_id=? AND id=?`, f.org, line).Scan(&refunded)
	f.a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=(SELECT item_id FROM pos_sale_lines WHERE organization_id=? AND id=?)`, f.org, f.org, line).Scan(&stock)
	f.a.db.QueryRow(`SELECT count(*) FROM pos_refunds WHERE organization_id=?`, f.org).Scan(&refunds)
	if refunded != 0 || stock != 10 || refunds != 0 {
		t.Fatalf("rejected refund effects refunded=%d stock=%d headers=%d", refunded, stock, refunds)
	}
	if w := request(f.a, http.MethodPost, path, cashMovement(f.shift, "in", 32, "Replenish drawer", "refund-liquidity-in"), f.front...); w.Code != http.StatusOK {
		t.Fatalf("cash replenishment: %d %s", w.Code, w.Body.String())
	}
	if w := request(f.a, http.MethodPost, path, cashRefund(line, "Partial cash return", "refund-liquidity-002"), f.admin...); w.Code != http.StatusOK {
		t.Fatalf("refund after liquidity restored: %d %s", w.Code, w.Body.String())
	}
}

func TestPOSShiftCloseRequiresAndStoresVarianceExplanation(t *testing.T) {
	f := newPOSCashFixture(t)
	path := "/api/stations/pos/business"
	withoutNotes := map[string]any{"action": "close", "shiftId": f.shift, "closingCash": 95, "idempotencyKey": "cash-close-no-note"}
	w := request(f.a, http.MethodPost, path, withoutNotes, f.front...)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"outcome":"not_applied"`) {
		t.Fatalf("variance without reason must be a terminal validation error: %d %s", w.Code, w.Body.String())
	}
	longNote := map[string]any{"action": "close", "shiftId": f.shift, "closingCash": 95, "notes": strings.Repeat("中", 67), "idempotencyKey": "cash-close-long-note"}
	if w := request(f.a, http.MethodPost, path, longNote, f.front...); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"outcome":"not_applied"`) {
		t.Fatalf("overlong UTF-8 variance note must be rejected: %d %s", w.Code, w.Body.String())
	}
	withNotes := map[string]any{"action": "close", "shiftId": f.shift, "closingCash": 95, "notes": "Drawer count is five short", "idempotencyKey": "cash-close-with-note"}
	w = request(f.a, http.MethodPost, path, withNotes, f.front...)
	if w.Code != http.StatusOK {
		t.Fatalf("close with variance explanation: %d %s", w.Code, w.Body.String())
	}
	var notes string
	if err := f.a.db.QueryRow(`SELECT notes FROM pos_shifts WHERE organization_id=? AND id=?`, f.org, f.shift).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if notes != "Drawer count is five short" {
		t.Fatalf("stored close explanation %q", notes)
	}
}

func TestPOSShiftCloseTerminallyReservesInvalidCountedCashForOldPendingRetry(t *testing.T) {
	f := newPOSCashFixture(t)
	path := "/api/stations/pos/business"
	key := "cash-close-old-invalid"
	invalid := map[string]any{"action": "close", "shiftId": f.shift, "closingCash": int64(1_000_000_001), "notes": "Old saved attempt", "idempotencyKey": key}
	first := request(f.a, http.MethodPost, path, invalid, f.front...)
	if first.Code != http.StatusBadRequest || !strings.Contains(first.Body.String(), `"outcome":"not_applied"`) {
		t.Fatalf("invalid saved close must receive a durable no-effect result: %d %s", first.Code, first.Body.String())
	}
	if replay := request(f.a, http.MethodPost, path, invalid, f.front...); replay.Code != first.Code || replay.Body.String() != first.Body.String() {
		t.Fatalf("same invalid close retry must replay its terminal response: %d %s; first %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	correctedSameKey := map[string]any{"action": "close", "shiftId": f.shift, "closingCash": int64(100), "idempotencyKey": key}
	if changed := request(f.a, http.MethodPost, path, correctedSameKey, f.front...); changed.Code != http.StatusConflict || strings.Contains(changed.Body.String(), `"outcome":"not_applied"`) {
		t.Fatalf("corrected close cannot reuse terminally rejected key: %d %s", changed.Code, changed.Body.String())
	}
	missingShift := map[string]any{"action": "close", "shiftId": "", "closingCash": int64(100), "idempotencyKey": "cash-close-old-empty"}
	emptyRejected := request(f.a, http.MethodPost, path, missingShift, f.front...)
	if emptyRejected.Code != http.StatusBadRequest || !strings.Contains(emptyRejected.Body.String(), `"outcome":"not_applied"`) {
		t.Fatalf("malformed old close without a shift must be safely rejected: %d %s", emptyRejected.Code, emptyRejected.Body.String())
	}
	if replay := request(f.a, http.MethodPost, path, missingShift, f.front...); replay.Code != emptyRejected.Code || replay.Body.String() != emptyRejected.Body.String() {
		t.Fatalf("malformed no-shift close should replay its terminal response: %d %s; first %s", replay.Code, replay.Body.String(), emptyRejected.Body.String())
	}
	correctedNewKey := map[string]any{"action": "close", "shiftId": f.shift, "closingCash": int64(100), "idempotencyKey": "cash-close-corrected"}
	if accepted := request(f.a, http.MethodPost, path, correctedNewKey, f.front...); accepted.Code != http.StatusOK {
		t.Fatalf("corrected close with a fresh key should succeed: %d %s", accepted.Code, accepted.Body.String())
	}
	var closed int
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_shifts WHERE organization_id=? AND id=? AND status='closed' AND closing_cash=100`, f.org, f.shift).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("corrected close state count=%d", closed)
	}
}

func TestPOSCashOutRaceAcrossDBHandlesCannotOverdraw(t *testing.T) {
	f := newPOSCashFixture(t)
	if _, err := f.a.db.Exec(`UPDATE pos_shifts SET opening_cash=10 WHERE organization_id=? AND id=?`, f.org, f.shift); err != nil {
		t.Fatal(err)
	}
	otherDB, err := openDB(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	other := &App{db: otherDB}
	key1, key2 := "cash-race-out-0001", "cash-race-out-0002"
	inputs := []map[string]any{cashMovement(f.shift, "out", 8, "cash out", key1), cashMovement(f.shift, "out", 8, "cash out", key2)}
	apps := []*App{f.a, other}
	type result struct {
		index, code int
		body        string
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range inputs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			w := request(apps[i], http.MethodPost, "/api/stations/pos/business", inputs[i], f.front...)
			results <- result{i, w.Code, w.Body.String()}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	codes := make([]int, 2)
	for r := range results {
		codes[r.index] = r.code
		if r.code != http.StatusOK && r.code != http.StatusServiceUnavailable && r.code != http.StatusConflict {
			t.Errorf("unexpected cash race response: %d %s", r.code, r.body)
		}
	}
	for i := 0; i < 2; i++ {
		if codes[i] == http.StatusServiceUnavailable {
			w := request(apps[i], http.MethodPost, "/api/stations/pos/business", inputs[i], f.front...)
			codes[i] = w.Code
			if w.Code != http.StatusOK && w.Code != http.StatusConflict {
				t.Fatalf("retry after lock: %d %s", w.Code, w.Body.String())
			}
		}
	}
	success := 0
	for _, code := range codes {
		if code == http.StatusOK {
			success++
		}
	}
	var movements, netOut int
	f.a.db.QueryRow(`SELECT count(*),COALESCE(SUM(CASE WHEN movement_type='out' THEN amount ELSE -amount END),0) FROM pos_cash_movements WHERE organization_id=? AND shift_id=?`, f.org, f.shift).Scan(&movements, &netOut)
	if success != 1 || movements != 1 || netOut != 8 {
		t.Fatalf("cash race success=%d movements=%d netOut=%d", success, movements, netOut)
	}
}

func TestPOSRefundRaceWithCashOutAcrossDBHandlesCannotOverdraw(t *testing.T) {
	f := newPOSCashFixture(t)
	_, line := seedCashRefundSale(t, f)
	if _, err := f.a.db.Exec(`UPDATE pos_shifts SET opening_cash=0 WHERE organization_id=? AND id=?`, f.org, f.shift); err != nil {
		t.Fatal(err)
	}
	otherDB, err := openDB(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	other := &App{db: otherDB}
	refund := cashRefund(line, "Partial cash return", "race-liquidity-refund")
	out := cashMovement(f.shift, "out", 80, "Safe drop", "race-liquidity-out")
	type result struct {
		action string
		code   int
		body   string
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	go func() {
		<-start
		w := request(f.a, http.MethodPost, "/api/stations/pos/business", refund, f.admin...)
		results <- result{"refund", w.Code, w.Body.String()}
	}()
	go func() {
		<-start
		w := request(other, http.MethodPost, "/api/stations/pos/business", out, f.front...)
		results <- result{"out", w.Code, w.Body.String()}
	}()
	close(start)
	first, second := <-results, <-results
	resultsByAction := map[string]result{first.action: first, second.action: second}
	for action, r := range resultsByAction {
		if r.code == http.StatusServiceUnavailable {
			app, cookies, input := f.a, f.admin, refund
			if action == "out" {
				app, cookies, input = other, f.front, out
			}
			w := request(app, http.MethodPost, "/api/stations/pos/business", input, cookies...)
			resultsByAction[action] = result{action, w.Code, w.Body.String()}
		}
	}
	rRefund, rOut := resultsByAction["refund"], resultsByAction["out"]
	for _, r := range []result{rRefund, rOut} {
		if r.code != http.StatusOK && r.code != http.StatusConflict {
			t.Fatalf("unexpected refund/cash-out race response: %s %d %s", r.action, r.code, r.body)
		}
		if r.code == http.StatusConflict && !strings.Contains(r.body, `"outcome":"not_applied"`) {
			t.Fatalf("terminal liquidity conflict missing marker: %s", r.body)
		}
	}
	var refunded, refunds, movements int
	if err := f.a.db.QueryRow(`SELECT refunded_quantity FROM pos_sale_lines WHERE organization_id=? AND id=?`, f.org, line).Scan(&refunded); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_refunds WHERE organization_id=?`, f.org).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_cash_movements WHERE organization_id=? AND shift_id=?`, f.org, f.shift).Scan(&movements); err != nil {
		t.Fatal(err)
	}
	if (rRefund.code == http.StatusOK) == (rOut.code == http.StatusOK) || refunds != refunded || refunds+movements != 1 {
		t.Fatalf("both liquidity-changing writes must not succeed: refund=%d/%d out=%d refunds=%d qty=%d movements=%d", rRefund.code, rOut.code, rOut.code, refunds, refunded, movements)
	}
	var opening int64
	if err := f.a.db.QueryRow(`SELECT opening_cash FROM pos_shifts WHERE organization_id=? AND id=?`, f.org, f.shift).Scan(&opening); err != nil {
		t.Fatal(err)
	}
	current, err := posShiftExpectedCash(f.a.db, f.org, f.shift, opening)
	if err != nil {
		t.Fatal(err)
	}
	if current < 0 {
		t.Fatalf("drawer became negative: %d", current)
	}
}

func TestPOSRefundRaceWithShiftCloseAcrossDBHandlesUsesOneCashSnapshot(t *testing.T) {
	f := newPOSCashFixture(t)
	_, line := seedCashRefundSale(t, f)
	if _, err := f.a.db.Exec(`UPDATE pos_shifts SET opening_cash=0 WHERE organization_id=? AND id=?`, f.org, f.shift); err != nil {
		t.Fatal(err)
	}
	otherDB, err := openDB(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	other := &App{db: otherDB}
	refund := cashRefund(line, "Partial cash return", "race-close-refund")
	closeInput := map[string]any{"action": "close", "shiftId": f.shift, "closingCash": 0, "notes": "Counted drawer at close", "idempotencyKey": "race-close-cash-key"}
	type result struct {
		action string
		code   int
		body   string
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	go func() {
		<-start
		w := request(f.a, http.MethodPost, "/api/stations/pos/business", refund, f.admin...)
		results <- result{"refund", w.Code, w.Body.String()}
	}()
	go func() {
		<-start
		w := request(other, http.MethodPost, "/api/stations/pos/business", closeInput, f.front...)
		results <- result{"close", w.Code, w.Body.String()}
	}()
	close(start)
	first, second := <-results, <-results
	byAction := map[string]result{first.action: first, second.action: second}
	for action, r := range byAction {
		if r.code == http.StatusServiceUnavailable {
			app, cookies, input := f.a, f.admin, refund
			if action == "close" {
				app, cookies, input = other, f.front, closeInput
			}
			w := request(app, http.MethodPost, "/api/stations/pos/business", input, cookies...)
			byAction[action] = result{action, w.Code, w.Body.String()}
		}
	}
	rRefund, rClose := byAction["refund"], byAction["close"]
	if rClose.code != http.StatusOK {
		t.Fatalf("close race result failed: %d %s", rClose.code, rClose.body)
	}
	var closeResult struct {
		Data struct {
			Expected int64 `json:"expectedCash"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(rClose.body), &closeResult); err != nil {
		t.Fatal(err)
	}
	var refunds int
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_refunds WHERE organization_id=?`, f.org).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if rRefund.code == http.StatusOK {
		if closeResult.Data.Expected != 68 || refunds != 1 {
			t.Fatalf("refund committed but close omitted it: expected=%d refunds=%d close=%s", closeResult.Data.Expected, refunds, rClose.body)
		}
	} else if rRefund.code == http.StatusConflict {
		if !strings.Contains(rRefund.body, `"outcome":"not_applied"`) || closeResult.Data.Expected != 101 || refunds != 0 {
			t.Fatalf("close won but refund was not safely rejected: refund=%d %s expected=%d rows=%d", rRefund.code, rRefund.body, closeResult.Data.Expected, refunds)
		}
	} else {
		t.Fatalf("unexpected refund-vs-close outcome: %d %s", rRefund.code, rRefund.body)
	}
}

func TestPOSCashMovementRacingCloseIsIncludedOrRejected(t *testing.T) {
	f := newPOSCashFixture(t)
	if _, err := f.a.db.Exec(`UPDATE pos_shifts SET opening_cash=100 WHERE organization_id=? AND id=?`, f.org, f.shift); err != nil {
		t.Fatal(err)
	}
	otherDB, err := openDB(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	other := &App{db: otherDB}
	move := cashMovement(f.shift, "in", 20, "Till float top-up", "cash-close-race-move")
	closeInput := map[string]any{"action": "close", "shiftId": f.shift, "closingCash": 120, "notes": "Counted closing drawer", "idempotencyKey": "cash-close-race-key"}
	type result struct {
		action string
		code   int
		body   string
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	go func() {
		<-start
		w := request(f.a, http.MethodPost, "/api/stations/pos/business", move, f.front...)
		results <- result{"move", w.Code, w.Body.String()}
	}()
	go func() {
		<-start
		w := request(other, http.MethodPost, "/api/stations/pos/business", closeInput, f.front...)
		results <- result{"close", w.Code, w.Body.String()}
	}()
	close(start)
	first, second := <-results, <-results
	moveResult, closeResult := first, second
	if first.action == "close" {
		moveResult, closeResult = second, first
	}
	if moveResult.code == http.StatusServiceUnavailable {
		w := request(f.a, http.MethodPost, "/api/stations/pos/business", move, f.front...)
		moveResult = result{"move", w.Code, w.Body.String()}
	}
	if closeResult.code == http.StatusServiceUnavailable {
		w := request(other, http.MethodPost, "/api/stations/pos/business", closeInput, f.front...)
		closeResult = result{"close", w.Code, w.Body.String()}
	}
	if closeResult.code != http.StatusOK {
		t.Fatalf("close race did not resolve through same-key retry: %d %s", closeResult.code, closeResult.body)
	}
	var closeData struct {
		Data struct {
			ExpectedCash int64 `json:"expectedCash"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(closeResult.body), &closeData); err != nil {
		t.Fatal(err)
	}
	var movements int
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_cash_movements WHERE organization_id=? AND shift_id=?`, f.org, f.shift).Scan(&movements); err != nil {
		t.Fatal(err)
	}
	if moveResult.code == http.StatusOK {
		if closeData.Data.ExpectedCash != 120 || movements != 1 {
			t.Fatalf("committed movement omitted from close: expected=%d movements=%d close=%s", closeData.Data.ExpectedCash, movements, closeResult.body)
		}
	} else if moveResult.code == http.StatusConflict {
		if closeData.Data.ExpectedCash != 100 || movements != 0 || !strings.Contains(moveResult.body, `"outcome":"not_applied"`) {
			t.Fatalf("movement lost after close should have no effect: movement=%d expected=%d rows=%d %s", moveResult.code, closeData.Data.ExpectedCash, movements, moveResult.body)
		}
	} else {
		t.Fatalf("unexpected movement race response: %d %s", moveResult.code, moveResult.body)
	}
}
