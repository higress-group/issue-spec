package model

import (
	"errors"
	"strings"
	"testing"
)

func TestSetProcessCodeChangeLinkChangesOnlyEmptyPRField(t *testing.T) {
	body, err := EnsureTypedBody("PROCESS", "PROCESS-006", `## Process: provider-neutral linking

### Parent TASK

- TASK-004

### Handoff

Keep this handoff byte-for-byte.`, BodyOptions{
		Agent: "Worker Agent", AgentSessionID: "session-006", AgentSessionSource: "CODEX_THREAD_ID",
		SubjectRevision: "head-abc", Status: "in-progress", Scope: "internal/commands/**",
		Links: map[string][]string{
			"Proposal Issue":   {"https://issues.example/acme/widgets/issues/1"},
			"Design Issue":     {"https://issues.example/acme/widgets/issues/2"},
			"Implement Issue":  {"https://issues.example/acme/widgets/issues/3"},
			"Related Comments": {"https://issues.example/acme/widgets/issues/3#issuecomment-4"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical := "https://code.example/acme/widgets/changes/42"
	updated, changed, err := SetProcessCodeChangeLink(body, "PROCESS-006", canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("empty PR field was not filled")
	}
	want := strings.Replace(body, "- PR: N/A", "- PR: "+canonical, 1)
	if updated != want {
		t.Fatalf("mutation changed bytes outside PR field:\n%s", updated)
	}
	before, after := ParseTypedComment(body), ParseTypedComment(updated)
	if before.Agent != after.Agent || before.AgentSessionID != after.AgentSessionID ||
		before.AgentSessionSource != after.AgentSessionSource || before.SubjectRevision != after.SubjectRevision ||
		before.Status != after.Status || before.Scope != after.Scope {
		t.Fatalf("typed metadata changed: before=%+v after=%+v", before, after)
	}
}

func TestSetProcessCodeChangeLinkIsIdempotentAndRejectsOverwrite(t *testing.T) {
	body, err := EnsureTypedBody("PROCESS", "PROCESS-006", "## Process\n\nKeep.", BodyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	body = strings.Replace(body, "- PR: N/A", "- PR: https://code.example/changes/42/", 1)
	unchanged, changed, err := SetProcessCodeChangeLink(body, "PROCESS-006", "https://code.example/changes/42")
	if err != nil || changed || unchanged != body {
		t.Fatalf("idempotent result changed=%t error=%v\n%s", changed, err, unchanged)
	}

	conflicting := strings.Replace(body, "https://code.example/changes/42/", "https://code.example/changes/41", 1)
	if _, _, err := SetProcessCodeChangeLink(conflicting, "PROCESS-006", "https://code.example/changes/42"); !errors.Is(err, ErrProcessPRLinkConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	multiple := strings.Replace(body, "https://code.example/changes/42/",
		"https://code.example/changes/42, https://code.example/changes/41", 1)
	if _, _, err := SetProcessCodeChangeLink(multiple, "PROCESS-006", "https://code.example/changes/42"); !errors.Is(err, ErrProcessPRLinkConflict) {
		t.Fatalf("multiple-value conflict error = %v", err)
	}
}

func TestSetProcessCodeChangeLinkRejectsInvalidArtifactOrURL(t *testing.T) {
	body, err := EnsureTypedBody("PROCESS", "PROCESS-006", "## Process\n\nKeep.", BodyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	invalid := []struct {
		name string
		body string
		id   string
		url  string
	}{
		{name: "wrong id", body: body, id: "PROCESS-007", url: "https://code.example/changes/42"},
		{name: "duplicate PR field", body: strings.Replace(body, "- PR: N/A", "- PR: N/A\n- PR: N/A", 1), id: "PROCESS-006", url: "https://code.example/changes/42"},
		{name: "unsafe URL", body: body, id: "PROCESS-006", url: "https://code.example/changes/42?token=secret"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := SetProcessCodeChangeLink(test.body, test.id, test.url); err == nil {
				t.Fatal("invalid mutation accepted")
			}
		})
	}
}
