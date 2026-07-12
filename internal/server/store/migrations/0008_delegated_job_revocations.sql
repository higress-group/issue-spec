CREATE TABLE delegated_job_revocations (
    organization_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    job_id text NOT NULL,
    revoked_at timestamptz NOT NULL,
    CONSTRAINT delegated_job_revocations_repository_fk
        FOREIGN KEY (organization_id, repository_id)
        REFERENCES repos(organization_id, id) ON DELETE CASCADE,
    CONSTRAINT delegated_job_revocations_job_id_nonempty CHECK (btrim(job_id) <> ''),
    CONSTRAINT delegated_job_revocations_pk PRIMARY KEY (organization_id, repository_id, job_id)
);

CREATE INDEX delegated_job_revocations_revoked_idx
    ON delegated_job_revocations (revoked_at DESC);
