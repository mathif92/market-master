package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	identityv1 "market-master/gen/identity/v1"
	"market-master/pkg/authn"
	"market-master/pkg/tenantctx"
)

type Config struct {
	IdentityGRPC string
	// Routes maps a path prefix to an upstream host:port.
	Routes    map[string]string
	JWTSecret string
	Logger    *slog.Logger
	// TenantCacheTTL is how long resolved market info (incl. suspension
	// status) is cached. 0 means 60s.
	TenantCacheTTL time.Duration
}

type tenantInfo struct {
	id     string
	status string
	until  time.Time
}

type route struct {
	prefix string
	addr   string
}

type Gateway struct {
	identity identityv1.IdentityServiceClient
	signer   *authn.Signer
	log      *slog.Logger

	mu      sync.Mutex
	cache   map[string]tenantInfo
	ttl     time.Duration
	routes  []route // sorted longest-prefix first
	proxies map[string]*httputil.ReverseProxy
}

func New(cfg Config) (*Gateway, error) {
	conn, err := grpc.NewClient(cfg.IdentityGRPC,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	proxies := make(map[string]*httputil.ReverseProxy, len(cfg.Routes))
	routes := make([]route, 0, len(cfg.Routes))
	for prefix, addr := range cfg.Routes {
		routes = append(routes, route{prefix: prefix, addr: addr})
		if _, ok := proxies[addr]; ok {
			continue
		}
		u, err := url.Parse("http://" + addr)
		if err != nil {
			return nil, err
		}
		p := httputil.NewSingleHostReverseProxy(u)
		p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			cfg.Logger.Error("upstream error", "upstream", addr, "path", r.URL.Path, "err", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": "bad_gateway", "message": "upstream unavailable"},
			})
		}
		proxies[addr] = p
	}
	// longest prefix first → most specific route wins
	sort.Slice(routes, func(i, j int) bool { return len(routes[i].prefix) > len(routes[j].prefix) })

	ttl := cfg.TenantCacheTTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &Gateway{
		identity: identityv1.NewIdentityServiceClient(conn),
		signer:   authn.NewSigner(cfg.JWTSecret, time.Hour),
		log:      cfg.Logger,
		cache:    map[string]tenantInfo{},
		ttl:      ttl,
		routes:   routes,
		proxies:  proxies,
	}, nil
}

// ServeHTTP resolves the market from the Host header, enforces auth,
// strips spoofable identity headers, then reverse-proxies upstream.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	slug := marketSlug(r)
	tenantID := ""
	if slug != "" {
		// Bootstrap (POST /v1/tenants) must not poison the cache: the very
		// next request will need the market that is being created.
		bootstrap := r.Method == http.MethodPost && r.URL.Path == "/v1/tenants"
		info, found, err := g.resolveTenant(r.Context(), slug, bootstrap)
		if err != nil {
			g.fail(w, http.StatusServiceUnavailable, "tenant_lookup_failed", "try again")
			return
		}
		if !found {
			if isTenantScopedPath(r.URL.Path) {
				g.fail(w, http.StatusNotFound, "unknown_market", "no market for this host")
				return
			}
		} else if !pathExemptFromMarket(r.URL.Path) && info.status != "active" {
			// Suspension never blocks platform/bootstrap paths: a suspended
			// market must still be reachable for reactivation and signup.
			g.fail(w, http.StatusForbidden, "market_suspended", "this market is suspended")
			return
		} else {
			tenantID = info.id
		}
	}

	// Never trust client-supplied identity headers.
	r.Header.Del(tenantctx.HeaderTenantID)
	r.Header.Del(tenantctx.HeaderUserID)
	r.Header.Del(tenantctx.HeaderUserRole)
	if tenantID != "" {
		r.Header.Set(tenantctx.HeaderTenantID, tenantID)
	}

	if !isPublic(r.Method, r.URL.Path) {
		claims, err := g.bearerClaims(r)
		if err != nil {
			g.fail(w, http.StatusUnauthorized, "unauthorized", "missing or invalid token")
			return
		}
		if claims.TenantID != "" && tenantID != "" && claims.TenantID != tenantID {
			g.fail(w, http.StatusForbidden, "tenant_mismatch",
				"token does not belong to this market")
			return
		}
		r.Header.Set(tenantctx.HeaderUserID, claims.Subject)
		r.Header.Set(tenantctx.HeaderUserRole, claims.Role)
	}

	addr, ok := g.route(r.URL.Path)
	if !ok {
		g.fail(w, http.StatusNotFound, "not_found", "no route for "+r.URL.Path)
		return
	}
	g.proxies[addr].ServeHTTP(w, r)
}

