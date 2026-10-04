package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	identityv1 "market-master/gen/identity/v1"
	"market-master/pkg/authn"
	"market-master/pkg/conf"
	"market-master/pkg/grpcx"
	"market-master/pkg/idempotency"
	"market-master/pkg/migrate"
	"market-master/pkg/psql"
	"market-master/services/identity/internal/api"
	"market-master/services/identity/internal/store"
	"market-master/services/identity/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := psql.Open(ctx, conf.MustGet("DATABASE_URL"))
	if err != nil {
		log.Error("database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := migrate.Run(ctx, pool, migrations.FS, "sql"); err != nil {
		log.Error("migrate", "err", err)
		os.Exit(1)
	}
	if err := idempotency.EnsureSchema(ctx, pool); err != nil {
		log.Error("idempotency schema", "err", err)
		os.Exit(1)
	}

	st := store.New(pool)

	// First platform admin comes from env; more are invited from the panel.
	if email := conf.Get("PLATFORM_ADMIN_EMAIL", ""); email != "" {
		pw := conf.Get("PLATFORM_ADMIN_PASSWORD", "")
		switch {
		case len(pw) < 8:
			log.Warn("PLATFORM_ADMIN_PASSWORD must be >= 8 chars, skipping platform admin bootstrap")
		default:
			hash, herr := store.HashPassword(pw)
			if herr != nil {
				log.Error("platform admin hash", "err", herr)
				os.Exit(1)
			}
			created, aerr := st.EnsurePlatformAdmin(ctx, email, hash)
			if aerr != nil {
				log.Error("platform admin bootstrap", "err", aerr)
				os.Exit(1)
			}
			if created {
				log.Info("platform admin bootstrapped", "email", email)
			}
		}
	}

	signer := authn.NewSigner(conf.MustGet("JWT_SECRET"), time.Hour)
	idem := idempotency.NewStore(pool, 30*time.Second)
	srv := api.NewServer(st, signer, idem, log)

	grpcSrv := grpc.NewServer(
		grpc.UnaryInterceptor(grxUnary(log)),
	)
	identityv1.RegisterIdentityServiceServer(grpcSrv, api.NewGRPC(st))
	reflection.Register(grpcSrv)

	grpcAddr := conf.Get("GRPC_ADDR", ":9081")
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Error("grpc listen", "err", err)
		os.Exit(1)
	}
	go func() {
		log.Info("gRPC listening", "addr", grpcAddr)
		if err := grpcSrv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Error("grpc serve", "err", err)
			os.Exit(1)
		}
	}()

	httpAddr := conf.Get("ADDR", ":8081")
	httpSrv := &http.Server{Addr: httpAddr, Handler: srv.Router()}
	go func() {
		log.Info("HTTP listening", "addr", httpAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http serve", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	grpcSrv.GracefulStop()
}

func grxUnary(log *slog.Logger) grpc.UnaryServerInterceptor {
	return grpcx.UnaryServerInterceptor(log)
}
