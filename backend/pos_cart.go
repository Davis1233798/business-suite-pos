package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type posCartLine struct {
	ItemID         string `json:"itemId"`
	Quantity       int64  `json:"quantity"`
	DiscountAmount int64  `json:"discountAmount,omitempty"`
}
type posCartRequest struct {
	Action   string        `json:"action"`
	ShiftID  string        `json:"shiftId"`
	CartID   string        `json:"cartId"`
	CartName string        `json:"cartName"`
	Lines    []posCartLine `json:"lines"`
	Tender   int64         `json:"tender"`
	Key      string        `json:"idempotencyKey"`
}

func (a *App) posCart(w http.ResponseWriter, r *http.Request, id *Identity, g *Grant) {
	if r.Method != http.MethodPost {
		fail(w, 405, "Use POST")
		return
	}
	if g.Role == "readonly" {
		fail(w, 403, "A write-enabled role is required")
		return
	}
	var in posCartRequest
	if body(r, &in) != nil || len(in.Key) < 8 || len(in.Key) > 128 || (in.Action != "hold" && in.Action != "claim" && in.Action != "release" && in.Action != "update" && in.Action != "discard" && in.Action != "checkout") {
		fail(w, 400, "Invalid cart request")
		return
	}
	in.CartName = strings.TrimSpace(in.CartName)
	tx, e := a.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 503, "POS unavailable")
		return
	}
	defer tx.Rollback()
	raw, _ := json.Marshal(in)
	hash := digest(id.UserID + "\x00" + string(raw))
	endpoint := "/station/pos/cart/" + in.Action
	var oldHash, oldBody string
	var oldStatus int
	e = tx.QueryRow("SELECT request_hash,response_status,response_body FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint=?", id.OrganizationID, in.Key, endpoint).Scan(&oldHash, &oldStatus, &oldBody)
	if e == nil {
		if hash != oldHash {
			fail(w, 409, "Retry key already used")
			return
		}
		var result map[string]any
		if json.Unmarshal([]byte(oldBody), &result) != nil {
			fail(w, 500, "Invalid retry result")
			return
		}
		if oldStatus != http.StatusOK {
			writeJSON(w, oldStatus, result)
			return
		}
		ok(w, result)
		return
	}
	if !errors.Is(e, sql.ErrNoRows) {
		fail(w, 503, "Cannot read retry state")
		return
	}
	if in.ShiftID == "" || len(in.ShiftID) > 256 || len(in.CartID) > 256 || in.Tender < 0 || in.Tender > 1000000000 {
		terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusBadRequest, "Invalid cart request")
		return
	}
	if in.Action == "hold" && (in.CartName == "" || len(in.CartName) > 100 || len(in.Lines) == 0 || len(in.Lines) > 100) {
		terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusBadRequest, "Enter a cart name and items")
		return
	}
	if (in.Action == "claim" || in.Action == "release" || in.Action == "update" || in.Action == "discard") && in.CartID == "" {
		terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusBadRequest, "Select a cart")
		return
	}
	if in.Action == "update" && (len(in.Lines) == 0 || len(in.Lines) > 100) {
		terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusBadRequest, "Cart must contain 1 to 100 items")
		return
	}
	if in.Action == "checkout" && in.CartID == "" && (len(in.Lines) == 0 || len(in.Lines) > 100) {
		terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusBadRequest, "Add items to the sale")
		return
	}
	var store, register, cashier, status string
	e = tx.QueryRow("SELECT store_id,register_id,cashier_id,status FROM pos_shifts WHERE organization_id=? AND id=?", id.OrganizationID, in.ShiftID).Scan(&store, &register, &cashier, &status)
	if errors.Is(e, sql.ErrNoRows) {
		terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusNotFound, "Shift not found")
		return
	}
	if e != nil {
		fail(w, 503, "Cannot read shift")
		return
	}
	closedShiftRecovery := status == "closed" && (in.Action == "release" || in.Action == "discard")
	if status != "open" && !closedShiftRecovery {
		terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Shift is not open")
		return
	}
	if cashier != id.UserID && g.Role != "admin" {
		fail(w, http.StatusForbidden, "Cannot access this shift")
		return
	}
	entity := ""
	result := map[string]any{}
	lines := in.Lines
	if in.Action == "claim" || in.Action == "release" || in.Action == "update" || in.Action == "discard" || (in.Action == "checkout" && in.CartID != "") {
		var cartStore, cartStatus, locked, stored string
		e = tx.QueryRow("SELECT store_id,status,COALESCE(locked_by,''),items_json FROM pos_held_carts WHERE organization_id=? AND id=?", id.OrganizationID, in.CartID).Scan(&cartStore, &cartStatus, &locked, &stored)
		if errors.Is(e, sql.ErrNoRows) {
			terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusNotFound, "Cart not found")
			return
		}
		if e != nil {
			fail(w, 503, "Cannot read cart")
			return
		}
		if cartStore != store {
			fail(w, http.StatusForbidden, "Cannot access this cart")
			return
		}
		entity = in.CartID
		if in.Action == "claim" {
			if cartStatus != "held" {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
				return
			}
			var claimed sql.Result
			claimed, e = tx.Exec("UPDATE pos_held_carts SET status='retrieved',locked_by=? WHERE organization_id=? AND id=? AND status='held'", id.UserID, id.OrganizationID, in.CartID)
			if e == nil {
				var n int64
				n, e = claimed.RowsAffected()
				if e == nil && n != 1 {
					terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
					return
				}
			}
			result = map[string]any{"cartId": entity, "status": "retrieved"}
		}
		if in.Action == "release" {
			if locked != id.UserID {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
				return
			}
			if cartStatus != "retrieved" {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
				return
			}
			var released sql.Result
			released, e = tx.Exec("UPDATE pos_held_carts SET status='held',locked_by=NULL WHERE organization_id=? AND id=? AND status='retrieved' AND locked_by=?", id.OrganizationID, in.CartID, id.UserID)
			if e == nil {
				var n int64
				n, e = released.RowsAffected()
				if e == nil && n != 1 {
					terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
					return
				}
			}
			result = map[string]any{"cartId": entity, "status": "held"}
		}
		if in.Action == "update" {
			if locked != id.UserID {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
				return
			}
			if cartStatus != "retrieved" {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
				return
			}
		}
		if in.Action == "discard" {
			if locked != id.UserID {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
				return
			}
			if cartStatus != "retrieved" {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
				return
			}
			updated, updateErr := tx.Exec("UPDATE pos_held_carts SET status='cancelled',locked_by=NULL WHERE organization_id=? AND id=? AND status='retrieved' AND locked_by=?", id.OrganizationID, in.CartID, id.UserID)
			e = updateErr
			if e == nil {
				var count int64
				count, e = updated.RowsAffected()
				if e == nil && count != 1 {
					terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
					return
				}
			}
			entity = in.CartID
			result = map[string]any{"cartId": entity, "status": "cancelled"}
		}
		if in.Action == "checkout" {
			if locked != id.UserID {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
				return
			}
			if cartStatus != "retrieved" {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
				return
			}
			if json.Unmarshal([]byte(stored), &lines) != nil {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Invalid stored cart")
				return
			}
		}
	}
	if in.Action == "hold" || in.Action == "update" || in.Action == "checkout" {
		if len(lines) == 0 || len(lines) > 100 {
			terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusBadRequest, "Cart must contain 1 to 100 items")
			return
		}
		seen := map[string]bool{}
		type resolved struct {
			posCartLine
			barcode, name string
			price, gross  int64
		}
		resolvedLines := []resolved{}
		var subtotal, discountTotal int64
		for _, line := range lines {
			if line.ItemID == "" || len(line.ItemID) > 256 || line.Quantity < 1 || line.Quantity > 1000 || seen[line.ItemID] {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusBadRequest, "Invalid or duplicate cart item")
				return
			}
			seen[line.ItemID] = true
			var barcode, name string
			var price, stock int64
			e = tx.QueryRow("SELECT barcode,name,price,stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=? AND is_active=1", id.OrganizationID, line.ItemID).Scan(&barcode, &name, &price, &stock)
			if errors.Is(e, sql.ErrNoRows) {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusNotFound, "Active product not found")
				return
			}
			if e != nil {
				fail(w, 503, "Cannot read product")
				return
			}
			if price < 0 || price > 1000000 || subtotal > 1000000000-price*line.Quantity {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusBadRequest, "Invalid cart total")
				return
			}
			gross := price * line.Quantity
			if line.DiscountAmount < 0 || line.DiscountAmount > gross {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusBadRequest, "Invalid line discount")
				return
			}
			if in.Action == "checkout" && stock < line.Quantity {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Insufficient stock")
				return
			}
			subtotal += gross
			discountTotal += line.DiscountAmount
			resolvedLines = append(resolvedLines, resolved{line, barcode, name, price, gross})
		}
		total := subtotal - discountTotal
		if in.Action == "hold" {
			encoded, _ := json.Marshal(lines)
			entity = "cart_" + token()
			_, e = tx.Exec("INSERT INTO pos_held_carts(id,organization_id,store_id,cart_name,items_json,status) VALUES(?,?,?,?,?,'held')", entity, id.OrganizationID, store, in.CartName, string(encoded))
			result = map[string]any{"cartId": entity, "status": "held"}
		} else if in.Action == "update" {
			encoded, _ := json.Marshal(lines)
			updated, updateErr := tx.Exec("UPDATE pos_held_carts SET items_json=? WHERE organization_id=? AND id=? AND status='retrieved' AND locked_by=?", string(encoded), id.OrganizationID, in.CartID, id.UserID)
			e = updateErr
			if e == nil {
				var count int64
				count, e = updated.RowsAffected()
				if e == nil && count != 1 {
					terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Cart is unavailable for this action")
					return
				}
			}
			entity = in.CartID
			result = map[string]any{"cartId": entity, "status": "retrieved"}
		} else {
			if in.Tender < total {
				terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusBadRequest, "Cash tender is less than the total")
				return
			}
			entity = "sale_" + token()
			receipt := "POS-" + strings.ToUpper(token()[:12])
			if _, e = tx.Exec("SAVEPOINT cart_business"); e != nil {
				fail(w, 503, "Could not protect cart checkout")
				return
			}
			_, e = tx.Exec("INSERT INTO pos_sales(id,organization_id,receipt_number,store_id,register_id,shift_id,cashier_id,subtotal,discount_amount,total_amount,tender_amount,change_amount,payment_method,status,idempotency_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'cash','completed',?)", entity, id.OrganizationID, receipt, store, register, in.ShiftID, id.UserID, subtotal, discountTotal, total, in.Tender, in.Tender-total, in.Key)
			for _, line := range resolvedLines {
				if e != nil {
					break
				}
				var updated sql.Result
				updated, e = tx.Exec("UPDATE pos_catalog_items SET stock_quantity=stock_quantity-? WHERE organization_id=? AND id=? AND stock_quantity>=?", line.Quantity, id.OrganizationID, line.ItemID, line.Quantity)
				if e == nil {
					var n int64
					n, e = updated.RowsAffected()
					if e == nil && n != 1 {
						terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Stock changed; refresh and retry", true)
						return
					}
				}
				if e == nil {
					_, e = tx.Exec("INSERT INTO pos_sale_lines(id,organization_id,sale_id,item_id,barcode,name,unit_price,quantity,discount_amount,line_total) VALUES(?,?,?,?,?,?,?,?,?,?)", "sale_line_"+token(), id.OrganizationID, entity, line.ItemID, line.barcode, line.name, line.price, line.Quantity, line.DiscountAmount, line.gross-line.DiscountAmount)
				}
			}
			if e == nil && in.CartID != "" {
				var completed sql.Result
				completed, e = tx.Exec("UPDATE pos_held_carts SET status='completed' WHERE organization_id=? AND id=? AND status='retrieved' AND locked_by=?", id.OrganizationID, in.CartID, id.UserID)
				if e == nil {
					var n int64
					n, e = completed.RowsAffected()
					if e == nil && n != 1 {
						terminalPOSCartRejection(w, tx, id.OrganizationID, in.Key, endpoint, hash, http.StatusConflict, "Claimed cart changed; refresh and retry", true)
						return
					}
				}
			}
			if e == nil {
				outboxLines := make([]OutboxSaleLine, 0, len(resolvedLines))
				for _, line := range resolvedLines {
					outboxLines = append(outboxLines, OutboxSaleLine{
						ItemID:         line.ItemID,
						Barcode:        line.barcode,
						Name:           line.name,
						UnitPrice:      line.price,
						Quantity:       line.Quantity,
						DiscountAmount: line.DiscountAmount,
						LineTotal:      line.gross - line.DiscountAmount,
					})
				}
				evtID := "evt_" + token()
				outboxPayload := OutboxSaleCompletedPayload{
					EventID:        evtID,
					SchemaVersion:  1,
					OrganizationID: id.OrganizationID,
					SourceID:       entity,
					ReceiptNumber:  receipt,
					TotalAmount:    total,
					Subtotal:       subtotal,
					DiscountAmount: discountTotal,
					PaymentMethod:  "cash",
					CustomerID:     nil,
					Lines:          outboxLines,
					OccurredAt:     time.Now().UTC().Format(time.RFC3339),
				}
				e = emitOutboxEvent(tx, id.OrganizationID, "pos.sale.completed", evtID, outboxPayload)
			}
			result = map[string]any{"saleId": entity, "receiptNumber": receipt, "subtotal": subtotal, "discountAmount": discountTotal, "totalAmount": total, "changeAmount": in.Tender - total}
		}
	}
	if e != nil {
		fail(w, 503, "Cannot save cart operation")
		return
	}
	encoded, _ := json.Marshal(result)
	_, e = tx.Exec("INSERT INTO idempotency_keys(id,organization_id,key,endpoint,request_hash,response_status,response_body) VALUES(?,?,?,?,?,200,?)", "idem_"+token(), id.OrganizationID, in.Key, endpoint, hash, string(encoded))
	if e == nil {
		_, e = tx.Exec("INSERT INTO audit_events(id,organization_id,actor_id,actor_role,action,entity_type,entity_id,after_value) VALUES(?,?,?,?,?,'pos_cart',?,?)", "audit_"+token(), id.OrganizationID, id.UserID, g.Role, "pos.cart."+in.Action, entity, string(encoded))
	}
	if e != nil {
		fail(w, 503, "Cannot record cart operation")
		return
	}
	if tx.Commit() != nil {
		fail(w, 503, "Cannot commit cart operation")
		return
	}
	ok(w, result)
}

