package main

import "net/http"

// posTerminal exposes only the registers in the caller's organization and
// open shifts owned by the signed-in cashier. It is the minimum context needed
// by the front-office register UI; it deliberately excludes back-office data.
func (a *App) posTerminal(w http.ResponseWriter, r *http.Request, id *Identity) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Use GET")
		return
	}
	registers, err := queryMaps(a.db, `SELECT id,name,store_id FROM pos_registers WHERE organization_id=? AND is_active=1 ORDER BY name,id`, id.OrganizationID)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Cannot read active registers")
		return
	}
	shifts, err := queryMaps(a.db, `SELECT id,store_id,register_id,opening_cash,opened_at FROM pos_shifts WHERE organization_id=? AND cashier_id=? AND status='open' ORDER BY opened_at,id`, id.OrganizationID, id.UserID)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Cannot read your open shifts")
		return
	}
	for _, shift := range shifts {
		shiftID, idOK := shift["id"].(string)
		opening, openingOK := shift["openingCash"].(int64)
		if !idOK || !openingOK {
			fail(w, http.StatusServiceUnavailable, "Invalid open shift data")
			return
		}
		expected, e := posShiftExpectedCash(a.db, id.OrganizationID, shiftID, opening)
		if e != nil {
			fail(w, http.StatusServiceUnavailable, "Cannot calculate current drawer balance")
			return
		}
		shift["expectedCash"] = expected
	}
	ok(w, map[string]any{"registers": registers, "openShifts": shifts})
}
