package finalization

import (
	"reflect"
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
)

func TestSelectionResolvesHistoricalProcessesToUniqueActiveSink(t *testing.T) {
	third := processArtifact(t, "PROCESS-003", "ready", "https://example.test/issues/1#comment-3", nil)
	second := processArtifact(t, "PROCESS-002", "superseded", "https://example.test/issues/1#comment-2", &model.SupersededBy{ProcessID: "PROCESS-003", URL: third.URL})
	first := processArtifact(t, "PROCESS-001", "done", "https://example.test/issues/1#comment-1", &model.SupersededBy{ProcessID: "PROCESS-002", URL: second.URL})

	selection := EvaluateSelection([]model.Artifact{third, first, second})
	if !selection.Valid() {
		t.Fatalf("diagnostics = %+v", selection.Diagnostics)
	}
	if !reflect.DeepEqual(selection.ActiveProcessIDs, []string{"PROCESS-003"}) {
		t.Fatalf("active = %v", selection.ActiveProcessIDs)
	}
	want := []HistoricalProcess{
		{ProcessID: "PROCESS-001", ActiveSinkID: "PROCESS-003", Chain: []string{"PROCESS-001", "PROCESS-002", "PROCESS-003"}},
		{ProcessID: "PROCESS-002", ActiveSinkID: "PROCESS-003", Chain: []string{"PROCESS-002", "PROCESS-003"}},
	}
	if !reflect.DeepEqual(selection.Historical, want) {
		t.Fatalf("historical = %+v", selection.Historical)
	}

	again := EvaluateSelection([]model.Artifact{second, third, first})
	if !reflect.DeepEqual(selection, again) {
		t.Fatalf("selection depends on input order:\nfirst=%+v\nsecond=%+v", selection, again)
	}
}

func TestSelectionFailsClosedOnCycleAndMissingTarget(t *testing.T) {
	oneURL := "https://example.test/issues/1#comment-1"
	twoURL := "https://example.test/issues/1#comment-2"
	one := processArtifact(t, "PROCESS-001", "superseded", oneURL, &model.SupersededBy{ProcessID: "PROCESS-002", URL: twoURL})
	two := processArtifact(t, "PROCESS-002", "superseded", twoURL, &model.SupersededBy{ProcessID: "PROCESS-001", URL: oneURL})
	selection := EvaluateSelection([]model.Artifact{one, two})
	if selection.Valid() || len(selection.Historical) != 0 || !reflect.DeepEqual(selection.ActiveProcessIDs, []string{"PROCESS-001", "PROCESS-002"}) {
		t.Fatalf("cycle did not fail closed: %+v", selection)
	}

	missing := processArtifact(t, "PROCESS-004", "superseded", "https://example.test/issues/1#comment-4", &model.SupersededBy{ProcessID: "PROCESS-099", URL: "https://example.test/issues/2#comment-99"})
	selection = EvaluateSelection([]model.Artifact{missing})
	if selection.Valid() || len(selection.Historical) != 0 || !reflect.DeepEqual(selection.ActiveProcessIDs, []string{"PROCESS-004"}) || selection.Diagnostics[0].Code != "missing-or-cross-change-target" {
		t.Fatalf("missing target did not fail closed: %+v", selection)
	}
}

func TestSelectionKeepsLegacySupersededProcessActiveAndBlocking(t *testing.T) {
	legacy := processArtifact(t, "PROCESS-001", "superseded", "https://example.test/issues/1#comment-1", nil)
	active := processArtifact(t, "PROCESS-002", "done", "https://example.test/issues/1#comment-2", nil)
	selection := Select([]model.Artifact{active, legacy})
	if !selection.Valid() || !reflect.DeepEqual(selection.ActiveProcessIDs, []string{"PROCESS-001", "PROCESS-002"}) ||
		!reflect.DeepEqual(selection.LegacySupersededProcessIDs, []string{"PROCESS-001"}) {
		t.Fatalf("legacy superseded status acquired replacement authority: %+v", selection)
	}
}

func processArtifact(t *testing.T, id, status, providerURL string, replacement *model.SupersededBy) model.Artifact {
	t.Helper()
	body, err := model.EnsureTypedBody("PROCESS", id, "## Process: test\n\n### Parent TASK\n\n- TASK-001", model.BodyOptions{Status: status})
	if err != nil {
		t.Fatal(err)
	}
	if replacement != nil {
		body, _, err = model.StampSupersededBy(body, id, *replacement)
		if err != nil {
			t.Fatal(err)
		}
	}
	return model.Artifact{URL: providerURL, Comment: model.ParseTypedComment(body)}
}
