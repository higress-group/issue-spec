package search

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOptionsNormalize(t *testing.T) {
	got, err := (Options{Query: "  鉴权锁  "}).normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got.Query != "鉴权锁" || got.State != "all" || got.Source != SourceIssue || got.Stage != "proposal" || got.Page != 1 || got.PerPage != DefaultPerPage {
		t.Fatalf("normalized options = %+v", got)
	}
	for _, options := range []Options{
		{}, {Query: strings.Repeat("x", MaxQueryBytes+1)}, {Query: "x", State: "merged"},
		{Query: "x", Source: Source("comments")}, {Query: "x", Source: Source("change")}, {Query: "x", Stage: "design"},
		{Query: "x", Stage: "implement"}, {Query: "x", Page: -1}, {Query: "x", PerPage: MaxPerPage + 1},
	} {
		if _, err := options.normalize(); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("normalize(%+v) error = %v", options, err)
		}
	}
}

func TestFullOptionsNormalize(t *testing.T) {
	got, err := (FullOptions{Query: "  comment token  ", State: "OPEN",
		Labels: []string{" Bug ", "bug", "Needs-Review"}}).normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got.Query != "comment token" || got.State != "open" || strings.Join(got.Labels, ",") != "bug,needs-review" ||
		got.Page != 1 || got.PerPage != DefaultPerPage || FullQueryTimeout != 60*time.Second {
		t.Fatalf("normalized full options = %+v timeout=%s", got, FullQueryTimeout)
	}
	for _, options := range []FullOptions{
		{}, {Query: strings.Repeat("x", MaxQueryBytes+1)}, {Query: "x", State: "merged"},
		{Query: "x", Labels: []string{""}}, {Query: "x", Labels: []string{strings.Repeat("x", MaxLabelBytes+1)}},
		{Query: "x", Labels: make([]string, MaxSearchLabels+1)}, {Query: "x", Page: -1}, {Query: "x", PerPage: MaxPerPage + 1},
	} {
		if len(options.Labels) == MaxSearchLabels+1 {
			for index := range options.Labels {
				options.Labels[index] = string(rune('a'+index%26)) + string(rune('0'+index/26))
			}
		}
		if _, err := options.normalize(); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("normalize full (%+v) error = %v", options, err)
		}
	}
}

func TestExcerptIsBoundedAroundMatch(t *testing.T) {
	value := strings.Repeat("前", 100) + "鉴权锁争用" + strings.Repeat("后", 100)
	got := excerpt(value, "锁")
	if !strings.Contains(got, "鉴权锁争用") || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("excerpt = %q", got)
	}
}

func TestSearchQueryMaterializesProposalsBeforeMatchingTitleAndBody(t *testing.T) {
	for _, required := range []string{"proposal_issues AS MATERIALIZED", "proposal.artifact_type = 'proposal'", "proposal.active",
		"LIKE public.likequery($3)", "to_tsvector('public.jiebacfg'::regconfig", "ts_rank_cd(", "public.bigm_similarity(",
		"(ranked.state = 'open') DESC", "LIMIT $5 OFFSET $6"} {
		if !strings.Contains(searchQuery, required) {
			t.Fatalf("search query missing %q", required)
		}
	}
	for _, excluded := range []string{"FROM comments", "JOIN comments", "lower(change_key) =", "i.number ="} {
		if strings.Contains(searchQuery, excluded) {
			t.Fatalf("search query unexpectedly contains %q", excluded)
		}
	}
}

func TestFullRepositorySearchQueryIncludesIssuesCommentsAndFilters(t *testing.T) {
	for _, required := range []string{"eligible_issues AS NOT MATERIALIZED", "FROM comments c", "FROM issue_labels il",
		"i.number = $4", "LIKE public.likequery($3)", "to_tsvector('public.jiebacfg'::regconfig", "LIMIT $8 OFFSET $9"} {
		if !strings.Contains(fullRepositorySearchQuery, required) {
			t.Fatalf("full repository search query missing %q", required)
		}
	}
	for _, excluded := range []string{"artifact_type = 'proposal'", "lower(change_key) ="} {
		if strings.Contains(fullRepositorySearchQuery, excluded) {
			t.Fatalf("full repository search query unexpectedly contains %q", excluded)
		}
	}
}
