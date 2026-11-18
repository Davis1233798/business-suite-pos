package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newVerifiedConsumerSession(t *testing.T, a *App, organizationID, slug, email, password string) *http.Cookie {
	t.Helper()
	rec := &recordingConsumerChallengeSender{}
	a.consumerChallengeSender = rec

	regReq := consumerRequest(a, http.MethodPost, slug, "register", consumerRegistrationInput{Email: email})
	if regReq.Code != http.StatusAccepted {
		t.Fatalf("register failed: %d: %s", regReq.Code, regReq.Body.String())
	}
	if rec.token == "" {
		t.Fatalf("expected verification token for %s", email)
	}

	verReq := consumerRequest(a, http.MethodPost, slug, "verify", consumerVerificationInput{Token: rec.token, Password: password})
	if verReq.Code != http.StatusOK {
		t.Fatalf("verify failed: %d: %s", verReq.Code, verReq.Body.String())
	}

	loginReq := consumerRequest(a, http.MethodPost, slug, "login", consumerLoginInput{Email: email, Password: password})
	if loginReq.Code != http.StatusOK {
		t.Fatalf("login failed: %d: %s", loginReq.Code, loginReq.Body.String())
	}

	for _, c := range loginReq.Result().Cookies() {
		if c.Name == consumerSessionCookieName {
			return c
		}
	}
	t.Fatal("session cookie not returned")
	return nil
}

