package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	orderv1 "market-master/gen/order/v1"
	"market-master/pkg/authn"
	"market-master/pkg/conf"
	"market-master/pkg/evt"
	"market-master/pkg/idempotency"
	"market-master/pkg/kafkax"
	"market-master/pkg/migrate"
	"market-master/pkg/outbox"
	"market-master/pkg/psql"
	"market-master/services/payment/internal/api"
	"market-master/services/payment/internal/consumer"
	"market-master/services/payment/internal/psp"
	"market-master/services/payment/internal/store"
	"market-master/services/payment/migrations"
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
		ClientID: "payment-svc",
	})
	if err != nil {
		log.Error("kafka producer", "err", err)
		os.Exit(1)
	}
	defer producer.Close()

	go outbox.NewRelay(pool, producer, outbox.RelayConfig{Logger: log}).Run(ctx)

	st := store.New(pool)
	provider := psp.NewFake(conf.Get("PSP_WEBHOOK_SECRET", "dev-psp-secret"))

	cons, err := kafkax.NewConsumer(kafkax.ConsumerConfig{
		Config: kafkax.Config{Brokers: conf.KafkaBrokers(), ClientID: "payment-svc"},
		Group:  consumer.Group,
		Topic:  evt.TopicOrders,
		Logger: log,
	}, producer)
	if err != nil {
		log.Error("kafka consumer", "err", err)
		os.Exit(1)
	}
	go func() {
		log.Info("consumer started", "group", consumer.Group)
		if err := cons.Run(ctx, consumer.New(st, provider, log).Handle); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("consumer stopped", "err", err)
		}
	}()

	orderConn, err := grpc.NewClient(conf.Get("ORDER_GRPC_ADDR", "localhost:9083"),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Error("order dial", "err", err)
		os.Exit(1)
	}
	defer func() { _ = orderConn.Close() }()

	signer := authn.NewSigner(conf.MustGet("JWT_SECRET"), time.Hour)
	idem := idempotency.NewStore(pool, 30*time.Second)
	srv := api.NewServer(st, signer, idem, provider,
		orderv1.NewOrderServiceClient(orderConn), log)

	httpAddr := conf.Get("ADDR", ":8084")
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
}
