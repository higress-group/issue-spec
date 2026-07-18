ALTER TABLE users
    ADD COLUMN onboarding_completed_at timestamptz,
    ADD COLUMN notification_email text,
    ADD COLUMN notification_email_key text GENERATED ALWAYS AS (lower(notification_email)) STORED,
    ADD COLUMN notification_email_verified_at timestamptz,
    ADD CONSTRAINT users_notification_email_nonempty CHECK (
        notification_email IS NULL OR (
            notification_email = btrim(notification_email)
            AND notification_email <> ''
            AND char_length(notification_email) <= 320
        )
    ),
    ADD CONSTRAINT users_notification_email_verified_consistent CHECK (
        (notification_email IS NULL AND notification_email_verified_at IS NULL) OR
        (notification_email IS NOT NULL AND notification_email_verified_at IS NOT NULL)
    );

CREATE UNIQUE INDEX users_notification_email_key_unique
    ON users (notification_email_key)
    WHERE notification_email_key IS NOT NULL;

CREATE TABLE email_verification_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    pending_email text NOT NULL,
    pending_email_key text GENERATED ALWAYS AS (lower(pending_email)) STORED,
    token_digest bytea NOT NULL,
    token_ciphertext bytea,
    expires_at timestamptz NOT NULL,
    sent_at timestamptz,
    consumed_at timestamptz,
    superseded_at timestamptz,
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT email_verification_requests_email_valid CHECK (
        pending_email = btrim(pending_email)
        AND pending_email <> ''
        AND char_length(pending_email) <= 320
    ),
    CONSTRAINT email_verification_requests_digest_valid CHECK (octet_length(token_digest) = 32),
    CONSTRAINT email_verification_requests_ciphertext_valid CHECK (
        token_ciphertext IS NULL OR octet_length(token_ciphertext) > 0
    ),
    CONSTRAINT email_verification_requests_expiry_valid CHECK (expires_at > created_at),
    CONSTRAINT email_verification_requests_sent_valid CHECK (sent_at IS NULL OR sent_at >= created_at),
    CONSTRAINT email_verification_requests_consumed_valid CHECK (
        consumed_at IS NULL OR (consumed_at >= created_at AND consumed_at <= expires_at)
    ),
    CONSTRAINT email_verification_requests_superseded_valid CHECK (
        superseded_at IS NULL OR superseded_at >= created_at
    ),
    CONSTRAINT email_verification_requests_terminal_exclusive CHECK (
        consumed_at IS NULL OR superseded_at IS NULL
    ),
    CONSTRAINT email_verification_requests_version_positive CHECK (representation_version > 0),
    CONSTRAINT email_verification_requests_token_unique UNIQUE (token_digest)
);

CREATE UNIQUE INDEX email_verification_requests_one_current_user
    ON email_verification_requests (user_id)
    WHERE consumed_at IS NULL AND superseded_at IS NULL;

CREATE INDEX email_verification_requests_user_created_idx
    ON email_verification_requests (user_id, created_at DESC, id);

CREATE INDEX email_verification_requests_current_expiry_idx
    ON email_verification_requests (expires_at, id)
    WHERE consumed_at IS NULL AND superseded_at IS NULL;

CREATE TABLE comment_mentions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    comment_id uuid NOT NULL,
    mentioned_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    first_seen_representation_version bigint NOT NULL,
    last_seen_representation_version bigint NOT NULL,
    present boolean NOT NULL DEFAULT true,
    first_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT comment_mentions_comment_fk
        FOREIGN KEY (organization_id, repository_id, issue_id, comment_id)
        REFERENCES comments(organization_id, repository_id, issue_id, id) ON DELETE CASCADE,
    CONSTRAINT comment_mentions_versions_valid CHECK (
        first_seen_representation_version > 0
        AND last_seen_representation_version >= first_seen_representation_version
    ),
    CONSTRAINT comment_mentions_times_valid CHECK (last_seen_at >= first_seen_at),
    CONSTRAINT comment_mentions_comment_user_unique UNIQUE (
        organization_id, repository_id, comment_id, mentioned_user_id
    )
);

