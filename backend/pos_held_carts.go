package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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

type posHeldCartCursor struct {
	Scope  string `json:"s"`
	Tier   int    `json:"t"`
	HeldAt string `json:"a"`
	CartID string `json:"i"`
}

type posHeldCartListPage struct {
	Items      []posHeldCartListEntry `json:"items"`
	HasMore    bool                   `json:"hasMore"`
	NextCursor string                 `json:"nextCursor"`
}

const posHeldCartPageSize = 50

func encodePosHeldCartCursor(c posHeldCartCursor) string {
	encoded, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodePosHeldCartCursor(encoded string) (posHeldCartCursor, error) {
	var cursor posHeldCartCursor
	if len(encoded) == 0 || len(encoded) > 2048 {
		return cursor, errors.New("invalid cursor length")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || json.Unmarshal(decoded, &cursor) != nil || (cursor.Tier != 0 && cursor.Tier != 1) || cursor.HeldAt == "" || cursor.CartID == "" {
		return posHeldCartCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func posHeldCartCursorScope(organizationID, storeID, shiftID, userID string) string {
	scope := sha256.Sum256([]byte(organizationID + "\x00" + storeID + "\x00" + shiftID + "\x00" + userID))
	return hex.EncodeToString(scope[:])
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
	limit := posHeldCartPageSize
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		parsed, parseErr := strconv.Atoi(rawLimit)
		if parseErr != nil || parsed < 1 || parsed > posHeldCartPageSize {
			fail(w, http.StatusBadRequest, "Page limit must be between 1 and 50")
			return
		}
		limit = parsed
	}
	cartID := r.URL.Query().Get("cartId")
	if len(cartID) > 256 {
		fail(w, http.StatusBadRequest, "Invalid cart id")
		return
	}
	cursorToken := r.URL.Query().Get("cursor")
	if cartID != "" && cursorToken != "" {
		fail(w, http.StatusBadRequest, "Targeted cart reads cannot use a cursor")
		return
	}
	query := `SELECT id,cart_name,held_at,status,items_json FROM pos_held_carts WHERE organization_id=? AND store_id=? AND (status='held' OR (status='retrieved' AND locked_by=?))`
	args := []any{id.OrganizationID, storeID, id.UserID}
	var cursor posHeldCartCursor
	if cartID != "" {
		query += ` AND id=?`
		args = append(args, cartID)
	} else if cursorToken != "" {
		cursor, err = decodePosHeldCartCursor(cursorToken)
		if err != nil || cursor.Scope != posHeldCartCursorScope(id.OrganizationID, storeID, shiftID, id.UserID) {
			fail(w, http.StatusBadRequest, "Invalid held-cart cursor")
			return
		}
		query += ` AND (CASE WHEN status='retrieved' THEN 0 ELSE 1 END > ? OR (CASE WHEN status='retrieved' THEN 0 ELSE 1 END = ? AND (held_at>? OR (held_at=? AND id>?))))`
		args = append(args, cursor.Tier, cursor.Tier, cursor.HeldAt, cursor.HeldAt, cursor.CartID)
	}
	query += ` ORDER BY CASE WHEN status='retrieved' THEN 0 ELSE 1 END,held_at,id LIMIT ?`
	if cartID == "" {
		args = append(args, limit+1)
	} else {
		args = append(args, 1)
	}
	rows, err := a.db.Query(query, args...)
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
	hasMore := cartID == "" && len(storedCarts) > limit
	if hasMore {
		storedCarts = storedCarts[:limit]
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
	page := posHeldCartListPage{Items: carts, HasMore: hasMore}
	if hasMore && len(storedCarts) > 0 {
		last := storedCarts[len(storedCarts)-1]
		tier := 1
		if last.status == "retrieved" {
			tier = 0
		}
		page.NextCursor = encodePosHeldCartCursor(posHeldCartCursor{Scope: posHeldCartCursorScope(id.OrganizationID, storeID, shiftID, id.UserID), Tier: tier, HeldAt: last.heldAt, CartID: last.id})
	}
	ok(w, page)
}
