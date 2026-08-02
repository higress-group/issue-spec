package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
	"github.com/higress-group/issue-spec/internal/templates"
)

func TestReceiptReviewFindingUsesStableReceiptIdentityOnRetry(t *testing.T) {
	finding := assignment.Finding{ID: "FINDING-101", SpecID: "SPEC-002", OwnerProcessID: "PROCESS-102", Path: "internal/foo.go", Side: "RIGHT", Line: 2,
		Severity: "P1", Message: "Fix exact-revision behavior."}
	receipt := testSealedReviewReceipt(t, assignment.ReviewChangesRequested, []assignment.Finding{finding})
	client := &fakeReviewClient{files: []github.PullRequestFile{{Filename: finding.Path, Patch: "@@ -1,2 +1,3 @@\n package foo\n+var X = 1\n"}}}
	for run := 0; run < 2; run++ {
		result, err := createReceiptReviewFinding(t.Context(), client, "o/r", 7, receipt.SubjectRevision, finding,
			"https://github.com/o/r/issues/1#issuecomment-2", receipt)
		if err != nil {
			t.Fatal(err)
		}
		if result.Created != (run == 0) {
			t.Fatalf("run=%d result=%+v", run, result)
		}
	}
	if len(client.comments) != 1 || !strings.Contains(client.comments[0].Body, "Receipt Digest: "+receipt.ReceiptDigest) ||
		!strings.Contains(client.comments[0].Body, "Review Side: RIGHT") {
		t.Fatalf("stable finding carrier=%+v", client.comments)
	}
}

func TestSubmittedReviewCarriesOnlyCompactAcceptedReceiptAuthority(t *testing.T) {
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	coverage := reviewOwnerCoverage{Processes: []model.Artifact{{URL: "https://github.com/o/r/issues/9#issuecomment-10",
		Comment: model.TypedComment{Type: "PROCESS", ID: "PROCESS-101"}}}, Specs: []model.Artifact{{
		URL: "https://github.com/o/r/issues/9#issuecomment-2", Comment: model.TypedComment{Type: "SPEC", ID: "SPEC-002"}}}}
	body, err := renderSubmittedReviewWithCoverage("REVIEW-101", "PROCESS-101", "https://github.com/o/r/pull/7", coverage, receipt)
	if err != nil {
		t.Fatal(err)
	}
	parsed := model.ParseTypedComment(body)
	authority, found, err := parseAcceptedReviewReceipt(body)
	if err != nil || !found || parsed.Agent != receipt.Provenance.Writer || parsed.SubjectRevision != receipt.SubjectRevision ||
		parsed.Status != "done" || authority.ReceiptDigest != receipt.ReceiptDigest || len(authority.FindingIDs) != 0 {
		t.Fatalf("review=%+v authority=%+v found=%t err=%v", parsed, authority, found, err)
	}
	if !strings.Contains(body, "### Covered PROCESSes\n\n- PROCESS-101") ||
		!strings.Contains(body, "### Covered SPECs\n\n- SPEC-002") {
		t.Fatalf("production REVIEW omitted semantic owner coverage: %s", body)
	}
	for _, forbidden := range []string{"runtime-attested", "evaluation", "gate_forecast", "coordinator"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("submitted REVIEW leaked %q authority: %s", forbidden, body)
		}
	}
}

func TestAcceptedNoFindingReviewProvidesExactCurrentFinalEvidence(t *testing.T) {
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	prURL := "https://github.com/o/r/pull/7"
	body, err := renderSubmittedReview("REVIEW-101", "PROCESS-101", "https://github.com/o/r/issues/9#issuecomment-10",
		prURL, []string{"https://github.com/o/r/issues/9#issuecomment-2"}, receipt)
	if err != nil {
		t.Fatal(err)
	}
	artifact := model.Artifact{Comment: model.ParseTypedComment(body)}
	revision, trusted, source := reviewArtifactRevision(artifact, prURL, reviewSyncReport{PRURL: prURL,
		SubjectRevision: receipt.SubjectRevision, RevisionSource: "github-pr-head"}, nil, "PROCESS-101", "SPEC-002")
	if !trusted || revision != receipt.SubjectRevision || source != "github-pr-head" {
		t.Fatalf("exact no-finding evidence revision=%q trusted=%t source=%q", revision, trusted, source)
	}
	body, err = renderSubmittedReview("REVIEW-101", "PROCESS-101", "https://github.com/o/r/issues/9#issuecomment-10",
		"", []string{"https://github.com/o/r/issues/9#issuecomment-2"}, receipt)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Comment = model.ParseTypedComment(body)
	if _, trusted, _ := reviewArtifactRevision(artifact, prURL, reviewSyncReport{PRURL: prURL,
		SubjectRevision: receipt.SubjectRevision, RevisionSource: "github-pr-head"}, nil, "PROCESS-101", "SPEC-002"); trusted {
		t.Fatal("accepted no-finding REVIEW without exact PR relationship was trusted")
	}
}

func TestPublishAcceptedReviewIsAppendOnlyUnderConcurrentReceipt(t *testing.T) {
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	body, err := renderSubmittedReview("REVIEW-101", "PROCESS-101", "https://github.com/o/r/issues/9#issuecomment-10",
		"https://github.com/o/r/pull/7", []string{"https://github.com/o/r/issues/9#issuecomment-2"}, receipt)
	if err != nil {
		t.Fatal(err)
	}
	other := receipt
	other.ID = "receipt-review-concurrent"
	other.ReceiptDigest = ""
	other, err = assignment.SealReceipt(other)
	if err != nil {
		t.Fatal(err)
	}
	otherBody, err := renderSubmittedReview("REVIEW-102", "PROCESS-101", "https://github.com/o/r/issues/9#issuecomment-10",
		"https://github.com/o/r/pull/7", []string{"https://github.com/o/r/issues/9#issuecomment-2"}, other)
	if err != nil {
		t.Fatal(err)
	}
	comments := []github.Comment{}
	creates := 0
	backend := fakeGitHubBackend{
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return append([]github.Comment(nil), comments...), nil
		},
		createComment: func(_ context.Context, _ string, _ int, submitted string) (github.Comment, error) {
			creates++
			created := github.Comment{ID: 11, Body: submitted}
			comments = append(comments, created, github.Comment{ID: 12, Body: otherBody})
			return created, nil
		},
	}
	if err := validateExistingReviewReceipt(nil, "REVIEW-101", "PROCESS-101", receipt); err != nil {
		t.Fatal(err)
	}
	tamperedBody := strings.Replace(body, `"assignment_process_id":"PROCESS-101"`,
		`"assignment_process_id":"PROCESS-999"`, 1)
	comments = []github.Comment{{ID: 10, Body: tamperedBody}}
	if _, _, err := publishAcceptedReview(t.Context(), backend, "o/r", 9, "REVIEW-101", "PROCESS-101", body, receipt); err == nil ||
		!strings.Contains(err.Error(), "immutable") || creates != 0 {
		t.Fatalf("tampered exact retry creates=%d err=%v", creates, err)
	}
	comments = []github.Comment{{ID: 12, Body: otherBody}}
	if _, _, err := publishAcceptedReview(t.Context(), backend, "o/r", 9, "REVIEW-101", "PROCESS-101", body, receipt); err == nil ||
		!strings.Contains(err.Error(), "different receipt") || creates != 0 {
		t.Fatalf("fresh competing observation creates=%d err=%v", creates, err)
	}
	comments = nil
	if _, _, err := publishAcceptedReview(t.Context(), backend, "o/r", 9, "REVIEW-101", "PROCESS-101", body, receipt); err == nil ||
		!strings.Contains(err.Error(), "conflicted") || creates != 1 {
		t.Fatalf("concurrent publish creates=%d err=%v", creates, err)
	}
	comments = []github.Comment{{ID: 11, Body: body}}
	creates = 0
	action, existing, err := publishAcceptedReview(t.Context(), backend, "o/r", 9, "REVIEW-101", "PROCESS-101", body, receipt)
	if err != nil || action != "unchanged" || existing.ID != 11 || creates != 0 {
		t.Fatalf("exact replay action=%q existing=%+v creates=%d err=%v", action, existing, creates, err)
	}
}

