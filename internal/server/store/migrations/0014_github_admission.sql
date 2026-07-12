CREATE TABLE github_admission_organizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id uuid NOT NULL REFERENCES auth_providers(id) ON DELETE CASCADE,
    external_org_id bigint NOT NULL,
    configured_login text NOT NULL,
    login_key text GENERATED ALWAYS AS (lower(configured_login)) STORED,
    last_observed_login text NOT NULL,
    first_verified_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_verified_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    representation_version bigint NOT NULL DEFAULT 1,
    CONSTRAINT github_admission_external_org_id_positive CHECK (external_org_id > 0),
    CONSTRAINT github_admission_configured_login_nonempty CHECK (btrim(configured_login) <> ''),
    CONSTRAINT github_admission_observed_login_nonempty CHECK (btrim(last_observed_login) <> ''),
    CONSTRAINT github_admission_representation_version_positive CHECK (representation_version > 0),
    CONSTRAINT github_admission_provider_external_id_unique UNIQUE (provider_id, external_org_id),
    CONSTRAINT github_admission_provider_login_unique UNIQUE (provider_id, login_key)
);

CREATE INDEX github_admission_organizations_verified_idx
    ON github_admission_organizations (provider_id, last_verified_at DESC, id);
