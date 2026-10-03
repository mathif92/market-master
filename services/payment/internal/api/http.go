package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	orderv1 "market-master/gen/order/v1"
	"market-master/pkg/authn"
	"market-master/pkg/evt"
	"market-master/pkg/grpcx"
	"market-master/pkg/httpmw"
	"market-master/pkg/httpx"
	"market-master/pkg/idempotency"
	"market-master/pkg/outbox"
	"market-master/pkg/tenantctx"
	"market-master/services/payment/internal/psp"
	"market-master/services/payment/internal/store"
)

type Server struct {
	store  *store.Store
	signer *authn.Signer
	idem   *idempotency.Store
	psp    psp.PSP
	orders orderv1.OrderServiceClient
	outbox outbox.Writer
	log    *slog.Logger
}

func NewServer(st *store.Store, signer *authn.Signer, idem *idempotency.Store,
	ps psp.PSP, orders orderv1.OrderServiceClient, log *slog.Logger) *Server {
	return &Server{store: st, signer: signer, idem: idem, psp: ps, orders: orders, log: log}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	// Provider webhooks authenticate with an HMAC signature, not a JWT.
	r.Post("/v1/psp/webhook", s.pspWebhook)

	r.Group(func(r chi.Router) {
		r.Use(s.signer.Middleware)
		r.Use(tenantctx.Middleware)
		r.Use(httpmw.BindTenant)
		r.Use(requireTenant)

		r.With(s.idem.Middleware("payment.create", true)).
			Post("/v1/payments", s.createPayment)
		r.Get("/v1/payments", s.listPayments)
		r.Get("/v1/payments/{id}", s.getPayment)
		r.With(authn.RequireRole(authn.RoleTenantAdmin, authn.RoleStaff)).
			Post("/v1/payments/{id}/capture", s.capturePayment)
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

type createPaymentReq struct {
	OrderID            string `json:"order_id"`
	PaymentMethodToken string `json:"payment_method_token"`
	Capture            *bool  `json:"capture"`
}

func (s *Server) createPayment(w http.ResponseWriter, r *http.Request) {
	var req createPaymentReq
	if err := httpx.Decode(r, &req); err != nil {
		s.fail(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.OrderID == "" || req.PaymentMethodToken == "" {
		s.fail(w, http.StatusBadRequest, "invalid_payment",
			"order_id and payment_method_token are required")
		return
	}
	claims, _ := authn.ClaimsFrom(r.Context())
	tenant := tenantctx.FromRequest(r)
	userID := claims.Subject

	// Domain-level idempotency: a settled payment is never charged twice,
	// even with a different Idempotency-Key.
	existing, found, err := s.store.GetByOrder(r.Context(), tenant, req.OrderID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	if found && isSettled(existing.Status) {
		httpx.JSON(w, http.StatusOK, map[string]any{"payment": existing})
		return
	}

	// Authoritative amount comes from the order service over gRPC.
	octx, cancel := context.WithTimeout(
		grpcx.WithOutgoing(r.Context(), tenant, userID), 10*time.Second)
	defer cancel()
	order, err := s.orders.GetOrder(octx, &orderv1.GetOrderRequest{
		Id: req.OrderID, TenantId: tenant,
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			s.fail(w, http.StatusNotFound, "order_not_found", "order does not exist")
			return
		}
		s.log.Error("order lookup failed", "err", err)
		s.fail(w, http.StatusServiceUnavailable, "order_unavailable", "retry later")
		return
	}
	if !isStaff(claims) && order.GetCustomerId() != userID {
		s.fail(w, http.StatusForbidden, "forbidden", "not your order")
		return
	}
	switch order.GetStatus() {
	case "created", "awaiting_payment", "payment_failed":
	default:
		s.fail(w, http.StatusConflict, "order_not_payable",
			"order in status "+order.GetStatus()+" cannot be paid")
		return
	}

	wantCapture := req.Capture == nil || *req.Capture
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" {
		idemKey = req.OrderID
	}
	pres, err := s.psp.Authorize(r.Context(), psp.AuthorizeRequest{
		Token:       req.PaymentMethodToken,
		AmountCents: order.GetTotalCents(),
		Currency:    order.GetCurrency(),
		IdemKey:     "authorize:" + req.OrderID + ":" + idemKey,
	})
	if err != nil {
		s.log.Error("psp authorize error", "err", err)
		s.fail(w, http.StatusBadGateway, "psp_unavailable", "payment provider error, retry later")
		return
	}

	resv := idempotency.From(r.Context())
	status := "failed"
	if pres.OK {
		if wantCapture {
			status = "captured"
		} else {
			status = "authorized"
		}
	}
	var outStatus int
	var body []byte
	it, alreadySettled, err := s.store.UpsertAttempt(r.Context(), tenant, store.Attempt{
		OrderID:       req.OrderID,
		CustomerID:    order.GetCustomerId(),
		AmountCents:   order.GetTotalCents(),
		Currency:      order.GetCurrency(),
		Psp:           s.psp.Name(),
		PspRef:        pres.PspRef,
		PspToken:      req.PaymentMethodToken,
		Brand:         pres.Brand,
		Last4:         pres.Last4,
		Status:        status,
		FailureReason: pres.FailureReason,
	}, func(tx pgx.Tx, it store.Intent) error {
		if err := s.emitPaymentEvents(r.Context(), tx, tenant, it); err != nil {
			return err
		}
		if pres.OK {
			outStatus = http.StatusCreated
		} else {
			outStatus = http.StatusPaymentRequired
		}
		body, err = json.Marshal(map[string]any{"payment": it})
		if err != nil {
			return err
		}
		return resv.CompleteInTx(r.Context(), tx, outStatus, body)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Lost a race against a concurrent payment for the same order:
			// release the authorization we just created, never double-charge.
			if pres.OK {
				_ = s.psp.Void(r.Context(), pres.PspRef)
			}
			cur, _, _ := s.store.GetByOrder(r.Context(), tenant, req.OrderID)
			httpx.JSON(w, http.StatusOK, map[string]any{"payment": cur})
			return
		}
		httpx.Internal(w, err)
		return
	}
	if alreadySettled {
		httpx.JSON(w, http.StatusOK, map[string]any{"payment": it})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(outStatus)
	_, _ = w.Write(body)
}

// emitPaymentEvents writes the outbox rows inside the payment transaction;
// all events key on order_id so they share the order's Kafka partition.
func (s *Server) emitPaymentEvents(ctx context.Context, tx pgx.Tx, tenant string,
	it store.Intent) error {
	authorized, err := evt.New(evt.OrderPaymentAuthorized, tenant, it.OrderID, "payment", it.ID,
		map[string]any{
			"payment_id": it.ID, "order_id": it.OrderID,
			"amount_cents": it.AmountCents, "currency": it.Currency,
			"psp": it.Psp, "psp_ref": it.PspRef, "brand": it.Brand, "last4": it.Last4,
		})
	if err != nil {
		return err
	}
	if err := s.outbox.Insert(ctx, tx, evt.TopicOrders, it.OrderID, authorized); err != nil {
		return err
	}
	if it.Status == "captured" {
		paid, err := evt.New(evt.OrderPaid, tenant, it.OrderID, "payment", it.ID,
			map[string]any{
				"payment_id": it.ID, "order_id": it.OrderID,
				"amount_cents": it.AmountCents, "currency": it.Currency,
				"captured": true,
			})
		if err != nil {
			return err
		}
		if err := s.outbox.Insert(ctx, tx, evt.TopicOrders, it.OrderID, paid); err != nil {
			return err
		}
	}
	if it.Status == "failed" {
		failed, err := evt.New(evt.OrderPaymentFailed, tenant, it.OrderID, "payment", it.ID,
			map[string]any{
				"order_id": it.OrderID, "reason": it.FailureReason,
			})
		if err != nil {
			return err
		}
		if err := s.outbox.Insert(ctx, tx, evt.TopicOrders, it.OrderID, failed); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) capturePayment(w http.ResponseWriter, r *http.Request) {
	tenant := tenantctx.FromRequest(r)
	id := chi.URLParam(r, "id")

	it, err := s.store.GetByID(r.Context(), tenant, id)
	if err != nil {
		writeIntentErr(w, err)
		return
	}
	if it.Status == "captured" {
		httpx.JSON(w, http.StatusOK, map[string]any{"payment": it}) // idempotent
		return
	}
	if it.Status != "authorized" {
		httpx.Err(w, http.StatusConflict, "not_capturable",
			"payment is not in authorized state")
		return
	}
	if err := s.psp.Capture(r.Context(), it.PspRef, "capture:"+id); err != nil {
		s.log.Error("psp capture error", "err", err)
		httpx.Err(w, http.StatusBadGateway, "psp_unavailable", "provider error, retry later")
		return
	}
	out, already, err := s.store.Capture(r.Context(), tenant, id, func(tx pgx.Tx, it store.Intent) error {
		paid, err := evt.New(evt.OrderPaid, tenant, it.OrderID, "payment", it.ID,
			map[string]any{
				"payment_id": it.ID, "order_id": it.OrderID,
				"amount_cents": it.AmountCents, "currency": it.Currency,
				"captured": true,
			})
		if err != nil {
			return err
		}
		return s.outbox.Insert(r.Context(), tx, evt.TopicOrders, it.OrderID, paid)
	})
	if err != nil {
		writeIntentErr(w, err)
		return
	}
	if already {
		httpx.JSON(w, http.StatusOK, map[string]any{"payment": out})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"payment": out})
}

func (s *Server) getPayment(w http.ResponseWriter, r *http.Request) {
	claims, _ := authn.ClaimsFrom(r.Context())
	it, err := s.store.GetByID(r.Context(), tenantctx.FromRequest(r), chi.URLParam(r, "id"))
	if err != nil {
		writeIntentErr(w, err)
		return
	}
	if !isStaff(claims) && it.CustomerID != claims.Subject {
		httpx.Err(w, http.StatusForbidden, "forbidden", "not your payment")
		return
	}
	httpx.JSON(w, http.StatusOK, it)
}

func (s *Server) listPayments(w http.ResponseWriter, r *http.Request) {
	claims, _ := authn.ClaimsFrom(r.Context())
	f := store.ListFilter{OrderID: r.URL.Query().Get("order_id")}
	if !isStaff(claims) {
		f.CustomerID = claims.Subject
	}
	intents, err := s.store.List(r.Context(), tenantctx.FromRequest(r), f)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"payments": intents})
}

type webhookReq struct {
	EventID string `json:"event_id"`
	Type    string `json:"type"`
	Data    struct {
		PspRef string `json:"psp_ref"`
	} `json:"data"`
}

// pspWebhook handles asynchronous provider events. Authenticity comes from
// the HMAC signature; delivery is deduped by (psp, event_id).
func (s *Server) pspWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "bad_body", "cannot read body")
		return
	}
	sig := r.Header.Get("X-PSP-Signature")
	if sig == "" || !s.psp.VerifyWebhook(sig, body) {
		httpx.Err(w, http.StatusUnauthorized, "bad_signature", "invalid webhook signature")
		return
	}
	var req webhookReq
	if err := json.Unmarshal(body, &req); err != nil || req.EventID == "" || req.Data.PspRef == "" {
		httpx.Err(w, http.StatusBadRequest, "invalid_event", "event_id and data.psp_ref are required")
		return
	}

	tenant, err := s.store.ResolveTenantByPspRef(r.Context(), req.Data.PspRef)
	if errors.Is(err, store.ErrNotFound) {
		// Unknown reference: acknowledge so the provider stops retrying.
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	s.store.AuditWebhook(r.Context(), s.psp.Name(), req.EventID, req.Type, body)

	switch req.Type {
	case "payment.captured":
		applied, err := s.store.ApplyWebhookEvent(r.Context(), tenant, req.Data.PspRef,
			req.EventID, "psp-webhook", "captured", func(tx pgx.Tx, it store.Intent) error {
				paid, err := evt.New(evt.OrderPaid, tenant, it.OrderID, "payment", it.ID,
					map[string]any{
						"payment_id": it.ID, "order_id": it.OrderID,
						"amount_cents": it.AmountCents, "currency": it.Currency,
						"captured": true, "via": "webhook",
					})
				if err != nil {
					return err
				}
				return s.outbox.Insert(r.Context(), tx, evt.TopicOrders, it.OrderID, paid)
			})
		if err != nil {
			httpx.Internal(w, err)
			return
		}
		s.log.Info("webhook processed", "event_id", req.EventID, "applied", applied)
	case "refund.succeeded":
		applied, err := s.store.ApplyWebhookEvent(r.Context(), tenant, req.Data.PspRef,
			req.EventID, "psp-webhook", "refunded", nil)
		if err != nil {
			httpx.Internal(w, err)
			return
		}
		s.log.Info("refund processed", "event_id", req.EventID, "applied", applied)
	default:
		s.log.Info("ignoring webhook event type", "type", req.Type)
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeIntentErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Err(w, http.StatusNotFound, "not_found", "payment not found")
	case errors.Is(err, store.ErrNotCapturable):
		httpx.Err(w, http.StatusConflict, "not_capturable", err.Error())
	default:
		httpx.Internal(w, err)
	}
}

func isSettled(status string) bool {
	return status == "authorized" || status == "captured" || status == "refunded"
}

func isStaff(c *authn.Claims) bool {
	return c != nil && (c.Role == authn.RoleTenantAdmin || c.Role == authn.RoleStaff ||
		c.Role == authn.RolePlatformAdmin)
}

func (s *Server) fail(w http.ResponseWriter, code int, errCode, msg string) {
	httpx.Err(w, code, errCode, msg)
}
