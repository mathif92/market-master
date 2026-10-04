package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"market-master/pkg/psql"
)

type Category struct {
	ID        string    `json:"id"`
	ParentID  string    `json:"parent_id,omitempty"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Product struct {
	ID          string    `json:"id"`
	CategoryID  string    `json:"category_id,omitempty"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`
	PriceCents  int64     `json:"price_cents"` // canonical — never rewritten by campaigns
	Currency    string    `json:"currency"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Sale fields are computed at read time from live non-code campaigns;
	// absent (null) when no campaign applies. Use effectivePrice() = the
	// sale price when set, otherwise price_cents.
	SalePriceCents *int64           `json:"sale_price_cents,omitempty"`
	Campaign       *ProductCampaign `json:"campaign,omitempty"`
}

var (
	ErrSlugTaken   = errors.New("slug already exists")
	ErrNotFound    = errors.New("not found")
	ErrHasProducts = errors.New("category still has products")
	ErrBadParent   = errors.New("parent category not found")
	ErrParentCycle = errors.New("parent would create a cycle")
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) CreateCategory(ctx context.Context, tenantID, parentID, name, slug string) (Category, error) {
	var c Category
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO categories (tenant_id, parent_id, name, slug)
			VALUES ($1, NULLIF($2, '')::uuid, $3, $4)
			RETURNING id, COALESCE(parent_id::text, ''), name, slug, created_at, updated_at`,
			tenantID, parentID, name, slug).
			Scan(&c.ID, &c.ParentID, &c.Name, &c.Slug, &c.CreatedAt, &c.UpdatedAt)
	})
	return c, mapErr(err)
}

func (s *Store) ListCategories(ctx context.Context, tenantID string) ([]Category, error) {
	var out []Category
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, COALESCE(parent_id::text, ''), name, slug, created_at, updated_at
			FROM categories WHERE tenant_id = $1 ORDER BY name`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Category
			if err := rows.Scan(&c.ID, &c.ParentID, &c.Name, &c.Slug, &c.CreatedAt, &c.UpdatedAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Category{}
	}
	return out, err
}

func (s *Store) GetCategory(ctx context.Context, tenantID, id string) (Category, error) {
	var c Category
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id, COALESCE(parent_id::text, ''), name, slug, created_at, updated_at
			FROM categories WHERE id = $1 AND tenant_id = $2`, id, tenantID).
			Scan(&c.ID, &c.ParentID, &c.Name, &c.Slug, &c.CreatedAt, &c.UpdatedAt)
	})
	return c, mapErr(err)
}

func (s *Store) UpdateCategory(ctx context.Context, tenantID, id, name, parentID string) (Category, error) {
	var c Category
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if parentID != "" && parentID == id {
			return ErrParentCycle
		}
		var parent any
		if parentID != "" {
			parent = parentID
		}
		return tx.QueryRow(ctx, `
			UPDATE categories SET name = $3, parent_id = $4::uuid, updated_at = now()
			WHERE id = $1 AND tenant_id = $2
			RETURNING id, COALESCE(parent_id::text, ''), name, slug, created_at, updated_at`,
			id, tenantID, name, parent).
			Scan(&c.ID, &c.ParentID, &c.Name, &c.Slug, &c.CreatedAt, &c.UpdatedAt)
	})
	return c, mapErr(err)
}

func (s *Store) DeleteCategory(ctx context.Context, tenantID, id string) error {
	return psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var hasProducts bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM products WHERE category_id = $1 AND tenant_id = $2)`,
			id, tenantID).Scan(&hasProducts); err != nil {
			return err
		}
		if hasProducts {
			return ErrHasProducts
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM categories WHERE id = $1 AND tenant_id = $2`, id, tenantID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) CreateProduct(ctx context.Context, tenantID string, p Product) (Product, error) {
	var out Product
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO products (tenant_id, category_id, name, slug, description, price_cents, currency, status)
			VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7, $8)
			RETURNING id, COALESCE(category_id::text, ''), name, slug, description,
				price_cents, currency, status, created_at, updated_at`,
			tenantID, p.CategoryID, p.Name, p.Slug, p.Description, p.PriceCents, p.Currency, p.Status).
			Scan(&out.ID, &out.CategoryID, &out.Name, &out.Slug, &out.Description,
				&out.PriceCents, &out.Currency, &out.Status, &out.CreatedAt, &out.UpdatedAt)
	})
	return out, mapErr(err)
}

type ProductFilter struct {
	CategoryID string
	Status     string
}

