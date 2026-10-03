-- order schema (RLS on domain tables)

CREATE TABLE orders (
	id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id            UUID NOT NULL,
	customer_id          UUID NOT NULL,
	status               TEXT NOT NULL DEFAULT 'created'
		CHECK (status IN ('created', 'awaiting_payment', 'payment_failed',
		                 'paid', 'dispatched', 'delivered', 'cancelled')),
	total_cents          BIGINT NOT NULL CHECK (total_cents >= 0),
	currency             TEXT NOT NULL,
	shipping_method_code TEXT NOT NULL DEFAULT '',
	recipient_name       TEXT NOT NULL DEFAULT '',
	address_line         TEXT NOT NULL DEFAULT '',
	city                 TEXT NOT NULL DEFAULT '',
	country              TEXT NOT NULL DEFAULT '',
	postal_code          TEXT NOT NULL DEFAULT '',
	created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX orders_tenant_customer_idx ON orders (tenant_id, customer_id);
CREATE INDEX orders_tenant_status_idx ON orders (tenant_id, status);

CREATE TABLE order_lines (
	id               BIGSERIAL PRIMARY KEY,
	order_id         UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
	tenant_id        UUID NOT NULL,
	product_id       TEXT NOT NULL,
	name             TEXT NOT NULL,
	quantity         INT NOT NULL CHECK (quantity > 0),
	unit_price_cents BIGINT NOT NULL CHECK (unit_price_cents >= 0),
	created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX order_lines_order_idx ON order_lines (order_id);

-- consumer-side exactly-once effect bookkeeping
CREATE TABLE consumed_events (
	event_id     TEXT PRIMARY KEY,
	consumer_group TEXT NOT NULL,
	event_type   TEXT NOT NULL,
	processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION current_tenant() RETURNS uuid
LANGUAGE sql STABLE AS $$ SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid $$;

ALTER TABLE orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE orders FORCE ROW LEVEL SECURITY;
CREATE POLICY orders_tenant_isolation ON orders
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());

ALTER TABLE order_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_lines FORCE ROW LEVEL SECURITY;
CREATE POLICY order_lines_tenant_isolation ON order_lines
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
