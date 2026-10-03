package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"market-master/pkg/evt"
)

const schema = `
CREATE TABLE IF NOT EXISTS outbox (
	id          BIGSERIAL PRIMARY KEY,
	topic       TEXT NOT NULL,
	partition_key TEXT NOT NULL,
	payload     BYTEA NOT NULL,
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	published_at TIMESTAMPTZ,
	attempts    INT NOT NULL DEFAULT 0,
	last_error  TEXT
);
CREATE INDEX IF NOT EXISTS outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;
`

// EnsureSchema creates the outbox table (call from service migrations).
func EnsureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schema)
	return err
}

// Writer inserts outbox rows inside the caller's transaction, giving atomic
// "state change + event emission" (no dual-write problem).
type Writer struct{}

func (Writer) Insert(ctx context.Context, tx pgx.Tx, topic, key string, env evt.Envelope) error {
	b, err := evt.Encode(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO outbox (topic, partition_key, payload) VALUES ($1, $2, $3)`,
		topic, key, b)
	if err != nil {
		return fmt.Errorf("outbox insert: %w", err)
	}
	return nil
}

// Publisher abstracts the Kafka producer for testability.
type Publisher interface {
	Publish(ctx context.Context, topic, key string, payload []byte) error
}

// Relay polls unpublished outbox rows and publishes them, in order per
// partition key thanks to the serial id ordering. At-least-once: a crash
// after publish but before marking published re-publishes; consumers must
// be idempotent (they are: event_id dedupe + state-machine guards).
type Relay struct {
	pool     *pgxpool.Pool
	pub      Publisher
	interval time.Duration
	batch    int
	log      *slog.Logger
}

type RelayConfig struct {
	Interval time.Duration
	Batch    int
	Logger   *slog.Logger
}

func NewRelay(pool *pgxpool.Pool, pub Publisher, cfg RelayConfig) *Relay {
	if cfg.Interval <= 0 {
		cfg.Interval = 200 * time.Millisecond
	}
	if cfg.Batch <= 0 {
		cfg.Batch = 100
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Relay{pool: pool, pub: pub, interval: cfg.Interval, batch: cfg.Batch, log: cfg.Logger}
}

func (r *Relay) Run(ctx context.Context) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.flush(ctx); err != nil && !errors.Is(err, context.Canceled) {
				r.log.Error("outbox flush failed", "err", err)
			}
		}
	}
}

func (r *Relay) flush(ctx context.Context) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id, topic, partition_key, payload
		FROM outbox
		WHERE published_at IS NULL
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, r.batch)
	if err != nil {
		return err
	}
	type item struct {
		id         int64
		topic, key string
		payload    []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.topic, &it.key, &it.payload); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(items) == 0 {
		return tx.Commit(ctx)
	}

	for _, it := range items {
		if err := r.pub.Publish(ctx, it.topic, it.key, it.payload); err != nil {
			_, uerr := tx.Exec(ctx,
				`UPDATE outbox SET attempts = attempts + 1, last_error = $2 WHERE id = $1`,
				it.id, err.Error())
			if uerr != nil {
				return uerr
			}
			return fmt.Errorf("publish topic=%s key=%s: %w", it.topic, it.key, err)
		}
	}
	ids := make([]int64, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.id)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE outbox SET published_at = now() WHERE id = ANY($1)`, ids); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
