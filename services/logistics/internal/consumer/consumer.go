package consumer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	orderv1 "market-master/gen/order/v1"
	"market-master/pkg/evt"
	"market-master/pkg/grpcx"
	"market-master/pkg/kafkax"
	"market-master/pkg/outbox"
	"market-master/services/logistics/internal/dispatch"
	"market-master/services/logistics/internal/store"
)

const Group = "logistics-svc"

// Handler turns order.paid into shipments and reacts to cancellations.
type Handler struct {
	store    *store.Store
	registry *dispatch.Registry
	orders   orderv1.OrderServiceClient
	log      *slog.Logger
}

func New(st *store.Store, reg *dispatch.Registry, orders orderv1.OrderServiceClient,
	log *slog.Logger) *Handler {
	return &Handler{store: st, registry: reg, orders: orders, log: log}
}

func (h *Handler) Handle(ctx context.Context, msg kafkax.Message) error {
	env, err := evt.Decode(msg.Value)
	if err != nil {
		return fmt.Errorf("poison message (will dead-letter after retries): %w", err)
	}
	switch env.EventType {
	case evt.OrderPaid:
		return h.dispatch(ctx, env)
	case evt.OrderCancelled:
		return h.cancel(ctx, env)
	default:
		return nil
	}
}

func (h *Handler) dispatch(ctx context.Context, env evt.Envelope) error {
	seen, err := h.store.EventSeen(ctx, env.EventID)
	if err != nil {
		return err
	}
	if seen {
		return nil // redelivery: skip provider side effects entirely
	}

	// Shipping destination comes from the order service over gRPC.
	octx, cancel := context.WithTimeout(
		grpcx.WithOutgoing(ctx, env.TenantID, ""), 10*time.Second)
	defer cancel()
	order, err := h.orders.GetOrder(octx, &orderv1.GetOrderRequest{
		Id: env.OrderID, TenantId: env.TenantID,
	})
	if err != nil {
		return fmt.Errorf("get order %s: %w", env.OrderID, err)
	}

	method, err := h.store.ResolveMethod(ctx, env.TenantID, order.GetShippingMethodCode())
	if err != nil {
		return err
	}
	d := h.registry.Get(method.Dispatcher)
	res, err := d.CreateDispatch(ctx, dispatch.Request{
		OrderID:        env.OrderID,
		MethodCode:     method.Code,
		RecipientName:  order.GetRecipientName(),
		AddressLine:    order.GetAddressLine(),
		City:           order.GetCity(),
		Country:        order.GetCountry(),
		PostalCode:     order.GetPostalCode(),
		IdempotencyKey: "dispatch:" + env.OrderID,
	})
	if err != nil {
		return fmt.Errorf("create dispatch via %s: %w", d.Name(), err)
	}

	sh, created, err := h.store.CreateShipment(ctx, env.TenantID, store.ShipmentInput{
		EventID:       env.EventID,
		Group:         Group,
		OrderID:       env.OrderID,
		CustomerID:    order.GetCustomerId(),
		MethodCode:    method.Code,
		Dispatcher:    d.Name(),
		ProviderRef:   res.ProviderRef,
		TrackingURL:   res.TrackingURL,
		RecipientName: order.GetRecipientName(),
		AddressLine:   order.GetAddressLine(),
		City:          order.GetCity(),
		Country:       order.GetCountry(),
		PostalCode:    order.GetPostalCode(),
	}, func(tx pgx.Tx, sh store.Shipment) error {
		return h.emit(ctx, tx, env.TenantID, evt.ShipmentDispatched, sh, map[string]any{
			"shipment_id": sh.ID, "order_id": sh.OrderID,
			"dispatcher": sh.Dispatcher, "provider_ref": sh.ProviderRef,
			"tracking_url": sh.TrackingURL,
		})
	})
	if err != nil {
		return err
	}
	if created {
		h.log.Info("shipment dispatched",
			"order_id", env.OrderID, "shipment_id", sh.ID,
			"dispatcher", sh.Dispatcher, "provider_ref", sh.ProviderRef)
	}
	return nil
}

func (h *Handler) cancel(ctx context.Context, env evt.Envelope) error {
	did, err := h.store.CancelByOrder(ctx, env.TenantID, env.OrderID, env.EventID, Group,
		func(sh store.Shipment) error {
			return h.registry.Get(sh.Dispatcher).CancelDispatch(ctx, sh.ProviderRef)
		})
	if err != nil {
		return err
	}
	if did {
		h.log.Info("shipment cancelled after order cancellation", "order_id", env.OrderID)
	}
	return nil
}

func (h *Handler) emit(ctx context.Context, tx pgx.Tx, tenant, eventType string,
	sh store.Shipment, payload map[string]any) error {
	env, err := evt.New(eventType, tenant, sh.OrderID, "shipment", sh.ID, payload)
	if err != nil {
		return err
	}
	return outbox.Writer{}.Insert(ctx, tx, evt.TopicOrders, sh.OrderID, env)
}
