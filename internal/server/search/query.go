package search

const searchQuery = `WITH proposal_issues AS MATERIALIZED (
	SELECT i.organization_id, i.repository_id, i.id, i.number, i.title, i.body, i.state, i.updated_at
	FROM issues i
	WHERE i.organization_id = $1 AND i.repository_id = ANY($2::uuid[])
		AND ($4 = 'all' OR i.state = $4)
		AND EXISTS (
			SELECT 1 FROM issue_spec_artifacts proposal
			WHERE proposal.organization_id = i.organization_id
				AND proposal.repository_id = i.repository_id
				AND proposal.issue_id = i.id
				AND proposal.artifact_type = 'proposal'
				AND proposal.active)
), ranked AS (
	SELECT i.*,
		(CASE WHEN lower(i.title) = $3 THEN 90000
			WHEN lower(i.title) LIKE public.likequery($3) THEN 80000
			WHEN lower(i.body) LIKE public.likequery($3) THEN 70000 ELSE 60000 END
			+ LEAST((ts_rank_cd(to_tsvector('public.jiebacfg'::regconfig, i.title || E'\n' || i.body),
				plainto_tsquery('public.jiebacfg'::regconfig, $3)) * 1000)::int, 999)
			+ LEAST((public.bigm_similarity(lower(i.title || E'\n' || i.body), $3) * 100)::int, 99)) AS score
	FROM proposal_issues i
	WHERE lower(i.title || E'\n' || i.body) LIKE public.likequery($3)
		OR to_tsvector('public.jiebacfg'::regconfig, i.title || E'\n' || i.body)
			@@ plainto_tsquery('public.jiebacfg'::regconfig, $3)
), selected AS (
	SELECT ranked.organization_id, o.name AS organization, ranked.repository_id, r.name AS repository,
		ranked.id, ranked.number, ranked.title, ranked.body, ranked.state, ranked.updated_at, ranked.score,
		count(*) OVER() AS total_count
	FROM ranked
	JOIN repos r ON r.organization_id = ranked.organization_id AND r.id = ranked.repository_id
	JOIN orgs o ON o.id = ranked.organization_id
	ORDER BY ranked.score DESC, (ranked.state = 'open') DESC, ranked.updated_at DESC, lower(r.name), ranked.number, ranked.id
	LIMIT $5 OFFSET $6
)
SELECT s.organization_id, s.organization, s.repository_id, s.repository, s.id, s.number, s.title, s.body,
	s.state, s.updated_at,
	COALESCE((SELECT jsonb_agg(jsonb_build_object('key', proposals.change_key, 'stage', 'proposal') ORDER BY proposals.change_key)
		FROM (SELECT DISTINCT proposal.change_key
			FROM issue_spec_artifacts proposal
			WHERE proposal.organization_id = s.organization_id
				AND proposal.repository_id = s.repository_id
				AND proposal.issue_id = s.id
				AND proposal.artifact_type = 'proposal'
				AND proposal.active) proposals), '[]'::jsonb),
	s.score, s.total_count
FROM selected s
ORDER BY s.score DESC, (s.state = 'open') DESC, s.updated_at DESC, lower(s.repository), s.number, s.id`

