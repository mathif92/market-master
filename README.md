# Market Master

Multi-tenant market platform: each tenant ("market") sells its own catalog
online, with per-market logistics (own fleet or 3PL) and a dedicated payment
service that proxies external PSPs without ever touching card data.

Built as Go microservices with an event-driven core around a Kafka
`orders` topic keyed by `order_id`.

```
                      ┌──────────┐
   browser/app ──────▶│ gateway  │  Host: acme.localhost → tenant (cached)
                      └────┬─────┘
        ┌──────────┬───────┼───────────┬────────────┐
        ▼          ▼       ▼           ▼            ▼
   ┌──────────┐ ┌────────┐ ┌────────┐ ┌─────────┐ ┌───────────┐
   │ identity │ │catalog │ │ order  │ │ payment │ │ logistics │
   │  :8081   │ │ :8082  │ │ :8083  │ │  :8084  │ │   :8085   │
   │  gRPC    │ │ gRPC   │ │ gRPC   │ │         │ │           │
   └──────────┘ └────────┘ └───┬────┘ └────┬────┘ └─────┬─────┘
                               │  outbox   │            │
                               ▼           ▼            ▼
                        ┌───────────────────────────────────┐
                        │ Kafka: market.orders.v1          │
                        │ 12 partitions, key = order_id    │
                        └───────────────────────────────────┘
                          consumer groups (each scales ≤ 12):
                          order-svc | payment-svc | logistics-svc
```

## Quickstart

```bash
make up      # docker compose: Postgres, Kafka (KRaft), 6 services, web (:8089)
make smoke   # end-to-end: tenant → catalog → order → pay → dispatch → deliver
             # (+ stock enforcement, campaigns, platform panel)
make logs    # follow logs
make down    # tear down (incl. volumes)
```

## Web app (React)

Single SPA in `web/` — customer storefront (`/`), market admin (`/admin`,
requires `tenant_admin`/`staff`) and platform panel (`/platform`, requires
`platform_admin`). Vite + React 19 + TS + Tailwind v4 + TanStack Query,
shadcn-style UI on Radix.

```bash
cd web && npm install

make web-dev   # Vite :5173, /v1 proxied to gateway :8080 (hot reload)
make web       # build + serve the SPA image on :8089 (prod-like, via nginx)
make web-build # typecheck + bundle into web/dist
```

Market resolution: `demo.localhost:5173` works out of the box; on plain
`localhost` the app stores a market slug (login page field) and sends it as
`X-Tenant-Slug`.

- **Shop**: browse/filter catalog → cart (localStorage, no backend cart) →
  checkout (order + simulated card payment) → live order tracking with
  status polling until delivered.
- **Admin**: dashboard (revenue/orders/in-transit), product & category CRUD,
  all-orders table + detail (cancel, mark delivered), shipments,
  shipping methods, users.
- **Sign up**: `/signup` ("Start selling" in the header) creates a new
  market + its first admin and drops you straight into that market's
  `/admin`.
- **Platform**: `/platform` — list all markets, suspend/activate them,
  invite/disable platform admins.
- Test cards in checkout: `tok_visa_ok` approves, `tok_visa_decline`
  declines (any token containing `decline` fails).

Local dev without Docker for services:

```bash
make test && make lint
docker compose -f deploy/docker-compose.yml up -d postgres kafka kafka-init
ADDR=:8083 DATABASE_URL=... JWT_SECRET=dev JWT KAFKA_BROKERS=localhost:9092 \
  go run ./services/order/cmd/server
```

## Services

| Service   | Port | gRPC   | Responsibility |
|-----------|------|--------|----------------|
| gateway   | 8080 | –      | Host → market resolution, JWT auth, REST routing, anti-spoofing header strip |
| identity  | 8081 | 9081   | Markets, users/roles, JWT issuing; `ResolveTenant`, `GetUser` |
| catalog   | 8082 | 9082   | Market-scoped categories/products; `ValidateOrderLines` (price snapshot) |
| order     | 8083 | 9083   | Order state machine, idempotent create, outbox; `GetOrder` |
| payment   | 8084 | –      | Tokenization proxy to PSPs, intents, webhooks, `order.paid` events |
| logistics | 8085 | –      | Shipments; `Dispatcher` port: own fleet + 3PL stub, per-market methods |

> Container ports are the ones above. If a host port is already taken
> (e.g. a local Postgres on 5432), remap the **host** side in
> `deploy/docker-compose.yml` — this checkout maps PG to `55432`, and
> identity/catalog/logistics HTTP to `18081/18082/18085`. Service-to-service
> traffic uses container ports and is unaffected.

