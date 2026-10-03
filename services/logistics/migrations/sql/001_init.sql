-- logistics schema

CREATE TABLE shipping_methods (
	id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id  UUID NOT NULL,
	code       TEXT NOT NULL,
	name       TEXT NOT NULL,
	dispatcher TEXT NOT NULL CHECK (dispatcher IN ('own_fleet', 'third_party')),
	is_default BOOLEAN NOT NULL DEFAULT false,
	config     JSONB NOT NULL DEFAULT '{}',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX shipping_methods_tenant_code_idx ON shipping_methods (tenant_id, code);
CREATE UNIQUE INDEX shipping_methods_tenant_default_idx ON shipping_methods (tenant_id) WHERE is_default;

CREATE TABLE shipments (
	id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id     UUID NOT NULL,
	order_id      TEXT NOT NULL,
	customer_id   TEXT NOT NULL DEFAULT '',
	method_code   TEXT NOT NULL DEFAULT '',
	dispatcher    TEXT NOT NULL,
	status        TEXT NOT NULL
		CHECK (status IN ('dispatched', 'in_transit', 'delivered', 'cancelled')),
	provider_ref  TEXT NOT NULL DEFAULT '',
	tracking_url  TEXT NOT NULL DEFAULT '',
	recipient_name TEXT NOT NULL DEFAULT '',
	address_line  TEXT NOT NULL DEFAULT '',
	city          TEXT NOT NULL DEFAULT '',
	country       TEXT NOT NULL DEFAULT '',
	postal_code   TEXT NOT NULL DEFAULT '',
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- one shipment per order in v1 (also the event-replay guard)
CREATE UNIQUE INDEX shipments_tenant_order_idx ON shipments (tenant_id, order_id);
CREATE INDEX shipments_tenant_customer_idx ON shipments (tenant_id, customer_id);

CREATE TABLE consumed_events (
	event_id       TEXT PRIMARY KEY,
	consumer_group TEXT NOT NULL,
	event_type     TEXT NOT NULL,
	processed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION current_tenant() RETURNS uuid
LANGUAGE sql STABLE AS $$ SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid $$;

ALTER TABLE shipping_methods ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipping_methods FORCE ROW LEVEL SECURITY;
CREATE POLICY shipping_methods_tenant_isolation ON shipping_methods
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());

ALTER TABLE shipments ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipments FORCE ROW LEVEL SECURITY;
CREATE POLICY shipments_tenant_isolation ON shipments
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
