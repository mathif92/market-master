package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"market-master/pkg/authn"
	"market-master/pkg/httpx"
	"market-master/pkg/idempotency"
	"market-master/pkg/tenantctx"
	"market-master/services/identity/internal/store"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

type Server struct {
	store  *store.Store
	signer *authn.Signer
	idem   *idempotency.Store
	log    *slog.Logger
}

func NewServer(st *store.Store, signer *authn.Signer, idem *idempotency.Store, log *slog.Logger) *Server {
	return &Server{store: st, signer: signer, idem: idem, log: log}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	r.With(s.idem.Middleware("identity.create_tenant", false)).
		Post("/v1/tenants", s.createTenant)
	r.Post("/v1/auth/login", s.login)
	r.Post("/v1/auth/refresh", s.refresh)
	r.With(tenantctx.Middleware).Post("/v1/auth/register", s.register)

	r.Group(func(r chi.Router) {
		r.Use(s.signer.Middleware)
		r.Use(tenantctx.Middleware)
		r.Use(bindTenant)

		r.Get("/v1/me", s.me)
		r.With(authn.RequireRole(authn.RoleTenantAdmin, authn.RoleStaff)).
			Get("/v1/users", s.listUsers)
		r.With(authn.RequireRole(authn.RoleTenantAdmin)).
			Post("/v1/users", s.createUser)
	})
	return r
}

// bindTenant rejects tenant-scoped tokens used against another market's
// host (platform admins bypass: their token carries no tenant).
func bindTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := authn.ClaimsFrom(r.Context())
		if claims != nil && claims.TenantID != "" && claims.TenantID != tenantctx.FromRequest(r) {
			httpx.Err(w, http.StatusForbidden, "tenant_mismatch", "token does not belong to this market")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type createTenantReq struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Admin struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	} `json:"admin"`
}

func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) {
	var req createTenantReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !slugRe.MatchString(req.Slug) {
		httpx.Err(w, http.StatusBadRequest, "invalid_slug", "slug must be 3-40 chars of a-z, 0-9, hyphen")
		return
	}
	if req.Name == "" {
		httpx.Err(w, http.StatusBadRequest, "invalid_name", "name is required")
		return
	}
	if len(req.Admin.Password) < 8 {
		httpx.Err(w, http.StatusBadRequest, "weak_password", "password must be at least 8 characters")
		return
	}
	hash, err := store.HashPassword(req.Admin.Password)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	resv := idempotency.From(r.Context())
	var out struct {
		Tenant      store.Tenant `json:"tenant"`
		AdminUserID string       `json:"admin_user_id"`
	}
	_, _, err = s.store.CreateTenantWithAdmin(r.Context(),
		req.Slug, req.Name, req.Admin.Email, hash,
		func(tx pgx.Tx, t store.Tenant, u store.User) error {
			out.Tenant = t
			out.AdminUserID = u.ID
			if resv == nil {
				return nil
			}
			body, merr := json.Marshal(out)
			if merr != nil {
				return merr
			}
			return resv.CompleteInTx(r.Context(), tx, http.StatusCreated, body)
		})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSlugTaken):
			httpx.Err(w, http.StatusConflict, "slug_taken", "tenant slug already exists")
		case errors.Is(err, store.ErrEmailTaken):
			httpx.Err(w, http.StatusConflict, "email_taken", "admin email already registered")
		default:
			httpx.Internal(w, err)
		}
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantSlug string `json:"tenant_slug"`
		Email      string `json:"email"`
		Password   string `json:"password"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	tenantID := ""
	if req.TenantSlug != "" {
		t, err := s.store.GetTenantBySlug(r.Context(), req.TenantSlug)
		if err != nil || t.Status != "active" {
			httpx.Err(w, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
			return
		}
		tenantID = t.ID
	}
	u, hash, err := s.store.GetUserByEmail(r.Context(), tenantID, req.Email)
	if err != nil || !store.CheckPassword(hash, req.Password) || u.Status != "active" {
		httpx.Err(w, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
		return
	}
	s.writeTokens(w, u)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := httpx.Decode(r, &req); err != nil || req.RefreshToken == "" {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", "refresh_token is required")
		return
	}
	claims, err := s.signer.ParseAny(req.RefreshToken)
	if err != nil || claims.Type != "refresh" {
		httpx.Err(w, http.StatusUnauthorized, "invalid_token", "invalid refresh token")
		return
	}
	u, err := s.store.GetUserByID(r.Context(), claims.Subject)
	if err != nil || u.Status != "active" {
		httpx.Err(w, http.StatusUnauthorized, "invalid_token", "invalid refresh token")
		return
	}
	s.writeTokens(w, u)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	tenantID := tenantctx.FromRequest(r)
	if tenantID == "" {
		httpx.Err(w, http.StatusBadRequest, "tenant_required", "unknown market for this host")
		return
	}
	if len(req.Password) < 8 {
		httpx.Err(w, http.StatusBadRequest, "weak_password", "password must be at least 8 characters")
		return
	}
	hash, err := store.HashPassword(req.Password)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	u, err := s.store.CreateUser(r.Context(), tenantID, req.Email, hash, authn.RoleCustomer)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrEmailTaken):
			httpx.Err(w, http.StatusConflict, "email_taken", "email already registered")
		default:
			httpx.Internal(w, err)
		}
		return
	}
	httpx.JSON(w, http.StatusCreated, u)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	claims, _ := authn.ClaimsFrom(r.Context())
	u, err := s.store.GetUserByID(r.Context(), claims.Subject)
	if err != nil {
		httpx.Err(w, http.StatusUnauthorized, "unknown_user", "user no longer exists")
		return
	}
	httpx.JSON(w, http.StatusOK, u)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context(), tenantctx.FromRequest(r))
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	if users == nil {
		users = []store.User{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	switch req.Role {
	case authn.RoleTenantAdmin, authn.RoleStaff, authn.RoleCustomer:
	default:
		httpx.Err(w, http.StatusBadRequest, "invalid_role", "role must be tenant_admin, staff or customer")
		return
	}
	if len(req.Password) < 8 {
		httpx.Err(w, http.StatusBadRequest, "weak_password", "password must be at least 8 characters")
		return
	}
	hash, err := store.HashPassword(req.Password)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	u, err := s.store.CreateUser(r.Context(), tenantctx.FromRequest(r), req.Email, hash, req.Role)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrEmailTaken):
			httpx.Err(w, http.StatusConflict, "email_taken", "email already registered")
		default:
			httpx.Internal(w, err)
		}
		return
	}
	httpx.JSON(w, http.StatusCreated, u)
}

func (s *Server) writeTokens(w http.ResponseWriter, u store.User) {
	access, err := s.signer.Sign(u.ID, u.TenantID, u.Role, "access", time.Hour)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	refresh, err := s.signer.Sign(u.ID, u.TenantID, u.Role, "refresh", 30*24*time.Hour)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"user":          u,
	})
}
