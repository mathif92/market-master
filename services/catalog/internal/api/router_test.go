package api

import (
	"log/slog"
	"testing"
	"time"

	"market-master/pkg/authn"
	"market-master/pkg/idempotency"
	"market-master/services/catalog/internal/store"
)

// TestRouterConstructs guards the chi convention: every r.Use(...) must be
// registered before routes — chi panics when middleware is added after a
// route, which would crash-loop the service in Docker instead of failing
// here. Constructing the router in CI makes that a red test.
func TestRouterConstructs(t *testing.T) {
	signer := authn.NewSigner("test-secret", time.Minute)
	idem := idempotency.NewStore(nil, time.Minute)
	srv := NewServer(store.New(nil), signer, idem, "test-ingest-secret", slog.Default())
	h := srv.Router()
	if h == nil {
		t.Fatal("Router() returned nil")
	}
}

func TestTenantExempt(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/v1/inventory/webhook", true},
		{"/v1/products", false},
		{"/v1/campaigns/active", false},
		{"/v1/orders", false},
	}
	for _, c := range cases {
		if got := tenantExempt(c.path); got != c.want {
			t.Errorf("tenantExempt(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
