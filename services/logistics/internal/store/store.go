package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"market-master/pkg/psql"
)

type ShippingMethod struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenant_id,omitempty"`
	Code       string          `json:"code"`
	Name       string          `json:"name"`
	Dispatcher string          `json:"dispatcher"`
	IsDefault  bool            `json:"is_default"`
	Config     json.RawMessage `json:"config"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type Shipment struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id,omitempty"`
	OrderID       string    `json:"order_id"`
	CustomerID    string    `json:"customer_id"`
	MethodCode    string    `json:"method_code"`
	Dispatcher    string    `json:"dispatcher"`
	Status        string    `json:"status"`
	ProviderRef   string    `json:"provider_ref"`
	TrackingURL   string    `json:"tracking_url"`
	RecipientName string    `json:"recipient_name"`
	AddressLine   string    `json:"address_line"`
	City          string    `json:"city"`
	Country       string    `json:"country"`
	PostalCode    string    `json:"postal_code"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

var (
	ErrNotFound       = errors.New("resource not found")
	ErrCodeTaken      = errors.New("shipping method code already exists")
	ErrBadDispatcher  = errors.New("dispatcher must be own_fleet or third_party")
	ErrNotDeliverable = errors.New("shipment cannot be delivered in its current state")
	ErrNotCancellable = errors.New("shipment cannot be cancelled in its current state")
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) CreateMethod(ctx context.Context, tenantID string, m ShippingMethod) (ShippingMethod, error) {
	var out ShippingMethod
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if m.IsDefault {
			if _, err := tx.Exec(ctx,
				`UPDATE shipping_methods SET is_default = false, updated_at = now()
				 WHERE tenant_id = $1 AND is_default`, tenantID); err != nil {
				return err
			}
		}
		cfg := m.Config
		if len(cfg) == 0 {
			cfg = json.RawMessage(`{}`)
		}
		return scanMethod(tx.QueryRow(ctx, `
			INSERT INTO shipping_methods (tenant_id, code, name, dispatcher, is_default, config)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING `+methodColumns,
			tenantID, m.Code, m.Name, m.Dispatcher, m.IsDefault, cfg), &out)
	})
	return out, mapErr(err)
}

func (s *Store) ListMethods(ctx context.Context, tenantID string) ([]ShippingMethod, error) {
	var out []ShippingMethod
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, methodSelect+` WHERE tenant_id = $1 ORDER BY code`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m ShippingMethod
			if err := scanMethod(rows, &m); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if out == nil {
		out = []ShippingMethod{}
	}
	return out, err
}

func (s *Store) DeleteMethod(ctx context.Context, tenantID, id string) error {
	return psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM shipping_methods WHERE id = $1 AND tenant_id = $2`, id, tenantID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ResolveMethod picks the dispatcher for an order: explicit method code,
// else the tenant default, else own_fleet.
func (s *Store) ResolveMethod(ctx context.Context, tenantID, code string) (ShippingMethod, error) {
	var m ShippingMethod
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanMethod(tx.QueryRow(ctx, methodSelect+`
			WHERE tenant_id = $1 AND code = $2`, tenantID, code), &m)
	})
	if err == nil {
		return m, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ShippingMethod{}, err
	}
	// fallback: default method
	err = psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanMethod(tx.QueryRow(ctx, methodSelect+`
			WHERE tenant_id = $1 AND is_default`, tenantID), &m)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ShippingMethod{Dispatcher: "own_fleet", Code: "standard"}, nil
	}
	return m, err
}

type ShipmentInput struct {
	EventID       string
	Group         string
	OrderID       string
	CustomerID    string
	MethodCode    string
	Dispatcher    string
	ProviderRef   string
	TrackingURL   string
	RecipientName string
	AddressLine   string
	City          string
	Country       string
	PostalCode    string
}

// CreateShipment records a dispatch exactly once per order, atomically with
// the event dedupe row and the outbox emission. Returns created=false for
// replayed events or an already-existing shipment.
func (s *Store) CreateShipment(ctx context.Context, tenantID string, in ShipmentInput,
	fn func(tx pgx.Tx, sh Shipment) error) (Shipment, bool, error) {
	var sh Shipment
	created := false
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO consumed_events (event_id, consumer_group, event_type)
			VALUES ($1, $2, 'order.paid') ON CONFLICT (event_id) DO NOTHING`,
			in.EventID, in.Group)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // duplicate event delivery
		}
		tag, err = tx.Exec(ctx, `
			INSERT INTO shipments (tenant_id, order_id, customer_id, method_code,
				dispatcher, status, provider_ref, tracking_url,
				recipient_name, address_line, city, country, postal_code)
			VALUES ($1, $2, $3, $4, $5, 'dispatched', $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (tenant_id, order_id) DO NOTHING`,
			tenantID, in.OrderID, in.CustomerID, in.MethodCode, in.Dispatcher,
			in.ProviderRef, in.TrackingURL, in.RecipientName, in.AddressLine,
			in.City, in.Country, in.PostalCode)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // shipment already exists for this order
		}
		if err := scanShipment(tx.QueryRow(ctx, shipmentSelect+`
			WHERE tenant_id = $1 AND order_id = $2`, tenantID, in.OrderID), &sh); err != nil {
			return err
		}
		created = true
		if fn != nil {
			return fn(tx, sh)
		}
		return nil
	})
	return sh, created, err
}

func (s *Store) GetShipment(ctx context.Context, tenantID, id string) (Shipment, error) {
	var sh Shipment
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanShipment(tx.QueryRow(ctx, shipmentSelect+`
			WHERE tenant_id = $1 AND id = $2`, tenantID, id), &sh)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Shipment{}, ErrNotFound
	}
	return sh, err
}

type ShipmentFilter struct {
	OrderID    string
	CustomerID string
}

func (s *Store) ListShipments(ctx context.Context, tenantID string, f ShipmentFilter) ([]Shipment, error) {
	var out []Shipment
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, shipmentSelect+`
			WHERE tenant_id = $1
				AND ($2 = '' OR order_id = $2)
				AND ($3 = '' OR customer_id = $3)
			ORDER BY created_at DESC LIMIT 100`, tenantID, f.OrderID, f.CustomerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sh Shipment
			if err := scanShipment(rows, &sh); err != nil {
				return err
			}
			out = append(out, sh)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Shipment{}
	}
	return out, err
}

// MarkDelivered transitions to delivered (emitting shipment.delivered via
// fn inside the same transaction). Idempotent when already delivered.
func (s *Store) MarkDelivered(ctx context.Context, tenantID, id string,
	fn func(tx pgx.Tx, sh Shipment) error) (Shipment, bool, error) {
	var sh Shipment
	already := false
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := scanShipment(tx.QueryRow(ctx, shipmentSelect+`
			WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id), &sh); err != nil {
			return err
		}
		switch sh.Status {
		case "delivered":
			already = true
			return nil
		case "dispatched", "in_transit":
		default:
			return ErrNotDeliverable
		}
		if err := scanShipment(tx.QueryRow(ctx, `
			UPDATE shipments SET status = 'delivered', updated_at = now()
			WHERE tenant_id = $1 AND id = $2 RETURNING `+shipmentColumns,
			tenantID, id), &sh); err != nil {
			return err
		}
		if fn != nil {
			return fn(tx, sh)
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Shipment{}, false, ErrNotFound
	}
	return sh, already, err
}

