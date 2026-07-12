-- Compatibility IDs cross a JSON boundary and are used in browser-built API
-- paths. Recreate the stored generated columns after narrowing the stable hash
-- to JavaScript's exactly representable positive integer range.
ALTER TABLE comments
    DROP CONSTRAINT comments_compatibility_id_positive,
    DROP CONSTRAINT comments_compatibility_id_unique,
    DROP COLUMN compatibility_id;

ALTER TABLE comment_reactions
    DROP CONSTRAINT comment_reactions_compatibility_id_positive,
    DROP CONSTRAINT comment_reactions_compatibility_id_unique,
    DROP COLUMN compatibility_id;

CREATE OR REPLACE FUNCTION issue_spec_stable_numeric_id(value text) RETURNS bigint
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE AS $function$
DECLARE
    bytes bytea;
    result bigint;
BEGIN
    bytes := issue_spec_extensions.digest(convert_to(value, 'UTF8'), 'sha256');
    result :=
        ((get_byte(bytes, 1)::bigint & 31) << 48) |
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
    ADD COLUMN compatibility_id bigint GENERATED ALWAYS AS (
        issue_spec_stable_numeric_id(id::text)
    ) STORED,
    ADD CONSTRAINT comments_compatibility_id_positive CHECK (
        compatibility_id > 0 AND compatibility_id <= 9007199254740991
    ),
    ADD CONSTRAINT comments_compatibility_id_unique UNIQUE (compatibility_id);

ALTER TABLE comment_reactions
    ADD COLUMN compatibility_id bigint GENERATED ALWAYS AS (
        issue_spec_stable_numeric_id(id::text)
    ) STORED,
    ADD CONSTRAINT comment_reactions_compatibility_id_positive CHECK (
        compatibility_id > 0 AND compatibility_id <= 9007199254740991
    ),
    ADD CONSTRAINT comment_reactions_compatibility_id_unique UNIQUE (compatibility_id);
