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

func seedClaimedCartForRetryTest(t *testing.T, a *App, staff []*http.Cookie, org, shift, item string, quantity int64) string {
	t.Helper()
	hold := map[string]any{
		"action": "hold", "shiftId": shift, "cartName": "Retry validation",
		"lines": []any{map[string]any{"itemId": item, "quantity": quantity}}, "idempotencyKey": "cart-terminal-hold-01",
	}
	w := request(a, http.MethodPost, "/api/stations/pos/cart", hold, staff...)
	if w.Code != http.StatusOK {
		t.Fatalf("hold cart: %d %s", w.Code, w.Body.String())
	}
	var held struct {
		Data struct {
			CartID string `json:"cartId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &held); err != nil {
		t.Fatal(err)
	}
	claim := map[string]any{"action": "claim", "shiftId": shift, "cartId": held.Data.CartID, "idempotencyKey": "cart-terminal-claim-01"}
	if w := request(a, http.MethodPost, "/api/stations/pos/cart", claim, staff...); w.Code != http.StatusOK {
		t.Fatalf("claim cart: %d %s", w.Code, w.Body.String())
	}
	return held.Data.CartID
}

func TestPOSCartTerminalCheckoutRejectionIsDurableAndLeavesCartUnchanged(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	staff := loginSite(t, a, "front@suite.test", space, "pos")
	var shift, item string
	var price, initialStock int64
	if err := a.db.QueryRow(`SELECT id FROM pos_shifts WHERE organization_id=? AND status='open'`, org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id,price,stock_quantity FROM pos_catalog_items WHERE organization_id=? AND is_active=1 LIMIT 1`, org).Scan(&item, &price, &initialStock); err != nil {
		t.Fatal(err)
	}
	cartID := seedClaimedCartForRetryTest(t, a, staff, org, shift, item, 1)
	if _, err := a.db.Exec(`UPDATE pos_catalog_items SET stock_quantity=0 WHERE organization_id=? AND id=?`, org, item); err != nil {
		t.Fatal(err)
	}
	path := "/api/stations/pos/cart"
	key := "cart-terminal-checkout-01"
	attempt := map[string]any{"action": "checkout", "shiftId": shift, "cartId": cartID, "tender": price, "idempotencyKey": key}
	first := request(a, http.MethodPost, path, attempt, staff...)
	if first.Code != http.StatusConflict {
		t.Fatalf("insufficient stock status %d: %s", first.Code, first.Body.String())
	}
	var firstEnvelope struct {
		Error struct {
			Outcome string `json:"outcome"`
		} `json:"error"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstEnvelope); err != nil || firstEnvelope.Error.Outcome != "not_applied" {
		t.Fatalf("expected terminal rejection marker, response=%s err=%v", first.Body.String(), err)
	}
	replay := request(a, http.MethodPost, path, attempt, staff...)
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() {
		t.Fatalf("terminal replay differs: first=%d %s replay=%d %s", first.Code, first.Body.String(), replay.Code, replay.Body.String())
	}
	changed := map[string]any{"action": "checkout", "shiftId": shift, "cartId": cartID, "tender": price + 1, "idempotencyKey": key}
	if w := request(a, http.MethodPost, path, changed, staff...); w.Code != http.StatusConflict || strings.Contains(w.Body.String(), `"outcome":"not_applied"`) {
		t.Fatalf("changed payload should conflict without a no-effect marker: %d %s", w.Code, w.Body.String())
	}
	var sales, stock int64
	var status string
	if err := a.db.QueryRow(`SELECT count(*) FROM pos_sales WHERE organization_id=? AND idempotency_key=?`, org, key).Scan(&sales); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, org, item).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT status FROM pos_held_carts WHERE organization_id=? AND id=?`, org, cartID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if sales != 0 || stock != 0 || status != "retrieved" {
		t.Fatalf("rejected checkout mutated state: sales=%d stock=%d cart=%s", sales, stock, status)
	}
	if _, err := a.db.Exec(`UPDATE pos_catalog_items SET stock_quantity=1 WHERE organization_id=? AND id=?`, org, item); err != nil {
		t.Fatal(err)
	}
	corrected := map[string]any{"action": "checkout", "shiftId": shift, "cartId": cartID, "tender": price + 1, "idempotencyKey": "cart-terminal-checkout-02"}
	if w := request(a, http.MethodPost, path, corrected, staff...); w.Code != http.StatusOK {
		t.Fatalf("corrected checkout with fresh key failed: %d %s", w.Code, w.Body.String())
	}
}

func TestPOSCartPartialCheckoutFailureRollsBackBeforeMarker(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	staff := loginSite(t, a, "front@suite.test", space, "pos")
	var shift string
	if err := a.db.QueryRow(`SELECT id FROM pos_shifts WHERE organization_id=? AND status='open'`, org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	rows, err := a.db.Query(`SELECT id,price,stock_quantity FROM pos_catalog_items WHERE organization_id=? AND is_active=1 AND stock_quantity>0 ORDER BY id LIMIT 2`, org)
	if err != nil {
		t.Fatal(err)
	}
	type product struct {
		id           string
		price, stock int64
	}
	products := []product{}
	for rows.Next() {
		var p product
		if err := rows.Scan(&p.id, &p.price, &p.stock); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		products = append(products, p)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(products) != 2 {
		t.Fatal("need two in-stock products")
	}
	trigger := fmt.Sprintf(`CREATE TRIGGER force_later_cart_stock_short AFTER UPDATE OF stock_quantity ON pos_catalog_items WHEN NEW.id='%s' BEGIN UPDATE pos_catalog_items SET stock_quantity=0 WHERE id='%s'; END`, products[0].id, products[1].id)
	if _, err := a.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	key := "cart-partial-rollback-01"
	in := map[string]any{
		"action": "checkout", "shiftId": shift,
		"lines":  []any{map[string]any{"itemId": products[0].id, "quantity": 1}, map[string]any{"itemId": products[1].id, "quantity": 1}},
		"tender": products[0].price + products[1].price, "idempotencyKey": key,
	}
	w := request(a, http.MethodPost, "/api/stations/pos/cart", in, staff...)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"outcome":"not_applied"`) {
		t.Fatalf("second stock update should fail terminally: %d %s", w.Code, w.Body.String())
	}
	for _, p := range products {
		var stock int64
		if err := a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, org, p.id).Scan(&stock); err != nil {
			t.Fatal(err)
		}
		if stock != p.stock {
			t.Fatalf("partial checkout changed stock for %s: %d want %d", p.id, stock, p.stock)
		}
	}
	var sales, lines, idem int
	if err := a.db.QueryRow(`SELECT count(*) FROM pos_sales WHERE organization_id=? AND idempotency_key=?`, org, key).Scan(&sales); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM pos_sale_lines WHERE organization_id=? AND sale_id IN (SELECT id FROM pos_sales WHERE idempotency_key=?)`, org, key).Scan(&lines); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint='/station/pos/cart/checkout'`, org, key).Scan(&idem); err != nil {
		t.Fatal(err)
	}
	if sales != 0 || lines != 0 || idem != 1 {
		t.Fatalf("partial rejection state sales=%d lines=%d idem=%d", sales, lines, idem)
	}
}

func TestPOSCartSameKeyValidationRaceAcrossDBHandlesReservesOneOutcome(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cart-race.sqlite")
	dbA, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbA.Close() })
	a := &App{db: dbA}
	space := token()
	org := "org_test_" + space
	staff := loginSite(t, a, "front@suite.test", space, "pos")
	var shift, item string
	var price, initialStock int64
	if err := a.db.QueryRow(`SELECT id FROM pos_shifts WHERE organization_id=? AND status='open'`, org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id,price,stock_quantity FROM pos_catalog_items WHERE organization_id=? AND is_active=1 LIMIT 1`, org).Scan(&item, &price, &initialStock); err != nil {
		t.Fatal(err)
	}
	cartID := seedClaimedCartForRetryTest(t, a, staff, org, shift, item, 1)
	dbB, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbB.Close() })
	b := &App{db: dbB}
	key := "cart-validation-race-01"
	invalid := map[string]any{"action": "checkout", "shiftId": shift, "cartId": cartID, "tender": price - 1, "idempotencyKey": key}
	valid := map[string]any{"action": "checkout", "shiftId": shift, "cartId": cartID, "tender": price, "idempotencyKey": key}
	start := make(chan struct{})
	type result struct {
		code int
		body string
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, call := range []struct {
		app  *App
		body map[string]any
	}{{a, invalid}, {b, valid}} {
		wg.Add(1)
		go func(app *App, body map[string]any) {
			defer wg.Done()
			<-start
			w := request(app, http.MethodPost, "/api/stations/pos/cart", body, staff...)
			results <- result{w.Code, w.Body.String()}
		}(call.app, call.body)
	}
	close(start)
	wg.Wait()
	close(results)
	seenCodes := []int{}
	for r := range results {
		seenCodes = append(seenCodes, r.code)
	}
	var sales, stock, idem int64
	var cartStatus string
	if err := a.db.QueryRow(`SELECT count(*) FROM pos_sales WHERE organization_id=? AND idempotency_key=?`, org, key).Scan(&sales); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, org, item).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT status FROM pos_held_carts WHERE organization_id=? AND id=?`, org, cartID).Scan(&cartStatus); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint='/station/pos/cart/checkout'`, org, key).Scan(&idem); err != nil {
		t.Fatal(err)
	}
	if idem != 1 || sales > 1 || (sales == 0 && (stock != initialStock || cartStatus != "retrieved")) || (sales == 1 && (stock != initialStock-1 || cartStatus != "completed")) {
		t.Fatalf("race left inconsistent state: codes=%v idem=%d sales=%d stock=%d cart=%s", seenCodes, idem, sales, stock, cartStatus)
	}
	if sales == 0 {
		var status int
		var body string
		if err := a.db.QueryRow(`SELECT response_status,response_body FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint='/station/pos/cart/checkout'`, org, key).Scan(&status, &body); err != nil {
			t.Fatal(err)
		}
		if status != http.StatusBadRequest || !strings.Contains(body, `"outcome":"not_applied"`) || !json.Valid([]byte(body)) {
			t.Fatalf("invalid request must reserve rejection: status=%d body=%s", status, body)
		}
	}
}

