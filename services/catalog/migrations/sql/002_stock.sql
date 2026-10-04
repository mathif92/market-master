-- inventory: audit ledger + current levels + order reservations + ingest batches.
-- A product with NO stock_levels row is untracked (unlimited) until first ingest.

CREATE TABLE stock_levels (
	tenant_id           UUID NOT NULL,
	product_id          UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
	on_hand             BIGINT NOT NULL DEFAULT 0 CHECK (on_hand >= 0),
	reserved            BIGINT NOT NULL DEFAULT 0 CHECK (reserved >= 0),
	low_stock_threshold INT NOT NULL DEFAULT 10 CHECK (low_stock_threshold >= 0),
	updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, product_id),
	-- available can never go negative (enforced app-side per row too)
	CHECK (on_hand - reserved >= 0)
);
CREATE INDEX stock_levels_product_idx ON stock_levels (product_id);

-- Append-only audit: who changed what, when, from which surface.
CREATE TABLE stock_movements (
	id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id   UUID NOT NULL,
	product_id  UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
	source      TEXT NOT NULL CHECK (source IN ('upload', 'webhook', 'api', 'adjustment', 'order')),
	kind        TEXT NOT NULL CHECK (kind IN ('in', 'adjust', 'sale', 'restock')),
	qty_delta   BIGINT NOT NULL,
	on_hand_after BIGINT NOT NULL,
	batch_key   TEXT NOT NULL DEFAULT '',
	ref_id      UUID,
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX stock_movements_tenant_product_idx
	ON stock_movements (tenant_id, product_id, created_at DESC);

-- Order-keyed holds: reserved at order create, committed on order.paid,
-- released on cancel (payment_failed keeps the hold — retry-safe).
-- State machine guards replays.
CREATE TABLE stock_reservations (
	order_id    UUID NOT NULL,
	product_id  UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
	tenant_id   UUID NOT NULL,
	qty         INT NOT NULL CHECK (qty > 0),
	status      TEXT NOT NULL DEFAULT 'reserved'
		CHECK (status IN ('reserved', 'committed', 'released')),
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (order_id, product_id)
);
CREATE INDEX stock_reservations_tenant_idx ON stock_reservations (tenant_id);

-- One row per ingestion batch; (tenant_id, batch_key) is the idempotency
-- contract for every surface (event_id / content hash / random per request).
CREATE TABLE stock_ingest_batches (
	id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id   UUID NOT NULL,
	batch_key   TEXT NOT NULL,
	source      TEXT NOT NULL CHECK (source IN ('upload', 'webhook', 'api')),
	mode        TEXT NOT NULL CHECK (mode IN ('set', 'delta')),
	total_rows  INT NOT NULL DEFAULT 0,
	applied_rows INT NOT NULL DEFAULT 0,
	error_rows  INT NOT NULL DEFAULT 0,
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (tenant_id, batch_key)
);

-- consumer-side exactly-once effect bookkeeping (same pattern as order-svc)
CREATE TABLE consumed_events (
	event_id       TEXT PRIMARY KEY,
	consumer_group TEXT NOT NULL,
	event_type     TEXT NOT NULL,
	processed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- RLS: every tenant-scoped table isolated like categories/products
ALTER TABLE stock_levels ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_levels FORCE ROW LEVEL SECURITY;
CREATE POLICY stock_levels_tenant_isolation ON stock_levels
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());

ALTER TABLE stock_movements ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_movements FORCE ROW LEVEL SECURITY;
CREATE POLICY stock_movements_tenant_isolation ON stock_movements
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());

ALTER TABLE stock_reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_reservations FORCE ROW LEVEL SECURITY;
CREATE POLICY stock_reservations_tenant_isolation ON stock_reservations
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());

ALTER TABLE stock_ingest_batches ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_ingest_batches FORCE ROW LEVEL SECURITY;
CREATE POLICY stock_ingest_batches_tenant_isolation ON stock_ingest_batches
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