func terminalPOSCartRejection(w http.ResponseWriter, tx *sql.Tx, organizationID, key, endpoint, fingerprint string, status int, message string, rollbackBusiness ...bool) {
	if len(rollbackBusiness) > 0 && rollbackBusiness[0] {
		if _, err := tx.Exec("ROLLBACK TO SAVEPOINT cart_business"); err != nil {
			fail(w, 503, "Could not roll back rejected cart operation")
			return
		}
		if _, err := tx.Exec("RELEASE SAVEPOINT cart_business"); err != nil {
			fail(w, 503, "Could not finish rejected cart operation")
			return
		}
	}
	body, _ := json.Marshal(map[string]any{"success": false, "error": map[string]string{"message": message, "outcome": "not_applied"}})
	if _, err := tx.Exec("INSERT INTO idempotency_keys(id,organization_id,key,endpoint,request_hash,response_status,response_body) VALUES(?,?,?,?,?,?,?)", "idem_"+token(), organizationID, key, endpoint, fingerprint, status, string(body)); err != nil {
		fail(w, 503, "Unable to save terminal cart retry state")
		return
	}
	if err := tx.Commit(); err != nil {
		fail(w, 503, "Could not save terminal cart rejection")
		return
	}
	writeJSON(w, status, map[string]any{"success": false, "error": map[string]string{"message": message, "outcome": "not_applied"}})
}
