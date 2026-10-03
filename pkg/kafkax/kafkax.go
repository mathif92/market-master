package kafkax

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"market-master/pkg/evt"
)

type Config struct {
	Brokers  string // comma separated host:port
	ClientID string
	Username string
	Password string
}

func brokers(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Producer wraps an idempotent franz-go producer (acks=all by default).
type Producer struct {
	cl *kgo.Client
}

func NewProducer(cfg Config) (*Producer, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers(cfg.Brokers)...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerLinger(10 * time.Millisecond),
	}
	if cfg.ClientID != "" {
		opts = append(opts, kgo.ClientID(cfg.ClientID))
	}
	if cfg.Username != "" {
		opts = append(opts, kgo.SASL(scram.Auth{
			User: cfg.Username, Pass: cfg.Password,
		}.AsSha256Mechanism()))
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka producer: %w", err)
	}
	return &Producer{cl: cl}, nil
}

func (p *Producer) Publish(ctx context.Context, topic, key string, payload []byte) error {
	rec := &kgo.Record{Topic: topic, Key: []byte(key), Value: payload}
	return p.cl.ProduceSync(ctx, rec).FirstErr()
}

func (p *Producer) Close() { p.cl.Close() }

// Handler processes one consumed message. Returning an error triggers
// retries, then the message is published to the group's DLQ and skipped.
type Handler func(ctx context.Context, msg Message) error

type Message struct {
	Topic     string
	Partition int32
	Offset    int64
	Key       []byte
	Value     []byte
}

type ConsumerConfig struct {
	Config
	Group       string
	Topic       string
	MaxAttempts int
	Logger      *slog.Logger
}

type Consumer struct {
	cfg  ConsumerConfig
	cl   *kgo.Client
	prod *Producer
	log  *slog.Logger
}

func NewConsumer(cfg ConsumerConfig, prod *Producer) (*Consumer, error) {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers(cfg.Brokers)...),
		kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(cfg.Topic),
		kgo.DisableAutoCommit(), // offsets committed only after successful handling/DLQ
		kgo.BlockRebalanceOnPoll(),
	}
	if cfg.ClientID != "" {
		opts = append(opts, kgo.ClientID(cfg.ClientID))
	}
	if cfg.Username != "" {
		opts = append(opts, kgo.SASL(scram.Auth{
			User: cfg.Username, Pass: cfg.Password,
		}.AsSha256Mechanism()))
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka consumer: %w", err)
	}
	return &Consumer{cfg: cfg, cl: cl, prod: prod, log: log}, nil
}

// Run consumes until ctx is cancelled. Delivery is at-least-once: offsets
// are committed after the handler succeeds (or after DLQ publish).
func (c *Consumer) Run(ctx context.Context, h Handler) error {
	defer c.cl.Close()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		polls := c.cl.PollFetches(pollCtx)
		cancel()
		if polls.IsClientClosed() {
			return nil
		}
		for _, e := range polls.Errors() {
			if !errors.Is(e.Err, context.DeadlineExceeded) && !errors.Is(e.Err, context.Canceled) {
				c.log.Warn("kafka fetch error", "topic", e.Topic, "err", e.Err)
			}
		}

		records := polls.Records()
		for _, rec := range records {
			c.handleOne(ctx, rec, h)
		}
		if len(records) > 0 {
			if err := c.cl.CommitUncommittedOffsets(ctx); err != nil {
				c.log.Error("commit offsets failed", "err", err)
				return err
			}
		}
	}
}

func (c *Consumer) handleOne(ctx context.Context, rec *kgo.Record, h Handler) {
	var lastErr error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		if err := h(ctx, Message{
			Topic: rec.Topic, Partition: rec.Partition,
			Offset: rec.Offset, Key: rec.Key, Value: rec.Value,
		}); err != nil {
			lastErr = err
			c.log.Warn("handler failed, will retry",
				"topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset,
				"attempt", attempt, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
			continue
		}
		return
	}
	c.deadLetter(ctx, rec, lastErr)
}

func (c *Consumer) deadLetter(ctx context.Context, rec *kgo.Record, cause error) {
	dlq := evt.DLQTopic(c.cfg.Group)
	payload := rec.Value
	if err := c.prod.Publish(ctx, dlq, string(rec.Key), payload); err != nil {
		// Do not commit: the record stays on the main topic and will be
		// re-consumed on restart rather than silently dropped.
		c.log.Error("DLQ publish failed; message left unconsumed",
			"dlq", dlq, "err", err)
		return
	}
	c.log.Error("message dead-lettered",
		"topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset,
		"dlq", dlq, "cause", cause)
}
