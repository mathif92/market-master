package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"market-master/pkg/psql"
)

type Tenant struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type User struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id,omitempty"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var ErrSlugTaken = errors.New("tenant slug already taken")
var ErrEmailTaken = errors.New("email already registered")
var ErrNotFound = errors.New("not found")

// CreateTenantWithAdmin creates the market and its first admin atomically.
// beforeCommit, when non-nil, runs inside the same transaction so callers
// can commit idempotency records together with the new rows.
func (s *Store) CreateTenantWithAdmin(ctx context.Context, slug, name, adminEmail, passwordHash string,
	beforeCommit func(tx pgx.Tx, t Tenant, u User) error) (Tenant, User, error) {
	var tenant Tenant
	var user User
	err := psql.ExecIn(ctx, s.pool, "", func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO tenants (slug, name) VALUES ($1, $2)
			RETURNING id, slug, name, status, created_at`, slug, name).
			Scan(&tenant.ID, &tenant.Slug, &tenant.Name, &tenant.Status, &tenant.CreatedAt); err != nil {
			if isUniqueViolation(err) {
				return ErrSlugTaken
			}
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (tenant_id, email, password_hash, role)
			VALUES ($1, $2, $3, 'tenant_admin')
			RETURNING id, tenant_id, email, role, status, created_at`, tenant.ID, strings.ToLower(adminEmail), passwordHash).
			Scan(&user.ID, &user.TenantID, &user.Email, &user.Role, &user.Status, &user.CreatedAt); err != nil {
			if isUniqueViolation(err) {
				return ErrEmailTaken
			}
			return err
		}
		if beforeCommit != nil {
			return beforeCommit(tx, tenant, user)
		}
		return nil
	})
	if err != nil {
		return Tenant{}, User{}, err
	}
	return tenant, user, nil
}

func (s *Store) GetTenantBySlug(ctx context.Context, slug string) (Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx,
		`SELECT id, slug, name, status, created_at FROM tenants WHERE slug = $1`, slug).
		Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, ErrNotFound
	}
	return t, err
}

func (s *Store) GetUserByEmail(ctx context.Context, tenantID, email string) (User, string, error) {
	var (
		u    User
		hash string
	)
	var row pgx.Row
	email = strings.ToLower(email)
	if tenantID == "" {
		row = s.pool.QueryRow(ctx, `
			SELECT id, COALESCE(tenant_id::text, ''), email, password_hash, role, status, created_at
			FROM users WHERE tenant_id IS NULL AND email = $1`, email)
	} else {
		row = s.pool.QueryRow(ctx, `
			SELECT id, COALESCE(tenant_id::text, ''), email, password_hash, role, status, created_at
			FROM users WHERE tenant_id = $1 AND email = $2`, tenantID, email)
	}
	err := row.Scan(&u.ID, &u.TenantID, &u.Email, &hash, &u.Role, &u.Status, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrNotFound
	}
	if err != nil {
		return User{}, "", err
	}
	return u, hash, nil
}

func (s *Store) GetUserByID(ctx context.Context, id string) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT id, COALESCE(tenant_id::text, ''), email, role, status, created_at
		FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.TenantID, &u.Email, &u.Role, &u.Status, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// CreateUser creates a tenant-scoped user. role must be tenant_admin, staff
// or customer; platform admins are created via CreatePlatformUser.
func (s *Store) CreateUser(ctx context.Context, tenantID, email, passwordHash, role string) (User, error) {
	if role == "" {
		role = "customer"
	}
	var u User
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, password_hash, role)
		VALUES ($1, $2, $3, $4)
		RETURNING id, COALESCE(tenant_id::text, ''), email, role, status, created_at`,
		tenantID, strings.ToLower(email), passwordHash, role).
		Scan(&u.ID, &u.TenantID, &u.Email, &u.Role, &u.Status, &u.CreatedAt)
	if isUniqueViolation(err) {
		return User{}, ErrEmailTaken
	}
	return u, err
}

func (s *Store) ListUsers(ctx context.Context, tenantID string) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, COALESCE(tenant_id::text, ''), email, role, status, created_at
		FROM users WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Email, &u.Role, &u.Status, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListTenants returns every market, newest first.
func (s *Store) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, slug, name, status, created_at
		FROM tenants ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tenant
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTenantStatus sets active/suspended. ErrNotFound when id is unknown.
func (s *Store) UpdateTenantStatus(ctx context.Context, id, status string) (Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx, `
		UPDATE tenants SET status = $2 WHERE id = $1
		RETURNING id, slug, name, status, created_at`, id, status).
		Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, ErrNotFound
	}
	return t, err
}

// EnsurePlatformAdmin idempotently creates the bootstrap platform admin.
// Returns true when a new row was inserted.
func (s *Store) EnsurePlatformAdmin(ctx context.Context, email, passwordHash string) (bool, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, password_hash, role)
		VALUES (NULL, $1, $2, 'platform_admin')
		ON CONFLICT (email) WHERE tenant_id IS NULL DO NOTHING
		RETURNING id`, strings.ToLower(email), passwordHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListPlatformUsers returns every platform-scoped user (tenant_id IS NULL).
func (s *Store) ListPlatformUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, email, role, status, created_at
		FROM users WHERE tenant_id IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.Status, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreatePlatformUser inserts a tenant-less platform admin. The role is
// pinned server-side. beforeCommit, when non-nil, runs inside the same
// transaction so callers can commit idempotency records together.
func (s *Store) CreatePlatformUser(ctx context.Context, email, passwordHash string,
	beforeCommit func(tx pgx.Tx, u User) error) (User, error) {
	var u User
	err := psql.ExecIn(ctx, s.pool, "", func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (tenant_id, email, password_hash, role)
			VALUES (NULL, $1, $2, 'platform_admin')
			RETURNING id, email, role, status, created_at`,
			strings.ToLower(email), passwordHash).
			Scan(&u.ID, &u.Email, &u.Role, &u.Status, &u.CreatedAt); err != nil {
			if isUniqueViolation(err) {
				return ErrEmailTaken
			}
			return err
		}
		if beforeCommit != nil {
			return beforeCommit(tx, u)
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// UpdatePlatformUserStatus sets active/disabled for a platform user.
func (s *Store) UpdatePlatformUserStatus(ctx context.Context, id, status string) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		UPDATE users SET status = $2
		WHERE id = $1 AND tenant_id IS NULL
		RETURNING id, email, role, status, created_at`, id, status).
		Scan(&u.ID, &u.Email, &u.Role, &u.Status, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