// fullRepositorySearchQuery is intentionally separate from searchQuery. The
// former powers the repository Issues page and searches complete discussions;
// the latter remains the bounded Proposal-discovery query used by CLI and the
// dedicated Search page.
const fullRepositorySearchQuery = `WITH eligible_issues AS NOT MATERIALIZED (
	SELECT i.*
	FROM issues i
	WHERE i.organization_id = $1 AND i.repository_id = $2
		AND ($5 = 'all' OR i.state = $5)
		AND ($7::int = 0 OR (SELECT count(DISTINCT l.name_key)
			FROM issue_labels il
			JOIN labels l ON l.organization_id = il.organization_id
				AND l.repository_id = il.repository_id AND l.id = il.label_id
			WHERE il.organization_id = i.organization_id AND il.repository_id = i.repository_id
				AND il.issue_id = i.id AND l.name_key = ANY($6::text[])) = $7)
), scoped_issue_bigm_matches AS MATERIALIZED (
	SELECT i.organization_id, i.repository_id, i.id AS issue_id,
		CASE WHEN lower(i.title) = $3 THEN 90000
			WHEN lower(i.title) LIKE public.likequery($3) THEN 80000 ELSE 70000 END AS score
	FROM issues i
	WHERE $4::bigint = 0
		AND lower(encode(uuid_send(i.organization_id), 'hex') || '/' ||
			encode(uuid_send(i.repository_id), 'hex') || E'\n' || i.title || E'\n' || i.body)
			LIKE lower(encode(uuid_send($1), 'hex') || '/' || encode(uuid_send($2), 'hex') || E'\n') || public.likequery($3)
), scoped_issue_jieba_matches AS MATERIALIZED (
	SELECT i.organization_id, i.repository_id, i.id AS issue_id, 60000 AS score
	FROM issues i
	WHERE $4::bigint = 0 AND i.organization_id = $1 AND i.repository_id = $2
		AND (to_tsvector('simple'::regconfig, 'org' || encode(uuid_send(i.organization_id), 'hex') ||
			' repo' || encode(uuid_send(i.repository_id), 'hex')) ||
			to_tsvector('public.jiebacfg'::regconfig, i.title || E'\n' || i.body))
			@@ (to_tsquery('simple'::regconfig, 'org' || encode(uuid_send($1), 'hex') ||
				' & repo' || encode(uuid_send($2), 'hex')) && plainto_tsquery('public.jiebacfg'::regconfig, $3))
), issue_text_matches AS MATERIALIZED (
	SELECT matched.issue_id, max(matched.score)::int AS score
	FROM (SELECT * FROM scoped_issue_bigm_matches UNION ALL SELECT * FROM scoped_issue_jieba_matches) matched
	GROUP BY matched.issue_id
), scoped_comment_bigm_matches AS MATERIALIZED (
	SELECT c.organization_id, c.repository_id, c.id, c.issue_id, c.body, c.updated_at, 50000 AS score
	FROM comments c
	WHERE $4::bigint = 0
		AND lower(encode(uuid_send(c.organization_id), 'hex') || '/' ||
			encode(uuid_send(c.repository_id), 'hex') || E'\n' || c.body)
			LIKE lower(encode(uuid_send($1), 'hex') || '/' || encode(uuid_send($2), 'hex') || E'\n') || public.likequery($3)
), scoped_comment_jieba_matches AS MATERIALIZED (
	SELECT c.organization_id, c.repository_id, c.id, c.issue_id, c.body, c.updated_at, 40000 AS score
	FROM comments c
	WHERE $4::bigint = 0 AND c.organization_id = $1 AND c.repository_id = $2
		AND (to_tsvector('simple'::regconfig, 'org' || encode(uuid_send(c.organization_id), 'hex') ||
			' repo' || encode(uuid_send(c.repository_id), 'hex')) ||
			to_tsvector('public.jiebacfg'::regconfig, c.body))
			@@ (to_tsquery('simple'::regconfig, 'org' || encode(uuid_send($1), 'hex') ||
				' & repo' || encode(uuid_send($2), 'hex')) && plainto_tsquery('public.jiebacfg'::regconfig, $3))
), comment_text_matches AS MATERIALIZED (
	SELECT matched.id, matched.issue_id, matched.body, matched.updated_at, max(matched.score)::int AS score
	FROM (SELECT * FROM scoped_comment_bigm_matches UNION ALL SELECT * FROM scoped_comment_jieba_matches) matched
	GROUP BY matched.id, matched.issue_id, matched.body, matched.updated_at
), comment_candidates AS MATERIALIZED (
	SELECT issue_id, max(score)::int AS score,
		jsonb_agg(jsonb_build_object('id', id, 'body', body)
			ORDER BY score DESC, updated_at DESC, id) FILTER (WHERE match_rank <= 3) AS matches
	FROM (SELECT matched.*,
		row_number() OVER (PARTITION BY issue_id ORDER BY score DESC, updated_at DESC, id) AS match_rank
		FROM comment_text_matches matched) ranked_comments
	GROUP BY issue_id
), raw_candidates AS (
	SELECT i.id AS issue_id, 100000 AS score, true AS issue_matched, false AS comment_matched
	FROM eligible_issues i
	WHERE $4::bigint > 0 AND i.number = $4
	UNION ALL
	SELECT matched.issue_id, matched.score, true, false
	FROM issue_text_matches matched
	JOIN eligible_issues i ON i.id = matched.issue_id
	UNION ALL
	SELECT matched.issue_id, matched.score, false, true
	FROM comment_candidates matched
	JOIN eligible_issues i ON i.id = matched.issue_id
), ranked AS (
	SELECT issue_id, max(score)::int AS score, bool_or(issue_matched) AS issue_matched,
		bool_or(comment_matched) AS comment_matched
	FROM raw_candidates GROUP BY issue_id
), selected AS (
	SELECT i.organization_id, o.name AS organization, i.repository_id, r.name AS repository,
		i.id, i.number, i.title, i.body, i.state, i.updated_at, ranked.score,
		ranked.issue_matched, ranked.comment_matched, count(*) OVER() AS total_count
	FROM ranked
	JOIN eligible_issues i ON i.id = ranked.issue_id
	JOIN repos r ON r.organization_id = i.organization_id AND r.id = i.repository_id
	JOIN orgs o ON o.id = i.organization_id
	ORDER BY ranked.score DESC, (i.state = 'open') DESC, i.updated_at DESC, i.number, i.id
	LIMIT $8 OFFSET $9
)
SELECT s.organization_id, s.organization, s.repository_id, s.repository, s.id, s.number,
	s.title, s.body, s.state, s.updated_at, '[]'::jsonb, s.score, s.issue_matched,
	COALESCE(matches.matches, '[]'::jsonb),
	s.total_count
FROM selected s
LEFT JOIN comment_candidates matches ON s.comment_matched AND matches.issue_id = s.id
ORDER BY s.score DESC, (s.state = 'open') DESC, s.updated_at DESC, s.number, s.id`