// CancelShipment cancels locally after the caller cancelled with the
// provider (or the provider acknowledged it).
func (s *Store) CancelShipment(ctx context.Context, tenantID, id string,
	fn func(tx pgx.Tx, sh Shipment) error) (Shipment, bool, error) {
	var sh Shipment
	already := false
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := scanShipment(tx.QueryRow(ctx, shipmentSelect+`
			WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id), &sh); err != nil {
			return err
		}
		switch sh.Status {
		case "cancelled":
			already = true
			return nil
		case "dispatched", "in_transit":
		default:
			return ErrNotCancellable
		}
		if err := scanShipment(tx.QueryRow(ctx, `
			UPDATE shipments SET status = 'cancelled', updated_at = now()
			WHERE tenant_id = $1 AND id = $2 RETURNING `+shipmentColumns,
			tenantID, id), &sh); err != nil {
			return err
		}
		if fn != nil {
			return fn(tx, sh)
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Shipment{}, false, ErrNotFound
	}
	return sh, already, err
}

// CancelByOrder handles order.cancelled: dedupes the event, cancels the
// shipment if it is still cancellable, and invokes cancelRemote (provider
// call) only when there is something to cancel.
func (s *Store) CancelByOrder(ctx context.Context, tenantID, orderID, eventID, group string,
	cancelRemote func(sh Shipment) error) (bool, error) {
	did := false
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO consumed_events (event_id, consumer_group, event_type)
			VALUES ($1, $2, 'order.cancelled') ON CONFLICT (event_id) DO NOTHING`,
			eventID, group)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		var sh Shipment
		if err := scanShipment(tx.QueryRow(ctx, shipmentSelect+`
			WHERE tenant_id = $1 AND order_id = $2 FOR UPDATE`, tenantID, orderID), &sh); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // nothing was dispatched
			}
			return err
		}
		if sh.Status != "dispatched" && sh.Status != "in_transit" {
			return nil
		}
		if cancelRemote != nil && sh.ProviderRef != "" {
			if err := cancelRemote(sh); err != nil {
				return err // consumer retries, then dead-letters
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE shipments SET status = 'cancelled', updated_at = now()
			WHERE tenant_id = $1 AND id = $2`, tenantID, sh.ID); err != nil {
			return err
		}
		did = true
		return nil
	})
	return did, err
}

// EventSeen is a cheap replay check before performing provider side effects.
func (s *Store) EventSeen(ctx context.Context, eventID string) (bool, error) {
	var seen bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM consumed_events WHERE event_id = $1)`, eventID).Scan(&seen)
	return seen, err
}

const methodColumns = `id, tenant_id, code, name, dispatcher, is_default, config, created_at, updated_at`
const methodSelect = `SELECT ` + methodColumns + ` FROM shipping_methods`

const shipmentColumns = `id, tenant_id, order_id, customer_id, method_code, dispatcher, status,
	provider_ref, tracking_url, recipient_name, address_line, city, country, postal_code,
	created_at, updated_at`
const shipmentSelect = `SELECT ` + shipmentColumns + ` FROM shipments`

func scanMethod(row pgx.Row, m *ShippingMethod) error {
	return row.Scan(&m.ID, &m.TenantID, &m.Code, &m.Name, &m.Dispatcher, &m.IsDefault,
		&m.Config, &m.CreatedAt, &m.UpdatedAt)
}

func scanShipment(row pgx.Row, sh *Shipment) error {
	return row.Scan(&sh.ID, &sh.TenantID, &sh.OrderID, &sh.CustomerID, &sh.MethodCode,
		&sh.Dispatcher, &sh.Status, &sh.ProviderRef, &sh.TrackingURL,
		&sh.RecipientName, &sh.AddressLine, &sh.City, &sh.Country, &sh.PostalCode,
		&sh.CreatedAt, &sh.UpdatedAt)
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrCodeTaken
		case "23503":
			return ErrNotFound
		}
	}
	return err
}
