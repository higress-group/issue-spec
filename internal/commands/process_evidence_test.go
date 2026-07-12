package commands

import (
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
)

func TestArtifactReferencesRejectIDPrefixCollisions(t *testing.T) {
	process := model.Artifact{URL: "https://example/process-1", Comment: model.TypedComment{ID: "PROCESS-001"}}
	review := model.Artifact{Comment: model.TypedComment{Body: "Reviewed PROCESS-0010 and covered SPEC-0010.", Links: map[string][]string{}}}
	if artifactReferencesProcess(review, process) {
		t.Fatal("PROCESS-0010 must not satisfy PROCESS-001")
	}
	if artifactReferencesSpec(review, "SPEC-001", "https://example/spec-1") {
		t.Fatal("SPEC-0010 must not satisfy SPEC-001")
	}
}

func TestArtifactReferencesAcceptExactIDsAndCanonicalLinks(t *testing.T) {
	process := model.Artifact{URL: "https://example/process-1", Comment: model.TypedComment{ID: "PROCESS-001"}}
	review := model.Artifact{Comment: model.TypedComment{Body: "Reviewed PROCESS-001; covered SPEC-001.", Links: map[string][]string{}}}
	if !artifactReferencesProcess(review, process) || !artifactReferencesSpec(review, "SPEC-001", "https://example/spec-1") {
		t.Fatal("exact typed IDs should be accepted")
	}
	linked := model.Artifact{Comment: model.TypedComment{Links: map[string][]string{"Related Comments": {process.URL, "https://example/spec-1"}}}}
	if !artifactReferencesProcess(linked, process) || !artifactReferencesSpec(linked, "SPEC-001", "https://example/spec-1") {
		t.Fatal("canonical related links should be accepted")
	}
}
