package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
)

type posHeldCartListLine struct {
	ItemID         string           `json:"itemId"`
	Quantity       int64            `json:"quantity"`
	DiscountAmount int64            `json:"discountAmount"`
	Available      bool             `json:"available"`
	Product        *posHeldCartItem `json:"product"`
}

type posHeldCartItem struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Barcode       string `json:"barcode"`
	Price         int64  `json:"price"`
	StockQuantity int64  `json:"stockQuantity"`
	IsActive      bool   `json:"isActive"`
}

type posHeldCartListEntry struct {
	CartID   string                `json:"cartId"`
	CartName string                `json:"cartName"`
	HeldAt   string                `json:"heldAt"`
	Status   string                `json:"status"`
	Lines    []posHeldCartListLine `json:"lines"`
}

// posHeldCarts lists the held carts available to the caller's authorized open
// shift. It deliberately returns a small cashier-facing projection rather than
// exposing the back-office heldCarts table.
func (a *App) posHeldCarts(w http.ResponseWriter, r *http.Request, id *Identity, g *Grant) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Use GET")
		return
	}
	if g.Role == "readonly" {
		fail(w, http.StatusForbidden, "A write-enabled role is required")
		return
	}
	shiftID := r.URL.Query().Get("shiftId")
	if shiftID == "" || len(shiftID) > 256 {
		fail(w, http.StatusBadRequest, "Select an open shift")
		return
	}
	var storeID, cashierID, status string
	err := a.db.QueryRow(`SELECT store_id,cashier_id,status FROM pos_shifts WHERE organization_id=? AND id=?`, id.OrganizationID, shiftID).Scan(&storeID, &cashierID, &status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "open") {
		fail(w, http.StatusNotFound, "Open shift not found")
		return
	}
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Cannot read open shift")
		return
	}
	if cashierID != id.UserID && g.Role != "admin" {
		fail(w, http.StatusForbidden, "Cannot view carts for another cashier's shift")
		return
	}
	rows, err := a.db.Query(`SELECT id,cart_name,held_at,status,items_json FROM pos_held_carts WHERE organization_id=? AND store_id=? AND (status='held' OR (status='retrieved' AND locked_by=?)) ORDER BY CASE WHEN status='retrieved' THEN 0 ELSE 1 END,held_at,id LIMIT 100`, id.OrganizationID, storeID, id.UserID)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Cannot read held carts")
		return
	}
	type storedCart struct {
		id, name, heldAt, status, encoded string
	}
	storedCarts := make([]storedCart, 0)
	for rows.Next() {
		var cart storedCart
		if err = rows.Scan(&cart.id, &cart.name, &cart.heldAt, &cart.status, &cart.encoded); err != nil {
			rows.Close()
			fail(w, http.StatusServiceUnavailable, "Cannot read held cart")
			return
		}
		storedCarts = append(storedCarts, cart)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		fail(w, http.StatusServiceUnavailable, "Cannot read held carts")
		return
	}
	if err = rows.Close(); err != nil {
		fail(w, http.StatusServiceUnavailable, "Cannot read held carts")
		return
	}
	carts := make([]posHeldCartListEntry, 0, len(storedCarts))
	products := make(map[string]*posHeldCartItem)
	unavailable := make(map[string]bool)
	for _, storedCart := range storedCarts {
		var stored []posCartLine
		if json.Unmarshal([]byte(storedCart.encoded), &stored) != nil || len(stored) == 0 || len(stored) > 100 {
			fail(w, http.StatusServiceUnavailable, "Held cart data is invalid")
			return
		}
		entry := posHeldCartListEntry{CartID: storedCart.id, CartName: storedCart.name, HeldAt: storedCart.heldAt, Status: storedCart.status, Lines: make([]posHeldCartListLine, 0, len(stored))}
		for _, line := range stored {
			if line.ItemID == "" || line.Quantity < 1 || line.DiscountAmount < 0 {
				fail(w, http.StatusServiceUnavailable, "Held cart data is invalid")
				return
			}
			listed := posHeldCartListLine{ItemID: line.ItemID, Quantity: line.Quantity, DiscountAmount: line.DiscountAmount}
			if product, found := products[line.ItemID]; found {
				listed.Product = product
				listed.Available = product.IsActive
			} else if !unavailable[line.ItemID] {
				var item posHeldCartItem
				err = a.db.QueryRow(`SELECT id,name,barcode,price,stock_quantity,is_active FROM pos_catalog_items WHERE organization_id=? AND id=?`, id.OrganizationID, line.ItemID).Scan(&item.ID, &item.Name, &item.Barcode, &item.Price, &item.StockQuantity, &item.IsActive)
				if errors.Is(err, sql.ErrNoRows) {
					unavailable[line.ItemID] = true
				} else if err != nil {
					fail(w, http.StatusServiceUnavailable, "Cannot read held cart item")
					return
				} else {
					products[line.ItemID] = &item
					listed.Product = &item
					listed.Available = item.IsActive
				}
			}
			entry.Lines = append(entry.Lines, listed)
		}
		carts = append(carts, entry)
	}
	ok(w, carts)
}
