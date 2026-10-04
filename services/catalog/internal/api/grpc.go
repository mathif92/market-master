package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	catalogv1 "market-master/gen/catalog/v1"
	"market-master/services/catalog/internal/store"
)

type GRPC struct {
	catalogv1.UnimplementedCatalogServiceServer
	store *store.Store
}

func NewGRPC(st *store.Store) *GRPC { return &GRPC{store: st} }

// ValidateOrderLines checks availability and computes authoritative price
// snapshots for an order in a single internal call: campaign pricing is
// resolved first (coupon_code presented → invalid_coupon / campaign_limit
// on failure), then — with reserve=true — stock is held atomically for the
// pre-generated order id (all-or-nothing).
func (g *GRPC) ValidateOrderLines(ctx context.Context, req *catalogv1.ValidateOrderLinesRequest) (
	*catalogv1.ValidateOrderLinesResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if len(req.GetLines()) == 0 {
		return &catalogv1.ValidateOrderLinesResponse{
			Valid: false, ErrorCode: "invalid_lines",
			ErrorMessage: "order must contain at least one line",
		}, nil
	}
	if req.GetReserve() && req.GetOrderId() == "" {
		return nil, status.Error(codes.InvalidArgument, "order_id is required when reserve is set")
	}

	ids := make([]string, 0, len(req.GetLines()))
	seen := make(map[string]bool, len(req.GetLines()))
	for _, l := range req.GetLines() {
		if l.GetProductId() == "" {
			return &catalogv1.ValidateOrderLinesResponse{
				Valid: false, ErrorCode: "invalid_lines",
				ErrorMessage: "line product_id is required",
			}, nil
		}
		if l.GetQuantity() < 1 || l.GetQuantity() > 100 {
			return &catalogv1.ValidateOrderLinesResponse{
				Valid: false, ErrorCode: "invalid_lines",
				ErrorMessage: fmt.Sprintf("quantity for product %s must be between 1 and 100", l.GetProductId()),
			}, nil
		}
		if seen[l.GetProductId()] {
			return &catalogv1.ValidateOrderLinesResponse{
				Valid: false, ErrorCode: "invalid_lines",
				ErrorMessage: fmt.Sprintf("duplicate product %s in order", l.GetProductId()),
			}, nil
		}
		seen[l.GetProductId()] = true
		ids = append(ids, l.GetProductId())
	}

	products, err := g.store.GetActiveProducts(ctx, req.GetTenantId(), ids)
	if err != nil {
		return nil, status.Error(codes.Internal, "catalog lookup failed")
	}

	reserveLines := make([]store.ReserveLine, 0, len(req.GetLines()))
	for _, l := range req.GetLines() {
		p, ok := products[l.GetProductId()]
		if !ok || p.Status != "active" {
			return &catalogv1.ValidateOrderLinesResponse{
				Valid: false, ErrorCode: "invalid_lines",
				ErrorMessage: fmt.Sprintf("product %s is unavailable", l.GetProductId()),
			}, nil
		}
		reserveLines = append(reserveLines, store.ReserveLine{
			ProductID: l.GetProductId(), Quantity: l.GetQuantity(),
		})
	}

	// One transaction: campaign pricing (+ redemption/counter when
	// reserving) then the stock hold. Pricing errors surface before any
	// stock is touched; everything rolls back together.
	var res store.ReserveResult
	if req.GetReserve() {
		res, err = g.store.PriceAndReserve(ctx, req.GetTenantId(), req.GetOrderId(),
			reserveLines, req.GetCouponCode())
	} else {
		res, err = g.store.PriceOrder(ctx, req.GetTenantId(), reserveLines, req.GetCouponCode())
	}
	if err != nil {
		return pricingErrorResponse(err)
	}

	snapshots := make([]*catalogv1.OrderLineSnapshot, 0, len(res.Lines))
	for _, pl := range res.Lines {
		p := products[pl.ProductID]
		snapshots = append(snapshots, &catalogv1.OrderLineSnapshot{
			ProductId:      pl.ProductID,
			Name:           p.Name,
			Quantity:       quantityOf(req, pl.ProductID),
			UnitPriceCents: pl.UnitFinalCents,
			ListPriceCents: pl.ListPriceCents,
			Currency:       p.Currency,
		})
	}
	return &catalogv1.ValidateOrderLinesResponse{Valid: true, Lines: snapshots}, nil
}

func quantityOf(req *catalogv1.ValidateOrderLinesRequest, productID string) int32 {
	for _, l := range req.GetLines() {
		if l.GetProductId() == productID {
			return l.GetQuantity()
		}
	}
	return 0
}

// pricingErrorResponse maps store pricing failures to invalid gRPC
// responses (never transport errors — the caller answers 422).
func pricingErrorResponse(err error) (*catalogv1.ValidateOrderLinesResponse, error) {
	var insuff *store.InsufficientStockError
	switch {
	case errors.As(err, &insuff):
		return &catalogv1.ValidateOrderLinesResponse{
			Valid: false, ErrorCode: "insufficient_stock",
			ErrorMessage: shortageMessage(insuff.Shortages),
		}, nil
	case errors.Is(err, store.ErrInvalidCoupon):
		return &catalogv1.ValidateOrderLinesResponse{
			Valid: false, ErrorCode: "invalid_coupon",
			ErrorMessage: err.Error(),
		}, nil
	case errors.Is(err, store.ErrCampaignLimit):
		return &catalogv1.ValidateOrderLinesResponse{
			Valid: false, ErrorCode: "campaign_limit",
			ErrorMessage: err.Error(),
		}, nil
	default:
		return nil, status.Error(codes.Internal, "stock reservation failed")
	}
}

// ReleaseOrderReservations drops an order's stock holds and campaign
// redemptions (order insert failed after reserve).
func (g *GRPC) ReleaseOrderReservations(ctx context.Context, req *catalogv1.ReleaseOrderReservationsRequest) (
	*catalogv1.ReleaseOrderReservationsResponse, error) {
	if req.GetTenantId() == "" || req.GetOrderId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and order_id are required")
	}
	if err := g.store.ReleaseReservations(ctx, req.GetTenantId(), req.GetOrderId()); err != nil {
		return nil, status.Error(codes.Internal, "reservation release failed")
	}
	return &catalogv1.ReleaseOrderReservationsResponse{}, nil
}

func shortageMessage(shortages []store.Shortage) string {
	parts := make([]string, 0, len(shortages))
	for _, s := range shortages {
		name := s.Name
		if name == "" {
			name = s.ProductID
		}
		parts = append(parts, fmt.Sprintf("%s: %d requested, %d available",
			name, s.Wanted, s.Available))
	}
	return "insufficient stock — " + strings.Join(parts, "; ")
}
