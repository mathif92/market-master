package dispatch

import (
	"context"
	"strings"
	"testing"
)

func TestDeterministicRefs(t *testing.T) {
	// Retries with the same idempotency key must yield the same provider
	// reference — that is what makes provider calls retry-safe.
	req := Request{OrderID: "order-1", IdempotencyKey: "dispatch:order-1"}

	own := OwnFleet{}
	a, err := own.CreateDispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := own.CreateDispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a.ProviderRef != b.ProviderRef {
		t.Fatalf("own fleet refs differ across retries: %s != %s", a.ProviderRef, b.ProviderRef)
	}
	if !strings.HasPrefix(a.ProviderRef, "own-") {
		t.Fatalf("unexpected ref %q", a.ProviderRef)
	}

	tp := NewThirdParty("key")
	c, err := tp.CreateDispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	d, err := tp.CreateDispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if c.ProviderRef != d.ProviderRef {
		t.Fatalf("3pl refs differ across retries: %s != %s", c.ProviderRef, d.ProviderRef)
	}
	if c.TrackingURL == "" {
		t.Fatal("3pl must return a tracking url")
	}

	// different orders must not collide
	e, _ := own.CreateDispatch(context.Background(), Request{OrderID: "order-2", IdempotencyKey: "dispatch:order-2"})
	if e.ProviderRef == a.ProviderRef {
		t.Fatal("distinct orders must get distinct refs")
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry(OwnFleet{}, NewThirdParty("k"))
	if got := r.Get("third_party").Name(); got != "third_party" {
		t.Fatalf("got %q", got)
	}
	if got := r.Get("nope").Name(); got != "own_fleet" {
		t.Fatalf("unknown dispatcher must fall back to own_fleet, got %q", got)
	}
}

func TestCancelIsAlwaysSafe(t *testing.T) {
	if err := (OwnFleet{}).CancelDispatch(context.Background(), "own-x"); err != nil {
		t.Fatal(err)
	}
	if err := NewThirdParty("k").CancelDispatch(context.Background(), "fs-x"); err != nil {
		t.Fatal(err)
	}
}
