# AGENTS.md

Guidance for contributors and coding agents working on this repo.

## What this is

A multi-tenant market platform (Go microservices, Kafka event core).
Each tenant is a market with its own catalog, orders, logistics config and
payments. REST at the edge (gateway), gRPC between services.

## Commands

```bash
make build    # build all 6 service binaries into bin/
make test     # go test ./...
make vet      # go vet ./...
make lint     # buf lint + golangci-lint (or vet fallback)
make proto    # regenerate gRPC code after editing api/proto/**.proto
make fmt      # gofmt + go mod tidy
make up       # docker compose (Postgres, Kafka, services, web)
make smoke    # scripts/e2e.sh — full order → pay → dispatch flow
make down     # compose down -v
make web-dev  # Vite dev server :5173 (proxies /v1 → gateway :8080)
make web      # build + serve SPA image on :8089 (nginx → gateway)
make web-build # tsc + vite bundle into web/dist
```

Go ≥ 1.26 (franz-go requirement; toolchain auto-downloads).
Tooling: `buf`, `protoc-gen-go`, `protoc-gen-go-grpc`, `golangci-lint`
(`go install` them into `$(go env GOPATH)/bin`, already on PATH via Makefile).

**Run `make lint test` before considering any change done.**

## Repository layout

```
api/proto/**            buf module (gRPC contracts) — REST is hand-written
gen/**                  generated — never edit by hand (make proto)
pkg/                    shared building blocks (see below)
services/<name>/cmd/server      binary entrypoint
services/<name>/internal        store/ + api/ + consumer/ per service
services/<name>/migrations      embed.FS SQL, applied in lexical order
web/                            React SPA: shop (/) + market admin (/admin)
deploy/docker-compose.yml       local stack (KRaft Kafka, Postgres 16)
deploy/Dockerfile               one image, SERVICE build-arg picks binary
deploy/web.Dockerfile           SPA build → nginx, /v1 → gateway
docs/adr/                      architecture decision records
scripts/e2e.sh                 smoke test
```

## Non-negotiable conventions

1. **Tenant everywhere.** Every domain table has `tenant_id` + FORCE RLS.
   All DB access goes through `psql.Tx/ExecIn(ctx, pool, tenantID, ...)`
   with the real tenant id (from `tenantctx`). Never open a tenant-scoped
   query without it — RLS will silently return zero rows.
2. **No dual-writes.** Any state change that must emit a Kafka event writes
   an outbox row **in the same transaction** (`outbox.Writer.Insert`).
   Never publish to Kafka directly from a request handler.
3. **Idempotency contract.**
   - Mutating HTTP endpoints that are not naturally idempotent MUST mount
     `idempotency.Store.Middleware(scope, required=true)` and complete the
     reservation **inside the business transaction**
     (`idempotency.From(ctx).CompleteInTx(tx, status, body)`).
   - Event handlers MUST dedupe on `event_id` (or an equivalent unique
     constraint) and/or guard with a state machine so replays no-op.
   - Outbound provider calls MUST carry a deterministic idempotency key
     derived from the business entity (e.g. `dispatch:<order_id>`).
4. **Events**: build them with `evt.New(type, tenant, orderID, aggType,
   aggID, payload)` and `Encode` — every event on `market.orders.v1`
   MUST carry `order_id` (it is the partition key). Never change the
   partition count of a live topic (ADR-0002).
5. **Internal comms are gRPC.** New service-to-service calls = new/changed
   proto in `api/proto`, `make proto`, buf lint must pass. REST only
   exists at the gateway edge.
6. **Auth**: gateway validates JWTs and strips inbound identity headers;
   each service ALSO validates JWTs on protected routes (defense in
   depth) via `authn.Signer.Middleware` + `httpmw.BindTenant`.
   Never trust `X-User-*` headers for authorization decisions.
7. **Money**: integer cents only (`*_cents BIGINT`), currency on every
   monetary record.

## Event flow reference (core loop)

1. `POST /v1/orders` → catalog gRPC price snapshot → tx: order +
   `order.created` outbox + idempotency completion.
2. `POST /v1/payments` → order gRPC (`GetOrder` for amount/status) → PSP
   authorize → tx: intent + `order.payment_authorized` [+ `order.paid`] /
   `order.payment_failed` outbox.
3. `order-svc` group: `payment_*`/`paid` → state machine transitions.
4. `logistics-svc` group: `order.paid` → dispatcher (`own_fleet` stub or
   `third_party` stub) → `shipment.dispatched`.
5. `payment-svc` group: `order.cancelled` → void authorized payments.
6. `order-svc` group: `shipment.dispatched/delivered` → order status.

Consumer groups each read the full topic; max parallelism per group =
partition count (12).

## Testing

- Unit tests are pure (state machine, envelope, dispatchers, JWT, gateway
  routing rules) — run with `make test`.
- The full integration path is `make smoke` against the compose stack.
  It asserts idempotent replay, paid→dispatched→delivered, declined card,
  and cancellation.
- When adding a consumer: cover replay (same event twice) and an
  illegal-transition case.
- Web app: `make web-build` (strict tsc + vite) and `make web-lint`
  (oxlint) must pass.

## Deployment note (future)

Local = docker compose. **Production target: Kubernetes on AWS EKS,
deployed with Helm charts** (`deploy/helm/` reserved). Keep services
12-factor: config via env (`pkg/conf`), stateless processes, health via
TCP/HTTP readiness, structured JSON logs to stdout, graceful shutdown on
SIGTERM (already wired in every main). Avoid host-specific paths or
assumptions so charts can wrap the same images built by `deploy/Dockerfile`.

## Out of scope for v1 (do not silently add)

Carts, inventory/stock, promotions, notifications, refunds API, real
Stripe/3PL integrations (interfaces + stubs exist), Helm charts.
