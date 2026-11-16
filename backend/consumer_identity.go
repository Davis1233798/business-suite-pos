package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

const (
	consumerSessionCookieName = "pos_consumer_session"
	consumerSessionLifetime   = 12 * time.Hour
	consumerChallengeLifetime = 24 * time.Hour
)

// ConsumerChallengeSender is the explicit boundary for verification email delivery.
// Implementations must not log or persist the raw challenge token. A nil sender keeps
// registration fail-closed; production wiring must supply an asynchronous real sender.
type ConsumerChallengeSender interface {
	SendVerification(ctx context.Context, organizationSlug, email, token string, expiresAt time.Time) error
}

type consumerIdentity struct {
	Email string `json:"email"`
}

type consumerRegistrationInput struct {
	Email string `json:"email"`
}

type consumerVerificationInput struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type consumerLoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Email matching intentionally applies only trim and lowercase. It does not remove
// dots, plus tags, or otherwise assume provider-specific mailbox equivalence.
func normalizeConsumerEmail(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if len(normalized) == 0 || len(normalized) > 254 || strings.ContainsAny(normalized, " \t\r\n") {
		return "", false
	}
	parsed, err := mail.ParseAddress(normalized)
	if err != nil || parsed.Address != normalized || strings.Count(normalized, "@") != 1 {
		return "", false
	}
	return normalized, true
}

func validConsumerOrganizationSlug(slug string) bool {
	if len(slug) == 0 || len(slug) > 120 {
		return false
	}
	for _, ch := range slug {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' {
			return false
		}
	}
	return slug[0] != '-' && slug[len(slug)-1] != '-' && slug[0] != '_' && slug[len(slug)-1] != '_'
}

func consumerUnavailable(w http.ResponseWriter) {
	fail(w, http.StatusServiceUnavailable, "Consumer verification is not configured")
}

func consumerAccepted(w http.ResponseWriter) {
	writeJSON(w, http.StatusAccepted, map[string]any{"success": true, "data": map[string]bool{"accepted": true}})
}

func (a *App) hashConsumerPassword(password string) (string, error) {
	if a.consumerPasswordHasher != nil {
		return a.consumerPasswordHasher(password)
	}
	return passwordHash(password, "")
}

func (a *App) consumerPasswordMatches(password, encoded string) bool {
	if a.consumerPasswordVerifier != nil {
		return a.consumerPasswordVerifier(password, encoded)
	}
	return passwordOK(password, encoded)
}