CREATE INDEX comment_mentions_user_created_idx
    ON comment_mentions (mentioned_user_id, created_at DESC, id);

CREATE INDEX comment_mentions_comment_present_idx
    ON comment_mentions (organization_id, repository_id, comment_id, mentioned_user_id)
    WHERE present;

CREATE TABLE change_notification_milestones (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    change_key text NOT NULL,
    change_key_key text GENERATED ALWAYS AS (lower(change_key)) STORED,
    milestone text NOT NULL,
    triggering_issue_id uuid NOT NULL,
    triggering_comment_id uuid,
    actor_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT change_notification_milestones_repository_fk
        FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT change_notification_milestones_issue_fk
        FOREIGN KEY (organization_id, repository_id, triggering_issue_id)
        REFERENCES issues(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT change_notification_milestones_comment_fk
        FOREIGN KEY (organization_id, repository_id, triggering_issue_id, triggering_comment_id)
        REFERENCES comments(organization_id, repository_id, issue_id, id) ON DELETE CASCADE,
    CONSTRAINT change_notification_milestones_change_key_valid CHECK (
        change_key = btrim(change_key) AND change_key <> '' AND char_length(change_key) <= 200
    ),
    CONSTRAINT change_notification_milestones_milestone_valid CHECK (
        milestone IN ('proposal', 'design', 'implement', 'completed')
    ),
    CONSTRAINT change_notification_milestones_occurred_valid CHECK (occurred_at <= created_at),
    CONSTRAINT change_notification_milestones_repo_id_unique UNIQUE (organization_id, repository_id, id),
    CONSTRAINT change_notification_milestones_change_stage_unique UNIQUE (
        organization_id, repository_id, change_key_key, milestone
    )
);

CREATE INDEX change_notification_milestones_repo_occurred_idx
    ON change_notification_milestones (organization_id, repository_id, occurred_at DESC, id);

CREATE TABLE email_deliveries (
    id uuid PRIMARY KEY,
    kind text NOT NULL,
    idempotency_key text NOT NULL,
    recipient_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    organization_id uuid,
    repository_id uuid,
    verification_request_id uuid REFERENCES email_verification_requests(id) ON DELETE CASCADE,
    comment_id uuid,
    issue_id uuid,
    milestone_id uuid,
    render_snapshot jsonb NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_expires_at timestamptz,
    delivered_at timestamptz,
    last_reason text,
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT email_deliveries_repository_fk
        FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) MATCH FULL ON DELETE CASCADE,
    CONSTRAINT email_deliveries_comment_fk
        FOREIGN KEY (organization_id, repository_id, comment_id)
        REFERENCES comments(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT email_deliveries_issue_fk
        FOREIGN KEY (organization_id, repository_id, issue_id)
        REFERENCES issues(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT email_deliveries_milestone_fk
        FOREIGN KEY (organization_id, repository_id, milestone_id)
        REFERENCES change_notification_milestones(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT email_deliveries_kind_valid CHECK (
        kind IN ('verification', 'mention', 'repo_issue_created', 'change_milestone')
    ),
    CONSTRAINT email_deliveries_key_valid CHECK (
        idempotency_key = btrim(idempotency_key)
        AND idempotency_key <> ''
        AND char_length(idempotency_key) <= 512
    ),
    CONSTRAINT email_deliveries_reference_valid CHECK (
        (kind = 'verification'
            AND organization_id IS NULL AND repository_id IS NULL
            AND verification_request_id IS NOT NULL
            AND comment_id IS NULL AND issue_id IS NULL AND milestone_id IS NULL) OR
        (kind = 'mention'
            AND organization_id IS NOT NULL AND repository_id IS NOT NULL
            AND verification_request_id IS NULL
            AND comment_id IS NOT NULL AND issue_id IS NULL AND milestone_id IS NULL) OR
        (kind = 'repo_issue_created'
            AND organization_id IS NOT NULL AND repository_id IS NOT NULL
            AND verification_request_id IS NULL
            AND comment_id IS NULL AND issue_id IS NOT NULL AND milestone_id IS NULL) OR
        (kind = 'change_milestone'
            AND organization_id IS NOT NULL AND repository_id IS NOT NULL
            AND verification_request_id IS NULL
            AND comment_id IS NULL AND issue_id IS NULL AND milestone_id IS NOT NULL)
    ),
    CONSTRAINT email_deliveries_snapshot_valid CHECK (
        jsonb_typeof(render_snapshot) = 'object'
        AND octet_length(convert_to(render_snapshot::text, 'UTF8')) <= 262144
    ),
    CONSTRAINT email_deliveries_state_valid CHECK (
        state IN ('pending', 'delivering', 'succeeded', 'failed', 'suppressed')
    ),
    CONSTRAINT email_deliveries_attempts_valid CHECK (attempts BETWEEN 0 AND 5),
    CONSTRAINT email_deliveries_lease_valid CHECK (
        (state = 'delivering' AND lease_expires_at IS NOT NULL) OR
        (state <> 'delivering' AND lease_expires_at IS NULL)
    ),
    CONSTRAINT email_deliveries_delivered_valid CHECK (
        (state = 'succeeded' AND delivered_at IS NOT NULL) OR
        (state <> 'succeeded' AND delivered_at IS NULL)
    ),
    CONSTRAINT email_deliveries_reason_valid CHECK (
        last_reason IS NULL OR (
            last_reason ~ '^[a-z][a-z0-9_]{0,63}$'
            AND state IN ('pending', 'failed', 'suppressed')
        )
    ),
    CONSTRAINT email_deliveries_version_positive CHECK (representation_version > 0),
    CONSTRAINT email_deliveries_kind_key_unique UNIQUE (kind, idempotency_key),
    CONSTRAINT email_deliveries_verification_recipient_unique UNIQUE (verification_request_id, recipient_user_id),
    CONSTRAINT email_deliveries_comment_recipient_unique UNIQUE (
        organization_id, repository_id, comment_id, recipient_user_id
    ),
    CONSTRAINT email_deliveries_issue_recipient_unique UNIQUE (
        organization_id, repository_id, issue_id, recipient_user_id
    ),
    CONSTRAINT email_deliveries_milestone_recipient_unique UNIQUE (
        organization_id, repository_id, milestone_id, recipient_user_id
    )
);

CREATE INDEX email_deliveries_ready_idx
    ON email_deliveries (next_attempt_at, created_at, id)
    WHERE state = 'pending';

CREATE INDEX email_deliveries_expired_lease_idx
    ON email_deliveries (lease_expires_at, created_at, id)
    WHERE state = 'delivering';

CREATE INDEX email_deliveries_recipient_created_idx
    ON email_deliveries (recipient_user_id, created_at DESC, id);

COMMENT ON COLUMN users.notification_email IS
    'User-verified notification address. Separate from provider-derived users.email metadata.';
COMMENT ON COLUMN email_verification_requests.token_ciphertext IS
    'Purpose-bound encrypted token retained only until SMTP acceptance, expiry, consumption, or supersession.';
COMMENT ON COLUMN email_deliveries.render_snapshot IS
    'Bounded immutable render input. Recipient addresses and plaintext verification tokens are forbidden.';

-- Run the legacy-account backfill after every schema change in this migration.
-- PostgreSQL rejects DDL on a table while that table still has pending trigger
-- events from an earlier write in the same transaction. Provider email metadata
-- is intentionally not copied into the new notification columns.
UPDATE users
SET onboarding_completed_at = clock_timestamp()
WHERE onboarding_completed_at IS NULL;