**REST at the edge, gRPC between services** (buf-managed, `api/proto/`,
generated into `gen/` — `make proto`).

## Platform admins & self-serve signup

Roles: `platform_admin` (cross-market control plane), `tenant_admin`,
`staff`, `customer`.

- **Self-serve signup**: `POST /v1/tenants` is public — anyone can create
  their own market (slug, name, admin email/password), or use `/signup` in
  the web app, which auto-logs the new admin into their market.
  *Known v1 gap: no rate limiting/CAPTCHA on public signup.*
- **First platform admin** is bootstrapped at identity startup from env
  `PLATFORM_ADMIN_EMAIL` + `PLATFORM_ADMIN_PASSWORD` (≥8 chars, idempotent).
  Compose dev default: `platform@market.test` / `platform12345`.
- **Platform endpoints** (JWT with role `platform_admin`, empty `tid` —
  bypasses tenant matching by design):

  ```bash
  # platform login (note the empty tenant_slug)
  PTOKEN=$(curl -s -X POST localhost:8080/v1/auth/login \
    -d '{"tenant_slug":"","email":"platform@market.test","password":"platform12345"}' \
    | jq -r .access_token)
  PAUTH="Authorization: Bearer $PTOKEN"

  curl -H "$PAUTH" localhost:8080/v1/tenants                 # list markets
  curl -H "$PAUTH" -X PATCH localhost:8080/v1/tenants/<id> \
    -d '{"status":"suspended"}'                               # suspend/activate
  curl -H "$PAUTH" localhost:8080/v1/platform/users           # list admins
  curl -H "$PAUTH" -X POST localhost:8080/v1/platform/users \
    -d '{"email":"ops@x.io","password":"another-pass"}'       # invite admin
  curl -H "$PAUTH" -X PATCH localhost:8080/v1/platform/users/<id> \
    -d '{"status":"disabled"}'                                # disable admin
  ```

  A suspended market returns `403 market_suspended` for its storefront;
  `/v1/tenants*` and `/v1/platform/*` keep working so it can be reactivated.

## API walkthrough (curl)

All requests go through the gateway. Use a market subdomain
(`http://demo.localhost:8080`) or the `X-Tenant-Slug` header (dev).

```bash
# bootstrap a market (public)
curl -X POST localhost:8080/v1/tenants -d '{"slug":"demo","name":"Demo",
  "admin":{"email":"a@b.c","password":"password123"}}'

TOKEN=$(curl -s -X POST localhost:8080/v1/auth/login \
  -d '{"tenant_slug":"demo","email":"a@b.c","password":"password123"}' | jq -r .access_token)
AUTH="Authorization: Bearer $TOKEN"

# catalog (staff)
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' -X POST localhost:8080/v1/categories \
  -d '{"name":"Snacks"}'
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' -X POST localhost:8080/v1/products \
  -d '{"name":"Popcorn","price_cents":1500}'

# order — Idempotency-Key is REQUIRED on POST /v1/orders and /v1/payments
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' -H 'Idempotency-Key: k1' \
  -X POST localhost:8080/v1/orders -d '{
    "lines":[{"product_id":"...","quantity":2}],
    "shipping":{"recipient_name":"Ada","line1":"1 Main","city":"X","country":"US","postal_code":"1"}}'

# pay (fake PSP: token "tok_visa_ok" succeeds, anything with "decline" fails)
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' -H 'Idempotency-Key: p1' \
  -X POST localhost:8080/v1/payments \
  -d '{"order_id":"...","payment_method_token":"tok_visa_ok"}'

# watch the order move created → paid → dispatched (then mark delivered)
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' localhost:8080/v1/orders/<id>
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' localhost:8080/v1/shipments?order_id=<id>
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' -X POST localhost:8080/v1/shipments/<sid>/deliver
```

## Inventory & stock

Stock lives in the catalog service (`stock_levels` + append-only
`stock_movements` ledger). A product with **no level row is untracked**:
orders accept any quantity until the first ingestion starts tracking it.
Tracked products enforce **reserve → commit → release**:

1. `POST /v1/orders` → catalog gRPC atomically checks availability and
   bumps `reserved` (all-or-nothing, keyed by a pre-generated `order_id`).
   Over-ordering fails with **422 `insufficient_stock`**.
2. `order.paid` → `catalog-svc` consumer commits (`on_hand -=`,
   movement `sale`).
