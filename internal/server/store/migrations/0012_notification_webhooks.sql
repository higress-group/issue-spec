ALTER TABLE webhook_subscriptions
    ADD COLUMN delivery_format text NOT NULL DEFAULT 'issue-spec.v1',
    ADD COLUMN signing_mode text NOT NULL DEFAULT 'bearer',
    ADD COLUMN issue_actions text[] NOT NULL DEFAULT ARRAY['opened','edited','closed','reopened']::text[],
    ADD COLUMN comment_actions text[] NOT NULL DEFAULT ARRAY['created','edited']::text[],
    ADD COLUMN issue_kinds text[] NOT NULL DEFAULT ARRAY['ordinary','proposal','design','implement']::text[],
    ADD COLUMN comment_classes text[] NOT NULL DEFAULT ARRAY['human-untyped','typed']::text[],
    ADD COLUMN actor_classes text[] NOT NULL DEFAULT ARRAY['human']::text[],
    ADD COLUMN destination_query_key_id text,
    ADD COLUMN destination_query_ciphertext bytea,
    ADD COLUMN destination_query_version bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT webhook_subscriptions_delivery_format_valid CHECK (
        delivery_format IN ('issue-spec.v1', 'github.v3')
    ),
    ADD CONSTRAINT webhook_subscriptions_signing_mode_valid CHECK (
        (delivery_format = 'issue-spec.v1' AND signing_mode = 'bearer') OR
        (delivery_format = 'github.v3' AND signing_mode IN ('none', 'hmac-sha256'))
    ),
    ADD CONSTRAINT webhook_subscriptions_issue_actions_valid CHECK (
        issue_actions <@ ARRAY['opened','edited','closed','reopened']::text[]
    ),
    ADD CONSTRAINT webhook_subscriptions_comment_actions_valid CHECK (
        comment_actions <@ ARRAY['created','edited']::text[]
    ),
    ADD CONSTRAINT webhook_subscriptions_issue_kinds_valid CHECK (
        issue_kinds <@ ARRAY['ordinary','proposal','design','implement']::text[]
    ),
    ADD CONSTRAINT webhook_subscriptions_comment_classes_valid CHECK (
        comment_classes <@ ARRAY['human-untyped','typed']::text[]
    ),
    ADD CONSTRAINT webhook_subscriptions_actor_classes_valid CHECK (
        actor_classes <@ ARRAY['human']::text[]
    ),
    ADD CONSTRAINT webhook_subscriptions_policy_nonempty CHECK (
        delivery_format = 'issue-spec.v1' OR
        (cardinality(issue_actions) > 0 OR cardinality(comment_actions) > 0)
    ),
    ADD CONSTRAINT webhook_subscriptions_destination_query_consistent CHECK (
        (destination_query_key_id IS NULL AND destination_query_ciphertext IS NULL AND destination_query_version = 0) OR
        (btrim(destination_query_key_id) <> '' AND octet_length(destination_query_ciphertext) > 0 AND destination_query_version > 0)
    );

ALTER TABLE webhook_deliveries
    ADD COLUMN delivery_format text NOT NULL DEFAULT 'issue-spec.v1',
    ADD COLUMN event_name text NOT NULL DEFAULT 'issue-spec',
    ADD COLUMN action text NOT NULL DEFAULT '',
    ADD COLUMN signing_mode text NOT NULL DEFAULT 'bearer',
    ADD COLUMN rendered_payload bytea,
    ADD COLUMN rendered_payload_hash bytea,
    ADD COLUMN destination_url text,
    ADD COLUMN destination_query_key_id text,
    ADD COLUMN destination_query_ciphertext bytea,
    ADD COLUMN destination_query_version bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT webhook_deliveries_delivery_format_valid CHECK (
        delivery_format IN ('issue-spec.v1', 'github.v3')
    ),
    ADD CONSTRAINT webhook_deliveries_signing_mode_valid CHECK (
        signing_mode IN ('bearer', 'none', 'hmac-sha256')
    ),
    ADD CONSTRAINT webhook_deliveries_rendered_payload_hash_valid CHECK (
        rendered_payload_hash IS NULL OR octet_length(rendered_payload_hash) = 32
    ),
    ADD CONSTRAINT webhook_deliveries_destination_query_consistent CHECK (
        (destination_query_key_id IS NULL AND destination_query_ciphertext IS NULL AND destination_query_version = 0) OR
        (btrim(destination_query_key_id) <> '' AND octet_length(destination_query_ciphertext) > 0 AND destination_query_version > 0)
    );

UPDATE webhook_deliveries AS delivery
SET rendered_payload = convert_to(event.payload::text, 'UTF8'),
    destination_url = subscription.url
FROM event_outbox AS event, webhook_subscriptions AS subscription
WHERE event.id = delivery.event_id
  AND subscription.id = delivery.subscription_id;

ALTER TABLE webhook_deliveries
    ALTER COLUMN rendered_payload SET NOT NULL,
    ALTER COLUMN destination_url SET NOT NULL;

CREATE TABLE webhook_suppressions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    event_id uuid NOT NULL,
    subscription_id uuid NOT NULL,
    event_type text NOT NULL,
    action text NOT NULL,
    issue_kind text NOT NULL,
    comment_class text,
    actor_class text NOT NULL,
    reason text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT webhook_suppressions_event_fk FOREIGN KEY (organization_id, repository_id, event_id)
        REFERENCES event_outbox(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT webhook_suppressions_subscription_fk FOREIGN KEY (organization_id, subscription_id)
        REFERENCES webhook_subscriptions(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT webhook_suppressions_event_subscription_unique UNIQUE (
        organization_id, repository_id, event_id, subscription_id
    )
);

CREATE INDEX webhook_suppressions_subscription_created_idx
    ON webhook_suppressions (organization_id, subscription_id, created_at DESC);

COMMENT ON COLUMN webhook_subscriptions.destination_query_ciphertext IS
    'Encrypted complete destination query. Never expose through API, audit, logs, metrics or errors.';
COMMENT ON COLUMN webhook_deliveries.rendered_payload IS
    'Immutable exact request body frozen when the outbox event is expanded.';
