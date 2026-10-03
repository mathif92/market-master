# ADR-0001: Multitenancy model

## Status

Accepted

## Context

Each tenant is a market selling independently: own categories, products,
orders, shipping methods, payment records. We expect many small markets at
first, with a possible future need to shard as the platform grows.

## Decision

- **Database per service** (identity, catalog, orders, payment, logistics),
  one Postgres instance locally.
- **Every domain row carries `tenant_id`**, indexed, with application-level
  scoping through `pkg/psql` transaction helpers.
- **Postgres RLS as defense in depth**: policies use
  `current_tenant()` ← `SET app.tenant_id` per transaction, and tables use
  `FORCE ROW LEVEL SECURITY` so even the owner role is bound (dev exercises
  the same policies production will).
- Identity tables (tenants, users) are intentionally **not** RLS-scoped:
  they are the system scope that resolves tenants in the first place.
- Cross-tenant lookup tables (`psp_refs`) exist only where an external party
  references us without tenant context (PSP webhooks).

## Consequences

- Sharding by tenant later = routing/moving tenant-keyed data, not a model
  change (every row knows its tenant).
- A forgotten tenant filter fails closed (zero rows) instead of leaking.
- RLS adds per-transaction setup cost; acceptable at our load.

## Alternatives considered

Schema-per-tenant (migration overhead explodes with tenant count),
DB-per-tenant (ops cost), no RLS (relies purely on code review).
