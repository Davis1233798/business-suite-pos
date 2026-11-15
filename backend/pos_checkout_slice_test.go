package main

import (
	"encoding/json"
	"testing"
)

func TestPOSCartCashierSliceDiscountedMultiQuantity(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	staff := loginSite(t, a, "front@suite.test", space, "pos")
	var shift, item string
	var stock int64
	if err := a.db.QueryRow("SELECT id FROM pos_shifts WHERE organization_id=? AND status='open' LIMIT 1", org).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT id,stock_quantity FROM pos_catalog_items WHERE organization_id=? AND barcode='4710001001' AND is_active=1", org).Scan(&item, &stock); err != nil {
		t.Fatal(err)
	}
	in := map[string]any{
		"action": "checkout", "shiftId": shift,
		"lines":  []any{map[string]any{"itemId": item, "quantity": 2, "discountAmount": 10}},
		"tender": 200, "idempotencyKey": "cashier-slice-190-0001",
	}
	w := request(a, "POST", "/api/stations/pos/cart", in, staff...)
	if w.Code != 200 {
		t.Fatalf("checkout %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Data struct {
			SaleID         string `json:"saleId"`
			ReceiptNumber  string `json:"receiptNumber"`
			Subtotal       int64  `json:"subtotal"`
			DiscountAmount int64  `json:"discountAmount"`
			TotalAmount    int64  `json:"totalAmount"`
			ChangeAmount   int64  `json:"changeAmount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.SaleID == "" || result.Data.ReceiptNumber == "" || result.Data.Subtotal != 200 || result.Data.DiscountAmount != 10 || result.Data.TotalAmount != 190 || result.Data.ChangeAmount != 10 {
		t.Fatalf("unexpected receipt totals: %+v", result.Data)
	}
	var savedSubtotal, savedDiscount, savedTotal, savedLineDiscount, savedLineTotal, remaining int64
	if err := a.db.QueryRow("SELECT subtotal,discount_amount,total_amount FROM pos_sales WHERE organization_id=? AND id=?", org, result.Data.SaleID).Scan(&savedSubtotal, &savedDiscount, &savedTotal); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT discount_amount,line_total FROM pos_sale_lines WHERE organization_id=? AND sale_id=?", org, result.Data.SaleID).Scan(&savedLineDiscount, &savedLineTotal); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT stock_quantity FROM pos_catalog_items WHERE organization_id=? AND id=?", org, item).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if savedSubtotal != 200 || savedDiscount != 10 || savedTotal != 190 || savedLineDiscount != 10 || savedLineTotal != 190 || remaining != stock-2 {
		t.Fatalf("persisted sale mismatch subtotal=%d discount=%d total=%d lineDiscount=%d lineTotal=%d stock=%d/%d", savedSubtotal, savedDiscount, savedTotal, savedLineDiscount, savedLineTotal, remaining, stock)
	}
}