func TestRunReviewSubmitNoFindingAndExactRevision(t *testing.T) {
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	receipt.SubjectRevision = strings.Repeat("b", 40)
	sealedAssignment := testReviewAssignment(t, receipt.SubjectRevision)
	sealedAssignment.Issue = 297
	assignmentDigest, err := assignment.AssignmentDigest(sealedAssignment)
	if err != nil {
		t.Fatal(err)
	}
	receipt.AssignmentDigest = assignmentDigest
	receipt.ReceiptDigest = ""
	receipt, err = assignment.SealReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	binding := &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: receipt.AssignmentID, Digest: receipt.AssignmentDigest, Role: assignment.RoleReview,
		SubjectRevision: receipt.SubjectRevision, Generation: receipt.AssignmentGeneration}
	now := time.Date(2026, 7, 19, 1, 0, 0, 0, time.UTC)
	workspace := model.ProcessWorkspace{SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "review-process-101",
		Repository: "o/r", ProcessID: "PROCESS-101", ExecutionClass: processworkspace.ExecutionReview,
		Mode: processworkspace.ModeSnapshot, BaseSHA: receipt.SubjectRevision, DetachedRevision: receipt.SubjectRevision,
		RuntimeNamespace: "review-process-101", Assignment: binding, State: processworkspace.StatePrepared,
		CreatedAt: now, UpdatedAt: now}
	processBody, err := templates.ProcessComment(templates.ProcessCommentOptions{
		Common: templates.CommonOptions{ID: "PROCESS-101", Status: "in-progress"}, Input: templates.ProcessInput{
			Title: "review exact receipt", Owner: "Independent Reviewer", ParentTask: "TASK-006",
			ExecutionClass: model.ProcessExecutionReview, WorkspaceManagement: model.ProcessWorkspaceManaged,
			Workspace: &workspace, Covers: []string{"SPEC-002"}, Handoff: "N/A"}})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(receipt)
	resultPath := filepath.Join(t.TempDir(), "review-receipt.json")
	if err := os.WriteFile(resultPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	assignmentPayload, _ := json.Marshal(sealedAssignment)
	assignmentPath := filepath.Join(t.TempDir(), "review-assignment.json")
	if err := os.WriteFile(assignmentPath, assignmentPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	specBody, err := templates.SpecComment(templates.SpecCommentOptions{Common: templates.CommonOptions{ID: "SPEC-002", Status: "confirmed"},
		Input: templates.SpecInput{Requirement: templates.SpecRequirementInput{Title: "exact review", Text: "Review evidence MUST be exact."},
			Scenarios: []templates.SpecScenarioInput{{Title: "current PR", When: "reviewed", Then: "evidence is exact"}}}})
	if err != nil {
		t.Fatal(err)
	}
	const proposalSpecURL = "https://github.com/o/r/issues/295#issuecomment-2"
	comments := []github.Comment{
		{ID: 10, HTMLURL: "https://github.com/o/r/issues/297#issuecomment-10", Body: processBody},
		{ID: 12, HTMLURL: "https://github.com/o/r/issues/297#issuecomment-12",
			Body: reviewCoverageProcessBody(t, "PROCESS-102", model.ProcessExecutionChangeBearing, "SPEC-002")},
	}
	proposalCommentsByIssue := map[int][]github.Comment{295: {{ID: 2, HTMLURL: proposalSpecURL, Body: specBody}}}
	changeIssues := map[int]github.Issue{
		295: {Number: 295, HTMLURL: "https://github.com/o/r/issues/295", Body: "<!-- issue-spec:issue=proposal change=review-change version=1 -->"},
		296: {Number: 296, HTMLURL: "https://github.com/o/r/issues/296", Body: "<!-- issue-spec:issue=design change=review-change version=1 -->\n- Proposal Issue: 295"},
		297: {Number: 297, HTMLURL: "https://github.com/o/r/issues/297", Body: "<!-- issue-spec:issue=implement change=review-change version=1 -->\n- Design Issue: 296"},
		305: {Number: 305, HTMLURL: "https://github.com/o/r/issues/305", Body: "<!-- issue-spec:issue=proposal change=review-change version=1 -->"},
		306: {Number: 306, HTMLURL: "https://github.com/o/r/issues/306", Body: "<!-- issue-spec:issue=design change=unrelated-change version=1 -->"},
		307: {Number: 307, HTMLURL: "https://github.com/o/r/issues/307", Body: "<!-- issue-spec:issue=implement change=unrelated-change version=1 -->"},
	}
	changeLinks := map[int][]github.Comment{
		295: {{ID: 90, Body: "https://github.com/o/r/issues/296#issuecomment-90"}},
		296: {{ID: 91, Body: "https://github.com/o/r/issues/297#issuecomment-91"}},
		305: {{ID: 92, Body: "https://github.com/o/r/issues/296#issuecomment-92 https://github.com/o/r/issues/297#issuecomment-93"}},
		306: {{ID: 93, Body: "https://github.com/o/r/issues/307#issuecomment-93"}},
	}
	created := 0
	head := receipt.SubjectRevision
	reviewClient := &fakeReviewClient{}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		backend := fakeGitHubBackend{info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			getIssue: func(_ context.Context, _ string, issue int) (github.Issue, error) {
				item, ok := changeIssues[issue]
				if !ok {
					return github.Issue{}, errors.New("unexpected issue lookup")
				}
				return item, nil
			},
			listIssueComments: func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
				items := append([]github.Comment(nil), proposalCommentsByIssue[issue]...)
				if issue == 297 {
					items = append(items, comments...)
				}
				items = append(items, changeLinks[issue]...)
				return items, nil
			}, getPullRequest: func(context.Context, string, int) (github.PullRequest, error) {
				pr := github.PullRequest{Number: 7, HTMLURL: "https://github.com/o/r/pull/7"}
				pr.Head.SHA = head
				return pr, nil
			}, createComment: func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
				created++
				comment := github.Comment{ID: 11, HTMLURL: "https://github.com/o/r/issues/297#issuecomment-11", Body: body}
				comments = append(comments, comment)
				return comment, nil
			}}
		return reviewSubmitCommandBackend{fakeGitHubBackend: backend, review: reviewClient}, nil
	}
	code := app.runReviewSubmit(t.Context(), []string{"--repo", "o/r", "--implement", "297", "--pr", "7",
		"--process", "PROCESS-101", "--id", "REVIEW-101", "--result-file", resultPath, "--assignment-file", assignmentPath})
	if code != 0 || created != 1 || len(comments) != 3 {
		t.Fatalf("exit=%d created=%d comments=%d out=%q err=%q", code, created, len(comments), out.String(), errOut.String())
	}
	parsed := model.ParseTypedComment(comments[2].Body)
	if parsed.Agent != "Independent Reviewer" || parsed.SubjectRevision != receipt.SubjectRevision ||
		!strings.Contains(comments[2].Body, "### Findings\n\n- None.") ||
		!linksContainURL(parsed.Links["PR"], "https://github.com/o/r/pull/7") ||
		!linksContainURL(parsed.Links["Related Comments"], proposalSpecURL) ||
		!strings.Contains(comments[2].Body, "### Covered PROCESSes\n\n- PROCESS-101\n- PROCESS-102") {
		t.Fatalf("submitted no-finding REVIEW=%+v body=%s", parsed, comments[2].Body)
	}
	head = strings.Repeat("c", 40)
	out.Reset()
	errOut.Reset()
	if code := app.runReviewSubmit(t.Context(), []string{"--repo", "o/r", "--implement", "297", "--pr", "7",
		"--process", "PROCESS-101", "--id", "REVIEW-101", "--result-file", resultPath, "--assignment-file", assignmentPath}); code != 1 ||
		!strings.Contains(errOut.String(), "exact current PR revision") || created != 1 {
		t.Fatalf("stale exit=%d created=%d err=%q", code, created, errOut.String())
	}

	finding := assignment.Finding{ID: "FINDING-101", SpecID: "SPEC-002", OwnerProcessID: "PROCESS-102", Path: "internal/foo.go", Side: "RIGHT", Line: 2,
		Severity: "P1", Message: "Fix exact-revision behavior."}
	findingReceipt := testSealedReviewReceipt(t, assignment.ReviewChangesRequested, []assignment.Finding{finding})
	findingReceipt.SubjectRevision = receipt.SubjectRevision
	findingReceipt.AssignmentDigest = assignmentDigest
	findingReceipt.ReceiptDigest = ""
	findingReceipt, err = assignment.SealReceipt(findingReceipt)
	if err != nil {
		t.Fatal(err)
	}
	workspace.Assignment = &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
		AssignmentID: findingReceipt.AssignmentID, Digest: findingReceipt.AssignmentDigest, Role: assignment.RoleReview,
		SubjectRevision: findingReceipt.SubjectRevision, Generation: findingReceipt.AssignmentGeneration}
	processBody, err = templates.ProcessComment(templates.ProcessCommentOptions{
		Common: templates.CommonOptions{ID: "PROCESS-101", Status: "in-progress"}, Input: templates.ProcessInput{
			Title: "review exact receipt", Owner: "Independent Reviewer", ParentTask: "TASK-006",
			ExecutionClass: model.ProcessExecutionReview, WorkspaceManagement: model.ProcessWorkspaceManaged,
			Workspace: &workspace, Covers: []string{"SPEC-002"}, Handoff: "N/A"}})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ = json.Marshal(findingReceipt)
	if err := os.WriteFile(resultPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	ownerBody, err := templates.ProcessComment(templates.ProcessCommentOptions{Common: templates.CommonOptions{ID: "PROCESS-102", Status: "in-progress"},
		Input: templates.ProcessInput{Title: "repair finding", Owner: "Worker", ParentTask: "TASK-006",
			ExecutionClass: model.ProcessExecutionChangeBearing, WriteOwnership: []string{"internal/foo.go"},
			Covers: []string{"SPEC-002"}, Handoff: "repair review finding"}})
	if err != nil {
		t.Fatal(err)
	}
	proposalComments := []github.Comment{{ID: 2, HTMLURL: proposalSpecURL, Body: specBody}}
	proposalCommentsByIssue = map[int][]github.Comment{295: proposalComments}
	comments = []github.Comment{
		{ID: 10, HTMLURL: "https://github.com/o/r/issues/297#issuecomment-10", Body: processBody},
		{ID: 12, HTMLURL: "https://github.com/o/r/issues/297#issuecomment-12", Body: ownerBody},
	}
	implementComments := append([]github.Comment(nil), comments...)
	reviewClient = &fakeReviewClient{files: []github.PullRequestFile{{Filename: finding.Path,
		Patch: "@@ -1,2 +1,3 @@\n package foo\n+var X = 1\n"}}}
	head = findingReceipt.SubjectRevision
	for _, test := range []struct {
		name             string
		proposal         string
		specURL          string
		proposalComments []github.Comment
		want             string
	}{
		{name: "wrong issue", proposal: "296", specURL: proposalSpecURL, proposalComments: proposalComments, want: "differs from canonical proposal issue 295"},
		{name: "duplicate same-key proposal with same SPEC", proposal: "305",
			specURL:          "https://github.com/o/r/issues/305#issuecomment-2",
			proposalComments: []github.Comment{{ID: 2, HTMLURL: "https://github.com/o/r/issues/305#issuecomment-2", Body: specBody}},
			want:             "differs from canonical proposal issue 295"},
		{name: "wrong spec", proposal: "295", specURL: proposalSpecURL,
			proposalComments: []github.Comment{{ID: 2, HTMLURL: proposalSpecURL, Body: strings.ReplaceAll(specBody, "SPEC-002", "SPEC-003")}}, want: "not one canonical"},
		{name: "noncanonical", proposal: "295", specURL: proposalSpecURL,
			proposalComments: []github.Comment{{ID: 2, HTMLURL: proposalSpecURL, Body: strings.ReplaceAll(specBody, "### Scenario:", "### Example:")}}, want: "not one canonical"},
		{name: "missing", proposal: "295", specURL: proposalSpecURL, want: "not one canonical"},
		{name: "ambiguous", proposal: "295", specURL: proposalSpecURL,
			proposalComments: []github.Comment{{ID: 2, HTMLURL: proposalSpecURL, Body: specBody},
				{ID: 3, HTMLURL: "https://github.com/o/r/issues/295#issuecomment-3", Body: specBody}}, want: "not one canonical"},
		{name: "wrong explicit URL", proposal: "295", specURL: "https://github.com/o/r/issues/296#issuecomment-2",
			proposalComments: proposalComments, want: "--spec-url conflicts"},
	} {
		t.Run("finding target "+test.name, func(t *testing.T) {
			comments = append([]github.Comment(nil), implementComments...)
			proposalIssue, parseErr := strconv.Atoi(test.proposal)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			proposalCommentsByIssue = map[int][]github.Comment{proposalIssue: append([]github.Comment(nil), test.proposalComments...)}
			reviewClient.comments = nil
			out.Reset()
			errOut.Reset()
			args := []string{"--repo", "o/r", "--proposal", test.proposal, "--implement", "297", "--pr", "7",
				"--process", "PROCESS-101", "--id", "REVIEW-101", "--result-file", resultPath, "--assignment-file", assignmentPath,
				"--spec", "SPEC-002", "--spec-url", test.specURL}
			if code := app.runReviewSubmit(t.Context(), args); code == 0 || created != 1 || len(reviewClient.comments) != 0 ||
				!strings.Contains(errOut.String(), test.want) {
				t.Fatalf("exit=%d created=%d findings=%d err=%q", code, created, len(reviewClient.comments), errOut.String())
			}
		})
	}
	comments = append([]github.Comment(nil), implementComments...)
	proposalCommentsByIssue = map[int][]github.Comment{295: {{ID: 2, HTMLURL: proposalSpecURL, Body: specBody}}}
	reviewClient.comments = nil
	out.Reset()
	errOut.Reset()
	if code := app.runReviewSubmit(t.Context(), []string{"--repo", "o/r", "--proposal", "295", "--implement", "297", "--pr", "7",
		"--process", "PROCESS-101", "--id", "REVIEW-101", "--result-file", resultPath, "--assignment-file", assignmentPath,
		"--spec", "SPEC-002", "--spec-url", proposalSpecURL}); code != 0 ||
		len(reviewClient.comments) != 1 || created != 2 {
		t.Fatalf("finding exit=%d created=%d findings=%d err=%q", code, created, len(reviewClient.comments), errOut.String())
	}
	marker, found, err := model.FindFindingMarker(reviewClient.comments[0].Body)
	if err != nil || !found || marker.Process != finding.OwnerProcessID || marker.Spec != finding.SpecID {
		t.Fatalf("sealed finding routing marker=%+v found=%t err=%v", marker, found, err)
	}
}

