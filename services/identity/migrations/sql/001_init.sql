-- identity service schema (no RLS: this is the system/tenancy scope)

CREATE TABLE tenants (
	id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	slug        TEXT NOT NULL UNIQUE,
	name        TEXT NOT NULL,
	status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
	id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id     UUID REFERENCES tenants(id) ON DELETE CASCADE,
	email         TEXT NOT NULL,
	password_hash TEXT NOT NULL,
	role          TEXT NOT NULL CHECK (role IN ('platform_admin', 'tenant_admin', 'staff', 'customer')),
	status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX users_tenant_email_idx ON users (tenant_id, email) WHERE tenant_id IS NOT NULL;
CREATE UNIQUE INDEX users_platform_email_idx ON users (email) WHERE tenant_id IS NULL;
