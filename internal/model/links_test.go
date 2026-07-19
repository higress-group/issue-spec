package model

import (
	"strings"
	"testing"
)

func TestMergeTypedHeaderRelationshipsPreservesKnownAndFutureEntries(t *testing.T) {
	existing := relationshipBody(t, "PROCESS", "PROCESS-002", map[string][]string{
		"Proposal Issue":   {"https://example.test/issues/1"},
		"Design Issue":     {"https://example.test/issues/2"},
		"Implement Issue":  {"https://example.test/issues/3"},
		"Related Comments": {"https://example.test/issues/3#issuecomment-4"},
		"PR":               {"https://example.test/pulls/5"},
	})
	existing = strings.Replace(existing, "- PR: https://example.test/pulls/5",
		"- PR: https://example.test/pulls/5\n- Future Relationship: https://example.test/future/6", 1)
	desired := relationshipBody(t, "PROCESS", "PROCESS-002", map[string][]string{
		"Related Comments": {"https://example.test/issues/3#issuecomment-7"},
		"PR":               {"https://example.test/pulls/8"},
	})

	merged, changed, err := MergeTypedHeaderRelationships(existing, desired)
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	parsed := ParseTypedComment(merged)
	want := map[string][]string{
		"Proposal Issue":      {"https://example.test/issues/1"},
		"Design Issue":        {"https://example.test/issues/2"},
		"Implement Issue":     {"https://example.test/issues/3"},
		"Related Comments":    {"https://example.test/issues/3#issuecomment-7", "https://example.test/issues/3#issuecomment-4"},
		"PR":                  {"https://example.test/pulls/8", "https://example.test/pulls/5"},
		"Future Relationship": {"https://example.test/future/6"},
	}
	for name, values := range want {
		if got := parsed.Links[name]; !sameLinkValues(got, values) {
			t.Errorf("%s=%v want %v", name, got, values)
		}
	}
	if !strings.Contains(merged, "desired content") {
		t.Fatalf("desired logical body was not retained:\n%s", merged)
	}

	replayed, replayChanged, err := MergeTypedHeaderRelationships(existing, merged)
	if err != nil || replayChanged || replayed != merged {
		t.Fatalf("idempotent replay changed=%t err=%v", replayChanged, err)
	}
}

func TestMergeTypedHeaderRelationshipsFailsClosedOnInvalidInput(t *testing.T) {
	body := relationshipBody(t, "PROCESS", "PROCESS-002", nil)
	duplicate := strings.Replace(body, "- PR: N/A", "- PR: N/A\n- PR: https://example.test/pulls/1", 1)
	other := relationshipBody(t, "PROCESS", "PROCESS-003", nil)
	for _, test := range []struct {
		name    string
		before  string
		desired string
	}{
		{name: "duplicate relationship", before: duplicate, desired: body},
		{name: "identity mismatch", before: body, desired: other},
		{name: "missing typed header", before: "plain text", desired: body},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := MergeTypedHeaderRelationships(test.before, test.desired); err == nil {
				t.Fatal("expected merge error")
			}
		})
	}
}

func relationshipBody(t *testing.T, kind, id string, links map[string][]string) string {
	t.Helper()
	body, err := EnsureTypedBody(kind, id, "## Work\n\ndesired content", BodyOptions{
		Agent: "Worker", Status: "in-progress", Scope: "test", Links: links,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
