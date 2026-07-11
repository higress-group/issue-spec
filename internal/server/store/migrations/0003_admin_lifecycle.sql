ALTER TABLE audit_events
    DROP CONSTRAINT audit_events_repository_fk,
    ADD CONSTRAINT audit_events_organization_fk FOREIGN KEY (organization_id)
        REFERENCES orgs(id) ON DELETE RESTRICT,
    ADD CONSTRAINT audit_events_repository_fk FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) MATCH SIMPLE ON DELETE RESTRICT,
    ADD CONSTRAINT audit_events_repository_requires_org CHECK (
        repository_id IS NULL OR organization_id IS NOT NULL
    );

ALTER TABLE orgs
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN base_permission text NOT NULL DEFAULT 'read',
    ADD COLUMN archived_at timestamptz,
    ADD CONSTRAINT orgs_base_permission_valid CHECK (
        base_permission IN ('none', 'read', 'triage', 'write', 'maintain', 'admin')
    ),
    ADD CONSTRAINT orgs_archived_valid CHECK (archived_at IS NULL OR archived_at >= created_at);

ALTER TABLE org_memberships
    ADD COLUMN state text NOT NULL DEFAULT 'active',
    ADD COLUMN invited_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN invited_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    ADD COLUMN activated_at timestamptz,
    ADD COLUMN archived_at timestamptz,
    ADD CONSTRAINT org_memberships_state_valid CHECK (state IN ('invited', 'active', 'suspended')),
    ADD CONSTRAINT org_memberships_inviter_required CHECK (
        state <> 'invited' OR invited_by_user_id IS NOT NULL
    ),
    ADD CONSTRAINT org_memberships_archived_valid CHECK (
        archived_at IS NULL OR archived_at >= created_at
    );

UPDATE org_memberships SET activated_at = created_at WHERE state = 'active' AND activated_at IS NULL;

ALTER TABLE org_memberships
    ADD CONSTRAINT org_memberships_activation_valid CHECK (
        state <> 'active' OR activated_at IS NOT NULL
    );

ALTER TABLE repos
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN contribution_policy text NOT NULL DEFAULT 'members',
    ADD COLUMN archived_at timestamptz,
    ADD CONSTRAINT repos_contribution_policy_valid CHECK (
        contribution_policy IN ('disabled', 'members', 'authenticated', 'public')
    ),
    ADD CONSTRAINT repos_archived_valid CHECK (archived_at IS NULL OR archived_at >= created_at);

ALTER TABLE repo_collaborators
    ADD COLUMN archived_at timestamptz,
    ADD CONSTRAINT repo_collaborators_archived_valid CHECK (
        archived_at IS NULL OR archived_at >= created_at
    );

CREATE TABLE managed_personal_access_tokens (
    personal_access_token_id uuid PRIMARY KEY REFERENCES personal_access_tokens(id) ON DELETE CASCADE,
    organization_id uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    created_by_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX orgs_active_updated_idx ON orgs (updated_at DESC, id) WHERE archived_at IS NULL;
CREATE INDEX org_memberships_active_org_idx
    ON org_memberships (organization_id, state, updated_at DESC, id) WHERE archived_at IS NULL;
CREATE INDEX repos_active_org_updated_idx
    ON repos (organization_id, updated_at DESC, id) WHERE archived_at IS NULL;
CREATE INDEX repo_collaborators_active_repo_idx
    ON repo_collaborators (organization_id, repository_id, updated_at DESC, id)
    WHERE archived_at IS NULL;
CREATE INDEX managed_personal_access_tokens_org_idx
    ON managed_personal_access_tokens (organization_id, created_at DESC, personal_access_token_id);
