package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"market-master/pkg/psql"
)

type Intent struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id,omitempty"`
	OrderID       string    `json:"order_id"`
	CustomerID    string    `json:"customer_id"`
	AmountCents   int64     `json:"amount_cents"`
	Currency      string    `json:"currency"`
	Status        string    `json:"status"`
	Psp           string    `json:"psp"`
	PspRef        string    `json:"psp_ref"`
	PspToken      string    `json:"-"`
	Brand         string    `json:"brand"`
	Last4         string    `json:"last4"`
	FailureReason string    `json:"failure_reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

var (
	ErrNotFound       = errors.New("payment not found")
	ErrAlreadySettled = errors.New("payment for this order is already settled")
	ErrNotCapturable  = errors.New("payment is not in an authorizable/capturable state")
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type Attempt struct {
	OrderID       string
	CustomerID    string
	AmountCents   int64
	Currency      string
	Psp           string
	PspRef        string
	PspToken      string
	Brand         string
	Last4         string
	Status        string // authorized | captured | failed
	FailureReason string
}

// UpsertAttempt records a payment attempt for an order. One intent row per
// order: a settled (authorized/captured) row is never overwritten, which
// makes concurrent duplicate charges impossible at the domain level. Failed
// attempts are replaced in place so retries keep history simple.
// alreadySettled=true means the caller must not charge and should return
// the existing intent instead.
func (s *Store) UpsertAttempt(ctx context.Context, tenantID string, in Attempt,
	beforeCommit func(tx pgx.Tx, it Intent) error) (it Intent, alreadySettled bool, err error) {
	err = psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var existing Intent
		findErr := scanIntent(tx.QueryRow(ctx, intentSelect+`
			WHERE tenant_id = $1 AND order_id = $2 FOR UPDATE`, tenantID, in.OrderID), &existing)
		switch {
		case findErr == nil && (existing.Status == "authorized" || existing.Status == "captured" ||
			existing.Status == "refunded"):
			it = existing
			alreadySettled = true
			return nil
		case findErr == nil:
			// failed/voided: retry replaces the attempt in place
			if err := scanInto(tx.QueryRow(ctx, `
				UPDATE payment_intents SET
					customer_id = $3, amount_cents = $4, currency = $5, psp = $6,
					psp_ref = $7, psp_token = $8, brand = $9, last4 = $10,
					status = $11, failure_reason = $12, updated_at = now()
				WHERE tenant_id = $1 AND order_id = $2
				RETURNING `+intentColumns,
				tenantID, in.OrderID, in.CustomerID, in.AmountCents, in.Currency,
				in.Psp, in.PspRef, in.PspToken, in.Brand, in.Last4,
				in.Status, in.FailureReason), &it); err != nil {
				return err
			}
		case errors.Is(findErr, pgx.ErrNoRows):
			if err := scanInto(tx.QueryRow(ctx, `
				INSERT INTO payment_intents (tenant_id, order_id, customer_id, amount_cents,
					currency, status, psp, psp_ref, psp_token, brand, last4, failure_reason)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
				RETURNING `+intentColumns,
				tenantID, in.OrderID, in.CustomerID, in.AmountCents, in.Currency,
				in.Status, in.Psp, in.PspRef, in.PspToken, in.Brand, in.Last4, in.FailureReason), &it); err != nil {
				return err
			}
		default:
			return findErr
		}
		if in.PspRef != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO psp_refs (psp_ref, tenant_id, intent_id)
				VALUES ($1, $2, $3) ON CONFLICT (psp_ref) DO NOTHING`,
				in.PspRef, tenantID, it.ID); err != nil {
				return err
			}
		}
		if beforeCommit != nil {
			return beforeCommit(tx, it)
		}
		return nil
	})
	if err != nil {
		return Intent{}, false, err
	}
	return it, alreadySettled, nil
}

// ResolveTenantByPspRef maps a provider reference to the owning tenant so
// webhooks (which carry no tenant context) can enter tenant-scoped paths.
func (s *Store) ResolveTenantByPspRef(ctx context.Context, pspRef string) (string, error) {
	var tenantID string
	err := s.pool.QueryRow(ctx,
		`SELECT tenant_id::text FROM psp_refs WHERE psp_ref = $1`, pspRef).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return tenantID, err
}

