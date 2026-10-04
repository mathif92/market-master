package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"market-master/pkg/psql"
)

// StockLevel is one product's current inventory state. Tracked=false means
// the product has no stock_levels row (untracked = unlimited until first
// ingest); OnHand/Reserved read 0 for such products.
type StockLevel struct {
	ProductID         string    `json:"product_id"`
	ProductSlug       string    `json:"product_slug"`
	ProductName       string    `json:"product_name"`
	Tracked           bool      `json:"tracked"`
	OnHand            int64     `json:"on_hand"`
	Reserved          int64     `json:"reserved"`
	Available         int64     `json:"available"`
	LowStockThreshold int       `json:"low_stock_threshold"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
}

type StockMovement struct {
	ID          string    `json:"id"`
	ProductID   string    `json:"product_id"`
	ProductName string    `json:"product_name"`
	Source      string    `json:"source"`
	Kind        string    `json:"kind"`
	QtyDelta    int64     `json:"qty_delta"`
	OnHandAfter int64     `json:"on_hand_after"`
	BatchKey    string    `json:"batch_key,omitempty"`
	RefID       string    `json:"ref_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// IngestItem is one normalized row of an ingestion batch. SKU holds either
// the product slug or the product id.
type IngestItem struct {
	Row      int    `json:"row"`
	SKU      string `json:"sku"`
	Quantity int64  `json:"quantity"`
}

type RowError struct {
	Row   int    `json:"row"`
	SKU   string `json:"sku"`
	Error string `json:"error"`
}

type IngestResult struct {
	BatchID     string     `json:"batch_id"`
	Replayed    bool       `json:"replayed"`
	Mode        string     `json:"mode"`
	TotalRows   int        `json:"total_rows"`
	AppliedRows int        `json:"applied_rows"`
	ErrorRows   int        `json:"error_rows"`
	Errors      []RowError `json:"errors"`
}

type Shortage struct {
	ProductID string `json:"product_id"`
	Name      string `json:"name,omitempty"`
	Available int64  `json:"available"`
	Wanted    int32  `json:"wanted"`
}

// InsufficientStockError aborts the reserve transaction without changing
// anything; the shortages tell the caller exactly what is missing.
type InsufficientStockError struct{ Shortages []Shortage }

func (e *InsufficientStockError) Error() string {
	return fmt.Sprintf("insufficient stock for %d product(s)", len(e.Shortages))
}

// ApplyIngest atomically applies one ingestion batch. batchKey is the
// idempotency contract: a replay of the same (tenant, batch_key) applies
// nothing and reports the original counts. Row errors (unknown SKU, would
// break the available invariant) are collected per row and never abort the
// valid rows in the batch. beforeCommit (optional) runs in the same tx —
// used to complete an idempotency reservation together with the batch.
func (s *Store) ApplyIngest(ctx context.Context, tenantID, batchKey, source, mode string,
	items []IngestItem, beforeCommit func(tx pgx.Tx, res IngestResult) error) (IngestResult, error) {
	if mode != "set" && mode != "delta" {
		return IngestResult{}, fmt.Errorf("mode must be set or delta, got %q", mode)
	}
	switch source {
	case "upload", "webhook", "api", "adjustment":
	default:
		return IngestResult{}, fmt.Errorf("invalid source %q", source)
	}
	out := IngestResult{Mode: mode, TotalRows: len(items), Errors: []RowError{}}
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var batchID string
		err := tx.QueryRow(ctx, `
			INSERT INTO stock_ingest_batches (tenant_id, batch_key, source, mode, total_rows)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, batch_key) DO NOTHING
			RETURNING id`,
			tenantID, batchKey, source, mode, len(items)).
			Scan(&batchID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Replay: report the original outcome, change nothing.
			// Per-row error details are not stored; counts are.
			var errorRows int
			if err := tx.QueryRow(ctx, `
				SELECT id, total_rows, applied_rows, error_rows
				FROM stock_ingest_batches WHERE tenant_id = $1 AND batch_key = $2`,
				tenantID, batchKey).
				Scan(&out.BatchID, &out.TotalRows, &out.AppliedRows, &errorRows); err != nil {
				return err
			}
			out.Replayed = true
			out.ErrorRows = errorRows
			if beforeCommit != nil {
				return beforeCommit(tx, out)
			}
			return nil
		}
		if err != nil {
			return err
		}
		out.BatchID = batchID

		products, err := resolveSKUs(ctx, tx, tenantID, items)
		if err != nil {
			return err
		}
		for _, it := range items {
			p, ok := products[it.SKU]
			if !ok {
				out.Errors = append(out.Errors, RowError{Row: it.Row, SKU: it.SKU,
					Error: "unknown product"})
				continue
			}
			if _, err := applyIngestRow(ctx, tx, tenantID, p.ID, source, mode, it, batchKey); err != nil {
				out.Errors = append(out.Errors, RowError{Row: it.Row, SKU: it.SKU, Error: err.Error()})
				continue
			}
			out.AppliedRows++
		}
		out.ErrorRows = len(out.Errors)
		_, err = tx.Exec(ctx, `
			UPDATE stock_ingest_batches
			SET total_rows = $3, applied_rows = $4, error_rows = $5
			WHERE id = $1 AND tenant_id = $2`,
			batchID, tenantID, len(items), out.AppliedRows, out.ErrorRows)
		if err != nil {
			return err
		}
		if beforeCommit != nil {
			return beforeCommit(tx, out)
		}
		return nil
	})
	if err != nil {
		return IngestResult{}, err
	}
	return out, nil
}

