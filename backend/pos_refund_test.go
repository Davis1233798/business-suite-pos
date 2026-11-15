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

type posRefundFixture struct {
	a                                                               *App
	dbPath                                                          string
	org, shift, store, register, cashier, item, sale, line, receipt string
	admin, front, office, viewer, foreign                           []*http.Cookie
}

func newPOSRefundFixture(t *testing.T, paymentMethod string) posRefundFixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "refund.sqlite")
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
	var shift, store, register, cashier, item string
	if err := a.db.QueryRow(`SELECT id,store_id,register_id,cashier_id FROM pos_shifts WHERE organization_id=? AND status='open' LIMIT 1`, org).Scan(&shift, &store, &register, &cashier); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id FROM pos_catalog_items WHERE organization_id=? ORDER BY id LIMIT 1`, org).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE pos_catalog_items SET stock_quantity=10 WHERE organization_id=? AND id=?`, org, item); err != nil {
		t.Fatal(err)
	}
	sale := "sale_refund_fixture_" + space
	line := "line_refund_fixture_" + space
	receipt := "POS-REFUND-" + strings.ToUpper(space[:8])
	if _, err := a.db.Exec(`INSERT INTO pos_sales(id,organization_id,receipt_number,store_id,register_id,shift_id,cashier_id,subtotal,discount_amount,tax_amount,total_amount,tender_amount,change_amount,payment_method,status,idempotency_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sale, org, receipt, store, register, shift, cashier, 102, 1, 0, 101, 101, 0, paymentMethod, "completed", "fixture-sale-"+space); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO pos_sale_lines(id,organization_id,sale_id,item_id,barcode,name,unit_price,quantity,discount_amount,line_total,refunded_quantity) VALUES(?,?,?,?,?,?,?,?,?,?,0)`, line, org, sale, item, "REFUND-ITEM", "Refund fixture item", 34, 3, 1, 101); err != nil {
		t.Fatal(err)
	}
	return posRefundFixture{a: a, dbPath: dbPath, org: org, shift: shift, store: store, register: register, cashier: cashier, item: item, sale: sale, line: line, receipt: receipt,
		admin: admin, front: front, office: office, viewer: viewer, foreign: foreign}
}

func refundInput(line string, quantity int, reason, key string) map[string]any {
	return map[string]any{"action": "refund", "saleLineId": line, "quantity": quantity, "reason": reason, "idempotencyKey": key}
}

