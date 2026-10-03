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
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"

	catalogv1 "market-master/gen/catalog/v1"
	orderv1 "market-master/gen/order/v1"
	"market-master/pkg/authn"
	"market-master/pkg/conf"
	"market-master/pkg/evt"
	"market-master/pkg/grpcx"
	"market-master/pkg/idempotency"
	"market-master/pkg/kafkax"
	"market-master/pkg/migrate"
	"market-master/pkg/outbox"
	"market-master/pkg/psql"
	"market-master/services/order/internal/api"
	"market-master/services/order/internal/consumer"
	"market-master/services/order/internal/store"
	"market-master/services/order/migrations"
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
	if err := outbox.EnsureSchema(ctx, pool); err != nil {
		log.Error("outbox schema", "err", err)
		os.Exit(1)
	}
	if err := idempotency.EnsureSchema(ctx, pool); err != nil {
		log.Error("idempotency schema", "err", err)
		os.Exit(1)
	}

	producer, err := kafkax.NewProducer(kafkax.Config{
		Brokers:  conf.KafkaBrokers(),
		ClientID: "order-svc",
	})
	if err != nil {
		log.Error("kafka producer", "err", err)
		os.Exit(1)
	}
	defer producer.Close()

	relay := outbox.NewRelay(pool, producer, outbox.RelayConfig{Logger: log})
	go relay.Run(ctx)

	cons, err := kafkax.NewConsumer(kafkax.ConsumerConfig{
		Config: kafkax.Config{Brokers: conf.KafkaBrokers(), ClientID: "order-svc"},
		Group:  consumer.Group,
		Topic:  evt.TopicOrders,
		Logger: log,
	}, producer)
	if err != nil {
		log.Error("kafka consumer", "err", err)
		os.Exit(1)
	}
	st := store.New(pool)
	go func() {
		log.Info("consumer started", "group", consumer.Group, "topic", evt.TopicOrders)
		if err := cons.Run(ctx, consumer.New(st, log).Handle); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("consumer stopped", "err", err)
		}
	}()

	catalogConn, err := grpc.NewClient(conf.Get("CATALOG_GRPC_ADDR", "localhost:9082"),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Error("catalog dial", "err", err)
		os.Exit(1)
	}
	defer func() { _ = catalogConn.Close() }()

	signer := authn.NewSigner(conf.MustGet("JWT_SECRET"), time.Hour)
	idem := idempotency.NewStore(pool, 30*time.Second)
	srv := api.NewServer(st, signer, idem, catalogv1.NewCatalogServiceClient(catalogConn), log)

	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(grpcx.UnaryServerInterceptor(log)))
	orderv1.RegisterOrderServiceServer(grpcSrv, api.NewGRPC(st))
	reflection.Register(grpcSrv)

	grpcAddr := conf.Get("GRPC_ADDR", ":9083")
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

	httpAddr := conf.Get("ADDR", ":8083")
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
