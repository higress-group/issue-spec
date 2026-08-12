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
), raw_candidates AS (
	SELECT i.id AS issue_id,
		(CASE WHEN $4::bigint > 0 AND i.number = $4 THEN 100000
			WHEN lower(i.title) = $3 THEN 90000
			WHEN lower(i.title) LIKE public.likequery($3) THEN 80000
			WHEN lower(i.body) LIKE public.likequery($3) THEN 70000 ELSE 60000 END
			+ LEAST((ts_rank_cd(to_tsvector('public.jiebacfg'::regconfig, i.title || E'\n' || i.body),
				plainto_tsquery('public.jiebacfg'::regconfig, $3)) * 1000)::int, 999)
			+ LEAST((public.bigm_similarity(lower(i.title || E'\n' || i.body), $3) * 100)::int, 99)) AS score,
		true AS issue_matched, false AS comment_matched
	FROM eligible_issues i
	WHERE ($4::bigint > 0 AND i.number = $4)
		OR lower(i.title || E'\n' || i.body) LIKE public.likequery($3)
		OR to_tsvector('public.jiebacfg'::regconfig, i.title || E'\n' || i.body)
			@@ plainto_tsquery('public.jiebacfg'::regconfig, $3)
	UNION ALL
	SELECT c.issue_id,
		(CASE WHEN lower(c.body) LIKE public.likequery($3) THEN 50000 ELSE 40000 END
			+ LEAST((ts_rank_cd(to_tsvector('public.jiebacfg'::regconfig, c.body),
				plainto_tsquery('public.jiebacfg'::regconfig, $3)) * 1000)::int, 999)
			+ LEAST((public.bigm_similarity(lower(c.body), $3) * 100)::int, 99)),
		false, true
	FROM comments c
	JOIN eligible_issues i ON i.id = c.issue_id
		AND i.organization_id = c.organization_id AND i.repository_id = c.repository_id
	WHERE c.organization_id = $1 AND c.repository_id = $2
		AND (lower(c.body) LIKE public.likequery($3)
			OR to_tsvector('public.jiebacfg'::regconfig, c.body)
				@@ plainto_tsquery('public.jiebacfg'::regconfig, $3))
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
	COALESCE((SELECT jsonb_agg(jsonb_build_object('id', matches.id, 'body', matches.body)
		ORDER BY matches.score DESC, matches.updated_at DESC)
		FROM (SELECT c.id, c.body, c.updated_at,
			(CASE WHEN lower(c.body) LIKE public.likequery($3) THEN 50000 ELSE 40000 END
				+ LEAST((ts_rank_cd(to_tsvector('public.jiebacfg'::regconfig, c.body),
					plainto_tsquery('public.jiebacfg'::regconfig, $3)) * 1000)::int, 999)
				+ LEAST((public.bigm_similarity(lower(c.body), $3) * 100)::int, 99)) AS score
			FROM comments c
			WHERE s.comment_matched AND c.organization_id = s.organization_id
				AND c.repository_id = s.repository_id AND c.issue_id = s.id
				AND (lower(c.body) LIKE public.likequery($3)
					OR to_tsvector('public.jiebacfg'::regconfig, c.body)
						@@ plainto_tsquery('public.jiebacfg'::regconfig, $3))
			ORDER BY score DESC, c.updated_at DESC, c.id LIMIT 3) matches), '[]'::jsonb),
	s.total_count
FROM selected s
ORDER BY s.score DESC, (s.state = 'open') DESC, s.updated_at DESC, s.number, s.id`
