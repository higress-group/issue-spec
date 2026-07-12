CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    login text NOT NULL,
    login_key text GENERATED ALWAYS AS (lower(login)) STORED,
    display_name text NOT NULL,
    email text,
    email_key text GENERATED ALWAYS AS (lower(email)) STORED,
    status text NOT NULL DEFAULT 'active',
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT users_login_nonempty CHECK (btrim(login) <> ''),
    CONSTRAINT users_display_name_nonempty CHECK (btrim(display_name) <> ''),
    CONSTRAINT users_email_nonempty CHECK (email IS NULL OR btrim(email) <> ''),
    CONSTRAINT users_status_valid CHECK (status IN ('active', 'disabled')),
    CONSTRAINT users_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT users_login_key_unique UNIQUE (login_key)
);

CREATE FUNCTION reject_user_login_change()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
    IF NEW.login IS DISTINCT FROM OLD.login THEN
        RAISE EXCEPTION 'user login is immutable'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$function$;

CREATE TRIGGER users_login_immutable
BEFORE UPDATE OF login ON users
FOR EACH ROW EXECUTE FUNCTION reject_user_login_change();

CREATE TABLE auth_providers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    kind text NOT NULL,
    issuer text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    config jsonb NOT NULL DEFAULT '{}'::jsonb,
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT auth_providers_name_nonempty CHECK (btrim(name) <> ''),
    CONSTRAINT auth_providers_kind_nonempty CHECK (btrim(kind) <> ''),
    CONSTRAINT auth_providers_issuer_nonempty CHECK (btrim(issuer) <> ''),
    CONSTRAINT auth_providers_config_object CHECK (jsonb_typeof(config) = 'object'),
    CONSTRAINT auth_providers_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT auth_providers_name_unique UNIQUE (name),
    CONSTRAINT auth_providers_issuer_unique UNIQUE (issuer)
);

CREATE TABLE identities (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_id uuid NOT NULL REFERENCES auth_providers(id) ON DELETE RESTRICT,
    issuer text NOT NULL,
    subject text NOT NULL,
    identity_key text NOT NULL,
    claims jsonb NOT NULL DEFAULT '{}'::jsonb,
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT identities_issuer_nonempty CHECK (btrim(issuer) <> ''),
    CONSTRAINT identities_subject_nonempty CHECK (btrim(subject) <> ''),
    CONSTRAINT identities_identity_key_nonempty CHECK (btrim(identity_key) <> ''),
    CONSTRAINT identities_claims_object CHECK (jsonb_typeof(claims) = 'object'),
    CONSTRAINT identities_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT identities_provider_key_unique UNIQUE (provider_id, identity_key),
    CONSTRAINT identities_provider_subject_unique UNIQUE (provider_id, issuer, subject)
);

CREATE TABLE oauth_login_transactions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id uuid NOT NULL REFERENCES auth_providers(id) ON DELETE CASCADE,
    state_hash bytea NOT NULL,
    nonce_hash bytea NOT NULL,
    pkce_verifier_ciphertext bytea NOT NULL,
    redirect_uri text NOT NULL,
    return_to text,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT oauth_login_transactions_state_hash_nonempty CHECK (octet_length(state_hash) > 0),
    CONSTRAINT oauth_login_transactions_nonce_hash_nonempty CHECK (octet_length(nonce_hash) > 0),
    CONSTRAINT oauth_login_transactions_pkce_nonempty CHECK (octet_length(pkce_verifier_ciphertext) > 0),
    CONSTRAINT oauth_login_transactions_redirect_uri_nonempty CHECK (btrim(redirect_uri) <> ''),
    CONSTRAINT oauth_login_transactions_expiry_valid CHECK (expires_at > created_at),
    CONSTRAINT oauth_login_transactions_consumed_valid CHECK (consumed_at IS NULL OR consumed_at >= created_at),
    CONSTRAINT oauth_login_transactions_state_unique UNIQUE (state_hash)
);

CREATE TABLE sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_prefix text NOT NULL,
    token_hash bytea NOT NULL,
    csrf_hash bytea NOT NULL,
    user_agent text,
    remote_address inet,
    idle_expires_at timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    CONSTRAINT sessions_token_prefix_nonempty CHECK (btrim(token_prefix) <> ''),
    CONSTRAINT sessions_token_hash_nonempty CHECK (octet_length(token_hash) > 0),
    CONSTRAINT sessions_csrf_hash_nonempty CHECK (octet_length(csrf_hash) > 0),
    CONSTRAINT sessions_expiry_valid CHECK (
        idle_expires_at > created_at AND absolute_expires_at >= idle_expires_at
    ),
    CONSTRAINT sessions_last_seen_valid CHECK (last_seen_at >= created_at),
    CONSTRAINT sessions_revoked_valid CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    CONSTRAINT sessions_token_prefix_unique UNIQUE (token_prefix),
    CONSTRAINT sessions_token_hash_unique UNIQUE (token_hash)
);