func (s *Store) ListProducts(ctx context.Context, tenantID string, f ProductFilter) ([]Product, error) {
	var out []Product
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, COALESCE(category_id::text, ''), name, slug, description,
				price_cents, currency, status, created_at, updated_at
			FROM products
			WHERE tenant_id = $1
				AND ($2 = '' OR category_id = $2::uuid)
				AND ($3 = '' OR status = $3)
			ORDER BY name`, tenantID, f.CategoryID, f.Status)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Product
			if err := rows.Scan(&p.ID, &p.CategoryID, &p.Name, &p.Slug, &p.Description,
				&p.PriceCents, &p.Currency, &p.Status, &p.CreatedAt, &p.UpdatedAt); err != nil {
				return err
			}
			out = append(out, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return decorateSalePrices(ctx, tx, tenantID, out)
	})
	if out == nil {
		out = []Product{}
	}
	return out, err
}

func (s *Store) GetProduct(ctx context.Context, tenantID, id string) (Product, error) {
	var p Product
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT id, COALESCE(category_id::text, ''), name, slug, description,
				price_cents, currency, status, created_at, updated_at
			FROM products WHERE id = $1 AND tenant_id = $2`, id, tenantID).
			Scan(&p.ID, &p.CategoryID, &p.Name, &p.Slug, &p.Description,
				&p.PriceCents, &p.Currency, &p.Status, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return err
		}
		single := []Product{p}
		if err := decorateSalePrices(ctx, tx, tenantID, single); err != nil {
			return err
		}
		p = single[0]
		return nil
	})
	return p, mapErr(err)
}

// decorateSalePrices attaches sale_price_cents + campaign to products that
// are covered by a live non-code campaign (shared selection rule with
// checkout, so badges always match the cart).
func decorateSalePrices(ctx context.Context, tx pgx.Tx, tenantID string, products []Product) error {
	if len(products) == 0 {
		return nil
	}
	campaigns, err := loadLiveCampaigns(ctx, tx, tenantID, false)
	if err != nil {
		return err
	}
	if len(campaigns) == 0 {
		return nil
	}
	for i := range products {
		c, ok := bestCampaignFor(campaigns, products[i].ID, products[i].CategoryID,
			products[i].PriceCents)
		if !ok {
			continue
		}
		d := ComputeDiscount(c.RuleType, c.RuleValue, products[i].PriceCents)
		if d <= 0 {
			continue
		}
		sale := products[i].PriceCents - d
		products[i].SalePriceCents = &sale
		products[i].Campaign = &ProductCampaign{
			ID: c.ID, Name: c.Name, RuleType: c.RuleType,
			RuleValue: c.RuleValue, EndsAt: c.EndsAt,
		}
	}
	return nil
}

// GetActiveProducts fetches the given ids for order-line validation.
func (s *Store) GetActiveProducts(ctx context.Context, tenantID string, ids []string) (map[string]Product, error) {
	out := make(map[string]Product, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, name, price_cents, currency, status
			FROM products WHERE tenant_id = $1 AND id = ANY($2)`, tenantID, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Product
			if err := rows.Scan(&p.ID, &p.Name, &p.PriceCents, &p.Currency, &p.Status); err != nil {
				return err
			}
			out[p.ID] = p
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) UpdateProduct(ctx context.Context, tenantID, id string, p Product) (Product, error) {
	var out Product
	err := psql.ExecIn(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			UPDATE products SET
				category_id = CASE WHEN $3 = '' THEN category_id ELSE $3::uuid END,
				name = CASE WHEN $4 = '' THEN name ELSE $4 END,
				description = CASE WHEN $5 = '' THEN description ELSE $5 END,
				price_cents = CASE WHEN $6 < 0 THEN price_cents ELSE $6 END,
				status = CASE WHEN $7 = '' THEN status ELSE $7 END,
				updated_at = now()
			WHERE id = $1 AND tenant_id = $2
			RETURNING id, COALESCE(category_id::text, ''), name, slug, description,
				price_cents, currency, status, created_at, updated_at`,
			id, tenantID, p.CategoryID, p.Name, p.Description, p.PriceCents, p.Status).
			Scan(&out.ID, &out.CategoryID, &out.Name, &out.Slug, &out.Description,
				&out.PriceCents, &out.Currency, &out.Status, &out.CreatedAt, &out.UpdatedAt)
	})
	return out, mapErr(err)
}

// Slugify lowercases and hyphenates a name (used for defaults).
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		case !prevHyphen && b.Len() > 0:
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
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
			return ErrSlugTaken
		case "23503":
			// FK violation: missing parent/category reference
			if strings.Contains(pgErr.ConstraintName, "parent") {
				return ErrBadParent
			}
			return ErrNotFound
		}
	}
	return err
}
