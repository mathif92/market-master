package evt

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	TopicOrders = "market.orders.v1"

	OrderCreated           = "order.created"
	OrderPaymentAuthorized = "order.payment_authorized"
	OrderPaymentFailed     = "order.payment_failed"
	OrderPaid              = "order.paid"
	OrderCancelled         = "order.cancelled"
	ShipmentDispatched     = "shipment.dispatched"
	ShipmentDelivered      = "shipment.delivered"

	SchemaVersion = 1
)

// Envelope is the schema-versioned event carried on the orders topic.
// Every event on TopicOrders must carry OrderID: it is the Kafka message
// key, so all events of one order are hash-partitioned to one partition.
type Envelope struct {
	EventID       string          `json:"event_id"`
	EventType     string          `json:"event_type"`
	OccurredAt    time.Time       `json:"occurred_at"`
	TenantID      string          `json:"tenant_id"`
	OrderID       string          `json:"order_id"`
	Aggregate     Aggregate       `json:"aggregate"`
	SchemaVersion int             `json:"schema_version"`
	Payload       json.RawMessage `json:"payload"`
}

type Aggregate struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// DLQTopic returns the dead-letter topic for a consumer group.
func DLQTopic(group string) string {
	return fmt.Sprintf("market.orders.dlq.%s", group)
}

func New(eventType, tenantID, orderID, aggType, aggID string, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal payload: %w", err)
	}
	return Envelope{
		EventID:       newID(),
		EventType:     eventType,
		OccurredAt:    time.Now().UTC(),
		TenantID:      tenantID,
		OrderID:       orderID,
		Aggregate:     Aggregate{Type: aggType, ID: aggID},
		SchemaVersion: SchemaVersion,
		Payload:       raw,
	}, nil
}

func Encode(e Envelope) ([]byte, error) { return json.Marshal(e) }

func Decode(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Envelope{}, fmt.Errorf("unmarshal envelope: %w", err)
	}
	if e.EventID == "" || e.EventType == "" || e.OrderID == "" {
		return Envelope{}, fmt.Errorf("invalid envelope: missing event_id, event_type or order_id")
	}
	return e, nil
}

func (e Envelope) DecodePayload(v any) error {
	return json.Unmarshal(e.Payload, v)
}

// Key is the Kafka partition key: always the order id.
func (e Envelope) Key() string { return e.OrderID }