func TestSubmittedReviewTargetsResolveCanonicalProposalBackwardFromImplement(t *testing.T) {
	const specURL = "https://github.com/o/r/issues/295#issuecomment-2"
	specBody, err := templates.SpecComment(templates.SpecCommentOptions{
		Common: templates.CommonOptions{ID: "SPEC-002", Status: "confirmed"},
		Input: templates.SpecInput{Requirement: templates.SpecRequirementInput{Title: "related review", Text: "Review routing MUST use authority."},
			Scenarios: []templates.SpecScenarioInput{{Title: "related proposal", When: "a review is submitted", Then: "the SPEC is resolved exactly"}}}})
	if err != nil {
		t.Fatal(err)
	}
	processBody, err := model.EnsureTypedBody("PROCESS", "PROCESS-101", "## Process: related review", model.BodyOptions{
		Status: "in-progress", Links: map[string][]string{"Related Comments": {"https://github.com/o/r/issues/305#issuecomment-2"}}})
	if err != nil {
		t.Fatal(err)
	}
	process := model.Artifact{Issue: 297, CommentID: 10, URL: "https://github.com/o/r/issues/297#issuecomment-10",
		Comment: model.ParseTypedComment(processBody)}
	changeIssues := map[int]github.Issue{
		295: {Number: 295, HTMLURL: "https://github.com/o/r/issues/295", Body: "<!-- issue-spec:issue=proposal change=role-receipts version=1 -->"},
		296: {Number: 296, HTMLURL: "https://github.com/o/r/issues/296", Body: "<!-- issue-spec:issue=design change=role-receipts version=1 -->\n- Proposal Issue: 295"},
		297: {Number: 297, HTMLURL: "https://github.com/o/r/issues/297", Body: "<!-- issue-spec:issue=implement change=role-receipts version=1 -->\n- Design Issue: 296"},
		305: {Number: 305, HTMLURL: "https://github.com/o/r/issues/305", Body: "<!-- issue-spec:issue=proposal change=role-receipts version=1 -->"},
	}
	backend := fakeGitHubBackend{
		getIssue: func(_ context.Context, _ string, issue int) (github.Issue, error) {
			item, ok := changeIssues[issue]
			if !ok {
				return github.Issue{}, errors.New("unexpected issue lookup")
			}
			return item, nil
		},
		listIssueComments: func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
			switch issue {
			case 295:
				return []github.Comment{{ID: 2, HTMLURL: specURL, Body: specBody}}, nil
			case 305:
				return []github.Comment{{ID: 2, HTMLURL: "https://github.com/o/r/issues/305#issuecomment-2", Body: specBody},
					{ID: 30, Body: "https://github.com/o/r/issues/296#issuecomment-30 https://github.com/o/r/issues/297#issuecomment-31"}}, nil
			default:
				return nil, nil
			}
		},
	}
	sources, err := loadSubmittedReviewSpecSources(t.Context(), backend, "o/r", 0, 297, process, nil)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := findUniqueSubmittedReviewSpec(sources, "SPEC-002")
	if err != nil || artifact.Issue != 295 || artifact.URL != specURL {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}

	process.Comment.Links["Related Comments"] = []string{"https://github.com/o/r/issues/305#issuecomment-2"}
	sources, err = loadSubmittedReviewSpecSources(t.Context(), backend, "o/r", 0, 297, process, nil)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err = findUniqueSubmittedReviewSpec(sources, "SPEC-002")
	if err != nil || artifact.Issue != 295 || artifact.URL != specURL {
		t.Fatalf("process-carried duplicate proposal changed root artifact=%+v err=%v", artifact, err)
	}

	if _, err := loadSubmittedReviewSpecSources(t.Context(), backend, "o/r", 305, 297, process, nil); err == nil ||
		!strings.Contains(err.Error(), "differs from canonical proposal issue 295") {
		t.Fatalf("duplicate same-key proposal err=%v", err)
	}
}

