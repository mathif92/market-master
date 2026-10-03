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

	catalogv1 "market-master/gen/catalog/v1"
	"market-master/pkg/authn"
	"market-master/pkg/conf"
	"market-master/pkg/grpcx"
	"market-master/pkg/migrate"
	"market-master/pkg/psql"
	"market-master/services/catalog/internal/api"
	"market-master/services/catalog/internal/store"
	"market-master/services/catalog/migrations"
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

	st := store.New(pool)
	signer := authn.NewSigner(conf.MustGet("JWT_SECRET"), time.Hour)
	srv := api.NewServer(st, signer, log)

	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(grpcx.UnaryServerInterceptor(log)))
	catalogv1.RegisterCatalogServiceServer(grpcSrv, api.NewGRPC(st))
	reflection.Register(grpcSrv)

	grpcAddr := conf.Get("GRPC_ADDR", ":9082")
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

	httpAddr := conf.Get("ADDR", ":8082")
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
