-- Migration 0009 intentionally changed the public numeric compatibility ID.
-- Rebuild the old 63-bit value from each stable comment UUID, then repair every
-- mutable Markdown source and projection in the same migration transaction.
CREATE FUNCTION pg_temp.issue_spec_v10_legacy_numeric_id(value text) RETURNS bigint
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

CREATE TEMP TABLE issue_spec_v10_comment_id_map (
    comment_id uuid PRIMARY KEY,
    legacy_id bigint NOT NULL UNIQUE,
    safe_id bigint NOT NULL UNIQUE,
    legacy_fragment text NOT NULL UNIQUE,
    safe_fragment text NOT NULL
) ON COMMIT DROP;

INSERT INTO issue_spec_v10_comment_id_map (
    comment_id, legacy_id, safe_id, legacy_fragment, safe_fragment
)
SELECT id, legacy_id, compatibility_id,
       '#issuecomment-' || legacy_id::text,
       '#issuecomment-' || compatibility_id::text
FROM (
    SELECT id,
           pg_temp.issue_spec_v10_legacy_numeric_id(id::text) AS legacy_id,
           compatibility_id
    FROM comments
) AS mapped
WHERE legacy_id <> compatibility_id;

-- Scan the original input exactly once so one replacement can never be
-- interpreted as another legacy fragment. Unknown and already-safe fragments
-- remain byte-for-byte unchanged, including values outside bigint range.
CREATE FUNCTION pg_temp.issue_spec_v10_rewrite_fragments(value text) RETURNS text
LANGUAGE plpgsql STABLE STRICT PARALLEL RESTRICTED AS $function$
DECLARE
    remaining text := value;
    rewritten text := '';
    captured text[];
    token text;
    replacement text;
    token_position integer;
BEGIN
    LOOP
        captured := regexp_match(remaining, '#issuecomment-([0-9]+)');
        EXIT WHEN captured IS NULL;

        token := '#issuecomment-' || captured[1];
        token_position := strpos(remaining, token);
        rewritten := rewritten || substr(remaining, 1, token_position - 1);

        SELECT safe_fragment INTO replacement
        FROM issue_spec_v10_comment_id_map
        WHERE legacy_fragment = token;
        rewritten := rewritten || COALESCE(replacement, token);
        remaining := substr(remaining, token_position + length(token));
    END LOOP;
    RETURN rewritten || remaining;
END
$function$;

-- Projection metadata contains parsed link arrays derived from Markdown. Walk
-- JSON recursively and rewrite only string leaves, preserving every key,
-- scalar, source identifier and non-link value.
CREATE FUNCTION pg_temp.issue_spec_v10_rewrite_json_fragments(value jsonb) RETURNS jsonb
LANGUAGE plpgsql STABLE STRICT PARALLEL RESTRICTED AS $function$
DECLARE
    rewritten jsonb;