func TestConsumerPairingAndCheckoutAssociation(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "consumer_pairing.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	a := &App{db: db, consumerIdentityEnabled: true}
	space := token()
	if err := a.initializeDemo(space); err != nil {
		t.Fatal(err)
	}

	var orgID, slug string
	if err := a.db.QueryRow(`SELECT id,slug FROM organizations WHERE slug=?`, "org_test_"+space).Scan(&orgID, &slug); err != nil {
		t.Fatal(err)
	}

	// 1. Unauthenticated pair request should fail
	unauthReq := consumerRequest(a, http.MethodPost, slug, "pair", map[string]any{})
	if unauthReq.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated pair, got %d", unauthReq.Code)
	}

	// 2. Verified consumer generates pairing token
	cookie := newVerifiedConsumerSession(t, a, orgID, slug, "shopper@example.test", "CorrectHorse8#")
	pairReq := consumerRequest(a, http.MethodPost, slug, "pair", map[string]any{}, cookie)
	if pairReq.Code != http.StatusOK {
		t.Fatalf("pair request failed: %d: %s", pairReq.Code, pairReq.Body.String())
	}

	var pairResp struct {
		Success bool `json:"success"`
		Data    struct {
			PairingToken string `json:"pairingToken"`
			ExpiresAt    int64  `json:"expiresAt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(pairReq.Body.Bytes(), &pairResp); err != nil {
		t.Fatalf("invalid pair response: %v", err)
	}
	if pairResp.Data.PairingToken == "" || pairResp.Data.ExpiresAt <= time.Now().UnixMilli() {
		t.Fatalf("expected valid pairing token and future expiry, got %+v", pairResp)
	}

	// 3. Get existing open shift and execute checkout with pairing token
	var shiftID, cashierID, itemID string
	if err := a.db.QueryRow(`SELECT id,cashier_id FROM pos_shifts WHERE organization_id=? AND status='open' LIMIT 1`, orgID).Scan(&shiftID, &cashierID); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT id FROM pos_catalog_items WHERE organization_id=? AND stock_quantity>5 LIMIT 1`, orgID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}

	id := &Identity{UserID: cashierID, OrganizationID: orgID}
	grant := &Grant{Role: "cashier", Scope: "front"}

	checkoutCmd := posCommand{
		Action:         "checkout",
		ShiftID:        shiftID,
		ItemID:         itemID,
		Quantity:       1,
		Tender:         5000,
		PairingToken:   pairResp.Data.PairingToken,
		IdempotencyKey: "idem_chk_" + token(),
	}
	chkW := posBusinessRequest(a, checkoutCmd, id, grant)
	if chkW.Code != http.StatusOK {
		t.Fatalf("checkout with pairing token failed: %d: %s", chkW.Code, chkW.Body.String())
	}

	var chkResp map[string]any
	json.Unmarshal(chkW.Body.Bytes(), &chkResp)
	saleData := chkResp["data"].(map[string]any)
	saleID := saleData["saleId"].(string)

	// Verify customer_id on pos_sales
	var customerID sqlNullString
	if err := a.db.QueryRow(`SELECT customer_id FROM pos_sales WHERE id=?`, saleID).Scan(&customerID); err != nil {
		t.Fatal(err)
	}
	if !customerID.Valid || customerID.String == "" {
		t.Fatalf("expected sale to have linked customer_id, got %+v", customerID)
	}

	// 4. Reusing pairing token must be rejected
	reuseCmd := posCommand{
		Action:         "checkout",
		ShiftID:        shiftID,
		ItemID:         itemID,
		Quantity:       1,
		Tender:         5000,
		PairingToken:   pairResp.Data.PairingToken,
		IdempotencyKey: "idem_reuse_" + token(),
	}
	reuseW := posBusinessRequest(a, reuseCmd, id, grant)
	if reuseW.Code != http.StatusConflict && reuseW.Code != http.StatusBadRequest {
		t.Fatalf("expected conflict or bad request on reused pairing token, got %d: %s", reuseW.Code, reuseW.Body.String())
	}

	// 5. Idempotent retry of original checkout replays cleanly
	retryW := posBusinessRequest(a, checkoutCmd, id, grant)
	if retryW.Code != http.StatusOK {
		t.Fatalf("idempotent retry failed: %d: %s", retryW.Code, retryW.Body.String())
	}

	// 6. Consumer transaction history returns the linked sale
	histReq := consumerRequest(a, http.MethodGet, slug, "transactions", nil, cookie)
	if histReq.Code != http.StatusOK {
		t.Fatalf("transaction history failed: %d: %s", histReq.Code, histReq.Body.String())
	}
	var histResp struct {
		Success bool `json:"success"`
		Data    struct {
			Transactions []struct {
				ID            string `json:"id"`
				ReceiptNumber string `json:"receiptNumber"`
				TotalAmount   int64  `json:"totalAmount"`
			} `json:"transactions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(histReq.Body.Bytes(), &histResp); err != nil {
		t.Fatalf("invalid history response: %v", err)
	}
	if len(histResp.Data.Transactions) != 1 || histResp.Data.Transactions[0].ID != saleID {
		t.Fatalf("expected history to contain sale %s, got %+v", saleID, histResp.Data.Transactions)
	}

	// 7. Another consumer cannot see this transaction
	cookie2 := newVerifiedConsumerSession(t, a, orgID, slug, "other@example.test", "OtherPassword123#")
	histReq2 := consumerRequest(a, http.MethodGet, slug, "transactions", nil, cookie2)
	if histReq2.Code != http.StatusOK {
		t.Fatalf("other history failed: %d", histReq2.Code)
	}
	var histResp2 struct {
		Data struct {
			Transactions []any `json:"transactions"`
		} `json:"data"`
	}
	json.Unmarshal(histReq2.Body.Bytes(), &histResp2)
	if len(histResp2.Data.Transactions) != 0 {
		t.Fatalf("other consumer must not see transactions, got %d", len(histResp2.Data.Transactions))
	}

	// 8. Cross-organization isolation
	otherSpace := token()
	if err := a.initializeDemo(otherSpace); err != nil {
		t.Fatal(err)
	}
	var otherOrgID, otherSlug string
	if err := a.db.QueryRow(`SELECT id,slug FROM organizations WHERE slug=?`, "org_test_"+otherSpace).Scan(&otherOrgID, &otherSlug); err != nil {
		t.Fatal(err)
	}
	otherCookie := newVerifiedConsumerSession(t, a, otherOrgID, otherSlug, "shopper@example.test", "CorrectHorse8#")
	otherPairReq := consumerRequest(a, http.MethodPost, otherSlug, "pair", map[string]any{}, otherCookie)
	if otherPairReq.Code != http.StatusOK {
		t.Fatalf("other pair request failed: %d", otherPairReq.Code)
	}
	var otherPairResp struct {
		Data struct {
			PairingToken string `json:"pairingToken"`
		} `json:"data"`
	}
	json.Unmarshal(otherPairReq.Body.Bytes(), &otherPairResp)

	// Attempt to use otherOrg pairing token in original org's checkout -> must be rejected
	crossCmd := posCommand{
		Action:         "checkout",
		ShiftID:        shiftID,
		ItemID:         itemID,
		Quantity:       1,
		Tender:         5000,
		PairingToken:   otherPairResp.Data.PairingToken,
		IdempotencyKey: "idem_cross_" + token(),
	}
	crossW := posBusinessRequest(a, crossCmd, id, grant)
	if crossW.Code != http.StatusConflict && crossW.Code != http.StatusBadRequest {
		t.Fatalf("expected cross-organization pairing token to be rejected, got %d", crossW.Code)
	}

	// 9. Expired pairing token is rejected
	expPairToken := "pair_" + token()
	if _, err := a.db.Exec(`INSERT INTO pos_consumer_pairing_tokens(token_hash,account_id,organization_id,expires_at,created_at) VALUES(?,?,?,?,?)`, digest(expPairToken), customerID.String, orgID, time.Now().Add(-1*time.Minute).UnixMilli(), time.Now().Add(-2*time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	expCmd := posCommand{
		Action:         "checkout",
		ShiftID:        shiftID,
		ItemID:         itemID,
		Quantity:       1,
		Tender:         5000,
		PairingToken:   expPairToken,
		IdempotencyKey: "idem_exp_" + token(),
	}
	expW := posBusinessRequest(a, expCmd, id, grant)
	if expW.Code != http.StatusConflict && expW.Code != http.StatusBadRequest {
		t.Fatalf("expected expired pairing token to be rejected, got %d: %s", expW.Code, expW.Body.String())
	}

	// 10. Anonymous checkout leaves customer_id null
	anonCmd := posCommand{
		Action:         "checkout",
		ShiftID:        shiftID,
		ItemID:         itemID,
		Quantity:       1,
		Tender:         5000,
		PairingToken:   "",
		IdempotencyKey: "idem_anon_" + token(),
	}
	anonW := posBusinessRequest(a, anonCmd, id, grant)
	if anonW.Code != http.StatusOK {
		t.Fatalf("anonymous checkout failed: %d: %s", anonW.Code, anonW.Body.String())
	}
	var anonResp map[string]any
	json.Unmarshal(anonW.Body.Bytes(), &anonResp)
	anonSaleID := anonResp["data"].(map[string]any)["saleId"].(string)
	var anonCustID sqlNullString
	if err := a.db.QueryRow(`SELECT customer_id FROM pos_sales WHERE id=?`, anonSaleID).Scan(&anonCustID); err != nil {
		t.Fatal(err)
	}
	if anonCustID.Valid && anonCustID.String != "" {
		t.Fatalf("expected anonymous checkout customer_id to be NULL, got %+v", anonCustID)
	}

	// 11. Disabled consumer account cannot pair
	if _, err := a.db.Exec(`UPDATE pos_consumer_accounts SET disabled=1 WHERE id=?`, customerID.String); err != nil {
		t.Fatal(err)
	}
	disPairReq := consumerRequest(a, http.MethodPost, slug, "pair", map[string]any{}, cookie)
	if disPairReq.Code != http.StatusUnauthorized && disPairReq.Code != http.StatusForbidden {
		t.Fatalf("expected disabled consumer pair to be unauthorized, got %d", disPairReq.Code)
	}
}

func posBusinessRequest(a *App, cmd posCommand, id *Identity, grant *Grant) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://localhost/station/pos/"+cmd.Action, strings.NewReader(testJSONString(cmd)))
	r.Header.Set("Content-Type", "application/json")
	a.posBusiness(w, r, id, grant)
	return w
}

type sqlNullString struct {
	String string
	Valid  bool
}

func (s *sqlNullString) Scan(value any) error {
	if value == nil {
		s.String, s.Valid = "", false
		return nil
	}
	switch v := value.(type) {
	case string:
		s.String, s.Valid = v, true
	case []byte:
		s.String, s.Valid = string(v), true
	}
	return nil
}

func testJSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
