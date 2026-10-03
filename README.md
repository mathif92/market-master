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
make up      # docker compose: Postgres, Kafka (KRaft), 6 services
make smoke   # end-to-end: tenant → catalog → order → pay → dispatch → deliver
make logs    # follow logs
make down    # tear down (incl. volumes)
```

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

## Events (Kafka `market.orders.v1`)

Envelope (`pkg/evt`): `event_id`, `event_type`, `occurred_at`, `tenant_id`,
**`order_id` (= partition key)**, `aggregate`, `schema_version`, `payload`.

`order.created` · `order.payment_authorized` · `order.payment_failed` ·
`order.paid` · `order.cancelled` · `shipment.dispatched` · `shipment.delivered`

- **12 partitions**, `partition = hash(order_id) mod 12` → every event of one
  order is ordered within one partition; each consumer group scales to at
  most 12 instances.
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
