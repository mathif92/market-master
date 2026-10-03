# ADR-0002: Orders topic — one topic, keyed by order_id, 12 partitions

## Status

Accepted

## Context

Order lifecycle spans three services (order, payment, logistics) and must
remain consistent per order: a `shipment.dispatched` must never be observed
before its `order.paid`, and consumers must be able to scale horizontally.

## Decision

- **Single topic** `market.orders.v1` carries all order lifecycle events:
  `order.created`, `order.payment_authorized`, `order.payment_failed`,
  `order.paid`, `order.cancelled`, `shipment.dispatched`,
  `shipment.delivered`.
- **Message key = `order_id`** (enforced: `evt.Decode` rejects envelopes
  without it). Broker computes `partition = hash(order_id) mod 12`, so all
  events of one order land in one partition and are consumed in order.
- **12 partitions**, created explicitly by `kafka-init` in compose
  (not via auto-create).
- **One consumer group per service** (`order-svc`, `payment-svc`,
  `logistics-svc`); each group reads the full topic. Useful parallelism
  per group ≤ 12 (a partition is owned by exactly one consumer per group).
- **At-least-once** delivery, offsets committed after handling; handlers
  are idempotent (event_id dedupe + state-machine guards). No
  exactly-once ambitions — they don't buy us correctness here.
- **Never change N on a live topic**: rehashing re-maps keys to other
  partitions and breaks per-order ordering across the change. If 12 ever
  becomes insufficient: create `market.orders.v2` with a larger count,
  migrate producers/consumers, keep v1 for replay.
- Poison messages: 3 attempts → DLQ `market.orders.dlq.<group>`; DLQ
  publish failure leaves the offset uncommitted (nothing is silently lost).

## Consequences

- Consumers scale to 12 instances per group; beyond that they idle.
- New event types slot into the same topic as long as they carry order_id.
- Aggregates unrelated to orders (catalog changes, etc.) must NOT use this
  topic — add a new topic keyed appropriately instead.

## Alternatives considered

Topic-per-event-type (ordering across types lost, more moving parts);
key by tenant_id (kills per-order ordering, caps parallelism at #tenants);
key by nothing/round-robin (no ordering guarantee at all).
