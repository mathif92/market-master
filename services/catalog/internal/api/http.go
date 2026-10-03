package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"market-master/pkg/authn"
	"market-master/pkg/httpmw"
	"market-master/pkg/httpx"
	"market-master/pkg/tenantctx"
	"market-master/services/catalog/internal/store"
)

type Server struct {
	store  *store.Store
	signer *authn.Signer
	log    *slog.Logger
}

func NewServer(st *store.Store, signer *authn.Signer, log *slog.Logger) *Server {
	return &Server{store: st, signer: signer, log: log}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(tenantctx.Middleware)
	r.Use(requireTenant)

	// public browsing
	r.Get("/v1/categories", s.listCategories)
	r.Get("/v1/categories/{id}", s.getCategory)
	r.Get("/v1/products", s.listProducts)
	r.Get("/v1/products/{id}", s.getProduct)

	// writes require an authenticated tenant-scoped principal
	r.Group(func(r chi.Router) {
		r.Use(s.signer.Middleware)
		r.Use(httpmw.BindTenant)

		staff := authn.RequireRole(authn.RoleTenantAdmin, authn.RoleStaff)

		r.With(staff).Post("/v1/categories", s.createCategory)
		r.With(staff).Patch("/v1/categories/{id}", s.updateCategory)
		r.With(staff).Delete("/v1/categories/{id}", s.deleteCategory)

		r.With(staff).Post("/v1/products", s.createProduct)
		r.With(staff).Patch("/v1/products/{id}", s.updateProduct)
	})
	return r
}

func requireTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tenantctx.FromRequest(r) == "" {
			httpx.Err(w, http.StatusBadRequest, "tenant_required",
				"could not resolve market from request host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) listCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.store.ListCategories(r.Context(), tenantctx.FromRequest(r))
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"categories": cats})
}

func (s *Server) getCategory(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetCategory(r.Context(), tenantctx.FromRequest(r), chi.URLParam(r, "id"))
	writeStoreErr(w, err)
	if err == nil {
		httpx.JSON(w, http.StatusOK, c)
	}
}

type categoryReq struct {
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	ParentID string `json:"parent_id"`
}

func (s *Server) createCategory(w http.ResponseWriter, r *http.Request) {
	var req categoryReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Name == "" {
		httpx.Err(w, http.StatusBadRequest, "invalid_name", "name is required")
		return
	}
	slug := req.Slug
	if slug == "" {
		slug = store.Slugify(req.Name)
	}
	c, err := s.store.CreateCategory(r.Context(), tenantctx.FromRequest(r), req.ParentID, req.Name, slug)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, c)
}

func (s *Server) updateCategory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tenant := tenantctx.FromRequest(r)
	existing, err := s.store.GetCategory(r.Context(), tenant, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var req categoryReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Name == "" {
		req.Name = existing.Name
	}
	c, err := s.store.UpdateCategory(r.Context(), tenant, id, req.Name, req.ParentID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (s *Server) deleteCategory(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteCategory(r.Context(), tenantctx.FromRequest(r), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listProducts(w http.ResponseWriter, r *http.Request) {
	products, err := s.store.ListProducts(r.Context(), tenantctx.FromRequest(r), store.ProductFilter{
		CategoryID: r.URL.Query().Get("category_id"),
		Status:     r.URL.Query().Get("status"),
	})
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"products": products})
}

func (s *Server) getProduct(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProduct(r.Context(), tenantctx.FromRequest(r), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

type productReq struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	PriceCents  *int64 `json:"price_cents"`
	Currency    string `json:"currency"`
	CategoryID  string `json:"category_id"`
	Status      string `json:"status"`
}

func (s *Server) createProduct(w http.ResponseWriter, r *http.Request) {
	var req productReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Name == "" || req.PriceCents == nil || *req.PriceCents < 0 {
		httpx.Err(w, http.StatusBadRequest, "invalid_product", "name and non-negative price_cents are required")
		return
	}
	slug := req.Slug
	if slug == "" {
		slug = store.Slugify(req.Name)
	}
	status := req.Status
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "archived" {
		httpx.Err(w, http.StatusBadRequest, "invalid_status", "status must be active or archived")
		return
	}
	currency := req.Currency
	if currency == "" {
		currency = "USD"
	}
	p, err := s.store.CreateProduct(r.Context(), tenantctx.FromRequest(r), store.Product{
		CategoryID:  req.CategoryID,
		Name:        req.Name,
		Slug:        slug,
		Description: req.Description,
		PriceCents:  *req.PriceCents,
		Currency:    currency,
		Status:      status,
	})
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, p)
}

func (s *Server) updateProduct(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tenant := tenantctx.FromRequest(r)
	existing, err := s.store.GetProduct(r.Context(), tenant, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var req productReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	patch := store.Product{
		CategoryID:  req.CategoryID,
		Name:        req.Name,
		Description: req.Description,
		PriceCents:  -1, // negative = unchanged (CASE in SQL)
		Status:      req.Status,
	}
	if req.PriceCents != nil {
		patch.PriceCents = *req.PriceCents
	}
	if patch.Name == "" {
		patch.Name = existing.Name
	}
	p, err := s.store.UpdateProduct(r.Context(), tenant, id, patch)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		httpx.Err(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, store.ErrSlugTaken):
		httpx.Err(w, http.StatusConflict, "slug_taken", "slug already exists in this market")
	case errors.Is(err, store.ErrHasProducts):
		httpx.Err(w, http.StatusConflict, "category_not_empty", "category still contains products")
	case errors.Is(err, store.ErrBadParent), errors.Is(err, store.ErrParentCycle):
		httpx.Err(w, http.StatusUnprocessableEntity, "invalid_parent", err.Error())
	default:
		httpx.Internal(w, err)
	}
}
