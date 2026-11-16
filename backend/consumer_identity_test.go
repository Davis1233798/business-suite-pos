package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordingConsumerChallengeSender struct {
	email string
	token string
}

type failingConsumerChallengeSender struct{}

func (failingConsumerChallengeSender) SendVerification(context.Context, string, string, string, time.Time) error {
	return errors.New("provider unavailable")
}

func (s *recordingConsumerChallengeSender) SendVerification(_ context.Context, _, email, token string, _ time.Time) error {
	s.email = email
	s.token = token
	return nil
}

func newConsumerIdentityTestApp(t *testing.T, enabled bool, sender ConsumerChallengeSender) *App {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "consumer.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := &App{db: db, consumerIdentityEnabled: enabled, consumerChallengeSender: sender}
	space := token()
	if err := a.initializeDemo(space); err != nil {
		t.Fatal(err)
	}
	return a
}

func consumerRequest(a *App, method, slug, action string, input any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	path := "/api/pos/consumer/" + slug + "/" + action
	encoded, _ := json.Marshal(input)
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://localhost")
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func consumerOrgSlug(t *testing.T, a *App) string {
	t.Helper()
	var slug string
	if err := a.db.QueryRow("SELECT slug FROM organizations ORDER BY slug LIMIT 1").Scan(&slug); err != nil {
		t.Fatal(err)
	}
	return slug
}

func TestConsumerIdentityDisabledByDefaultAndDoesNotUseEmployeeSession(t *testing.T) {
	a := newConsumerIdentityTestApp(t, false, nil)
	slug := consumerOrgSlug(t, a)
	employee := login(t, a, "admin@suite.test", strings.TrimPrefix(slug, "org_test_"))
	for _, action := range []string{"register", "verify", "login", "me", "logout"} {
		method := http.MethodGet
		if action != "me" {
			method = http.MethodPost
		}
		if got := consumerRequest(a, method, slug, action, map[string]string{}, employee...); got.Code != http.StatusNotFound {
			t.Fatalf("disabled consumer route %s returned %d", action, got.Code)
		}
	}
}

func TestConsumerRegistrationVerificationSessionAndTenantIsolation(t *testing.T) {
	sender := &recordingConsumerChallengeSender{}
	a := newConsumerIdentityTestApp(t, true, sender)
	slug := consumerOrgSlug(t, a)
	input := map[string]string{"email": "  Alice.Example@Example.TEST "}
	w := consumerRequest(a, http.MethodPost, slug, "register", input)
	if w.Code != http.StatusAccepted {
		t.Fatalf("registration: %d %s", w.Code, w.Body.String())
	}
	if sender.token == "" || sender.email != "alice.example@example.test" {
		t.Fatalf("verification delivery not invoked with normalized email: %#v", sender)
	}
	firstChallenge := sender.token
	if got := consumerRequest(a, http.MethodPost, slug, "register", map[string]string{"email": input["email"], "password": "attacker-chosen password"}); got.Code != http.StatusBadRequest {
		t.Fatalf("registration must not accept a pre-verification password: %d", got.Code)
	}
	if got := consumerRequest(a, http.MethodPost, slug, "register", map[string]string{"email": "alice.example@example.test"}); got.Code != http.StatusAccepted {
		t.Fatalf("duplicate pending registration: %d %s", got.Code, got.Body.String())
	}
	if sender.token == firstChallenge {
		t.Fatal("duplicate pending registration did not issue a fresh one-time challenge")
	}
	if strings.Contains(w.Body.String(), sender.token) || strings.Contains(w.Body.String(), "password") {
		t.Fatal("registration response leaked credentials or challenge")
	}
	var accountID, storedEmail, encodedPassword string
	var verifiedAt any
	if err := a.db.QueryRow(`SELECT id,normalized_email,password_hash,verified_at FROM pos_consumer_accounts WHERE organization_id=(SELECT id FROM organizations WHERE slug=?)`, slug).Scan(&accountID, &storedEmail, &encodedPassword, &verifiedAt); err != nil {
		t.Fatal(err)
	}
	if storedEmail != "alice.example@example.test" || encodedPassword != "" || verifiedAt != nil {
		t.Fatalf("unexpected account state: email=%q hash=%q verified=%v", storedEmail, encodedPassword, verifiedAt)
	}
	var storedChallenge string
	if err := a.db.QueryRow(`SELECT token_hash FROM pos_consumer_verification_challenges WHERE account_id=?`, accountID).Scan(&storedChallenge); err != nil {
		t.Fatal(err)
	}
	if storedChallenge != digest(sender.token) || storedChallenge == sender.token {
		t.Fatal("challenge secret was not stored only as a hash")
	}
	attackerPassword := "attacker-chosen password"
	if got := consumerRequest(a, http.MethodPost, slug, "login", map[string]string{"email": input["email"], "password": attackerPassword}); got.Code != http.StatusUnauthorized {
		t.Fatalf("unverified account logged in: %d", got.Code)
	}
	victimPassword := "victim-chosen password"
	if got := consumerRequest(a, http.MethodPost, slug, "verify", map[string]string{"token": sender.token, "password": victimPassword}); got.Code != http.StatusOK {
		t.Fatalf("verification: %d %s", got.Code, got.Body.String())
	}
	if got := consumerRequest(a, http.MethodPost, slug, "verify", map[string]string{"token": sender.token, "password": victimPassword}); got.Code != http.StatusBadRequest {
		t.Fatalf("verification challenge was reusable: %d", got.Code)
	}
	if got := consumerRequest(a, http.MethodPost, slug, "login", map[string]string{"email": input["email"], "password": attackerPassword}); got.Code != http.StatusUnauthorized {
		t.Fatalf("pre-registration attacker password was accepted after victim verified: %d", got.Code)
	}
	w = consumerRequest(a, http.MethodPost, slug, "login", map[string]string{"email": input["email"], "password": victimPassword})
	if w.Code != http.StatusOK {
		t.Fatalf("consumer login: %d %s", w.Code, w.Body.String())
	}
	var cookie *http.Cookie
	for _, candidate := range w.Result().Cookies() {
		if candidate.Name == consumerSessionCookieName {
			cookie = candidate
		}
	}
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.Domain != "" || cookie.MaxAge <= 0 {
		t.Fatalf("consumer cookie flags are unsafe: %#v", cookie)
	}
	if cookie.Name == cookieName || cookie.Name == cookieName+"_pos" {
		t.Fatal("consumer session reused an employee cookie")
	}
	var storedSession string
	if err := a.db.QueryRow(`SELECT token_hash FROM pos_consumer_sessions WHERE account_id=?`, accountID).Scan(&storedSession); err != nil {
		t.Fatal(err)
	}
	if storedSession != digest(cookie.Value) {
		t.Fatal("consumer session secret was not stored only as a hash")
	}
	w = consumerRequest(a, http.MethodGet, slug, "me", nil, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("consumer me: %d %s", w.Code, w.Body.String())
	}
	var me struct {
		Data struct {
			Email string `json:"email"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || me.Data.Email != storedEmail || strings.Contains(w.Body.String(), "organizationId") || strings.Contains(w.Body.String(), accountID) {
		t.Fatalf("unexpected self identity: %#v, %v", me, err)
	}

	otherSpace := token()
	if err := a.initializeDemo(otherSpace); err != nil {
		t.Fatal(err)
	}
	otherSlug := "org_test_" + otherSpace
	if w = consumerRequest(a, http.MethodGet, otherSlug, "me", nil, cookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("consumer session crossed organization boundary: %d", w.Code)
	}
	employee := login(t, a, "admin@suite.test", strings.TrimPrefix(slug, "org_test_"))
	if w = consumerRequest(a, http.MethodGet, slug, "me", nil, employee...); w.Code != http.StatusUnauthorized {
		t.Fatalf("employee session fell back to consumer identity: %d", w.Code)
	}
	if w = consumerRequest(a, http.MethodPost, slug, "logout", map[string]string{}, cookie); w.Code != http.StatusOK {
		t.Fatalf("consumer logout: %d", w.Code)
	}
	if w = consumerRequest(a, http.MethodGet, slug, "me", nil, cookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked consumer session accepted: %d", w.Code)
	}
}

func TestConsumerLoginUsesDummyPasswordHashForUnverifiedAndUnknownAccounts(t *testing.T) {
	sender := &recordingConsumerChallengeSender{}
	a := newConsumerIdentityTestApp(t, true, sender)
	slug := consumerOrgSlug(t, a)
	if w := consumerRequest(a, http.MethodPost, slug, "register", map[string]string{"email": "pending@example.test"}); w.Code != http.StatusAccepted {
		t.Fatalf("registration: %d %s", w.Code, w.Body.String())
	}
	var checked []string
	a.consumerPasswordVerifier = func(_, encoded string) bool {
		checked = append(checked, encoded)
		return false
	}
	for _, email := range []string{"pending@example.test", "missing@example.test"} {
		if w := consumerRequest(a, http.MethodPost, slug, "login", map[string]string{"email": email, "password": "candidate password"}); w.Code != http.StatusUnauthorized {
			t.Fatalf("unexpected login result for %s: %d", email, w.Code)
		}
	}
	if len(checked) != 2 || checked[0] != dummyConsumerPasswordHash || checked[1] != dummyConsumerPasswordHash {
		t.Fatalf("pending/unknown accounts did not use equal-cost dummy verification: %#v", checked)
	}
}

func TestConsumerVerificationChecksChallengeBeforePasswordHashing(t *testing.T) {
	sender := &recordingConsumerChallengeSender{}
	a := newConsumerIdentityTestApp(t, true, sender)
	slug := consumerOrgSlug(t, a)
	if w := consumerRequest(a, http.MethodPost, slug, "register", map[string]string{"email": "verify@example.test"}); w.Code != http.StatusAccepted {
		t.Fatalf("registration: %d %s", w.Code, w.Body.String())
	}
	hashCalls := 0
	a.consumerPasswordHasher = func(string) (string, error) {
		hashCalls++
		return "test-password-hash", nil
	}
	if w := consumerRequest(a, http.MethodPost, slug, "verify", map[string]string{"token": token(), "password": "recipient-selected password"}); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown challenge accepted: %d", w.Code)
	}
	if hashCalls != 0 {
		t.Fatalf("invalid challenge triggered password hashing: calls=%d", hashCalls)
	}
	if w := consumerRequest(a, http.MethodPost, slug, "verify", map[string]string{"token": sender.token, "password": "recipient-selected password"}); w.Code != http.StatusOK {
		t.Fatalf("valid challenge rejected: %d %s", w.Code, w.Body.String())
	}
	if hashCalls != 1 {
		t.Fatalf("valid challenge did not hash recipient password once: calls=%d", hashCalls)
	}
}

func TestConsumerRegistrationFailsClosedWithoutDeliveryAndNormalizesUniquenessPerTenant(t *testing.T) {
	a := newConsumerIdentityTestApp(t, true, nil)
	slug := consumerOrgSlug(t, a)
	input := map[string]string{"email": "Unique@Example.Test"}
	if w := consumerRequest(a, http.MethodPost, slug, "register", input); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("registration without delivery must fail closed: %d", w.Code)
	}
	var count int
	if err := a.db.QueryRow(`SELECT count(*) FROM pos_consumer_accounts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("un-deliverable registration persisted: count=%d err=%v", count, err)
	}
}

func TestConsumerEmailNormalizationAndOrganizationScopedUniqueness(t *testing.T) {
	sender := &recordingConsumerChallengeSender{}
	a := newConsumerIdentityTestApp(t, true, sender)
	slug := consumerOrgSlug(t, a)
	first := map[string]string{"email": "Member@Example.Test"}
	if w := consumerRequest(a, http.MethodPost, slug, "register", first); w.Code != http.StatusAccepted {
		t.Fatalf("registration: %d %s", w.Code, w.Body.String())
	}
	second := map[string]string{"email": " member@example.test "}
	if w := consumerRequest(a, http.MethodPost, slug, "register", second); w.Code != http.StatusAccepted {
		t.Fatalf("duplicate normalized registration should be non-enumerating: %d %s", w.Code, w.Body.String())
	}
	var sameOrgCount int
	if err := a.db.QueryRow(`SELECT count(*) FROM pos_consumer_accounts WHERE organization_id=(SELECT id FROM organizations WHERE slug=?) AND normalized_email='member@example.test'`, slug).Scan(&sameOrgCount); err != nil || sameOrgCount != 1 {
		t.Fatalf("email normalization/uniqueness failed: count=%d err=%v", sameOrgCount, err)
	}
	otherSpace := token()
	if err := a.initializeDemo(otherSpace); err != nil {
		t.Fatal(err)
	}
	if w := consumerRequest(a, http.MethodPost, "org_test_"+otherSpace, "register", first); w.Code != http.StatusAccepted {
		t.Fatalf("cross-organization same email should remain independent: %d %s", w.Code, w.Body.String())
	}
	var total int
	if err := a.db.QueryRow(`SELECT count(*) FROM pos_consumer_accounts WHERE normalized_email='member@example.test'`).Scan(&total); err != nil || total != 2 {
		t.Fatalf("organization-scoped email uniqueness failed: count=%d err=%v", total, err)
	}
}

func TestConsumerDeliveryFailureKeepsAccountUnverifiedAndChallengeInactive(t *testing.T) {
	a := newConsumerIdentityTestApp(t, true, failingConsumerChallengeSender{})
	slug := consumerOrgSlug(t, a)
	input := map[string]string{"email": "retry@example.test"}
	if w := consumerRequest(a, http.MethodPost, slug, "register", input); w.Code != http.StatusAccepted {
		t.Fatalf("registration response should not disclose provider outcome: %d", w.Code)
	}
	var verifiedAt any
	var storedPassword string
	var activeChallenges int
	if err := a.db.QueryRow(`SELECT verified_at,password_hash FROM pos_consumer_accounts WHERE normalized_email='retry@example.test'`).Scan(&verifiedAt, &storedPassword); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM pos_consumer_verification_challenges WHERE consumed_at IS NULL`).Scan(&activeChallenges); err != nil {
		t.Fatal(err)
	}
	if verifiedAt != nil || storedPassword != "" || activeChallenges != 0 {
		t.Fatalf("failed delivery left a usable account/challenge: verified=%v password=%q active=%d", verifiedAt, storedPassword, activeChallenges)
	}
}

func TestConsumerMutationRequiresSameOriginAndTrustedProxy(t *testing.T) {
	a := newConsumerIdentityTestApp(t, true, nil)
	slug := consumerOrgSlug(t, a)
	makeRequest := func(origin, forwardedProto string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://localhost/api/pos/consumer/"+slug+"/register", strings.NewReader(`{"email":"origin@example.test"}`))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if forwardedProto != "" {
			r.Header.Set("X-Forwarded-Proto", forwardedProto)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	if w := makeRequest("", ""); w.Code != http.StatusForbidden {
		t.Fatalf("missing Origin accepted: %d", w.Code)
	}
	if w := makeRequest("https://localhost", "https"); w.Code != http.StatusForbidden {
		t.Fatalf("untrusted forwarded proto accepted: %d", w.Code)
	}
	a.trustedProxyNetworks = parseTrustedProxyCIDRs("192.0.2.0/24")
	if w := makeRequest("https://localhost", "https"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("configured trusted proxy scheme was not used: %d", w.Code)
	}
}

func TestConsumerMigrationIsAdditiveAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "consumer-migration.sqlite")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO organizations(id,name,slug) VALUES('migration-org','Migration org','migration-org')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO pos_consumer_accounts(id,organization_id,normalized_email,password_hash,created_at,updated_at) VALUES('migration-account','migration-org','migration@example.test','hash',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var accountCount, markerVersion int
	if err = db.QueryRow(`SELECT count(*) FROM pos_consumer_accounts WHERE id='migration-account'`).Scan(&accountCount); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT version FROM pos_repository_schema`).Scan(&markerVersion); err != nil {
		t.Fatal(err)
	}
	if accountCount != 1 || markerVersion != 1 {
		t.Fatalf("additive migration changed existing rows or marker: accounts=%d marker=%d", accountCount, markerVersion)
	}
}

func TestConsumerSchemaRejectsVerifiedAccountWithoutPassword(t *testing.T) {
	a := newConsumerIdentityTestApp(t, true, &recordingConsumerChallengeSender{})
	var organizationID string
	if err := a.db.QueryRow(`SELECT id FROM organizations ORDER BY id LIMIT 1`).Scan(&organizationID); err != nil {
		t.Fatal(err)
	}
	_, err := a.db.Exec(`INSERT INTO pos_consumer_accounts(id,organization_id,normalized_email,password_hash,verified_at,created_at,updated_at) VALUES('empty-verified',?,?,'',1,1,1)`, organizationID, "empty@example.test")
	if err == nil {
		t.Fatal("database accepted a verified account without a password hash")
	}
}
