package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestBuildReviewSyncReportClassifiesRationaleFindingsAndChecks(t *testing.T) {
	rationale, err := model.RenderRationaleBody("Worker", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "why", "a.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 1, Body: rationale, Path: "a.go", Line: 10},
		{ID: 2, Body: "P1: fix this", Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
	}, []github.Comment{{ID: 3}}, github.CombinedStatus{Statuses: []github.Status{
		{Context: "license/cla", State: "success"},
		{Context: "ci/test", State: "failure"},
	}}, []github.CheckRun{
		{Name: "DCO", Status: "completed", Conclusion: "success"},
		{Name: "build", Status: "queued"},
	})
	if report.OK {
		t.Fatal("finding and failed/pending checks should block review sync")
	}
	if report.RationaleComments != 1 || len(report.ActionableFindings) != 1 || len(report.FailedChecks) != 1 || len(report.PendingChecks) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(report.BlockingFindings) != 1 {
		t.Fatalf("expected one blocking finding: %+v", report.BlockingFindings)
	}
	if report.ActionableFindings[0].Severity != "P1" {
		t.Fatalf("severity = %s", report.ActionableFindings[0].Severity)
	}
}

func TestBuildReviewSyncReportIncludesSessionMetadataDiagnostics(t *testing.T) {
	finding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: finding, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
	}, nil, github.CombinedStatus{}, nil)
	if len(report.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v", report.Diagnostics)
	}
	if report.Diagnostics[0].Code != "missing_session_metadata" || report.Diagnostics[0].Artifact != "FINDING/FINDING-001" {
		t.Fatalf("unexpected diagnostic: %+v", report.Diagnostics[0])
	}
}

func TestFirstFindingSummarySkipsSessionMetadata(t *testing.T) {
	body := `<!-- issue-spec:finding id=FINDING-001 severity=P1 process=PROCESS-001 spec=SPEC-001 status=open path=a.go line=1 version=1 -->
Agent: Review Agent
Agent Session ID: codex-session-123
Agent Session Source: CODEX_THREAD_ID
Type: FINDING
ID: FINDING-001

Fix the real bug.`
	if got := firstFindingSummary(body); got != "Fix the real bug." {
		t.Fatalf("summary = %q", got)
	}
}

func TestBuildReviewSyncReportP2FindingDoesNotBlock(t *testing.T) {
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: "P2: polish this before follow-up", Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
	}, nil, github.CombinedStatus{}, nil)
	if !report.OK {
		t.Fatalf("P2-only findings should not block review sync: %+v", report)
	}
	if len(report.ActionableFindings) != 1 || len(report.BlockingFindings) != 0 {
		t.Fatalf("unexpected finding classification: %+v", report)
	}
}

func TestBuildReviewSyncReportResolvedFindingReply(t *testing.T) {
	finding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this before merge.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := model.RenderFindingReplyBody("Review", "FINDING-001", "PROCESS-001", "resolved", "Re-checked; the fix satisfies the finding.")
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: finding, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
		{ID: 3, InReplyToID: 2, Body: reply, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r3"},
	}, nil, github.CombinedStatus{}, nil)
	if !report.OK {
		t.Fatalf("resolved finding should not block review sync: %+v", report)
	}
	if len(report.ActionableFindings) != 0 || len(report.BlockingFindings) != 0 || len(report.ResolvedFindings) != 1 {
		t.Fatalf("unexpected finding classification: %+v", report)
	}
}

func TestBuildReviewSyncReportWorkerReplyAloneDoesNotResolve(t *testing.T) {
	finding, err := model.RenderFindingBody("Review Agent", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this before merge.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	// A worker's own terminal reply must not clear a blocking finding; only the
	// owning review agent's re-check resolves it (SPEC-003).
	workerReply, err := model.RenderFindingReplyBody("Worker Agent", "FINDING-001", "PROCESS-001", "fixed", "Applied the fix.")
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: finding, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
		{ID: 3, InReplyToID: 2, Body: workerReply, HTMLURL: "https://github.com/o/r/pull/4#discussion_r3"},
	}, nil, github.CombinedStatus{}, nil)
	if report.OK {
		t.Fatalf("worker reply alone must keep the finding blocking: %+v", report)
	}
	if len(report.ResolvedFindings) != 0 || len(report.BlockingFindings) != 1 {
		t.Fatalf("unexpected finding classification: %+v", report)
	}
	if len(report.FindingReplies) != 1 || report.FindingReplies[0].Agent != "Worker Agent" {
		t.Fatalf("expected the worker fix reply to be exposed: %+v", report.FindingReplies)
	}
}