CREATE TABLE personal_access_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL,
    token_prefix text NOT NULL,
    token_hash bytea NOT NULL,
    representation_version bigint NOT NULL DEFAULT 1,
    expires_at timestamptz,
    last_used_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT personal_access_tokens_name_nonempty CHECK (btrim(name) <> ''),
    CONSTRAINT personal_access_tokens_prefix_nonempty CHECK (btrim(token_prefix) <> ''),
    CONSTRAINT personal_access_tokens_hash_nonempty CHECK (octet_length(token_hash) > 0),
    CONSTRAINT personal_access_tokens_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT personal_access_tokens_expiry_valid CHECK (expires_at IS NULL OR expires_at > created_at),
    CONSTRAINT personal_access_tokens_last_used_valid CHECK (last_used_at IS NULL OR last_used_at >= created_at),
    CONSTRAINT personal_access_tokens_revoked_valid CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    CONSTRAINT personal_access_tokens_prefix_unique UNIQUE (token_prefix),
    CONSTRAINT personal_access_tokens_hash_unique UNIQUE (token_hash)
);

CREATE TABLE pat_scopes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    personal_access_token_id uuid NOT NULL REFERENCES personal_access_tokens(id) ON DELETE CASCADE,
    scope text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT pat_scopes_scope_nonempty CHECK (btrim(scope) <> ''),
    CONSTRAINT pat_scopes_token_scope_unique UNIQUE (personal_access_token_id, scope)
);

CREATE TABLE orgs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    name_key text GENERATED ALWAYS AS (lower(name)) STORED,
    display_name text NOT NULL,
    representation_version bigint NOT NULL DEFAULT 1,
    repositories_collection_version bigint NOT NULL DEFAULT 1,
    members_collection_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT orgs_name_nonempty CHECK (btrim(name) <> ''),
    CONSTRAINT orgs_display_name_nonempty CHECK (btrim(display_name) <> ''),
    CONSTRAINT orgs_versions_positive CHECK (
        representation_version > 0 AND repositories_collection_version > 0 AND members_collection_version > 0
    ),
    CONSTRAINT orgs_name_key_unique UNIQUE (name_key)
);

CREATE TABLE org_memberships (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role text NOT NULL,
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT org_memberships_role_valid CHECK (role IN ('owner', 'maintainer', 'member', 'reader')),
    CONSTRAINT org_memberships_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT org_memberships_org_user_unique UNIQUE (organization_id, user_id)
);

CREATE TABLE repos (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name text NOT NULL,
    name_key text GENERATED ALWAYS AS (lower(name)) STORED,
    display_name text NOT NULL,
    visibility text NOT NULL DEFAULT 'private',
    default_branch text NOT NULL DEFAULT 'main',
    next_issue_number bigint NOT NULL DEFAULT 1,
    representation_version bigint NOT NULL DEFAULT 1,
    issues_collection_version bigint NOT NULL DEFAULT 1,
    labels_collection_version bigint NOT NULL DEFAULT 1,
    artifacts_collection_version bigint NOT NULL DEFAULT 1,
    webhooks_collection_version bigint NOT NULL DEFAULT 1,
    comments_collection_version bigint NOT NULL DEFAULT 1,
    reactions_collection_version bigint NOT NULL DEFAULT 1,
    bindings_collection_version bigint NOT NULL DEFAULT 1,
    references_collection_version bigint NOT NULL DEFAULT 1,
    evidence_collection_version bigint NOT NULL DEFAULT 1,
    collaborators_collection_version bigint NOT NULL DEFAULT 1,
    subscriptions_collection_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT repos_name_nonempty CHECK (btrim(name) <> ''),
    CONSTRAINT repos_display_name_nonempty CHECK (btrim(display_name) <> ''),
    CONSTRAINT repos_visibility_valid CHECK (visibility IN ('public', 'internal', 'private')),
    CONSTRAINT repos_default_branch_nonempty CHECK (btrim(default_branch) <> ''),
    CONSTRAINT repos_next_issue_number_positive CHECK (next_issue_number > 0),
    CONSTRAINT repos_versions_positive CHECK (
        representation_version > 0 AND issues_collection_version > 0 AND
        labels_collection_version > 0 AND artifacts_collection_version > 0 AND
        webhooks_collection_version > 0 AND comments_collection_version > 0 AND
        reactions_collection_version > 0 AND bindings_collection_version > 0 AND
        references_collection_version > 0 AND evidence_collection_version > 0 AND
        collaborators_collection_version > 0 AND
        subscriptions_collection_version > 0
    ),
    CONSTRAINT repos_org_id_unique UNIQUE (organization_id, id),
    CONSTRAINT repos_org_name_key_unique UNIQUE (organization_id, name_key)
);

