package contextbundle

import (
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
)

func TestBuildBundleOrdersArtifactsAndLabelsSources(t *testing.T) {
	spec := typedArtifact(t, 24, 201, "SPEC", "SPEC-002", "confirmed", "auth-command-parsing", "## Requirement\n\nSPEC body")
	task := typedArtifact(t, 25, 301, "TASK", "TASK-012", "ready", "context-bundle-coordinator-contract", "## Scope\n\nTASK body")
	process := typedArtifact(t, 30, 401, "PROCESS", "NATIVE-007", "ready", "context-bundle-coordinator-contract", "## Scope\n\nPROCESS body")

	bundle, err := BuildBundle(BuildOptions{
		Command: newCommand(),
		Runner:  newRunner(),
		Artifacts: []model.Artifact{
			process,
			task,
			spec,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Command.SourceLabel != SourceAuthorizedCommand || bundle.Command.Trust != TrustRunnerProduced {
		t.Fatalf("unexpected command source/trust: %+v", bundle.Command)
	}
	if bundle.Runner.SourceLabel != SourceRunnerMetadata || bundle.Runner.Trust != TrustRunnerProduced {
		t.Fatalf("unexpected runner source/trust: %+v", bundle.Runner)
	}
	gotIDs := []string{bundle.Artifacts[0].ID, bundle.Artifacts[1].ID, bundle.Artifacts[2].ID}
	wantIDs := []string{"SPEC-002", "TASK-012", "NATIVE-007"}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("artifact order = %v, want %v", gotIDs, wantIDs)
		}
		if bundle.Artifacts[i].SourceLabel != SourceIssueSpecArtifact {
			t.Fatalf("artifact %d source = %q", i, bundle.Artifacts[i].SourceLabel)
		}
		if bundle.Artifacts[i].Trust != TrustUntrustedData {
			t.Fatalf("artifact %d trust = %q", i, bundle.Artifacts[i].Trust)
		}
		if bundle.Artifacts[i].ContentSHA256 == "" || bundle.Artifacts[i].IncludedSHA256 == "" {
			t.Fatalf("artifact %d missing hashes: %+v", i, bundle.Artifacts[i])
		}
	}

	reordered, err := BuildBundle(BuildOptions{
		Command:   newCommand(),
		Runner:    newRunner(),
		Artifacts: []model.Artifact{task, spec, process},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.BundleSHA256 != reordered.BundleSHA256 {
		t.Fatalf("bundle hash changed with input order: %s != %s", bundle.BundleSHA256, reordered.BundleSHA256)
	}
}

func TestBuildBundleTruncatesAndRedactsBoundedContent(t *testing.T) {
	secret := "SECRET_TOKEN"
	artifact := typedArtifact(t, 24, 201, "SPEC", "SPEC-002", "confirmed", "auth-command-parsing", strings.Repeat("A", 12)+secret+strings.Repeat("B", 12))
	bundle, err := BuildBundle(BuildOptions{
		Command: CommandCandidate{
			Authorized:        true,
			Verb:              CommandNew,
			Repo:              "owner/repo",
			Issue:             24,
			TriggerCommentID:  99,
			TriggerCommentURL: "https://github.com/owner/repo/issues/24#issuecomment-99",
			Commenter:         "alice",
			Prompt:            strings.Repeat("p", 12) + secret,
		},
		Runner: newRunner(),
		Artifacts: []model.Artifact{
			artifact,
		},
		Bounds: Bounds{
			MaxCommandPromptBytes: 10,
			MaxArtifactBytes:      18,
		},
		RedactionValues: []string{secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bundle.Command.PromptTruncated || !bundle.Command.PromptRedacted {
		t.Fatalf("expected prompt truncation and redaction: %+v", bundle.Command)
	}
	if len([]byte(bundle.Command.Prompt)) > 10 {
		t.Fatalf("prompt exceeded bound: %q", bundle.Command.Prompt)
	}
	if strings.Contains(bundle.Command.Prompt, secret) {
		t.Fatalf("prompt leaked secret: %q", bundle.Command.Prompt)
	}
	if !bundle.Artifacts[0].Truncated || !bundle.Artifacts[0].Redacted {
		t.Fatalf("expected artifact truncation and redaction: %+v", bundle.Artifacts[0])
	}
	if len([]byte(bundle.Artifacts[0].Content)) > 18 {
		t.Fatalf("artifact exceeded bound: %q", bundle.Artifacts[0].Content)
	}
	if strings.Contains(bundle.Artifacts[0].Content, secret) {
		t.Fatalf("artifact leaked secret: %q", bundle.Artifacts[0].Content)
	}
	if len(bundle.Truncations) < 2 {
		t.Fatalf("expected truncation metadata, got %+v", bundle.Truncations)
	}
	if len(bundle.Redactions) < 2 {
		t.Fatalf("expected redaction metadata, got %+v", bundle.Redactions)
	}
}

func newCommand() CommandCandidate {
	return CommandCandidate{
		Authorized:        true,
		Verb:              CommandNew,
		Repo:              "owner/repo",
		Issue:             24,
		TriggerCommentID:  99,
		TriggerCommentURL: "https://github.com/owner/repo/issues/24#issuecomment-99",
		Commenter:         "alice",
		IdempotencyKey:    "comment-99:first-observed",
		Prompt:            "create issue-spec workflow artifacts",
	}
}

func newRunner() RunnerMetadata {
	return RunnerMetadata{
		JobID:           "job-001",
		PublicSessionID: "s_001",
		Repo:            "owner/repo",
		Issue:           24,
		WorkspacePath:   "/workspace",
		Branch:          "main",
		Ref:             "refs/heads/main",
		AgentKind:       "codex",
		Model:           "gpt-5.5",
		IssueSpecBinary: "issue-spec",
		Constraints: []string{
			"Do not ask the coordinator to rediscover trigger comments.",
			"Workflow artifacts are written through issue-spec CLI.",
		},
	}
}

func typedArtifact(t *testing.T, issue int, commentID int64, commentType, id, status, scope, content string) model.Artifact {
	t.Helper()
	body, err := model.EnsureTypedBody(commentType, id, content, model.BodyOptions{
		Agent:  "Coordinator",
		Status: status,
		Scope:  scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{
		Issue:     issue,
		CommentID: commentID,
		URL:       "https://github.com/owner/repo/issues/1#issuecomment-" + id,
		APIURL:    "https://api.github.com/repos/owner/repo/issues/comments/1",
		Comment:   model.ParseTypedComment(body),
	}
}