func (a *App) consumerIdentityAPI(w http.ResponseWriter, r *http.Request) {
	if !a.consumerIdentityEnabled {
		fail(w, http.StatusNotFound, "Consumer identity is unavailable")
		return
	}
	slug := r.PathValue("organization")
	if !validConsumerOrganizationSlug(slug) {
		fail(w, http.StatusNotFound, "Organization not found")
		return
	}
	var organizationID string
	if err := a.db.QueryRow("SELECT id FROM organizations WHERE slug=?", slug).Scan(&organizationID); err != nil {
		fail(w, http.StatusNotFound, "Organization not found")
		return
	}
	action := r.PathValue("action")
	switch action {
	case "register":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		a.consumerRegister(w, r, organizationID, slug)
	case "verify":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		a.consumerVerify(w, r, organizationID)
	case "login":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		a.consumerLogin(w, r, organizationID)
	case "logout":
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		a.consumerLogout(w, r, organizationID)
	case "me":
		if r.Method != http.MethodGet {
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		a.consumerMe(w, r, organizationID)
	default:
		fail(w, http.StatusNotFound, "Operation not found")
	}
}

func (a *App) consumerRegister(w http.ResponseWriter, r *http.Request, organizationID, slug string) {
	if a.consumerChallengeSender == nil {
		consumerUnavailable(w)
		return
	}
	var input consumerRegistrationInput
	if body(r, &input) != nil {
		fail(w, http.StatusBadRequest, "Invalid registration data")
		return
	}
	email, ok := normalizeConsumerEmail(input.Email)
	if !ok {
		fail(w, http.StatusBadRequest, "Invalid registration data")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Registration service is temporarily unavailable")
		return
	}
	defer tx.Rollback()

	var accountID string
	var verifiedAt sql.NullInt64
	err = tx.QueryRow(`SELECT id,verified_at FROM pos_consumer_accounts WHERE organization_id=? AND normalized_email=?`, organizationID, email).Scan(&accountID, &verifiedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusServiceUnavailable, "Registration service is temporarily unavailable")
		return
	}
	if err == nil && verifiedAt.Valid {
		consumerAccepted(w)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		accountID = "consumer_" + token()
		now := time.Now().UnixMilli()
		if _, err = tx.Exec(`INSERT INTO pos_consumer_accounts(id,organization_id,normalized_email,password_hash,created_at,updated_at) VALUES(?,?,?,'',?,?)`, accountID, organizationID, email, now, now); err != nil {
			fail(w, http.StatusServiceUnavailable, "Registration service is temporarily unavailable")
			return
		}
	} else {
		// Replace expired or superseded verification challenges for this unverified account.
		if _, err = tx.Exec(`DELETE FROM pos_consumer_verification_challenges WHERE organization_id=? AND account_id=? AND consumed_at IS NULL`, organizationID, accountID); err != nil {
			fail(w, http.StatusServiceUnavailable, "Registration service is temporarily unavailable")
			return
		}
	}

	challenge := token()
	expires := time.Now().Add(consumerChallengeLifetime)
	if _, err = tx.Exec(`INSERT INTO pos_consumer_verification_challenges(token_hash,account_id,organization_id,expires_at,created_at) VALUES(?,?,?,?,?)`, digest(challenge), accountID, organizationID, expires.UnixMilli(), time.Now().UnixMilli()); err != nil {
		fail(w, http.StatusServiceUnavailable, "Registration service is temporarily unavailable")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusServiceUnavailable, "Registration service is temporarily unavailable")
		return
	}
	// Delivery is injected and happens after the challenge is durable. A failure
	// consumes that challenge; a later registration can issue a replacement.
	// Production enablement still requires a real sender and stable public routing.
	challengeHash := digest(challenge)
	if err = a.consumerChallengeSender.SendVerification(r.Context(), slug, email, challenge, expires); err != nil {
		_, _ = a.db.Exec(`UPDATE pos_consumer_verification_challenges SET consumed_at=? WHERE token_hash=? AND organization_id=? AND consumed_at IS NULL`, time.Now().UnixMilli(), challengeHash, organizationID)
		log.Print("consumer verification delivery failed")
		consumerAccepted(w)
		return
	}
	consumerAccepted(w)
}

