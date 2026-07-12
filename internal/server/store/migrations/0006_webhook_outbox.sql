ALTER TABLE repos
    ADD COLUMN next_event_sequence bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT repos_next_event_sequence_positive CHECK (next_event_sequence > 0);

ALTER TABLE event_outbox
    ADD COLUMN schema_version integer NOT NULL DEFAULT 1,
    ADD COLUMN repository_sequence bigint,
    ADD CONSTRAINT event_outbox_schema_version_positive CHECK (schema_version > 0);

WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY organization_id, repository_id
               ORDER BY created_at, id
           ) AS repository_sequence
    FROM event_outbox
)
UPDATE event_outbox AS event
SET repository_sequence = ranked.repository_sequence
FROM ranked
WHERE event.id = ranked.id;

ALTER TABLE event_outbox
    ALTER COLUMN repository_sequence SET NOT NULL,
    ADD CONSTRAINT event_outbox_repository_sequence_positive CHECK (repository_sequence > 0),
    ADD CONSTRAINT event_outbox_repository_sequence_unique UNIQUE (
        organization_id, repository_id, repository_sequence
    );

UPDATE repos AS repository
SET next_event_sequence = COALESCE(sequences.next_sequence, 1)
FROM (
    SELECT organization_id, repository_id, max(repository_sequence) + 1 AS next_sequence
    FROM event_outbox
    GROUP BY organization_id, repository_id
) AS sequences
WHERE repository.organization_id = sequences.organization_id
  AND repository.id = sequences.repository_id;

CREATE FUNCTION allocate_event_repository_sequence()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
    IF NEW.repository_sequence IS NULL THEN
        UPDATE repos
        SET next_event_sequence = next_event_sequence + 1
        WHERE organization_id = NEW.organization_id
          AND id = NEW.repository_id
        RETURNING next_event_sequence - 1 INTO NEW.repository_sequence;

        IF NEW.repository_sequence IS NULL THEN
            RAISE EXCEPTION 'event repository does not exist'
                USING ERRCODE = '23503';
        END IF;
    END IF;
    RETURN NEW;
END
$function$;

CREATE TRIGGER event_outbox_allocate_repository_sequence
BEFORE INSERT ON event_outbox
FOR EACH ROW EXECUTE FUNCTION allocate_event_repository_sequence();

CREATE INDEX event_outbox_repo_pending_sequence_idx
    ON event_outbox (organization_id, repository_id, repository_sequence)
    WHERE published_at IS NULL;

CREATE INDEX event_outbox_pending_available_sequence_idx
    ON event_outbox (available_at, organization_id, repository_id, repository_sequence)
    WHERE published_at IS NULL;

ALTER TABLE webhook_subscriptions
    ADD COLUMN retry_max_attempts integer NOT NULL DEFAULT 8,
    ADD COLUMN retry_initial_backoff interval NOT NULL DEFAULT interval '1 second',
    ADD COLUMN retry_max_backoff interval NOT NULL DEFAULT interval '5 minutes',
    ADD CONSTRAINT webhook_subscriptions_retry_max_attempts_valid CHECK (
        retry_max_attempts BETWEEN 1 AND 100
    ),
    ADD CONSTRAINT webhook_subscriptions_retry_initial_backoff_positive CHECK (
        retry_initial_backoff > interval '0 seconds'
    ),
    ADD CONSTRAINT webhook_subscriptions_retry_max_backoff_valid CHECK (
        retry_max_backoff >= retry_initial_backoff
    );

ALTER TABLE webhook_secret_versions
    ADD COLUMN encryption_key_id text NOT NULL DEFAULT 'legacy',
    ADD COLUMN accept_until timestamptz,
    ADD COLUMN revoked_at timestamptz,
    ADD CONSTRAINT webhook_secret_versions_key_id_nonempty CHECK (
        btrim(encryption_key_id) <> ''
    ),
    ADD CONSTRAINT webhook_secret_versions_accept_until_valid CHECK (
        accept_until IS NULL OR accept_until >= created_at
    ),
    ADD CONSTRAINT webhook_secret_versions_revoked_valid CHECK (
        revoked_at IS NULL OR revoked_at >= created_at
    );

CREATE INDEX webhook_secret_versions_previous_accept_idx
    ON webhook_secret_versions (organization_id, subscription_id, accept_until DESC)
    WHERE NOT active AND revoked_at IS NULL AND accept_until IS NOT NULL;
