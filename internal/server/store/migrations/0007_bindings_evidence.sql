ALTER TABLE external_references
    DROP CONSTRAINT external_references_external_unique,
    ADD COLUMN visibility text NOT NULL DEFAULT 'repository',
    ADD CONSTRAINT external_references_visibility_valid CHECK (
        visibility IN ('repository', 'maintainers')
    ),
    ADD CONSTRAINT external_references_external_unique UNIQUE (
        organization_id,
        repository_id,
        issue_id,
        provider_key,
        relation_kind,
        external_repository_id,
        external_id
    );

ALTER TABLE external_evidence
    ADD COLUMN external_repository_id text,
    ADD COLUMN visibility text NOT NULL DEFAULT 'repository',
    ADD CONSTRAINT external_evidence_visibility_valid CHECK (
        visibility IN ('repository', 'maintainers')
    );

UPDATE external_evidence AS evidence
SET external_repository_id = COALESCE(
    (
        SELECT binding.external_repository_id
        FROM source_bindings AS binding
        WHERE binding.organization_id = evidence.organization_id
          AND binding.repository_id = evidence.repository_id
          AND binding.provider_key = evidence.provider_key
          AND binding.active
        ORDER BY binding.version DESC
        LIMIT 1
    ),
    'legacy:' || evidence.repository_id::text
);

ALTER TABLE external_evidence
    ALTER COLUMN external_repository_id SET NOT NULL,
    ADD CONSTRAINT external_evidence_external_repository_nonempty CHECK (
        btrim(external_repository_id) <> ''
    );

CREATE TABLE repository_evidence_policies (
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    representation_version bigint NOT NULL DEFAULT 1,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT repository_evidence_policies_pk PRIMARY KEY (
        organization_id,
        repository_id
    ),
    CONSTRAINT repository_evidence_policies_repository_fk FOREIGN KEY (
        organization_id,
        repository_id
    ) REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT repository_evidence_policies_version_positive CHECK (
        representation_version > 0
    )
);

CREATE TABLE repository_evidence_requirements (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    evidence_type text NOT NULL,
    freshness interval,
    representation_version bigint NOT NULL DEFAULT 1,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT repository_evidence_requirements_policy_fk FOREIGN KEY (
        organization_id,
        repository_id
    ) REFERENCES repository_evidence_policies(organization_id, repository_id) ON DELETE CASCADE,
    CONSTRAINT repository_evidence_requirements_type_nonempty CHECK (
        btrim(evidence_type) <> ''
    ),
    CONSTRAINT repository_evidence_requirements_freshness_positive CHECK (
        freshness IS NULL OR freshness > interval '0 seconds'
    ),
    CONSTRAINT repository_evidence_requirements_version_positive CHECK (
        representation_version > 0
    ),
    CONSTRAINT repository_evidence_requirements_type_unique UNIQUE (
        organization_id,
        repository_id,
        evidence_type
    )
);

CREATE TABLE repository_evidence_writers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    active boolean NOT NULL DEFAULT true,
    representation_version bigint NOT NULL DEFAULT 1,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT repository_evidence_writers_repository_fk FOREIGN KEY (
        organization_id,
        repository_id
    ) REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT repository_evidence_writers_version_positive CHECK (
        representation_version > 0
    ),
    CONSTRAINT repository_evidence_writers_user_unique UNIQUE (
        organization_id,
        repository_id,
        user_id
    )
);

CREATE INDEX source_bindings_repo_active_version_idx
    ON source_bindings (organization_id, repository_id, active, version DESC);

CREATE INDEX external_references_repo_visibility_updated_idx
    ON external_references (
        organization_id,
        repository_id,
        visibility,
        updated_at DESC,
        id
    );

CREATE INDEX external_evidence_repo_revision_idx
    ON external_evidence (
        organization_id,
        repository_id,
        provider_key,
        external_repository_id,
        subject_revision,
        evidence_type,
        observed_at DESC,
        id
    );

CREATE INDEX repository_evidence_policy_version_idx
    ON repository_evidence_policies (
        organization_id,
        repository_id,
        representation_version
    );

CREATE INDEX repository_evidence_requirement_type_idx
    ON repository_evidence_requirements (
        organization_id,
        repository_id,
        evidence_type,
        representation_version
    );

CREATE INDEX repository_evidence_writer_active_idx
    ON repository_evidence_writers (
        organization_id,
        repository_id,
        user_id,
        representation_version
    ) WHERE active;