3. `order.cancelled` → the hold is released; a committed hold would be
   restocked. `order.payment_failed` deliberately **keeps** the hold —
   checkout retries payment on the same order, and only cancellation
   releases (declined-then-abandoned orders hold stock until cancelled).

Three ingestion surfaces (all idempotent, all tenant-scoped, row-level
report of unknown SKUs / invariant violations):

```bash
# JSON batch (staff JWT; Idempotency-Key required — the key is the batch key)
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' -H 'Idempotency-Key: s1' \
  -X POST localhost:8080/v1/inventory/ingest \
  -d '{"mode":"set","items":[{"sku":"popcorn","quantity":50}]}'

# file upload (CSV/XLSX: header sku,quantity — deduped by content hash)
printf 'sku,quantity\npopcorn,50\n' > stock.csv
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' \
  -X POST localhost:8080/v1/inventory/upload \
  -F mode=set -F file=@stock.csv

# HMAC webhook (market comes from the signed payload; no JWT)
BODY='{"event_id":"evt-1","tenant_id":"<tenant-uuid>","mode":"set",
       "items":[{"sku":"popcorn","quantity":50}]}'
SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$INGEST_WEBHOOK_SECRET" \
  | awk '{print $NF}')
curl -X POST localhost:8080/v1/inventory/webhook \
  -H "X-Ingest-Signature: $SIG" -d "$BODY"

# inspection (staff)
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' localhost:8080/v1/inventory/levels
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' \
  'localhost:8080/v1/inventory/movements?limit=25'
```

Modes: `set` replaces `on_hand`, `delta` adds (negative allowed); a `set`
can never drop below what's reserved. Admin UI: **Inventory** in the
market admin (`/admin/inventory`) — adjustment dialog, file upload,
untracked/low-stock warnings, movement feed.

Known gaps (v1): an orphan hold can survive a crash between reserve and
order insert when the best-effort release also fails; holds on
`awaiting_payment` orders are only released by cancel/payment-failure
(no TTL sweeper yet); one shared webhook secret; no rate limiting.

## Campaigns & discounts

Campaigns live in the catalog service (`campaigns`, `campaign_codes`,
`campaign_redemptions` — all tenant-scoped with FORCE RLS). A campaign
is a time-boxed discount rule (`percent` basis points or `fixed` cents)
scoped to `sitewide`, a `category`, or a `product`, optionally gated by
a coupon code. Admin UI: **Campaigns** in the market admin
(`/admin/campaigns`).

**Selection & checkout rules**

1. **One campaign per order, no stacking.** All candidates (live,
   window-open, scope-matching) compete; the winner is the one that
   **maximizes the total discount for that order**. Ties break: more
   specific scope → coupon over auto → lower id. This is deliberate:
   specificity-first at order level could let a product-5% beat a
   sitewide-30%, which reads wrong on a cart that mixes both.
2. Coupon campaigns are **candidates only when the order carries the
   code** (`requires_code` never auto-applies, never appears in
   read-time sale prices or the public banner). A wrong/inactive/expired
   code → **422 `invalid_coupon`** (no side effects). A losing code
   never errors and never stacks — it simply isn't consumed.
3. Caps: `max_redemptions` (campaign) and `max_uses` (per code) are
   enforced **inside the reserve transaction** → **422
   `campaign_limit`**. Counters are guarded single-row updates; the
   `campaign_redemptions` ledger is the truth (counters are recomputed
   from it on release, so a failed transaction can never leak a slot).
4. **Price snapshot**: `order.created` lines carry both
   `unit_price_cents` (final, campaign-applied, clamped to ≥ 1¢) and
   `list_price_cents` (canonical, pre-campaign). Payments take the
   order total as-is — the PSP never sees campaign logic.
5. **Read-time decoration**: product reads return `sale_price_cents`
   and the winning `campaign` (same selection function as checkout, so
   display always matches what the cart will charge). Canonical
   `price_cents` is never rewritten. Banners come from the public
   `GET /v1/campaigns/active` (no JWT; tenant from host/header).
6. **Lifecycle**: `draft` → `active` → `archived`. `PUT /v1/campaigns/{id}`
   requires `status` explicitly; `DELETE` archives (keeps the redemption
   ledger). Archiving removes the sale from reads immediately; existing
   orders keep their snapshot. `POST` create and code minting require an
   `Idempotency-Key` (completed inside the business tx).

