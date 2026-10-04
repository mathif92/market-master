package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"market-master/pkg/conf"
	"market-master/services/gateway/internal/proxy"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ttl := time.Minute
	if v := conf.Get("TENANT_CACHE_TTL", "60s"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			log.Warn("invalid TENANT_CACHE_TTL, using 60s", "value", v)
		} else {
			ttl = d
		}
	}

	gw, err := proxy.New(proxy.Config{
		IdentityGRPC:   conf.Get("IDENTITY_GRPC_ADDR", "localhost:9081"),
		TenantCacheTTL: ttl,
		Routes: map[string]string{
			"/v1/tenants":          conf.Get("IDENTITY_HTTP_ADDR", "localhost:8081"),
			"/v1/auth/":            conf.Get("IDENTITY_HTTP_ADDR", "localhost:8081"),
			"/v1/platform/":        conf.Get("IDENTITY_HTTP_ADDR", "localhost:8081"),
			"/v1/users":            conf.Get("IDENTITY_HTTP_ADDR", "localhost:8081"),
			"/v1/me":               conf.Get("IDENTITY_HTTP_ADDR", "localhost:8081"),
			"/v1/categories":       conf.Get("CATALOG_HTTP_ADDR", "localhost:8082"),
			"/v1/products":         conf.Get("CATALOG_HTTP_ADDR", "localhost:8082"),
			"/v1/campaigns":        conf.Get("CATALOG_HTTP_ADDR", "localhost:8082"),
			"/v1/inventory":        conf.Get("CATALOG_HTTP_ADDR", "localhost:8082"),
			"/v1/orders":           conf.Get("ORDER_HTTP_ADDR", "localhost:8083"),
			"/v1/payments":         conf.Get("PAYMENT_HTTP_ADDR", "localhost:8084"),
			"/v1/psp/":             conf.Get("PAYMENT_HTTP_ADDR", "localhost:8084"),
			"/v1/shipments":        conf.Get("LOGISTICS_HTTP_ADDR", "localhost:8085"),
			"/v1/shipping-methods": conf.Get("LOGISTICS_HTTP_ADDR", "localhost:8085"),
		},
		JWTSecret: conf.MustGet("JWT_SECRET"),
		Logger:    log,
	})
	if err != nil {
		log.Error("gateway init", "err", err)
		os.Exit(1)
	}

	httpAddr := conf.Get("ADDR", ":8080")
	srv := &http.Server{Addr: httpAddr, Handler: gw}
	go func() {
		log.Info("gateway listening", "addr", httpAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("gateway serve", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