func TestPOSRefundLookupRequiresAdminAndReturnsScopedSaleLines(t *testing.T) {
	f := newPOSRefundFixture(t, "cash")
	path := "/api/stations/pos/refund-lookup?receiptNumber=" + f.receipt
	w := request(f.a, http.MethodGet, "/api/stations/pos/refund-lookup", nil, f.admin...)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing receipt expected 400: %d %s", w.Code, w.Body.String())
	}
	for name, cookies := range map[string][]*http.Cookie{"front": f.front, "back operator": f.office, "read only": f.viewer} {
		if w := request(f.a, http.MethodGet, path, nil, cookies...); w.Code != http.StatusForbidden {
			t.Fatalf("%s lookup expected 403: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := request(f.a, http.MethodGet, path, nil, f.foreign...); w.Code != http.StatusNotFound {
		t.Fatalf("foreign organization lookup should be indistinguishable from missing receipt: %d %s", w.Code, w.Body.String())
	}
	w = request(f.a, http.MethodGet, path, nil, f.admin...)
	if w.Code != http.StatusOK {
		t.Fatalf("admin lookup: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			SaleID        string `json:"saleId"`
			ReceiptNumber string `json:"receiptNumber"`
			SaleStatus    string `json:"saleStatus"`
			PaymentMethod string `json:"paymentMethod"`
			ShiftStatus   string `json:"shiftStatus"`
			Eligible      bool   `json:"eligible"`
			Lines         []struct {
				SaleLineID        string `json:"saleLineId"`
				ItemID            string `json:"itemId"`
				Name              string `json:"name"`
				Barcode           string `json:"barcode"`
				Quantity          int    `json:"quantity"`
				RefundedQuantity  int    `json:"refundedQuantity"`
				RemainingQuantity int    `json:"remainingQuantity"`
				LineTotal         int64  `json:"lineTotal"`
			} `json:"lines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode lookup: %v %s", err, w.Body.String())
	}
	d := response.Data
	if d.SaleID != f.sale || d.ReceiptNumber != f.receipt || d.SaleStatus != "completed" || d.PaymentMethod != "cash" || d.ShiftStatus != "open" || !d.Eligible || len(d.Lines) != 1 {
		t.Fatalf("unexpected lookup: %+v", d)
	}
	line := d.Lines[0]
	if line.SaleLineID != f.line || line.ItemID != f.item || line.Name != "Refund fixture item" || line.Barcode != "REFUND-ITEM" || line.Quantity != 3 || line.RefundedQuantity != 0 || line.RemainingQuantity != 3 || line.LineTotal != 101 {
		t.Fatalf("unexpected lookup line: %+v", line)
	}
	for _, forbidden := range []string{"organizationId", "organization_id", "cashierId", "cashier_id", "lockedBy", "passwordHash"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("lookup leaked %q: %s", forbidden, w.Body.String())
		}
	}
	missing := request(f.a, http.MethodGet, "/api/stations/pos/refund-lookup?receiptNumber=not-found", nil, f.admin...)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown receipt expected 404: %d %s", missing.Code, missing.Body.String())
	}
}

func TestPOSRefundLookupCapsSaleLinesAndRejectsDuplicateReceiptNumbers(t *testing.T) {
	f := newPOSRefundFixture(t, "cash")
	for i := 0; i < 100; i++ {
		lineID := fmt.Sprintf("extra-refund-line-%03d-%s", i, f.org[len(f.org)-8:])
		if _, err := f.a.db.Exec(`INSERT INTO pos_sale_lines(id,organization_id,sale_id,item_id,barcode,name,unit_price,quantity,discount_amount,line_total,refunded_quantity) VALUES(?,?,?,?,?,?,?,?,?,?,0)`, lineID, f.org, f.sale, f.item, "EXTRA", "Additional line", 1, 1, 0, 1); err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/stations/pos/refund-lookup?receiptNumber=" + f.receipt
	w := request(f.a, http.MethodGet, path, nil, f.admin...)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "too many lines") {
		t.Fatalf("101-line sale must be refused by bounded lookup: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.a.db.Exec(`INSERT INTO pos_sales(id,organization_id,receipt_number,store_id,register_id,shift_id,cashier_id,subtotal,discount_amount,tax_amount,total_amount,tender_amount,change_amount,payment_method,status,idempotency_key) SELECT id||'_duplicate',organization_id,receipt_number,store_id,register_id,shift_id,cashier_id,subtotal,discount_amount,tax_amount,total_amount,tender_amount,change_amount,payment_method,status,idempotency_key||'_duplicate' FROM pos_sales WHERE organization_id=? AND id=?`, f.org, f.sale); err != nil {
		t.Fatal(err)
	}
	w = request(f.a, http.MethodGet, path, nil, f.admin...)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "ambiguous") {
		t.Fatalf("duplicate receipt must not select an arbitrary sale: %d %s", w.Code, w.Body.String())
	}
}

func TestPOSRefundLookupMarksClosedNonCashAndFullyRefundedSalesIneligible(t *testing.T) {
	for _, tc := range []struct {
		name, paymentMethod, shiftStatus string
		refundedQuantity                 int
	}{
		{name: "closed original shift", paymentMethod: "cash", shiftStatus: "closed"},
		{name: "non-cash sale", paymentMethod: "card", shiftStatus: "open"},
		{name: "fully refunded sale", paymentMethod: "cash", shiftStatus: "open", refundedQuantity: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPOSRefundFixture(t, tc.paymentMethod)
			if tc.shiftStatus == "closed" {
				if _, err := f.a.db.Exec(`UPDATE pos_shifts SET status='closed' WHERE organization_id=? AND id=?`, f.org, f.shift); err != nil {
					t.Fatal(err)
				}
			}
			if tc.refundedQuantity > 0 {
				if _, err := f.a.db.Exec(`UPDATE pos_sale_lines SET refunded_quantity=? WHERE organization_id=? AND id=?`, tc.refundedQuantity, f.org, f.line); err != nil {
					t.Fatal(err)
				}
			}
			w := request(f.a, http.MethodGet, "/api/stations/pos/refund-lookup?receiptNumber="+f.receipt, nil, f.admin...)
			if w.Code != http.StatusOK {
				t.Fatalf("lookup: %d %s", w.Code, w.Body.String())
			}
			var response struct {
				Data struct {
					Eligible      bool   `json:"eligible"`
					PaymentMethod string `json:"paymentMethod"`
					ShiftStatus   string `json:"shiftStatus"`
					Lines         []struct {
						RemainingQuantity int64 `json:"remainingQuantity"`
					} `json:"lines"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Data.Eligible || response.Data.PaymentMethod != tc.paymentMethod || response.Data.ShiftStatus != tc.shiftStatus || len(response.Data.Lines) != 1 || response.Data.Lines[0].RemainingQuantity != int64(3-tc.refundedQuantity) {
				t.Fatalf("expected ineligible sale state: %+v", response.Data)
			}
		})
	}
}

