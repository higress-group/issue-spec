package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/finalization"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/reconcile"
)

const (
	finalizeTestHead         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	finalizeTestBaseline     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	finalizeTestProviderBase = "dddddddddddddddddddddddddddddddddddddddd"
)

type finalizeCommandBackend struct {
	fakeGitHubBackend
	pr       github.PullRequest
	issues   map[int]github.Issue
	comments map[int][]github.Comment
	writes   int
	writeOK  bool
}

func (b *finalizeCommandBackend) GetIssue(_ context.Context, _ string, number int) (github.Issue, error) {
	issue, ok := b.issues[number]
	if !ok {
		return github.Issue{}, errors.New("missing issue")
	}
	return issue, nil
}

func (b *finalizeCommandBackend) ListIssueComments(_ context.Context, _ string, number int) ([]github.Comment, error) {
	return append([]github.Comment(nil), b.comments[number]...), nil
}

func (b *finalizeCommandBackend) GetPullRequest(context.Context, string, int) (github.PullRequest, error) {
	return b.pr, nil
}

func (*finalizeCommandBackend) ListPullRequestReviewComments(context.Context, string, int) ([]github.PullRequestReviewComment, error) {
	return []github.PullRequestReviewComment{}, nil
}

func (*finalizeCommandBackend) GetCombinedStatus(context.Context, string, string) (github.CombinedStatus, error) {
	return github.CombinedStatus{State: "success", Statuses: []github.Status{}}, nil
}

func (*finalizeCommandBackend) ListCheckRuns(context.Context, string, string) ([]github.CheckRun, error) {
	return []github.CheckRun{}, nil
}

func (b *finalizeCommandBackend) ListPullRequestCommits(context.Context, string, int) ([]github.PullRequestCommit, error) {
	return []github.PullRequestCommit{{SHA: b.pr.Head.SHA}}, nil
}

func (b *finalizeCommandBackend) CreateComment(context.Context, string, int, string) (github.Comment, error) {
	b.writes++
	return github.Comment{}, errors.New("preview attempted a write")
}

func (b *finalizeCommandBackend) UpdateComment(_ context.Context, _ string, commentID int64, body string) (github.Comment, error) {
	b.writes++
	if !b.writeOK {
		return github.Comment{}, errors.New("preview attempted a write")
	}
	for issue, comments := range b.comments {
		for index := range comments {
			if comments[index].ID != commentID {
				continue
			}
			comments[index].Body = body
			comments[index].IssueNumber = issue
			b.comments[issue] = comments
			return comments[index], nil
		}
	}
	return github.Comment{}, errors.New("missing update target")
}

