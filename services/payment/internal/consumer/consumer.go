package consumer

import (
	"context"
	"fmt"
	"log/slog"

	"market-master/pkg/evt"
	"market-master/pkg/kafkax"
	"market-master/services/payment/internal/psp"
	"market-master/services/payment/internal/store"
)

const Group = "payment-svc"

// Handler voids authorized payments when their order is cancelled.
type Handler struct {
	store *store.Store
	psp   psp.PSP
	log   *slog.Logger
}

func New(st *store.Store, ps psp.PSP, log *slog.Logger) *Handler {
	return &Handler{store: st, psp: ps, log: log}
}

func (h *Handler) Handle(ctx context.Context, msg kafkax.Message) error {
	env, err := evt.Decode(msg.Value)
	if err != nil {
		return fmt.Errorf("poison message (will dead-letter after retries): %w", err)
	}
	if env.EventType != evt.OrderCancelled {
		return nil
	}
	voided, err := h.store.ApplyOrderCancelled(ctx, env.TenantID, env.OrderID,
		env.EventID, Group, func(pspRef string) error {
			return h.psp.Void(ctx, pspRef)
		})
	if err != nil {
		return err
	}
	if voided {
		h.log.Info("payment voided after order cancellation", "order_id", env.OrderID)
	}
	return nil
}
