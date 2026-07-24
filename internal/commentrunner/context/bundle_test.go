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

func TestBuildBundleReferenceOnlyOmitsBodiesButKeepsProvenance(t *testing.T) {
	content := "## Scope\n\nTASK body that should not be re-inlined on resume"
	task := typedArtifact(t, 25, 301, "TASK", "TASK-012", "ready", "resume-minimization", content)

	inlined, err := BuildBundle(BuildOptions{
		Command:   newCommand(),
		Runner:    newRunner(),
		Artifacts: []model.Artifact{task},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inlined.Artifacts[0].Content == "" || inlined.Artifacts[0].ReferenceOnly {
		t.Fatalf("expected inlined body on /new turn: %+v", inlined.Artifacts[0])
	}

	refOnly, err := BuildBundle(BuildOptions{
		Command:                newCommand(),
		Runner:                 newRunner(),
		Artifacts:              []model.Artifact{task},
		ReferenceOnlyArtifacts: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := refOnly.Artifacts[0]
	if !got.ReferenceOnly {
		t.Fatalf("expected reference_only flag: %+v", got)
	}
	if got.Content != "" || got.IncludedBytes != 0 {
		t.Fatalf("reference-only artifact must omit body: %+v", got)
	}
	if got.ContentSHA256 != inlined.Artifacts[0].ContentSHA256 {
		t.Fatalf("reference-only artifact must keep original content hash: %q != %q", got.ContentSHA256, inlined.Artifacts[0].ContentSHA256)
	}
	if got.ContentBytes != inlined.Artifacts[0].ContentBytes {
		t.Fatalf("reference-only artifact must keep original byte count: %d != %d", got.ContentBytes, inlined.Artifacts[0].ContentBytes)
	}
	if got.Trust != TrustUntrustedData || got.SourceLabel != SourceIssueSpecArtifact {
		t.Fatalf("reference-only artifact must keep trust metadata: %+v", got)
	}
	if got.ID != "TASK-012" || got.Type != "TASK" || got.URL == "" {
		t.Fatalf("reference-only artifact must keep pointer metadata: %+v", got)
	}
}

func TestBuildBundleFoldsPreviewSourceBeforeNewAndResumeSerialization(t *testing.T) {
	artifactSource := "## Scope\n\nvisible\n```html-preview id=artifact-preview version=1\nARTIFACT_HOSTILE\n```\n"
	artifact := typedArtifact(t, 24, 201, "SPEC", "SPEC-002", "confirmed", "preview-folding", artifactSource)
	command := newCommand()
	command.Prompt = "continue\n```html-preview id=prompt-preview version=1\nPROMPT_HOSTILE\n```\n"

	inlined, err := BuildBundle(BuildOptions{
		Command:   command,
		Runner:    newRunner(),
		Artifacts: []model.Artifact{artifact},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inlined.Command.Prompt, "PROMPT_HOSTILE") ||
		strings.Contains(inlined.Artifacts[0].Content, "ARTIFACT_HOSTILE") {
		t.Fatalf("new bundle leaked preview source: command=%q artifact=%q", inlined.Command.Prompt, inlined.Artifacts[0].Content)
	}
	if !strings.Contains(inlined.Command.Prompt, `"id":"prompt-preview"`) ||
		!strings.Contains(inlined.Artifacts[0].Content, `"id":"artifact-preview"`) {
		t.Fatalf("new bundle omission was not explicit: command=%q artifact=%q", inlined.Command.Prompt, inlined.Artifacts[0].Content)
	}
	if inlined.Artifacts[0].ContentSHA256 != sha256String(artifact.Comment.Body) ||
		inlined.Artifacts[0].ContentBytes != len([]byte(artifact.Comment.Body)) {
		t.Fatalf("original artifact provenance was not retained: %+v", inlined.Artifacts[0])
	}

	command.Verb = CommandResume
	command.PublicSessionID = "public-resume"
	resumed, err := BuildBundle(BuildOptions{
		Command:                command,
		Runner:                 newRunner(),
		Artifacts:              []model.Artifact{artifact},
		ReferenceOnlyArtifacts: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resumed.Command.Prompt, "PROMPT_HOSTILE") || resumed.Artifacts[0].Content != "" ||
		!resumed.Artifacts[0].ReferenceOnly {
		t.Fatalf("resume bundle accumulated rich source: command=%q artifact=%+v", resumed.Command.Prompt, resumed.Artifacts[0])
	}
}

func TestBuildBundleIncludesOnlyProviderSelectedEffectiveAnswerForNewAndResume(t *testing.T) {
	question := choiceQuestionArtifact(t, 24, 201, "QUESTION-001")
	snapshot, err := model.SnapshotQuestion(question.Comment.Body, question.URL)
	if err != nil {
		t.Fatal(err)
	}
	effective := choiceAnswerArtifact(t, 24, 203, "ANSWER-002", snapshot, "new")
	malformedBody, err := model.EnsureTypedBody("ANSWER", "ANSWER-003", "## Answer\n\n```json\n{bad-json}\n```\n",
		model.BodyOptions{Status: "done", Scope: "QUESTION-001"})
	if err != nil {
		t.Fatal(err)
	}
	malformed := model.Artifact{
		Issue: 24, CommentID: 204,
		URL:     "https://github.com/owner/repo/issues/24#issuecomment-204",
		Comment: model.ParseTypedComment(malformedBody),
	}
	wrongSnapshot := snapshot
	wrongSnapshot.SourceURL = "https://github.com/owner/repo/issues/24#issuecomment-999"
	wrongSource := choiceAnswerArtifact(t, 24, 205, "ANSWER-004", wrongSnapshot, "old")
	wrongQuestionScope := choiceAnswerArtifactWithScope(t, 24, 206, "ANSWER-005", snapshot, "old", "QUESTION-999")
	unrelatedSnapshot := snapshot
	unrelatedSnapshot.ID = "QUESTION-999"
	unrelatedSnapshot.SourceURL = "https://github.com/owner/repo/issues/24#issuecomment-998"
	unrelated := choiceAnswerArtifact(t, 24, 207, "ANSWER-006", unrelatedSnapshot, "old")

	command := newCommand()
	for _, test := range []struct {
		name          string
		command       CommandCandidate
		referenceOnly bool
	}{
		{name: "new", command: command},
		{name: "resume", command: func() CommandCandidate {
			resume := command
			resume.Verb = CommandResume
			resume.PublicSessionID = "public-resume"
			return resume
		}(), referenceOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bundle, err := BuildBundle(BuildOptions{
				Command:                test.command,
				Runner:                 newRunner(),
				Artifacts:              []model.Artifact{unrelated, malformed, effective, question, wrongSource, wrongQuestionScope},
				ReferenceOnlyArtifacts: test.referenceOnly,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(bundle.Artifacts) != 2 ||
				bundle.Artifacts[0].ID != "QUESTION-001" ||
				bundle.Artifacts[1].ID != "ANSWER-002" {
				t.Fatalf("Agent context artifacts = %+v", bundle.Artifacts)
			}
			if test.referenceOnly {
				if bundle.Artifacts[0].Content != "" || bundle.Artifacts[1].Content != "" {
					t.Fatalf("resume inlined artifacts: %+v", bundle.Artifacts)
				}
			} else if !strings.Contains(bundle.Artifacts[1].Content, `"id":"new"`) {
				t.Fatalf("new context omitted effective selection: %q", bundle.Artifacts[1].Content)
			}
		})
	}
}

func TestBuildBundleRefusesToChooseBetweenUnresolvedValidAnswerHistory(t *testing.T) {
	question := choiceQuestionArtifact(t, 24, 201, "QUESTION-001")
	snapshot, err := model.SnapshotQuestion(question.Comment.Body, question.URL)
	if err != nil {
		t.Fatal(err)
	}
	older := choiceAnswerArtifact(t, 24, 202, "ANSWER-001", snapshot, "old")
	newer := choiceAnswerArtifact(t, 24, 203, "ANSWER-002", snapshot, "new")

	_, err = BuildBundle(BuildOptions{
		Command:   newCommand(),
		Runner:    newRunner(),
		Artifacts: []model.Artifact{question, newer, older},
	})
	if err == nil || !strings.Contains(err.Error(), "lack provider effective-answer selection") {
		t.Fatalf("ambiguous ANSWER history error = %v", err)
	}
}

func choiceQuestionArtifact(t *testing.T, issue int, commentID int64, id string) model.Artifact {
	t.Helper()
	choice := model.ChoiceModel{
		Version: model.ChoiceModelVersion,
		Mode:    model.ChoiceModeSingle,
		Options: []model.ChoiceOption{
			{ID: "old", Label: "Older choice"},
			{ID: "new", Label: "Newer choice"},
		},
	}
	raw, err := model.CanonicalJSON(choice)
	if err != nil {
		t.Fatal(err)
	}
	body, err := model.EnsureTypedBody("QUESTION", id, "## Question\n\nChoose one.\n\n## Blocking\n\ntrue\n\n## Default Assumption\n\nOlder choice\n\n## Choice Model\n\n```json\n"+raw+"\n```\n",
		model.BodyOptions{Status: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{
		Issue: issue, CommentID: commentID,
		URL:     "https://github.com/owner/repo/issues/24#issuecomment-201",
		Comment: model.ParseTypedComment(body),
	}
}

func choiceAnswerArtifact(t *testing.T, issue int, commentID int64, id string, snapshot model.QuestionSnapshot, optionID string) model.Artifact {
	t.Helper()
	return choiceAnswerArtifactWithScope(t, issue, commentID, id, snapshot, optionID, snapshot.ID)
}

func choiceAnswerArtifactWithScope(t *testing.T, issue int, commentID int64, id string, snapshot model.QuestionSnapshot, optionID, scope string) model.Artifact {
	t.Helper()
	payload, err := model.BuildAnswerPayload(snapshot, []string{optionID}, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := model.CanonicalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	body, err := model.EnsureTypedBody("ANSWER", id, "## Answer\n\n```json\n"+raw+"\n```\n",
		model.BodyOptions{Status: "done", Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{
		Issue: issue, CommentID: commentID,
		URL:     "https://github.com/owner/repo/issues/24#issuecomment-" + id,
		Comment: model.ParseTypedComment(body),
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
