-- campaigns: time-windowed discounts (sitewide/category/product scope,
-- percent (basis points) or fixed cents-off per unit, optional coupon codes).
-- No stacking: exactly ONE campaign applies per order, chosen at order
-- create from live candidates (status='active' AND now() inside the window).
-- Read-time prices expose only non-code campaigns (sale_price_cents is
-- computed, price_cents stays canonical); requires_code campaigns apply
-- only when a coupon is presented at checkout.

CREATE TABLE campaigns (
	id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id          UUID NOT NULL,
	name               TEXT NOT NULL CHECK (name <> ''),
	status             TEXT NOT NULL DEFAULT 'draft'
		CHECK (status IN ('draft', 'active', 'archived')),
	rule_type          TEXT NOT NULL CHECK (rule_type IN ('percent', 'fixed')),
	-- percent: basis points (10000 = 100%); fixed: cents off the unit list price
	rule_value         INT NOT NULL CHECK (rule_value > 0),
	scope_type         TEXT NOT NULL CHECK (scope_type IN ('sitewide', 'category', 'product')),
	scope_id           UUID,
	requires_code      BOOLEAN NOT NULL DEFAULT FALSE,
	starts_at          TIMESTAMPTZ NOT NULL,
	ends_at            TIMESTAMPTZ NOT NULL,
	max_redemptions    INT CHECK (max_redemptions IS NULL OR max_redemptions > 0),
	-- denormalized admission counter (guarded increment, ledger is the truth:
	-- release paths recompute from campaign_redemptions)
	redemptions_count  INT NOT NULL DEFAULT 0 CHECK (redemptions_count >= 0),
	created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
	CHECK (ends_at > starts_at),
	CHECK ((scope_type = 'sitewide' AND scope_id IS NULL)
		OR (scope_type <> 'sitewide' AND scope_id IS NOT NULL))
);
CREATE INDEX campaigns_tenant_status_idx ON campaigns (tenant_id, status);

CREATE TABLE campaign_codes (
	id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id    UUID NOT NULL,
	campaign_id  UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
	code         TEXT NOT NULL,
	max_uses     INT CHECK (max_uses IS NULL OR max_uses > 0),
	uses_count   INT NOT NULL DEFAULT 0 CHECK (uses_count >= 0),
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (tenant_id, code)
);
CREATE INDEX campaign_codes_campaign_idx ON campaign_codes (campaign_id);

-- One redemption per (order, campaign): the replay key mirroring
-- stock_reservations. discount_cents is the total across all lines.
CREATE TABLE campaign_redemptions (
	order_id      UUID NOT NULL,
	campaign_id   UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
	tenant_id     UUID NOT NULL,
	code_id       UUID REFERENCES campaign_codes(id) ON DELETE SET NULL,
	discount_cents BIGINT NOT NULL CHECK (discount_cents >= 0),
	status        TEXT NOT NULL DEFAULT 'active'
		CHECK (status IN ('active', 'released')),
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (order_id, campaign_id)
);
CREATE INDEX campaign_redemptions_campaign_idx
	ON campaign_redemptions (tenant_id, campaign_id, status);
CREATE INDEX campaign_redemptions_code_idx
	ON campaign_redemptions (code_id) WHERE status = 'active';

-- RLS: isolated like every other domain table
ALTER TABLE campaigns ENABLE ROW LEVEL SECURITY;
ALTER TABLE campaigns FORCE ROW LEVEL SECURITY;
CREATE POLICY campaigns_tenant_isolation ON campaigns
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());

ALTER TABLE campaign_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE campaign_codes FORCE ROW LEVEL SECURITY;
CREATE POLICY campaign_codes_tenant_isolation ON campaign_codes
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());

ALTER TABLE campaign_redemptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE campaign_redemptions FORCE ROW LEVEL SECURITY;
CREATE POLICY campaign_redemptions_tenant_isolation ON campaign_redemptions
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