type reviewSubmitCommandBackend struct {
	fakeGitHubBackend
	review *fakeReviewClient
}

func (b reviewSubmitCommandBackend) ListPullRequestFiles(ctx context.Context, repo string, pr int) ([]github.PullRequestFile, error) {
	return b.review.ListPullRequestFiles(ctx, repo, pr)
}

func (b reviewSubmitCommandBackend) ListPullRequestReviewComments(ctx context.Context, repo string, pr int) ([]github.PullRequestReviewComment, error) {
	return b.review.ListPullRequestReviewComments(ctx, repo, pr)
}

func (b reviewSubmitCommandBackend) CreatePullRequestReviewComment(ctx context.Context, repo string, pr int,
	body, revision, path string, line int, side string) (github.PullRequestReviewComment, error) {
	return b.review.CreatePullRequestReviewComment(ctx, repo, pr, body, revision, path, line, side)
}

func TestExternalReviewSyncIsForcedAndIdempotent(t *testing.T) {
	app, native, comments, creates, updates, out, errOut := setupExternalReviewSyncCommand(t)
	args := []string{"--repo", "acme/widgets", "--hostname", "issues.test", "--implement", "9",
		"--revision", "head-abc", "--id", "REVIEW-101", "--agent", "Review Agent", "--agent-session", "review-session-7"}
	var firstCompletion externalReviewCompletion
	for run := 1; run <= 2; run++ {
		out.Reset()
		errOut.Reset()
		if code := app.runReviewSync(t.Context(), args); code != 0 {
			t.Fatalf("run %d exit=%d stdout=%q stderr=%q", run, code, out.String(), errOut.String())
		}
		completion, found, err := parseExternalReviewCompletion((*comments)[0].Body)
		if err != nil || !found {
			t.Fatalf("run %d completion found=%t err=%v body=%q", run, found, err, (*comments)[0].Body)
		}
		if run == 1 {
			firstCompletion = completion
			linked, _, linkErr := model.AddRelatedCommentLink((*comments)[0].Body,
				"https://issues.test/acme/widgets/issues/9#issuecomment-process")
			if linkErr != nil {
				t.Fatal(linkErr)
			}
			(*comments)[0].Body = linked
		} else if !completion.SynchronizedAt.After(firstCompletion.SynchronizedAt) {
			t.Fatalf("completion timestamp did not advance: first=%s second=%s", firstCompletion.SynchronizedAt, completion.SynchronizedAt)
		}
	}
	if *creates != 1 || *updates != 1 || len(*comments) != 1 || native.syncs != 2 || native.resolveCalls != 4 {
		t.Fatalf("creates=%d updates=%d comments=%d syncs=%d resolves=%d", *creates, *updates, len(*comments),
			native.syncs, native.resolveCalls)
	}
	parsed := model.ParseTypedComment((*comments)[0].Body)
	if parsed.Type != "REVIEW" || parsed.ID != "REVIEW-101" || parsed.Status != "done" ||
		parsed.Agent != "Review Agent" || parsed.AgentSessionID != "" ||
		parsed.AgentSessionSource != "" || parsed.SubjectRevision != "head-abc" ||
		strings.Count((*comments)[0].Body, externalReviewCompletionStart) != 1 ||
		!linksContainURL(parsed.Links["Related Comments"], "https://issues.test/acme/widgets/issues/9#issuecomment-process") ||
		!linksContainURL(parsed.Links["Related Comments"], "https://issues.test/acme/widgets/issues/9#issuecomment-review-process") ||
		!linksContainURL(parsed.Links["Related Comments"], "https://issues.test/acme/widgets/issues/7#issuecomment-spec") ||
		!strings.Contains((*comments)[0].Body, "### Covered PROCESSes") ||
		!strings.Contains((*comments)[0].Body, "### Covered SPECs") ||
		!strings.Contains((*comments)[0].Body, `"subject_revision":"head-abc"`) {
		t.Fatalf("persisted REVIEW=%+v", parsed)
	}
}

