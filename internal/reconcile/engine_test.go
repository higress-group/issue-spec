package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

type fakeBackend struct {
	comments     map[int][]github.Comment
	versions     map[int64]int64
	nextID       int64
	createLost   bool
	failUpdateID int64
	writes       int
	listCalls    map[int]int
	listHook     func(*fakeBackend, int, int)
}

type plainBackend struct{ github.IssueBackend }

func newFake() *fakeBackend {
	return &fakeBackend{comments: map[int][]github.Comment{}, versions: map[int64]int64{}, nextID: 10, listCalls: map[int]int{}}
}
func (f *fakeBackend) BackendInfo() github.BackendInfo { return github.BackendInfo{Name: "fake"} }
func (f *fakeBackend) GetUser(context.Context) (github.User, []string, error) {
	return github.User{}, nil, nil
}
func (f *fakeBackend) CreateIssue(context.Context, string, string, string, []string) (github.Issue, error) {
	return github.Issue{}, errors.New("unused")
}
func (f *fakeBackend) GetIssue(context.Context, string, int) (github.Issue, error) {
	return github.Issue{}, errors.New("unused")
}
func (f *fakeBackend) UpdateIssue(context.Context, string, int, github.UpdateIssueOptions) (github.Issue, error) {
	return github.Issue{}, errors.New("unused")
}
func (f *fakeBackend) CreateLabel(context.Context, string, string, string, string) (github.LabelResult, error) {
	return github.LabelResult{}, errors.New("unused")
}
func (f *fakeBackend) ListIssueComments(_ context.Context, _ string, issue int) ([]github.Comment, error) {
	f.listCalls[issue]++
	if f.listHook != nil {
		f.listHook(f, issue, f.listCalls[issue])
	}
	return append([]github.Comment(nil), f.comments[issue]...), nil
}
func (f *fakeBackend) CreateComment(_ context.Context, _ string, issue int, body string) (github.Comment, error) {
	f.nextID++
	f.writes++
	c := github.Comment{ID: f.nextID, IssueNumber: issue, HTMLURL: fmt.Sprintf("https://x/o/r/issues/%d#issuecomment-%d", issue, f.nextID), Body: body}
	f.comments[issue] = append(f.comments[issue], c)
	f.versions[c.ID] = 1
	if f.createLost {
		f.createLost = false
		return github.Comment{}, errors.New("temporary lost response")
	}
	return c, nil
}
func (f *fakeBackend) UpdateComment(_ context.Context, _ string, id int64, body string) (github.Comment, error) {
	return f.update(id, body)
}
func (f *fakeBackend) GetCommentRepresentation(_ context.Context, _ string, id int64) (github.CommentRepresentation, error) {
	for _, cs := range f.comments {
		for _, c := range cs {
			if c.ID == id {
				return github.CommentRepresentation{Comment: c, RepresentationVersion: f.versions[id]}, nil
			}
		}
	}
	return github.CommentRepresentation{}, errors.New("missing")
}
func (f *fakeBackend) UpdateCommentConditional(_ context.Context, _ string, id, expected int64, body string) (github.CommentRepresentation, error) {
	if f.versions[id] != expected {
		return github.CommentRepresentation{}, &github.CommentMutationConflictError{Expected: expected, Current: f.versions[id]}
	}
	c, err := f.update(id, body)
	return github.CommentRepresentation{Comment: c, RepresentationVersion: f.versions[id]}, err
}
func (f *fakeBackend) update(id int64, body string) (github.Comment, error) {
	if f.failUpdateID == id {
		f.failUpdateID = 0
		return github.Comment{}, &github.APIError{StatusCode: http.StatusTooManyRequests}
	}
	for issue, cs := range f.comments {
		for i := range cs {
			if cs[i].ID == id {
				f.writes++
				cs[i].Body = body
				f.comments[issue] = cs
				f.versions[id]++
				return cs[i], nil
			}
		}
	}
	return github.Comment{}, errors.New("missing")
}

