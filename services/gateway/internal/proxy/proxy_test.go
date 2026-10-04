package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMarketSlug(t *testing.T) {
	cases := []struct {
		host, header, want string
	}{
		{"acme.localhost:8080", "", "acme"},
		{"groceries.example.com", "", "groceries"},
		{"localhost:8080", "", ""},
		{"127.0.0.1:8080", "", ""},
		{"localhost:8080", "demo", "demo"}, // header override wins
		{"acme.localhost:8080", "other", "other"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = c.host
		if c.header != "" {
			r.Header.Set("X-Tenant-Slug", c.header)
		}
		if got := marketSlug(r); got != c.want {
			t.Errorf("marketSlug(host=%q, header=%q) = %q, want %q", c.host, c.header, got, c.want)
		}
	}
}

func TestIsPublic(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/v1/tenants", true},
		{http.MethodPost, "/v1/auth/login", true},
		{http.MethodPost, "/v1/auth/register", true},
		{http.MethodPost, "/v1/psp/webhook", true},
		{http.MethodGet, "/v1/products", true},
		{http.MethodGet, "/v1/categories", true},
		{http.MethodGet, "/v1/shipping-methods", true},
		{http.MethodPost, "/v1/products", false},
		{http.MethodPost, "/v1/orders", false},
		{http.MethodPost, "/v1/payments", false},
		{http.MethodGet, "/v1/orders", false},
		{http.MethodGet, "/v1/me", false},
		{http.MethodGet, "/v1/shipments", false},
		// platform control plane always needs a JWT
		{http.MethodGet, "/v1/tenants", false},
		{http.MethodPatch, "/v1/tenants/123", false},
		{http.MethodGet, "/v1/platform/users", false},
		{http.MethodPost, "/v1/platform/users", false},
		{http.MethodPatch, "/v1/platform/users/123", false},
	}
	for _, c := range cases {
		if got := isPublic(c.method, c.path); got != c.want {
			t.Errorf("isPublic(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestIsTenantScopedPath(t *testing.T) {
	if isTenantScopedPath("/v1/auth/login") {
		t.Error("auth routes must work without a resolved market")
	}
	if isTenantScopedPath("/v1/tenants") || isTenantScopedPath("/v1/tenants/123") {
		t.Error("tenant lifecycle routes must work without a resolved market")
	}
	if isTenantScopedPath("/v1/platform/users") {
		t.Error("platform routes must work without a resolved market")
	}
	if !isTenantScopedPath("/v1/orders") {
		t.Error("orders require a resolved market")
	}
}

func TestPathExemptFromMarket(t *testing.T) {
	exempt := []string{"/v1/tenants", "/v1/tenants/123", "/v1/platform/users"}
	for _, p := range exempt {
		if !pathExemptFromMarket(p) {
			t.Errorf("pathExemptFromMarket(%q) = false, want true", p)
		}
	}
	blocked := []string{"/v1/auth/login", "/v1/orders", "/v1/products", "/v1/me"}
	for _, p := range blocked {
		if pathExemptFromMarket(p) {
			t.Errorf("pathExemptFromMarket(%q) = true, want false", p)
		}
	}
}
