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
