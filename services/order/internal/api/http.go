package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	catalogv1 "market-master/gen/catalog/v1"
	"market-master/pkg/authn"
	"market-master/pkg/evt"
	"market-master/pkg/grpcx"
	"market-master/pkg/httpmw"
	"market-master/pkg/httpx"
	"market-master/pkg/idempotency"
	"market-master/pkg/outbox"
	"market-master/pkg/tenantctx"
	"market-master/services/order/internal/store"
)

type Server struct {
	store   *store.Store
	signer  *authn.Signer
	idem    *idempotency.Store
	outbox  outbox.Writer
	catalog catalogv1.CatalogServiceClient
	log     *slog.Logger
}

func NewServer(st *store.Store, signer *authn.Signer, idem *idempotency.Store,
	catalog catalogv1.CatalogServiceClient, log *slog.Logger) *Server {
	return &Server{store: st, signer: signer, idem: idem, catalog: catalog, log: log}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Group(func(r chi.Router) {
		r.Use(s.signer.Middleware)
		r.Use(tenantctx.Middleware)
		r.Use(httpmw.BindTenant)
		r.Use(requireTenant)

		r.With(s.idem.Middleware("order.create", true)).Post("/v1/orders", s.createOrder)
		r.Get("/v1/orders", s.listOrders)
		r.With(authn.RequireRole(authn.RoleTenantAdmin, authn.RoleStaff)).
			Get("/v1/orders/all", s.listAllOrders)
		r.Get("/v1/orders/{id}", s.getOrder)
		r.Post("/v1/orders/{id}/cancel", s.cancelOrder)
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

type createOrderReq struct {
	Lines []struct {
		ProductID string `json:"product_id"`
		Quantity  int    `json:"quantity"`
	} `json:"lines"`
	Shipping struct {
		RecipientName string `json:"recipient_name"`
		Line1         string `json:"line1"`
		City          string `json:"city"`
		Country       string `json:"country"`
		PostalCode    string `json:"postal_code"`
	} `json:"shipping"`
	ShippingMethodCode string `json:"shipping_method_code"`
}

func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderReq
	if err := httpx.Decode(r, &req); err != nil {
		s.fail(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if len(req.Lines) == 0 {
		s.fail(w, http.StatusBadRequest, "empty_order", "at least one line is required")
		return
	}
	if req.Shipping.RecipientName == "" || req.Shipping.Line1 == "" || req.Shipping.Country == "" {
		s.fail(w, http.StatusBadRequest, "invalid_shipping",
			"recipient_name, line1 and country are required")
		return
	}
	claims, _ := authn.ClaimsFrom(r.Context())
	tenant := tenantctx.FromRequest(r)
	userID := claims.Subject

	// One internal gRPC round-trip: availability check + price snapshot.
	in := make([]*catalogv1.OrderLineInput, 0, len(req.Lines))
	for _, l := range req.Lines {
		in = append(in, &catalogv1.OrderLineInput{ProductId: l.ProductID, Quantity: int32(l.Quantity)})
	}
	gctx, cancel := context.WithTimeout(grpcx.WithOutgoing(r.Context(), tenant, userID), 10*time.Second)
	defer cancel()
	resp, err := s.catalog.ValidateOrderLines(gctx, &catalogv1.ValidateOrderLinesRequest{
		TenantId: tenant, Lines: in,
	})
	if err != nil {
		s.log.Error("catalog validation failed", "err", err)
		s.fail(w, http.StatusServiceUnavailable, "catalog_unavailable",
			"could not validate order lines, retry later")
		return
	}
	if !resp.GetValid() {
		s.fail(w, http.StatusUnprocessableEntity, "invalid_lines", resp.GetErrorMessage())
		return
	}
	if len(resp.GetLines()) == 0 {
		s.fail(w, http.StatusUnprocessableEntity, "invalid_lines", "no lines returned")
		return
	}
	currency := resp.GetLines()[0].GetCurrency()
	var total int64
	for _, l := range resp.GetLines() {
		if l.GetCurrency() != currency {
			s.fail(w, http.StatusUnprocessableEntity, "mixed_currency",
				"all lines must share one currency")
			return
		}
		total += l.GetUnitPriceCents() * int64(l.GetQuantity())
	}

	method := req.ShippingMethodCode
	if method == "" {
		method = "standard"
	}
	lines := make([]store.Line, 0, len(resp.GetLines()))
	for _, l := range resp.GetLines() {
		lines = append(lines, store.Line{
			ProductID:      l.GetProductId(),
			Name:           l.GetName(),
			Quantity:       int(l.GetQuantity()),
			UnitPriceCents: l.GetUnitPriceCents(),
		})
	}

	resv := idempotency.From(r.Context())
	var status int
	var body []byte

	o, err := s.store.Create(r.Context(), tenant, store.CreateInput{
		CustomerID:         userID,
		TotalCents:         total,
		Currency:           currency,
		ShippingMethodCode: method,
		RecipientName:      req.Shipping.RecipientName,
		AddressLine:        req.Shipping.Line1,
		City:               req.Shipping.City,
		Country:            req.Shipping.Country,
		PostalCode:         req.Shipping.PostalCode,
		Lines:              lines,
	}, func(tx pgx.Tx, o store.Order) error {
		env, err := evt.New(evt.OrderCreated, tenant, o.ID, "order", o.ID, buildPayload(o, lines))
		if err != nil {
			return err
		}
		if err := s.outbox.Insert(r.Context(), tx, evt.TopicOrders, o.ID, env); err != nil {
			return err
		}
		status = http.StatusCreated
		body, err = json.Marshal(map[string]any{
			"order": o, "lines": lines,
		})
		if err != nil {
			return err
		}
		return resv.CompleteInTx(r.Context(), tx, status, body)
	})
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	_ = o
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request) {
	claims, _ := authn.ClaimsFrom(r.Context())
	tenant := tenantctx.FromRequest(r)
	o, err := s.store.Get(r.Context(), tenant, chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !isStaff(claims) && o.CustomerID != claims.Subject {
		httpx.Err(w, http.StatusForbidden, "forbidden", "not your order")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"order": o})
}

// listOrders returns the caller's own orders.
func (s *Server) listOrders(w http.ResponseWriter, r *http.Request) {
	claims, _ := authn.ClaimsFrom(r.Context())
	orders, err := s.store.List(r.Context(), tenantctx.FromRequest(r), store.ListFilter{
		CustomerID: claims.Subject,
		Status:     r.URL.Query().Get("status"),
	})
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"orders": orders})
}

// listAllOrders is the staff view of every order in the market.
func (s *Server) listAllOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := s.store.List(r.Context(), tenantctx.FromRequest(r), store.ListFilter{
		Status: r.URL.Query().Get("status"),
	})
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"orders": orders})
}

