DO $$ BEGIN RAISE NOTICE '[Migration 000119 down] Dropping tenant_bindings'; END $$;

DROP TABLE IF EXISTS tenant_bindings;
