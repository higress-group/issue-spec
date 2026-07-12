CREATE TABLE pat_repositories (
    personal_access_token_id uuid NOT NULL REFERENCES personal_access_tokens(id) ON DELETE CASCADE,
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT pat_repositories_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT pat_repositories_primary PRIMARY KEY (personal_access_token_id, organization_id, repository_id)
);

CREATE INDEX pat_repositories_repo_token_idx
    ON pat_repositories (organization_id, repository_id, personal_access_token_id);

CREATE TABLE service_accounts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    organization_id uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name text NOT NULL,
    name_key text GENERATED ALWAYS AS (lower(name)) STORED,
    created_by_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    disabled_at timestamptz,
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT service_accounts_name_nonempty CHECK (btrim(name) <> ''),
    CONSTRAINT service_accounts_version_positive CHECK (representation_version > 0),
    CONSTRAINT service_accounts_disabled_valid CHECK (disabled_at IS NULL OR disabled_at >= created_at),
    CONSTRAINT service_accounts_org_name_unique UNIQUE (organization_id, name_key)
);

CREATE TABLE recovery_credentials (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_prefix text NOT NULL,
    token_hash bytea NOT NULL,
    scope text NOT NULL,
    issued_by text NOT NULL,
    reason text NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    revoked_at timestamptz,
    audit_event_id uuid NOT NULL REFERENCES audit_events(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT recovery_credentials_prefix_nonempty CHECK (btrim(token_prefix) <> ''),
    CONSTRAINT recovery_credentials_hash_nonempty CHECK (octet_length(token_hash) > 0),
    CONSTRAINT recovery_credentials_scope_valid CHECK (scope = 'site-admin-recovery'),
    CONSTRAINT recovery_credentials_issued_by_nonempty CHECK (btrim(issued_by) <> ''),
    CONSTRAINT recovery_credentials_reason_nonempty CHECK (btrim(reason) <> ''),
    CONSTRAINT recovery_credentials_expiry_valid CHECK (expires_at > created_at),
    CONSTRAINT recovery_credentials_consumed_valid CHECK (consumed_at IS NULL OR consumed_at >= created_at),
    CONSTRAINT recovery_credentials_revoked_valid CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    CONSTRAINT recovery_credentials_prefix_unique UNIQUE (token_prefix),
    CONSTRAINT recovery_credentials_hash_unique UNIQUE (token_hash)
);

CREATE INDEX recovery_credentials_active_idx
    ON recovery_credentials (token_prefix, expires_at)
    WHERE consumed_at IS NULL AND revoked_at IS NULL;
