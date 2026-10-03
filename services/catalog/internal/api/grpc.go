package api

import (
	"context"
	"fmt"

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
// snapshots for an order in a single internal call.
func (g *GRPC) ValidateOrderLines(ctx context.Context, req *catalogv1.ValidateOrderLinesRequest) (
	*catalogv1.ValidateOrderLinesResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if len(req.GetLines()) == 0 {
		return &catalogv1.ValidateOrderLinesResponse{
			Valid: false, ErrorMessage: "order must contain at least one line",
		}, nil
	}

	ids := make([]string, 0, len(req.GetLines()))
	seen := make(map[string]bool, len(req.GetLines()))
	for _, l := range req.GetLines() {
		if l.GetProductId() == "" {
			return &catalogv1.ValidateOrderLinesResponse{
				Valid: false, ErrorMessage: "line product_id is required",
			}, nil
		}
		if l.GetQuantity() < 1 || l.GetQuantity() > 100 {
			return &catalogv1.ValidateOrderLinesResponse{
				Valid:        false,
				ErrorMessage: fmt.Sprintf("quantity for product %s must be between 1 and 100", l.GetProductId()),
			}, nil
		}
		if seen[l.GetProductId()] {
			return &catalogv1.ValidateOrderLinesResponse{
				Valid:        false,
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

	snapshots := make([]*catalogv1.OrderLineSnapshot, 0, len(req.GetLines()))
	for _, l := range req.GetLines() {
		p, ok := products[l.GetProductId()]
		if !ok || p.Status != "active" {
			return &catalogv1.ValidateOrderLinesResponse{
				Valid:        false,
				ErrorMessage: fmt.Sprintf("product %s is unavailable", l.GetProductId()),
			}, nil
		}
		snapshots = append(snapshots, &catalogv1.OrderLineSnapshot{
			ProductId:      p.ID,
			Name:           p.Name,
			Quantity:       l.GetQuantity(),
			UnitPriceCents: p.PriceCents,
			Currency:       p.Currency,
		})
	}
	return &catalogv1.ValidateOrderLinesResponse{Valid: true, Lines: snapshots}, nil
}