CREATE TABLE delegated_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    personal_access_token_id uuid REFERENCES personal_access_tokens(id) ON DELETE CASCADE,
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    job_id text NOT NULL,
    purpose text NOT NULL,
    token_hash bytea NOT NULL,
    audience text NOT NULL,
    subject text NOT NULL,
    claims jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT delegated_tokens_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT delegated_tokens_job_id_nonempty CHECK (btrim(job_id) <> ''),
    CONSTRAINT delegated_tokens_purpose_nonempty CHECK (btrim(purpose) <> ''),
    CONSTRAINT delegated_tokens_hash_nonempty CHECK (octet_length(token_hash) > 0),
    CONSTRAINT delegated_tokens_audience_nonempty CHECK (btrim(audience) <> ''),
    CONSTRAINT delegated_tokens_subject_nonempty CHECK (btrim(subject) <> ''),
    CONSTRAINT delegated_tokens_claims_object CHECK (jsonb_typeof(claims) = 'object'),
    CONSTRAINT delegated_tokens_expiry_valid CHECK (expires_at > created_at),
    CONSTRAINT delegated_tokens_used_valid CHECK (used_at IS NULL OR used_at >= created_at),
    CONSTRAINT delegated_tokens_revoked_valid CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    CONSTRAINT delegated_tokens_hash_unique UNIQUE (token_hash)
);

CREATE TABLE repo_collaborators (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role text NOT NULL,
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT repo_collaborators_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT repo_collaborators_role_valid CHECK (role IN ('admin', 'maintain', 'write', 'triage', 'read')),
    CONSTRAINT repo_collaborators_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT repo_collaborators_repo_user_unique UNIQUE (organization_id, repository_id, user_id)
);

CREATE TABLE repo_subscriptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reason text NOT NULL DEFAULT 'manual',
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT repo_subscriptions_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT repo_subscriptions_reason_nonempty CHECK (btrim(reason) <> ''),
    CONSTRAINT repo_subscriptions_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT repo_subscriptions_repo_user_unique UNIQUE (organization_id, repository_id, user_id)
);

CREATE TABLE site_role_assignments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role text NOT NULL,
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT site_role_assignments_role_valid CHECK (role IN ('site_admin', 'auditor')),
    CONSTRAINT site_role_assignments_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT site_role_assignments_user_role_unique UNIQUE (user_id, role)
);

CREATE TABLE bootstrap_state (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    singleton_key boolean NOT NULL DEFAULT true,
    completed boolean NOT NULL DEFAULT false,
    completed_by_user_id uuid REFERENCES users(id) ON DELETE RESTRICT,
    completed_at timestamptz,
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT bootstrap_state_singleton_true CHECK (singleton_key),
    CONSTRAINT bootstrap_state_completion_consistent CHECK (
        (completed AND completed_by_user_id IS NOT NULL AND completed_at IS NOT NULL) OR
        (NOT completed AND completed_by_user_id IS NULL AND completed_at IS NULL)
    ),
    CONSTRAINT bootstrap_state_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT bootstrap_state_singleton_unique UNIQUE (singleton_key)
);

CREATE TABLE audit_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid,
    repository_id uuid,
    actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    actor_identity_key text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id uuid,
    request_id text NOT NULL,
    remote_address inet,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT audit_events_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) MATCH FULL ON DELETE RESTRICT,
    CONSTRAINT audit_events_actor_identity_key_nonempty CHECK (btrim(actor_identity_key) <> ''),
    CONSTRAINT audit_events_action_nonempty CHECK (btrim(action) <> ''),
    CONSTRAINT audit_events_resource_type_nonempty CHECK (btrim(resource_type) <> ''),
    CONSTRAINT audit_events_request_id_nonempty CHECK (btrim(request_id) <> ''),
    CONSTRAINT audit_events_metadata_object CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE issues (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    number bigint NOT NULL,
    author_id uuid REFERENCES users(id) ON DELETE SET NULL,
    title text NOT NULL,
    body text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'open',
    representation_version bigint NOT NULL DEFAULT 1,
    comments_collection_version bigint NOT NULL DEFAULT 1,
    labels_collection_version bigint NOT NULL DEFAULT 1,
    bindings_collection_version bigint NOT NULL DEFAULT 1,
    references_collection_version bigint NOT NULL DEFAULT 1,
    evidence_collection_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    closed_at timestamptz,
    CONSTRAINT issues_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT issues_title_nonempty CHECK (btrim(title) <> ''),
    CONSTRAINT issues_number_positive CHECK (number > 0),
    CONSTRAINT issues_state_valid CHECK (state IN ('open', 'closed')),
    CONSTRAINT issues_closed_state_consistent CHECK (
        (state = 'open' AND closed_at IS NULL) OR (state = 'closed' AND closed_at IS NOT NULL)
    ),
    CONSTRAINT issues_versions_positive CHECK (
        representation_version > 0 AND comments_collection_version > 0 AND
        labels_collection_version > 0 AND bindings_collection_version > 0 AND
        references_collection_version > 0 AND evidence_collection_version > 0
    ),
    CONSTRAINT issues_org_repo_id_unique UNIQUE (organization_id, repository_id, id),
    CONSTRAINT issues_repo_number_unique UNIQUE (organization_id, repository_id, number)
);

