# Migration Consolidation Context

## Conventions for all consolidated migrations

1. **File naming**: `1XX_domain_name.sql` in `db/migrations/`
2. **Version numbers**: 100-135 range
3. **IF NOT EXISTS**: ALL CREATE TABLE, CREATE INDEX, CREATE SCHEMA, CREATE TYPE must use `IF NOT EXISTS`
4. **ALTER TABLE ADD COLUMN**: Use `DO $$ BEGIN ALTER TABLE ... ADD COLUMN ...; EXCEPTION WHEN duplicate_column THEN NULL; END $$;` for existing-DB safety
5. **INSERT/seed data**: Use `INSERT INTO ... ON CONFLICT DO NOTHING` for all seed data
6. **No DROP statements**: Never DROP tables/columns in consolidated migrations (those are nullification patterns we're eliminating)
7. **No RENAME**: Never rename anything (migrations that rename are bridge migrations we're eliminating)
8. **Output**: Write valid PostgreSQL SQL that produces the final state directly
9. **Read ALL listed source files before writing** - the final schema is the cumulative result of all of them
