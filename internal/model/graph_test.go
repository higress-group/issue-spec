package model

import (
	"errors"
	"strings"
	"testing"
)

func TestVerifyTraceabilityCanonicalOwnerEdgesIgnoreReverseBacklinks(t *testing.T) {
	spec := Artifact{Issue: 381, URL: "https://example.test/issues/381#issuecomment-1",
		Comment: TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "confirmed"}}
	taskBody, err := EnsureTypedBody("TASK", "TASK-001", "## Task\n\n### Covers\n\n- SPEC-001",
		BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	processBody, err := EnsureTypedBody("PROCESS", "PROCESS-001",
		"## Process\n\n### Parent TASK\n\n- TASK-001\n\n### Dependencies\n\n- N/A\n\n### Covers\n\n- SPEC-001",
		BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	task := Artifact{Issue: 383, URL: "https://example.test/issues/383#issuecomment-2", Comment: ParseTypedComment(taskBody)}
	process := Artifact{Issue: 383, URL: "https://example.test/issues/383#issuecomment-3", Comment: ParseTypedComment(processBody)}
	artifacts := []Artifact{spec, task, process}
	edges := []TraceabilityEdge{
		{Kind: "task-covers-spec", OwnerID: "TASK-001", TargetID: "SPEC-001"},
		{Kind: "process-parent-task", OwnerID: "PROCESS-001", TargetID: "TASK-001"},
	}

	withoutBacklinks := VerifyTraceabilityWithRelationships(artifacts, edges, nil)
	if !withoutBacklinks.OK {
		t.Fatalf("canonical owner edges required reverse backlinks: %+v", withoutBacklinks)
	}
	artifacts[0].Comment.Links = map[string][]string{"Related Comments": {task.URL}}
	artifacts[1].Comment.Links["Related Comments"] = []string{process.URL}
	withBacklinks := VerifyTraceabilityWithRelationships(artifacts, edges, nil)
	if !withBacklinks.OK || len(withBacklinks.Errors) != len(withoutBacklinks.Errors) {
		t.Fatalf("legacy reverse backlinks changed readiness: before=%+v after=%+v", withoutBacklinks, withBacklinks)
	}

	missingOwner := VerifyTraceabilityWithRelationships(artifacts, edges[:1], nil)
	if missingOwner.OK || !containsVerifyError(missingOwner.Errors, "Parent TASK TASK-001 is missing its canonical TASK URL") {
		t.Fatalf("missing canonical owner edge did not fail closed: %+v", missingOwner)
	}
}

func TestVerifyTraceabilityRelationshipFailureCannotUseLegacyBacklinks(t *testing.T) {
	spec := Artifact{Issue: 1, URL: "https://example.test/issues/1#spec",
		Comment: TypedComment{Type: "SPEC", ID: "SPEC-001", Status: "confirmed"}}
	taskBody, err := EnsureTypedBody("TASK", "TASK-001", "### Covers\n\n- SPEC-001",
		BodyOptions{Status: "done", Links: map[string][]string{"Related Comments": {spec.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	task := Artifact{Issue: 3, URL: "https://example.test/issues/3#task", Comment: ParseTypedComment(taskBody)}
	// The old model-only reader accepts this exact owner backlink. Indexed
	// readers must retain the construction error instead of invoking it.
	if legacy := VerifyTraceability([]Artifact{spec, task}); !legacy.OK {
		t.Fatalf("fixture did not reproduce legacy authority: %+v", legacy)
	}
	indexErr := errors.New("relationship_ambiguous: duplicate physical link")
	report := VerifyTraceabilityWithRelationships([]Artifact{spec, task}, nil, indexErr)
	if report.OK || !containsVerifyError(report.Errors, indexErr.Error()) ||
		!containsVerifyError(report.Errors, "missing its canonical SPEC URL") {
		t.Fatalf("indexed reader recovered from its construction failure: %+v", report)
	}
}

func containsVerifyError(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
