ALTER TABLE comment_reactions
    ADD COLUMN compatibility_id bigint GENERATED ALWAYS AS (
        issue_spec_stable_numeric_id(id::text)
    ) STORED,
    ADD CONSTRAINT comment_reactions_compatibility_id_positive CHECK (compatibility_id > 0),
    ADD CONSTRAINT comment_reactions_compatibility_id_unique UNIQUE (compatibility_id),
    ADD CONSTRAINT comment_reactions_key_valid CHECK (
        reaction_key IN ('+1', '-1', 'laugh', 'confused', 'heart', 'hooray', 'rocket', 'eyes')
    );

CREATE INDEX comment_reactions_repo_comment_list_idx
    ON comment_reactions (
        organization_id,
        repository_id,
        comment_id,
        created_at,
        id
    );

CREATE INDEX issue_labels_repo_issue_created_idx
    ON issue_labels (organization_id, repository_id, issue_id, created_at, label_id);
