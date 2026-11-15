package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPOSTerminalScopesRegistersAndOpenShifts(t *testing.T) {
	a := appForTest(t)
	space := token()
	front := loginSite(t, a, "front@suite.test", space, "pos")
	admin := loginSite(t, a, "admin@suite.test", space, "pos")
	other := loginSite(t, a, "admin@suite.test", token(), "pos")
	read := func(cookies ...*http.Cookie) map[string]json.RawMessage {
		t.Helper()
		w := request(a, "GET", "/api/stations/pos/terminal", nil, cookies...)
		if w.Code != 200 {
			t.Fatalf("terminal: %d %s", w.Code, w.Body.String())
		}
		var envelope struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	var frontData struct {
		Registers  []map[string]any `json:"registers"`
		OpenShifts []map[string]any `json:"openShifts"`
	}
	if err := json.Unmarshal(mustJSON(t, read(front...)), &frontData); err != nil {
		t.Fatal(err)
	}
	if len(frontData.Registers) == 0 || len(frontData.OpenShifts) != 1 {
		t.Fatalf("front terminal registers=%d openShifts=%d", len(frontData.Registers), len(frontData.OpenShifts))
	}
	if frontData.OpenShifts[0]["expectedCash"] != frontData.OpenShifts[0]["openingCash"] {
		t.Fatalf("fresh terminal balance should match opening float: %#v", frontData.OpenShifts[0])
	}
	var adminData struct {
		Registers  []map[string]any `json:"registers"`
		OpenShifts []map[string]any `json:"openShifts"`
	}
	if err := json.Unmarshal(mustJSON(t, read(admin...)), &adminData); err != nil {
		t.Fatal(err)
	}
	if len(adminData.Registers) == 0 || len(adminData.OpenShifts) != 0 {
		t.Fatalf("admin terminal registers=%d ownOpenShifts=%d", len(adminData.Registers), len(adminData.OpenShifts))
	}
	var foreign struct {
		Registers  []map[string]any `json:"registers"`
		OpenShifts []map[string]any `json:"openShifts"`
	}
	if err := json.Unmarshal(mustJSON(t, read(other...)), &foreign); err != nil {
		t.Fatal(err)
	}
	if len(foreign.Registers) == 0 || len(foreign.OpenShifts) != 0 || foreign.Registers[0]["id"] == frontData.Registers[0]["id"] {
		t.Fatalf("foreign organization got unexpected terminal registers=%d shifts=%d", len(foreign.Registers), len(foreign.OpenShifts))
	}
}

func mustJSON(t *testing.T, data map[string]json.RawMessage) []byte {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
