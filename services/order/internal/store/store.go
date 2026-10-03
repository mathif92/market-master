package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"market-master/pkg/psql"
	"market-master/services/order/internal/sm"
)

type Order struct {
	ID                 string    `json:"id"`
	TenantID           string    `json:"tenant_id,omitempty"`
	CustomerID         string    `json:"customer_id"`
	Status             string    `json:"status"`
	TotalCents         int64     `json:"total_cents"`
	Currency           string    `json:"currency"`
	ShippingMethodCode string    `json:"shipping_method_code"`
	RecipientName      string    `json:"recipient_name"`
	AddressLine        string    `json:"address_line"`
	City               string    `json:"city"`
	Country            string    `json:"country"`
	PostalCode         string    `json:"postal_code"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type Line struct {
	OrderID        string `json:"order_id"`
	ProductID      string `json:"product_id"`
	Name           string `json:"name"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
}

type OrderWithLines struct {
	Order
	Lines []Line `json:"lines"`
}

type CreateInput struct {
	CustomerID         string
	TotalCents         int64
	Currency           string
	ShippingMethodCode string
	RecipientName      string
	AddressLine        string
	City               string
	Country            string
	PostalCode         string
	Lines              []Line
}

var (
	ErrNotFound       = errors.New("order not found")
	ErrNotCancellable = errors.New("order is not cancellable in its current state")
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Create inserts the order and its lines. beforeCommit runs in the same
// transaction (outbox event + idempotency completion commit atomically
// with the order).
func (s *Store) Create(ctx context.Context, tenantID string, in CreateInput,
	beforeCommit func(tx pgx.Tx, o Order) error) (Order, error) {
	var o Order
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO orders (tenant_id, customer_id, total_cents, currency,
				shipping_method_code, recipient_name, address_line, city, country, postal_code)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			RETURNING id, tenant_id, customer_id, status, total_cents, currency,
				shipping_method_code, recipient_name, address_line, city, country,
				postal_code, created_at, updated_at`,
			tenantID, in.CustomerID, in.TotalCents, in.Currency,
			in.ShippingMethodCode, in.RecipientName, in.AddressLine, in.City,
			in.Country, in.PostalCode).
			Scan(&o.ID, &o.TenantID, &o.CustomerID, &o.Status, &o.TotalCents, &o.Currency,
				&o.ShippingMethodCode, &o.RecipientName, &o.AddressLine, &o.City,
				&o.Country, &o.PostalCode, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return err
		}
		for _, l := range in.Lines {
			if _, err := tx.Exec(ctx, `
				INSERT INTO order_lines (order_id, tenant_id, product_id, name, quantity, unit_price_cents)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				o.ID, tenantID, l.ProductID, l.Name, l.Quantity, l.UnitPriceCents); err != nil {
				return err
			}
		}
		if beforeCommit != nil {
			return beforeCommit(tx, o)
		}
		return nil
	})
	return o, err
}

func (s *Store) Get(ctx context.Context, tenantID, id string) (OrderWithLines, error) {
	var out OrderWithLines
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := scanOrder(tx.QueryRow(ctx, orderSelect+` WHERE id = $1 AND tenant_id = $2`, id, tenantID),
			&out.Order); err != nil {
			return err
		}
		lines, err := scanLines(ctx, tx, out.ID)
		if err != nil {
			return err
		}
		out.Lines = lines
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderWithLines{}, ErrNotFound
	}
	return out, err
}

type ListFilter struct {
	CustomerID string // empty = all customers (staff)
	Status     string
}

func (s *Store) List(ctx context.Context, tenantID string, f ListFilter) ([]Order, error) {
	var out []Order
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, orderSelect+`
			WHERE tenant_id = $1
				AND ($2 = '' OR customer_id = $2)
				AND ($3 = '' OR status = $3)
			ORDER BY created_at DESC
			LIMIT 200`, tenantID, f.CustomerID, f.Status)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o Order
			if err := scanOrder(rows, &o); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Order{}
	}
	return out, err
}

// Cancel transitions to cancelled if the state machine allows it.
// alreadyCancelled=true is returned (with no error) when the order is
// already cancelled — the API stays idempotent on retries.
func (s *Store) Cancel(ctx context.Context, tenantID, id string,
	beforeCommit func(tx pgx.Tx, o Order) error) (o Order, alreadyCancelled bool, err error) {
	err = psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := scanOrder(tx.QueryRow(ctx, orderSelect+` WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, tenantID),
			&o); err != nil {
			return err
		}
		cur, perr := sm.Parse(o.Status)
		if perr != nil {
			return perr
		}
		if cur == sm.Cancelled {
			alreadyCancelled = true
			return nil
		}
		if !sm.CanTransition(cur, sm.Cancelled) {
			return ErrNotCancellable
		}
		if err := tx.QueryRow(ctx, `
			UPDATE orders SET status = 'cancelled', updated_at = now()
			WHERE id = $1 AND tenant_id = $2
			RETURNING `+orderColumns, id, tenantID).
			Scan(&o.ID, &o.TenantID, &o.CustomerID, &o.Status, &o.TotalCents, &o.Currency,
				&o.ShippingMethodCode, &o.RecipientName, &o.AddressLine, &o.City,
				&o.Country, &o.PostalCode, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return err
		}
		if beforeCommit != nil {
			return beforeCommit(tx, o)
		}
		return nil
	})
	return o, alreadyCancelled, err
}

// ApplyEvent performs the idempotent consumer-side state transition:
// it deduplicates on event_id and validates the transition through the
// state machine, all in one transaction. Returns whether the transition
// was applied (false = duplicate or illegal transition, both harmless).
func (s *Store) ApplyEvent(ctx context.Context, tenantID, orderID string, to sm.Status,
	eventID, group, eventType string) (bool, error) {
	applied := false
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO consumed_events (event_id, consumer_group, event_type)
			VALUES ($1, $2, $3) ON CONFLICT (event_id) DO NOTHING`,
			eventID, group, eventType)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // duplicate delivery: already applied
		}
		var status string
		err = tx.QueryRow(ctx,
			`SELECT status FROM orders WHERE id = $1 AND tenant_id = $2 FOR UPDATE`,
			orderID, tenantID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		from, err := sm.Parse(status)
		if err != nil {
			return err
		}
		if from == to || !sm.CanTransition(from, to) {
			return nil // illegal/redundant transition: no-op by design
		}
		if _, err := tx.Exec(ctx, `
			UPDATE orders SET status = $3, updated_at = now()
			WHERE id = $1 AND tenant_id = $2`, orderID, tenantID, string(to)); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

const orderColumns = `id, tenant_id, customer_id, status, total_cents, currency,
	shipping_method_code, recipient_name, address_line, city, country,
	postal_code, created_at, updated_at`

const orderSelect = `SELECT ` + orderColumns + ` FROM orders`

func scanOrder(row pgx.Row, o *Order) error {
	return row.Scan(&o.ID, &o.TenantID, &o.CustomerID, &o.Status, &o.TotalCents,
		&o.Currency, &o.ShippingMethodCode, &o.RecipientName, &o.AddressLine,
		&o.City, &o.Country, &o.PostalCode, &o.CreatedAt, &o.UpdatedAt)
}

func scanLines(ctx context.Context, q pgx.Tx, orderID string) ([]Line, error) {
	rows, err := q.Query(ctx, `
		SELECT order_id::text, product_id, name, quantity, unit_price_cents
		FROM order_lines WHERE order_id = $1 ORDER BY id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Line
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.OrderID, &l.ProductID, &l.Name, &l.Quantity, &l.UnitPriceCents); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