func TestGitHubReviewSyncPublishesCompleteOwnerCoverageOnce(t *testing.T) {
	specBody, err := templates.SpecComment(templates.SpecCommentOptions{Common: templates.CommonOptions{
		ID: "SPEC-001", Status: "confirmed"}, Input: templates.SpecInput{Requirement: templates.SpecRequirementInput{
		Title: "GitHub sync", Text: "GitHub review sync MUST publish complete owner coverage."}, Scenarios: []templates.SpecScenarioInput{{
		Title: "single write", When: "review converges", Then: "only REVIEW is written"}}}})
	if err != nil {
		t.Fatal(err)
	}
	static := []github.Comment{
		{ID: 61, HTMLURL: "https://github.com/o/r/issues/9#issuecomment-61",
			Body: reviewCoverageProcessBody(t, "PROCESS-900", model.ProcessExecutionReview, "SPEC-001")},
		{ID: 62, HTMLURL: "https://github.com/o/r/issues/9#issuecomment-62",
			Body: reviewCoverageProcessBody(t, "PROCESS-001", model.ProcessExecutionChangeBearing, "SPEC-001")},
	}
	var published []github.Comment
	creates, updates := 0, 0
	backend := reviewSyncGitHubBackend{fakeGitHubBackend: fakeGitHubBackend{
		info: github.BackendInfo{Name: "gh", Kind: "external-cli", Host: "github.com"},
		getIssue: func(_ context.Context, _ string, issue int) (github.Issue, error) {
			items := map[int]github.Issue{
				7: {Number: 7, HTMLURL: "https://github.com/o/r/issues/7", Body: "<!-- issue-spec:issue=proposal change=github-review version=1 -->"},
				8: {Number: 8, HTMLURL: "https://github.com/o/r/issues/8", Body: "<!-- issue-spec:issue=design change=github-review version=1 -->\n- Proposal Issue: 7"},
				9: {Number: 9, HTMLURL: "https://github.com/o/r/issues/9", Body: "<!-- issue-spec:issue=implement change=github-review version=1 -->\n- Design Issue: 8"},
			}
			return items[issue], nil
		},
		listIssueComments: func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
			switch issue {
			case 7:
				return []github.Comment{{ID: 51, HTMLURL: "https://github.com/o/r/issues/7#issuecomment-51", Body: specBody}}, nil
			case 9:
				return append(append([]github.Comment(nil), static...), published...), nil
			default:
				return nil, nil
			}
		},
		getPullRequest: func(context.Context, string, int) (github.PullRequest, error) {
			pr := github.PullRequest{Number: 42, HTMLURL: "https://github.com/o/r/pull/42"}
			pr.Head.SHA = "head-abc"
			return pr, nil
		},
		listPRReviewComments: func(context.Context, string, int) ([]github.PullRequestReviewComment, error) { return nil, nil },
		createComment: func(_ context.Context, _ string, issue int, body string) (github.Comment, error) {
			if issue != 9 {
				t.Fatalf("owner publication issue=%d", issue)
			}
			creates++
			comment := github.Comment{ID: 71, HTMLURL: "https://github.com/o/r/issues/9#issuecomment-71", Body: body}
			published = append(published, comment)
			return comment, nil
		},
		updateComment: func(context.Context, string, int64, string) (github.Comment, error) {
			updates++
			return github.Comment{}, nil
		},
	}}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	code := app.runReviewSync(t.Context(), []string{"--repo", "o/r", "--implement", "9", "--pr", "42",
		"--process", "PROCESS-900", "--id", "REVIEW-900", "--agent", "Independent Reviewer", "--json"})
	if code != 0 || creates != 1 || updates != 0 || len(published) != 1 {
		t.Fatalf("exit=%d creates=%d updates=%d stdout=%q stderr=%q", code, creates, updates, out.String(), errOut.String())
	}
	parsed := model.ParseTypedComment(published[0].Body)
	for _, target := range []string{"https://github.com/o/r/issues/9#issuecomment-61",
		"https://github.com/o/r/issues/9#issuecomment-62", "https://github.com/o/r/issues/7#issuecomment-51"} {
		if !linksContainURL(parsed.Links["Related Comments"], target) {
			t.Fatalf("missing owner target %s in %q", target, published[0].Body)
		}
	}
}

func TestGitHubReviewSyncRejectsAcceptedReceiptWithoutWrites(t *testing.T) {
	body := reviewSyncAcceptedReceiptBody(t, "REVIEW-900")
	comments := []github.Comment{{ID: 71, HTMLURL: "https://github.com/o/r/issues/9#issuecomment-71", Body: body}}
	creates, updates, prReads := 0, 0, 0
	backend := reviewSyncGitHubBackend{fakeGitHubBackend: fakeGitHubBackend{
		info: github.BackendInfo{Name: "gh", Kind: "external-cli", Host: "github.com"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return append([]github.Comment(nil), comments...), nil
		},
		getPullRequest: func(context.Context, string, int) (github.PullRequest, error) {
			prReads++
			return github.PullRequest{}, errors.New("accepted receipt guard ran too late")
		},
		createComment: func(context.Context, string, int, string) (github.Comment, error) {
			creates++
			return github.Comment{}, nil
		},
		updateComment: func(context.Context, string, int64, string) (github.Comment, error) {
			updates++
			return github.Comment{}, nil
		},
	}}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	code := app.runReviewSync(t.Context(), []string{"--repo", "o/r", "--implement", "9", "--pr", "42",
		"--process", "PROCESS-900", "--id", "REVIEW-900", "--agent", "Independent Reviewer", "--json"})
	if code != 1 || creates != 0 || updates != 0 || prReads != 0 || comments[0].Body != body ||
		!strings.Contains(errOut.String(), "alternative publication paths") {
		t.Fatalf("exit=%d creates=%d updates=%d pr_reads=%d body_changed=%t stderr=%q",
			code, creates, updates, prReads, comments[0].Body != body, errOut.String())
	}
}

