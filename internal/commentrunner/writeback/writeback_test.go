package writeback

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

type fakeBackend struct {
	comments  []github.Comment
	created   []string
	updated   []int64
	listErr   error
	createErr error
	updateErr error
}

func (f *fakeBackend) ListIssueComments(context.Context, string, int) ([]github.Comment, error) {
	return f.comments, f.listErr
}
func (f *fakeBackend) CreateComment(context.Context, string, int, string) (github.Comment, error) {
	if f.createErr != nil {
		return github.Comment{}, f.createErr
	}
	f.created = append(f.created, "")
	return github.Comment{ID: int64(len(f.created))}, nil
}
func (f *fakeBackend) UpdateComment(context.Context, string, int64, string) (github.Comment, error) {
	if f.updateErr != nil {
		return github.Comment{}, f.updateErr
	}
	f.updated = append(f.updated, 0)
	return github.Comment{ID: 1}, nil
}

func TestStatusCommentRenderAndBoundedDiagnostics(t *testing.T) {
	body, err := templates.StatusComment(templates.StatusCommentOptions{
		JobID:       "PROCESS-001",
		State:       "running",
		Command:     "/new something",
		Provenance:  "comment#1",
		Diagnostics: []string{strings.Repeat("a", 200), "ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "issue-spec:status job=PROCESS-001 version=1") {
		t.Fatal(body)
	}
	if !strings.Contains(body, "comment#1") {
		t.Fatal(body)
	}
	if !strings.Contains(body, strings.Repeat("a", 119)+"…") {
		t.Fatal(body)
	}
}

func TestUpsertStatusCommentCreatesAndReusesTrackedComment(t *testing.T) {
	rs := &model.RepoState{}
	backend := &fakeBackend{}
	first, err := UpsertStatusComment(context.Background(), backend, rs, Request{Repo: "o/r", IssueNumber: 1, IssueKey: "o/r#1", JobID: "PROCESS-001", State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != 1 || rs.StatusComments["o/r#1"].CommentID != 1 {
		t.Fatalf("state = %+v first=%+v", rs.StatusComments["o/r#1"], first)
	}
	backend.comments = []github.Comment{{ID: 1, Body: first.Body}}
	second, err := UpsertStatusComment(context.Background(), backend, rs, Request{Repo: "o/r", IssueNumber: 1, IssueKey: "o/r#1", JobID: "PROCESS-001", State: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if len(backend.updated) != 1 || second.ID != 1 {
		t.Fatalf("updated=%v second=%+v", backend.updated, second)
	}
}

func TestStatusMarkerParseRejectsUnsafeMarkers(t *testing.T) {
	if _, ok := ParseStatusMarker("<!-- issue-spec:status job=PROCESS-001 version=1 extra=bad -->"); ok {
		t.Fatal("unsafe marker accepted")
	}
	if marker, ok := ParseStatusMarker("<!-- issue-spec:status job=PROCESS-001 version=1 -->"); !ok || marker.JobID != "PROCESS-001" || marker.Version != 1 {
		t.Fatal("safe marker rejected")
	}
}

func TestStatusWordingInterruptedAndCancelled(t *testing.T) {
	interrupted, err := templates.StatusComment(templates.StatusCommentOptions{JobID: "PROCESS-001", State: "running", Interrupted: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(interrupted, "Interrupted.") {
		t.Fatal(interrupted)
	}
	cancelled, err := templates.StatusComment(templates.StatusCommentOptions{JobID: "PROCESS-001", State: "running", Cancelled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cancelled, "Cancelled.") {
		t.Fatal(cancelled)
	}
}

func TestUpsertStatusCommentBackendFailures(t *testing.T) {
	rs := &model.RepoState{}
	backend := &fakeBackend{listErr: errors.New("transient")}
	if _, err := UpsertStatusComment(context.Background(), backend, rs, Request{Repo: "o/r", IssueNumber: 1, JobID: "PROCESS-001"}); err == nil {
		t.Fatal("expected list failure")
	}
}