func TestBuildReviewSyncReportExposesRationaleOwner(t *testing.T) {
	rationale, err := model.RenderRationaleBodyWithSession("Worker Agent Gamma", "worker-session-9", "agent-session-parameter", "PROCESS-001", "SPEC-002", "https://github.com/o/r/issues/1#issuecomment-1", "This block implements owner exposure.", "a.go", 5)
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: rationale, Path: "a.go", Line: 5, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
	}, nil, github.CombinedStatus{}, nil)
	if report.RationaleComments != 1 || len(report.Rationales) != 1 {
		t.Fatalf("expected one exposed rationale: %+v", report.Rationales)
	}
	if report.Rationales[0].Agent != "Worker Agent Gamma" || report.Rationales[0].Process != "PROCESS-001" {
		t.Fatalf("rationale owner not recoverable: %+v", report.Rationales[0])
	}
}

func TestBuildReviewSyncReportExposesLogicalOwners(t *testing.T) {
	finding, err := model.RenderFindingBody("Review Agent Alpha", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this before merge.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	workerReply, err := model.RenderFindingReplyBody("Worker Agent Beta", "FINDING-001", "PROCESS-001", "fixed", "Applied the fix in the latest patch.")
	if err != nil {
		t.Fatal(err)
	}
	reviewResolution, err := model.RenderFindingReplyBody("Review Agent Alpha", "FINDING-001", "PROCESS-001", "resolved", "Re-checked the diff; the fix satisfies the finding.")
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: finding, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
		{ID: 3, InReplyToID: 2, Body: workerReply, HTMLURL: "https://github.com/o/r/pull/4#discussion_r3"},
		{ID: 4, InReplyToID: 2, Body: reviewResolution, HTMLURL: "https://github.com/o/r/pull/4#discussion_r4"},
	}, nil, github.CombinedStatus{}, nil)

	if len(report.ResolvedFindings) != 1 {
		t.Fatalf("expected one resolved finding, got %+v", report.ResolvedFindings)
	}
	resolved := report.ResolvedFindings[0]
	if resolved.Agent != "Review Agent Alpha" {
		t.Fatalf("expected finding owner Review Agent Alpha, got %q", resolved.Agent)
	}
	if resolved.ResolvedByAgent != "Review Agent Alpha" {
		t.Fatalf("expected resolution owner Review Agent Alpha, got %q", resolved.ResolvedByAgent)
	}
	if len(report.FindingReplies) != 2 {
		t.Fatalf("expected two finding replies, got %+v", report.FindingReplies)
	}
	if report.FindingReplies[0].Agent != "Worker Agent Beta" {
		t.Fatalf("expected fix-reply owner Worker Agent Beta, got %q", report.FindingReplies[0].Agent)
	}
	if report.FindingReplies[1].Agent != "Review Agent Alpha" {
		t.Fatalf("expected resolution-reply owner Review Agent Alpha, got %q", report.FindingReplies[1].Agent)
	}
}

func TestBuildReviewSyncReportDoesNotResolveDuplicateFindingIDAcrossThreads(t *testing.T) {
	firstFinding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this first issue.", "open", "a.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	secondFinding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-002", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this second issue.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := model.RenderFindingReplyBody("Review", "FINDING-001", "PROCESS-001", "resolved", "Re-checked only the first thread.")
	if err != nil {
		t.Fatal(err)
	}

	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: firstFinding, Path: "a.go", Line: 10, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
		{ID: 3, Body: secondFinding, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r3"},
		{ID: 4, InReplyToID: 2, Body: reply, Path: "a.go", Line: 10, HTMLURL: "https://github.com/o/r/pull/4#discussion_r4"},
	}, nil, github.CombinedStatus{}, nil)

	if report.OK {
		t.Fatalf("second duplicate finding should still block review sync: %+v", report)
	}
	if len(report.ResolvedFindings) != 1 || report.ResolvedFindings[0].CommentID != 2 {
		t.Fatalf("unexpected resolved findings: %+v", report.ResolvedFindings)
	}
	if len(report.BlockingFindings) != 1 || report.BlockingFindings[0].CommentID != 3 {
		t.Fatalf("unexpected blocking findings: %+v", report.BlockingFindings)
	}
}

