package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

// This is the posCommand layout shipped before P06 added movementType, amount,
// and notes. Existing idempotency rows store a hash of this exact JSON shape.
type posCommandBeforeP06 struct {
	Action         string `json:"action"`
	RegisterID     string `json:"registerId"`
	ShiftID        string `json:"shiftId"`
	ItemID         string `json:"itemId"`
	SaleLineID     string `json:"saleLineId"`
	Reason         string `json:"reason"`
	Quantity       int    `json:"quantity"`
	OpeningCash    int64  `json:"openingCash"`
	ClosingCash    int64  `json:"closingCash"`
	Tender         int64  `json:"tender"`
	IdempotencyKey string `json:"idempotencyKey"`
}

func TestPOSBusinessReplayAcceptsPreP06RefundAndCheckoutFingerprints(t *testing.T) {
	f := newPOSRefundFixture(t, "cash")
	cases := []struct {
		name     string
		cookies  []*http.Cookie
		endpoint string
		request  posCommandBeforeP06
		response string
	}{
		{
			name:     "refund",
			cookies:  f.admin,
			endpoint: "/station/pos/refund",
			request:  posCommandBeforeP06{Action: "refund", SaleLineID: f.line, Reason: "Customer return", Quantity: 1, IdempotencyKey: "legacy-refund-key-01"},
			response: `{"refundId":"refund_legacy","refundNumber":"REF-LEGACY","refundAmount":34,"quantity":1}`,
		},
		{
			name:     "checkout",
			cookies:  f.front,
			endpoint: "/station/pos/checkout",
			request:  posCommandBeforeP06{Action: "checkout", ShiftID: f.shift, ItemID: f.item, Quantity: 1, Tender: 100000, IdempotencyKey: "legacy-checkout-key-01"},
			response: `{"saleId":"sale_legacy","receiptNumber":"POS-LEGACY","totalAmount":100,"changeAmount":99900}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldJSON, err := json.Marshal(tc.request)
			if err != nil {
				t.Fatal(err)
			}
			var current posCommand
			if err := json.Unmarshal(oldJSON, &current); err != nil {
				t.Fatal(err)
			}
			currentJSON, err := json.Marshal(current)
			if err != nil {
				t.Fatal(err)
			}
			if string(currentJSON) != string(oldJSON) {
				t.Fatalf("post-upgrade command changed old JSON fingerprint:\nold: %s\nnew: %s", oldJSON, currentJSON)
			}

			var userID string
			var sessionCookie *http.Cookie
			for _, cookie := range tc.cookies {
				if cookie.Name == cookieName+"_pos" {
					sessionCookie = cookie
					break
				}
			}
			if sessionCookie == nil {
				t.Fatal("missing POS session cookie")
			}
			if err := f.a.db.QueryRow(`SELECT user_id FROM station_sessions WHERE token_hash=?`, digest(sessionCookie.Value)).Scan(&userID); err != nil {
				t.Fatal(err)
			}
			oldHash := digest(userID + "\x00" + string(oldJSON))
			if _, err := f.a.db.Exec(`INSERT INTO idempotency_keys(id,organization_id,key,endpoint,request_hash,response_status,response_body) VALUES(?,?,?,?,?,200,?)`, "idem_legacy_"+tc.name, f.org, tc.request.IdempotencyKey, tc.endpoint, oldHash, tc.response); err != nil {
				t.Fatal(err)
			}

			w := request(f.a, http.MethodPost, "/api/stations/pos/business", tc.request, tc.cookies...)
			if w.Code != http.StatusOK {
				t.Fatalf("prior deployment retry returned %d: %s", w.Code, w.Body.String())
			}
			var got struct {
				Data any `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			var want any
			if err := json.Unmarshal([]byte(tc.response), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Data, want) {
				t.Fatalf("replayed response %#v, want %#v", got.Data, want)
			}
		})
	}
}