func (a *App) consumerVerify(w http.ResponseWriter, r *http.Request, organizationID string) {
	var input consumerVerificationInput
	if body(r, &input) != nil || len(input.Token) != 64 || len(input.Password) < 12 || len(input.Password) > 128 {
		fail(w, http.StatusBadRequest, "Invalid verification challenge")
		return
	}
	if _, err := hex.DecodeString(input.Token); err != nil {
		fail(w, http.StatusBadRequest, "Invalid verification challenge")
		return
	}
	challengeHash := digest(input.Token)
	now := time.Now().UnixMilli()
	var active int
	err := a.db.QueryRow(`SELECT count(*) FROM pos_consumer_verification_challenges c JOIN pos_consumer_accounts a ON a.id=c.account_id AND a.organization_id=c.organization_id WHERE c.token_hash=? AND c.organization_id=? AND c.consumed_at IS NULL AND c.expires_at>? AND a.verified_at IS NULL AND a.disabled=0`, challengeHash, organizationID, now).Scan(&active)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Verification service is temporarily unavailable")
		return
	}
	if active != 1 {
		fail(w, http.StatusBadRequest, "Invalid or expired verification challenge")
		return
	}
	// Do expensive PBKDF2 work only after the opaque challenge is known to be active.
	// The transaction below rechecks atomically in case another request consumes it.
	password, err := a.hashConsumerPassword(input.Password)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Unable to set consumer password")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Verification service is temporarily unavailable")
		return
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE pos_consumer_verification_challenges SET consumed_at=? WHERE token_hash=? AND organization_id=? AND consumed_at IS NULL AND expires_at>?`, now, challengeHash, organizationID, now)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Verification service is temporarily unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		fail(w, http.StatusBadRequest, "Invalid or expired verification challenge")
		return
	}
	result, err = tx.Exec(`UPDATE pos_consumer_accounts SET password_hash=?,verified_at=?,updated_at=? WHERE id=(SELECT account_id FROM pos_consumer_verification_challenges WHERE token_hash=? AND organization_id=?) AND organization_id=? AND verified_at IS NULL AND disabled=0`, password, now, now, challengeHash, organizationID, organizationID)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Verification service is temporarily unavailable")
		return
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		fail(w, http.StatusBadRequest, "Invalid or expired verification challenge")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusServiceUnavailable, "Verification service is temporarily unavailable")
		return
	}
	ok(w, map[string]bool{"verified": true})
}

var dummyConsumerPasswordHash = func() string {
	hash, _ := passwordHash("not-a-consumer-password", "pos-consumer-dummy")
	return hash
}()

func (a *App) consumerLogin(w http.ResponseWriter, r *http.Request, organizationID string) {
	var input consumerLoginInput
	if body(r, &input) != nil || len(input.Password) == 0 || len(input.Password) > 128 {
		fail(w, http.StatusBadRequest, "Invalid sign-in data")
		return
	}
	email, validEmail := normalizeConsumerEmail(input.Email)
	if !validEmail {
		email = "invalid@example.invalid"
	}
	var accountID, encodedPassword string
	var verifiedAt sql.NullInt64
	var disabled int
	err := a.db.QueryRow(`SELECT id,password_hash,verified_at,disabled FROM pos_consumer_accounts WHERE organization_id=? AND normalized_email=?`, organizationID, email).Scan(&accountID, &encodedPassword, &verifiedAt, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		_ = a.consumerPasswordMatches(input.Password, dummyConsumerPasswordHash)
		fail(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Sign-in service is temporarily unavailable")
		return
	}
	passwordHashToCheck := encodedPassword
	if !verifiedAt.Valid || disabled != 0 || passwordHashToCheck == "" {
		passwordHashToCheck = dummyConsumerPasswordHash
	}
	validPassword := a.consumerPasswordMatches(input.Password, passwordHashToCheck)
	if !validEmail || !validPassword || !verifiedAt.Valid || disabled != 0 {
		fail(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}
	value := token()
	now := time.Now()
	expires := now.Add(consumerSessionLifetime)
	if _, err = a.db.Exec(`INSERT INTO pos_consumer_sessions(token_hash,account_id,organization_id,expires_at,created_at) VALUES(?,?,?,?,?)`, digest(value), accountID, organizationID, expires.UnixMilli(), now.UnixMilli()); err != nil {
		fail(w, http.StatusServiceUnavailable, "Sign-in service is temporarily unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: consumerSessionCookieName, Value: value, Path: "/api/pos/consumer/", MaxAge: int(consumerSessionLifetime.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	ok(w, map[string]bool{"signedIn": true})
}

func (a *App) consumerMe(w http.ResponseWriter, r *http.Request, organizationID string) {
	cookie, err := r.Cookie(consumerSessionCookieName)
	if err != nil || len(cookie.Value) != 64 {
		fail(w, http.StatusUnauthorized, "Please sign in")
		return
	}
	if _, err = hex.DecodeString(cookie.Value); err != nil {
		fail(w, http.StatusUnauthorized, "Please sign in")
		return
	}
	var identity consumerIdentity
	err = a.db.QueryRow(`SELECT a.normalized_email FROM pos_consumer_sessions s JOIN pos_consumer_accounts a ON a.id=s.account_id AND a.organization_id=s.organization_id WHERE s.token_hash=? AND s.organization_id=? AND s.expires_at>? AND s.revoked_at IS NULL AND a.verified_at IS NOT NULL AND a.disabled=0`, digest(cookie.Value), organizationID, time.Now().UnixMilli()).Scan(&identity.Email)
	if err != nil {
		fail(w, http.StatusUnauthorized, "Your consumer session has expired")
		return
	}
	ok(w, identity)
}

func (a *App) consumerLogout(w http.ResponseWriter, r *http.Request, organizationID string) {
	if cookie, err := r.Cookie(consumerSessionCookieName); err == nil && len(cookie.Value) == 64 {
		_, _ = a.db.Exec(`UPDATE pos_consumer_sessions SET revoked_at=? WHERE token_hash=? AND organization_id=? AND revoked_at IS NULL`, time.Now().UnixMilli(), digest(cookie.Value), organizationID)
	}
	http.SetCookie(w, &http.Cookie{Name: consumerSessionCookieName, Value: "", Path: "/api/pos/consumer/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	ok(w, map[string]bool{"signedOut": true})
}

func trustedProxyRequestSecure(r *http.Request, trusted []*net.IPNet) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	remote := net.ParseIP(host)
	if remote == nil {
		return false
	}
	for _, network := range trusted {
		if network != nil && network.Contains(remote) {
			return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
		}
	}
	return false
}

func parseTrustedProxyCIDRs(value string) []*net.IPNet {
	var networks []*net.IPNet
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		_, network, err := net.ParseCIDR(item)
		if err != nil {
			log.Printf("ignoring invalid trusted proxy CIDR")
			continue
		}
		networks = append(networks, network)
	}
	return networks
}