func TestReconcileLostCreateResponseResumesUnchanged(t *testing.T) {
	f := newFake()
	f.createLost = true
	body := typedBody(t, "TASK", "TASK-001", "confirmed")
	plan := Plan{Version: 1, Repo: "o/r", AllowNonAtomic: true, Operations: []Operation{{ID: "create", Kind: "upsert", Target: Target{Issue: 1, Type: "TASK", ID: "TASK-001"}, Desired: Desired{Body: body}}}}
	cp := t.TempDir() + "/cp.json"
	first, err := (Engine{Backend: f}).Run(context.Background(), plan, cp)
	if err != nil {
		t.Fatal(err)
	}
	if first.Pending != 1 || first.Atomic || len(f.comments[1]) != 1 {
		t.Fatalf("first=%+v comments=%d", first, len(f.comments[1]))
	}
	second, err := (Engine{Backend: f}).Run(context.Background(), plan, cp)
	if err != nil {
		t.Fatal(err)
	}
	if !second.OK || second.Unchanged != 1 || len(f.comments[1]) != 1 {
		t.Fatalf("second=%+v", second)
	}
}

func TestReconcilePartialBacklinkRateLimitResumes(t *testing.T) {
	f := newFake()
	addComment(f, 1, 1, typedBody(t, "SPEC", "SPEC-001", "confirmed"))
	addComment(f, 2, 2, typedBody(t, "TASK", "TASK-001", "confirmed"))
	f.failUpdateID = 2
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "link", Kind: "link", Target: Target{Issue: 1, Type: "SPEC", ID: "SPEC-001"}, Desired: Desired{Peer: &Target{Issue: 2, Type: "TASK", ID: "TASK-001"}}}}}
	cp := t.TempDir() + "/cp.json"
	first, err := (Engine{Backend: f}).Run(context.Background(), plan, cp)
	if err != nil {
		t.Fatal(err)
	}
	if first.Pending != 1 || first.Atomic || first.Operations[0].Atomic || len(model.RelatedCommentURLs(model.ParseTypedComment(f.comments[1][0].Body))) != 1 {
		t.Fatalf("first=%+v", first)
	}
	second, err := (Engine{Backend: f}).Run(context.Background(), plan, cp)
	if err != nil {
		t.Fatal(err)
	}
	if !second.OK || second.Updated != 1 || len(model.RelatedCommentURLs(model.ParseTypedComment(f.comments[2][0].Body))) != 1 {
		t.Fatalf("second=%+v", second)
	}
}

func TestReconcileStrictCreateRequiresNonAtomicAcknowledgement(t *testing.T) {
	f := newFake()
	body := typedBody(t, "TASK", "TASK-001", "confirmed")
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "create", Kind: "upsert", Target: Target{Issue: 1, Type: "TASK", ID: "TASK-001"}, Desired: Desired{Body: body}}}}

	result, err := (Engine{Backend: f}).Run(context.Background(), plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Conflicted != 1 || f.writes != 0 {
		t.Fatalf("result=%+v writes=%d", result, f.writes)
	}

	plan.AllowNonAtomic = true
	result, err = (Engine{Backend: f}).Run(context.Background(), plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Created != 1 || result.Atomic || result.Operations[0].Atomic || f.writes != 1 {
		t.Fatalf("result=%+v writes=%d", result, f.writes)
	}
}

func TestReconcileCreateReobservesDuplicateLogicalMarker(t *testing.T) {
	f := newFake()
	body := typedBody(t, "TASK", "TASK-001", "confirmed")
	f.listHook = func(f *fakeBackend, issue, call int) {
		if issue == 1 && call == 3 {
			addComment(f, issue, 99, body)
		}
	}
	plan := Plan{Version: 1, Repo: "o/r", AllowNonAtomic: true, Operations: []Operation{{ID: "create", Kind: "upsert", Target: Target{Issue: 1, Type: "TASK", ID: "TASK-001"}, Desired: Desired{Body: body}}}}

	result, err := (Engine{Backend: f}).Run(context.Background(), plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Conflicted != 1 || f.writes != 1 || !strings.Contains(result.Operations[0].Message, "duplicate logical marker") {
		t.Fatalf("result=%+v writes=%d", result, f.writes)
	}
}

func TestReconcileLinkRecomputesFromReobservedPeerBody(t *testing.T) {
	f := newFake()
	addComment(f, 1, 1, typedBody(t, "SPEC", "SPEC-001", "confirmed"))
	addComment(f, 2, 2, typedBody(t, "TASK", "TASK-001", "confirmed"))
	f.listHook = func(f *fakeBackend, issue, call int) {
		if issue == 2 && call == 3 {
			f.comments[issue][0].Body += "\n\nConcurrent human note."
			f.versions[2]++
		}
	}
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "link", Kind: "link", Target: Target{Issue: 1, Type: "SPEC", ID: "SPEC-001"}, Desired: Desired{Peer: &Target{Issue: 2, Type: "TASK", ID: "TASK-001"}}}}}

	result, err := (Engine{Backend: f}).Run(context.Background(), plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Updated != 1 || result.Atomic || !strings.Contains(f.comments[2][0].Body, "Concurrent human note.") || len(model.RelatedCommentURLs(model.ParseTypedComment(f.comments[2][0].Body))) != 1 {
		t.Fatalf("result=%+v peer=%q", result, f.comments[2][0].Body)
	}
}

