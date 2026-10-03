package consumer

import (
	"context"
	"fmt"
	"log/slog"

	"market-master/pkg/evt"
	"market-master/pkg/kafkax"
	"market-master/services/order/internal/sm"
	"market-master/services/order/internal/store"
)

const Group = "order-svc"

// Handler applies payment and shipment events to order state. It is
// idempotent twice over: consumed_events dedupes by event_id, and the
// state machine rejects illegal/redundant transitions.
type Handler struct {
	store *store.Store
	log   *slog.Logger
}

func New(st *store.Store, log *slog.Logger) *Handler {
	return &Handler{store: st, log: log}
}

func (h *Handler) Handle(ctx context.Context, msg kafkax.Message) error {
	env, err := evt.Decode(msg.Value)
	if err != nil {
		return fmt.Errorf("poison message (will dead-letter after retries): %w", err)
	}

	var to sm.Status
	switch env.EventType {
	case evt.OrderPaymentAuthorized:
		to = sm.AwaitingPayment
	case evt.OrderPaymentFailed:
		to = sm.PaymentFailed
	case evt.OrderPaid:
		to = sm.Paid
	case evt.ShipmentDispatched:
		to = sm.Dispatched
	case evt.ShipmentDelivered:
		to = sm.Delivered
	default:
		return nil // order.created / order.cancelled are produced by us
	}

	applied, err := h.store.ApplyEvent(ctx, env.TenantID, env.OrderID, to,
		env.EventID, Group, env.EventType)
	if err != nil {
		return err
	}
	if applied {
		h.log.Info("order transition applied",
			"order_id", env.OrderID, "event", env.EventType, "status", string(to))
	}
	return nil
}