CREATE TABLE comments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    author_id uuid REFERENCES users(id) ON DELETE SET NULL,
    body text NOT NULL,
    representation_version bigint NOT NULL DEFAULT 1,
    reactions_collection_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT comments_issue_fk FOREIGN KEY (organization_id, repository_id, issue_id)
        REFERENCES issues(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT comments_body_nonempty CHECK (btrim(body) <> ''),
    CONSTRAINT comments_versions_positive CHECK (
        representation_version > 0 AND reactions_collection_version > 0
    ),
    CONSTRAINT comments_org_repo_id_unique UNIQUE (organization_id, repository_id, id),
    CONSTRAINT comments_issue_id_unique UNIQUE (organization_id, repository_id, issue_id, id)
);

CREATE TABLE labels (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    name text NOT NULL,
    name_key text GENERATED ALWAYS AS (lower(name)) STORED,
    color text NOT NULL,
    description text NOT NULL DEFAULT '',
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT labels_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT labels_name_nonempty CHECK (btrim(name) <> ''),
    CONSTRAINT labels_color_valid CHECK (color ~ '^[0-9A-Fa-f]{6}$'),
    CONSTRAINT labels_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT labels_org_repo_id_unique UNIQUE (organization_id, repository_id, id),
    CONSTRAINT labels_repo_name_key_unique UNIQUE (organization_id, repository_id, name_key)
);

