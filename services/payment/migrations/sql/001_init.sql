-- payment schema (tokenization proxy: no PANs, only PSP tokens/references)

CREATE TABLE payment_intents (
	id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id     UUID NOT NULL,
	order_id      TEXT NOT NULL,
	customer_id   TEXT NOT NULL,
	amount_cents  BIGINT NOT NULL CHECK (amount_cents >= 0),
	currency      TEXT NOT NULL,
	status        TEXT NOT NULL
		CHECK (status IN ('processing', 'authorized', 'captured', 'failed', 'voided', 'refunded')),
	psp           TEXT NOT NULL DEFAULT 'fake',
	psp_ref       TEXT,
	psp_token     TEXT NOT NULL,
	brand         TEXT NOT NULL DEFAULT '',
	last4         TEXT NOT NULL DEFAULT '',
	failure_reason TEXT NOT NULL DEFAULT '',
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- one settled payment per order (the platform's idempotency at domain level)
CREATE UNIQUE INDEX payment_intents_order_idx ON payment_intents (tenant_id, order_id);

-- cross-tenant lookup for provider webhooks (a PSP only knows its own refs)
CREATE TABLE psp_refs (
	psp_ref   TEXT PRIMARY KEY,
	tenant_id UUID NOT NULL,
	intent_id UUID NOT NULL
);

CREATE TABLE psp_webhooks (
	psp         TEXT NOT NULL,
	event_id    TEXT NOT NULL,
	event_type  TEXT NOT NULL,
	payload     BYTEA NOT NULL,
	received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (psp, event_id)
);

CREATE TABLE consumed_events (
	event_id       TEXT PRIMARY KEY,
	consumer_group TEXT NOT NULL,
	event_type     TEXT NOT NULL,
	processed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION current_tenant() RETURNS uuid
LANGUAGE sql STABLE AS $$ SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid $$;

ALTER TABLE payment_intents ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_intents FORCE ROW LEVEL SECURITY;
CREATE POLICY payment_intents_tenant_isolation ON payment_intents
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
