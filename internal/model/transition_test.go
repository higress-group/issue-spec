package model

import (
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/processworkspace"
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

func TestApplyTypedTransitionAddsWorkspaceWithoutChangingOtherBytes(t *testing.T) {
	body, err := EnsureTypedBody("PROCESS", "PROCESS-001", transitionProcessBody, BodyOptions{
		Agent: "Worker", AgentSessionID: "stable-session", AgentSessionSource: "CODEX_THREAD_ID",
		Status: "in-progress", Scope: "workspace", Links: map[string][]string{"Proposal Issue": {"https://example.test/proposal"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace := testProcessWorkspace("PROCESS-001", ProcessExecutionChangeBearing)
	result, err := ApplyTypedTransition(body, TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-001", Workspace: &workspace})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || withoutWorkspace(result.Body) != body {
		t.Fatalf("workspace mutation changed bytes outside its section\n--- before\n%s\n--- after\n%s", body, result.Body)
	}
	parsed := ParseProcessWorkspace("PROCESS-001", "", result.Body)
	if parsed.Blocking() || parsed.Workspace == nil || parsed.Workspace.WorkspaceID != workspace.WorkspaceID {
		t.Fatalf("workspace parse = %+v", parsed)
	}
	before, after := ParseTypedComment(body), ParseTypedComment(result.Body)
	if before.AgentSessionID != after.AgentSessionID || before.AgentSessionSource != after.AgentSessionSource || before.Links["Proposal Issue"][0] != after.Links["Proposal Issue"][0] {
		t.Fatal("workspace mutation changed session metadata or links")
	}
}

func TestApplyTypedTransitionUpdatesWorkspaceEvidenceIdempotently(t *testing.T) {
	workspace := testProcessWorkspace("PROCESS-001", ProcessExecutionChangeBearing)
	logical := strings.Replace(transitionProcessBody, "### Handoff", mustWorkspaceSection(t, workspace)+"\n\n### Handoff", 1)
	body, err := EnsureTypedBody("PROCESS", "PROCESS-001", logical, BodyOptions{Status: "in-progress", AgentSessionID: "unchanged"})
	if err != nil {
		t.Fatal(err)
	}
	updatedWorkspace := workspace
	updatedWorkspace.State = "worker-complete"
	updatedWorkspace.ResultCommit = "2222222222222222222222222222222222222222"
	updatedWorkspace.UpdatedAt = updatedWorkspace.UpdatedAt.Add(time.Minute)
	request := TransitionRequest{ToStatus: "in-progress", Workspace: &updatedWorkspace}
	first, err := ApplyTypedTransition(body, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ApplyTypedTransition(first.Body, request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || second.Changed || second.Body != first.Body {
		t.Fatalf("workspace evidence mutation not idempotent: first=%+v second=%+v", first, second)
	}
	if withoutWorkspace(first.Body) != withoutWorkspace(body) {
		t.Fatal("workspace evidence update changed bytes outside Workspace")
	}
	if got := ParseProcessWorkspace("PROCESS-001", "", first.Body); got.Workspace == nil || got.Workspace.ResultCommit != updatedWorkspace.ResultCommit {
		t.Fatalf("updated workspace = %+v", got)
	}
}

func TestApplyTypedTransitionRejectsCorruptWorkspaceSection(t *testing.T) {
	logical := strings.Replace(transitionProcessBody, "### Handoff", "### Workspace\n\n```json\n{}\n```\n\n### Handoff", 1)
	body, err := EnsureTypedBody("PROCESS", "PROCESS-001", logical, BodyOptions{Status: "in-progress"})
	if err != nil {
		t.Fatal(err)
	}
	workspace := testProcessWorkspace("PROCESS-001", ProcessExecutionChangeBearing)
	if _, err := ApplyTypedTransition(body, TransitionRequest{Workspace: &workspace}); err == nil || !strings.Contains(err.Error(), "Workspace") {
		t.Fatalf("expected corrupt Workspace rejection, got %v", err)
	}
}

func TestApplyTypedTransitionRejectsUnsafeWorkspaceMutationMatrix(t *testing.T) {
	base := testProcessWorkspace("PROCESS-001", ProcessExecutionChangeBearing)
	base.UpdatedAt = base.CreatedAt.Add(time.Minute)
	base.RetentionExpiresAt = base.CreatedAt.Add(48 * time.Hour)
	resultSHA := "2222222222222222222222222222222222222222"
	integrationSHA := "3333333333333333333333333333333333333333"
	tests := []struct {
		name         string
		prepare      func(*ProcessWorkspace)
		mutate       func(*ProcessWorkspace)
		wantContains string
	}{
		{name: "schema version", mutate: func(w *ProcessWorkspace) { w.SchemaVersion++ }, wantContains: "reservation identity"},
		{name: "workspace id", mutate: func(w *ProcessWorkspace) { w.WorkspaceID = "ws-other" }, wantContains: "reservation identity"},
		{name: "repository", mutate: func(w *ProcessWorkspace) { w.Repository = "higress-group/other" }, wantContains: "reservation identity"},
		{name: "process id", mutate: func(w *ProcessWorkspace) { w.ProcessID = "PROCESS-002" }, wantContains: "reservation identity"},
		{name: "execution class", mutate: func(w *ProcessWorkspace) { w.ExecutionClass = processworkspace.ExecutionExternal }, wantContains: "reservation identity"},
		{name: "mode", mutate: func(w *ProcessWorkspace) { w.Mode = processworkspace.ModeSnapshot }, wantContains: "reservation identity"},
		{name: "base sha", mutate: func(w *ProcessWorkspace) { w.BaseSHA = "4444444444444444444444444444444444444444" }, wantContains: "reservation identity"},
		{name: "branch", mutate: func(w *ProcessWorkspace) { w.Branch = "codex/other" }, wantContains: "reservation identity"},
		{name: "detached revision", mutate: func(w *ProcessWorkspace) { w.DetachedRevision = workspaceBaseSHA }, wantContains: "reservation identity"},
		{name: "write ownership", mutate: func(w *ProcessWorkspace) { w.WriteOwnership = []string{"internal/templates/**"} }, wantContains: "reservation identity"},
		{name: "shared touchpoints", mutate: func(w *ProcessWorkspace) { w.SharedTouchpoints = []string{"go.mod"} }, wantContains: "reservation identity"},
		{name: "integration owner", mutate: func(w *ProcessWorkspace) { w.IntegrationOwner = "PROCESS-099" }, wantContains: "reservation identity"},
		{name: "runtime namespace", mutate: func(w *ProcessWorkspace) { w.RuntimeNamespace = "runtime-other" }, wantContains: "reservation identity"},
		{name: "runtime resources", mutate: func(w *ProcessWorkspace) {
			w.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "port", Name: "http", Exclusive: true}}
		}, wantContains: "reservation identity"},
		{name: "created at", mutate: func(w *ProcessWorkspace) { w.CreatedAt = w.CreatedAt.Add(-time.Minute) }, wantContains: "reservation identity"},
		{name: "illegal lifecycle rollback", mutate: func(w *ProcessWorkspace) {
			w.State = processworkspace.StatePreparing
			w.UpdatedAt = w.UpdatedAt.Add(time.Minute)
		}, wantContains: "illegal workspace lifecycle transition"},
		{name: "updated at rollback", mutate: func(w *ProcessWorkspace) { w.UpdatedAt = w.CreatedAt }, wantContains: "updated_at cannot move backwards"},
		{name: "material change without clock advance", mutate: func(w *ProcessWorkspace) {
			w.State = processworkspace.StateWorkerComplete
			w.ResultCommit = resultSHA
		}, wantContains: "require updated_at to advance"},
		{name: "shorten retention", mutate: func(w *ProcessWorkspace) { w.RetentionExpiresAt = w.CreatedAt.Add(24 * time.Hour) }, wantContains: "cannot be cleared or shortened"},
		{name: "clear retention", mutate: func(w *ProcessWorkspace) { w.RetentionExpiresAt = time.Time{} }, wantContains: "cannot be cleared or shortened"},
		{name: "replace result commit", prepare: func(w *ProcessWorkspace) {
			w.State = processworkspace.StateWorkerComplete
			w.ResultCommit = resultSHA
		}, mutate: func(w *ProcessWorkspace) {
			w.ResultCommit = "5555555555555555555555555555555555555555"
			w.UpdatedAt = w.UpdatedAt.Add(time.Minute)
		}, wantContains: "cannot clear or replace result commit"},
		{name: "clear result commit", prepare: func(w *ProcessWorkspace) {
			w.State = processworkspace.StateWorkerComplete
			w.ResultCommit = resultSHA
		}, mutate: func(w *ProcessWorkspace) {
			w.State = processworkspace.StateConflicted
			w.ResultCommit = ""
			w.UpdatedAt = w.UpdatedAt.Add(time.Minute)
		}, wantContains: "cannot clear or replace result commit"},
		{name: "replace integration sha", prepare: func(w *ProcessWorkspace) {
			w.State = processworkspace.StateIntegrated
			w.ResultCommit = resultSHA
			w.IntegrationSHA = integrationSHA
		}, mutate: func(w *ProcessWorkspace) {
			w.IntegrationSHA = "6666666666666666666666666666666666666666"
			w.UpdatedAt = w.UpdatedAt.Add(time.Minute)
		}, wantContains: "cannot clear or replace integration SHA"},
		{name: "clear integration sha", prepare: func(w *ProcessWorkspace) {
			w.State = processworkspace.StateIntegrated
			w.ResultCommit = resultSHA
			w.IntegrationSHA = integrationSHA
		}, mutate: func(w *ProcessWorkspace) {
			w.State = processworkspace.StateConflicted
			w.IntegrationSHA = ""
			w.UpdatedAt = w.UpdatedAt.Add(time.Minute)
		}, wantContains: "cannot clear or replace integration SHA"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := base
			if test.prepare != nil {
				test.prepare(&before)
			}
			after := before
			test.mutate(&after)
			body := typedTransitionWorkspaceBody(t, before)
			_, err := ApplyTypedTransition(body, TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-001", Workspace: &after})
			if err == nil || !strings.Contains(err.Error(), test.wantContains) {
				t.Fatalf("expected %q, got %v", test.wantContains, err)
			}
		})
	}
}

func TestApplyTypedTransitionRejectsLateWorkspaceEvidenceMatrix(t *testing.T) {
	resultSHA := "2222222222222222222222222222222222222222"
	integrationSHA := "3333333333333333333333333333333333333333"
	for _, state := range []processworkspace.LifecycleState{processworkspace.StateConflicted, processworkspace.StateCleanupPending, processworkspace.StateCleaned} {
		for _, evidence := range []string{"result", "integration"} {
			t.Run(string(state)+"/"+evidence, func(t *testing.T) {
				before := testProcessWorkspace("PROCESS-001", ProcessExecutionChangeBearing)
				before.State = state
				after := before
				after.UpdatedAt = after.UpdatedAt.Add(time.Minute)
				if evidence == "result" {
					after.ResultCommit = resultSHA
				} else {
					after.IntegrationSHA = integrationSHA
				}
				_, err := ApplyTypedTransition(typedTransitionWorkspaceBody(t, before), TransitionRequest{Workspace: &after})
				if err == nil {
					t.Fatalf("%s accepted late %s evidence", state, evidence)
				}
			})
		}
	}

	prepared := testProcessWorkspace("PROCESS-001", ProcessExecutionChangeBearing)
	workerComplete := prepared
	workerComplete.State = processworkspace.StateWorkerComplete
	workerComplete.ResultCommit = resultSHA
	workerComplete.UpdatedAt = workerComplete.UpdatedAt.Add(time.Minute)
	if _, err := ApplyTypedTransition(typedTransitionWorkspaceBody(t, prepared), TransitionRequest{Workspace: &workerComplete}); err != nil {
		t.Fatalf("legal result evidence edge rejected: %v", err)
	}
	integrated := workerComplete
	integrated.State = processworkspace.StateIntegrated
	integrated.IntegrationSHA = integrationSHA
	integrated.UpdatedAt = integrated.UpdatedAt.Add(time.Minute)
	if _, err := ApplyTypedTransition(typedTransitionWorkspaceBody(t, workerComplete), TransitionRequest{Workspace: &integrated}); err != nil {
		t.Fatalf("legal integration evidence edge rejected: %v", err)
	}
}

func TestApplyTypedTransitionIgnoresWorkspaceExamplesInFencesAndIndentedCode(t *testing.T) {
	examples := "### Scope\n\n```markdown\n### Workspace\n```\n\n~~~markdown\n### Workspace\n~~~\n\n    ### Workspace\n    example\n\n"
	logical := strings.Replace(transitionProcessBody, "### Handoff", examples+"### Handoff", 1)
	body, err := EnsureTypedBody("PROCESS", "PROCESS-001", logical, BodyOptions{Status: "in-progress", AgentSessionID: "stable-session"})
	if err != nil {
		t.Fatal(err)
	}
	workspace := testProcessWorkspace("PROCESS-001", ProcessExecutionChangeBearing)
	result, err := ApplyTypedTransition(body, TransitionRequest{Workspace: &workspace, Handoff: &HandoffMutation{Value: "ready"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Body, examples) || strings.Count(result.Body, "### Workspace") != 4 {
		t.Fatalf("code examples changed during transition:\n%s", result.Body)
	}
	parsed := ParseProcessWorkspace("PROCESS-001", "", result.Body)
	if parsed.Blocking() || parsed.Workspace == nil {
		t.Fatalf("inserted Workspace not canonical: %+v", parsed)
	}
}

func TestApplyTypedTransitionMutatesOnlyStructuralHandoff(t *testing.T) {
	examples := "### Scope\n\n```markdown\n### Handoff\nexample in fence\n```\n\n~~~markdown\n### Handoff\nexample in tilde fence\n~~~\n\n    ### Handoff\n    example in indented code\n\n"
	logical := strings.Replace(transitionProcessBody, "### Handoff", examples+"### Handoff", 1)
	body, err := EnsureTypedBody("PROCESS", "PROCESS-001", logical, BodyOptions{Status: "in-progress", AgentSessionID: "stable-session"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyTypedTransition(body, TransitionRequest{Handoff: &HandoffMutation{Value: "real handoff ready"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Body, examples) {
		t.Fatalf("Handoff examples changed:\n%s", result.Body)
	}
	sections := markdownSectionContents(transitionLogicalBody(result.Body), "### Handoff")
	if len(sections) != 1 || sections[0] != "real handoff ready" {
		t.Fatalf("structural Handoff was not updated: %v", sections)
	}
	if withoutHandoff(transitionLogicalBody(result.Body)) != withoutHandoff(transitionLogicalBody(body)) {
		t.Fatal("Handoff mutation changed bytes outside the structural section")
	}
}

func TestApplyTypedTransitionRejectsDuplicateStructuralHandoff(t *testing.T) {
	logical := transitionProcessBody + "\n\n### Handoff\n\nsecond handoff"
	body, err := EnsureTypedBody("PROCESS", "PROCESS-001", logical, BodyOptions{Status: "in-progress"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ApplyTypedTransition(body, TransitionRequest{Handoff: &HandoffMutation{Value: "ready"}})
	if err == nil || !strings.Contains(err.Error(), "multiple `### Handoff`") {
		t.Fatalf("expected duplicate structural Handoff rejection, got %v", err)
	}
}

func TestApplyTypedTransitionAllowsWorkspaceLifecycleEvidenceChain(t *testing.T) {
	workspace := testProcessWorkspace("PROCESS-001", ProcessExecutionChangeBearing)
	body := typedTransitionWorkspaceBody(t, workspace)
	originalOutside := withoutWorkspace(body)

	workerComplete := workspace
	workerComplete.State = processworkspace.StateWorkerComplete
	workerComplete.ResultCommit = "2222222222222222222222222222222222222222"
	workerComplete.UpdatedAt = workerComplete.UpdatedAt.Add(time.Minute)
	workerComplete.RetentionExpiresAt = workerComplete.CreatedAt.Add(48 * time.Hour)
	integrating := workerComplete
	integrating.State = processworkspace.StateIntegrating
	integrating.UpdatedAt = integrating.UpdatedAt.Add(time.Minute)
	integrated := integrating
	integrated.State = processworkspace.StateIntegrated
	integrated.IntegrationSHA = "3333333333333333333333333333333333333333"
	integrated.UpdatedAt = integrated.UpdatedAt.Add(time.Minute)

	for _, next := range []*ProcessWorkspace{&workerComplete, &integrating, &integrated} {
		result, err := ApplyTypedTransition(body, TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-001", Workspace: next})
		if err != nil {
			t.Fatalf("transition to %s: %v", next.State, err)
		}
		if !result.Changed || withoutWorkspace(result.Body) != originalOutside {
			t.Fatalf("transition to %s changed bytes outside Workspace", next.State)
		}
		body = result.Body
	}
	idempotent, err := ApplyTypedTransition(body, TransitionRequest{Workspace: &integrated})
	if err != nil {
		t.Fatal(err)
	}
	if idempotent.Changed || idempotent.Body != body {
		t.Fatalf("integrated evidence mutation must be idempotent: %+v", idempotent)
	}
}

func typedTransitionWorkspaceBody(t *testing.T, workspace ProcessWorkspace) string {
	t.Helper()
	logical := strings.Replace(transitionProcessBody, "### Handoff", mustWorkspaceSection(t, workspace)+"\n\n### Handoff", 1)
	body, err := EnsureTypedBody("PROCESS", "PROCESS-001", logical, BodyOptions{
		Status: "in-progress", AgentSessionID: "stable-session", AgentSessionSource: "CODEX_THREAD_ID",
		Links: map[string][]string{"Proposal Issue": {"https://example.test/proposal"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func mustWorkspaceSection(t *testing.T, workspace ProcessWorkspace) string {
	t.Helper()
	section, err := RenderProcessWorkspaceSection(workspace)
	if err != nil {
		t.Fatal(err)
	}
	return section
}
