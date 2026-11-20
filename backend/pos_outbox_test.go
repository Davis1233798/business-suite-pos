package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestPOSOutboxSingleItemCheckoutAndPairing(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "pos_outbox_single.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	a := &App{db: db, consumerIdentityEnabled: true}
	space := token()
	if err := a.initializeDemo(space); err != nil {
		t.Fatal(err)
	}

	var orgID, slug string
	if err := a.db.QueryRow(`SELECT id,slug FROM organizations WHERE slug=?`, "org_test_"+space).Scan(&orgID, &slug); err != nil {
		t.Fatal(err)
	}

	var shiftID, cashierID, itemID, barcode, itemName string
	var price, stock int64
	if err := a.db.QueryRow(`SELECT id,cashier_id FROM pos_shifts WHERE organization_id=? AND status='open' LIMIT 1`, orgID).Scan(&shiftID, &cashierID); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id,barcode,name,price,stock_quantity FROM pos_catalog_items WHERE organization_id=? AND stock_quantity>5 LIMIT 1`, orgID).Scan(&itemID, &barcode, &itemName, &price, &stock); err != nil {
		t.Fatal(err)
	}

	id := &Identity{UserID: cashierID, OrganizationID: orgID}
	grant := &Grant{Role: "cashier", Scope: "front"}

	// 1. Anonymous checkout
	anonKey := "idem_anon_" + token()
	anonCmd := posCommand{
		Action:         "checkout",
		ShiftID:        shiftID,
		ItemID:         itemID,
		Quantity:       1,
		Tender:         price * 2,
		IdempotencyKey: anonKey,
	}
	anonResp := posBusinessRequest(a, anonCmd, id, grant)
	if anonResp.Code != http.StatusOK {
		t.Fatalf("anon checkout failed: %d: %s", anonResp.Code, anonResp.Body.String())
	}

	var anonBody struct {
		Success bool `json:"success"`
		Data    struct {
			SaleID        string `json:"saleId"`
			ReceiptNumber string `json:"receiptNumber"`
			TotalAmount   int64  `json:"totalAmount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(anonResp.Body.Bytes(), &anonBody); err != nil {
		t.Fatal(err)
	}

	// Verify outbox event for anonymous checkout
	var evtID, evtType, payloadStr string
	err = a.db.QueryRow(`SELECT id, event_type, payload FROM outbox_events WHERE organization_id=? AND event_type='pos.sale.completed' AND json_extract(payload, '$.sourceId')=?`, orgID, anonBody.Data.SaleID).Scan(&evtID, &evtType, &payloadStr)
	if err != nil {
		t.Fatalf("outbox event not found for anonymous checkout: %v", err)
	}

	var payload OutboxSaleCompletedPayload
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}

	if evtID != payload.EventID {
		t.Fatalf("mismatched event ID: %s vs %s", evtID, payload.EventID)
	}
	if payload.SchemaVersion != 1 {
		t.Fatalf("expected schemaVersion 1, got %d", payload.SchemaVersion)
	}
	if payload.OrganizationID != orgID {
		t.Fatalf("expected organizationId %s, got %s", orgID, payload.OrganizationID)
	}
	if payload.SourceID != anonBody.Data.SaleID {
		t.Fatalf("expected sourceId %s, got %s", anonBody.Data.SaleID, payload.SourceID)
	}
	if payload.ReceiptNumber != anonBody.Data.ReceiptNumber {
		t.Fatalf("expected receiptNumber %s, got %s", anonBody.Data.ReceiptNumber, payload.ReceiptNumber)
	}
	if payload.TotalAmount != price {
		t.Fatalf("expected totalAmount %d, got %d", price, payload.TotalAmount)
	}
	if payload.PaymentMethod != "cash" {
		t.Fatalf("expected paymentMethod cash, got %s", payload.PaymentMethod)
	}
	if payload.CustomerID != nil {
		t.Fatalf("expected customerId nil for anonymous checkout, got %v", *payload.CustomerID)
	}
	if len(payload.Lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(payload.Lines))
	}
	line := payload.Lines[0]
	if line.ItemID != itemID || line.Barcode != barcode || line.Name != itemName || line.UnitPrice != price || line.Quantity != 1 || line.LineTotal != price {
		t.Fatalf("unexpected line in outbox payload: %+v", line)
	}
	if _, err := time.Parse(time.RFC3339, payload.OccurredAt); err != nil {
		t.Fatalf("invalid occurredAt timestamp format: %s (%v)", payload.OccurredAt, err)
	}

	// 2. Checkout with paired consumer
	cookie := newVerifiedConsumerSession(t, a, orgID, slug, "paired.shopper@example.com", "SecretPass123!")
	pairReq := consumerRequest(a, http.MethodPost, slug, "pair", nil, cookie)
	if pairReq.Code != http.StatusOK {
		t.Fatalf("consumer pair failed: %d: %s", pairReq.Code, pairReq.Body.String())
	}
	var pairResp struct {
		Data struct {
			PairingToken string `json:"pairingToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(pairReq.Body.Bytes(), &pairResp); err != nil {
		t.Fatal(err)
	}

	var expectedAccountID string
	if err := a.db.QueryRow(`SELECT id FROM pos_consumer_accounts WHERE organization_id=? AND normalized_email='paired.shopper@example.com'`, orgID).Scan(&expectedAccountID); err != nil {
		t.Fatal(err)
	}

	pairedKey := "idem_paired_" + token()
	pairedCmd := posCommand{
		Action:         "checkout",
		ShiftID:        shiftID,
		ItemID:         itemID,
		Quantity:       2,
		Tender:         price * 4,
		PairingToken:   pairResp.Data.PairingToken,
		IdempotencyKey: pairedKey,
	}
	pairedResp := posBusinessRequest(a, pairedCmd, id, grant)
	if pairedResp.Code != http.StatusOK {
		t.Fatalf("paired checkout failed: %d: %s", pairedResp.Code, pairedResp.Body.String())
	}
	var pairedBody struct {
		Data struct {
			SaleID string `json:"saleId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(pairedResp.Body.Bytes(), &pairedBody); err != nil {
		t.Fatal(err)
	}

	var pairedPayloadStr string
	err = a.db.QueryRow(`SELECT payload FROM outbox_events WHERE organization_id=? AND event_type='pos.sale.completed' AND json_extract(payload, '$.sourceId')=?`, orgID, pairedBody.Data.SaleID).Scan(&pairedPayloadStr)
	if err != nil {
		t.Fatalf("paired outbox event not found: %v", err)
	}
	var pairedPayload OutboxSaleCompletedPayload
	if err := json.Unmarshal([]byte(pairedPayloadStr), &pairedPayload); err != nil {
		t.Fatal(err)
	}
	if pairedPayload.CustomerID == nil || *pairedPayload.CustomerID != expectedAccountID {
		t.Fatalf("expected customerId %s, got %v", expectedAccountID, pairedPayload.CustomerID)
	}
	if pairedPayload.TotalAmount != price*2 {
		t.Fatalf("expected totalAmount %d, got %d", price*2, pairedPayload.TotalAmount)
	}
	if len(pairedPayload.Lines) != 1 || pairedPayload.Lines[0].Quantity != 2 || pairedPayload.Lines[0].LineTotal != price*2 {
		t.Fatalf("unexpected lines: %+v", pairedPayload.Lines)
	}
}

