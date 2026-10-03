package api

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	orderv1 "market-master/gen/order/v1"
	"market-master/services/order/internal/store"
)

type GRPC struct {
	orderv1.UnimplementedOrderServiceServer
	store *store.Store
}

func NewGRPC(st *store.Store) *GRPC { return &GRPC{store: st} }

// GetOrder returns authoritative order facts (amount, customer, status,
// shipping destination) for internal callers such as payments/logistics.
func (g *GRPC) GetOrder(ctx context.Context, req *orderv1.GetOrderRequest) (
	*orderv1.GetOrderResponse, error) {
	if req.GetId() == "" || req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id and tenant_id are required")
	}
	o, err := g.store.Get(ctx, req.GetTenantId(), req.GetId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "order not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "lookup failed")
	}
	return &orderv1.GetOrderResponse{
		Id:                 o.ID,
		TenantId:           o.TenantID,
		CustomerId:         o.CustomerID,
		TotalCents:         o.TotalCents,
		Currency:           o.Currency,
		Status:             o.Status,
		ShippingMethodCode: o.ShippingMethodCode,
		RecipientName:      o.RecipientName,
		AddressLine:        o.AddressLine,
		City:               o.City,
		Country:            o.Country,
		PostalCode:         o.PostalCode,
	}, nil
}
