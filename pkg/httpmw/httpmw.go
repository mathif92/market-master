package httpmw

import (
	"net/http"

	"market-master/pkg/authn"
	"market-master/pkg/httpx"
	"market-master/pkg/tenantctx"
)

// BindTenant rejects tenant-scoped tokens used against another market's
// host. Platform admins bypass the check (their token carries no tenant).
func BindTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := authn.ClaimsFrom(r.Context())
		if claims != nil && claims.TenantID != "" && claims.TenantID != tenantctx.From(r.Context()) {
			httpx.Err(w, http.StatusForbidden, "tenant_mismatch",
				"token does not belong to this market")
			return
		}
		next.ServeHTTP(w, r)
	})
}
