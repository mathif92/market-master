# ADR-0003: Idempotency — three layers

## Status

Accepted

## Context

Retries are inevitable: clients retry on timeout, Kafka redelivers after a
crash, PSPs/3PLs replay webhooks. Several operations are not naturally
idempotent (creating an order, authorizing a card, dispatching a parcel).

## Decision

### Layer 1 — HTTP requests

- Non-idempotent POSTs (`POST /v1/orders`, `POST /v1/payments`) require an
  `Idempotency-Key` header, enforced by `idempotency.Store.Middleware`.
- The store keys `(scope, key)` with the SHA-256 of `method+path+body`:
  - same key + same body → replay the stored response
    (`Idempotency-Replayed: true`);
  - same key + different body → `409`;
  - concurrent same key → `409` + `Retry-After` (lease-based, so a crashed
    worker's key becomes claimable again after the lease expires).
- **The response is recorded inside the business transaction**
  (`Reservation.CompleteInTx`): the row insert (order/payment) and the
  replay record commit or roll back together, closing the classic
  "charged but response lost" window.
- If a handler does not complete the reservation in-tx, the middleware
  **releases** the key (never stores a response for state that didn't land).

### Layer 2 — event consumers

- `consumed_events(event_id PK)` written atomically with the state change;
  a redelivered event hits `ON CONFLICT DO NOTHING` and no-ops.
- The order state machine (`services/order/internal/sm`) rejects
  illegal/redundant transitions, so even a wrongly re-injected event
  cannot corrupt state.
- Natural unique constraints back this up: one shipment per order
  (`shipments (tenant_id, order_id)` unique), one intent per order
  (`payment_intents (tenant_id, order_id)` unique).

### Layer 3 — outbound side effects

- Outbox relay is at-least-once; consumers absorb duplicates (layer 2).
- PSP authorize carries `Idempotency-Key: authorize:<order_id>:<client key>`.
- Dispatches derive their key as `dispatch:<order_id>`; adapters compute
  provider refs as `hash(idem_key)` so a retried call returns the same
  reference instead of creating a second parcel.
- Webhooks dedupe on `(psp, event_id)` / `consumed_events` before any
  state transition.

## Consequences

- Client contract: same key ⇒ same request body (else 409).
- Handlers must call `CompleteInTx` as the last statement of their tx.
- Concurrent duplicate payments are additionally blocked at the DB
  (unique index); the loser voids its fresh authorization.

## Alternatives considered

Only-unique-constraints (no response replay, clients can't recover);
distributed locks (extra failure mode, no replay benefit);
Kafka exactly-once (does not cover HTTP or provider calls).