func TestFinalizePreviewIsWriteFreeDeterministicAndDetailOmitsBodies(t *testing.T) {
	backend := finalizePreviewFixture()
	application := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	application.selectGitHubBackend = ghSelection
	application.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	var resolvedProviderBases []string
	application.resolveFinalizationBaseline = func(_ context.Context, subject, providerBase string) (string, error) {
		if subject != finalizeTestHead {
			t.Fatalf("baseline subject=%s", subject)
		}
		resolvedProviderBases = append(resolvedProviderBases, providerBase)
		return finalizeTestBaseline, nil
	}
	directory := t.TempDir()
	intentPath := filepath.Join(directory, "intent.json")
	if err := os.WriteFile(intentPath, []byte(`{"version":1,"baseline_revision":"`+finalizeTestBaseline+`","superseded_by":[{"from":"PROCESS-001","to":"PROCESS-002"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	preview := func(name string) finalization.Plan {
		planPath := filepath.Join(directory, name)
		application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
		code := application.runFinalizePreview(t.Context(), []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--pr", "9",
			"--intent-file", intentPath, "--plan-out", planPath, "--json"})
		if code != 0 {
			t.Fatalf("preview code=%d stderr=%s", code, application.err.(*bytes.Buffer).String())
		}
		file, err := os.Open(planPath)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		plan, err := finalization.ReadPlan(file)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	first, second := preview("plan-1.json"), preview("plan-2.json")
	if backend.writes != 0 {
		t.Fatalf("preview provider writes=%d", backend.writes)
	}
	if first.PlanDigest != second.PlanDigest || first.Subject.BaselineRevision != finalizeTestBaseline ||
		first.Subject.ProviderBaseRevision != finalizeTestProviderBase {
		t.Fatalf("plans differ or baseline drifted: first=%+v second=%+v", first.Subject, second.Subject)
	}
	if len(resolvedProviderBases) != 2 || resolvedProviderBases[0] != finalizeTestProviderBase || resolvedProviderBases[1] != finalizeTestProviderBase {
		t.Fatalf("baseline resolver inputs=%v", resolvedProviderBases)
	}
	application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
	if code := application.runFinalizeDetail([]string{"--plan", filepath.Join(directory, "plan-1.json"), "--json"}); code != 0 {
		t.Fatalf("detail code=%d stderr=%s", code, application.err.(*bytes.Buffer).String())
	}
	detail := application.out.(*bytes.Buffer).String()
	if strings.Contains(detail, "issue-spec:superseded-by") || strings.Contains(detail, `"desired"`) {
		t.Fatalf("detail leaked mutation bodies: %s", detail)
	}
}

func TestFinalizeSevenEdgePreviewApplyWritesOnlyHistoricalOwners(t *testing.T) {
	backend, intentData, historical, stableBodies := finalizeSevenEdgeFixture(t)
	application := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	application.selectGitHubBackend = ghSelection
	application.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	application.resolveFinalizationBaseline = func(context.Context, string, string) (string, error) { return finalizeTestBaseline, nil }
	directory := t.TempDir()
	intentPath := filepath.Join(directory, "intent.json")
	if err := os.WriteFile(intentPath, intentData, 0o600); err != nil {
		t.Fatal(err)
	}
	preview := func(name string) finalization.Plan {
		t.Helper()
		planPath := filepath.Join(directory, name)
		application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
		code := application.runFinalizePreview(t.Context(), []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--pr", "9",
			"--intent-file", intentPath, "--plan-out", planPath, "--json"})
		if code != 0 {
			t.Fatalf("preview code=%d stderr=%s", code, application.err.(*bytes.Buffer).String())
		}
		file, err := os.Open(planPath)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		plan, err := finalization.ReadPlan(file)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	first, second := preview("plan-1.json"), preview("plan-2.json")
	if backend.writes != 0 || first.PlanDigest != second.PlanDigest {
		t.Fatalf("preview writes=%d digests=%s/%s", backend.writes, first.PlanDigest, second.PlanDigest)
	}
	if len(first.Reconcile.Operations) != len(historical) {
		t.Fatalf("operations=%+v, want one per historical owner", first.Reconcile.Operations)
	}
	for _, operation := range first.Reconcile.Operations {
		toID, ok := historical[operation.Target.ID]
		if !ok || operation.Kind != "upsert" || operation.Target.Type != "PROCESS" || operation.Desired.Peer != nil ||
			operation.Desired.RelationshipUpdate != nil || operation.Desired.CarrierAuthorizedBacklink || len(operation.Precondition.Endpoints) != 0 {
			t.Fatalf("non-owner finalization operation=%+v", operation)
		}
		desired := model.ParseTypedComment(operation.Desired.Body)
		marker, found, err := model.ParseSupersededBy(operation.Desired.Body, operation.Target.ID)
		if err != nil || !found || desired.Status != "superseded" || marker.ProcessID != toID {
			t.Fatalf("combined owner postimage=%s marker=%+v found=%t err=%v", desired.Status, marker, found, err)
		}
	}

	backend.writeOK = true
	apply := func() finalizeApplyResult {
		t.Helper()
		application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
		code := application.runFinalizeApply(t.Context(), []string{"--plan", filepath.Join(directory, "plan-1.json"),
			"--checkpoint", filepath.Join(directory, "checkpoint.json"), "--allow-nonatomic", "--json"})
		if code != 0 && code != 1 {
			t.Fatalf("apply code=%d stderr=%s", code, application.err.(*bytes.Buffer).String())
		}
		if stderr := application.err.(*bytes.Buffer).String(); stderr != "" {
			t.Fatalf("apply failed before bounded result: %s", stderr)
		}
		var result finalizeApplyResult
		if err := json.Unmarshal(application.out.(*bytes.Buffer).Bytes(), &result); err != nil {
			t.Fatalf("decode apply result: %v output=%s", err, application.out.(*bytes.Buffer).String())
		}
		return result
	}
	result := apply()
	if backend.writes != len(historical) || !result.Reconcile.OK || result.Reconcile.Updated != len(historical) {
		t.Fatalf("apply writes=%d result=%+v", backend.writes, result.Reconcile)
	}
	for _, comments := range backend.comments {
		for _, comment := range comments {
			typed := model.ParseTypedComment(comment.Body)
			if toID, isHistorical := historical[typed.ID]; isHistorical {
				marker, found, err := model.ParseSupersededBy(comment.Body, typed.ID)
				if err != nil || !found || typed.Status != "superseded" || marker.ProcessID != toID {
					t.Fatalf("applied historical owner %s status=%s marker=%+v found=%t err=%v", typed.ID, typed.Status, marker, found, err)
				}
				continue
			}
			if before, stable := stableBodies[typed.ID]; stable && comment.Body != before {
				t.Fatalf("peer %s changed outside its owner: before=%q after=%q", typed.ID, before, comment.Body)
			}
		}
	}
	writesAfterFirstApply := backend.writes
	retry := apply()
	if backend.writes != writesAfterFirstApply || retry.Reconcile.Updated != 0 || retry.Reconcile.Unchanged != len(historical) {
		t.Fatalf("checkpoint retry wrote again: writes=%d result=%+v", backend.writes, retry.Reconcile)
	}

	exactOwnerBody := ""
	for _, operation := range first.Reconcile.Operations {
		if operation.Target.ID == "PROCESS-001" {
			exactOwnerBody = operation.Desired.Body
			break
		}
	}
	if exactOwnerBody == "" {
		t.Fatal("missing exact PROCESS-001 owner postimage")
	}
	setOwnerBody := func(body string) {
		t.Helper()
		for issue, comments := range backend.comments {
			for index := range comments {
				if model.ParseTypedComment(comments[index].Body).ID != "PROCESS-001" {
					continue
				}
				comments[index].Body = body
				backend.comments[issue] = comments
				return
			}
		}
		t.Fatal("missing PROCESS-001 owner fixture")
	}
	assertPeersStable := func() {
		t.Helper()
		for _, comments := range backend.comments {
			for _, comment := range comments {
				typed := model.ParseTypedComment(comment.Body)
				if before, stable := stableBodies[typed.ID]; stable && comment.Body != before {
					t.Fatalf("peer %s changed while owner postimage drifted: before=%q after=%q", typed.ID, before, comment.Body)
				}
			}
		}
	}
	expectPostimageDrift := func(name, body string) {
		t.Helper()
		setOwnerBody(body)
		writesBefore := backend.writes
		application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
		code := application.runFinalizeApply(t.Context(), []string{"--plan", filepath.Join(directory, "plan-1.json"),
			"--checkpoint", filepath.Join(directory, "checkpoint.json"), "--allow-nonatomic", "--json"})
		if code != 1 || !strings.Contains(application.err.(*bytes.Buffer).String(), "postcondition drifted") ||
			application.out.(*bytes.Buffer).Len() != 0 {
			t.Fatalf("%s drift did not fail closed: code=%d stdout=%s stderr=%s", name, code,
				application.out.(*bytes.Buffer).String(), application.err.(*bytes.Buffer).String())
		}
		if backend.writes != writesBefore {
			t.Fatalf("%s drift triggered a write: before=%d after=%d", name, writesBefore, backend.writes)
		}
		assertPeersStable()
		setOwnerBody(exactOwnerBody)
	}

	statusDrift := strings.Replace(exactOwnerBody, "Status: superseded", "Status: in-progress", 1)
	headerDrift := strings.Replace(exactOwnerBody, "Scope: test", "Scope: unrelated-drift", 1)
	linkDrift, changed, err := model.AddRelatedCommentLink(exactOwnerBody, "https://github.com/o/r/issues/3#issuecomment-999")
	if err != nil || !changed {
		t.Fatalf("build link drift: changed=%t err=%v", changed, err)
	}
	logicalDrift := strings.TrimRight(exactOwnerBody, "\n") + "\n\nunrelated logical drift\n"
	for name, body := range map[string]string{
		"status-only": statusDrift,
		"header-only": headerDrift,
		"link-only":   linkDrift,
		"logical":     logicalDrift,
	} {
		if body == exactOwnerBody {
			t.Fatalf("%s fixture did not change the owner body", name)
		}
		expectPostimageDrift(name, body)
	}
}

func TestFinalizePreviewAndApplyBindSharedGateToExactImplementIssue(t *testing.T) {
	backend := finalizeImplementBoundaryFixture(t)
	application := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	application.selectGitHubBackend = ghSelection
	application.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	application.resolveFinalizationBaseline = func(context.Context, string, string) (string, error) { return finalizeTestBaseline, nil }
	directory := t.TempDir()
	intentPath, planPath := filepath.Join(directory, "intent.json"), filepath.Join(directory, "plan.json")
	if err := os.WriteFile(intentPath, []byte(`{"version":1,"baseline_revision":"`+finalizeTestBaseline+`","superseded_by":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := application.runFinalizePreview(t.Context(), []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--pr", "9",
		"--intent-file", intentPath, "--plan-out", planPath}); code != 0 {
		t.Fatalf("preview code=%d stderr=%s", code, application.err.(*bytes.Buffer).String())
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	var plan finalization.Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) != 0 {
		t.Fatalf("persisted Proposal/Design PROCESS comments became preview gate carriers: %+v", plan.Blockers)
	}
	if len(plan.Selection.ActiveProcessIDs) != 1 || plan.Selection.ActiveProcessIDs[0] != "PROCESS-001" {
		t.Fatalf("preview active PROCESS selection=%v", plan.Selection.ActiveProcessIDs)
	}
	if len(plan.Reconcile.Operations) != 0 {
		t.Fatalf("already-final lifecycle unexpectedly produced mutations: %+v", plan.Reconcile.Operations)
	}
	// Keep one frozen, non-gate blocker so the persisted plan has a non-empty
	// blocker representation. Apply must still publish the independent final
	// shared-gate observation, which is the boundary under test here.
	plan.Blockers = []finalization.Blocker{{Code: "manual-approval", Message: "manual approval remains pending"}}
	plan.PlanDigest = ""
	plan.PlanDigest, err = finalization.DigestPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	data, err = finalization.CanonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
	code := application.runFinalizeApply(t.Context(), []string{"--plan", planPath,
		"--checkpoint", filepath.Join(directory, "checkpoint.json"), "--allow-nonatomic", "--json"})
	if code != 1 {
		t.Fatalf("apply code=%d stderr=%s stdout=%s", code, application.err.(*bytes.Buffer).String(), application.out.(*bytes.Buffer).String())
	}
	var result finalizeApplyResult
	if err := json.Unmarshal(application.out.(*bytes.Buffer).Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.FinalGateReady {
		t.Fatalf("persisted Proposal/Design PROCESS comments became apply final-gate carriers: %+v", result)
	}
	if len(result.FinalSelection.ActiveProcessIDs) != 1 || result.FinalSelection.ActiveProcessIDs[0] != "PROCESS-001" {
		t.Fatalf("apply final PROCESS selection=%v", result.FinalSelection.ActiveProcessIDs)
	}
	if backend.writes != 0 {
		t.Fatalf("already-final apply provider writes=%d", backend.writes)
	}
}

func TestFinalizeApplyRejectsSubjectDriftBeforeWrite(t *testing.T) {
	backend := finalizePreviewFixture()
	application := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	application.selectGitHubBackend = ghSelection
	application.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	application.resolveFinalizationBaseline = func(context.Context, string, string) (string, error) { return finalizeTestBaseline, nil }
	directory := t.TempDir()
	intentPath, planPath := filepath.Join(directory, "intent.json"), filepath.Join(directory, "plan.json")
	if err := os.WriteFile(intentPath, []byte(`{"version":1,"baseline_revision":"`+finalizeTestBaseline+`","superseded_by":[{"from":"PROCESS-001","to":"PROCESS-002"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := application.runFinalizePreview(t.Context(), []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--pr", "9",
		"--intent-file", intentPath, "--plan-out", planPath}); code != 0 {
		t.Fatalf("preview code=%d", code)
	}
	backend.pr.Head.SHA = "cccccccccccccccccccccccccccccccccccccccc"
	application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
	code := application.runFinalizeApply(t.Context(), []string{"--plan", planPath, "--checkpoint", filepath.Join(directory, "checkpoint.json"), "--allow-nonatomic"})
	if code != 1 || backend.writes != 0 || !strings.Contains(application.err.(*bytes.Buffer).String(), "subject identity differs") {
		t.Fatalf("apply code=%d writes=%d stderr=%s", code, backend.writes, application.err.(*bytes.Buffer).String())
	}
}

func TestFinalizeApplyRejectsProviderBaseDriftBeforeWrite(t *testing.T) {
	backend := finalizePreviewFixture()
	application := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	application.selectGitHubBackend = ghSelection
	application.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	application.resolveFinalizationBaseline = func(context.Context, string, string) (string, error) { return finalizeTestBaseline, nil }
	directory := t.TempDir()
	intentPath, planPath := filepath.Join(directory, "intent.json"), filepath.Join(directory, "plan.json")
	if err := os.WriteFile(intentPath, []byte(`{"version":1,"baseline_revision":"`+finalizeTestBaseline+`","superseded_by":[{"from":"PROCESS-001","to":"PROCESS-002"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := application.runFinalizePreview(t.Context(), []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--pr", "9",
		"--intent-file", intentPath, "--plan-out", planPath}); code != 0 {
		t.Fatalf("preview code=%d", code)
	}
	backend.pr.Base.SHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
	code := application.runFinalizeApply(t.Context(), []string{"--plan", planPath, "--checkpoint", filepath.Join(directory, "checkpoint.json"), "--allow-nonatomic"})
	if code != 1 || backend.writes != 0 || !strings.Contains(application.err.(*bytes.Buffer).String(), "provider base revision differs") {
		t.Fatalf("apply code=%d writes=%d stderr=%s", code, backend.writes, application.err.(*bytes.Buffer).String())
	}
}

func TestRunRemainingFinalizationOperationsResumesCheckpoint(t *testing.T) {
	firstBody := finalizeTypedBody("PROCESS", "PROCESS-001", "done", nil, "first")
	secondBody := finalizeTypedBody("PROCESS", "PROCESS-002", "in-progress", nil, "second")
	comments := []github.Comment{
		{ID: 1, HTMLURL: "https://github.com/o/r/issues/3#issuecomment-1", URL: "https://api.github.com/comments/1", Body: firstBody},
		{ID: 2, HTMLURL: "https://github.com/o/r/issues/3#issuecomment-2", URL: "https://api.github.com/comments/2", Body: secondBody},
	}
	updates := []int64{}
	backend := fakeGitHubBackend{
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return append([]github.Comment(nil), comments...), nil
		},
		updateComment: func(_ context.Context, _ string, id int64, body string) (github.Comment, error) {
			updates = append(updates, id)
			for i := range comments {
				if comments[i].ID == id {
					comments[i].Body = body
					return comments[i], nil
				}
			}
			return github.Comment{}, errors.New("missing")
		},
	}
	plan := reconcile.Plan{Version: reconcile.PlanVersion, Repo: "o/r", AllowNonAtomic: true, Operations: []reconcile.Operation{
		{ID: "one", Kind: "transition", Target: reconcile.Target{Issue: 3, Type: "PROCESS", ID: "PROCESS-001"}, Desired: reconcile.Desired{Status: "done"}},
		{ID: "two", Kind: "transition", DependsOn: []string{"one"}, Target: reconcile.Target{Issue: 3, Type: "PROCESS", ID: "PROCESS-002"}, Desired: reconcile.Desired{Status: "done"},
			Precondition: reconcile.Precondition{BodyDigest: model.RepresentationDigest(secondBody)}},
	}}
	_, digest, err := reconcile.Validate(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.PlanDigest = digest
	checkpointPath := filepath.Join(t.TempDir(), "checkpoint.json")
	checkpoint := reconcile.Checkpoint{Version: 1, PlanDigest: digest, Completed: map[string]string{"one": "updated"}}
	if err := reconcile.SaveCheckpoint(checkpointPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	result, err := runRemainingFinalizationOperations(t.Context(), backend, plan, checkpointPath, checkpoint)
	if err != nil || !result.OK {
		t.Fatalf("resume result=%+v err=%v", result, err)
	}
	if len(updates) != 1 || updates[0] != 2 || model.ParseTypedComment(comments[1].Body).Status != "done" {
		t.Fatalf("updates=%v comments=%+v", updates, comments)
	}
	reloaded, err := reconcile.LoadCheckpoint(checkpointPath, digest)
	if err != nil || len(reloaded.Completed) != 2 {
		t.Fatalf("checkpoint=%+v err=%v", reloaded, err)
	}
}

func TestValidateFinalizationRepresentationsAcceptsOnlyPlannedHalfLink(t *testing.T) {
	leftURL := "https://github.com/o/r/issues/3#issuecomment-1"
	rightURL := "https://github.com/o/r/issues/3#issuecomment-2"
	leftBefore := finalizeTypedBody("PROCESS", "PROCESS-001", "in-progress", nil, "left")
	rightBefore := finalizeTypedBody("PROCESS", "PROCESS-002", "in-progress", nil, "right")
	leftAfter, changed, err := model.AddRelatedCommentLink(leftBefore, rightURL)
	if err != nil || !changed {
		t.Fatalf("plan half-link: changed=%v err=%v", changed, err)
	}
	comments := []github.Comment{
		{ID: 1, HTMLURL: leftURL, URL: "https://api.github.com/comments/1", Body: leftAfter},
		{ID: 2, HTMLURL: rightURL, URL: "https://api.github.com/comments/2", Body: rightBefore},
	}
	backend := fakeGitHubBackend{listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
		return append([]github.Comment(nil), comments...), nil
	}}
	leftTarget := reconcile.Target{Issue: 3, Type: "PROCESS", ID: "PROCESS-001"}
	rightTarget := reconcile.Target{Issue: 3, Type: "PROCESS", ID: "PROCESS-002"}
	plan := finalization.Plan{Repository: "o/r", Representations: []finalization.Representation{
		{Issue: 3, CommentID: 1, URL: leftURL, APIURL: comments[0].URL, Type: "PROCESS", ID: "PROCESS-001", RepresentationDigest: model.RepresentationDigest(leftBefore)},
		{Issue: 3, CommentID: 2, URL: rightURL, APIURL: comments[1].URL, Type: "PROCESS", ID: "PROCESS-002", RepresentationDigest: model.RepresentationDigest(rightBefore)},
	}, Reconcile: reconcile.Plan{Operations: []reconcile.Operation{{ID: "link", Kind: "link", Target: leftTarget,
		Desired: reconcile.Desired{Peer: &rightTarget}, Precondition: reconcile.Precondition{Endpoints: []reconcile.EndpointPrecondition{
			{Target: leftTarget, BodyDigest: model.RepresentationDigest(leftBefore), AfterDigest: model.RepresentationDigest(leftAfter)},
			{Target: rightTarget, BodyDigest: model.RepresentationDigest(rightBefore), AfterDigest: model.RepresentationDigest(rightBefore)},
		}}}}}}
	if _, err := validateFinalizationRepresentations(t.Context(), backend, plan, map[string]string{}); err != nil {
		t.Fatalf("planned half-link was not resumable: %v", err)
	}
	comments[0].Body = leftAfter + "unplanned drift\n"
	if _, err := validateFinalizationRepresentations(t.Context(), backend, plan, map[string]string{}); err == nil {
		t.Fatal("unplanned half-link representation drift was accepted")
	}
}

func finalizePreviewFixture() *finalizeCommandBackend {
	taskURL := "https://github.com/o/r/issues/1#issuecomment-10"
	p1URL := "https://github.com/o/r/issues/3#issuecomment-11"
	p2URL := "https://github.com/o/r/issues/3#issuecomment-12"
	prURL := "https://github.com/o/r/pull/9"
	backend := &finalizeCommandBackend{issues: map[int]github.Issue{}, comments: map[int][]github.Comment{}}
	backend.info = github.BackendInfo{Name: "fake", Kind: "test", Host: "github.com"}
	backend.issues[1] = github.Issue{Number: 1, HTMLURL: "https://github.com/o/r/issues/1", Body: "<!-- issue-spec:issue=proposal change=x version=1 -->\n"}
	backend.issues[2] = github.Issue{Number: 2, HTMLURL: "https://github.com/o/r/issues/2", Body: "<!-- issue-spec:issue=design change=x version=1 -->\n- Proposal Issue: 1\n"}
	backend.issues[3] = github.Issue{Number: 3, HTMLURL: "https://github.com/o/r/issues/3", Body: "<!-- issue-spec:issue=implement change=x version=1 -->\n- Design Issue: 2\n"}
	backend.pr = github.PullRequest{Number: 9, HTMLURL: prURL}
	backend.pr.Head.SHA, backend.pr.Head.Ref = finalizeTestHead, "feature"
	backend.pr.Base.SHA, backend.pr.Base.Ref = finalizeTestProviderBase, "main"
	backend.comments[1] = []github.Comment{{ID: 10, HTMLURL: taskURL, URL: "https://api.github.com/comments/10",
		Body: finalizeTypedBody("TASK", "TASK-001", "confirmed", map[string][]string{"Related Comments": {p1URL, p2URL}}, "task")}}
	backend.comments[3] = []github.Comment{
		{ID: 11, HTMLURL: p1URL, URL: "https://api.github.com/comments/11", Body: finalizeTypedBody("PROCESS", "PROCESS-001", "in-progress",
			map[string][]string{"Related Comments": {taskURL}, "PR": {prURL}}, "### Execution Class\n\nchange-bearing")},
		{ID: 12, HTMLURL: p2URL, URL: "https://api.github.com/comments/12", Body: finalizeTypedBody("PROCESS", "PROCESS-002", "in-progress",
			map[string][]string{"Related Comments": {taskURL}, "PR": {prURL}}, "### Execution Class\n\nchange-bearing")},
	}
	return backend
}

func finalizeSevenEdgeFixture(t *testing.T) (*finalizeCommandBackend, []byte, map[string]string, map[string]string) {
	t.Helper()
	backend := finalizePreviewFixture()
	taskURL := backend.comments[1][0].HTMLURL
	prURL := backend.pr.HTMLURL
	historical := map[string]string{}
	stableBodies := map[string]string{}
	var processURLs []string
	var comments []github.Comment
	intent := finalization.Intent{Version: finalization.IntentVersion, BaselineRevision: finalizeTestBaseline}
	for index := 1; index <= 7; index++ {
		fromID := fmt.Sprintf("PROCESS-%03d", index)
		toID := fmt.Sprintf("PROCESS-%03d", index+100)
		fromCommentID, toCommentID := int64(100+index), int64(200+index)
		fromURL := fmt.Sprintf("https://github.com/o/r/issues/3#issuecomment-%d", fromCommentID)
		toURL := fmt.Sprintf("https://github.com/o/r/issues/3#issuecomment-%d", toCommentID)
		links := map[string][]string{"Related Comments": {taskURL}, "PR": {prURL}}
		fromBody := finalizeTypedBody("PROCESS", fromID, "in-progress", links,
			fmt.Sprintf("### Execution Class\n\nchange-bearing\n\nhistorical-%d", index))
		toBody := finalizeTypedBody("PROCESS", toID, "done", links,
			fmt.Sprintf("### Execution Class\n\nchange-bearing\n\nsuccessor-%d", index))
		comments = append(comments,
			github.Comment{ID: fromCommentID, IssueNumber: 3, HTMLURL: fromURL, URL: fmt.Sprintf("https://api.github.com/comments/%d", fromCommentID), Body: fromBody},
			github.Comment{ID: toCommentID, IssueNumber: 3, HTMLURL: toURL, URL: fmt.Sprintf("https://api.github.com/comments/%d", toCommentID), Body: toBody})
		processURLs = append(processURLs, fromURL, toURL)
		historical[fromID] = toID
		stableBodies[toID] = toBody
		intent.SupersededBy = append(intent.SupersededBy, finalization.IntentEdge{From: fromID, To: toID})
	}
	backend.comments[3] = comments
	taskBody := finalizeTypedBody("TASK", "TASK-001", "done", map[string][]string{"Related Comments": processURLs}, "task")
	backend.comments[1][0].Body = taskBody
	backend.comments[1][0].IssueNumber = 1
	stableBodies["TASK-001"] = taskBody
	specURL := "https://github.com/o/r/issues/1#issuecomment-20"
	specBody := finalizeTypedBody("SPEC", "SPEC-001", "confirmed", nil,
		"## Requirement: peer stability\n\nPeer bodies MUST remain stable.\n\n### Scenario: no peer writes\n\n- **WHEN** finalization applies\n- **THEN** only historical owners change")
	backend.comments[1] = append(backend.comments[1], github.Comment{ID: 20, IssueNumber: 1, HTMLURL: specURL,
		URL: "https://api.github.com/comments/20", Body: specBody})
	stableBodies["SPEC-001"] = specBody

	reviewReceipt := testSealedReviewReceipt(t, "approve", nil)
	reviewBody, err := renderSubmittedReview("REVIEW-001", "PROCESS-101", comments[1].HTMLURL, prURL, []string{specURL}, reviewReceipt)
	if err != nil {
		t.Fatal(err)
	}
	verifyReceipt := testSealedVerificationReceipt(t, []assignment.TestResult{{ID: "unit", Command: "go test ./internal/finalization",
		Outcome: assignment.TestPassed, Assurance: assignment.AssuranceSelfReported}}, nil)
	verifyBody, err := renderSubmittedVerification("VERIFY-001", comments[3].HTMLURL, []string{"SPEC-001"}, verifyReceipt,
		nil, testVerificationSubmission("Verifier"), specURL)
	if err != nil {
		t.Fatal(err)
	}
	backend.comments[3] = append(backend.comments[3],
		github.Comment{ID: 300, IssueNumber: 3, HTMLURL: "https://github.com/o/r/issues/3#issuecomment-300",
			URL: "https://api.github.com/comments/300", Body: reviewBody},
		github.Comment{ID: 301, IssueNumber: 3, HTMLURL: "https://github.com/o/r/issues/3#issuecomment-301",
			URL: "https://api.github.com/comments/301", Body: verifyBody})
	stableBodies["REVIEW-001"] = reviewBody
	stableBodies["VERIFY-001"] = verifyBody
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	return backend, data, historical, stableBodies
}

func finalizeImplementBoundaryFixture(t *testing.T) *finalizeCommandBackend {
	t.Helper()
	backend := finalizePreviewFixture()
	prURL := backend.pr.HTMLURL
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	currentContent := strings.Replace(canonicalProcessContentWithClass(model.ProcessExecutionOrchestration),
		"### Handoff\n\nN/A", "### Handoff\n\nCoordinates the exact final lifecycle without changing code.", 1)
	currentContent = strings.Replace(currentContent, "### Covers\n\n- TASK-001", "### Covers\n\n- TASK-001\n- SPEC-001", 1)
	current := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", currentContent)
	foreignProposal := typedArtifact(t, 1, "PROCESS", "PROCESS-901", "done", canonicalProcessContentWithClass(model.ProcessExecutionChangeBearing))
	foreignDesign := typedArtifact(t, 2, "PROCESS", "PROCESS-902", "done", canonicalProcessContentWithClass(model.ProcessExecutionChangeBearing))
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	artifacts := []*model.Artifact{&spec, &task, &current, &foreignProposal, &foreignDesign, &verify}
	for index, artifact := range artifacts {
		artifact.URL = fmt.Sprintf("https://github.com/o/r/issues/%d#issuecomment-%d", artifact.Issue, 100+index)
		artifact.APIURL = fmt.Sprintf("https://api.github.com/comments/%d", 100+index)
	}
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &current)
	linkArtifacts(t, &spec, &current)
	linkArtifacts(t, &task, &foreignProposal)
	linkArtifacts(t, &task, &foreignDesign)
	currentLinks := make(map[string][]string, len(current.Comment.Links)+1)
	for name, urls := range current.Comment.Links {
		currentLinks[name] = append([]string(nil), urls...)
	}
	currentLinks["PR"] = []string{prURL}
	currentBody, err := model.EnsureTypedBody("PROCESS", current.Comment.ID, model.LogicalBody(current.Comment.Body),
		model.BodyOptions{Status: current.Comment.Status, Links: currentLinks})
	if err != nil {
		t.Fatal(err)
	}
	current.Comment = model.ParseTypedComment(currentBody)
	backend.comments = map[int][]github.Comment{}
	for index, artifact := range artifacts {
		backend.comments[artifact.Issue] = append(backend.comments[artifact.Issue], github.Comment{
			ID: int64(100 + index), HTMLURL: artifact.URL, URL: artifact.APIURL, Body: artifact.Comment.Body,
		})
	}
	return backend
}

func finalizeTypedBody(typ, id, status string, links map[string][]string, logical string) string {
	return model.RenderMarker(typ, id, 1) + "\n" + model.RenderHeader(typ, id, model.BodyOptions{
		Agent: "Worker", Status: status, Scope: "test", Links: links,
	}) + "\n\n" + logical + "\n"
}

var _ github.Backend = (*finalizeCommandBackend)(nil)
var _ github.PullRequestCommitBackend = (*finalizeCommandBackend)(nil)
