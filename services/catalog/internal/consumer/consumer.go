package consumer

import (
	"context"
	"fmt"
	"log/slog"

	"market-master/pkg/evt"
	"market-master/pkg/kafkax"
	"market-master/services/catalog/internal/store"
)

const Group = "catalog-svc"

// Handler commits/releases stock reservations in response to order
// outcomes. Hold-until-cancel: payment_failed keeps the hold so checkout can
// retry payment on the same order; only cancellation releases it.
// Idempotent twice over: consumed_events dedupes by event_id, and the
// reservation state machine makes replays no-ops.
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

	switch env.EventType {
	case evt.OrderPaid:
		applied, err := h.store.FulfillReservations(ctx, env.TenantID, env.OrderID,
			env.EventID, Group, env.EventType)
		if err != nil {
			return err
		}
		if applied {
			h.log.Info("stock committed", "order_id", env.OrderID, "tenant_id", env.TenantID)
		}
	case evt.OrderCancelled:
		applied, err := h.store.CancelReservations(ctx, env.TenantID, env.OrderID,
			env.EventID, Group, env.EventType)
		if err != nil {
			return err
		}
		if applied {
			h.log.Info("stock released", "order_id", env.OrderID,
				"tenant_id", env.TenantID, "event", env.EventType)
		}
	default:
		// order.created / payment_authorized / payment_failed / shipment.*
		// don't touch stock: payment_failed intentionally keeps the hold
		// (retry-safe), cancellation is the only release path.
		return nil
	}
	return nil
}
