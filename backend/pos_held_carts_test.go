package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestPOSHeldCartListIsScopedAndExposesOnlyHeldCartSnapshot(t *testing.T) {
	a := appForTest(t)
	space := token()
	front := loginSite(t, a, "front@suite.test", space, "pos")
	admin := loginSite(t, a, "admin@suite.test", space, "pos")
	viewer := loginSite(t, a, "viewer@suite.test", space, "pos")
	foreign := loginSite(t, a, "admin@suite.test", token(), "pos")
	org := "org_test_" + space
	var shift, item string
	var price int64
	if err := a.db.QueryRow(`SELECT id FROM pos_shifts WHERE organization_id=? AND cashier_id=(SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test') AND status='open' LIMIT 1`, org, org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id,price FROM pos_catalog_items WHERE organization_id=? AND is_active=1 ORDER BY id LIMIT 1`, org).Scan(&item, &price); err != nil {
		t.Fatal(err)
	}
	hold := func(name string) string {
		t.Helper()
		key := "held-list-hold-" + strings.ToLower(strings.ReplaceAll(name, " ", "-"))
		w := request(a, http.MethodPost, "/api/stations/pos/cart", map[string]any{
			"action": "hold", "shiftId": shift, "cartName": name,
			"lines": []any{map[string]any{"itemId": item, "quantity": 2, "discountAmount": 10}}, "idempotencyKey": key,
		}, front...)
		if w.Code != http.StatusOK {
			t.Fatalf("hold %q: %d %s", name, w.Code, w.Body.String())
		}
		var result struct {
			Data struct {
				CartID string `json:"cartId"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Data.CartID == "" {
			t.Fatalf("decode held cart: %s (%v)", w.Body.String(), err)
		}
		return result.Data.CartID
	}
	claimedID := hold("Claimed cart")
	listedID := hold("Return later")
	otherClaimedID := hold("Other cashier claimed")
	otherStoreID := hold("Other store")
	if _, err := a.db.Exec(`UPDATE pos_held_carts SET store_id='other-store' WHERE organization_id=? AND id=?`, org, otherStoreID); err != nil {
		t.Fatal(err)
	}
	claimed := request(a, http.MethodPost, "/api/stations/pos/cart", map[string]any{
		"action": "claim", "shiftId": shift, "cartId": claimedID, "idempotencyKey": "held-list-claim-0001",
	}, front...)
	if claimed.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", claimed.Code, claimed.Body.String())
	}
	claimed = request(a, http.MethodPost, "/api/stations/pos/cart", map[string]any{
		"action": "claim", "shiftId": shift, "cartId": otherClaimedID, "idempotencyKey": "held-list-claim-0002",
	}, admin...)
	if claimed.Code != http.StatusOK {
		t.Fatalf("admin claim: %d %s", claimed.Code, claimed.Body.String())
	}

	read := func(cookies ...*http.Cookie) (int, []byte) {
		t.Helper()
		w := request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId="+shift, nil, cookies...)
		return w.Code, w.Body.Bytes()
	}
	code, body := read(front...)
	if code != http.StatusOK {
		t.Fatalf("list held carts: %d %s", code, body)
	}
	var response struct {
		Data struct {
			Items []struct {
				CartID   string `json:"cartId"`
				CartName string `json:"cartName"`
				Status   string `json:"status"`
				Lines    []struct {
					ItemID         string `json:"itemId"`
					Quantity       int64  `json:"quantity"`
					DiscountAmount int64  `json:"discountAmount"`
					Available      bool   `json:"available"`
					Product        struct {
						Name          string `json:"name"`
						Barcode       string `json:"barcode"`
						Price         int64  `json:"price"`
						StockQuantity int64  `json:"stockQuantity"`
					} `json:"product"`
				} `json:"lines"`
			} `json:"items"`
			HasMore    bool   `json:"hasMore"`
			NextCursor string `json:"nextCursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode list: %s (%v)", body, err)
	}
	if len(response.Data.Items) != 2 || response.Data.HasMore || response.Data.NextCursor != "" {
		t.Fatalf("unexpected visible carts/page: %+v", response.Data)
	}
	var held, ownedClaim *struct {
		CartID   string `json:"cartId"`
		CartName string `json:"cartName"`
		Status   string `json:"status"`
		Lines    []struct {
			ItemID         string `json:"itemId"`
			Quantity       int64  `json:"quantity"`
			DiscountAmount int64  `json:"discountAmount"`
			Available      bool   `json:"available"`
			Product        struct {
				Name          string `json:"name"`
				Barcode       string `json:"barcode"`
				Price         int64  `json:"price"`
				StockQuantity int64  `json:"stockQuantity"`
			} `json:"product"`
		} `json:"lines"`
	}
	for i := range response.Data.Items {
		cart := &response.Data.Items[i]
		if cart.CartID == listedID {
			held = cart
		}
		if cart.CartID == claimedID {
			ownedClaim = cart
		}
		if cart.CartID == otherClaimedID || cart.CartID == otherStoreID {
			t.Fatalf("list exposed another user's claim or another store: %+v", *cart)
		}
	}
	if held == nil || held.Status != "held" || held.CartName != "Return later" || ownedClaim == nil || ownedClaim.Status != "retrieved" {
		t.Fatalf("unexpected held/owned-claim visibility: held=%+v claimed=%+v", held, ownedClaim)
	}
	if len(held.Lines) != 1 {
		t.Fatalf("unexpected lines: %+v", held.Lines)
	}
	line := held.Lines[0]
	if line.ItemID != item || line.Quantity != 2 || line.DiscountAmount != 10 || !line.Available || line.Product.Price != price || line.Product.Name == "" || line.Product.Barcode == "" {
		t.Fatalf("unexpected restorable line snapshot: %+v", line)
	}
	if strings.Contains(string(body), "lockedBy") || strings.Contains(string(body), "locked_by") || strings.Contains(string(body), "organizationId") || strings.Contains(string(body), "organization_id") {
		t.Fatalf("response leaked back-office fields: %s", body)
	}
	if code, body := read(admin...); code != http.StatusOK || !strings.Contains(string(body), listedID) || strings.Contains(string(body), claimedID) {
		t.Fatalf("same-organization admin should list authorized shift: %d %s", code, body)
	}
	if code, body := read(viewer...); code != http.StatusForbidden {
		t.Fatalf("read-only account should not list held carts: %d %s", code, body)
	}
	if code, body := read(foreign...); code != http.StatusNotFound {
		t.Fatalf("foreign organization should not resolve shift: %d %s", code, body)
	}
}

func TestPOSHeldCartListRejectsClosedOrMissingShift(t *testing.T) {
	a := appForTest(t)
	space := token()
	front := loginSite(t, a, "front@suite.test", space, "pos")
	w := request(a, http.MethodGet, "/api/stations/pos/held-carts", nil, front...)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing shift expected 400, got %d %s", w.Code, w.Body.String())
	}
	w = request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId=missing-shift", nil, front...)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing shift expected 404, got %d %s", w.Code, w.Body.String())
	}
	var shift string
	if err := a.db.QueryRow(`SELECT id FROM pos_shifts WHERE organization_id=? AND cashier_id=(SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test') AND status='open' LIMIT 1`, "org_test_"+space, "org_test_"+space).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE pos_shifts SET status='closed' WHERE organization_id=? AND id=?`, "org_test_"+space, shift); err != nil {
		t.Fatal(err)
	}
	w = request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId="+shift, nil, front...)
	if w.Code != http.StatusNotFound {
		t.Fatalf("closed shift expected 404, got %d %s", w.Code, w.Body.String())
	}
}

func TestPOSClaimedCartRecoveryAndServerValidatedCorrection(t *testing.T) {
	a := appForTest(t)
	space := token()
	front := loginSite(t, a, "front@suite.test", space, "pos")
	admin := loginSite(t, a, "admin@suite.test", space, "pos")
	foreign := loginSite(t, a, "admin@suite.test", token(), "pos")
	org := "org_test_" + space
	var shift, item string
	var price, stock int64
	if err := a.db.QueryRow(`SELECT id FROM pos_shifts WHERE organization_id=? AND cashier_id=(SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test') AND status='open' LIMIT 1`, org, org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id,price,stock_quantity FROM pos_catalog_items WHERE organization_id=? AND is_active=1 ORDER BY id LIMIT 1`, org).Scan(&item, &price, &stock); err != nil {
		t.Fatal(err)
	}
	hold := request(a, http.MethodPost, "/api/stations/pos/cart", map[string]any{
		"action": "hold", "shiftId": shift, "cartName": "Reprice correction",
		"lines": []any{map[string]any{"itemId": item, "quantity": 2, "discountAmount": price}}, "idempotencyKey": "recover-hold-000001",
	}, front...)
	if hold.Code != http.StatusOK {
		t.Fatalf("hold: %d %s", hold.Code, hold.Body.String())
	}
	var held struct {
		Data struct {
			CartID string `json:"cartId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(hold.Body.Bytes(), &held); err != nil || held.Data.CartID == "" {
		t.Fatalf("decode hold response: %s (%v)", hold.Body.String(), err)
	}
	cartID := held.Data.CartID
	claim := request(a, http.MethodPost, "/api/stations/pos/cart", map[string]any{
		"action": "claim", "shiftId": shift, "cartId": cartID, "idempotencyKey": "recover-claim-0001",
	}, front...)
	if claim.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", claim.Code, claim.Body.String())
	}
	if _, err := a.db.Exec(`UPDATE pos_catalog_items SET price=? WHERE organization_id=? AND id=?`, price/4, org, item); err != nil {
		t.Fatal(err)
	}
	checkout := request(a, http.MethodPost, "/api/stations/pos/cart", map[string]any{
		"action": "checkout", "shiftId": shift, "cartId": cartID,
		"lines":  []any{map[string]any{"itemId": item, "quantity": 2, "discountAmount": 0}},
		"tender": price * 2, "idempotencyKey": "recover-checkout-0001",
	}, front...)
	if checkout.Code != http.StatusBadRequest || !strings.Contains(checkout.Body.String(), "Invalid line discount") {
		t.Fatalf("stale cart discount should be rejected without mutation: %d %s", checkout.Code, checkout.Body.String())
	}
	var saleCount, stockAfter int64
	var state string
	if err := a.db.QueryRow(`SELECT count(*) FROM pos_sales WHERE organization_id=? AND idempotency_key='recover-checkout-0001'`, org).Scan(&saleCount); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, org, item).Scan(&stockAfter); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT status FROM pos_held_carts WHERE organization_id=? AND id=?`, org, cartID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if saleCount != 0 || stockAfter != stock || state != "retrieved" {
		t.Fatalf("stale checkout mutated state: sales=%d stock=%d/%d cart=%s", saleCount, stockAfter, stock, state)
	}
	list := request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId="+shift, nil, front...)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"status":"retrieved"`) || !strings.Contains(list.Body.String(), cartID) {
		t.Fatalf("claimant must be able to recover claimed cart: %d %s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), `"lockedBy"`) || strings.Contains(list.Body.String(), `"locked_by"`) {
		t.Fatalf("recovery list leaked claimant metadata: %s", list.Body.String())
	}
	foreignList := request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId="+shift, nil, foreign...)
	if foreignList.Code != http.StatusNotFound || strings.Contains(foreignList.Body.String(), cartID) {
		t.Fatalf("foreign organization observed claimed cart: %d %s", foreignList.Code, foreignList.Body.String())
	}
	corrected := map[string]any{
		"action": "update", "shiftId": shift, "cartId": cartID,
		"lines":          []any{map[string]any{"itemId": item, "quantity": 2, "discountAmount": price / 4}},
		"idempotencyKey": "recover-update-0001",
	}
	if w := request(a, http.MethodPost, "/api/stations/pos/cart", corrected, admin...); w.Code != http.StatusConflict {
		t.Fatalf("non-claimant admin cannot rewrite cart: %d %s", w.Code, w.Body.String())
	}
	invalid := map[string]any{
		"action": "update", "shiftId": shift, "cartId": cartID,
		"lines":          []any{map[string]any{"itemId": item, "quantity": 2, "discountAmount": price}},
		"idempotencyKey": "recover-update-invalid",
	}
	if w := request(a, http.MethodPost, "/api/stations/pos/cart", invalid, front...); w.Code != http.StatusBadRequest {
		t.Fatalf("correction above current gross must fail: %d %s", w.Code, w.Body.String())
	}
	updated := request(a, http.MethodPost, "/api/stations/pos/cart", corrected, front...)
	if updated.Code != http.StatusOK {
		t.Fatalf("valid correction: %d %s", updated.Code, updated.Body.String())
	}
	if replay := request(a, http.MethodPost, "/api/stations/pos/cart", corrected, front...); replay.Code != http.StatusOK {
		t.Fatalf("correction replay: %d %s", replay.Code, replay.Body.String())
	}
	changed := map[string]any{
		"action": "update", "shiftId": shift, "cartId": cartID,
		"lines":          []any{map[string]any{"itemId": item, "quantity": 2, "discountAmount": 0}},
		"idempotencyKey": "recover-update-0001",
	}
	if conflict := request(a, http.MethodPost, "/api/stations/pos/cart", changed, front...); conflict.Code != http.StatusConflict {
		t.Fatalf("changed payload must conflict on replay key: %d %s", conflict.Code, conflict.Body.String())
	}
	checkout = request(a, http.MethodPost, "/api/stations/pos/cart", map[string]any{
		"action": "checkout", "shiftId": shift, "cartId": cartID,
		"lines":  []any{map[string]any{"itemId": item, "quantity": 2, "discountAmount": 0}},
		"tender": price, "idempotencyKey": "recover-checkout-valid-001",
	}, front...)
	if checkout.Code != http.StatusOK {
		t.Fatalf("checkout corrected snapshot: %d %s", checkout.Code, checkout.Body.String())
	}
	var receipt struct {
		Data struct {
			Subtotal int64 `json:"subtotal"`
			Discount int64 `json:"discountAmount"`
			Total    int64 `json:"totalAmount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(checkout.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	currentPrice := price / 4
	if receipt.Data.Subtotal != currentPrice*2 || receipt.Data.Discount != currentPrice || receipt.Data.Total != currentPrice {
		t.Fatalf("checkout did not use server-corrected line data: %+v", receipt.Data)
	}
	if err := a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, org, item).Scan(&stockAfter); err != nil {
		t.Fatal(err)
	}
	if stockAfter != stock-2 {
		t.Fatalf("corrected checkout deducted unexpected stock: %d -> %d", stock, stockAfter)
	}
}

func TestPOSHeldCartListPrioritizesOwnedRecoveryRows(t *testing.T) {
	a := appForTest(t)
	space := token()
	front := loginSite(t, a, "front@suite.test", space, "pos")
	org := "org_test_" + space
	var shift, store, item string
	if err := a.db.QueryRow(`SELECT id,store_id FROM pos_shifts WHERE organization_id=? AND cashier_id=(SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test') AND status='open' LIMIT 1`, org, org).Scan(&shift, &store); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id FROM pos_catalog_items WHERE organization_id=? AND is_active=1 ORDER BY id LIMIT 1`, org).Scan(&item); err != nil {
		t.Fatal(err)
	}
	hold := request(a, http.MethodPost, "/api/stations/pos/cart", map[string]any{
		"action": "hold", "shiftId": shift, "cartName": "Must recover",
		"lines": []any{map[string]any{"itemId": item, "quantity": 1}}, "idempotencyKey": "priority-recovery-hold",
	}, front...)
	var held struct {
		Data struct {
			CartID string `json:"cartId"`
		} `json:"data"`
	}
	if hold.Code != http.StatusOK || json.Unmarshal(hold.Body.Bytes(), &held) != nil {
		t.Fatalf("hold: %d %s", hold.Code, hold.Body.String())
	}
	claim := request(a, http.MethodPost, "/api/stations/pos/cart", map[string]any{
		"action": "claim", "shiftId": shift, "cartId": held.Data.CartID, "idempotencyKey": "priority-recovery-claim",
	}, front...)
	if claim.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", claim.Code, claim.Body.String())
	}
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("older-held-%03d", i)
		if _, err := a.db.Exec(`INSERT INTO pos_held_carts(id,organization_id,store_id,cart_name,items_json,status,held_at) VALUES(?,?,?,?,?,'held','2000-01-01T00:00:00Z')`, id, org, store, id, `[{"itemId":"`+item+`","quantity":1}]`); err != nil {
			t.Fatal(err)
		}
	}
	list := request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId="+shift, nil, front...)
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	var response struct {
		Data struct {
			Items []struct {
				CartID string `json:"cartId"`
				Status string `json:"status"`
			} `json:"items"`
			HasMore    bool   `json:"hasMore"`
			NextCursor string `json:"nextCursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Items) != 50 || !response.Data.HasMore || response.Data.NextCursor == "" {
		t.Fatalf("expected bounded first page with cursor, got %+v", response.Data)
	}
	for _, cart := range response.Data.Items {
		if cart.CartID == held.Data.CartID && cart.Status == "retrieved" {
			return
		}
	}
	t.Fatalf("owned retrieved cart was dropped behind older shared carts: %s", list.Body.String())
}

func TestPOSClaimedCartDiscardIsOwnerOnlyIdempotentAndHasNoSaleEffects(t *testing.T) {
	a := appForTest(t)
	space := token()
	front := loginSite(t, a, "front@suite.test", space, "pos")
	admin := loginSite(t, a, "admin@suite.test", space, "pos")
	org := "org_test_" + space
	var shift, item string
	var stock int64
	if err := a.db.QueryRow(`SELECT id FROM pos_shifts WHERE organization_id=? AND cashier_id=(SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test') AND status='open' LIMIT 1`, org, org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id,stock_quantity FROM pos_catalog_items WHERE organization_id=? AND is_active=1 ORDER BY id LIMIT 1`, org).Scan(&item, &stock); err != nil {
		t.Fatal(err)
	}
	hold := request(a, http.MethodPost, "/api/stations/pos/cart", map[string]any{
		"action": "hold", "shiftId": shift, "cartName": "Unavailable product",
		"lines": []any{map[string]any{"itemId": item, "quantity": 2}}, "idempotencyKey": "discard-hold-0001",
	}, front...)
	var held struct {
		Data struct {
			CartID string `json:"cartId"`
		} `json:"data"`
	}
	if hold.Code != http.StatusOK || json.Unmarshal(hold.Body.Bytes(), &held) != nil {
		t.Fatalf("hold: %d %s", hold.Code, hold.Body.String())
	}
	claim := request(a, http.MethodPost, "/api/stations/pos/cart", map[string]any{
		"action": "claim", "shiftId": shift, "cartId": held.Data.CartID, "idempotencyKey": "discard-claim-0001",
	}, front...)
	if claim.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", claim.Code, claim.Body.String())
	}
	discard := map[string]any{"action": "discard", "shiftId": shift, "cartId": held.Data.CartID, "idempotencyKey": "discard-cart-0001"}
	if w := request(a, http.MethodPost, "/api/stations/pos/cart", discard, admin...); w.Code != http.StatusConflict {
		t.Fatalf("non-claimant admin discard: %d %s", w.Code, w.Body.String())
	}
	w := request(a, http.MethodPost, "/api/stations/pos/cart", discard, front...)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("owner discard: %d %s", w.Code, w.Body.String())
	}
	if replay := request(a, http.MethodPost, "/api/stations/pos/cart", discard, front...); replay.Code != http.StatusOK {
		t.Fatalf("discard replay: %d %s", replay.Code, replay.Body.String())
	}
	if retryConflict := request(a, http.MethodPost, "/api/stations/pos/cart", map[string]any{
		"action": "discard", "shiftId": shift, "cartId": "different-cart", "idempotencyKey": discard["idempotencyKey"],
	}, front...); retryConflict.Code != http.StatusConflict {
		t.Fatalf("changed discard payload should conflict: %d %s", retryConflict.Code, retryConflict.Body.String())
	}
	var state, locked string
	var stockAfter, sales, audit, idem int64
	if err := a.db.QueryRow(`SELECT status,COALESCE(locked_by,'') FROM pos_held_carts WHERE organization_id=? AND id=?`, org, held.Data.CartID).Scan(&state, &locked); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, org, item).Scan(&stockAfter); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM pos_sales WHERE organization_id=?`, org).Scan(&sales); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM audit_events WHERE organization_id=? AND entity_type='pos_cart' AND entity_id=? AND action='pos.cart.discard'`, org, held.Data.CartID).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint='/station/pos/cart/discard'`, org, discard["idempotencyKey"]).Scan(&idem); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" || locked != "" || stockAfter != stock || sales != 0 || audit != 1 || idem != 1 {
		t.Fatalf("discard effects state=%s locked=%s stock=%d/%d sales=%d audit=%d idem=%d", state, locked, stockAfter, stock, sales, audit, idem)
	}
	list := request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId="+shift, nil, front...)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), held.Data.CartID) {
		t.Fatalf("cancelled cart remained visible: %d %s", list.Code, list.Body.String())
	}
}

func TestPOSHeldCartListPaginatesAllOwnedAndSharedRowsAndSupportsTargetRead(t *testing.T) {
	a := appForTest(t)
	space := token()
	front := loginSite(t, a, "front@suite.test", space, "pos")
	admin := loginSite(t, a, "admin@suite.test", space, "pos")
	foreign := loginSite(t, a, "admin@suite.test", token(), "pos")
	org := "org_test_" + space
	var shift, store, cashier string
	if err := a.db.QueryRow(`SELECT id,store_id,cashier_id FROM pos_shifts WHERE organization_id=? AND cashier_id=(SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test') AND status='open' LIMIT 1`, org, org).Scan(&shift, &store, &cashier); err != nil {
		t.Fatal(err)
	}
	var item string
	if err := a.db.QueryRow(`SELECT id FROM pos_catalog_items WHERE organization_id=? AND is_active=1 ORDER BY id LIMIT 1`, org).Scan(&item); err != nil {
		t.Fatal(err)
	}
	const ownedCount, heldCount = 117, 137
	for i := 0; i < ownedCount; i++ {
		id := fmt.Sprintf("owned-page-%03d", i)
		heldAt := fmt.Sprintf("2030-01-%02dT00:00:%02dZ", i/60+1, i%60)
		if _, err := a.db.Exec(`INSERT INTO pos_held_carts(id,organization_id,store_id,cart_name,items_json,status,locked_by,held_at) VALUES(?,?,?,?,?,'retrieved',?,?)`, id, org, store, id, `[{"itemId":"`+item+`","quantity":1}]`, cashier, heldAt); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < heldCount; i++ {
		id := fmt.Sprintf("shared-page-%03d", i)
		heldAt := fmt.Sprintf("2029-01-%02dT00:00:%02dZ", i/60+1, i%60)
		if _, err := a.db.Exec(`INSERT INTO pos_held_carts(id,organization_id,store_id,cart_name,items_json,status,held_at) VALUES(?,?,?,?,?,'held',?)`, id, org, store, id, `[{"itemId":"`+item+`","quantity":1}]`, heldAt); err != nil {
			t.Fatal(err)
		}
	}

	type pageEnvelope struct {
		Data struct {
			Items []struct {
				CartID string `json:"cartId"`
				Status string `json:"status"`
			} `json:"items"`
			HasMore    bool   `json:"hasMore"`
			NextCursor string `json:"nextCursor"`
		} `json:"data"`
	}
	cursor := ""
	seen := make(map[string]string)
	seenHeld := false
	firstCursor := ""
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		path := "/api/stations/pos/held-carts?shiftId=" + shift + "&limit=50"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		response := request(a, http.MethodGet, path, nil, front...)
		if response.Code != http.StatusOK {
			t.Fatalf("page %d: %d %s", pageNumber, response.Code, response.Body.String())
		}
		var page pageEnvelope
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode page %d: %v", pageNumber, err)
		}
		if len(page.Data.Items) == 0 || len(page.Data.Items) > 50 {
			t.Fatalf("page %d size=%d", pageNumber, len(page.Data.Items))
		}
		for _, cart := range page.Data.Items {
			if cart.Status == "held" {
				seenHeld = true
			}
			if cart.Status == "retrieved" && seenHeld {
				t.Fatalf("owned retrieved rows must precede held rows")
			}
			if _, duplicate := seen[cart.CartID]; duplicate {
				t.Fatalf("duplicate row across pages: %s", cart.CartID)
			}
			seen[cart.CartID] = cart.Status
		}
		if page.Data.HasMore != (page.Data.NextCursor != "") {
			t.Fatalf("page %d inconsistent continuation: %+v", pageNumber, page.Data)
		}
		if !page.Data.HasMore {
			break
		}
		if page.Data.NextCursor == cursor {
			t.Fatalf("page %d repeated cursor", pageNumber)
		}
		if firstCursor == "" {
			firstCursor = page.Data.NextCursor
		}
		cursor = page.Data.NextCursor
	}
	if len(seen) != ownedCount+heldCount {
		t.Fatalf("pagination exposed %d/%d scoped rows", len(seen), ownedCount+heldCount)
	}
	for i := 0; i < ownedCount; i++ {
		if seen[fmt.Sprintf("owned-page-%03d", i)] != "retrieved" {
			t.Fatalf("missing owned retrieved cart %d", i)
		}
	}
	for i := 0; i < heldCount; i++ {
		if seen[fmt.Sprintf("shared-page-%03d", i)] != "held" {
			t.Fatalf("missing shared held cart %d", i)
		}
	}

	target := request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId="+shift+"&cartId=owned-page-116", nil, front...)
	if target.Code != http.StatusOK || !strings.Contains(target.Body.String(), `"cartId":"owned-page-116"`) || strings.Contains(target.Body.String(), `"cartId":"owned-page-115"`) {
		t.Fatalf("owner target read failed: %d %s", target.Code, target.Body.String())
	}
	foreignTarget := request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId="+shift+"&cartId=owned-page-116", nil, foreign...)
	if foreignTarget.Code != http.StatusNotFound || strings.Contains(foreignTarget.Body.String(), "owned-page-116") {
		t.Fatalf("foreign target read disclosed cart: %d %s", foreignTarget.Code, foreignTarget.Body.String())
	}
	otherCashierTarget := request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId="+shift+"&cartId=owned-page-116", nil, admin...)
	if otherCashierTarget.Code != http.StatusOK || strings.Contains(otherCashierTarget.Body.String(), "owned-page-116") {
		t.Fatalf("same-org other cashier should not read the claim: %d %s", otherCashierTarget.Code, otherCashierTarget.Body.String())
	}
	wrongUserCursor := request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId="+shift+"&cursor="+firstCursor, nil, admin...)
	if wrongUserCursor.Code != http.StatusBadRequest {
		t.Fatalf("cursor must be bound to caller: %d %s", wrongUserCursor.Code, wrongUserCursor.Body.String())
	}
	malformedCursor := request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId="+shift+"&cursor=not-a-cursor", nil, front...)
	if malformedCursor.Code != http.StatusBadRequest {
		t.Fatalf("malformed cursor expected 400: %d %s", malformedCursor.Code, malformedCursor.Body.String())
	}
	badLimit := request(a, http.MethodGet, "/api/stations/pos/held-carts?shiftId="+shift+"&limit=500", nil, front...)
	if badLimit.Code != http.StatusBadRequest {
		t.Fatalf("oversized page limit should be rejected: %d %s", badLimit.Code, badLimit.Body.String())
	}
}
