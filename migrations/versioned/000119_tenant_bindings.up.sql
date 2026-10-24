-- Managed tenants: maps a stable external (SaaS) tenant reference to a
-- WeKnora workspace so the SaaS never stores WeKnora's numeric tenant ids.
-- SSO tokens may carry the external reference instead of the numeric id;
-- disabled bindings reject logins with 403 while keeping workspace data.

CREATE TABLE IF NOT EXISTS tenant_bindings (
    id BIGSERIAL PRIMARY KEY,
    external_source VARCHAR(64) NOT NULL DEFAULT 'qh-saas',
    external_tenant_id VARCHAR(128) NOT NULL,
    tenant_id BIGINT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT uq_tenant_bindings_external UNIQUE (external_source, external_tenant_id)
);

CREATE INDEX IF NOT EXISTS idx_tenant_bindings_tenant ON tenant_bindings (tenant_id);

DO $$ BEGIN RAISE NOTICE '[Migration 000119] Created tenant_bindings'; END $$;
