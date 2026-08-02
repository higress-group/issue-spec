package model

import (
	"reflect"
	"strings"
	"testing"
)

func TestArtifactRefRequiresExactTypedProviderIdentity(t *testing.T) {
	body, err := EnsureTypedBody("PROCESS", "PROCESS-001", "## Process: work", BodyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Issue: 3, CommentID: 7,
		URL:    "https://issues.example/acme/widgets/issues/3#issuecomment-7/",
		APIURL: "https://issues.example/api/comments/7/", Comment: ParseTypedComment(body)}
	ref, err := artifact.Ref()
	if err != nil {
		t.Fatal(err)
	}
	if ref.Issue != 3 || ref.Type != "PROCESS" || ref.ID != "PROCESS-001" || ref.CommentID != 7 ||
		ref.URL != "https://issues.example/acme/widgets/issues/3#issuecomment-7" {
		t.Fatalf("unexpected ref: %+v", ref)
	}
	if got, want := ArtifactProviderURLs(artifact), []string{
		"https://issues.example/acme/widgets/issues/3#issuecomment-7",
		"https://issues.example/api/comments/7",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("urls=%v want %v", got, want)
	}
}

func TestArtifactRefRejectsInvalidIdentityAndURL(t *testing.T) {
	valid := ArtifactRef{Issue: 3, Type: "PROCESS", ID: "PROCESS-001", URL: "https://example.test/process/1"}
	for name, mutate := range map[string]func(*ArtifactRef){
		"missing issue": func(value *ArtifactRef) { value.Issue = 0 },
		"wrong type":    func(value *ArtifactRef) { value.Type = "TASK" },
		"unclean id":    func(value *ArtifactRef) { value.ID = " PROCESS-001" },
		"relative url":  func(value *ArtifactRef) { value.URL = "/process/1" },
		"user info":     func(value *ArtifactRef) { value.URL = "https://user@example.test/process/1" },
		"trailing slash": func(value *ArtifactRef) {
			value.URL += "/"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("invalid ref accepted: %+v", candidate)
			}
		})
	}

	body, err := EnsureTypedBody("PROCESS", "PROCESS-001", "## Process: work", BodyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	parsed := ParseTypedComment(strings.Replace(body, "ID: PROCESS-001", "ID: PROCESS-002", 1))
	if _, err := (Artifact{Issue: 3, URL: valid.URL, Comment: parsed}).Ref(); err == nil {
		t.Fatal("marker/header identity drift was accepted")
	}
}