CREATE TABLE issue_labels (
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    label_id uuid NOT NULL,
    assigned_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT issue_labels_issue_fk FOREIGN KEY (organization_id, repository_id, issue_id)
        REFERENCES issues(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT issue_labels_label_fk FOREIGN KEY (organization_id, repository_id, label_id)
        REFERENCES labels(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT issue_labels_pk PRIMARY KEY (issue_id, label_id)
);

CREATE TABLE comment_reactions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    comment_id uuid NOT NULL,
    user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    identity_key text NOT NULL,
    reaction_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT comment_reactions_issue_fk FOREIGN KEY (organization_id, repository_id, issue_id)
        REFERENCES issues(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT comment_reactions_comment_fk
        FOREIGN KEY (organization_id, repository_id, issue_id, comment_id)
        REFERENCES comments(organization_id, repository_id, issue_id, id) ON DELETE CASCADE,
    CONSTRAINT comment_reactions_identity_key_nonempty CHECK (btrim(identity_key) <> ''),
    CONSTRAINT comment_reactions_reaction_key_nonempty CHECK (btrim(reaction_key) <> ''),
    CONSTRAINT comment_reactions_unique UNIQUE (
        organization_id, repository_id, comment_id, identity_key, reaction_key
    )
);

CREATE TABLE source_bindings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    provider_key text NOT NULL,
    external_repository_id text NOT NULL,
    clone_url text NOT NULL,
    web_url text NOT NULL,
    default_branch text NOT NULL,
    version bigint NOT NULL,
    active boolean NOT NULL DEFAULT true,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT source_bindings_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT source_bindings_provider_key_nonempty CHECK (btrim(provider_key) <> ''),
    CONSTRAINT source_bindings_external_repository_id_nonempty CHECK (btrim(external_repository_id) <> ''),
    CONSTRAINT source_bindings_clone_url_nonempty CHECK (btrim(clone_url) <> ''),
    CONSTRAINT source_bindings_web_url_nonempty CHECK (btrim(web_url) <> ''),
    CONSTRAINT source_bindings_default_branch_nonempty CHECK (btrim(default_branch) <> ''),
    CONSTRAINT source_bindings_clone_url_no_query_or_fragment CHECK (
        strpos(clone_url, '?') = 0 AND strpos(clone_url, '#') = 0
    ),
    CONSTRAINT source_bindings_clone_url_no_userinfo CHECK (clone_url !~ '^[a-zA-Z][a-zA-Z0-9+.-]*://[^/]*@'),
    CONSTRAINT source_bindings_version_positive CHECK (version > 0),
    CONSTRAINT source_bindings_repo_version_unique UNIQUE (organization_id, repository_id, version),
    CONSTRAINT source_bindings_org_repo_id_unique UNIQUE (organization_id, repository_id, id)
);

CREATE UNIQUE INDEX source_bindings_one_active
    ON source_bindings (organization_id, repository_id)
    WHERE active;

CREATE TABLE external_references (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    provider_key text NOT NULL,
    relation_kind text NOT NULL,
    external_repository_id text NOT NULL,
    external_id text NOT NULL,
    canonical_url text NOT NULL,
    title text,
    lifecycle_state text NOT NULL DEFAULT 'active',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT external_references_issue_fk FOREIGN KEY (organization_id, repository_id, issue_id)
        REFERENCES issues(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT external_references_provider_nonempty CHECK (btrim(provider_key) <> ''),
    CONSTRAINT external_references_kind_nonempty CHECK (btrim(relation_kind) <> ''),
    CONSTRAINT external_references_external_repository_nonempty CHECK (btrim(external_repository_id) <> ''),
    CONSTRAINT external_references_external_id_nonempty CHECK (btrim(external_id) <> ''),
    CONSTRAINT external_references_canonical_url_nonempty CHECK (btrim(canonical_url) <> ''),
    CONSTRAINT external_references_lifecycle_nonempty CHECK (btrim(lifecycle_state) <> ''),
    CONSTRAINT external_references_metadata_object CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT external_references_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT external_references_external_unique UNIQUE (
        organization_id, repository_id, issue_id, provider_key, relation_kind, external_id
    )
);

CREATE TABLE external_evidence (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    provider_key text NOT NULL,
    evidence_type text NOT NULL,
    external_id text NOT NULL DEFAULT '',
    ingest_key text NOT NULL,
    normalized_state text NOT NULL,
    subject_revision text NOT NULL,
    base_revision text,
    merge_revision text,
    observed_at timestamptz NOT NULL,
    valid_until timestamptz,
    payload_hash bytea NOT NULL,
    payload jsonb NOT NULL,
    provenance jsonb NOT NULL,
    writer_user_id uuid REFERENCES users(id) ON DELETE RESTRICT,
    writer_identity_key text NOT NULL,
    supersedes_evidence_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT external_evidence_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE RESTRICT,
    CONSTRAINT external_evidence_issue_fk FOREIGN KEY (organization_id, repository_id, issue_id)
        REFERENCES issues(organization_id, repository_id, id) ON DELETE RESTRICT,
    CONSTRAINT external_evidence_provider_nonempty CHECK (btrim(provider_key) <> ''),
    CONSTRAINT external_evidence_type_nonempty CHECK (btrim(evidence_type) <> ''),
    CONSTRAINT external_evidence_ingest_key_nonempty CHECK (btrim(ingest_key) <> ''),
    CONSTRAINT external_evidence_state_nonempty CHECK (btrim(normalized_state) <> ''),
    CONSTRAINT external_evidence_subject_revision_nonempty CHECK (btrim(subject_revision) <> ''),
    CONSTRAINT external_evidence_writer_identity_nonempty CHECK (btrim(writer_identity_key) <> ''),
    CONSTRAINT external_evidence_validity_valid CHECK (valid_until IS NULL OR valid_until >= observed_at),
    CONSTRAINT external_evidence_payload_hash_nonempty CHECK (octet_length(payload_hash) > 0),
    CONSTRAINT external_evidence_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT external_evidence_provenance_object CHECK (jsonb_typeof(provenance) = 'object'),
    CONSTRAINT external_evidence_org_repo_id_unique UNIQUE (organization_id, repository_id, id),
    CONSTRAINT external_evidence_supersedes_fk
        FOREIGN KEY (organization_id, repository_id, supersedes_evidence_id)
        REFERENCES external_evidence(organization_id, repository_id, id) ON DELETE RESTRICT,
    CONSTRAINT external_evidence_not_self_superseding CHECK (supersedes_evidence_id IS DISTINCT FROM id),
    CONSTRAINT external_evidence_repo_ingest_unique UNIQUE (
        organization_id, repository_id, provider_key, ingest_key
    )
);

CREATE FUNCTION reject_external_evidence_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
    RAISE EXCEPTION 'external_evidence is append-only'
        USING ERRCODE = '55000';
END;
$function$;

CREATE TRIGGER external_evidence_append_only
BEFORE UPDATE OR DELETE ON external_evidence
FOR EACH ROW EXECUTE FUNCTION reject_external_evidence_mutation();

CREATE TABLE webhook_subscriptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    repository_id uuid,
    scope_type text NOT NULL,
    url text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    event_types text[] NOT NULL,
    representation_version bigint NOT NULL DEFAULT 1,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT webhook_subscriptions_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT webhook_subscriptions_scope_valid CHECK (
        (scope_type = 'organization' AND repository_id IS NULL) OR
        (scope_type = 'repository' AND repository_id IS NOT NULL)
    ),
    CONSTRAINT webhook_subscriptions_url_nonempty CHECK (btrim(url) <> ''),
    CONSTRAINT webhook_subscriptions_event_types_nonempty CHECK (cardinality(event_types) > 0),
    CONSTRAINT webhook_subscriptions_event_types_values_nonempty CHECK (
        array_position(event_types, '') IS NULL
    ),
    CONSTRAINT webhook_subscriptions_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT webhook_subscriptions_org_id_unique UNIQUE (organization_id, id)
);

CREATE TABLE webhook_secret_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid,
    subscription_id uuid NOT NULL,
    version bigint NOT NULL,
    secret_ciphertext bytea NOT NULL,
    active boolean NOT NULL DEFAULT true,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    retired_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT webhook_secret_versions_subscription_fk
        FOREIGN KEY (organization_id, subscription_id)
        REFERENCES webhook_subscriptions(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT webhook_secret_versions_version_positive CHECK (version > 0),
    CONSTRAINT webhook_secret_versions_secret_nonempty CHECK (octet_length(secret_ciphertext) > 0),
    CONSTRAINT webhook_secret_versions_retired_valid CHECK (retired_at IS NULL OR retired_at >= created_at),
    CONSTRAINT webhook_secret_versions_retired_consistent CHECK (
        (active AND retired_at IS NULL) OR (NOT active)
    ),
    CONSTRAINT webhook_secret_versions_subscription_version_unique UNIQUE (
        organization_id, subscription_id, version
    ),
    CONSTRAINT webhook_secret_versions_subscription_id_unique UNIQUE (
        organization_id, subscription_id, id
    )
);

CREATE UNIQUE INDEX webhook_secret_versions_one_active
    ON webhook_secret_versions (organization_id, subscription_id)
    WHERE active;

CREATE TABLE event_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    event_type text NOT NULL,
    event_key text NOT NULL,
    payload_hash bytea NOT NULL,
    payload jsonb NOT NULL,
    available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT event_outbox_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE RESTRICT,
    CONSTRAINT event_outbox_aggregate_type_nonempty CHECK (btrim(aggregate_type) <> ''),
    CONSTRAINT event_outbox_event_type_nonempty CHECK (btrim(event_type) <> ''),
    CONSTRAINT event_outbox_event_key_nonempty CHECK (btrim(event_key) <> ''),
    CONSTRAINT event_outbox_payload_hash_nonempty CHECK (octet_length(payload_hash) > 0),
    CONSTRAINT event_outbox_published_valid CHECK (published_at IS NULL OR published_at >= created_at),
    CONSTRAINT event_outbox_semantic_event_unique UNIQUE (organization_id, repository_id, event_key),
    CONSTRAINT event_outbox_org_repo_id_unique UNIQUE (organization_id, repository_id, id)
);

