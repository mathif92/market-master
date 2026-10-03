package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"market-master/pkg/authn"
	"market-master/pkg/evt"
	"market-master/pkg/httpmw"
	"market-master/pkg/httpx"
	"market-master/pkg/outbox"
	"market-master/pkg/tenantctx"
	"market-master/services/logistics/internal/dispatch"
	"market-master/services/logistics/internal/store"
)

type Server struct {
	store    *store.Store
	signer   *authn.Signer
	registry *dispatch.Registry
	outbox   outbox.Writer
	log      *slog.Logger
}

func NewServer(st *store.Store, signer *authn.Signer, reg *dispatch.Registry,
	log *slog.Logger) *Server {
	return &Server{store: st, signer: signer, registry: reg, log: log}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(tenantctx.Middleware)
	r.Use(requireTenant)

	r.Get("/v1/shipping-methods", s.listMethods) // public (checkout UX)

	r.Group(func(r chi.Router) {
		r.Use(s.signer.Middleware)
		r.Use(httpmw.BindTenant)

		r.Get("/v1/shipments", s.listShipments)
		r.Get("/v1/shipments/{id}", s.getShipment)
		r.With(authn.RequireRole(authn.RoleTenantAdmin, authn.RoleStaff)).
			Post("/v1/shipments/{id}/deliver", s.deliverShipment)
		r.With(authn.RequireRole(authn.RoleTenantAdmin, authn.RoleStaff)).
			Post("/v1/shipments/{id}/cancel", s.cancelShipment)
		r.With(authn.RequireRole(authn.RoleTenantAdmin)).
			Post("/v1/shipping-methods", s.createMethod)
		r.With(authn.RequireRole(authn.RoleTenantAdmin)).
			Delete("/v1/shipping-methods/{id}", s.deleteMethod)
	})
	return r
}

func requireTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tenantctx.FromRequest(r) == "" {
			httpx.Err(w, http.StatusBadRequest, "tenant_required", "could not resolve market from host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) listMethods(w http.ResponseWriter, r *http.Request) {
	methods, err := s.store.ListMethods(r.Context(), tenantctx.FromRequest(r))
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"shipping_methods": methods})
}

type methodReq struct {
	Code       string          `json:"code"`
	Name       string          `json:"name"`
	Dispatcher string          `json:"dispatcher"`
	IsDefault  bool            `json:"is_default"`
	Config     json.RawMessage `json:"config"`
}

func (s *Server) createMethod(w http.ResponseWriter, r *http.Request) {
	var req methodReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Code == "" || req.Name == "" {
		httpx.Err(w, http.StatusBadRequest, "invalid_method", "code and name are required")
		return
	}
	if req.Dispatcher == "" {
		req.Dispatcher = "own_fleet"
	}
	if req.Dispatcher != "own_fleet" && req.Dispatcher != "third_party" {
		httpx.Err(w, http.StatusBadRequest, "invalid_dispatcher", "dispatcher must be own_fleet or third_party")
		return
	}
	m, err := s.store.CreateMethod(r.Context(), tenantctx.FromRequest(r), store.ShippingMethod{
		Code: req.Code, Name: req.Name, Dispatcher: req.Dispatcher,
		IsDefault: req.IsDefault, Config: req.Config,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, m)
}

func (s *Server) deleteMethod(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteMethod(r.Context(), tenantctx.FromRequest(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getShipment(w http.ResponseWriter, r *http.Request) {
	claims, _ := authn.ClaimsFrom(r.Context())
	sh, err := s.store.GetShipment(r.Context(), tenantctx.FromRequest(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !isStaff(claims) && sh.CustomerID != claims.Subject {
		httpx.Err(w, http.StatusForbidden, "forbidden", "not your shipment")
		return
	}
	httpx.JSON(w, http.StatusOK, sh)
}

func (s *Server) listShipments(w http.ResponseWriter, r *http.Request) {
	claims, _ := authn.ClaimsFrom(r.Context())
	f := store.ShipmentFilter{OrderID: r.URL.Query().Get("order_id")}
	if !isStaff(claims) {
		f.CustomerID = claims.Subject
	}
	shipments, err := s.store.ListShipments(r.Context(), tenantctx.FromRequest(r), f)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"shipments": shipments})
}

// deliverShipment simulates/attempts proof of delivery (own fleet) or a
// provider "delivered" webhook equivalent; it emits shipment.delivered.
func (s *Server) deliverShipment(w http.ResponseWriter, r *http.Request) {
	tenant := tenantctx.FromRequest(r)
	id := chi.URLParam(r, "id")
	sh, already, err := s.store.MarkDelivered(r.Context(), tenant, id,
		func(tx pgx.Tx, sh store.Shipment) error {
			return s.emitOrderEvent(r.Context(), tx, tenant, evt.ShipmentDelivered, sh,
				map[string]any{
					"shipment_id": sh.ID, "order_id": sh.OrderID,
					"provider_ref": sh.ProviderRef,
				})
		})
	if err != nil {
		writeErr(w, err)
		return
	}
	if already {
		httpx.JSON(w, http.StatusOK, map[string]any{"shipment": sh})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"shipment": sh})
}

func (s *Server) cancelShipment(w http.ResponseWriter, r *http.Request) {
	tenant := tenantctx.FromRequest(r)
	id := chi.URLParam(r, "id")
	sh, err := s.store.GetShipment(r.Context(), tenant, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if sh.Status == "cancelled" {
		httpx.JSON(w, http.StatusOK, map[string]any{"shipment": sh}) // idempotent
		return
	}
	if sh.Status != "dispatched" && sh.Status != "in_transit" {
		httpx.Err(w, http.StatusConflict, "not_cancellable", "shipment already "+sh.Status)
		return
	}
	if err := s.registry.Get(sh.Dispatcher).CancelDispatch(r.Context(), sh.ProviderRef); err != nil {
		s.log.Error("provider cancel failed", "err", err, "shipment", sh.ID)
		httpx.Err(w, http.StatusBadGateway, "provider_unavailable", "provider error, retry later")
		return
	}
	out, already, err := s.store.CancelShipment(r.Context(), tenant, id, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if already {
		httpx.JSON(w, http.StatusOK, map[string]any{"shipment": out})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"shipment": out})
}

func (s *Server) emitOrderEvent(ctx context.Context, tx pgx.Tx,
	tenant, eventType string, sh store.Shipment, payload map[string]any) error {
	env, err := evt.New(eventType, tenant, sh.OrderID, "shipment", sh.ID, payload)
	if err != nil {
		return err
	}
	return s.outbox.Insert(ctx, tx, evt.TopicOrders, sh.OrderID, env)
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Err(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, store.ErrCodeTaken):
		httpx.Err(w, http.StatusConflict, "code_taken", "shipping method code already exists")
	case errors.Is(err, store.ErrNotDeliverable):
		httpx.Err(w, http.StatusConflict, "not_deliverable", err.Error())
	case errors.Is(err, store.ErrNotCancellable):
		httpx.Err(w, http.StatusConflict, "not_cancellable", err.Error())
	default:
		httpx.Internal(w, err)
	}
}

func isStaff(c *authn.Claims) bool {
	return c != nil && (c.Role == authn.RoleTenantAdmin || c.Role == authn.RoleStaff ||
		c.Role == authn.RolePlatformAdmin)
}
