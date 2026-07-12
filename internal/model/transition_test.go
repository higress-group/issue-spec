package model

import (
	"strings"
	"testing"
)

const transitionProcessBody = `## Process: implement

### Owner

- Worker

### Parent TASK

- TASK-001

### Write Ownership

- internal/model

### Dependencies

- N/A

### Covers

- TASK-001

### Handoff

N/A`

func TestApplyTypedTransitionPreservesUndeclaredContent(t *testing.T) {
	body, err := EnsureTypedBody("PROCESS", "PROCESS-001", transitionProcessBody, BodyOptions{
		Agent: "Worker", AgentSessionID: "old-session", AgentSessionSource: "old-source",
		Status: "confirmed", Scope: "transition", Links: map[string][]string{"Proposal Issue": {"https://example.test/proposal"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyTypedTransition(body, TransitionRequest{
		ExpectedType: "PROCESS", ExpectedID: "PROCESS-001", ToStatus: "in-progress",
		Handoff: &HandoffMutation{Value: "API contract ready"},
		PRLinks: []string{"https://example.test/pull/7"}, RelatedLinks: []string{"https://example.test/task"},
		AgentSessionID: "new-session", AgentSessionSource: "CODEX_THREAD_ID",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.FromStatus != "confirmed" || result.ToStatus != "in-progress" {
		t.Fatalf("result = %+v", result)
	}
	before, after := ParseTypedComment(body), ParseTypedComment(result.Body)
	if before.Marker != after.Marker || before.Agent != after.Agent || before.Scope != after.Scope || LogicalBody(result.Body) == LogicalBody(body) {
		t.Fatalf("invariants or declared handoff not applied\n%s", result.Body)
	}
	for _, want := range []string{"API contract ready", "https://example.test/pull/7", "https://example.test/task", "Agent Session ID: new-session"} {
		if !strings.Contains(result.Body, want) {
			t.Fatalf("missing %q\n%s", want, result.Body)
		}
	}
}

func TestApplyTypedTransitionIsIdempotent(t *testing.T) {
	body, _ := EnsureTypedBody("PROCESS", "PROCESS-001", transitionProcessBody, BodyOptions{Status: "in-progress"})
	request := TransitionRequest{ToStatus: "in-progress", Handoff: &HandoffMutation{Value: "ready", Append: true}, PRLinks: []string{"https://example.test/pr/1"}}
	first, err := ApplyTypedTransition(body, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ApplyTypedTransition(first.Body, request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || second.Changed || second.Body != first.Body {
		t.Fatalf("idempotence first=%+v second=%+v", first, second)
	}
}

func TestApplyTypedTransitionRejectsIllegalEdgesAndUndeclaredIdentity(t *testing.T) {
	body, _ := EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y", BodyOptions{Status: "done"})
	for name, request := range map[string]TransitionRequest{
		"backward":        {ToStatus: "draft"},
		"wrong id":        {ExpectedID: "SPEC-002", ToStatus: "done"},
		"handoff on spec": {ToStatus: "done", Handoff: &HandoffMutation{Value: "bad"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ApplyTypedTransition(body, request); err == nil {
				t.Fatal("transition should fail")
			}
		})
	}
}

func TestApplyTypedTransitionTypeSpecificEdges(t *testing.T) {
	tests := []struct {
		typ, from, to string
		allowed       bool
	}{
		{"SPEC", "confirmed", "done", true}, {"SPEC", "done", "confirmed", false},
		{"TASK", "confirmed", "in-progress", true}, {"TASK", "done", "ready", false},
		{"PROCESS", "in-progress", "blocked", true}, {"PROCESS", "done", "in-progress", false},
		{"QUESTION", "blocked", "confirmed", true}, {"QUESTION", "confirmed", "blocked", false},
		{"REVIEW", "in-progress", "done", true}, {"VERIFY", "ready", "done", true},
	}
	for _, test := range tests {
		if got := allowedTransition(test.typ, test.from, test.to); got != test.allowed {
			t.Fatalf("%s %s -> %s = %v want %v", test.typ, test.from, test.to, got, test.allowed)
		}
	}
}

func TestApplyTypedTransitionRequiresCanonicalTypedEnvelope(t *testing.T) {
	if _, err := ApplyTypedTransition("Type: TASK\nID: TASK-001\nStatus: ready", TransitionRequest{ToStatus: "done"}); err == nil {
		t.Fatal("markerless transition should fail")
	}
	linkless := "<!-- issue-spec:type=TASK id=TASK-001 version=1 -->\nAgent: Worker\nType: TASK\nID: TASK-001\nStatus: ready\nScope: test\n\n## Task: x"
	if _, err := ApplyTypedTransition(linkless, TransitionRequest{ToStatus: "done"}); err == nil {
		t.Fatal("linkless transition should fail")
	}
}
