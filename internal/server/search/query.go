package search

const searchQuery = `WITH raw_candidates AS (
	SELECT i.id AS issue_id,
		(CASE WHEN $4::bigint > 0 AND i.number = $4 THEN 100000
			WHEN lower(i.title) = $3 THEN 90000
			WHEN lower(i.title) LIKE public.likequery($3) THEN 80000
			WHEN lower(i.body) LIKE public.likequery($3) THEN 70000 ELSE 60000 END
			+ LEAST((ts_rank_cd(to_tsvector('public.jiebacfg'::regconfig, i.title || E'\n' || i.body),
				plainto_tsquery('public.jiebacfg'::regconfig, $3)) * 1000)::int, 999)
			+ LEAST((public.bigm_similarity(lower(i.title || E'\n' || i.body), $3) * 100)::int, 99)) AS score,
		true AS issue_matched, false AS comment_matched, false AS change_matched
	FROM issues i
	WHERE i.organization_id = $1 AND i.repository_id = ANY($2::uuid[])
		AND ($5 = 'all' OR i.state = $5)
		AND $6 IN ('all', 'issue')
		AND (($4::bigint > 0 AND i.number = $4)
			OR lower(i.title || E'\n' || i.body) LIKE public.likequery($3)
			OR to_tsvector('public.jiebacfg'::regconfig, i.title || E'\n' || i.body) @@ plainto_tsquery('public.jiebacfg'::regconfig, $3))
	UNION ALL
	SELECT c.issue_id,
		(CASE WHEN lower(c.body) LIKE public.likequery($3) THEN 50000 ELSE 40000 END
			+ LEAST((ts_rank_cd(to_tsvector('public.jiebacfg'::regconfig, c.body),
				plainto_tsquery('public.jiebacfg'::regconfig, $3)) * 1000)::int, 999)
			+ LEAST((public.bigm_similarity(lower(c.body), $3) * 100)::int, 99)),
		false, true, false
	FROM comments c
	JOIN issues i ON i.organization_id = c.organization_id AND i.repository_id = c.repository_id AND i.id = c.issue_id
	WHERE c.organization_id = $1 AND c.repository_id = ANY($2::uuid[])
		AND ($5 = 'all' OR i.state = $5)
		AND $6 IN ('all', 'comments')
		AND (lower(c.body) LIKE public.likequery($3)
			OR to_tsvector('public.jiebacfg'::regconfig, c.body) @@ plainto_tsquery('public.jiebacfg'::regconfig, $3))
	UNION ALL
	SELECT a.issue_id, 100000, false, false, true
	FROM issue_spec_artifacts a
	JOIN issues i ON i.organization_id = a.organization_id AND i.repository_id = a.repository_id AND i.id = a.issue_id
	WHERE a.organization_id = $1 AND a.repository_id = ANY($2::uuid[]) AND a.active AND a.issue_id IS NOT NULL
		AND ($5 = 'all' OR i.state = $5) AND $6 IN ('all', 'change') AND lower(a.change_key) = $3
), ranked AS (
	SELECT issue_id, max(score)::int AS score, bool_or(issue_matched) AS issue_matched,
		bool_or(comment_matched) AS comment_matched, bool_or(change_matched) AS change_matched
	FROM raw_candidates GROUP BY issue_id
), selected AS (
	SELECT i.organization_id, o.name AS organization, i.repository_id, r.name AS repository, i.id, i.number, i.title, i.body,
		i.state, i.updated_at, ranked.score, ranked.issue_matched, ranked.comment_matched, ranked.change_matched,
		count(*) OVER() AS total_count
	FROM ranked JOIN issues i ON i.organization_id = $1 AND i.repository_id = ANY($2::uuid[]) AND i.id = ranked.issue_id
	JOIN repos r ON r.organization_id = i.organization_id AND r.id = i.repository_id
	JOIN orgs o ON o.id = i.organization_id
	WHERE $7 = '' OR EXISTS (
		SELECT 1 FROM issue_spec_artifacts stage_artifact
		WHERE stage_artifact.organization_id = i.organization_id AND stage_artifact.repository_id = i.repository_id
			AND stage_artifact.issue_id = i.id AND stage_artifact.active
		GROUP BY stage_artifact.change_key
		HAVING CASE WHEN bool_or(stage_artifact.artifact_type = 'implement') THEN 'implement'
			WHEN bool_or(stage_artifact.artifact_type = 'design') THEN 'design'
			WHEN bool_or(stage_artifact.artifact_type = 'proposal') THEN 'proposal' ELSE 'unknown' END = $7)
	ORDER BY ranked.score DESC, (i.state = 'open') DESC, i.updated_at DESC, lower(r.name), i.number, i.id
	LIMIT $8 OFFSET $9
)
SELECT s.organization_id, s.organization, s.repository_id, s.repository, s.id, s.number, s.title, s.body, s.state, s.updated_at,
	COALESCE((SELECT jsonb_agg(jsonb_build_object('key', changes.change_key, 'stage', changes.stage) ORDER BY changes.change_key)
		FROM (SELECT a.change_key, CASE WHEN bool_or(a.artifact_type = 'implement') THEN 'implement'
			WHEN bool_or(a.artifact_type = 'design') THEN 'design'
			WHEN bool_or(a.artifact_type = 'proposal') THEN 'proposal' ELSE 'unknown' END AS stage
			FROM issue_spec_artifacts a WHERE a.organization_id = s.organization_id AND a.repository_id = s.repository_id
				AND a.issue_id = s.id AND a.active GROUP BY a.change_key) changes), '[]'::jsonb),
	s.score, s.issue_matched, s.change_matched,
	COALESCE((SELECT jsonb_agg(jsonb_build_object('id', matches.id, 'body', matches.body) ORDER BY matches.score DESC, matches.updated_at DESC)
		FROM (SELECT c.id, c.body, c.updated_at,
			(CASE WHEN lower(c.body) LIKE public.likequery($3) THEN 50000 ELSE 40000 END
				+ LEAST((ts_rank_cd(to_tsvector('public.jiebacfg'::regconfig, c.body),
					plainto_tsquery('public.jiebacfg'::regconfig, $3)) * 1000)::int, 999)
				+ LEAST((public.bigm_similarity(lower(c.body), $3) * 100)::int, 99)) AS score
			FROM comments c WHERE s.comment_matched AND c.organization_id = s.organization_id
				AND c.repository_id = s.repository_id AND c.issue_id = s.id
				AND (lower(c.body) LIKE public.likequery($3)
					OR to_tsvector('public.jiebacfg'::regconfig, c.body) @@ plainto_tsquery('public.jiebacfg'::regconfig, $3))
			ORDER BY score DESC, c.updated_at DESC, c.id LIMIT 3) matches), '[]'::jsonb),
	s.total_count
FROM selected s
ORDER BY s.score DESC, (s.state = 'open') DESC, s.updated_at DESC, lower(s.repository), s.number, s.id`