func TestReviewSyncWriteHelpersRecheckAcceptedAuthority(t *testing.T) {
	body := reviewSyncAcceptedReceiptBody(t, "REVIEW-101")
	creates, updates := 0, 0
	backend := fakeGitHubBackend{
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 71, Body: body}}, nil
		},
		createComment: func(context.Context, string, int, string) (github.Comment, error) {
			creates++
			return github.Comment{}, nil
		},
		updateComment: func(context.Context, string, int64, string) (github.Comment, error) {
			updates++
			return github.Comment{}, nil
		},
	}
	if _, _, err := upsertReviewSyncComment(t.Context(), backend, "o/r", 9, "REVIEW-101", "replacement"); err == nil ||
		!strings.Contains(err.Error(), "accepted receipt authority") {
		t.Fatalf("GitHub write helper accepted receipt err=%v", err)
	}
	if _, _, err := upsertExternalReviewSyncCommentAt(t.Context(), backend, "o/r", 9, "REVIEW-101",
		"Reviewer", writerSession{}, "review", externalGateResult{}, time.Now().UTC()); err == nil ||
		!strings.Contains(err.Error(), "accepted receipt authority") {
		t.Fatalf("external write helper accepted receipt err=%v", err)
	}
	if creates != 0 || updates != 0 {
		t.Fatalf("accepted receipt crossed fresh write guard: creates=%d updates=%d", creates, updates)
	}
}

type reviewSyncGitHubBackend struct{ fakeGitHubBackend }

func (reviewSyncGitHubBackend) GetCombinedStatus(context.Context, string, string) (github.CombinedStatus, error) {
	return github.CombinedStatus{}, nil
}

func (reviewSyncGitHubBackend) ListCheckRuns(context.Context, string, string) ([]github.CheckRun, error) {
	return nil, nil
}

func TestExternalReviewSyncZeroFindingsWritesCompletionWithoutReviewFacts(t *testing.T) {
	app, native, comments, creates, updates, _, errOut := setupExternalReviewSyncCommand(t)
	native.target.Provider = &commandEvidenceProvider{snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
		Reference: native.target.Reference, SubjectRevision: native.target.SubjectRevision, CapturedAt: time.Now().UTC()}}
	code := app.runReviewSync(t.Context(), []string{"--repo", "acme/widgets", "--hostname", "issues.test",
		"--implement", "9", "--revision", "head-abc", "--id", "REVIEW-101", "--agent", "Independent Reviewer"})
	if code != 0 || *creates != 1 || *updates != 0 || len(*comments) != 1 {
		t.Fatalf("exit=%d creates=%d updates=%d comments=%d stderr=%q", code, *creates, *updates, len(*comments), errOut.String())
	}
	body := (*comments)[0].Body
	if strings.Contains(body, consumedEvidenceStart) || !strings.Contains(body, "### Canonical Findings\n\n```json\n[]\n```") {
		t.Fatalf("zero-finding REVIEW invented review facts or consumed bindings: %q", body)
	}
	completion, found, err := parseExternalReviewCompletion(body)
	if err != nil || !found || completion.SubjectRevision != "head-abc" || completion.ReferenceVersion != 7 {
		t.Fatalf("completion=%+v found=%t err=%v", completion, found, err)
	}
}

func TestExternalReviewSyncRejectsAcceptedMalformedAndDuplicateAuthorityWithoutWrites(t *testing.T) {
	accepted := reviewSyncAcceptedReceiptBody(t, "REVIEW-101")
	tests := []struct {
		name   string
		bodies []string
		want   string
	}{
		{name: "accepted", bodies: []string{accepted}, want: "alternative publication paths"},
		{name: "malformed", bodies: []string{strings.Replace(accepted, acceptedReviewReceiptEnd, "", 1)}, want: "authority is malformed"},
		{name: "duplicate", bodies: []string{accepted, accepted}, want: "multiple active REVIEW comments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, native, comments, creates, updates, _, errOut := setupExternalReviewSyncCommand(t)
			for index, body := range test.bodies {
				*comments = append(*comments, github.Comment{ID: int64(71 + index), Body: body,
					HTMLURL: fmt.Sprintf("https://issues.test/acme/widgets/issues/9#comment-%d", 71+index)})
			}
			before := append([]github.Comment(nil), (*comments)...)
			code := app.runReviewSync(t.Context(), []string{"--repo", "acme/widgets", "--hostname", "issues.test",
				"--implement", "9", "--revision", "head-abc", "--id", "REVIEW-101", "--agent", "Independent Reviewer"})
			unchanged := len(*comments) == len(before)
			for index := range before {
				unchanged = unchanged && (*comments)[index].ID == before[index].ID && (*comments)[index].Body == before[index].Body
			}
			if code != 1 || native.syncs != 0 || native.resolveCalls != 0 || *creates != 0 || *updates != 0 || !unchanged ||
				!strings.Contains(errOut.String(), test.want) {
				t.Fatalf("exit=%d syncs=%d resolves=%d creates=%d updates=%d unchanged=%t stderr=%q",
					code, native.syncs, native.resolveCalls, *creates, *updates, unchanged, errOut.String())
			}
		})
	}
}

func TestGeneratedWorkflowOmitsReviewAuthoritySkill(t *testing.T) {
	for _, skill := range templates.IssueSpecSkills("o/r") {
		if skill.Name == "issue-spec-review" {
			t.Fatalf("retired review authority skill was generated: %s", skill.Content)
		}
	}
}