**Atomicity**: pricing, counter guards, redemption inserts and stock
reserves run in **one catalog transaction** keyed by the pre-generated
`order_id`; a stock shortage rolls the campaign slot back (verified in
smoke). Lock order: campaign rows → code rows → product levels (sorted
by `product_id`). Replays of the same `order_id` recompute prices
without re-taking holds or re-bumping counters. `order.cancelled`
releases holds **and** refunds redemption slots; `payment_failed` keeps
both (retry on the same order).

```bash
# create an active sitewide 50% campaign (staff JWT + Idempotency-Key)
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' -H 'Idempotency-Key: c1' \
  -X POST localhost:8080/v1/campaigns -d '{
    "name":"Black Friday","status":"active","rule_type":"percent",
    "rule_value":5000,"scope_type":"sitewide","requires_code":false,
    "starts_at":"2026-11-27T00:00:00Z","ends_at":"2026-11-28T00:00:00Z"}'

# mint 100 single-use codes for a coupon campaign, then order with it
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' -H 'Idempotency-Key: c2' \
  -X POST localhost:8080/v1/campaigns/<id>/codes \
  -d '{"count":100,"max_uses":1}'
curl -H "$AUTH" -H 'X-Tenant-Slug: demo' -H 'Idempotency-Key: k9' \
  -X POST localhost:8080/v1/orders \
  -d '{"coupon_code":"ABCDE12345","lines":[...],"shipping":{...}}'

# public banner (no JWT)
curl localhost:8080/v1/campaigns/active -H 'X-Tenant-Slug: demo'
```

Known gaps (v1): no stacking/multi-campaign carts, no per-customer
caps, no percentage rounding policies beyond ≥ 1¢ clamp, category
scope matches products whose `category_id` is set (no subcategory
expansion), mid-window edits apply to orders created afterwards, and
`payment_failed` orders hold their redemption slot until cancelled (no
TTL sweeper).

## Events (Kafka `market.orders.v1`)

Envelope (`pkg/evt`): `event_id`, `event_type`, `occurred_at`, `tenant_id`,
**`order_id` (= partition key)**, `aggregate`, `schema_version`, `payload`.

`order.created` · `order.payment_authorized` · `order.payment_failed` ·
`order.paid` · `order.cancelled` · `shipment.dispatched` · `shipment.delivered`

- **12 partitions**, `partition = hash(order_id) mod 12` → every event of one
  order is ordered within one partition; each consumer group scales to at
  most 12 instances.
- Consumer groups (each reads the full topic): `order-svc`, `payment-svc`,
  `logistics-svc`, `catalog-svc` (stock commit/release).
- Emission is via **transactional outbox** (state change + event commit
  atomically; relay publishes with `FOR UPDATE SKIP LOCKED`).
- Delivery is **at-least-once**; handlers are idempotent (event_id dedupe +
  state-machine guards), so duplicates are harmless no-ops.
- Per-group DLQ: `market.orders.dlq.<group>` (after 3 failed attempts).

See `docs/adr/0002-orders-topic.md` for the partition-count evolution rules.

## Idempotency

Three layers (see `docs/adr/0003-idempotency.md`):

1. **HTTP**: `Idempotency-Key` + request hash. The response is recorded
   **inside the business transaction** (`Reservation.CompleteInTx`), so a
   crash can never produce a duplicate order/payment on retry.
2. **Consumers**: `consumed_events` (event_id PK) written atomically with
   the state change; the order state machine additionally rejects illegal
   transitions.
3. **Outbound effects**: deterministic idempotency keys to PSPs/3PLs
   (`authorize:<order>:<key>`, `dispatch:<order_id>`), webhook dedupe on
   provider event ids.

## Data & multitenancy

- One Postgres, **database per service** (identity, catalog, orders,
  payment, logistics).
- Every domain table carries `tenant_id` and has **FORCE row-level
  security** (`current_tenant()` from `SET app.tenant_id`, set per
  transaction by `pkg/psql`).
- Future sharding by tenant is a routing change, not a model change:
  every row already knows its tenant.

## Development

```bash
make proto    # buf lint + generate (requires buf, protoc-gen-go, protoc-gen-go-grpc)
make lint     # buf lint + golangci-lint (falls back to go vet)
make test     # go test ./...
make build    # binaries into bin/
```

Layout: `pkg/` shared building blocks · `services/<name>/cmd/server` ·
`services/<name>/internal` · `api/proto` + `gen/` · `deploy/`.

## Deployment

Local dev uses `deploy/docker-compose.yml`. **The intended production target
is Kubernetes on AWS EKS via Helm charts** (`deploy/helm/` is reserved for
that work) — see `AGENTS.md` for the conventions to keep portable.
