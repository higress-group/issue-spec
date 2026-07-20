WITH activated AS (
    UPDATE org_memberships
    SET state = 'active',
        activated_at = COALESCE(activated_at, invited_at, clock_timestamp()),
        updated_at = clock_timestamp(),
        representation_version = representation_version + 1
    WHERE state = 'invited' AND archived_at IS NULL
    RETURNING organization_id
)
UPDATE orgs
SET members_collection_version = members_collection_version + 1,
    updated_at = clock_timestamp()
WHERE id IN (SELECT DISTINCT organization_id FROM activated);