func TestExternalReviewSyncRejectsPRBeforeProviderSynchronization(t *testing.T) {
	app, native, comments, creates, updates, _, errOut := setupExternalReviewSyncCommand(t)
	code := app.runReviewSync(t.Context(), []string{"--repo", "acme/widgets", "--hostname", "issues.test",
		"--implement", "9", "--revision", "head-abc", "--pr", "42", "--id", "REVIEW-101"})
	if code != 2 || !strings.Contains(errOut.String(), "--pr is not a self-hosted code authority") {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	if native.syncs != 0 || native.resolveCalls != 0 || *creates != 0 || *updates != 0 || len(*comments) != 0 {
		t.Fatalf("invalid --pr crossed preflight: syncs=%d resolves=%d creates=%d updates=%d comments=%d",
			native.syncs, native.resolveCalls, *creates, *updates, len(*comments))
	}
}

func TestExternalReviewSyncFailedRetryLeavesExistingReviewByteStable(t *testing.T) {
	app, native, comments, creates, updates, _, errOut := setupExternalReviewSyncCommand(t)
	args := []string{"--repo", "acme/widgets", "--hostname", "issues.test", "--implement", "9",
		"--revision", "head-abc", "--id", "REVIEW-101"}
	if code := app.runReviewSync(t.Context(), args); code != 0 {
		t.Fatalf("initial sync exit=%d stderr=%q", code, errOut.String())
	}
	before := (*comments)[0].Body
	native.syncErr = errors.New("ledger persistence unavailable")
	errOut.Reset()
	if code := app.runReviewSync(t.Context(), args); code != 1 {
		t.Fatalf("retry exit=%d stderr=%q", code, errOut.String())
	}
	if *creates != 1 || *updates != 0 || len(*comments) != 1 || (*comments)[0].Body != before {
		t.Fatalf("failed retry mutated REVIEW: creates=%d updates=%d body_changed=%t", *creates, *updates, (*comments)[0].Body != before)
	}
}

func TestExternalReviewSyncDisplaysOnlyCanonicalLedgerFindings(t *testing.T) {
	app, _, comments, _, _, _, errOut := setupExternalReviewSyncCommand(t)
	if code := app.runReviewSync(t.Context(), []string{"--repo", "acme/widgets", "--hostname", "issues.test",
		"--implement", "9", "--revision", "head-abc", "--id", "REVIEW-101"}); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	body := (*comments)[0].Body
	for _, want := range []string{`"evidence_id": "review-ledger"`, `"finding_id": "FINDING-001"`,
		`"process_id": "PROCESS-001"`, `"spec_id": "SPEC-001"`, `"severity": "P2"`, `"state": "resolved"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("canonical finding field %q missing from %q", want, body)
		}
	}
	if strings.Contains(body, "PayloadDigest") || strings.Contains(body, "payload_digest") {
		t.Fatalf("provider payload detail leaked into REVIEW: %q", body)
	}
}

func TestExternalReviewSyncFailuresNeverMutateReview(t *testing.T) {
	tests := map[string]func(*app, *commandNativeEvidence, *commandEvidenceProvider){
		"authorization": func(app *app, _ *commandNativeEvidence, _ *commandEvidenceProvider) {
			app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) {
				return nil, errors.New("authorization denied")
			}
		},
		"registry": func(app *app, _ *commandNativeEvidence, _ *commandEvidenceProvider) {
			app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) {
				return nil, errors.New("operator registry malformed")
			}
		},
		"capability": func(_ *app, _ *commandNativeEvidence, provider *commandEvidenceProvider) {
			provider.capabilities = []codereview.Capability{codereview.CapabilityChangeComment}
		},
		"snapshot": func(_ *app, _ *commandNativeEvidence, provider *commandEvidenceProvider) {
			provider.snapshotErr = errors.New("snapshot unavailable")
		},
		"persistence": func(_ *app, native *commandNativeEvidence, _ *commandEvidenceProvider) {
			native.syncErr = errors.New("writer authorization denied")
		},
		"reference movement": func(_ *app, native *commandNativeEvidence, _ *commandEvidenceProvider) {
			moved := native.target
			moved.ReferenceVersion++
			native.targets = []coreevidence.NativeTarget{native.target, moved}
		},
	}
	for name, fail := range tests {
		t.Run(name, func(t *testing.T) {
			app, native, comments, creates, updates, _, errOut := setupExternalReviewSyncCommand(t)
			provider, err := app.lookupOperatorProvider(t.Context(), auth.Profile{}, "code.example")
			if err != nil {
				t.Fatal(err)
			}
			fail(app, native, provider.(*commandEvidenceProvider))
			code := app.runReviewSync(t.Context(), []string{"--repo", "acme/widgets", "--hostname", "issues.test",
				"--implement", "9", "--revision", "head-abc", "--id", "REVIEW-101"})
			if code != 1 || *creates != 0 || *updates != 0 || len(*comments) != 0 {
				t.Fatalf("exit=%d creates=%d updates=%d comments=%d stderr=%q", code, *creates, *updates,
					len(*comments), errOut.String())
			}
		})
	}
}

func setupExternalReviewSyncCommand(t *testing.T) (*app, *commandNativeEvidence, *[]github.Comment, *int, *int,
	*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	clearCommandAuthEnv(t)
	t.Setenv("ISSUE_SPEC_TOKEN", "review-token")
	t.Setenv(codereview.OperatorProvidersFileEnv, "")
	t.Chdir(t.TempDir())
	profile := auth.Profile{Name: "review-sync-test", Kind: auth.ProfileKindHosted, Hostname: "issues.test",
		APIURL: "https://issues.test/api/v3", NativeAPIURL: "https://issues.test/api/v1",
		WebURL: "https://issues.test", ServerInstanceID: "review-sync-test-instance"}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	provider := &commandEvidenceProvider{snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion,
		Reference: reference, SubjectRevision: "head-abc", CapturedAt: now}}
	ledger := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
		SubjectRevision: "head-abc", CapturedAt: now, Records: []codereview.EvidenceRecord{
			testEvidenceRecord("review-ledger", codereview.EvidenceReview, "resolved", "head-abc", now),
		}}
	native := &commandNativeEvidence{target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7,
		SubjectRevision: "head-abc", Provider: &commandEvidenceProvider{snapshot: ledger}}}
	comments := []github.Comment{}
	specBody, err := templates.SpecComment(templates.SpecCommentOptions{Common: templates.CommonOptions{
		ID: "SPEC-001", Status: "confirmed"}, Input: templates.SpecInput{Requirement: templates.SpecRequirementInput{
		Title: "provider review", Text: "Provider review MUST publish owner coverage."}, Scenarios: []templates.SpecScenarioInput{{
		Title: "complete sync", When: "review converges", Then: "owner coverage is complete"}}}})
	if err != nil {
		t.Fatal(err)
	}
	processComments := []github.Comment{
		{ID: 61, HTMLURL: "https://issues.test/acme/widgets/issues/9#issuecomment-review-process",
			Body: reviewCoverageProcessBody(t, "PROCESS-900", model.ProcessExecutionReview, "SPEC-001")},
		{ID: 62, HTMLURL: "https://issues.test/acme/widgets/issues/9#issuecomment-process",
			Body: reviewCoverageProcessBody(t, "PROCESS-001", model.ProcessExecutionChangeBearing, "SPEC-001")},
	}
	creates, updates := 0, 0
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "rest", Kind: "rest", Host: profile.Hostname},
		getIssue: func(_ context.Context, _ string, issue int) (github.Issue, error) {
			items := map[int]github.Issue{
				7: {Number: 7, HTMLURL: "https://issues.test/acme/widgets/issues/7", Body: "<!-- issue-spec:issue=proposal change=provider-review version=1 -->"},
				8: {Number: 8, HTMLURL: "https://issues.test/acme/widgets/issues/8", Body: "<!-- issue-spec:issue=design change=provider-review version=1 -->\n- Proposal Issue: 7"},
				9: {Number: 9, HTMLURL: "https://issues.test/acme/widgets/issues/9", Body: "<!-- issue-spec:issue=implement change=provider-review version=1 -->\n- Design Issue: 8"},
			}
			item, ok := items[issue]
			if !ok {
				return github.Issue{}, errors.New("unexpected issue")
			}
			return item, nil
		},
		listIssueComments: func(_ context.Context, _ string, issue int) ([]github.Comment, error) {
			switch issue {
			case 7:
				return []github.Comment{{ID: 51, HTMLURL: "https://issues.test/acme/widgets/issues/7#issuecomment-spec", Body: specBody}}, nil
			case 9:
				return append(append([]github.Comment(nil), processComments...), comments...), nil
			default:
				return nil, nil
			}
		},
		createComment: func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			creates++
			created := github.Comment{ID: 71, Body: body, HTMLURL: "https://issues.test/acme/widgets/issues/9#comment-71"}
			comments = append(comments, created)
			return created, nil
		},
		updateComment: func(_ context.Context, _ string, id int64, body string) (github.Comment, error) {
			updates++
			if len(comments) != 1 || comments[0].ID != id {
				return github.Comment{}, errors.New("unexpected REVIEW update target")
			}
			comments[0].Body = body
			return comments[0], nil
		},
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := newApp(strings.NewReader(""), out, errOut)
	app.profileName = profile.Name
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	app.newNativeEvidenceProvider = func(auth.Profile, string) (nativeEvidenceProvider, error) { return native, nil }
	app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) { return provider, nil }
	return app, native, &comments, &creates, &updates, out, errOut
}

func reviewSyncAcceptedReceiptBody(t *testing.T, reviewID string) string {
	t.Helper()
	receipt := testSealedReviewReceipt(t, assignment.ReviewApprove, nil)
	body, err := renderSubmittedReview(reviewID, "PROCESS-900",
		"https://issues.test/acme/widgets/issues/9#issuecomment-review-process", "",
		[]string{"https://issues.test/acme/widgets/issues/7#issuecomment-spec"}, receipt)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func reviewCoverageProcessBody(t *testing.T, id string, class model.ProcessExecutionClass, specID string) string {
	t.Helper()
	body, err := model.EnsureTypedBody("PROCESS", id, fmt.Sprintf(`## Process: owner coverage

### Parent TASK

- TASK-001

### Dependencies

- N/A

### Execution Class

- %s

### Covers

- %s

### Handoff

N/A`, class, specID), model.BodyOptions{Status: "in-progress"})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

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

func TestBuildReviewSyncReportDoesNotRequireSessionMetadata(t *testing.T) {
	finding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}, []github.PullRequestReviewComment{
		{ID: 2, Body: finding, Path: "b.go", Line: 20, HTMLURL: "https://github.com/o/r/pull/4#discussion_r2"},
	}, nil, github.CombinedStatus{}, nil)
	if len(report.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", report.Diagnostics)
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

func TestBuildReviewSyncReportRetainsProviderOwnedRevisions(t *testing.T) {
	finding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Fix this.", "open", "b.go", 20)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := model.RenderFindingReplyBody("Review", "FINDING-001", "PROCESS-001", "resolved", "Re-checked old revision.")
	if err != nil {
		t.Fatal(err)
	}
	report := buildReviewSyncReport(github.PullRequest{Number: 4}, []github.PullRequestReviewComment{
		{ID: 2, Body: finding, CommitID: "finding-head", Path: "b.go", Line: 20},
		{ID: 3, InReplyToID: 2, Body: reply, CommitID: "reviewed-old-head"},
	}, nil, github.CombinedStatus{Statuses: []github.Status{{Context: "legacy", State: "success"}}}, []github.CheckRun{
		{ID: 9, Name: "unit", HeadSHA: "checked-new-head", Status: "completed", Conclusion: "success"},
	})
	if got := report.ResolvedFindings[0]; got.SubjectRevision != "reviewed-old-head" || got.RevisionSource != "github-pr-review-comment:3" {
		t.Fatalf("resolved finding revision = %+v", got)
	}
	if len(report.PassedChecks) != 2 {
		t.Fatalf("passed checks = %+v", report.PassedChecks)
	}
	for _, check := range report.PassedChecks {
		switch check.Name {
		case "legacy":
			if check.Trusted || check.SubjectRevision != "" {
				t.Fatalf("revisionless status context became trusted: %+v", check)
			}
		case "unit":
			if !check.Trusted || check.SubjectRevision != "checked-new-head" {
				t.Fatalf("check-run head not retained: %+v", check)
			}
		}
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
	const head = "0123456789abcdef0123456789abcdef01234567"
	body, err := renderReviewSyncComment("REVIEW-001", "Coordinator", writerSession{}, "pr-review", "https://github.com/o/r/pull/4", reviewSyncReport{
		OK:                true,
		PR:                4,
		PRURL:             "https://github.com/o/r/pull/4",
		SubjectRevision:   head,
		RevisionSource:    "github-pull-request-head:4",
		RationaleComments: 2,
		PassedChecks: []reviewCheck{{Name: "DCO", State: "completed", Conclusion: "success", URL: "https://github.com/o/r/checks/42",
			SubjectRevision: head, Trusted: true, Source: "github-check-run:42"}},
		ProcessEvidence: []gates.ProcessEvidenceReport{{ProcessID: "PROCESS-001", Missing: []string{"recomputable forecast"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Type: REVIEW", "ID: REVIEW-001", "Status: done", "Subject Revision: " + head,
		"Review sync passed", "DCO", "subject_revision=" + head, "trusted=true", "source=github-check-run:42"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{"PROCESS Evidence Observation", "MUST NOT be treated as final readiness", "recomputable forecast"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("durable REVIEW retained %q:\n%s", forbidden, body)
		}
	}
	parsed := model.ParseTypedComment(body)
	if parsed.SubjectRevision != head {
		t.Fatalf("subject revision = %q, want %q", parsed.SubjectRevision, head)
	}
}

func TestBuildReviewSyncReportCapturesAuthoritativePullRequestHead(t *testing.T) {
	pr := github.PullRequest{Number: 4, HTMLURL: "https://github.com/o/r/pull/4"}
	pr.Head.SHA = "0123456789abcdef0123456789abcdef01234567"
	report := buildReviewSyncReport(pr, nil, nil, github.CombinedStatus{}, nil)
	if report.SubjectRevision != pr.Head.SHA || report.RevisionSource != "github-pull-request-head:4" {
		t.Fatalf("subject revision was not captured from PR head: %+v", report)
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

func TestReplyReviewFindingAllowsOwningReviewerAfterWorker(t *testing.T) {
	ctx := context.Background()
	client := &fakeReviewClient{comments: []github.PullRequestReviewComment{{
		ID: 10, HTMLURL: "https://github.com/o/r/pull/7#discussion_r10", Body: "P1: fix this",
		Path: "internal/foo.go", Line: 2,
	}}}
	worker, err := replyReviewFinding(ctx, client, "o/r", 7, 10, "FINDING-001", "PROCESS-001", "resolved", "Worker Agent", writerSession{}, "Fixed.")
	if err != nil || !worker.Created {
		t.Fatalf("worker reply = %+v, %v", worker, err)
	}
	reviewer, err := replyReviewFinding(ctx, client, "o/r", 7, 10, "FINDING-001", "PROCESS-001", "resolved", "Review Agent", writerSession{}, "Re-checked.")
	if err != nil || !reviewer.Created || reviewer.CommentID == worker.CommentID {
		t.Fatalf("reviewer reply = %+v, %v", reviewer, err)
	}
	reviewer, err = replyReviewFinding(ctx, client, "o/r", 7, 10, "FINDING-001", "PROCESS-001", "resolved", "Review Agent", writerSession{}, "Re-checked.")
	if err != nil || reviewer.Created {
		t.Fatalf("idempotent reviewer reply = %+v, %v", reviewer, err)
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
	if err != nil || !ok || marker.ID == "" || marker.Severity != "P1" {
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