func TestPOSRefundCumulativeAllocationAndIdempotentReplay(t *testing.T) {
	f := newPOSRefundFixture(t, "cash")
	for _, tc := range []struct {
		name    string
		cookies []*http.Cookie
		want    int
	}{{"front", f.front, http.StatusForbidden}, {"back operator", f.office, http.StatusForbidden}, {"read only", f.viewer, http.StatusForbidden}, {"foreign organization", f.foreign, http.StatusNotFound}} {
		w := request(f.a, http.MethodPost, "/api/stations/pos/business", refundInput(f.line, 1, "denied", "denied-refund-0001"), tc.cookies...)
		if w.Code != tc.want {
			t.Fatalf("%s refund status=%d want=%d: %s", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
	if w := request(f.a, http.MethodPost, "/api/stations/pos/business", refundInput(f.line, 1, "   ", "blank-reason-00001"), f.admin...); w.Code != http.StatusBadRequest {
		t.Fatalf("blank reason expected 400: %d %s", w.Code, w.Body.String())
	}
	input := refundInput(f.line, 1, "  partial return  ", "refund-partial-0001")
	first := request(f.a, http.MethodPost, "/api/stations/pos/business", input, f.admin...)
	if first.Code != http.StatusOK {
		t.Fatalf("first partial refund: %d %s", first.Code, first.Body.String())
	}
	var firstResult struct {
		Data struct {
			RefundAmount int64  `json:"refundAmount"`
			RefundID     string `json:"refundId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstResult); err != nil {
		t.Fatal(err)
	}
	if firstResult.Data.RefundAmount != 33 {
		t.Fatalf("first cumulative amount=%d, want 33", firstResult.Data.RefundAmount)
	}
	replay := request(f.a, http.MethodPost, "/api/stations/pos/business", input, f.admin...)
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("same-key replay changed response: %d %s; original %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	changed := refundInput(f.line, 2, "different payload", "refund-partial-0001")
	if w := request(f.a, http.MethodPost, "/api/stations/pos/business", changed, f.admin...); w.Code != http.StatusConflict {
		t.Fatalf("changed payload should conflict: %d %s", w.Code, w.Body.String())
	}
	amounts := []int64{firstResult.Data.RefundAmount}
	for i, key := range []string{"refund-partial-0002", "refund-partial-0003"} {
		w := request(f.a, http.MethodPost, "/api/stations/pos/business", refundInput(f.line, 1, "remaining return", key), f.admin...)
		if w.Code != http.StatusOK {
			t.Fatalf("partial refund %d: %d %s", i+2, w.Code, w.Body.String())
		}
		var result struct {
			Data struct {
				RefundAmount int64 `json:"refundAmount"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		amounts = append(amounts, result.Data.RefundAmount)
	}
	if fmt.Sprint(amounts) != "[33 34 34]" {
		t.Fatalf("cumulative discount allocation=%v, want [33 34 34]", amounts)
	}
	var total, stock, refundRows, refundLines, auditRows, idemRows int64
	if err := f.a.db.QueryRow(`SELECT COALESCE(SUM(refund_amount),0) FROM pos_refunds WHERE organization_id=? AND original_sale_id=?`, f.org, f.sale).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, f.org, f.item).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_refunds WHERE organization_id=? AND original_sale_id=?`, f.org, f.sale).Scan(&refundRows); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_refund_lines rl JOIN pos_refunds r ON r.id=rl.refund_id WHERE r.organization_id=? AND r.original_sale_id=?`, f.org, f.sale).Scan(&refundLines); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM audit_events WHERE organization_id=? AND action='pos.refund' AND entity_id IN (SELECT id FROM pos_refunds WHERE original_sale_id=?)`, f.org, f.sale).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND endpoint='/station/pos/refund' AND key LIKE 'refund-partial-%'`, f.org).Scan(&idemRows); err != nil {
		t.Fatal(err)
	}
	if total != 101 || stock != 13 || refundRows != 3 || refundLines != 3 || auditRows != 3 || idemRows != 3 {
		t.Fatalf("refund effects total=%d stock=%d headers=%d lines=%d audit=%d idem=%d", total, stock, refundRows, refundLines, auditRows, idemRows)
	}
	var reason string
	if err := f.a.db.QueryRow(`SELECT reason FROM pos_refunds WHERE organization_id=? AND id=?`, f.org, firstResult.Data.RefundID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "partial return" {
		t.Fatalf("stored reason was not trimmed/preserved: %q", reason)
	}
	if w := request(f.a, http.MethodPost, "/api/stations/pos/business", refundInput(f.line, 1, "over refund", "refund-over-00001"), f.admin...); w.Code != http.StatusConflict {
		t.Fatalf("over-refund should conflict: %d %s", w.Code, w.Body.String())
	}
}

func TestPOSRefundValidationDeclaresNoEffectForUnsupportedQuantityAndUTF8Reason(t *testing.T) {
	for _, tc := range []struct {
		name     string
		quantity int
		reason   string
	}{
		{name: "quantity above endpoint maximum", quantity: 1001, reason: "valid reason"},
		{name: "reason above UTF-8 byte maximum", quantity: 1, reason: strings.Repeat("中", 67)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPOSRefundFixture(t, "cash")
			key := "refund-too-many-0001"
			if tc.quantity == 1 {
				key = "refund-long-reason-01"
			}
			input := refundInput(f.line, tc.quantity, tc.reason, key)
			w := request(f.a, http.MethodPost, "/api/stations/pos/business", input, f.admin...)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("invalid refund request expected 400: %d %s", w.Code, w.Body.String())
			}
			var response struct {
				Error struct {
					Outcome string `json:"outcome"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode validation response: %v %s", err, w.Body.String())
			}
			if response.Error.Outcome != "not_applied" {
				t.Fatalf("validation failure must explicitly prove no effect: %s", w.Body.String())
			}
			replay := request(f.a, http.MethodPost, "/api/stations/pos/business", input, f.admin...)
			if replay.Code != w.Code || replay.Body.String() != w.Body.String() {
				t.Fatalf("same invalid key must replay its terminal rejection: %d %s; original %s", replay.Code, replay.Body.String(), w.Body.String())
			}
			corrected := request(f.a, http.MethodPost, "/api/stations/pos/business", refundInput(f.line, 1, "corrected request", key), f.admin...)
			if corrected.Code != http.StatusConflict || strings.Contains(corrected.Body.String(), `"outcome":"not_applied"`) {
				t.Fatalf("different payload must not reuse terminally rejected key: %d %s", corrected.Code, corrected.Body.String())
			}
			var refunded, stock, refunds, idempotency int64
			if err := f.a.db.QueryRow(`SELECT refunded_quantity FROM pos_sale_lines WHERE organization_id=? AND id=?`, f.org, f.line).Scan(&refunded); err != nil {
				t.Fatal(err)
			}
			if err := f.a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, f.org, f.item).Scan(&stock); err != nil {
				t.Fatal(err)
			}
			if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_refunds WHERE organization_id=?`, f.org).Scan(&refunds); err != nil {
				t.Fatal(err)
			}
			if err := f.a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND endpoint='/station/pos/refund'`, f.org).Scan(&idempotency); err != nil {
				t.Fatal(err)
			}
			if refunded != 0 || stock != 10 || refunds != 0 || idempotency != 1 {
				t.Fatalf("invalid refund mutated state refunded=%d stock=%d refunds=%d idempotency=%d", refunded, stock, refunds, idempotency)
			}
		})
	}
}

func TestPOSRefundValidationDoesNotOverrideAnExistingIdempotencyKey(t *testing.T) {
	f := newPOSRefundFixture(t, "cash")
	key := "refund-key-used-0001"
	first := request(f.a, http.MethodPost, "/api/stations/pos/business", refundInput(f.line, 1, "valid refund", key), f.admin...)
	if first.Code != http.StatusOK {
		t.Fatalf("initial refund: %d %s", first.Code, first.Body.String())
	}
	invalid := request(f.a, http.MethodPost, "/api/stations/pos/business", refundInput(f.line, 1001, "valid reason", key), f.admin...)
	if invalid.Code != http.StatusConflict {
		t.Fatalf("existing key with a changed invalid payload must conflict before validation: %d %s", invalid.Code, invalid.Body.String())
	}
	if strings.Contains(invalid.Body.String(), `"outcome":"not_applied"`) {
		t.Fatalf("conflicting key must not claim the earlier operation had no effect: %s", invalid.Body.String())
	}
	replay := request(f.a, http.MethodPost, "/api/stations/pos/business", refundInput(f.line, 1, "valid refund", key), f.admin...)
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("same-key replay must still resolve to the committed result: %d %s; original %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	var refunded, stock, refunds, idempotency int64
	if err := f.a.db.QueryRow(`SELECT refunded_quantity FROM pos_sale_lines WHERE organization_id=? AND id=?`, f.org, f.line).Scan(&refunded); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, f.org, f.item).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_refunds WHERE organization_id=? AND original_sale_id=?`, f.org, f.sale).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint='/station/pos/refund'`, f.org, key).Scan(&idempotency); err != nil {
		t.Fatal(err)
	}
	if refunded != 1 || stock != 11 || refunds != 1 || idempotency != 1 {
		t.Fatalf("conflicting invalid retry changed committed state refunded=%d stock=%d refunds=%d idempotency=%d", refunded, stock, refunds, idempotency)
	}
}

func TestPOSRefundMissingLineReservesTerminalNoEffectOutcome(t *testing.T) {
	f := newPOSRefundFixture(t, "cash")
	key := "refund-missing-line-0001"
	input := refundInput("deleted-sale-line", 1, "return", key)
	w := request(f.a, http.MethodPost, "/api/stations/pos/business", input, f.admin...)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"outcome":"not_applied"`) {
		t.Fatalf("missing line must explicitly report a terminal no-effect result: %d %s", w.Code, w.Body.String())
	}
	replay := request(f.a, http.MethodPost, "/api/stations/pos/business", input, f.admin...)
	if replay.Code != w.Code || replay.Body.String() != w.Body.String() {
		t.Fatalf("missing-line result must replay exactly: %d %s; original %s", replay.Code, replay.Body.String(), w.Body.String())
	}
	changed := request(f.a, http.MethodPost, "/api/stations/pos/business", refundInput(f.line, 1, "return", key), f.admin...)
	if changed.Code != http.StatusConflict || strings.Contains(changed.Body.String(), `"outcome":"not_applied"`) {
		t.Fatalf("changed request must conflict with the reserved missing-line key: %d %s", changed.Code, changed.Body.String())
	}
	var stock, refunded, refunds, idempotency int64
	if err := f.a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, f.org, f.item).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT refunded_quantity FROM pos_sale_lines WHERE organization_id=? AND id=?`, f.org, f.line).Scan(&refunded); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_refunds WHERE organization_id=? AND original_sale_id=?`, f.org, f.sale).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND endpoint='/station/pos/refund' AND key=?`, f.org, key).Scan(&idempotency); err != nil {
		t.Fatal(err)
	}
	if stock != 10 || refunded != 0 || refunds != 0 || idempotency != 1 {
		t.Fatalf("missing-line rejection side effects stock=%d refunded=%d refunds=%d idempotency=%d", stock, refunded, refunds, idempotency)
	}
}

func TestPOSRefundInvalidAndValidSameKeyAcrossDBHandlesReserveOnlyOneOutcome(t *testing.T) {
	f := newPOSRefundFixture(t, "cash")
	otherDB, err := openDB(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	other := &App{db: otherDB}
	key := "refund-racing-key-0001"
	invalidInput := refundInput(f.line, 1001, "invalid quantity", key)
	validInput := refundInput(f.line, 1, "valid quantity", key)
	type response struct {
		code int
		body string
	}
	results := make(chan response, 2)
	start := make(chan struct{})
	go func() {
		<-start
		w := request(f.a, http.MethodPost, "/api/stations/pos/business", invalidInput, f.admin...)
		results <- response{w.Code, w.Body.String()}
	}()
	go func() {
		<-start
		w := request(other, http.MethodPost, "/api/stations/pos/business", validInput, f.admin...)
		results <- response{w.Code, w.Body.String()}
	}()
	close(start)
	first, second := <-results, <-results
	if first.code == http.StatusBadRequest && second.code == http.StatusOK || first.code == http.StatusOK && second.code == http.StatusBadRequest {
		t.Fatalf("concurrent different payloads both claimed an outcome for one key: first=%d %s second=%d %s", first.code, first.body, second.code, second.body)
	}
	if first.code == http.StatusServiceUnavailable || second.code == http.StatusServiceUnavailable {
		// Retry the transient lock loser after the winning transaction has committed.
		if first.code == http.StatusServiceUnavailable {
			w := request(f.a, http.MethodPost, "/api/stations/pos/business", invalidInput, f.admin...)
			first = response{w.Code, w.Body.String()}
		}
		if second.code == http.StatusServiceUnavailable {
			w := request(other, http.MethodPost, "/api/stations/pos/business", validInput, f.admin...)
			second = response{w.Code, w.Body.String()}
		}
	}
	if first.code != http.StatusBadRequest && first.code != http.StatusConflict && first.code != http.StatusOK || second.code != http.StatusBadRequest && second.code != http.StatusConflict && second.code != http.StatusOK {
		t.Fatalf("unexpected concurrent/retry responses: first=%d %s second=%d %s", first.code, first.body, second.code, second.body)
	}
	if first.code == http.StatusBadRequest && second.code == http.StatusOK || first.code == http.StatusOK && second.code == http.StatusBadRequest {
		t.Fatalf("same key was both terminally rejected and committed: first=%d %s second=%d %s", first.code, first.body, second.code, second.body)
	}
	terminalRejected := first.code == http.StatusBadRequest || second.code == http.StatusBadRequest
	for _, result := range []response{first, second} {
		if result.code == http.StatusBadRequest && !strings.Contains(result.body, `"outcome":"not_applied"`) {
			t.Fatalf("terminal validation rejection must include its no-effect marker: %s", result.body)
		}
	}
	var idem, refunds, refunded int
	if err := f.a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND endpoint='/station/pos/refund' AND key=?`, f.org, key).Scan(&idem); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_refunds WHERE organization_id=? AND original_sale_id=?`, f.org, f.sale).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT refunded_quantity FROM pos_sale_lines WHERE organization_id=? AND id=?`, f.org, f.line).Scan(&refunded); err != nil {
		t.Fatal(err)
	}
	if idem != 1 || refunds > 1 || refunded > 1 || (terminalRejected && (refunds != 0 || refunded != 0)) {
		t.Fatalf("key reservation effects idem=%d refunds=%d refunded=%d responses=%d/%d", idem, refunds, refunded, first.code, second.code)
	}
}

