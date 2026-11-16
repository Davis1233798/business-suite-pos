package main

import (
	"encoding/json"
	"testing"
)

func TestPOSFrontReceiptScope(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	front := loginSite(t, a, "front@suite.test", space, "pos")
	admin := loginSite(t, a, "admin@suite.test", space, "pos")
	var me struct {
		Data Identity `json:"data"`
	}
	if e := json.Unmarshal(request(a, "GET", "/api/auth/me", nil, front...).Body.Bytes(), &me); e != nil {
		t.Fatal(e)
	}
	for _, row := range []struct{ id, owner string }{{"own-sale", me.Data.UserID}, {"team-sale", "different-cashier"}} {
		if _, e := a.db.Exec("INSERT INTO pos_sales(id,organization_id,receipt_number,store_id,register_id,shift_id,cashier_id,subtotal,total_amount,tender_amount) VALUES(?,?,?,'store','register','shift',?,10,10,10)", row.id, org, row.id, row.owner); e != nil {
			t.Fatal(e)
		}
	}
	frontData := request(a, "GET", "/api/stations/pos/data?view=front", nil, front...)
	var listed struct {
		Data struct {
			Sales []map[string]any `json:"sales"`
		} `json:"data"`
	}
	if e := json.Unmarshal(frontData.Body.Bytes(), &listed); e != nil {
		t.Fatal(e)
	}
	if len(listed.Data.Sales) != 1 || listed.Data.Sales[0]["id"] != "own-sale" {
		t.Fatalf("cashier receipt list included another cashier's sale: %s", frontData.Body.String())
	}
	firstPrint := request(a, "GET", "/api/stations/pos/print?kind=pos-receipt&id=own-sale", nil, front...)
	if firstPrint.Code != 200 {
		t.Fatalf("own %d %s", firstPrint.Code, firstPrint.Body.String())
	}
	var ordinary struct {
		Data map[string]any `json:"data"`
	}
	if e := json.Unmarshal(firstPrint.Body.Bytes(), &ordinary); e != nil {
		t.Fatal(e)
	}
	if _, marked := ordinary.Data["isReprint"]; marked {
		t.Fatalf("ordinary receipt preview was marked as a reprint: %s", firstPrint.Body.String())
	}
	reprint := request(a, "GET", "/api/stations/pos/print?kind=pos-receipt&id=own-sale&reprint=true", nil, front...)
	if reprint.Code != 200 {
		t.Fatalf("own cashier reprint %d %s", reprint.Code, reprint.Body.String())
	}
	var marked struct {
		Data struct {
			IsReprint bool `json:"isReprint"`
		} `json:"data"`
	}
	if e := json.Unmarshal(reprint.Body.Bytes(), &marked); e != nil || !marked.Data.IsReprint {
		t.Fatalf("explicit receipt reprint marker missing: %s (%v)", reprint.Body.String(), e)
	}
	for _, suffix := range []string{"reprint=1", "reprint=true&reprint=false", "reprint", "reprint=%ZZ"} {
		w := request(a, "GET", "/api/stations/pos/print?kind=pos-receipt&id=own-sale&"+suffix, nil, front...)
		if w.Code != 400 {
			t.Errorf("invalid reprint query %q: %d %s", suffix, w.Code, w.Body.String())
		}
	}
	if w := request(a, "GET", "/api/stations/pos/print?kind=pos-shift&id=shift&reprint=true", nil, admin...); w.Code != 400 {
		t.Errorf("reprint query on non-receipt format: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{
		"/api/stations/pos/print?kind=pos-receipt&kind=pos-receipt&id=own-sale",
		"/api/stations/pos/print?kind=pos-receipt&id=own-sale&id=team-sale",
	} {
		if w := request(a, "GET", path, nil, front...); w.Code != 400 {
			t.Errorf("ambiguous print query %q: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := request(a, "GET", "/api/stations/pos/print?kind=pos-receipt&id=team-sale", nil, front...); w.Code != 404 {
		t.Fatalf("other cashier %d", w.Code)
	}
	if w := request(a, "GET", "/api/stations/pos/print?kind=pos-receipt&id=team-sale", nil, admin...); w.Code != 200 {
		t.Fatalf("admin %d", w.Code)
	}
	if w := request(a, "GET", "/api/stations/pos/print?kind=pos-receipt&id=team-sale&reprint=true", nil, admin...); w.Code != 404 {
		t.Fatalf("administrator reprinted another cashier's sale: %d %s", w.Code, w.Body.String())
	}
	if w := request(a, "GET", "/api/stations/pos/print?kind=pos-shift&id=shift", nil, front...); w.Code != 403 {
		t.Fatalf("shift %d", w.Code)
	}
	var saleReprintState int
	if e := a.db.QueryRow("SELECT is_reprint FROM pos_sales WHERE id='own-sale'").Scan(&saleReprintState); e != nil || saleReprintState != 0 {
		t.Fatalf("receipt reprint mutated sale state: is_reprint=%d err=%v", saleReprintState, e)
	}
}