CREATE TABLE webhook_deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    event_id uuid NOT NULL,
    subscription_id uuid NOT NULL,
    secret_version_id uuid NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    delivered_at timestamptz,
    last_error text,
    representation_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT webhook_deliveries_event_fk FOREIGN KEY (organization_id, repository_id, event_id)
        REFERENCES event_outbox(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT webhook_deliveries_subscription_fk
        FOREIGN KEY (organization_id, subscription_id)
        REFERENCES webhook_subscriptions(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT webhook_deliveries_secret_version_fk
        FOREIGN KEY (organization_id, subscription_id, secret_version_id)
        REFERENCES webhook_secret_versions(organization_id, subscription_id, id)
        ON DELETE RESTRICT,
    CONSTRAINT webhook_deliveries_state_valid CHECK (state IN ('pending', 'delivering', 'succeeded', 'failed', 'dead')),
    CONSTRAINT webhook_deliveries_delivered_consistent CHECK (
        (state = 'succeeded' AND delivered_at IS NOT NULL) OR
        (state <> 'succeeded' AND delivered_at IS NULL)
    ),
    CONSTRAINT webhook_deliveries_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT webhook_deliveries_event_subscription_unique UNIQUE (
        organization_id, repository_id, event_id, subscription_id
    ),
    CONSTRAINT webhook_deliveries_org_repo_id_unique UNIQUE (organization_id, repository_id, id)
);

CREATE TABLE webhook_delivery_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    delivery_id uuid NOT NULL,
    attempt_number bigint NOT NULL,
    request_headers jsonb NOT NULL DEFAULT '{}'::jsonb,
    response_status integer,
    response_headers jsonb,
    response_body text,
    error text,
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT webhook_delivery_attempts_delivery_fk
        FOREIGN KEY (organization_id, repository_id, delivery_id)
        REFERENCES webhook_deliveries(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT webhook_delivery_attempts_attempt_positive CHECK (attempt_number > 0),
    CONSTRAINT webhook_delivery_attempts_status_valid CHECK (
        response_status IS NULL OR response_status BETWEEN 100 AND 599
    ),
    CONSTRAINT webhook_delivery_attempts_request_headers_object CHECK (jsonb_typeof(request_headers) = 'object'),
    CONSTRAINT webhook_delivery_attempts_response_headers_object CHECK (
        response_headers IS NULL OR jsonb_typeof(response_headers) = 'object'
    ),
    CONSTRAINT webhook_delivery_attempts_completed_valid CHECK (
        completed_at IS NULL OR completed_at >= started_at
    ),
    CONSTRAINT webhook_delivery_attempts_delivery_number_unique UNIQUE (
        organization_id, repository_id, delivery_id, attempt_number
    )
);

CREATE TABLE issue_spec_artifacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    issue_id uuid,
    change_key text NOT NULL,
    artifact_type text NOT NULL,
    content text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    active boolean NOT NULL DEFAULT true,
    representation_version bigint NOT NULL DEFAULT 1,
    typed_comments_collection_version bigint NOT NULL DEFAULT 1,
    supersedes_artifact_id uuid,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT issue_spec_artifacts_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT issue_spec_artifacts_issue_fk FOREIGN KEY (organization_id, repository_id, issue_id)
        REFERENCES issues(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT issue_spec_artifacts_change_key_nonempty CHECK (btrim(change_key) <> ''),
    CONSTRAINT issue_spec_artifacts_type_nonempty CHECK (btrim(artifact_type) <> ''),
    CONSTRAINT issue_spec_artifacts_content_nonempty CHECK (btrim(content) <> ''),
    CONSTRAINT issue_spec_artifacts_metadata_object CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT issue_spec_artifacts_versions_positive CHECK (
        representation_version > 0 AND typed_comments_collection_version > 0
    ),
    CONSTRAINT issue_spec_artifacts_org_repo_id_unique UNIQUE (organization_id, repository_id, id),
    CONSTRAINT issue_spec_artifacts_supersedes_fk
        FOREIGN KEY (organization_id, repository_id, supersedes_artifact_id)
        REFERENCES issue_spec_artifacts(organization_id, repository_id, id) ON DELETE RESTRICT,
    CONSTRAINT issue_spec_artifacts_not_self_superseding CHECK (supersedes_artifact_id IS DISTINCT FROM id)
);

CREATE UNIQUE INDEX issue_spec_artifacts_one_active
    ON issue_spec_artifacts (organization_id, repository_id, change_key, artifact_type)
    WHERE active;

CREATE TABLE issue_spec_typed_comments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    comment_id uuid,
    artifact_id uuid,
    comment_type text NOT NULL,
    comment_key text NOT NULL,
    body text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    representation_version bigint NOT NULL DEFAULT 1,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT issue_spec_typed_comments_issue_fk FOREIGN KEY (organization_id, repository_id, issue_id)
        REFERENCES issues(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT issue_spec_typed_comments_comment_fk
        FOREIGN KEY (organization_id, repository_id, issue_id, comment_id)
        REFERENCES comments(organization_id, repository_id, issue_id, id) ON DELETE CASCADE,
    CONSTRAINT issue_spec_typed_comments_artifact_fk FOREIGN KEY (organization_id, repository_id, artifact_id)
        REFERENCES issue_spec_artifacts(organization_id, repository_id, id) ON DELETE CASCADE,
    CONSTRAINT issue_spec_typed_comments_type_nonempty CHECK (btrim(comment_type) <> ''),
    CONSTRAINT issue_spec_typed_comments_key_nonempty CHECK (btrim(comment_key) <> ''),
    CONSTRAINT issue_spec_typed_comments_body_nonempty CHECK (btrim(body) <> ''),
    CONSTRAINT issue_spec_typed_comments_metadata_object CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT issue_spec_typed_comments_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT issue_spec_typed_comments_source_required CHECK (comment_id IS NOT NULL OR artifact_id IS NOT NULL),
    CONSTRAINT issue_spec_typed_comments_repo_key_unique UNIQUE (
        organization_id, repository_id, comment_key
    )
);

CREATE TABLE projection_anomalies (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    projection_name text NOT NULL,
    source_type text NOT NULL,
    source_id uuid NOT NULL,
    anomaly_key text NOT NULL,
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    resolved_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT projection_anomalies_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT projection_anomalies_projection_name_nonempty CHECK (btrim(projection_name) <> ''),
    CONSTRAINT projection_anomalies_source_type_nonempty CHECK (btrim(source_type) <> ''),
    CONSTRAINT projection_anomalies_key_nonempty CHECK (btrim(anomaly_key) <> ''),
    CONSTRAINT projection_anomalies_details_object CHECK (jsonb_typeof(details) = 'object'),
    CONSTRAINT projection_anomalies_resolved_valid CHECK (resolved_at IS NULL OR resolved_at >= observed_at),
    CONSTRAINT projection_anomalies_repo_key_unique UNIQUE (
        organization_id, repository_id, projection_name, anomaly_key
    )
);

CREATE INDEX identities_user_id_idx ON identities (user_id);
CREATE INDEX identities_claims_idx ON identities USING gin (claims);
CREATE INDEX oauth_login_transactions_provider_expiry_idx
    ON oauth_login_transactions (provider_id, expires_at) WHERE consumed_at IS NULL;
CREATE INDEX sessions_user_active_idx
    ON sessions (user_id, idle_expires_at, absolute_expires_at) WHERE revoked_at IS NULL;
CREATE INDEX personal_access_tokens_user_active_idx
    ON personal_access_tokens (user_id, expires_at) WHERE revoked_at IS NULL;
CREATE INDEX pat_scopes_scope_token_idx ON pat_scopes (scope, personal_access_token_id);
CREATE INDEX org_memberships_user_org_idx ON org_memberships (user_id, organization_id);
CREATE INDEX repos_org_updated_idx ON repos (organization_id, updated_at DESC, id);
CREATE INDEX delegated_tokens_repo_expiry_idx
    ON delegated_tokens (organization_id, repository_id, expires_at) WHERE revoked_at IS NULL;
CREATE INDEX delegated_tokens_user_idx ON delegated_tokens (user_id, created_at DESC);
CREATE INDEX delegated_tokens_pat_idx ON delegated_tokens (personal_access_token_id)
    WHERE personal_access_token_id IS NOT NULL;
CREATE INDEX delegated_tokens_claims_idx ON delegated_tokens USING gin (claims);
CREATE INDEX repo_collaborators_user_repo_idx
    ON repo_collaborators (user_id, organization_id, repository_id);
CREATE INDEX repo_subscriptions_user_repo_idx
    ON repo_subscriptions (user_id, organization_id, repository_id);
CREATE INDEX site_role_assignments_role_user_idx ON site_role_assignments (role, user_id);
CREATE INDEX audit_events_repo_created_idx
    ON audit_events (organization_id, repository_id, created_at DESC, id);
CREATE INDEX audit_events_actor_created_idx ON audit_events (actor_user_id, created_at DESC);
CREATE INDEX audit_events_request_id_idx ON audit_events (request_id);
CREATE INDEX audit_events_resource_idx ON audit_events (resource_type, resource_id, created_at DESC);
CREATE INDEX issues_repo_state_updated_idx
    ON issues (organization_id, repository_id, state, updated_at DESC, id);
CREATE INDEX issues_author_idx ON issues (author_id, created_at DESC);
CREATE INDEX comments_issue_created_idx
    ON comments (organization_id, repository_id, issue_id, created_at, id);
CREATE INDEX comments_author_idx ON comments (author_id, created_at DESC);
CREATE INDEX issue_labels_label_issue_idx
    ON issue_labels (organization_id, repository_id, label_id, issue_id);
CREATE INDEX comment_reactions_comment_idx
    ON comment_reactions (organization_id, repository_id, comment_id, created_at, id);
CREATE INDEX comment_reactions_user_idx ON comment_reactions (user_id, created_at DESC);
CREATE INDEX source_bindings_external_idx
    ON source_bindings (provider_key, external_repository_id, updated_at DESC, id);
CREATE INDEX external_references_issue_idx
    ON external_references (organization_id, repository_id, issue_id, created_at, id);
CREATE INDEX external_references_external_idx
    ON external_references (provider_key, external_repository_id, external_id);
CREATE INDEX external_evidence_issue_created_idx
    ON external_evidence (organization_id, repository_id, issue_id, created_at, id);
CREATE INDEX external_evidence_external_idx
    ON external_evidence (provider_key, external_id) WHERE external_id <> '';
CREATE INDEX webhook_subscriptions_repo_active_idx
    ON webhook_subscriptions (organization_id, repository_id, created_at, id) WHERE active;
CREATE INDEX webhook_subscriptions_event_types_idx ON webhook_subscriptions USING gin (event_types);
CREATE INDEX webhook_secret_versions_subscription_idx
    ON webhook_secret_versions (organization_id, subscription_id, version DESC);
CREATE INDEX event_outbox_pending_idx
    ON event_outbox (available_at, created_at, id) WHERE published_at IS NULL;
CREATE INDEX event_outbox_repo_aggregate_idx
    ON event_outbox (organization_id, repository_id, aggregate_type, aggregate_id, created_at, id);
CREATE INDEX webhook_deliveries_ready_idx
    ON webhook_deliveries (next_attempt_at, created_at, id) WHERE state IN ('pending', 'failed');
CREATE INDEX webhook_deliveries_subscription_idx
    ON webhook_deliveries (organization_id, repository_id, subscription_id, created_at DESC);
CREATE INDEX webhook_delivery_attempts_delivery_idx
    ON webhook_delivery_attempts (organization_id, repository_id, delivery_id, attempt_number DESC);
CREATE INDEX issue_spec_artifacts_issue_idx
    ON issue_spec_artifacts (organization_id, repository_id, issue_id, created_at DESC, id);
CREATE INDEX issue_spec_artifacts_change_idx
    ON issue_spec_artifacts (organization_id, repository_id, change_key, created_at DESC, id);
CREATE INDEX issue_spec_typed_comments_issue_type_idx
    ON issue_spec_typed_comments (organization_id, repository_id, issue_id, comment_type, created_at, id);
CREATE INDEX issue_spec_typed_comments_comment_idx
    ON issue_spec_typed_comments (organization_id, repository_id, comment_id)
    WHERE comment_id IS NOT NULL;
CREATE INDEX issue_spec_typed_comments_artifact_idx
    ON issue_spec_typed_comments (organization_id, repository_id, artifact_id)
    WHERE artifact_id IS NOT NULL;
CREATE INDEX projection_anomalies_unresolved_idx
    ON projection_anomalies (organization_id, repository_id, observed_at, id)
    WHERE resolved_at IS NULL;
