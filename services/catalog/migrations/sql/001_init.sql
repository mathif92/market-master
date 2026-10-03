-- catalog schema (RLS: defense-in-depth on top of tenant-scoped queries)

CREATE TABLE categories (
	id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id  UUID NOT NULL,
	parent_id  UUID REFERENCES categories(id) ON DELETE RESTRICT,
	name       TEXT NOT NULL,
	slug       TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX categories_tenant_slug_idx ON categories (tenant_id, slug);
CREATE INDEX categories_tenant_idx ON categories (tenant_id);

CREATE TABLE products (
	id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id   UUID NOT NULL,
	category_id UUID REFERENCES categories(id) ON DELETE RESTRICT,
	name        TEXT NOT NULL,
	slug        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	price_cents BIGINT NOT NULL CHECK (price_cents >= 0),
	currency    TEXT NOT NULL DEFAULT 'USD',
	status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX products_tenant_slug_idx ON products (tenant_id, slug);
CREATE INDEX products_tenant_category_idx ON products (tenant_id, category_id);

-- RLS helpers + policies
CREATE OR REPLACE FUNCTION current_tenant() RETURNS uuid
LANGUAGE sql STABLE AS $$ SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid $$;

ALTER TABLE categories ENABLE ROW LEVEL SECURITY;
ALTER TABLE categories FORCE ROW LEVEL SECURITY;
CREATE POLICY categories_tenant_isolation ON categories
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());

ALTER TABLE products ENABLE ROW LEVEL SECURITY;
ALTER TABLE products FORCE ROW LEVEL SECURITY;
CREATE POLICY products_tenant_isolation ON products
	USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
