package grpcx

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"market-master/pkg/tenantctx"
)

const (
	mdTenant = "x-tenant-id"
	mdUser   = "x-user-id"
)

// WithOutgoing returns ctx annotated with tenant/user metadata for the
// gRPC client interceptor to send.
func WithOutgoing(ctx context.Context, tenantID, userID string) context.Context {
	pairs := []string{}
	if tenantID != "" {
		pairs = append(pairs, mdTenant, tenantID)
	}
	if userID != "" {
		pairs = append(pairs, mdUser, userID)
	}
	if len(pairs) == 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, pairs...)
}

// UserIDFrom extracts the user id propagated by the caller.
func UserIDFrom(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(mdUser); len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// UnaryClientInterceptor forwards tenant/user metadata.
func UnaryClientInterceptor(ctx context.Context, method string, req, reply any,
	cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	return invoker(ctx, method, req, reply, cc, opts...)
}

// UnaryServerInterceptor recovers panics, injects the tenant id from
// metadata into the context, and logs calls.
func UnaryServerInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("grpc panic", "method", info.FullMethod, "panic", r)
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v := md.Get(mdTenant); len(v) > 0 {
				ctx = tenantctx.With(ctx, v[0])
			}
		}
		resp, err = handler(ctx, req)
		if err != nil {
			log.Warn("grpc call failed", "method", info.FullMethod, "err", err)
		}
		return resp, err
	}
}