func TestPOSOutboxCartCheckout(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	staff := loginSite(t, a, "front@suite.test", space, "pos")
	path := "/api/stations/pos/cart"

	var shift string
	if err := a.db.QueryRow("SELECT id FROM pos_shifts WHERE organization_id=? AND status='open' LIMIT 1", org).Scan(&shift); err != nil {
		t.Fatal(err)
	}

	rows, err := a.db.Query("SELECT id,barcode,name,price,stock_quantity FROM pos_catalog_items WHERE organization_id=? AND is_active=1 AND stock_quantity>=5 ORDER BY id LIMIT 2", org)
	if err != nil {
		t.Fatal(err)
	}
	type item struct {
		id, barcode, name string
		price, stock      int64
	}
	var products []item
	for rows.Next() {
		var v item
		if err := rows.Scan(&v.id, &v.barcode, &v.name, &v.price, &v.stock); err != nil {
			t.Fatal(err)
		}
		products = append(products, v)
	}
	rows.Close()
	if len(products) != 2 {
		t.Fatal("need two products")
	}

	discount := int64(15)
	lines := []any{
		map[string]any{"itemId": products[0].id, "quantity": 2, "discountAmount": discount},
		map[string]any{"itemId": products[1].id, "quantity": 1, "discountAmount": int64(0)},
	}
	expectedGross := products[0].price*2 + products[1].price*1
	expectedTotal := expectedGross - discount

	key := "cart-outbox-chk-" + token()
	chk := map[string]any{
		"action":         "checkout",
		"shiftId":        shift,
		"lines":          lines,
		"tender":         expectedTotal + 1000,
		"idempotencyKey": key,
	}

	w := request(a, "POST", path, chk, staff...)
	if w.Code != http.StatusOK {
		t.Fatalf("cart checkout failed: %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			SaleID        string `json:"saleId"`
			ReceiptNumber string `json:"receiptNumber"`
			Subtotal      int64  `json:"subtotal"`
			Discount      int64  `json:"discountAmount"`
			TotalAmount   int64  `json:"totalAmount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	// Verify outbox event
	var payloadStr string
	err = a.db.QueryRow(`SELECT payload FROM outbox_events WHERE organization_id=? AND event_type='pos.sale.completed' AND json_extract(payload, '$.sourceId')=?`, org, resp.Data.SaleID).Scan(&payloadStr)
	if err != nil {
		t.Fatalf("cart outbox event not found: %v", err)
	}

	var payload OutboxSaleCompletedPayload
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatal(err)
	}

	if payload.SourceID != resp.Data.SaleID {
		t.Fatalf("expected sourceId %s, got %s", resp.Data.SaleID, payload.SourceID)
	}
	if payload.ReceiptNumber != resp.Data.ReceiptNumber {
		t.Fatalf("expected receiptNumber %s, got %s", resp.Data.ReceiptNumber, payload.ReceiptNumber)
	}
	if payload.Subtotal != expectedGross {
		t.Fatalf("expected subtotal %d, got %d", expectedGross, payload.Subtotal)
	}
	if payload.DiscountAmount != discount {
		t.Fatalf("expected discount %d, got %d", discount, payload.DiscountAmount)
	}
	if payload.TotalAmount != expectedTotal {
		t.Fatalf("expected totalAmount %d, got %d", expectedTotal, payload.TotalAmount)
	}
	if len(payload.Lines) != 2 {
		t.Fatalf("expected 2 lines in payload, got %d", len(payload.Lines))
	}
	if payload.Lines[0].ItemID != products[0].id || payload.Lines[0].DiscountAmount != discount || payload.Lines[0].LineTotal != products[0].price*2-discount {
		t.Fatalf("unexpected line 0: %+v", payload.Lines[0])
	}
	if payload.Lines[1].ItemID != products[1].id || payload.Lines[1].LineTotal != products[1].price {
		t.Fatalf("unexpected line 1: %+v", payload.Lines[1])
	}
}

func TestPOSOutboxIdempotentRetry(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	staff := loginSite(t, a, "front@suite.test", space, "pos")

	var shift, item string
	var price int64
	if err := a.db.QueryRow("SELECT id FROM pos_shifts WHERE organization_id=? AND status='open' LIMIT 1", org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT id,price FROM pos_catalog_items WHERE organization_id=? AND stock_quantity>5 LIMIT 1", org).Scan(&item, &price); err != nil {
		t.Fatal(err)
	}

	// 1. Single-item checkout idempotent retry
	singleKey := "idem-test-outbox-single-" + token()
	singlePath := "/api/stations/pos/business"
	cmd := map[string]any{
		"action":         "checkout",
		"shiftId":        shift,
		"itemId":         item,
		"quantity":       1,
		"tender":         price * 2,
		"idempotencyKey": singleKey,
	}

	w1 := request(a, "POST", singlePath, cmd, staff...)
	if w1.Code != http.StatusOK {
		t.Fatalf("initial single checkout failed: %d %s", w1.Code, w1.Body.String())
	}
	var resp1 struct {
		Data struct {
			SaleID string `json:"saleId"`
		} `json:"data"`
	}
	json.Unmarshal(w1.Body.Bytes(), &resp1)

	// Idempotent retry
	w2 := request(a, "POST", singlePath, cmd, staff...)
	if w2.Code != http.StatusOK {
		t.Fatalf("single checkout retry failed: %d %s", w2.Code, w2.Body.String())
	}

	// Verify exactly 1 outbox event exists for this sale
	var count int
	if err := a.db.QueryRow(`SELECT count(*) FROM outbox_events WHERE organization_id=? AND event_type='pos.sale.completed' AND json_extract(payload, '$.sourceId')=?`, org, resp1.Data.SaleID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 outbox event after retry, got %d", count)
	}

	// 2. Cart checkout idempotent retry
	cartKey := "idem-test-outbox-cart-" + token()
	cartPath := "/api/stations/pos/cart"
	cartCmd := map[string]any{
		"action":         "checkout",
		"shiftId":        shift,
		"lines":          []any{map[string]any{"itemId": item, "quantity": 1}},
		"tender":         price * 2,
		"idempotencyKey": cartKey,
	}

	cw1 := request(a, "POST", cartPath, cartCmd, staff...)
	if cw1.Code != http.StatusOK {
		t.Fatalf("initial cart checkout failed: %d %s", cw1.Code, cw1.Body.String())
	}
	var cresp1 struct {
		Data struct {
			SaleID string `json:"saleId"`
		} `json:"data"`
	}
	json.Unmarshal(cw1.Body.Bytes(), &cresp1)

	cw2 := request(a, "POST", cartPath, cartCmd, staff...)
	if cw2.Code != http.StatusOK {
		t.Fatalf("cart checkout retry failed: %d %s", cw2.Code, cw2.Body.String())
	}

	if err := a.db.QueryRow(`SELECT count(*) FROM outbox_events WHERE organization_id=? AND event_type='pos.sale.completed' AND json_extract(payload, '$.sourceId')=?`, org, cresp1.Data.SaleID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 outbox event after cart retry, got %d", count)
	}
}

func TestPOSOutboxFailedCheckoutNoOrphan(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	staff := loginSite(t, a, "front@suite.test", space, "pos")

	var shift, item string
	var price int64
	var stock int
	if err := a.db.QueryRow("SELECT id FROM pos_shifts WHERE organization_id=? AND status='open' LIMIT 1", org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT id,price,stock_quantity FROM pos_catalog_items WHERE organization_id=? AND stock_quantity>0 LIMIT 1", org).Scan(&item, &price, &stock); err != nil {
		t.Fatal(err)
	}

	var initialCount int
	if err := a.db.QueryRow("SELECT count(*) FROM outbox_events WHERE organization_id=?", org).Scan(&initialCount); err != nil {
		t.Fatal(err)
	}

	// 1. Single checkout failure (insufficient stock)
	singleFail := map[string]any{
		"action":         "checkout",
		"shiftId":        shift,
		"itemId":         item,
		"quantity":       stock + 999,
		"tender":         price * int64(stock+999),
		"idempotencyKey": "fail-single-" + token(),
	}
	w := request(a, "POST", "/api/stations/pos/business", singleFail, staff...)
	if w.Code == http.StatusOK {
		t.Fatalf("expected failure for excessive quantity, got %d", w.Code)
	}

	var afterSingle int
	if err := a.db.QueryRow("SELECT count(*) FROM outbox_events WHERE organization_id=?", org).Scan(&afterSingle); err != nil {
		t.Fatal(err)
	}
	if afterSingle != initialCount {
		t.Fatalf("orphaned outbox event found after failed single checkout: %d -> %d", initialCount, afterSingle)
	}

	// 2. Cart checkout failure (tender less than total)
	cartFail := map[string]any{
		"action":         "checkout",
		"shiftId":        shift,
		"lines":          []any{map[string]any{"itemId": item, "quantity": 1}},
		"tender":         price - 1, // less than price
		"idempotencyKey": "fail-cart-" + token(),
	}
	cw := request(a, "POST", "/api/stations/pos/cart", cartFail, staff...)
	if cw.Code == http.StatusOK {
		t.Fatalf("expected failure for short tender, got %d", cw.Code)
	}

	var afterCart int
	if err := a.db.QueryRow("SELECT count(*) FROM outbox_events WHERE organization_id=?", org).Scan(&afterCart); err != nil {
		t.Fatal(err)
	}
	if afterCart != initialCount {
		t.Fatalf("orphaned outbox event found after failed cart checkout: %d -> %d", initialCount, afterCart)
	}
}

func TestPOSOutboxRefund(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "pos")
	front := loginSite(t, a, "front@suite.test", space, "pos")

	var shift, item string
	var price int64
	if err := a.db.QueryRow("SELECT id FROM pos_shifts WHERE organization_id=? AND status='open' LIMIT 1", org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT id,price FROM pos_catalog_items WHERE organization_id=? AND stock_quantity>=5 LIMIT 1", org).Scan(&item, &price); err != nil {
		t.Fatal(err)
	}

	// 1. Execute a sale first
	chkCmd := map[string]any{
		"action":         "checkout",
		"shiftId":        shift,
		"itemId":         item,
		"quantity":       2,
		"tender":         price * 2,
		"idempotencyKey": "sale-for-refund-" + token(),
	}
	chkW := request(a, "POST", "/api/stations/pos/business", chkCmd, front...)
	if chkW.Code != http.StatusOK {
		t.Fatalf("initial sale failed: %d %s", chkW.Code, chkW.Body.String())
	}
	var saleResp struct {
		Data struct {
			SaleID string `json:"saleId"`
		} `json:"data"`
	}
	json.Unmarshal(chkW.Body.Bytes(), &saleResp)

	var saleLineID string
	if err := a.db.QueryRow(`SELECT id FROM pos_sale_lines WHERE organization_id=? AND sale_id=? LIMIT 1`, org, saleResp.Data.SaleID).Scan(&saleLineID); err != nil {
		t.Fatal(err)
	}

	// 2. Perform a valid refund (as back-office admin)
	refundCmd := map[string]any{
		"action":         "refund",
		"saleLineId":     saleLineID,
		"quantity":       1,
		"reason":         "Customer return - defective",
		"idempotencyKey": "refund-outbox-" + token(),
	}
	refW := request(a, "POST", "/api/stations/pos/business", refundCmd, admin...)
	if refW.Code != http.StatusOK {
		t.Fatalf("refund failed: %d %s", refW.Code, refW.Body.String())
	}
	var refResp struct {
		Data struct {
			RefundID     string `json:"refundId"`
			RefundNumber string `json:"refundNumber"`
			RefundAmount int64  `json:"refundAmount"`
		} `json:"data"`
	}
	json.Unmarshal(refW.Body.Bytes(), &refResp)

	// Verify pos.sale.refunded outbox event
	var evtID, payloadStr string
	err := a.db.QueryRow(`SELECT id, payload FROM outbox_events WHERE organization_id=? AND event_type='pos.sale.refunded' AND json_extract(payload, '$.sourceId')=?`, org, refResp.Data.RefundID).Scan(&evtID, &payloadStr)
	if err != nil {
		t.Fatalf("refund outbox event not found: %v", err)
	}

	var payload OutboxSaleRefundedPayload
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatal(err)
	}

	if evtID != payload.EventID {
		t.Fatalf("mismatched refund event ID: %s vs %s", evtID, payload.EventID)
	}
	if payload.SchemaVersion != 1 {
		t.Fatalf("expected schemaVersion 1, got %d", payload.SchemaVersion)
	}
	if payload.OrganizationID != org {
		t.Fatalf("expected org %s, got %s", org, payload.OrganizationID)
	}
	if payload.SourceID != refResp.Data.RefundID {
		t.Fatalf("expected sourceId %s, got %s", refResp.Data.RefundID, payload.SourceID)
	}
	if payload.OriginalSaleID != saleResp.Data.SaleID {
		t.Fatalf("expected originalSaleId %s, got %s", saleResp.Data.SaleID, payload.OriginalSaleID)
	}
	if payload.RefundNumber != refResp.Data.RefundNumber {
		t.Fatalf("expected refundNumber %s, got %s", refResp.Data.RefundNumber, payload.RefundNumber)
	}
	if payload.RefundAmount != price {
		t.Fatalf("expected refundAmount %d, got %d", price, payload.RefundAmount)
	}
	if payload.Reason != "Customer return - defective" {
		t.Fatalf("expected reason 'Customer return - defective', got '%s'", payload.Reason)
	}
	if len(payload.Lines) != 1 || payload.Lines[0].SaleLineID != saleLineID || payload.Lines[0].Quantity != 1 || payload.Lines[0].Amount != price {
		t.Fatalf("unexpected refund lines: %+v", payload.Lines)
	}
	if _, err := time.Parse(time.RFC3339, payload.OccurredAt); err != nil {
		t.Fatalf("invalid occurredAt format: %s", payload.OccurredAt)
	}

	// 3. Verify failed refund does not emit orphaned outbox event
	var countBefore int
	a.db.QueryRow(`SELECT count(*) FROM outbox_events WHERE organization_id=? AND event_type='pos.sale.refunded'`, org).Scan(&countBefore)

	// Attempt to refund more than remaining quantity (already refunded 1 of 2, now ask for 2)
	badRefundCmd := map[string]any{
		"action":         "refund",
		"saleLineId":     saleLineID,
		"quantity":       2,
		"reason":         "Exceeds quantity",
		"idempotencyKey": "bad-refund-" + token(),
	}
	badW := request(a, "POST", "/api/stations/pos/business", badRefundCmd, admin...)
	if badW.Code == http.StatusOK {
		t.Fatalf("expected refund to fail for excessive quantity")
	}

	var countAfter int
	a.db.QueryRow(`SELECT count(*) FROM outbox_events WHERE organization_id=? AND event_type='pos.sale.refunded'`, org).Scan(&countAfter)
	if countAfter != countBefore {
		t.Fatalf("orphaned refund outbox event created on failure: %d -> %d", countBefore, countAfter)
	}
}