// AuditWebhook records the raw provider event (best effort).
func (s *Store) AuditWebhook(ctx context.Context, psp, eventID, eventType string, payload []byte) {
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO psp_webhooks (psp, event_id, event_type, payload)
		VALUES ($1, $2, $3, $4) ON CONFLICT (psp, event_id) DO NOTHING`,
		psp, eventID, eventType, payload)
}

func (s *Store) GetByOrder(ctx context.Context, tenantID, orderID string) (Intent, bool, error) {
	var it Intent
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanIntent(tx.QueryRow(ctx, intentSelect+`
			WHERE tenant_id = $1 AND order_id = $2`, tenantID, orderID), &it)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Intent{}, false, nil
	}
	if err != nil {
		return Intent{}, false, err
	}
	return it, true, nil
}

func (s *Store) GetByID(ctx context.Context, tenantID, id string) (Intent, error) {
	var it Intent
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanIntent(tx.QueryRow(ctx, intentSelect+`
			WHERE tenant_id = $1 AND id = $2`, tenantID, id), &it)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Intent{}, ErrNotFound
	}
	return it, err
}

// Capture moves authorized → captured (beforeCommit emits order.paid).
func (s *Store) Capture(ctx context.Context, tenantID, id string,
	beforeCommit func(tx pgx.Tx, it Intent) error) (Intent, bool, error) {
	var it Intent
	already := false
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := scanIntent(tx.QueryRow(ctx, intentSelect+`
			WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id), &it); err != nil {
			return err
		}
		switch it.Status {
		case "captured":
			already = true
			return nil
		case "authorized":
		default:
			return ErrNotCapturable
		}
		if err := scanInto(tx.QueryRow(ctx, `
			UPDATE payment_intents SET status = 'captured', updated_at = now()
			WHERE tenant_id = $1 AND id = $2 RETURNING `+intentColumns, tenantID, id), &it); err != nil {
			return err
		}
		if beforeCommit != nil {
			return beforeCommit(tx, it)
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNotCapturable) {
		if errors.Is(err, pgx.ErrNoRows) {
			return Intent{}, false, ErrNotFound
		}
		return Intent{}, false, err
	}
	return it, already, err
}

// ApplyOrderCancelled voids an authorized payment when its order is
// cancelled. Idempotent: dedupes on event_id and no-ops unless status is
// authorized. Returns whether a void happened.
func (s *Store) ApplyOrderCancelled(ctx context.Context, tenantID, orderID, eventID, group string,
	void func(pspRef string) error) (bool, error) {
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
		var it Intent
		if err := scanIntent(tx.QueryRow(ctx, intentSelect+`
			WHERE tenant_id = $1 AND order_id = $2 FOR UPDATE`, tenantID, orderID), &it); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // no payment was ever made: nothing to void
			}
			return err
		}
		if it.Status != "authorized" {
			return nil
		}
		if it.PspRef != "" {
			if err := void(it.PspRef); err != nil {
				return err // retried by the consumer, then dead-lettered
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE payment_intents SET status = 'voided', updated_at = now()
			WHERE tenant_id = $1 AND id = $2`, tenantID, it.ID); err != nil {
			return err
		}
		did = true
		return nil
	})
	return did, err
}

type ListFilter struct {
	OrderID    string
	CustomerID string
}

func (s *Store) List(ctx context.Context, tenantID string, f ListFilter) ([]Intent, error) {
	var out []Intent
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, intentSelect+`
			WHERE tenant_id = $1
				AND ($2 = '' OR order_id = $2)
				AND ($3 = '' OR customer_id = $3)
			ORDER BY created_at DESC LIMIT 100`, tenantID, f.OrderID, f.CustomerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it Intent
			if err := scanIntent(rows, &it); err != nil {
				return err
			}
			out = append(out, it)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Intent{}
	}
	return out, err
}

// ApplyWebhookEvent applies a deduped provider webhook transition
// (e.g. payment.captured → captured). Returns false when the event was a
// duplicate or not applicable.
func (s *Store) ApplyWebhookEvent(ctx context.Context, tenantID, pspRef, eventID, group, toStatus string,
	beforeCommit func(tx pgx.Tx, it Intent) error) (bool, error) {
	applied := false
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO consumed_events (event_id, consumer_group, event_type)
			VALUES ($1, $2, $3) ON CONFLICT (event_id) DO NOTHING`,
			eventID, group, "psp_webhook:"+toStatus)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		var it Intent
		if err := scanIntent(tx.QueryRow(ctx, intentSelect+`
			WHERE tenant_id = $1 AND psp_ref = $2 FOR UPDATE`, tenantID, pspRef), &it); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // unknown ref: record event, nothing to do
			}
			return err
		}
		legal := (toStatus == "captured" && it.Status == "authorized") ||
			(toStatus == "refunded" && it.Status == "captured")
		if !legal {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE payment_intents SET status = $3, updated_at = now()
			WHERE tenant_id = $1 AND id = $2`, tenantID, it.ID, toStatus); err != nil {
			return err
		}
		it.Status = toStatus
		if beforeCommit != nil {
			return beforeCommit(tx, it)
		}
		applied = true
		return nil
	})
	return applied, err
}

const intentColumns = `id, tenant_id, order_id, customer_id, amount_cents, currency,
	status, psp, psp_ref, psp_token, brand, last4, failure_reason, created_at, updated_at`

const intentSelect = `SELECT ` + intentColumns + ` FROM payment_intents`

func scanIntent(row pgx.Row, it *Intent) error {
	return row.Scan(&it.ID, &it.TenantID, &it.OrderID, &it.CustomerID, &it.AmountCents,
		&it.Currency, &it.Status, &it.Psp, &it.PspRef, &it.PspToken, &it.Brand,
		&it.Last4, &it.FailureReason, &it.CreatedAt, &it.UpdatedAt)
}

type scannable interface{ Scan(dest ...any) error }

func scanInto(row scannable, it *Intent) error {
	return row.Scan(&it.ID, &it.TenantID, &it.OrderID, &it.CustomerID, &it.AmountCents,
		&it.Currency, &it.Status, &it.Psp, &it.PspRef, &it.PspToken, &it.Brand,
		&it.Last4, &it.FailureReason, &it.CreatedAt, &it.UpdatedAt)
}
