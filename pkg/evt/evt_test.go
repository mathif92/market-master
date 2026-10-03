package evt

import (
	"encoding/json"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	env, err := New(OrderPaid, "tenant-1", "order-1", "payment", "pay-1",
		map[string]any{"amount_cents": int64(1500)})
	if err != nil {
		t.Fatal(err)
	}
	if env.Key() != "order-1" {
		t.Fatalf("Key() = %q, want order-1 (partition key must be order_id)", env.Key())
	}
	b, err := Encode(env)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if back.EventID != env.EventID || back.EventType != OrderPaid ||
		back.TenantID != "tenant-1" || back.OrderID != "order-1" {
		t.Fatalf("round trip mismatch: %+v", back)
	}
	if back.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %d", back.SchemaVersion)
	}
	var payload map[string]any
	if err := back.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["amount_cents"].(float64) != 1500 {
		t.Fatalf("payload lost: %v", payload)
	}
}

func TestDecodeRejectsIncomplete(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"event_id":"x","event_type":"y"}`, // missing order_id
		`not json`,
	} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Errorf("Decode(%s) should fail", raw)
		}
	}
}

func TestEnvelopeOrderingFields(t *testing.T) {
	env, _ := New(OrderCreated, "t", "o", "order", "o", struct{}{})
	raw, _ := json.Marshal(env)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"event_id", "event_type", "occurred_at", "tenant_id",
		"order_id", "aggregate", "schema_version", "payload"} {
		if _, ok := m[k]; !ok {
			t.Errorf("envelope missing %q", k)
		}
	}
}