func TestPOSCartClosedShiftRejectsCheckoutWithMarkerAndAllowsSafeRelease(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	staff := loginSite(t, a, "front@suite.test", space, "pos")
	var shift, item string
	var price int64
	if err := a.db.QueryRow(`SELECT id FROM pos_shifts WHERE organization_id=? AND status='open'`, org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id,price FROM pos_catalog_items WHERE organization_id=? AND is_active=1 LIMIT 1`, org).Scan(&item, &price); err != nil {
		t.Fatal(err)
	}
	cartID := seedClaimedCartForRetryTest(t, a, staff, org, shift, item, 1)
	if _, err := a.db.Exec(`UPDATE pos_shifts SET status='closed' WHERE organization_id=? AND id=?`, org, shift); err != nil {
		t.Fatal(err)
	}
	checkout := map[string]any{"action": "checkout", "shiftId": shift, "cartId": cartID, "tender": price, "idempotencyKey": "closed-cart-checkout-01"}
	first := request(a, http.MethodPost, "/api/stations/pos/cart", checkout, staff...)
	if first.Code != http.StatusConflict || !strings.Contains(first.Body.String(), `"outcome":"not_applied"`) {
		t.Fatalf("closed shift checkout must be durably rejected: %d %s", first.Code, first.Body.String())
	}
	if replay := request(a, http.MethodPost, "/api/stations/pos/cart", checkout, staff...); replay.Code != first.Code || replay.Body.String() != first.Body.String() {
		t.Fatalf("closed shift rejection replay differs: %d %s", replay.Code, replay.Body.String())
	}
	release := map[string]any{"action": "release", "shiftId": shift, "cartId": cartID, "idempotencyKey": "closed-cart-release-01"}
	if w := request(a, http.MethodPost, "/api/stations/pos/cart", release, staff...); w.Code != http.StatusOK {
		t.Fatalf("closed shift claimant must be able to safely release cart: %d %s", w.Code, w.Body.String())
	}
	var status string
	if err := a.db.QueryRow(`SELECT status FROM pos_held_carts WHERE organization_id=? AND id=?`, org, cartID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "held" {
		t.Fatalf("released cart status=%q", status)
	}
}

func TestPOSAuthorizedStaleCartActionsReserveAndReplayNoEffectConflicts(t *testing.T) {
	for _, action := range []string{"release", "update", "discard", "checkout"} {
		t.Run(action, func(t *testing.T) {
			a := appForTest(t)
			space := token()
			org := "org_test_" + space
			staff := loginSite(t, a, "front@suite.test", space, "pos")
			var shift, item string
			var price int64
			if err := a.db.QueryRow(`SELECT id FROM pos_shifts WHERE organization_id=? AND status='open'`, org).Scan(&shift); err != nil {
				t.Fatal(err)
			}
			if err := a.db.QueryRow(`SELECT id,price FROM pos_catalog_items WHERE organization_id=? AND is_active=1 LIMIT 1`, org).Scan(&item, &price); err != nil {
				t.Fatal(err)
			}
			cartID := seedClaimedCartForRetryTest(t, a, staff, org, shift, item, 1)
			var ownerID string
			for _, c := range staff {
				if c.Name == cookieName+"_pos" {
					if err := a.db.QueryRow(`SELECT user_id FROM station_sessions WHERE token_hash=?`, digest(c.Value)).Scan(&ownerID); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			if ownerID == "" {
				t.Fatal("could not resolve claimant")
			}
			if _, err := a.db.Exec(`UPDATE pos_held_carts SET status='completed',locked_by=? WHERE organization_id=? AND id=?`, ownerID, org, cartID); err != nil {
				t.Fatal(err)
			}
			key := "stale-" + action + "-key-01"
			input := map[string]any{"action": action, "shiftId": shift, "cartId": cartID, "idempotencyKey": key}
			if action == "update" {
				input["lines"] = []any{map[string]any{"itemId": item, "quantity": 1, "discountAmount": 0}}
			}
			if action == "checkout" {
				input["tender"] = price
			}
			first := request(a, http.MethodPost, "/api/stations/pos/cart", input, staff...)
			if first.Code != http.StatusConflict || !strings.Contains(first.Body.String(), `"outcome":"not_applied"`) {
				t.Fatalf("authorized stale %s should receive durable no-effect conflict: %d %s", action, first.Code, first.Body.String())
			}
			replay := request(a, http.MethodPost, "/api/stations/pos/cart", input, staff...)
			if replay.Code != first.Code || replay.Body.String() != first.Body.String() {
				t.Fatalf("stale %s retry differs: first=%d %s replay=%d %s", action, first.Code, first.Body.String(), replay.Code, replay.Body.String())
			}
			changed := map[string]any{"action": action, "shiftId": shift, "cartId": "other-cart", "idempotencyKey": key}
			if action == "update" {
				changed["lines"] = input["lines"]
			}
			if action == "checkout" {
				changed["tender"] = price
			}
			if conflict := request(a, http.MethodPost, "/api/stations/pos/cart", changed, staff...); conflict.Code != http.StatusConflict || strings.Contains(conflict.Body.String(), `"outcome":"not_applied"`) {
				t.Fatalf("changed %s payload should get unmarked key conflict: %d %s", action, conflict.Code, conflict.Body.String())
			}
		})
	}
}

func TestPOSSameStoreOperationsOnAnotherClaimReturnGenericTerminalConflict(t *testing.T) {
	for _, action := range []string{"claim", "release", "update", "discard", "checkout"} {
		t.Run(action, func(t *testing.T) {
			a := appForTest(t)
			space := token()
			org := "org_test_" + space
			staff := loginSite(t, a, "front@suite.test", space, "pos")
			admin := loginSite(t, a, "admin@suite.test", space, "pos")
			var shift, item string
			var price, stock int64
			if err := a.db.QueryRow(`SELECT id FROM pos_shifts WHERE organization_id=? AND status='open'`, org).Scan(&shift); err != nil {
				t.Fatal(err)
			}
			if err := a.db.QueryRow(`SELECT id,price,stock_quantity FROM pos_catalog_items WHERE organization_id=? AND is_active=1 LIMIT 1`, org).Scan(&item, &price, &stock); err != nil {
				t.Fatal(err)
			}
			cartID := seedClaimedCartForRetryTest(t, a, staff, org, shift, item, 1)
			var ownerID string
			for _, c := range staff {
				if c.Name == cookieName+"_pos" {
					if err := a.db.QueryRow(`SELECT user_id FROM station_sessions WHERE token_hash=?`, digest(c.Value)).Scan(&ownerID); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
			if ownerID == "" {
				t.Fatal("could not resolve cart owner")
			}
			key := "other-claim-" + action + "-01"
			input := map[string]any{"action": action, "shiftId": shift, "cartId": cartID, "idempotencyKey": key}
			if action == "update" {
				input["lines"] = []any{map[string]any{"itemId": item, "quantity": 1, "discountAmount": 0}}
			}
			if action == "checkout" {
				input["tender"] = price
			}
			first := request(a, http.MethodPost, "/api/stations/pos/cart", input, admin...)
			if first.Code != http.StatusConflict || !strings.Contains(first.Body.String(), `"outcome":"not_applied"`) {
				t.Fatalf("authorized same-store %s conflict should be terminal: %d %s", action, first.Code, first.Body.String())
			}
			if strings.Contains(first.Body.String(), ownerID) || strings.Contains(first.Body.String(), "locked_by") || strings.Contains(first.Body.String(), "claimant") {
				t.Fatalf("generic conflict disclosed cart ownership: %s", first.Body.String())
			}
			if replay := request(a, http.MethodPost, "/api/stations/pos/cart", input, admin...); replay.Code != first.Code || replay.Body.String() != first.Body.String() {
				t.Fatalf("other-claim %s rejection did not replay: %d %s", action, replay.Code, replay.Body.String())
			}
			changed := map[string]any{"action": action, "shiftId": shift, "cartId": "different-cart", "idempotencyKey": key}
			if action == "update" {
				changed["lines"] = input["lines"]
			}
			if action == "checkout" {
				changed["tender"] = price
			}
			if conflict := request(a, http.MethodPost, "/api/stations/pos/cart", changed, admin...); conflict.Code != http.StatusConflict || strings.Contains(conflict.Body.String(), `"outcome":"not_applied"`) {
				t.Fatalf("changed other-claim payload should conflict unmarked: %d %s", conflict.Code, conflict.Body.String())
			}
			var status, locked string
			var sales, after int64
			if err := a.db.QueryRow(`SELECT status,COALESCE(locked_by,'') FROM pos_held_carts WHERE organization_id=? AND id=?`, org, cartID).Scan(&status, &locked); err != nil {
				t.Fatal(err)
			}
			if err := a.db.QueryRow(`SELECT count(*) FROM pos_sales WHERE organization_id=? AND idempotency_key=?`, org, key).Scan(&sales); err != nil {
				t.Fatal(err)
			}
			if err := a.db.QueryRow(`SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?`, org, item).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if status != "retrieved" || locked != ownerID || sales != 0 || after != stock {
				t.Fatalf("%s conflict changed another claimant's cart: status=%s owner=%t sales=%d stock=%d/%d", action, status, locked == ownerID, sales, after, stock)
			}
		})
	}
}

func TestPOSCartUnauthorizedShiftDenialStaysUnmarked(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	owner := loginSite(t, a, "front@suite.test", space, "pos")
	unauthorized := loginSite(t, a, "office@suite.test", space, "pos")
	var shift, item string
	if err := a.db.QueryRow(`SELECT id FROM pos_shifts WHERE organization_id=? AND status='open'`, org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id FROM pos_catalog_items WHERE organization_id=? AND is_active=1 LIMIT 1`, org).Scan(&item); err != nil {
		t.Fatal(err)
	}
	key := "cart-unauthorized-shift-01"
	input := map[string]any{"action": "hold", "shiftId": shift, "cartName": "Must not reserve", "lines": []any{map[string]any{"itemId": item, "quantity": 1}}, "idempotencyKey": key}
	w := request(a, http.MethodPost, "/api/stations/pos/cart", input, unauthorized...)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), `"outcome":"not_applied"`) || strings.Contains(w.Body.String(), "another cashier") {
		t.Fatalf("unauthorized shift denial must remain an unmarked 403: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint='/station/pos/cart/hold'`, org, key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("403 denial reserved idempotency key: %d", count)
	}
	storeKey := "cart-unauthorized-store-01"
	if _, err := a.db.Exec(`INSERT INTO pos_held_carts(id,organization_id,store_id,cart_name,items_json,status) VALUES(?,?,?,'Private other-store cart','[]','held')`, "cart_other_store_test", org, "store_out_of_scope"); err != nil {
		t.Fatal(err)
	}
	storeAttempt := map[string]any{"action": "claim", "shiftId": shift, "cartId": "cart_other_store_test", "idempotencyKey": storeKey}
	storeDenied := request(a, http.MethodPost, "/api/stations/pos/cart", storeAttempt, owner...)
	if storeDenied.Code != http.StatusForbidden || strings.Contains(storeDenied.Body.String(), `"outcome":"not_applied"`) || strings.Contains(storeDenied.Body.String(), "store") {
		t.Fatalf("cross-store denial must remain generic and unmarked: %d %s", storeDenied.Code, storeDenied.Body.String())
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint='/station/pos/cart/claim'`, org, storeKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("cross-store denial reserved idempotency key: %d", count)
	}
	// The actual shift owner can use the same key; denial did not squat it.
	if accepted := request(a, http.MethodPost, "/api/stations/pos/cart", input, owner...); accepted.Code != http.StatusOK {
		t.Fatalf("owner blocked by unauthorized request's key: %d %s", accepted.Code, accepted.Body.String())
	}
}

func TestPOSShiftCloseBlocksClaimedButNotSharedHeldCarts(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		name := "shared-held"
		if claimed {
			name = "cashier-claimed"
		}
		t.Run(name, func(t *testing.T) {
			a := appForTest(t)
			space := token()
			org := "org_test_" + space
			staff := loginSite(t, a, "front@suite.test", space, "pos")
			closer := staff
			if claimed {
				closer = loginSite(t, a, "admin@suite.test", space, "pos")
			}
			var shift, item string
			var opening int64
			if err := a.db.QueryRow(`SELECT id,opening_cash FROM pos_shifts WHERE organization_id=? AND status='open'`, org).Scan(&shift, &opening); err != nil {
				t.Fatal(err)
			}
			if err := a.db.QueryRow(`SELECT id FROM pos_catalog_items WHERE organization_id=? AND is_active=1 LIMIT 1`, org).Scan(&item); err != nil {
				t.Fatal(err)
			}
			cartID := seedClaimedCartForRetryTest(t, a, staff, org, shift, item, 1)
			if !claimed {
				if _, err := a.db.Exec(`UPDATE pos_held_carts SET status='held',locked_by=NULL WHERE organization_id=? AND id=?`, org, cartID); err != nil {
					t.Fatal(err)
				}
			}
			closeInput := map[string]any{"action": "close", "shiftId": shift, "closingCash": opening, "idempotencyKey": "close-cart-check-001"}
			w := request(a, http.MethodPost, "/api/stations/pos/business", closeInput, closer...)
			if claimed {
				if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"outcome":"not_applied"`) {
					t.Fatalf("close with own retrieved cart should reject durably: %d %s", w.Code, w.Body.String())
				}
				if replay := request(a, http.MethodPost, "/api/stations/pos/business", closeInput, closer...); replay.Code != w.Code || replay.Body.String() != w.Body.String() {
					t.Fatalf("claimed-cart close rejection did not replay: %d %s", replay.Code, replay.Body.String())
				}
				changed := map[string]any{"action": "close", "shiftId": shift, "closingCash": opening + 1, "notes": "Changed count", "idempotencyKey": closeInput["idempotencyKey"]}
				if conflict := request(a, http.MethodPost, "/api/stations/pos/business", changed, closer...); conflict.Code != http.StatusConflict || strings.Contains(conflict.Body.String(), `"outcome":"not_applied"`) {
					t.Fatalf("changed close payload should conflict without marker: %d %s", conflict.Code, conflict.Body.String())
				}
				var status string
				if err := a.db.QueryRow(`SELECT status FROM pos_shifts WHERE organization_id=? AND id=?`, org, shift).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status != "open" {
					t.Fatalf("shift closed with claimed cart: %q", status)
				}
				release := map[string]any{"action": "release", "shiftId": shift, "cartId": cartID, "idempotencyKey": "release-before-close-001"}
				if released := request(a, http.MethodPost, "/api/stations/pos/cart", release, staff...); released.Code != http.StatusOK {
					t.Fatalf("claimant release recovery: %d %s", released.Code, released.Body.String())
				}
				correctedClose := map[string]any{"action": "close", "shiftId": shift, "closingCash": opening, "idempotencyKey": "close-after-cart-release-01"}
				if closed := request(a, http.MethodPost, "/api/stations/pos/business", correctedClose, closer...); closed.Code != http.StatusOK {
					t.Fatalf("close after releasing claim: %d %s", closed.Code, closed.Body.String())
				}
			} else if w.Code != http.StatusOK {
				t.Fatalf("shared held cart should not block close: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPOSShiftCloseRacingClaimCannotLeaveClosedShiftWithRetrievedCart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cart-close-claim-race.sqlite")
	dbA, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbA.Close() })
	a := &App{db: dbA}
	space := token()
	org := "org_test_" + space
	staff := loginSite(t, a, "front@suite.test", space, "pos")
	var shift, item string
	var opening int64
	if err := a.db.QueryRow(`SELECT id,opening_cash FROM pos_shifts WHERE organization_id=? AND status='open'`, org).Scan(&shift, &opening); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id FROM pos_catalog_items WHERE organization_id=? AND is_active=1 LIMIT 1`, org).Scan(&item); err != nil {
		t.Fatal(err)
	}
	cartID := seedClaimedCartForRetryTest(t, a, staff, org, shift, item, 1)
	if _, err := a.db.Exec(`UPDATE pos_held_carts SET status='held',locked_by=NULL WHERE organization_id=? AND id=?`, org, cartID); err != nil {
		t.Fatal(err)
	}
	dbB, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbB.Close() })
	b := &App{db: dbB}
	claim := map[string]any{"action": "claim", "shiftId": shift, "cartId": cartID, "idempotencyKey": "claim-close-race-01"}
	closeInput := map[string]any{"action": "close", "shiftId": shift, "closingCash": opening, "idempotencyKey": "close-claim-race-01"}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, call := range []struct {
		app   *App
		path  string
		input map[string]any
	}{{a, "/api/stations/pos/cart", claim}, {b, "/api/stations/pos/business", closeInput}} {
		wg.Add(1)
		go func(app *App, path string, input map[string]any) {
			defer wg.Done()
			<-start
			request(app, http.MethodPost, path, input, staff...)
		}(call.app, call.path, call.input)
	}
	close(start)
	wg.Wait()
	// Retrying both exact payloads resolves any transient SQLite busy response.
	request(a, http.MethodPost, "/api/stations/pos/business", closeInput, staff...)
	request(b, http.MethodPost, "/api/stations/pos/cart", claim, staff...)
	var shiftStatus, cartStatus string
	if err := a.db.QueryRow(`SELECT status FROM pos_shifts WHERE organization_id=? AND id=?`, org, shift).Scan(&shiftStatus); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT status FROM pos_held_carts WHERE organization_id=? AND id=?`, org, cartID).Scan(&cartStatus); err != nil {
		t.Fatal(err)
	}
	if shiftStatus == "closed" && cartStatus == "retrieved" {
		t.Fatalf("close/claim race left closed shift with a claimed cart")
	}
	if (shiftStatus != "closed" && shiftStatus != "open") || (cartStatus != "held" && cartStatus != "retrieved") {
		t.Fatalf("unexpected race state: shift=%s cart=%s", shiftStatus, cartStatus)
	}
	if shiftStatus == "open" && cartStatus != "retrieved" {
		t.Fatalf("open shift should imply successful claim in race state: shift=%s cart=%s", shiftStatus, cartStatus)
	}
	if shiftStatus == "closed" && cartStatus != "held" {
		t.Fatalf("closed shift should retain shared held cart: shift=%s cart=%s", shiftStatus, cartStatus)
	}
}