func TestReconcilePrewriteDuplicateAndIllegalTransitionDoZeroWrites(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeBackend)
		op    Operation
	}{
		{"duplicate", func(f *fakeBackend) {
			b := typedBody(t, "TASK", "TASK-001", "confirmed")
			addComment(f, 1, 1, b)
			addComment(f, 1, 2, b)
		}, Operation{ID: "x", Kind: "transition", Target: Target{Issue: 1, Type: "TASK", ID: "TASK-001"}, Desired: Desired{Status: "done"}}},
		{"illegal", func(f *fakeBackend) { addComment(f, 1, 1, typedBody(t, "TASK", "TASK-001", "done")) }, Operation{ID: "x", Kind: "transition", Target: Target{Issue: 1, Type: "TASK", ID: "TASK-001"}, Desired: Desired{Status: "in-progress"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			tc.setup(f)
			_, err := (Engine{Backend: f}).Run(context.Background(), Plan{Version: 1, Repo: "o/r", Operations: []Operation{tc.op}}, "")
			if err == nil || f.writes != 0 {
				t.Fatalf("err=%v writes=%d", err, f.writes)
			}
		})
	}
}

func TestReconcileStrictConditionalDefaultAndPlanAcknowledgement(t *testing.T) {
	f := newFake()
	addComment(f, 1, 1, typedBody(t, "TASK", "TASK-001", "confirmed"))
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "transition", Kind: "transition", Target: Target{Issue: 1, Type: "TASK", ID: "TASK-001"}, Desired: Desired{Status: "done"}}}}
	strict, err := (Engine{Backend: plainBackend{IssueBackend: f}}).Run(context.Background(), plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if strict.Conflicted != 1 || f.writes != 0 {
		t.Fatalf("strict=%+v writes=%d", strict, f.writes)
	}
	plan.AllowNonAtomic = true
	accepted, err := (Engine{Backend: plainBackend{IssueBackend: f}}).Run(context.Background(), plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if !accepted.OK || accepted.Updated != 1 || accepted.Operations[0].Atomic {
		t.Fatalf("accepted=%+v", accepted)
	}
}

func TestValidateDAGDigestAndCheckpointMismatch(t *testing.T) {
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "a", Kind: "upsert", DependsOn: []string{"b"}, Target: Target{Issue: 1, Type: "TASK", ID: "A"}, Desired: Desired{Body: "x"}}, {ID: "b", Kind: "upsert", DependsOn: []string{"a"}, Target: Target{Issue: 1, Type: "TASK", ID: "B"}, Desired: Desired{Body: "x"}}}}
	if _, _, err := Validate(plan); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("err=%v", err)
	}
	path := t.TempDir() + "/cp.json"
	if err := SaveCheckpoint(path, Checkpoint{Version: 1, PlanDigest: "one", Completed: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint(path, "two"); err == nil {
		t.Fatal("expected digest mismatch")
	}
}

func addComment(f *fakeBackend, issue int, id int64, body string) {
	c := github.Comment{ID: id, IssueNumber: issue, HTMLURL: fmt.Sprintf("https://x/o/r/issues/%d#issuecomment-%d", issue, id), Body: body}
	f.comments[issue] = append(f.comments[issue], c)
	f.versions[id] = 1
}
func typedBody(t *testing.T, kind, id, status string) string {
	t.Helper()
	body, err := model.EnsureTypedBody(kind, id, "## Work\n\ncontent", model.BodyOptions{Agent: "Worker", Status: status, Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
