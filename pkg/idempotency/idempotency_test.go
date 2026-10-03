package idempotency

import "testing"

func TestHashStableAndDistinct(t *testing.T) {
	h1 := Hash("POST", "/v1/orders", []byte(`{"a":1}`))
	h2 := Hash("POST", "/v1/orders", []byte(`{"a":1}`))
	if h1 != h2 {
		t.Fatal("identical requests must hash identically")
	}
	if Hash("POST", "/v1/orders", []byte(`{"a":2}`)) == h1 {
		t.Fatal("different body must hash differently")
	}
	if Hash("PUT", "/v1/orders", []byte(`{"a":1}`)) == h1 {
		t.Fatal("different method must hash differently")
	}
	if Hash("POST", "/v1/payments", []byte(`{"a":1}`)) == h1 {
		t.Fatal("different path must hash differently")
	}
}
