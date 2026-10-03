package api

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "market-master/gen/identity/v1"
	"market-master/services/identity/internal/store"
)

type GRPC struct {
	identityv1.UnimplementedIdentityServiceServer
	store *store.Store
}

func NewGRPC(st *store.Store) *GRPC { return &GRPC{store: st} }

func (g *GRPC) ResolveTenant(ctx context.Context, req *identityv1.ResolveTenantRequest) (
	*identityv1.ResolveTenantResponse, error) {
	if req.GetSlug() == "" {
		return nil, status.Error(codes.InvalidArgument, "slug is required")
	}
	t, err := g.store.GetTenantBySlug(ctx, req.GetSlug())
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "lookup failed")
	}
	return &identityv1.ResolveTenantResponse{
		Tenant: &identityv1.Tenant{
			Id: t.ID, Slug: t.Slug, Name: t.Name, Status: t.Status,
		},
	}, nil
}

func (g *GRPC) GetUser(ctx context.Context, req *identityv1.GetUserRequest) (
	*identityv1.GetUserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	u, err := g.store.GetUserByID(ctx, req.GetId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "user not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "lookup failed")
	}
	return &identityv1.GetUserResponse{
		User: &identityv1.User{
			Id: u.ID, TenantId: u.TenantID, Email: u.Email, Role: u.Role, Status: u.Status,
		},
	}, nil
}