func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request) {
	claims, _ := authn.ClaimsFrom(r.Context())
	tenant := tenantctx.FromRequest(r)
	id := chi.URLParam(r, "id")

	existing, err := s.store.Get(r.Context(), tenant, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !isStaff(claims) && existing.CustomerID != claims.Subject {
		httpx.Err(w, http.StatusForbidden, "forbidden", "not your order")
		return
	}

	var status int
	var body []byte
	_, already, err := s.store.Cancel(r.Context(), tenant, id, func(tx pgx.Tx, o store.Order) error {
		env, err := evt.New(evt.OrderCancelled, tenant, o.ID, "order", o.ID, map[string]any{
			"customer_id": o.CustomerID,
			"reason":      "cancelled_by_customer",
		})
		if err != nil {
			return err
		}
		if err := s.outbox.Insert(r.Context(), tx, evt.TopicOrders, o.ID, env); err != nil {
			return err
		}
		status = http.StatusOK
		body, err = json.Marshal(map[string]any{"order": o})
		return err
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeErr(w, err)
		case errors.Is(err, store.ErrNotCancellable):
			httpx.Err(w, http.StatusConflict, "not_cancellable",
				"order can no longer be cancelled in its current state")
		default:
			httpx.Internal(w, err)
		}
		return
	}
	if already {
		httpx.JSON(w, http.StatusOK, map[string]any{"order": existing})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.Err(w, http.StatusNotFound, "not_found", "order not found")
		return
	}
	httpx.Internal(w, err)
}

func (s *Server) fail(w http.ResponseWriter, code int, errCode, msg string) {
	httpx.Err(w, code, errCode, msg)
}

func isStaff(c *authn.Claims) bool {
	return c != nil && (c.Role == authn.RoleTenantAdmin || c.Role == authn.RoleStaff ||
		c.Role == authn.RolePlatformAdmin)
}

type payloadLine struct {
	ProductID      string `json:"product_id"`
	Name           string `json:"name"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
}

type orderPayload struct {
	CustomerID    string        `json:"customer_id"`
	TotalCents    int64         `json:"total_cents"`
	Currency      string        `json:"currency"`
	MethodCode    string        `json:"shipping_method_code"`
	RecipientName string        `json:"recipient_name"`
	AddressLine   string        `json:"address_line"`
	City          string        `json:"city"`
	Country       string        `json:"country"`
	PostalCode    string        `json:"postal_code"`
	Lines         []payloadLine `json:"lines"`
}

func buildPayload(o store.Order, lines []store.Line) orderPayload {
	pl := make([]payloadLine, 0, len(lines))
	for _, l := range lines {
		pl = append(pl, payloadLine{
			ProductID: l.ProductID, Name: l.Name,
			Quantity: l.Quantity, UnitPriceCents: l.UnitPriceCents,
		})
	}
	return orderPayload{
		CustomerID: o.CustomerID, TotalCents: o.TotalCents, Currency: o.Currency,
		MethodCode: o.ShippingMethodCode, RecipientName: o.RecipientName,
		AddressLine: o.AddressLine, City: o.City, Country: o.Country,
		PostalCode: o.PostalCode, Lines: pl,
	}
}