func TestRenderReviewSyncComment(t *testing.T) {
	body, err := renderReviewSyncComment("REVIEW-001", "Coordinator", writerSession{}, "pr-review", "https://github.com/o/r/pull/4", reviewSyncReport{
		OK:                true,
		PR:                4,
		PRURL:             "https://github.com/o/r/pull/4",
		RationaleComments: 2,
		PassedChecks:      []reviewCheck{{Name: "DCO", State: "completed", Conclusion: "success"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Type: REVIEW", "ID: REVIEW-001", "Status: done", "Review sync passed", "DCO"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
}

func TestCreateReviewFindingIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := &fakeReviewClient{
		files: []github.PullRequestFile{{
			Filename: "internal/foo.go",
			Patch: `@@ -1,2 +1,3 @@
 package foo
+var X = 1
`,
		}},
		pr: github.PullRequest{Number: 7},
	}
	client.pr.Head.SHA = "abc123"
	result, err := createReviewFinding(ctx, client, "o/r", 7, "internal/foo.go", 2, "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Review Agent", writerSession{}, "Fix this.")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.CommentID == 0 || result.Severity != "P1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	result, err = createReviewFinding(ctx, client, "o/r", 7, "internal/foo.go", 2, "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Review Agent", writerSession{}, "Fix this.")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created {
		t.Fatalf("expected idempotent existing result: %+v", result)
	}
}

func TestCreateReviewFindingWithGHBackendUsesPatchLines(t *testing.T) {
	ctx := context.Background()
	runner := &commandSequenceRunner{results: []github.ExternalCLIResult{
		{Stdout: []byte(`[{"filename":"internal/foo.go","patch":"@@ -1,2 +1,3 @@\n package foo\n+var X = 1\n"}]`)},
		{Stdout: []byte(`[]`)},
		{Stdout: []byte(`{"number":7,"html_url":"https://github.com/o/r/pull/7","head":{"sha":"abc123","ref":"feature"},"base":{"ref":"main"}}`)},
		{Stdout: []byte(`{"id":100,"html_url":"https://github.com/o/r/pull/7#discussion_r100","body":"created","path":"internal/foo.go","line":2,"commit_id":"abc123"}`)},
	}}
	client := newCommandTestGHBackend(t, runner)

	result, err := createReviewFinding(ctx, client, "o/r", 7, "internal/foo.go", 2, "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Review Agent", writerSession{}, "Fix this.")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.CommentID != 100 || result.Severity != "P1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got, want := len(runner.commands), 4; got != want {
		t.Fatalf("gh commands = %d, want %d", got, want)
	}
}

func TestReplyReviewFindingIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := &fakeReviewClient{
		comments: []github.PullRequestReviewComment{{
			ID:      10,
			HTMLURL: "https://github.com/o/r/pull/7#discussion_r10",
			Body:    "P1: fix this",
			Path:    "internal/foo.go",
			Line:    2,
		}},
	}
	result, err := replyReviewFinding(ctx, client, "o/r", 7, 10, "FINDING-001", "PROCESS-001", "resolved", "Worker Agent", writerSession{}, "Fixed.")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.CommentID == 0 || result.ParentCommentID != 10 {
		t.Fatalf("unexpected result: %+v", result)
	}
	result, err = replyReviewFinding(ctx, client, "o/r", 7, 10, "FINDING-001", "PROCESS-001", "resolved", "Worker Agent", writerSession{}, "Fixed.")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created {
		t.Fatalf("expected idempotent existing reply: %+v", result)
	}
}

type fakeReviewClient struct {
	pr       github.PullRequest
	files    []github.PullRequestFile
	comments []github.PullRequestReviewComment
}

func (f *fakeReviewClient) GetPullRequest(context.Context, string, int) (github.PullRequest, error) {
	return f.pr, nil
}

func (f *fakeReviewClient) ListPullRequestFiles(context.Context, string, int) ([]github.PullRequestFile, error) {
	return f.files, nil
}

func (f *fakeReviewClient) ListPullRequestReviewComments(context.Context, string, int) ([]github.PullRequestReviewComment, error) {
	return f.comments, nil
}

func (f *fakeReviewClient) CreatePullRequestReviewComment(_ context.Context, _ string, _ int, body, commitID, path string, line int, side string) (github.PullRequestReviewComment, error) {
	if commitID == "" || path == "" || line == 0 || side != "RIGHT" {
		panic("invalid create review comment args")
	}
	marker, ok, err := model.FindFindingMarker(body)
	if err != nil || !ok || marker.ID != "FINDING-001" || marker.Severity != "P1" {
		panic("missing finding marker")
	}
	comment := github.PullRequestReviewComment{
		ID:       int64(len(f.comments) + 1),
		HTMLURL:  "https://github.com/o/r/pull/7#discussion_r1",
		Body:     body,
		Path:     path,
		Line:     line,
		CommitID: commitID,
	}
	f.comments = append(f.comments, comment)
	return comment, nil
}

func (f *fakeReviewClient) ReplyPullRequestReviewComment(_ context.Context, _ string, prNumber int, parentCommentID int64, body string) (github.PullRequestReviewComment, error) {
	if prNumber != 7 {
		panic("invalid pull request number")
	}
	marker, ok, err := model.FindFindingReplyMarker(body)
	if err != nil || !ok || marker.Finding != "FINDING-001" || marker.Status != "resolved" {
		panic("missing finding reply marker")
	}
	comment := github.PullRequestReviewComment{
		ID:          int64(len(f.comments) + 1),
		InReplyToID: parentCommentID,
		HTMLURL:     "https://github.com/o/r/pull/7#discussion_r2",
		Body:        body,
	}
	f.comments = append(f.comments, comment)
	return comment, nil
}