func TestPOSRefundRejectsClosedOriginalShiftAndNonCashWithoutMutation(t *testing.T) {
	for _, tc := range []struct{ name, paymentMethod, closeShift string }{
		{name: "closed shift", paymentMethod: "cash", closeShift: "closed"},
		{name: "non-cash payment", paymentMethod: "card", closeShift: "open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPOSRefundFixture(t, tc.paymentMethod)
			if tc.closeShift == "closed" {
				if _, err := f.a.db.Exec(`UPDATE pos_shifts SET status='closed' WHERE organization_id=? AND id=?`, f.org, f.shift); err != nil {
					t.Fatal(err)
				}
			}
			w := request(f.a, http.MethodPost, "/api/stations/pos/business", refundInput(f.line, 1, "return", "refund-ineligible-01"), f.admin...)
			if w.Code != http.StatusConflict {
				t.Fatalf("ineligible sale expected 409: %d %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"outcome":"not_applied"`) {
				t.Fatalf("refund conflict must explicitly report no effect: %s", w.Body.String())
			}
			var refunded, stock, refunds, refundLines, audits, idempotency int64
			if err := f.a.db.QueryRow(`SELECT refunded_quantity FROM pos_sale_lines WHERE organization_id=? AND id=?`, f.org, f.line).Scan(&refunded); err != nil {
				t.Fatal(err)
			}
			if err := f.a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, f.org, f.item).Scan(&stock); err != nil {
				t.Fatal(err)
			}
			if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_refunds WHERE organization_id=?`, f.org).Scan(&refunds); err != nil {
				t.Fatal(err)
			}
			if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_refund_lines WHERE organization_id=?`, f.org).Scan(&refundLines); err != nil {
				t.Fatal(err)
			}
			if err := f.a.db.QueryRow(`SELECT count(*) FROM audit_events WHERE organization_id=? AND action='pos.refund'`, f.org).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if err := f.a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND endpoint='/station/pos/refund'`, f.org).Scan(&idempotency); err != nil {
				t.Fatal(err)
			}
			if refunded != 0 || stock != 10 || refunds != 0 || refundLines != 0 || audits != 0 || idempotency != 1 {
				t.Fatalf("ineligible request mutated state refunded=%d stock=%d refund=%d lines=%d audit=%d idem=%d", refunded, stock, refunds, refundLines, audits, idempotency)
			}
		})
	}
}

func TestPOSConcurrentRefundsCannotExceedRemainingQuantity(t *testing.T) {
	f := newPOSRefundFixture(t, "cash")
	type result struct {
		code int
		body string
	}
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for _, key := range []string{"refund-concurrent-0001", "refund-concurrent-0002"} {
		wait.Add(1)
		go func(key string) {
			defer wait.Done()
			w := request(f.a, http.MethodPost, "/api/stations/pos/business", refundInput(f.line, 2, "concurrent return", key), f.admin...)
			results <- result{code: w.Code, body: w.Body.String()}
		}(key)
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for result := range results {
		switch result.code {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			t.Errorf("unexpected concurrent refund result %d %s", result.code, result.body)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent refund outcomes success=%d conflict=%d", successes, conflicts)
	}
	var refunded, stock, amount int64
	if err := f.a.db.QueryRow(`SELECT refunded_quantity FROM pos_sale_lines WHERE organization_id=? AND id=?`, f.org, f.line).Scan(&refunded); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, f.org, f.item).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT COALESCE(SUM(refund_amount),0) FROM pos_refunds WHERE organization_id=? AND original_sale_id=?`, f.org, f.sale).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if refunded != 2 || stock != 12 || amount != 67 {
		t.Fatalf("concurrent refund state refunded=%d stock=%d amount=%d", refunded, stock, amount)
	}
}

