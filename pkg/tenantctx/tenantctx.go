package tenantctx

import (
	"context"
	"net/http"
)

type ctxKey struct{}

const (
	HeaderTenantID = "X-Tenant-ID"
	HeaderUserID   = "X-User-ID"
	HeaderUserRole = "X-User-Role"
)

func With(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, tenantID)
}

func From(ctx context.Context) string {
	v, _ := ctx.Value(ctxKey{}).(string)
	return v
}

// Middleware extracts the tenant id injected by the gateway and exposes it
// on the request context. Downstream DB access must go through psql helpers
// scoped with this value (enforced by Postgres RLS on domain tables).
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(With(r.Context(), r.Header.Get(HeaderTenantID))))
	})
}

// FromRequest is a convenience for handlers that need the raw header.
func FromRequest(r *http.Request) string { return r.Header.Get(HeaderTenantID) }
