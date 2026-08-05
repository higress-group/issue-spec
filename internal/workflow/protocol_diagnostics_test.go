package workflow

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestResolveWarnsOnConflictingProjectPhaseProtocol(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "issue-spec", "config.yaml"), `
schema: custom
context:
  project_guidance: |
    Workflow shape:
    - Create the proposal issue and initial SPEC typed comments first.
    - Then create QUESTION typed comments.
rules:
  questions: Create QUESTION typed comments after initial SPEC comments and before design.
  design:
    - Read proposal QUESTION typed comments before writing design.
    - If multiple viable plans or unresolved decisions remain, include a Confirmation Checklist and wait before tasks.
`)
	writeFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "schema.yaml"), `
artifacts:
  proposal:
    type: proposal
  questions:
    type: questions
    instructions: Create concrete QUESTION typed comments after initial SPEC comments and before design.
  design:
    type: design
    instructions: The Design issue should include architecture, rollout and rollback notes, and remaining questions.
  tasks:
    type: tasks
    instructions: Create canonical TASK typed comments after design.
`)

	plan, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve returned error: %v diagnostics=%+v", err, plan.Diagnostics)
	}
	if got, want := diagnosticSources(plan.Diagnostics, phaseOrderConflictDiagnostic), []string{
		"artifacts.questions.instructions",
		"context.project_guidance",
		"rules.questions",
	}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("phase-order diagnostic sources = %v, want %v; diagnostics=%+v", got, want, plan.Diagnostics)
	}
	if got, want := diagnosticSources(plan.Diagnostics, openDecisionCarrierConflictDiagnostic), []string{
		"artifacts.design.instructions",
		"rules.design[1]",
	}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("carrier diagnostic sources = %v, want %v; diagnostics=%+v", got, want, plan.Diagnostics)
	}
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Code != phaseOrderConflictDiagnostic && diagnostic.Code != openDecisionCarrierConflictDiagnostic {
			continue
		}
		if diagnostic.Severity != "warning" || diagnostic.Source == "" || diagnostic.Path == "" {
			t.Fatalf("protocol diagnostic lacks warning source metadata: %+v", diagnostic)
		}
	}
}

func TestResolveDoesNotWarnOnCompatibleProjectPhaseProtocolReferences(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "issue-spec", "config.yaml"), `
schema: custom
context:
  project_guidance: Do not author SPEC before QUESTION typed comments.
rules:
  questions: Never create QUESTION typed comments after initial SPEC comments.
  design:
    - Read proposal QUESTION typed comments before writing design.
    - Do not include remaining questions in the Design issue; create each as a blocking typed QUESTION.
`)
	writeFile(t, filepath.Join(root, "issue-spec", "schemas", "custom", "schema.yaml"), `
artifacts:
  design:
    type: design
    instructions: Include a Confirmation Checklist for resolved SPEC links and reference blocking typed QUESTION comments by ID.
  tasks:
    type: tasks
    instructions: Create canonical TASK typed comments after design.
`)

	plan, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve returned error: %v diagnostics=%+v", err, plan.Diagnostics)
	}
	for _, code := range []string{phaseOrderConflictDiagnostic, openDecisionCarrierConflictDiagnostic} {
		if hasDiagnostic(plan.Diagnostics, code) {
			t.Fatalf("compatible guidance emitted %s: %+v", code, plan.Diagnostics)
		}
	}
}

func diagnosticSources(diagnostics []Diagnostic, code string) []string {
	sources := make([]string, 0)
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			sources = append(sources, diagnostic.Source)
		}
	}
	sort.Strings(sources)
	return sources
}