BEGIN
    CASE jsonb_typeof(value)
    WHEN 'string' THEN
        RETURN to_jsonb(pg_temp.issue_spec_v10_rewrite_fragments(value #>> '{}'));
    WHEN 'array' THEN
        SELECT COALESCE(
            jsonb_agg(pg_temp.issue_spec_v10_rewrite_json_fragments(item.value)
                      ORDER BY item.ordinality),
            '[]'::jsonb
        ) INTO rewritten
        FROM jsonb_array_elements(value) WITH ORDINALITY AS item(value, ordinality);
        RETURN rewritten;
    WHEN 'object' THEN
        SELECT COALESCE(
            jsonb_object_agg(item.key,
                pg_temp.issue_spec_v10_rewrite_json_fragments(item.value)),
            '{}'::jsonb
        ) INTO rewritten
        FROM jsonb_each(value) AS item(key, value);
        RETURN rewritten;
    ELSE
        RETURN value;
    END CASE;
END
$function$;

CREATE TEMP TABLE issue_spec_v10_changed_issues ON COMMIT DROP AS
WITH candidates AS (
    SELECT id, pg_temp.issue_spec_v10_rewrite_fragments(body) AS body
    FROM issues
    WHERE body LIKE '%#issuecomment-%'
), updated AS (
    UPDATE issues AS issue
    SET body = candidate.body,
        representation_version = issue.representation_version + 1,
        updated_at = clock_timestamp()
    FROM candidates AS candidate
    WHERE issue.id = candidate.id
      AND issue.body IS DISTINCT FROM candidate.body
    RETURNING issue.organization_id, issue.repository_id, issue.id AS issue_id
)
SELECT * FROM updated;

CREATE TEMP TABLE issue_spec_v10_changed_comments ON COMMIT DROP AS
WITH candidates AS (
    SELECT id, pg_temp.issue_spec_v10_rewrite_fragments(body) AS body
    FROM comments
    WHERE body LIKE '%#issuecomment-%'
), updated AS (
    UPDATE comments AS comment
    SET body = candidate.body,
        representation_version = comment.representation_version + 1,
        updated_at = clock_timestamp()
    FROM candidates AS candidate
    WHERE comment.id = candidate.id
      AND comment.body IS DISTINCT FROM candidate.body
    RETURNING comment.organization_id, comment.repository_id,
              comment.issue_id, comment.id AS comment_id
)
SELECT * FROM updated;

CREATE TEMP TABLE issue_spec_v10_changed_artifacts ON COMMIT DROP AS
WITH candidates AS (
    SELECT id,
           pg_temp.issue_spec_v10_rewrite_fragments(content) AS content,
           pg_temp.issue_spec_v10_rewrite_json_fragments(metadata) AS metadata
    FROM issue_spec_artifacts
    WHERE content LIKE '%#issuecomment-%'
       OR metadata::text LIKE '%#issuecomment-%'
), updated AS (
    UPDATE issue_spec_artifacts AS artifact
    SET content = candidate.content,
        metadata = candidate.metadata,
        representation_version = artifact.representation_version + 1,
        updated_at = clock_timestamp()
    FROM candidates AS candidate
    WHERE artifact.id = candidate.id
      AND (artifact.content IS DISTINCT FROM candidate.content
           OR artifact.metadata IS DISTINCT FROM candidate.metadata)
    RETURNING artifact.organization_id, artifact.repository_id,
              artifact.id AS artifact_id
)
SELECT * FROM updated;

CREATE TEMP TABLE issue_spec_v10_changed_typed_comments ON COMMIT DROP AS
WITH candidates AS (
    SELECT id,
           pg_temp.issue_spec_v10_rewrite_fragments(body) AS body,
           pg_temp.issue_spec_v10_rewrite_json_fragments(metadata) AS metadata
    FROM issue_spec_typed_comments
    WHERE body LIKE '%#issuecomment-%'
       OR metadata::text LIKE '%#issuecomment-%'
), updated AS (
    UPDATE issue_spec_typed_comments AS typed_comment
    SET body = candidate.body,
        metadata = candidate.metadata,
        representation_version = typed_comment.representation_version + 1,
        updated_at = clock_timestamp()
    FROM candidates AS candidate
    WHERE typed_comment.id = candidate.id
      AND (typed_comment.body IS DISTINCT FROM candidate.body
           OR typed_comment.metadata IS DISTINCT FROM candidate.metadata)
    RETURNING typed_comment.organization_id, typed_comment.repository_id,
              typed_comment.issue_id, typed_comment.id AS typed_comment_id
)
SELECT * FROM updated;

-- Match normal mutation invalidation semantics without replaying outbox hooks:
-- this is a compatibility repair, not a new user-authored revision.
UPDATE issues AS issue
SET comments_collection_version = issue.comments_collection_version + 1,
    updated_at = clock_timestamp()
WHERE EXISTS (
    SELECT 1 FROM issue_spec_v10_changed_comments AS changed
    WHERE changed.organization_id = issue.organization_id
      AND changed.repository_id = issue.repository_id
      AND changed.issue_id = issue.id
);

UPDATE repos AS repository
SET issues_collection_version = repository.issues_collection_version + 1,
    updated_at = clock_timestamp()
WHERE EXISTS (
    SELECT 1 FROM issue_spec_v10_changed_issues AS changed
    WHERE changed.organization_id = repository.organization_id
      AND changed.repository_id = repository.id
) OR EXISTS (
    SELECT 1 FROM issue_spec_v10_changed_comments AS changed
    WHERE changed.organization_id = repository.organization_id
      AND changed.repository_id = repository.id
);

UPDATE repos AS repository
SET comments_collection_version = repository.comments_collection_version + 1,
    updated_at = clock_timestamp()
WHERE EXISTS (
    SELECT 1 FROM issue_spec_v10_changed_comments AS changed
    WHERE changed.organization_id = repository.organization_id
      AND changed.repository_id = repository.id
);

UPDATE repos AS repository
SET artifacts_collection_version = repository.artifacts_collection_version + 1,
    updated_at = clock_timestamp()
WHERE EXISTS (
    SELECT 1 FROM issue_spec_v10_changed_artifacts AS changed
    WHERE changed.organization_id = repository.organization_id
      AND changed.repository_id = repository.id
) OR EXISTS (
    SELECT 1 FROM issue_spec_v10_changed_typed_comments AS changed
    WHERE changed.organization_id = repository.organization_id
      AND changed.repository_id = repository.id
);

-- event_outbox payloads are immutable schema-versioned envelopes. Their
-- payload_hash is the idempotency authority and webhook deliveries/attempts
-- are signed audit snapshots of those exact bytes. Never rewrite them here:
-- stable_id remains the replay authority, while a legacy numeric_id or raw_body
-- is interpreted as the schema-v1 value captured when the event was emitted.