// resolveSKUs resolves every referenced slug/id in one query.
func resolveSKUs(ctx context.Context, tx pgx.Tx, tenantID string,
	items []IngestItem) (map[string]Product, error) {
	keys := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if it.SKU == "" || seen[it.SKU] {
			continue
		}
		seen[it.SKU] = true
		keys = append(keys, it.SKU)
	}
	out := make(map[string]Product, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, slug, name FROM products
		WHERE tenant_id = $1 AND (slug = ANY($2) OR id::text = ANY($2))`,
		tenantID, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name); err != nil {
			return nil, err
		}
		out[p.Slug] = p
		out[p.ID] = p
	}
	return out, rows.Err()
}

// applyIngestRow validates one row under a row lock, then upserts the level
// and appends the ledger movement. Returns a caller-safe error for
// per-row problems (never a transaction abort).
func applyIngestRow(ctx context.Context, tx pgx.Tx, tenantID, productID, source, mode string,
	it IngestItem, batchKey string) (bool, error) {
	var onHand, reserved int64
	err := tx.QueryRow(ctx, `
		SELECT on_hand, reserved FROM stock_levels
		WHERE tenant_id = $1 AND product_id = $2
		FOR UPDATE`, tenantID, productID).
		Scan(&onHand, &reserved)
	if errors.Is(err, pgx.ErrNoRows) {
		onHand, reserved = 0, 0
	} else if err != nil {
		return false, err
	}

	var newOnHand int64
	if mode == "set" {
		newOnHand = it.Quantity
	} else {
		newOnHand = onHand + it.Quantity
	}
	if newOnHand < 0 {
		return false, fmt.Errorf("would set on_hand to %d (negative)", newOnHand)
	}
	if newOnHand < reserved {
		return false, fmt.Errorf(
			"would set on_hand to %d below reserved %d", newOnHand, reserved)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO stock_levels (tenant_id, product_id, on_hand, reserved)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, product_id)
		DO UPDATE SET on_hand = $3, updated_at = now()`,
		tenantID, productID, newOnHand, reserved); err != nil {
		return false, err
	}
	kind := "adjust"
	if mode == "delta" && it.Quantity > 0 {
		kind = "in"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO stock_movements
			(tenant_id, product_id, source, kind, qty_delta, on_hand_after, batch_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		tenantID, productID, source, kind, newOnHand-onHand, newOnHand,
		batchKey); err != nil {
		return false, err
	}
	return true, nil
}

type ReserveLine struct {
	ProductID string `json:"product_id"`
	Quantity  int32  `json:"quantity"`
}

