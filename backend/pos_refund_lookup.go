package main

import (
	"net/http"
	"strings"
)

type posRefundLookupLine struct {
	SaleLineID        string `json:"saleLineId"`
	ItemID            string `json:"itemId"`
	Name              string `json:"name"`
	Barcode           string `json:"barcode"`
	Quantity          int64  `json:"quantity"`
	RefundedQuantity  int64  `json:"refundedQuantity"`
	RemainingQuantity int64  `json:"remainingQuantity"`
	LineTotal         int64  `json:"lineTotal"`
}

type posRefundLookup struct {
	SaleID        string                `json:"saleId"`
	ReceiptNumber string                `json:"receiptNumber"`
	SaleStatus    string                `json:"saleStatus"`
	PaymentMethod string                `json:"paymentMethod"`
	ShiftStatus   string                `json:"shiftStatus"`
	Eligible      bool                  `json:"eligible"`
	Lines         []posRefundLookupLine `json:"lines"`
}

// posRefundLookup returns a bounded, organization-scoped sale projection for
// authorized back-office administrators. POST remains authoritative.
func (a *App) posRefundLookup(w http.ResponseWriter, r *http.Request, id *Identity, grant *Grant) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Use GET")
		return
	}
	if grant.Role != "admin" || grant.Scope != "back" {
		fail(w, http.StatusForbidden, "Refund lookup requires a back-office administrator")
		return
	}
	receiptNumber := strings.TrimSpace(r.URL.Query().Get("receiptNumber"))
	if receiptNumber == "" || len(receiptNumber) > 128 {
		fail(w, http.StatusBadRequest, "Enter a valid receipt number")
		return
	}
	rows, err := a.db.Query(`SELECT s.id,s.receipt_number,s.status,s.payment_method,sh.status
		FROM pos_sales s JOIN pos_shifts sh ON sh.id=s.shift_id AND sh.organization_id=s.organization_id
		WHERE s.organization_id=? AND s.receipt_number=? LIMIT 2`, id.OrganizationID, receiptNumber)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Cannot read sale")
		return
	}
	type saleState struct{ id, receipt, saleStatus, paymentMethod, shiftStatus string }
	sales := make([]saleState, 0, 2)
	for rows.Next() {
		var sale saleState
		if err = rows.Scan(&sale.id, &sale.receipt, &sale.saleStatus, &sale.paymentMethod, &sale.shiftStatus); err != nil {
			rows.Close()
			fail(w, http.StatusServiceUnavailable, "Cannot read sale")
			return
		}
		sales = append(sales, sale)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		fail(w, http.StatusServiceUnavailable, "Cannot read sale")
		return
	}
	if err = rows.Close(); err != nil {
		fail(w, http.StatusServiceUnavailable, "Cannot read sale")
		return
	}
	if len(sales) == 0 {
		fail(w, http.StatusNotFound, "Receipt not found")
		return
	}
	if len(sales) != 1 {
		fail(w, http.StatusConflict, "Receipt lookup is ambiguous")
		return
	}
	sale := sales[0]
	lineRows, err := a.db.Query(`SELECT id,item_id,name,barcode,quantity,refunded_quantity,line_total
		FROM pos_sale_lines WHERE organization_id=? AND sale_id=? ORDER BY created_at,id LIMIT 101`, id.OrganizationID, sale.id)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Cannot read sale lines")
		return
	}
	lines := make([]posRefundLookupLine, 0, 100)
	hasRemainingQuantity := false
	for lineRows.Next() {
		var line posRefundLookupLine
		if err = lineRows.Scan(&line.SaleLineID, &line.ItemID, &line.Name, &line.Barcode, &line.Quantity, &line.RefundedQuantity, &line.LineTotal); err != nil {
			lineRows.Close()
			fail(w, http.StatusServiceUnavailable, "Cannot read sale line")
			return
		}
		if len(lines) == 100 {
			lineRows.Close()
			fail(w, http.StatusConflict, "Sale has too many lines for refund lookup")
			return
		}
		if line.Quantity < 1 || line.Quantity > 1000000 || line.RefundedQuantity < 0 || line.RefundedQuantity > line.Quantity || line.LineTotal < 0 || line.LineTotal > 1000000000 {
			lineRows.Close()
			fail(w, http.StatusServiceUnavailable, "Sale line data is invalid")
			return
		}
		line.RemainingQuantity = line.Quantity - line.RefundedQuantity
		if line.RemainingQuantity > 0 {
			hasRemainingQuantity = true
		}
		lines = append(lines, line)
	}
	if err = lineRows.Err(); err != nil {
		lineRows.Close()
		fail(w, http.StatusServiceUnavailable, "Cannot read sale lines")
		return
	}
	if err = lineRows.Close(); err != nil {
		fail(w, http.StatusServiceUnavailable, "Cannot read sale lines")
		return
	}
	if len(lines) == 0 {
		fail(w, http.StatusNotFound, "Receipt not found")
		return
	}
	eligible := sale.saleStatus == "completed" && sale.shiftStatus == "open" && sale.paymentMethod == "cash" && hasRemainingQuantity
	ok(w, posRefundLookup{SaleID: sale.id, ReceiptNumber: sale.receipt, SaleStatus: sale.saleStatus, PaymentMethod: sale.paymentMethod, ShiftStatus: sale.shiftStatus, Eligible: eligible, Lines: lines})
}
