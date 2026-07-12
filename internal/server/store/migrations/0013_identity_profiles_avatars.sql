ALTER TABLE identities
    ADD COLUMN avatar_url text,
    ADD CONSTRAINT identities_avatar_url_bounded CHECK (
        avatar_url IS NULL OR (btrim(avatar_url) <> '' AND length(avatar_url) <= 2048)
    ),
    ADD CONSTRAINT identities_user_id_id_unique UNIQUE (user_id, id);

ALTER TABLE users ADD COLUMN profile_identity_id uuid;

UPDATE users u
SET profile_identity_id = (
    SELECT i.id
    FROM identities i
    WHERE i.user_id = u.id
    ORDER BY i.created_at, i.id
    LIMIT 1
)
WHERE EXISTS (SELECT 1 FROM identities i WHERE i.user_id = u.id);

ALTER TABLE users
    ADD CONSTRAINT users_profile_identity_owner_fk
    FOREIGN KEY (id, profile_identity_id) REFERENCES identities (user_id, id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX identities_profile_avatar_lookup_idx
    ON identities (user_id, id) WHERE avatar_url IS NOT NULL;