func (g *Gateway) resolveTenant(ctx context.Context, slug string, skipNegative bool) (tenantInfo, bool, error) {
	g.mu.Lock()
	if info, ok := g.cache[slug]; ok && time.Now().Before(info.until) {
		g.mu.Unlock()
		return info, info.id != "", nil
	}
	g.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := g.identity.ResolveTenant(ctx, &identityv1.ResolveTenantRequest{Slug: slug})

	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		if status.Code(err) == codes.NotFound {
			if !skipNegative {
				// unknown market: negative-cache briefly so typos can't hammer identity
				g.cache[slug] = tenantInfo{until: time.Now().Add(5 * time.Second)}
			}
			return tenantInfo{}, false, nil
		}
		if !skipNegative {
			g.cache[slug] = tenantInfo{until: time.Now().Add(5 * time.Second)}
		}
		return tenantInfo{}, false, err
	}
	t := resp.GetTenant()
	info := tenantInfo{id: t.GetId(), status: t.GetStatus(), until: time.Now().Add(g.ttl)}
	g.cache[slug] = info
	return info, true, nil
}

var errUnauthorized = errors.New("unauthorized")

func (g *Gateway) bearerClaims(r *http.Request) (*authn.Claims, error) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return nil, errUnauthorized
	}
	return g.signer.Parse(strings.TrimPrefix(h, "Bearer "))
}

func (g *Gateway) route(path string) (string, bool) {
	for _, rt := range g.routes { // already sorted longest-prefix first
		if strings.HasPrefix(path, rt.prefix) {
			return rt.addr, true
		}
	}
	return "", false
}

func (g *Gateway) fail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

// marketSlug derives the market name from the request host:
// acme.localhost:8080 → "acme". X-Tenant-Slug is a curl-friendly override
// (the gateway strips it from upstream after use).
func marketSlug(r *http.Request) string {
	if s := r.Header.Get("X-Tenant-Slug"); s != "" {
		return s
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	if host == "localhost" || host == "127.0.0.1" || host == "" || net.ParseIP(host) != nil {
		return ""
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		return host[:i]
	}
	return host
}

func isTenantScopedPath(path string) bool {
	if path == "/v1/tenants" || strings.HasPrefix(path, "/v1/tenants/") ||
		strings.HasPrefix(path, "/v1/platform/") || strings.HasPrefix(path, "/v1/auth/") {
		return false // bootstrap/login/platform resolve the market from the body
	}
	return true
}

// pathExemptFromMarket lists paths that work even when the market resolved
// from the host is suspended (market lifecycle + platform control plane).
func pathExemptFromMarket(path string) bool {
	return path == "/v1/tenants" ||
		strings.HasPrefix(path, "/v1/tenants/") ||
		strings.HasPrefix(path, "/v1/platform/")
}

// isPublic lists endpoints reachable without a JWT.
func isPublic(method, path string) bool {
	switch {
	case path == "/v1/tenants" && method == http.MethodPost:
		return true
	case strings.HasPrefix(path, "/v1/auth/"):
		return true
	case path == "/v1/psp/webhook" && method == http.MethodPost:
		return true // HMAC-authenticated provider callback
	case path == "/v1/inventory/webhook" && method == http.MethodPost:
		return true // HMAC-authenticated stock ingestion callback
	case method == http.MethodGet &&
		(strings.HasPrefix(path, "/v1/categories") ||
			strings.HasPrefix(path, "/v1/products") ||
			strings.HasPrefix(path, "/v1/shipping-methods")):
		return true
	case path == "/v1/campaigns/active" && method == http.MethodGet:
		return true // public sale banner (admin campaign routes stay JWT-gated)
	default:
		return false
	}
}