// PriceAndReserve prices the order under live campaigns, then validates
// availability for every tracked line and bumps reserved + inserts
// order-keyed reservation rows — all-or-nothing in one transaction (a
// stock shortage rolls back any redemption/counter bump with it).
//
// Pricing runs first so coupon/campaign errors (422 invalid_coupon /
// campaign_limit) never touch stock. Untracked products (no stock_levels
// row) pass without a reservation. Returns *InsufficientStockError,
// *ErrInvalidCoupon or *ErrCampaignLimit. Replays of the same order id
// (reservations or redemptions already exist) recompute prices but take no
// new holds and consume no campaign slots.
func (s *Store) PriceAndReserve(ctx context.Context, tenantID, orderID string,
	lines []ReserveLine, couponCode string) (ReserveResult, error) {
	if orderID == "" {
		return ReserveResult{}, fmt.Errorf("order_id is required to reserve stock")
	}
	var res ReserveResult
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var replay bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM stock_reservations
				WHERE tenant_id = $1 AND order_id = $2)
			OR EXISTS (
				SELECT 1 FROM campaign_redemptions
				WHERE tenant_id = $1 AND order_id = $2)`,
			tenantID, orderID).Scan(&replay); err != nil {
			return err
		}
		var err error
		res, err = priceLines(ctx, tx, tenantID, orderID, lines, couponCode, !replay)
		if err != nil {
			return err
		}
		if replay {
			return nil // same order id: pricing only, nothing to re-take
		}
		return reserveStockInTx(ctx, tx, tenantID, orderID, lines)
	})
	return res, err
}

// reserveStockInTx holds stock for the tracked lines. Lock order: product
// levels are locked in product_id order so concurrent orders can never
// deadlock against each other (campaign rows are already locked before this
// runs — global order is campaign → code → products sorted).
func reserveStockInTx(ctx context.Context, tx pgx.Tx, tenantID, orderID string,
	lines []ReserveLine) error {
	type hold struct {
		productID string
		qty       int32
		onHand    int64
		reserved  int64
		name      string
	}
	sorted := make([]ReserveLine, len(lines))
	copy(sorted, lines)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ProductID < sorted[j].ProductID })

	var holds []hold
	var shortages []Shortage
	for _, l := range sorted {
		var onHand, reserved int64
		var name string
		err := tx.QueryRow(ctx, `
			SELECT l.on_hand, l.reserved, p.name
			FROM stock_levels l
			JOIN products p ON p.id = l.product_id
			WHERE l.tenant_id = $1 AND l.product_id = $2
			FOR UPDATE OF l`, tenantID, l.ProductID).
			Scan(&onHand, &reserved, &name)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // untracked: unlimited, no reservation row
		}
		if err != nil {
			return err
		}
		if onHand-reserved < int64(l.Quantity) {
			shortages = append(shortages, Shortage{
				ProductID: l.ProductID, Name: name,
				Available: onHand - reserved, Wanted: l.Quantity,
			})
			continue
		}
		holds = append(holds, hold{productID: l.ProductID, qty: l.Quantity,
			onHand: onHand, reserved: reserved, name: name})
	}
	if len(shortages) > 0 {
		return &InsufficientStockError{Shortages: shortages}
	}
	for _, h := range holds {
		if _, err := tx.Exec(ctx, `
			UPDATE stock_levels SET reserved = reserved + $3, updated_at = now()
			WHERE tenant_id = $1 AND product_id = $2`,
			tenantID, h.productID, h.qty); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO stock_reservations (order_id, product_id, tenant_id, qty)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (order_id, product_id) DO NOTHING`,
			orderID, h.productID, tenantID, h.qty); err != nil {
			return err
		}
	}
	return nil
}

// ReleaseReservations drops stock holds AND campaign redemptions for an
// order that was never placed (order insert failed after reserve),
// recounting campaign counters from the ledger. Safe to call repeatedly.
func (s *Store) ReleaseReservations(ctx context.Context, tenantID, orderID string) error {
	return psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := releaseInTx(ctx, tx, tenantID, orderID); err != nil {
			return err
		}
		return releaseRedemptionsInTx(ctx, tx, tenantID, orderID)
	})
}

