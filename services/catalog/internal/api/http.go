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
	"market-master/pkg/idempotency"
	"market-master/pkg/tenantctx"
	"market-master/services/catalog/internal/store"
)

type Server struct {
	store        *store.Store
	signer       *authn.Signer
	idem         *idempotency.Store
	ingestSecret string
	log          *slog.Logger
}

func NewServer(st *store.Store, signer *authn.Signer, idem *idempotency.Store,
	ingestSecret string, log *slog.Logger) *Server {
	return &Server{store: st, signer: signer, idem: idem, ingestSecret: ingestSecret, log: log}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(tenantctx.Middleware)
	r.Use(requireTenant)

	// HMAC-signed ingestion: the market comes from the payload, so the
	// tenant requirement is exempted for this path in requireTenant.
	r.Post("/v1/inventory/webhook", s.ingestWebhook)

	// public browsing
	r.Get("/v1/categories", s.listCategories)
	r.Get("/v1/categories/{id}", s.getCategory)
	r.Get("/v1/products", s.listProducts)
	r.Get("/v1/products/{id}", s.getProduct)
	// live campaign banner (tenant from host; no JWT — the shop is public)
	r.Get("/v1/campaigns/active", s.listActiveCampaigns)

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

		// campaigns: create + code minting are not naturally idempotent
		// (Idempotency-Key required, completed inside the business tx)
		r.With(staff, s.idem.Middleware("campaign.create", true)).
			Post("/v1/campaigns", s.createCampaign)
		r.With(staff).Get("/v1/campaigns", s.listCampaigns)
		r.With(staff).Get("/v1/campaigns/{id}", s.getCampaign)
		r.With(staff).Put("/v1/campaigns/{id}", s.updateCampaign)
		r.With(staff).Delete("/v1/campaigns/{id}", s.archiveCampaign)
		r.With(staff, s.idem.Middleware("campaign.codes", true)).
			Post("/v1/campaigns/{id}/codes", s.generateCodes)
		r.With(staff).Get("/v1/campaigns/{id}/codes", s.listCodes)
		r.With(staff).Delete("/v1/campaigns/codes/{code_id}", s.deleteCode)

		// inventory: ingestion + stock inspection
		r.With(staff, s.idem.Middleware("stock.ingest", true)).
			Post("/v1/inventory/ingest", s.ingestStock)
		r.With(staff).Post("/v1/inventory/upload", s.uploadStock)
		r.With(staff).Get("/v1/inventory/levels", s.listStockLevels)
		r.With(staff).Get("/v1/inventory/movements", s.listStockMovements)
		r.With(staff).Patch("/v1/inventory/levels/{product_id}", s.patchLevel)
	})
	return r
}

// tenantExempt lists paths that resolve their market themselves instead of
// from the request host. Keeping exemptions in one pure function makes the
// routing rules unit-testable (mirrors the gateway's isPublic/tenant
// exemptions). ALL r.Use(...) calls must precede route registration —
// chi panics when middleware is added after routes.
func tenantExempt(path string) bool {
	// The ingest webhook authenticates by HMAC and carries the market
	// in its signed payload; no host/tenant context is required.
	return path == "/v1/inventory/webhook"
}

func requireTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tenantExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
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
	tenant := tenantctx.FromRequest(r)
	id := chi.URLParam(r, "id")
	p, err := s.store.GetProduct(r.Context(), tenant, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	// Attach stock so the shop can gate add-to-cart: tracked products get
	// their level, untracked ones stock=null (unlimited).
	stock, err := s.store.GetStockLevel(r.Context(), tenant, id)
	if errors.Is(err, store.ErrNotFound) {
		stock = store.StockLevel{}
	} else if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		store.Product
		Stock *store.StockLevel `json:"stock"`
	}{Product: p, Stock: stockForJSON(stock)})
}

// stockForJSON returns nil for untracked products (JSON null).
func stockForJSON(lv store.StockLevel) *store.StockLevel {
	if !lv.Tracked {
		return nil
	}
	return &lv
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