func TestPOSRefundMissingProductRollsBackRefundState(t *testing.T) {
	f := newPOSRefundFixture(t, "cash")
	if _, err := f.a.db.Exec(`DELETE FROM pos_catalog_items WHERE organization_id=? AND id=?`, f.org, f.item); err != nil {
		t.Fatal(err)
	}
	w := request(f.a, http.MethodPost, "/api/stations/pos/business", refundInput(f.line, 1, "return", "refund-missing-item-01"), f.admin...)
	if w.Code != http.StatusConflict {
		t.Fatalf("missing product expected conflict: %d %s", w.Code, w.Body.String())
	}
	var refunded, refunds, refundLines, audits, idempotency int64
	if err := f.a.db.QueryRow(`SELECT refunded_quantity FROM pos_sale_lines WHERE organization_id=? AND id=?`, f.org, f.line).Scan(&refunded); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_refunds WHERE organization_id=?`, f.org).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM pos_refund_lines WHERE organization_id=?`, f.org).Scan(&refundLines); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM audit_events WHERE organization_id=? AND action='pos.refund'`, f.org).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND key='refund-missing-item-01'`, f.org).Scan(&idempotency); err != nil {
		t.Fatal(err)
	}
	if refunded != 0 || refunds != 0 || refundLines != 0 || audits != 0 || idempotency != 1 {
		t.Fatalf("rejected refund effects/reservation refunded=%d refunds=%d lines=%d audit=%d idem=%d", refunded, refunds, refundLines, audits, idempotency)
	}
}