func releaseInTx(ctx context.Context, tx pgx.Tx, tenantID, orderID string) error {
	rows, err := tx.Query(ctx, `
		SELECT product_id, qty FROM stock_reservations
		WHERE tenant_id = $1 AND order_id = $2 AND status = 'reserved'
		FOR UPDATE`, tenantID, orderID)
	if err != nil {
		return err
	}
	type hold struct {
		id  string
		qty int32
	}
	var holds []hold
	for rows.Next() {
		var h hold
		if err := rows.Scan(&h.id, &h.qty); err != nil {
			rows.Close()
			return err
		}
		holds = append(holds, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, h := range holds {
		if _, err := tx.Exec(ctx, `
			UPDATE stock_levels
			SET reserved = GREATEST(reserved - $3, 0), updated_at = now()
			WHERE tenant_id = $1 AND product_id = $2`,
			tenantID, h.id, h.qty); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE stock_reservations
			SET status = 'released', updated_at = now()
			WHERE order_id = $1 AND product_id = $2 AND tenant_id = $3`,
			orderID, h.id, tenantID); err != nil {
			return err
		}
	}
	return nil
}

// FulfillReservations commits holds on order.paid: on_hand decreases and a
// 'sale' movement is appended. Idempotent via consumed_events (eventID)
// and via the reservation state machine (committed rows are skipped).
// Returns whether this call did the work.
func (s *Store) FulfillReservations(ctx context.Context, tenantID, orderID, eventID, group, eventType string) (bool, error) {
	applied := false
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		ok, err := markConsumed(ctx, tx, eventID, group, eventType)
		if err != nil {
			return err
		}
		if !ok {
			return nil // replayed event
		}
		rows, err := tx.Query(ctx, `
			SELECT product_id, qty FROM stock_reservations
			WHERE tenant_id = $1 AND order_id = $2 AND status = 'reserved'
			FOR UPDATE`, tenantID, orderID)
		if err != nil {
			return err
		}
		type hold struct {
			id  string
			qty int32
		}
		var holds []hold
		for rows.Next() {
			var h hold
			if err := rows.Scan(&h.id, &h.qty); err != nil {
				rows.Close()
				return err
			}
			holds = append(holds, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, h := range holds {
			var after int64
			if err := tx.QueryRow(ctx, `
				UPDATE stock_levels
				SET on_hand = GREATEST(on_hand - $3, 0),
				    reserved = GREATEST(reserved - $3, 0),
				    updated_at = now()
				WHERE tenant_id = $1 AND product_id = $2
				RETURNING on_hand`,
				tenantID, h.id, h.qty).Scan(&after); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE stock_reservations
				SET status = 'committed', updated_at = now()
				WHERE order_id = $1 AND product_id = $2 AND tenant_id = $3`,
				orderID, h.id, tenantID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO stock_movements
					(tenant_id, product_id, source, kind, qty_delta, on_hand_after, batch_key, ref_id)
				VALUES ($1, $2, 'order', 'sale', $3, $4, $5, $6::uuid)`,
				tenantID, h.id, -int64(h.qty), after, eventID, orderID); err != nil {
				return err
			}
			applied = true
		}
		return nil
	})
	return applied, err
}

// CancelReservations releases holds and campaign redemptions on
// order.cancelled (the ONLY release path — payment_failed keeps holds and
// slots for retry). A reservation that already committed (paid-then-cancelled
// edge) is restocked; campaign counters are recounted from the ledger.
// Idempotent via consumed_events and reservation/redemption state.
// Returns whether it did work.
func (s *Store) CancelReservations(ctx context.Context, tenantID, orderID, eventID, group, eventType string) (bool, error) {
	applied := false
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		ok, err := markConsumed(ctx, tx, eventID, group, eventType)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := releaseRedemptionsInTx(ctx, tx, tenantID, orderID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT product_id, qty, status FROM stock_reservations
			WHERE tenant_id = $1 AND order_id = $2 AND status <> 'released'
			FOR UPDATE`, tenantID, orderID)
		if err != nil {
			return err
		}
		type hold struct {
			id     string
			qty    int32
			status string
		}
		var holds []hold
		for rows.Next() {
			var h hold
			if err := rows.Scan(&h.id, &h.qty, &h.status); err != nil {
				rows.Close()
				return err
			}
			holds = append(holds, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, h := range holds {
			if h.status == "reserved" {
				if _, err := tx.Exec(ctx, `
					UPDATE stock_levels
					SET reserved = GREATEST(reserved - $3, 0), updated_at = now()
					WHERE tenant_id = $1 AND product_id = $2`,
					tenantID, h.id, h.qty); err != nil {
					return err
				}
			} else { // committed: restock it
				var after int64
				if err := tx.QueryRow(ctx, `
					UPDATE stock_levels
					SET on_hand = on_hand + $3, updated_at = now()
					WHERE tenant_id = $1 AND product_id = $2
					RETURNING on_hand`,
					tenantID, h.id, h.qty).Scan(&after); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO stock_movements
						(tenant_id, product_id, source, kind, qty_delta, on_hand_after, batch_key, ref_id)
					VALUES ($1, $2, 'order', 'restock', $3, $4, $5, $6::uuid)`,
					tenantID, h.id, int64(h.qty), after, eventID, orderID); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `
				UPDATE stock_reservations
				SET status = 'released', updated_at = now()
				WHERE order_id = $1 AND product_id = $2 AND tenant_id = $3`,
				orderID, h.id, tenantID); err != nil {
				return err
			}
			applied = true
		}
		return nil
	})
	return applied, err
}

// markConsumed records event_id for the consumer group; returns false when
// the event was already processed (replay).
func markConsumed(ctx context.Context, tx pgx.Tx, eventID, group, eventType string) (bool, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO consumed_events (event_id, consumer_group, event_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id) DO NOTHING
		RETURNING event_id`, eventID, group, eventType).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListStockLevels returns every product of the market with its stock state
// (tracked=false rows are untracked products).
func (s *Store) ListStockLevels(ctx context.Context, tenantID string) ([]StockLevel, error) {
	out := []StockLevel{}
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT p.id, p.slug, p.name,
			       l.product_id IS NOT NULL,
			       COALESCE(l.on_hand, 0), COALESCE(l.reserved, 0),
			       COALESCE(l.low_stock_threshold, 10), l.updated_at
			FROM products p
			LEFT JOIN stock_levels l
				ON l.product_id = p.id AND l.tenant_id = p.tenant_id
			WHERE p.tenant_id = $1
			ORDER BY p.name`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var lv StockLevel
			var updated *time.Time
			if err := rows.Scan(&lv.ProductID, &lv.ProductSlug, &lv.ProductName,
				&lv.Tracked, &lv.OnHand, &lv.Reserved, &lv.LowStockThreshold, &updated); err != nil {
				return err
			}
			lv.Available = lv.OnHand - lv.Reserved
			if updated != nil {
				lv.UpdatedAt = *updated
			}
			out = append(out, lv)
		}
		return rows.Err()
	})
	return out, err
}

// GetStockLevel returns a single product's stock state (Tracked=false when
// the product has no level row).
func (s *Store) GetStockLevel(ctx context.Context, tenantID, productID string) (StockLevel, error) {
	var lv StockLevel
	var updated *time.Time
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT p.id, p.slug, p.name, l.product_id IS NOT NULL,
			       COALESCE(l.on_hand, 0), COALESCE(l.reserved, 0),
			       COALESCE(l.low_stock_threshold, 10), l.updated_at
			FROM products p
			LEFT JOIN stock_levels l
				ON l.product_id = p.id AND l.tenant_id = p.tenant_id
			WHERE p.tenant_id = $1 AND p.id = $2`, tenantID, productID).
			Scan(&lv.ProductID, &lv.ProductSlug, &lv.ProductName, &lv.Tracked,
				&lv.OnHand, &lv.Reserved, &lv.LowStockThreshold, &updated)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return StockLevel{}, ErrNotFound
	}
	if err == nil {
		lv.Available = lv.OnHand - lv.Reserved
		if updated != nil {
			lv.UpdatedAt = *updated
		}
	}
	return lv, err
}

type MovementFilter struct {
	ProductID string
	Limit     int
}

func (s *Store) ListStockMovements(ctx context.Context, tenantID string, f MovementFilter) ([]StockMovement, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := []StockMovement{}
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT m.id::text, m.product_id::text, p.name, m.source, m.kind,
			       m.qty_delta, m.on_hand_after, m.batch_key,
			       COALESCE(m.ref_id::text, ''), m.created_at
			FROM stock_movements m
			JOIN products p ON p.id = m.product_id
			WHERE m.tenant_id = $1
				AND ($2 = '' OR m.product_id = $2::uuid)
			ORDER BY m.created_at DESC
			LIMIT $3`, tenantID, f.ProductID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var mv StockMovement
			if err := rows.Scan(&mv.ID, &mv.ProductID, &mv.ProductName, &mv.Source,
				&mv.Kind, &mv.QtyDelta, &mv.OnHandAfter, &mv.BatchKey,
				&mv.RefID, &mv.CreatedAt); err != nil {
				return err
			}
			out = append(out, mv)
		}
		return rows.Err()
	})
	return out, err
}

// StartTracking creates a level row (on_hand 0) for a product that was
// untracked, unless one already exists.
func (s *Store) StartTracking(ctx context.Context, tenantID, productID string, threshold int) error {
	if threshold < 0 {
		threshold = 10
	}
	return psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM stock_levels
				WHERE tenant_id = $1 AND product_id = $2)`,
			tenantID, productID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			_, err := tx.Exec(ctx, `
				UPDATE stock_levels SET low_stock_threshold = $3, updated_at = now()
				WHERE tenant_id = $1 AND product_id = $2`,
				tenantID, productID, threshold)
			return err
		}
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO stock_levels (tenant_id, product_id, on_hand, reserved, low_stock_threshold)
			VALUES ($1, $2, 0, 0, $3)
			RETURNING product_id::text`, tenantID, productID, threshold).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrNotFound // unknown product id
		}
		return err
	})
}
