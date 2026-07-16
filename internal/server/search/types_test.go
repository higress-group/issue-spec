package search

import (
	"errors"
	"strings"
	"testing"
)

func TestOptionsNormalize(t *testing.T) {
	got, err := (Options{Query: "  鉴权锁  "}).normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got.Query != "鉴权锁" || got.State != "all" || got.Source != SourceAll || got.Page != 1 || got.PerPage != DefaultPerPage {
		t.Fatalf("normalized options = %+v", got)
	}
	for _, options := range []Options{
		{}, {Query: strings.Repeat("x", MaxQueryBytes+1)}, {Query: "x", State: "merged"},
		{Query: "x", Source: "artifact"}, {Query: "x", Stage: "archive"}, {Query: "x", Page: -1}, {Query: "x", PerPage: MaxPerPage + 1},
	} {
		if _, err := options.normalize(); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("normalize(%+v) error = %v", options, err)
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

func TestSearchQueryKeepsAuthorizationOutsideAndUsesBothIndexes(t *testing.T) {
	for _, required := range []string{"LIKE public.likequery($3)", "to_tsvector('public.jiebacfg'::regconfig", "issue_spec_artifacts", "LIMIT $8 OFFSET $9"} {
		if !strings.Contains(searchQuery, required) {
			t.Fatalf("search query missing %q", required)
		}
	}
}
