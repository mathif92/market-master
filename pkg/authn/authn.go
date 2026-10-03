package authn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	RolePlatformAdmin = "platform_admin"
	RoleTenantAdmin   = "tenant_admin"
	RoleStaff         = "staff"
	RoleCustomer      = "customer"
)

type Claims struct {
	TenantID string `json:"tid,omitempty"`
	Role     string `json:"role"`
	Type     string `json:"typ"` // access | refresh
	jwt.RegisteredClaims
}

type ctxKey struct{}

func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

func ClaimsFrom(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(*Claims)
	return c, ok
}

type Signer struct {
	secret    []byte
	accessTTL time.Duration
}

func NewSigner(secret string, accessTTL time.Duration) *Signer {
	if accessTTL <= 0 {
		accessTTL = time.Hour
	}
	return &Signer{secret: []byte(secret), accessTTL: accessTTL}
}

func (s *Signer) Sign(userID, tenantID, role, typ string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		if typ == "refresh" {
			ttl = 30 * 24 * time.Hour
		} else {
			ttl = s.accessTTL
		}
	}
	now := time.Now()
	claims := Claims{
		TenantID: tenantID,
		Role:     role,
		Type:     typ,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "market-master",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

func (s *Signer) Parse(token string) (*Claims, error) {
	c, err := s.ParseAny(token)
	if err != nil {
		return nil, err
	}
	if c.Type != "access" {
		return nil, errors.New("not an access token")
	}
	return c, nil
}

// ParseAny validates signature and expiry for any token type; callers check
// Claims.Type themselves.
func (s *Signer) ParseAny(token string) (*Claims, error) {
	var c Claims
	t, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return s.secret, nil
	}, jwt.WithValidMethods([]string{"JWT", "HS256"}))
	if err != nil {
		return nil, err
	}
	if !t.Valid {
		return nil, errors.New("invalid token")
	}
	return &c, nil
}

// Middleware validates Bearer tokens and puts claims in the context.
func (s *Signer) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if h == "" || !strings.HasPrefix(h, "Bearer ") {
			writeJSONErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := s.Parse(strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			writeJSONErr(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), claims)))
	})
}

// RequireRole rejects callers lacking one of the allowed roles.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := ClaimsFrom(r.Context())
			if !ok {
				writeJSONErr(w, http.StatusUnauthorized, "no claims")
				return
			}
			for _, role := range roles {
				if c.Role == role {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeJSONErr(w, http.StatusForbidden, "insufficient role")
		})
	}
}

func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, http.StatusText(status), msg)
}
