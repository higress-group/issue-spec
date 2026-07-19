package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestArchiveDurableSpecClosesIssuesAfterMergedLinkedPR(t *testing.T) {
	t.Chdir(t.TempDir())
	const prURL = "https://github.com/o/r/pull/7"
	specBody := archiveTestTypedBody(t, "SPEC", "SPEC-001", "confirmed", "close workflow", nil)
	processBody := archiveTestTypedBody(t, "PROCESS", "PROCESS-001", "done", "implementation", map[string][]string{"PR": {prURL}})

	var closed []int
	var out, errOut bytes.Buffer
	app := newArchiveCloseTestApp(t, &out, &errOut, github.PullRequest{Number: 7, HTMLURL: prURL, Merged: true, Body: archiveTestClosingBody(t, 1, 2, 3)}, specBody, processBody, func(_ context.Context, repo string, issueNumber int, opts github.UpdateIssueOptions) (github.Issue, error) {
		if repo != "o/r" {
			t.Fatalf("repo = %q, want o/r", repo)
		}
		if opts.State == nil || *opts.State != "closed" {
			t.Fatalf("state update = %+v, want closed", opts)
		}
		closed = append(closed, issueNumber)
		return github.Issue{Number: issueNumber, HTMLURL: fmt.Sprintf("https://github.com/o/r/issues/%d", issueNumber), State: *opts.State}, nil
	})

	code := app.runArchive(context.Background(), []string{
		"durable-spec",
		"--repo", "o/r",
		"--proposal", "1",
		"--design", "2",
		"--implement", "3",
		"--pr", "7",
		"--capability", "workflow-close",
		"--close-issues",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !reflect.DeepEqual(closed, []int{1, 2, 3}) {
		t.Fatalf("closed issues = %+v, want [1 2 3]", closed)
	}
	var result struct {
		OK                  bool                 `json:"ok"`
		ImplementationPR    int                  `json:"implementation_pr"`
		ImplementationPRURL string               `json:"implementation_pr_url"`
		ClosedIssues        []closedArchiveIssue `json:"closed_issues"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.ImplementationPR != 7 || result.ImplementationPRURL != prURL || len(result.ClosedIssues) != 3 {
		t.Fatalf("unexpected archive result: %+v", result)
	}
}

func TestArchiveDurableSpecDoesNotCloseIssuesForUnmergedPR(t *testing.T) {
	t.Chdir(t.TempDir())
	const prURL = "https://github.com/o/r/pull/7"
	specBody := archiveTestTypedBody(t, "SPEC", "SPEC-001", "confirmed", "close workflow", nil)
	processBody := archiveTestTypedBody(t, "PROCESS", "PROCESS-001", "done", "implementation", map[string][]string{"PR": {prURL}})
	updateCalled := false
	var out, errOut bytes.Buffer
	app := newArchiveCloseTestApp(t, &out, &errOut, github.PullRequest{Number: 7, HTMLURL: prURL, Merged: false}, specBody, processBody, func(context.Context, string, int, github.UpdateIssueOptions) (github.Issue, error) {
		updateCalled = true
		return github.Issue{}, errors.New("unexpected update")
	})

	code := app.runArchive(context.Background(), []string{
		"durable-spec",
		"--repo", "o/r",
		"--proposal", "1",
		"--design", "2",
		"--implement", "3",
		"--pr", "7",
		"--capability", "workflow-close",
		"--close-issues",
	})
	if code != 1 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if updateCalled {
		t.Fatal("unmerged PR should not close issues")
	}
	if !strings.Contains(errOut.String(), "must be merged") {
		t.Fatalf("stderr missing merged guard: %q", errOut.String())
	}
}

func TestArchiveDurableSpecRequiresProcessLinkedToPRBeforeClosingIssues(t *testing.T) {
	t.Chdir(t.TempDir())
	const prURL = "https://github.com/o/r/pull/7"
	specBody := archiveTestTypedBody(t, "SPEC", "SPEC-001", "confirmed", "close workflow", nil)
	processBody := archiveTestTypedBody(t, "PROCESS", "PROCESS-001", "done", "implementation", nil)
	updateCalled := false
	var out, errOut bytes.Buffer
	app := newArchiveCloseTestApp(t, &out, &errOut, github.PullRequest{Number: 7, HTMLURL: prURL, Merged: true, Body: archiveTestClosingBody(t, 1, 2, 3)}, specBody, processBody, func(context.Context, string, int, github.UpdateIssueOptions) (github.Issue, error) {
		updateCalled = true
		return github.Issue{}, errors.New("unexpected update")
	})

	code := app.runArchive(context.Background(), []string{
		"durable-spec",
		"--repo", "o/r",
		"--proposal", "1",
		"--design", "2",
		"--implement", "3",
		"--pr", "7",
		"--capability", "workflow-close",
		"--close-issues",
	})
	if code != 1 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if updateCalled {
		t.Fatal("unlinked PR should not close issues")
	}
	if !strings.Contains(errOut.String(), "has no active PROCESS linked") {
		t.Fatalf("stderr missing linked PROCESS guard: %q", errOut.String())
	}
}

func TestArchiveDurableSpecRequiresClosingLinksBeforeClosingIssues(t *testing.T) {
	t.Chdir(t.TempDir())
	const prURL = "https://github.com/o/r/pull/7"
	specBody := archiveTestTypedBody(t, "SPEC", "SPEC-001", "confirmed", "close workflow", nil)
	processBody := archiveTestTypedBody(t, "PROCESS", "PROCESS-001", "done", "implementation", map[string][]string{"PR": {prURL}})
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "missing block", body: "## Summary\n\nImplementation.\n", wantErr: "missing issue-spec PR closing block"},
		{name: "wrong issue", body: archiveTestClosingBody(t, 1, 2, 99), wantErr: "unexpected issue closing link Closes #99"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updateCalled := false
			var out, errOut bytes.Buffer
			app := newArchiveCloseTestApp(t, &out, &errOut, github.PullRequest{Number: 7, HTMLURL: prURL, Merged: true, Body: tt.body}, specBody, processBody, func(context.Context, string, int, github.UpdateIssueOptions) (github.Issue, error) {
				updateCalled = true
				return github.Issue{}, errors.New("unexpected update")
			})

			code := app.runArchive(context.Background(), []string{
				"durable-spec",
				"--repo", "o/r",
				"--proposal", "1",
				"--design", "2",
				"--implement", "3",
				"--pr", "7",
				"--capability", "workflow-close",
				"--close-issues",
			})
			if code != 1 {
				t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			if updateCalled {
				t.Fatal("invalid PR closing links should not close issues")
			}
			if !strings.Contains(errOut.String(), tt.wantErr) {
				t.Fatalf("stderr missing %q: %q", tt.wantErr, errOut.String())
			}
		})
	}
}

func TestArchiveLinkedProcessUsesSharedActiveSelection(t *testing.T) {
	const prURL = "https://github.com/o/r/pull/7"
	process := func(id, status, url string, links map[string][]string) model.Artifact {
		t.Helper()
		body := archiveTestTypedBody(t, "PROCESS", id, status, "manual role-owned evidence", links)
		return model.Artifact{Issue: 3, CommentID: int64(len(id)), URL: url,
			Comment: model.ParseTypedComment(body)}
	}
	const (
		historicalURL = "https://github.com/o/r/issues/3#issuecomment-301"
		currentURL    = "https://github.com/o/r/issues/3#issuecomment-302"
		legacyURL     = "https://github.com/o/r/issues/3#issuecomment-303"
	)
	historical := process("PROCESS-001", "superseded", historicalURL, map[string][]string{"PR": {prURL}})
	current := process("PROCESS-002", "done", currentURL, nil)
	stamped, _, err := model.StampSupersededBy(historical.Comment.Body, historical.Comment.ID,
		model.SupersededBy{ProcessID: current.Comment.ID, URL: current.URL})
	if err != nil {
		t.Fatal(err)
	}
	historical.Comment = model.ParseTypedComment(stamped)
	if processArtifactsLinkPullRequest([]model.Artifact{historical, current}, prURL) {
		t.Fatal("historical PROCESS supplied the active archive PR link")
	}

	current = process("PROCESS-002", "done", currentURL, map[string][]string{"PR": {prURL}})
	if !processArtifactsLinkPullRequest([]model.Artifact{historical, current}, prURL) {
		t.Fatal("active successor PROCESS did not supply the archive PR link")
	}

	legacy := process("PROCESS-003", "superseded", legacyURL, map[string][]string{"PR": {prURL}})
	if !processArtifactsLinkPullRequest([]model.Artifact{legacy}, prURL) {
		t.Fatal("legacy status-only PROCESS was excluded from archive selection")
	}

	manual := process("PROCESS-004", "done", "https://github.com/o/r/issues/3#issuecomment-304",
		map[string][]string{"PR": {prURL}})
	if !processArtifactsLinkPullRequest([]model.Artifact{manual}, prURL) {
		t.Fatal("independently complete manual PROCESS evidence was excluded from archive selection")
	}
}

func TestArchiveReadsOnlyImplementationReviewCompletion(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	artifacts, implementationGate := externalReviewCompletionFixture(t, now, "Independent Reviewer")
	implementationGate.ReviewCompletionPolicy = ReviewCompletionPolicy{Required: true, Freshness: time.Hour}
	before := artifacts[3].Comment.Body
	if err := validateArchiveImplementationReviewCompletion(artifacts, implementationGate, now); err != nil {
		t.Fatalf("implementation completion rejected: %v", err)
	}
	if artifacts[3].Comment.Body != before {
		t.Fatal("archive completion validation mutated REVIEW bytes")
	}

	archiveGate := implementationGate
	archiveGate.Target.Reference.ChangeID = "archive-change-9"
	archiveGate.Snapshot.Reference = archiveGate.Target.Reference
	archiveGate.Target.SubjectRevision = "archive-head"
	archiveGate.Snapshot.SubjectRevision = "archive-head"
	if err := validateArchiveImplementationReviewCompletion(artifacts, archiveGate, now); err == nil {
		t.Fatal("implementation REVIEW completion was applied to archive_change")
	}
	if artifacts[3].Comment.Body != before {
		t.Fatal("archive_change mismatch mutated implementation REVIEW")
	}

	optional := implementationGate
	optional.ReviewCompletionPolicy = ReviewCompletionPolicy{}
	withoutReview := append([]model.Artifact(nil), artifacts[:3]...)
	if err := validateArchiveImplementationReviewCompletion(withoutReview, optional, now); err != nil {
		t.Fatalf("archive required completion when merge policy did not: %v", err)
	}
}

func TestArchiveMergeProjectsReviewCompletionAndPreservesProviderFacts(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "change-42"}
	check := testEvidenceRecord("check-ledger", codereview.EvidenceCheck, "passed", "head-abc", now)
	check.Name = "unit"
	merge := testEvidenceRecord("merge-ledger", codereview.EvidenceMerge, "merged", "head-abc", now)
	merge.MergeRevision = "head-abc"
	gate := externalGateResult{
		Target: coreevidence.NativeTarget{Reference: reference, ReferenceVersion: 7, SubjectRevision: "head-abc"},
		Snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
			SubjectRevision: "head-abc", CapturedAt: now, Records: []codereview.EvidenceRecord{check, merge}},
		Consumption: externalEvidenceConsumption{ProviderKey: reference.ProviderKey,
			ExternalRepository: reference.ExternalRepository, ChangeID: reference.ChangeID,
			ReferenceVersion: 7, SubjectRevision: "head-abc"},
	}
	policy := coreevidence.Policy{RequiredKinds: []codereview.EvidenceKind{codereview.EvidenceReview, codereview.EvidenceCheck},
		Freshness: map[codereview.EvidenceKind]time.Duration{codereview.EvidenceReview: time.Hour}}
	completion := projectReviewCompletionPolicy(&policy)
	if !completion.Required || completion.Freshness != time.Hour {
		t.Fatalf("review completion policy was not projected: %+v", completion)
	}
	evaluated := evaluateArchiveImplementationMerge(gate, policy, now)
	if !evaluated.Evaluation.Passed || strings.Join(evaluated.Consumption.EvidenceIDs, ",") != "check-ledger,merge-ledger" {
		t.Fatalf("projected merge gate lost authoritative evidence: %+v", evaluated)
	}

	for name, mutate := range map[string]func(*externalGateResult){
		"open finding": func(candidate *externalGateResult) {
			open := testEvidenceRecord("review-open", codereview.EvidenceReview, "open", "head-abc", now)
			open.Severity = "P1"
			candidate.Snapshot.Records = append(candidate.Snapshot.Records, open)
		},
		"missing merge": func(candidate *externalGateResult) {
			candidate.Snapshot.Records = candidate.Snapshot.Records[:1]
		},
		"failed check": func(candidate *externalGateResult) {
			candidate.Snapshot.Records[0].State = "failed"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := gate
			candidate.Snapshot.Records = append([]codereview.EvidenceRecord(nil), gate.Snapshot.Records...)
			mutate(&candidate)
			result := evaluateArchiveImplementationMerge(candidate, policy, now)
			if result.Evaluation.Passed {
				t.Fatalf("archive suppressed authoritative provider failure: %+v", result.Evaluation)
			}
		})
	}
}

func newArchiveCloseTestApp(t *testing.T, out, errOut *bytes.Buffer, pr github.PullRequest, specBody, processBody string, updateIssue func(context.Context, string, int, github.UpdateIssueOptions) (github.Issue, error)) *app {
	t.Helper()
	app := newApp(strings.NewReader(""), out, errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		return fakeGitHubBackend{
			info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			getIssue: func(_ context.Context, repo string, issueNumber int) (github.Issue, error) {
				if repo != "o/r" {
					t.Fatalf("repo = %q, want o/r", repo)
				}
				return github.Issue{Number: issueNumber, HTMLURL: fmt.Sprintf("https://github.com/o/r/issues/%d", issueNumber), State: "open"}, nil
			},
			getPullRequest: func(_ context.Context, repo string, prNumber int) (github.PullRequest, error) {
				if repo != "o/r" || prNumber != pr.Number {
					t.Fatalf("unexpected PR lookup repo=%q pr=%d", repo, prNumber)
				}
				return pr, nil
			},
			listIssueComments: func(_ context.Context, repo string, issueNumber int) ([]github.Comment, error) {
				if repo != "o/r" {
					t.Fatalf("repo = %q, want o/r", repo)
				}
				switch issueNumber {
				case 1:
					return []github.Comment{{ID: 101, HTMLURL: "https://github.com/o/r/issues/1#issuecomment-101", URL: "https://api.github.com/repos/o/r/issues/comments/101", Body: specBody}}, nil
				case 3:
					return []github.Comment{{ID: 301, HTMLURL: "https://github.com/o/r/issues/3#issuecomment-301", URL: "https://api.github.com/repos/o/r/issues/comments/301", Body: processBody}}, nil
				default:
					return nil, nil
				}
			},
			updateIssue: updateIssue,
		}, nil
	}
	return app
}

func archiveTestClosingBody(t *testing.T, proposal, design, implement int) string {
	t.Helper()
	body, _, err := model.AddIssueClosureBlock("## Summary\n\nImplementation.\n", []model.IssueClosureRef{
		{Kind: "proposal", Number: proposal},
		{Kind: "design", Number: design},
		{Kind: "implement", Number: implement},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func archiveTestTypedBody(t *testing.T, typ, id, status, scope string, links map[string][]string) string {
	t.Helper()
	content := "## Evidence\n\nArchive close workflow test.\n"
	if typ == "SPEC" {
		content = `## Requirement: Close workflow issues

The archive command MUST close proposal, design, and implement issues after the linked implementation PR is merged.

### Scenario: Close active issues after archive

- **WHEN** archive durable-spec runs with --close-issues after a merged implementation PR
- **THEN** it closes the proposal, design, and implement issues.
`
	}
	body, err := model.EnsureTypedBody(typ, id, content, model.BodyOptions{
		Agent:  "Test Agent",
		Status: status,
		Scope:  scope,
		Links:  links,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
