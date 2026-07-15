ALTER TABLE users
    ADD COLUMN nickname text,
    ADD CONSTRAINT users_nickname_valid CHECK (
        nickname IS NULL OR (
            nickname = btrim(nickname)
            AND nickname <> ''
            AND char_length(nickname) <= 80
        )
    );
