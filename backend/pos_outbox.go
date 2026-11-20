package main

import (
	"database/sql"
	"encoding/json"
)

type OutboxSaleLine struct {
	ItemID         string `json:"itemId"`
	Barcode        string `json:"barcode"`
	Name           string `json:"name"`
	UnitPrice      int64  `json:"unitPrice"`
	Quantity       int64  `json:"quantity"`
	DiscountAmount int64  `json:"discountAmount,omitempty"`
	LineTotal      int64  `json:"lineTotal"`
}

type OutboxSaleCompletedPayload struct {
	EventID        string           `json:"eventId"`
	SchemaVersion  int              `json:"schemaVersion"`
	OrganizationID string           `json:"organizationId"`
	SourceID       string           `json:"sourceId"`
	ReceiptNumber  string           `json:"receiptNumber"`
	TotalAmount    int64            `json:"totalAmount"`
	Subtotal       int64            `json:"subtotal,omitempty"`
	DiscountAmount int64            `json:"discountAmount,omitempty"`
	PaymentMethod  string           `json:"paymentMethod"`
	CustomerID     *string          `json:"customerId"`
	Lines          []OutboxSaleLine `json:"lines"`
	OccurredAt     string           `json:"occurredAt"`
}

type OutboxRefundLine struct {
	SaleLineID string `json:"saleLineId"`
	ItemID     string `json:"itemId"`
	Quantity   int64  `json:"quantity"`
	Amount     int64  `json:"amount"`
}

type OutboxSaleRefundedPayload struct {
	EventID        string             `json:"eventId"`
	SchemaVersion  int                `json:"schemaVersion"`
	OrganizationID string             `json:"organizationId"`
	SourceID       string             `json:"sourceId"`
	OriginalSaleID string             `json:"originalSaleId"`
	RefundNumber   string             `json:"refundNumber"`
	RefundAmount   int64              `json:"refundAmount"`
	TotalAmount    int64              `json:"totalAmount"`
	Reason         string             `json:"reason"`
	Lines          []OutboxRefundLine `json:"lines"`
	OccurredAt     string             `json:"occurredAt"`
}

func emitOutboxEvent(tx *sql.Tx, orgID, eventType, eventID string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		"INSERT INTO outbox_events(id, organization_id, event_type, payload, status, retry_count) VALUES(?,?,?,?, 'pending', 0)",
		eventID, orgID, eventType, string(encoded),
	)
	return err
}
