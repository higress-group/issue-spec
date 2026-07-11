-- Migration 0001 predates schema-scoped installations and creates pgcrypto in
-- the current schema. Move the relocatable extension to a durable namespace so
-- dropping one tenant/test schema cannot remove it for every other schema.
CREATE SCHEMA IF NOT EXISTS issue_spec_extensions;

DO $block$
DECLARE
    extension_schema text;
BEGIN
    SELECT n.nspname INTO extension_schema
    FROM pg_extension e
    JOIN pg_namespace n ON n.oid = e.extnamespace
    WHERE e.extname = 'pgcrypto';

    IF extension_schema IS NULL THEN
        CREATE EXTENSION pgcrypto WITH SCHEMA issue_spec_extensions;
    ELSIF extension_schema <> 'issue_spec_extensions' THEN
        ALTER EXTENSION pgcrypto SET SCHEMA issue_spec_extensions;
    END IF;
END
$block$;

SELECT set_config(
    'search_path',
    quote_ident(current_schema()) || ',issue_spec_extensions',
    true
);

CREATE FUNCTION issue_spec_stable_numeric_id(value text) RETURNS bigint
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE AS $function$
DECLARE
    bytes bytea;
    result bigint;
BEGIN
    bytes := issue_spec_extensions.digest(convert_to(value, 'UTF8'), 'sha256');
    result :=
        ((get_byte(bytes, 0)::bigint & 127) << 56) |
        (get_byte(bytes, 1)::bigint << 48) |
        (get_byte(bytes, 2)::bigint << 40) |
        (get_byte(bytes, 3)::bigint << 32) |
        (get_byte(bytes, 4)::bigint << 24) |
        (get_byte(bytes, 5)::bigint << 16) |
        (get_byte(bytes, 6)::bigint << 8) |
        get_byte(bytes, 7)::bigint;
    IF result = 0 THEN
        RETURN 1;
    END IF;
    RETURN result;
END
$function$;

ALTER TABLE comments
    DROP CONSTRAINT comments_body_nonempty,
    ADD COLUMN compatibility_id bigint GENERATED ALWAYS AS (
        issue_spec_stable_numeric_id(id::text)
    ) STORED,
    ADD CONSTRAINT comments_compatibility_id_positive CHECK (compatibility_id > 0),
    ADD CONSTRAINT comments_compatibility_id_unique UNIQUE (compatibility_id);

ALTER TABLE projection_anomalies
    DROP CONSTRAINT projection_anomalies_repo_key_unique,
    ADD CONSTRAINT projection_anomalies_source_key_unique UNIQUE (
        organization_id, repository_id, projection_name, source_type, source_id, anomaly_key
    );

CREATE INDEX comments_repo_updated_idx
    ON comments (organization_id, repository_id, updated_at, id);
CREATE INDEX comments_issue_updated_idx
    ON comments (organization_id, repository_id, issue_id, updated_at, id);
