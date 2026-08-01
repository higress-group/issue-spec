package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

type fakeBackend struct {
	comments                map[int][]github.Comment
	versions                map[int64]int64
	nextID                  int64
	createLost              bool
	updateLost              bool
	failUpdateID            int64
	writes                  int
	listCalls               map[int]int
	listHook                func(*fakeBackend, int, int)
	conditionalResponseBody string
	conditionalHook         func(*fakeBackend, int64)
	conditionalLost         bool
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
func (f *fakeBackend) ListIssues(context.Context, string, github.ListIssueOptions) ([]github.Issue, error) {
	return nil, errors.New("unused")
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
	comment, err := f.update(id, body)
	if err == nil && f.updateLost {
		f.updateLost = false
		return github.Comment{}, &github.APIError{StatusCode: http.StatusServiceUnavailable}
	}
	return comment, err
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
	if f.conditionalHook != nil {
		f.conditionalHook(f, id)
	}
	if f.versions[id] != expected {
		return github.CommentRepresentation{}, &github.CommentMutationConflictError{Expected: expected, Current: f.versions[id]}
	}
	c, err := f.update(id, body)
	if err == nil && f.conditionalLost {
		f.conditionalLost = false
		return github.CommentRepresentation{}, errors.New("temporary conditional response lost")
	}
	if err == nil && f.conditionalResponseBody != "" {
		c.Body = f.conditionalResponseBody
	}
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
	if operation := second.Operations[0]; operation.BeforeDigest != model.RepresentationDigest(body) || operation.AfterDigest != operation.BeforeDigest {
		t.Fatalf("unchanged operation digests=%+v", operation)
	}
}

func TestReconcilePartialBacklinkRateLimitResumes(t *testing.T) {
	t.Skip("superseded by reconcile v2 owner-only relationship updates")
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

func TestValidateLinkEndpointPreconditionContract(t *testing.T) {
	target := Target{Issue: 1, Type: "SPEC", ID: "SPEC-001"}
	peer := Target{Issue: 2, Type: "TASK", ID: "TASK-001"}
	digestA, digestB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	valid := []EndpointPrecondition{
		{Target: target, BodyDigest: digestA, AfterDigest: digestB},
		{Target: peer, RepresentationVersion: 1, AfterDigest: digestA},
	}
	operation := func(endpoints []EndpointPrecondition) Operation {
		return Operation{ID: "link", Kind: "link", Target: target, Desired: Desired{Peer: &peer},
			Precondition: Precondition{Endpoints: endpoints}}
	}
	if _, _, err := Validate(Plan{Version: 1, Repo: "o/r", Operations: []Operation{operation(valid)}}); err != nil {
		t.Fatalf("valid endpoint contract: %v", err)
	}
	for _, test := range []struct {
		name string
		op   Operation
	}{
		{name: "missing primary", op: operation(valid[1:])},
		{name: "duplicate", op: operation([]EndpointPrecondition{valid[0], valid[0], valid[1]})},
		{name: "foreign", op: operation([]EndpointPrecondition{valid[0], {Target: Target{Issue: 3, Type: "TASK", ID: "TASK-003"}, BodyDigest: digestA, AfterDigest: digestB}})},
		{name: "ambiguous before", op: operation([]EndpointPrecondition{{Target: target, RepresentationVersion: 1, BodyDigest: digestA, AfterDigest: digestB}, valid[1]})},
		{name: "missing before", op: operation([]EndpointPrecondition{{Target: target, AfterDigest: digestB}, valid[1]})},
		{name: "missing after", op: operation([]EndpointPrecondition{{Target: target, BodyDigest: digestA}, valid[1]})},
		{name: "legacy overlap", op: func() Operation {
			op := operation(valid)
			op.Precondition.BodyDigest = digestA
			return op
		}()},
		{name: "same endpoint", op: func() Operation {
			op := operation(valid[:1])
			op.Desired.Peer = &target
			return op
		}()},
		{name: "non-link", op: Operation{ID: "transition", Kind: "transition", Target: target,
			Desired: Desired{Status: "done"}, Precondition: Precondition{Endpoints: valid}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := Validate(Plan{Version: 1, Repo: "o/r", Operations: []Operation{test.op}}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestReconcileV1IncompleteLinkRequiresReplanWithoutProviderIO(t *testing.T) {
	f := newFake()
	plan := Plan{Version: LegacyPlanVersion, Repo: "o/r", Operations: []Operation{{ID: "legacy-link", Kind: "link",
		Target:  Target{Issue: 1, Type: "SPEC", ID: "SPEC-001"},
		Desired: Desired{Peer: &Target{Issue: 2, Type: "TASK", ID: "TASK-001"}}}}}
	result, err := (Engine{Backend: f}).Run(context.Background(), plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Conflicted != 1 || result.Remediation != "legacy_link_plan_requires_replan" ||
		len(result.Operations) != 1 || result.Operations[0].Message != "legacy_link_plan_requires_replan" ||
		f.writes != 0 || len(f.listCalls) != 0 {
		t.Fatalf("result=%+v writes=%d reads=%+v", result, f.writes, f.listCalls)
	}
}

func TestReconcileV1CompletedLinkCheckpointIsReadableWithoutProviderIO(t *testing.T) {
	f := newFake()
	plan := Plan{Version: LegacyPlanVersion, Repo: "o/r", Operations: []Operation{{ID: "legacy-link", Kind: "link",
		Target:  Target{Issue: 1, Type: "SPEC", ID: "SPEC-001"},
		Desired: Desired{Peer: &Target{Issue: 2, Type: "TASK", ID: "TASK-001"}}}}}
	digest, err := Digest(plan)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := t.TempDir() + "/checkpoint.json"
	if err := SaveCheckpoint(checkpoint, Checkpoint{Version: 1, PlanDigest: digest,
		Completed: map[string]string{"legacy-link": "updated"}}); err != nil {
		t.Fatal(err)
	}
	result, err := (Engine{Backend: f}).Run(context.Background(), plan, checkpoint)
	if err != nil || !result.OK || result.Updated != 1 || len(result.Operations) != 1 ||
		result.Operations[0].Message != "completed legacy v1 checkpoint history" || f.writes != 0 || len(f.listCalls) != 0 {
		t.Fatalf("result=%+v writes=%d reads=%+v err=%v", result, f.writes, f.listCalls, err)
	}
}

func TestReconcileV2RelationshipUpdateWritesOneOwnerAndRecoversLostResponse(t *testing.T) {
	f := newFake()
	ownerBody := strings.Replace(typedBody(t, "TASK", "TASK-001", "confirmed"), "## Work\n\ncontent",
		"## Task\n\n### Covers\n\n- SPEC-001\n\n### Unrelated\n\nkeep", 1)
	peerBody := typedBody(t, "SPEC", "SPEC-001", "confirmed")
	addComment(f, 1, 1, ownerBody)
	addComment(f, 2, 2, peerBody)
	f.conditionalLost = true
	plan := Plan{Version: PlanVersion2, Repo: "o/r", Operations: []Operation{{ID: "owner-update", Kind: "relationship-update",
		Target: Target{Issue: 1, Type: "TASK", ID: "TASK-001"}, Desired: Desired{RelationshipUpdate: &RelationshipUpdate{
			Version: RelationshipUpdateVersion, Add: []RelationshipTarget{{Target: Target{Issue: 2, Type: "SPEC", ID: "SPEC-001"},
				URL: "https://x/o/r/issues/2#issuecomment-2"}}}}}}}
	result, err := (Engine{Backend: f}).Run(context.Background(), plan, "")
	if err != nil || !result.OK || result.Updated != 1 || !result.Atomic || result.Operations[0].Guarantee != github.CommentMutationStrictConditional ||
		f.writes != 1 || f.comments[2][0].Body != peerBody || !strings.Contains(f.comments[1][0].Body, "### Unrelated\n\nkeep") {
		t.Fatalf("result=%+v writes=%d peer_changed=%v err=%v", result, f.writes, f.comments[2][0].Body != peerBody, err)
	}
	firstWrites := f.writes
	second, err := (Engine{Backend: f}).Run(context.Background(), plan, "")
	if err != nil || !second.OK || second.Unchanged != 1 || f.writes != firstWrites {
		t.Fatalf("idempotent result=%+v writes=%d err=%v", second, f.writes, err)
	}
}

func TestReconcileV2RelationshipUpdateStaleConditionalWritesNothing(t *testing.T) {
	f := newFake()
	ownerBody := strings.Replace(typedBody(t, "TASK", "TASK-001", "confirmed"), "## Work\n\ncontent",
		"## Task\n\n### Covers\n\n- SPEC-001", 1)
	addComment(f, 1, 1, ownerBody)
	f.conditionalHook = func(backend *fakeBackend, id int64) {
		backend.conditionalHook = nil
		backend.comments[1][0].Body = strings.Replace(backend.comments[1][0].Body, "Scope: test", "Scope: unrelated-drift", 1)
		backend.versions[id]++
	}
	plan := Plan{Version: PlanVersion2, Repo: "o/r", Operations: []Operation{{ID: "owner-update", Kind: "relationship-update",
		Target: Target{Issue: 1, Type: "TASK", ID: "TASK-001"}, Desired: Desired{RelationshipUpdate: &RelationshipUpdate{
			Version: RelationshipUpdateVersion, Add: []RelationshipTarget{{Target: Target{Issue: 2, Type: "SPEC", ID: "SPEC-001"},
				URL: "https://x/o/r/issues/2#issuecomment-2"}}}}}}}
	result, err := (Engine{Backend: f}).Run(context.Background(), plan, "")
	if err != nil || result.OK || result.Conflicted != 1 || f.writes != 0 || !strings.Contains(result.Operations[0].Message, "representation conflict") {
		t.Fatalf("result=%+v writes=%d err=%v", result, f.writes, err)
	}
}

func TestReconcileReciprocalLinkReportsEveryMutatedEndpoint(t *testing.T) {
	t.Skip("superseded by reconcile v2 owner-only relationship updates")
	for _, conditional := range []bool{true, false} {
		t.Run(map[bool]string{true: "conditional", false: "non-atomic"}[conditional], func(t *testing.T) {
			f := newFake()
			if conditional {
				f.conditionalResponseBody = "mutation response is not the exact provider re-observation"
			}
			leftBefore := typedBody(t, "SPEC", "SPEC-001", "confirmed")
			rightBefore := typedBody(t, "TASK", "TASK-001", "confirmed")
			addComment(f, 1, 1, leftBefore)
			addComment(f, 2, 2, rightBefore)
			leftTarget := Target{Issue: 1, Type: "SPEC", ID: "SPEC-001"}
			rightTarget := Target{Issue: 2, Type: "TASK", ID: "TASK-001"}
			leftAfter, _, _ := model.AddRelatedCommentLink(leftBefore, f.comments[2][0].HTMLURL)
			rightAfter, _, _ := model.AddRelatedCommentLink(rightBefore, f.comments[1][0].HTMLURL)
			endpoints := linkEndpointPreconditions(leftTarget, rightTarget, leftBefore, rightBefore, leftAfter, rightAfter, conditional)
			plan := Plan{Version: 1, Repo: "o/r", AllowNonAtomic: !conditional, Operations: []Operation{{
				ID: "link", Kind: "link", Target: leftTarget, Desired: Desired{Peer: &rightTarget},
				Precondition: Precondition{Endpoints: endpoints},
			}}}
			var backend Backend = f
			if !conditional {
				backend = plainBackend{IssueBackend: f}
			}

			result, err := (Engine{Backend: backend}).Run(t.Context(), plan, "")
			if err != nil || !result.OK || result.Updated != 1 || f.writes != 2 || len(result.Operations[0].Endpoints) != 2 {
				t.Fatalf("result=%+v writes=%d err=%v", result, f.writes, err)
			}
			operation := result.Operations[0]
			if operation.CommentID != f.comments[1][0].ID || operation.BeforeDigest != model.RepresentationDigest(leftBefore) ||
				operation.AfterDigest != model.RepresentationDigest(leftAfter) {
				t.Fatalf("primary compatibility result=%+v", operation)
			}
			assertEndpointResult(t, operation.Endpoints[0], leftTarget, leftBefore, leftAfter)
			assertEndpointResult(t, operation.Endpoints[1], rightTarget, rightBefore, rightAfter)
		})
	}
}

func TestReconcileReciprocalLinkEndpointDriftStopsBeforeFirstWrite(t *testing.T) {
	t.Skip("superseded by reconcile v2 owner-only relationship updates")
	for _, conditional := range []bool{true, false} {
		t.Run(map[bool]string{true: "conditional", false: "non-atomic"}[conditional], func(t *testing.T) {
			f := newFake()
			leftBefore := typedBody(t, "SPEC", "SPEC-001", "confirmed")
			rightBefore := typedBody(t, "TASK", "TASK-001", "confirmed")
			addComment(f, 1, 1, leftBefore)
			addComment(f, 2, 2, rightBefore)
			leftTarget := Target{Issue: 1, Type: "SPEC", ID: "SPEC-001"}
			rightTarget := Target{Issue: 2, Type: "TASK", ID: "TASK-001"}
			leftAfter, _, _ := model.AddRelatedCommentLink(leftBefore, f.comments[2][0].HTMLURL)
			rightAfter, _, _ := model.AddRelatedCommentLink(rightBefore, f.comments[1][0].HTMLURL)
			endpoints := linkEndpointPreconditions(leftTarget, rightTarget, leftBefore, rightBefore, leftAfter, rightAfter, conditional)
			f.comments[2][0].Body += "\nPeer drift after preview."
			f.versions[2]++
			plan := Plan{Version: 1, Repo: "o/r", AllowNonAtomic: !conditional, Operations: []Operation{{
				ID: "link", Kind: "link", Target: leftTarget, Desired: Desired{Peer: &rightTarget},
				Precondition: Precondition{Endpoints: endpoints},
			}}}
			var backend Backend = f
			if !conditional {
				backend = plainBackend{IssueBackend: f}
			}

			result, err := (Engine{Backend: backend}).Run(t.Context(), plan, "")
			if err != nil || result.Conflicted != 1 || f.writes != 0 ||
				!strings.Contains(result.Operations[0].Message, "endpoint") {
				t.Fatalf("result=%+v writes=%d err=%v", result, f.writes, err)
			}
		})
	}
}

func TestReconcileReciprocalLinkEndpointContractResumesPartialWrite(t *testing.T) {
	t.Skip("superseded by reconcile v2 owner-only relationship updates")
	f := newFake()
	leftBefore := typedBody(t, "SPEC", "SPEC-001", "confirmed")
	rightBefore := typedBody(t, "TASK", "TASK-001", "confirmed")
	addComment(f, 1, 1, leftBefore)
	addComment(f, 2, 2, rightBefore)
	leftTarget := Target{Issue: 1, Type: "SPEC", ID: "SPEC-001"}
	rightTarget := Target{Issue: 2, Type: "TASK", ID: "TASK-001"}
	leftAfter, _, _ := model.AddRelatedCommentLink(leftBefore, f.comments[2][0].HTMLURL)
	rightAfter, _, _ := model.AddRelatedCommentLink(rightBefore, f.comments[1][0].HTMLURL)
	f.failUpdateID = 2
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "link", Kind: "link", Target: leftTarget,
		Desired: Desired{Peer: &rightTarget}, Precondition: Precondition{Endpoints: linkEndpointPreconditions(
			leftTarget, rightTarget, leftBefore, rightBefore, leftAfter, rightAfter, true)}}}}
	cp := t.TempDir() + "/checkpoint.json"

	first, err := (Engine{Backend: f}).Run(t.Context(), plan, cp)
	if err != nil || first.Pending != 1 || f.writes != 1 || len(first.Operations[0].Endpoints) != 1 {
		t.Fatalf("first=%+v writes=%d err=%v", first, f.writes, err)
	}
	assertEndpointResult(t, first.Operations[0].Endpoints[0], leftTarget, leftBefore, leftAfter)
	second, err := (Engine{Backend: f}).Run(t.Context(), plan, cp)
	if err != nil || !second.OK || second.Updated != 1 || f.writes != 2 || len(second.Operations[0].Endpoints) != 1 {
		t.Fatalf("second=%+v writes=%d err=%v", second, f.writes, err)
	}
	assertEndpointResult(t, second.Operations[0].Endpoints[0], rightTarget, rightBefore, rightAfter)
}

func TestReconcileLegacyPrimaryLinkPreconditionOnlyBindsPrimaryTarget(t *testing.T) {
	t.Skip("superseded by reconcile v2 owner-only relationship updates")
	f := newFake()
	leftBefore := typedBody(t, "SPEC", "SPEC-001", "confirmed")
	rightBefore := typedBody(t, "TASK", "TASK-001", "confirmed")
	addComment(f, 1, 1, leftBefore)
	addComment(f, 2, 2, rightBefore)
	rightTarget := Target{Issue: 2, Type: "TASK", ID: "TASK-001"}
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "link", Kind: "link",
		Target: Target{Issue: 1, Type: "SPEC", ID: "SPEC-001"}, Desired: Desired{Peer: &rightTarget},
		Precondition: Precondition{BodyDigest: model.RepresentationDigest(leftBefore)}}}}

	result, err := (Engine{Backend: f}).Run(t.Context(), plan, "")
	if err != nil || !result.OK || result.Updated != 1 || f.writes != 2 || result.Operations[0].CommentID != 1 {
		t.Fatalf("result=%+v writes=%d err=%v", result, f.writes, err)
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
	if operation := result.Operations[0]; operation.BeforeDigest != "" || operation.AfterDigest != model.RepresentationDigest(body) {
		t.Fatalf("created operation digests=%+v", operation)
	}
}

func TestReconcileCreateReobservesDuplicateLogicalMarker(t *testing.T) {
	f := newFake()
	body := typedBody(t, "TASK", "TASK-001", "confirmed")
	f.listHook = func(f *fakeBackend, issue, call int) {
		if issue == 1 && call == 3 {
			addComment(f, issue, 99, body+"\n\nConcurrent human note.")
		}
	}
	plan := Plan{Version: 1, Repo: "o/r", AllowNonAtomic: true, Operations: []Operation{{ID: "create", Kind: "upsert", Target: Target{Issue: 1, Type: "TASK", ID: "TASK-001"}, Desired: Desired{Body: body}}}}

	result, err := (Engine{Backend: f}).Run(context.Background(), plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Conflicted != 1 || f.writes != 0 || !strings.Contains(result.Operations[0].Message, "appeared after initial observation") {
		t.Fatalf("result=%+v writes=%d", result, f.writes)
	}
}

func TestReconcileUpsertPreservesEveryTypedHeaderRelationship(t *testing.T) {
	f := newFake()
	existing := typedBodyWithLinks(t, "PROCESS", "PROCESS-002", "in-progress", map[string][]string{
		"Proposal Issue":   {"https://example.test/issues/1"},
		"Design Issue":     {"https://example.test/issues/2"},
		"Implement Issue":  {"https://example.test/issues/3"},
		"Related Comments": {"https://example.test/issues/3#issuecomment-4"},
		"PR":               {"https://example.test/pulls/5"},
	})
	existing = strings.Replace(existing, "- PR: https://example.test/pulls/5",
		"- PR: https://example.test/pulls/5\n- Future Relationship: https://example.test/future/6", 1)
	addComment(f, 3, 2, existing)
	desired := typedBodyWithLinks(t, "PROCESS", "PROCESS-002", "in-progress", map[string][]string{
		"Related Comments": {"https://example.test/issues/3#issuecomment-7"},
	})
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "upsert", Kind: "upsert",
		Target: Target{Issue: 3, Type: "PROCESS", ID: "PROCESS-002"}, Desired: Desired{Body: desired}}}}

	result, err := (Engine{Backend: f}).Run(t.Context(), plan, "")
	if err != nil || !result.OK || result.Updated != 1 || f.writes != 1 {
		t.Fatalf("result=%+v writes=%d err=%v", result, f.writes, err)
	}
	operation := result.Operations[0]
	if operation.BeforeDigest != model.RepresentationDigest(existing) ||
		operation.AfterDigest != model.RepresentationDigest(f.comments[3][0].Body) ||
		operation.BeforeDigest == operation.AfterDigest {
		t.Fatalf("operation digests=%+v", operation)
	}
	links := model.ParseTypedComment(f.comments[3][0].Body).Links
	for name, value := range map[string]string{
		"Proposal Issue": "https://example.test/issues/1", "Design Issue": "https://example.test/issues/2",
		"Implement Issue": "https://example.test/issues/3", "PR": "https://example.test/pulls/5",
		"Future Relationship": "https://example.test/future/6",
	} {
		if !containsLinkValue(links[name], value) {
			t.Errorf("%s missing %s in %v", name, value, links[name])
		}
	}
	for _, value := range []string{"https://example.test/issues/3#issuecomment-4", "https://example.test/issues/3#issuecomment-7"} {
		if !containsLinkValue(links["Related Comments"], value) {
			t.Errorf("Related Comments missing %s in %v", value, links["Related Comments"])
		}
	}
}

func TestReconcileLifecyclePreservesRelationshipsAndUsesExactReobservedDigests(t *testing.T) {
	f := newFake()
	before := typedBodyWithLinks(t, "PROCESS", "PROCESS-002", "in-progress", map[string][]string{
		"Proposal Issue": {"https://example.test/issues/1"},
		"PR":             {"https://example.test/pulls/5"},
	})
	addComment(f, 3, 2, before)
	f.conditionalResponseBody = "provider response body must not define the result digest"
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "transition", Kind: "transition",
		Target: Target{Issue: 3, Type: "PROCESS", ID: "PROCESS-002"}, Desired: Desired{Status: "done"},
		Precondition: Precondition{BodyDigest: model.RepresentationDigest(before)}}}}

	result, err := (Engine{Backend: f}).Run(t.Context(), plan, "")
	if err != nil || !result.OK || result.Updated != 1 || f.writes != 1 {
		t.Fatalf("result=%+v writes=%d err=%v", result, f.writes, err)
	}
	after := f.comments[3][0].Body
	operation := result.Operations[0]
	if operation.BeforeDigest != model.RepresentationDigest(before) || operation.AfterDigest != model.RepresentationDigest(after) ||
		operation.AfterDigest == model.RepresentationDigest(f.conditionalResponseBody) {
		t.Fatalf("operation=%+v response_digest=%s", operation, model.RepresentationDigest(f.conditionalResponseBody))
	}
	links := model.ParseTypedComment(after).Links
	if !containsLinkValue(links["Proposal Issue"], "https://example.test/issues/1") ||
		!containsLinkValue(links["PR"], "https://example.test/pulls/5") {
		t.Fatalf("lifecycle dropped relationships: %v", links)
	}
}

func TestReconcileLinkRecomputesFromReobservedPeerBody(t *testing.T) {
	t.Skip("superseded by reconcile v2 owner-only relationship updates")
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

func TestReconcileAcceptedImplementationReceiptMissingOrMismatchDoesZeroWrites(t *testing.T) {
	const receiptID = "receipt-implementation-1"
	digest := strings.Repeat("a", 64)
	expected := &model.AcceptedReceiptAuthority{Role: assignment.RoleImplementation,
		ReceiptID: receiptID, Digest: digest, Generation: 1}
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing marker", body: typedBody(t, "PROCESS", "PROCESS-001", "in-progress")},
		{name: "mismatched marker", body: acceptedReceiptBody(t, "PROCESS", "PROCESS-001", "in-progress",
			assignment.RoleImplementation, receiptID, strings.Repeat("b", 64), 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFake()
			addComment(f, 1, 1, test.body)
			plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "accept", Kind: "transition",
				Target: Target{Issue: 1, Type: "PROCESS", ID: "PROCESS-001"}, Desired: Desired{Status: "done"},
				Precondition: Precondition{AcceptedReceipt: expected}}}}
			if _, err := (Engine{Backend: f}).Run(context.Background(), plan, ""); err == nil || f.writes != 0 {
				t.Fatalf("err=%v writes=%d", err, f.writes)
			}
		})
	}
}

func TestReconcileAcceptedReviewReceiptMatchesAlreadyDoneCarrier(t *testing.T) {
	const receiptID = "receipt-review-1"
	digest := strings.Repeat("c", 64)
	f := newFake()
	addComment(f, 1, 1, acceptedReceiptBody(t, "REVIEW", "REVIEW-001", "done",
		assignment.RoleReview, receiptID, digest, 2))
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "accept", Kind: "transition",
		Target: Target{Issue: 1, Type: "REVIEW", ID: "REVIEW-001"}, Desired: Desired{Status: "done"},
		Precondition: Precondition{AcceptedReceipt: &model.AcceptedReceiptAuthority{Role: assignment.RoleReview,
			ReceiptID: receiptID, Digest: digest, Generation: 2}}}}}
	result, err := (Engine{Backend: f}).Run(context.Background(), plan, "")
	if err != nil || !result.OK || result.Unchanged != 1 || f.writes != 0 ||
		model.ParseTypedComment(f.comments[1][0].Body).Status != "done" {
		t.Fatalf("result=%+v writes=%d err=%v", result, f.writes, err)
	}
}

func TestReconcileAcceptedReceiptCannotStrengthenCarrierLifecycle(t *testing.T) {
	const receiptID = "receipt-review-1"
	digest := strings.Repeat("c", 64)
	expected := &model.AcceptedReceiptAuthority{Role: assignment.RoleReview,
		ReceiptID: receiptID, Digest: digest, Generation: 2}
	for _, test := range []struct {
		name    string
		status  string
		desired Desired
	}{
		{name: "in-progress to done", status: "in-progress", desired: Desired{Status: "done"}},
		{name: "done to superseded", status: "done", desired: Desired{Status: "superseded"}},
		{name: "done with caller link", status: "done", desired: Desired{Status: "done", RelatedLinks: []string{"https://example.test/forged"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFake()
			addComment(f, 1, 1, acceptedReceiptBody(t, "REVIEW", "REVIEW-001", test.status,
				assignment.RoleReview, receiptID, digest, 2))
			plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "accept", Kind: "transition",
				Target: Target{Issue: 1, Type: "REVIEW", ID: "REVIEW-001"}, Desired: test.desired,
				Precondition: Precondition{AcceptedReceipt: expected}}}}
			if _, err := (Engine{Backend: f}).Run(context.Background(), plan, ""); err == nil || f.writes != 0 {
				t.Fatalf("err=%v writes=%d", err, f.writes)
			}
		})
	}
}

func TestReconcileAcceptedReceiptBackfillsOnlyCarrierAuthorizedRelationshipsWithoutRewritingCarrier(t *testing.T) {
	t.Skip("superseded by reconcile v2 owner-only relationship updates")
	const receiptID = "receipt-verification-1"
	digest := strings.Repeat("d", 64)
	f := newFake()
	carrier := acceptedReceiptBody(t, "VERIFY", "VERIFY-001", "done", assignment.RoleVerification, receiptID, digest, 3)
	for _, targetURL := range []string{
		"https://x/o/r/issues/9#issuecomment-2",
		"https://x/o/r/issues/9#issuecomment-3",
	} {
		var changed bool
		var err error
		carrier, changed, err = model.AddRelatedCommentLink(carrier, targetURL)
		if err != nil || !changed {
			t.Fatalf("seed carrier relationship changed=%t err=%v", changed, err)
		}
	}
	immutableCarrier := carrier
	addComment(f, 9, 1, carrier)
	addComment(f, 9, 2, typedBody(t, "SPEC", "SPEC-001", "confirmed"))
	addComment(f, 9, 3, typedBody(t, "PROCESS", "PROCESS-001", "done"))

	plan, err := CompileReceiptProjection(ReceiptProjection{Version: ReceiptProjectionVersion, Repo: "o/r", Hostname: "github.com", Proposal: 7, Issue: 9,
		AcceptedReceipts: []AcceptedReceiptProjection{{Role: assignment.RoleVerification,
			Carrier: Target{Issue: 9, Type: "VERIFY", ID: "VERIFY-001"}, ReceiptID: receiptID, ReceiptDigest: digest, Generation: 3,
			Lifecycle:       []ReceiptLifecycle{{Target: Target{Issue: 9, Type: "VERIFY", ID: "VERIFY-001"}, Status: "done"}},
			CoverageTargets: []Target{{Issue: 9, Type: "SPEC", ID: "SPEC-001"}},
			CurrentTargets:  []Target{{Issue: 9, Type: "PROCESS", ID: "PROCESS-001"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	cp := t.TempDir() + "/checkpoint.json"
	first, err := (Engine{Backend: f}).Run(context.Background(), plan, cp)
	if err != nil {
		t.Fatal(err)
	}
	if !first.OK || first.Updated != 2 || first.Unchanged != 1 || f.writes != 2 || f.comments[9][0].Body != immutableCarrier {
		t.Fatalf("first=%+v writes=%d carrier_changed=%t", first, f.writes, f.comments[9][0].Body != immutableCarrier)
	}
	carrierURL := f.comments[9][0].HTMLURL
	for _, comment := range f.comments[9][1:] {
		if !hasRelatedCommentLink(comment.Body, f.comments[9][0]) {
			t.Fatalf("target %d missing carrier backlink %s: %s", comment.ID, carrierURL, comment.Body)
		}
	}

	second, err := (Engine{Backend: f}).Run(context.Background(), plan, cp)
	if err != nil {
		t.Fatal(err)
	}
	if !second.OK || second.Unchanged != 3 || f.writes != 2 || f.comments[9][0].Body != immutableCarrier {
		t.Fatalf("replay=%+v writes=%d carrier_changed=%t", second, f.writes, f.comments[9][0].Body != immutableCarrier)
	}

	f.comments[9][0].Body = strings.Replace(f.comments[9][0].Body, digest, strings.Repeat("f", 64), 1)
	if _, err := (Engine{Backend: f}).Run(context.Background(), plan, cp); err == nil ||
		!strings.Contains(err.Error(), "does not match projection") || f.writes != 2 {
		t.Fatalf("stale accepted carrier err=%v writes=%d", err, f.writes)
	}
}

func TestReconcileCarrierAuthorizedBacklinkUsesOnlyPeerEndpointContract(t *testing.T) {
	t.Skip("superseded by reconcile v2 owner-only relationship updates")
	const receiptID = "receipt-verification-1"
	receiptDigest := strings.Repeat("d", 64)
	f := newFake()
	carrier := acceptedReceiptBody(t, "VERIFY", "VERIFY-001", "done",
		assignment.RoleVerification, receiptID, receiptDigest, 1)
	peerBefore := typedBody(t, "SPEC", "SPEC-001", "confirmed")
	addComment(f, 9, 1, carrier)
	addComment(f, 9, 2, peerBefore)
	carrier, _, _ = model.AddRelatedCommentLink(carrier, f.comments[9][1].HTMLURL)
	f.comments[9][0].Body = carrier
	immutableCarrier := carrier
	carrierTarget := Target{Issue: 9, Type: "VERIFY", ID: "VERIFY-001"}
	peerTarget := Target{Issue: 9, Type: "SPEC", ID: "SPEC-001"}
	peerAfter, _, _ := model.AddRelatedCommentLink(peerBefore, f.comments[9][0].HTMLURL)
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "backlink", Kind: "link",
		Target: carrierTarget, Desired: Desired{Peer: &peerTarget, CarrierAuthorizedBacklink: true},
		Precondition: Precondition{
			AcceptedReceipt: &model.AcceptedReceiptAuthority{Role: assignment.RoleVerification,
				ReceiptID: receiptID, Digest: receiptDigest, Generation: 1},
			Endpoints: []EndpointPrecondition{{Target: peerTarget, RepresentationVersion: 1,
				AfterDigest: model.RepresentationDigest(peerAfter)}},
		},
	}}}

	result, err := (Engine{Backend: f}).Run(t.Context(), plan, "")
	if err != nil || !result.OK || result.Updated != 1 || f.writes != 1 || f.comments[9][0].Body != immutableCarrier ||
		len(result.Operations[0].Endpoints) != 1 {
		t.Fatalf("result=%+v writes=%d carrier_changed=%t err=%v", result, f.writes,
			f.comments[9][0].Body != immutableCarrier, err)
	}
	operation := result.Operations[0]
	if operation.CommentID != f.comments[9][1].ID {
		t.Fatalf("carrier backlink primary result changed: %+v", operation)
	}
	assertEndpointResult(t, operation.Endpoints[0], peerTarget, peerBefore, peerAfter)
}

func TestReconcileAcceptedReceiptRejectsWrongTypeValidRelationshipBeforeWrite(t *testing.T) {
	t.Skip("superseded by reconcile v2 owner-only relationship updates")
	const receiptID = "receipt-verification-1"
	digest := strings.Repeat("e", 64)
	f := newFake()
	carrier := acceptedReceiptBody(t, "VERIFY", "VERIFY-001", "done", assignment.RoleVerification, receiptID, digest, 1)
	carrier, _, _ = model.AddRelatedCommentLink(carrier, "https://x/o/r/issues/9#issuecomment-2")
	carrier, _, _ = model.AddRelatedCommentLink(carrier, "https://x/o/r/issues/9#issuecomment-4")
	addComment(f, 9, 1, carrier)
	addComment(f, 9, 2, typedBody(t, "SPEC", "SPEC-001", "confirmed"))
	addComment(f, 9, 3, typedBody(t, "SPEC", "SPEC-999", "confirmed"))
	addComment(f, 9, 4, typedBody(t, "PROCESS", "PROCESS-001", "done"))

	plan, err := CompileReceiptProjection(ReceiptProjection{Version: ReceiptProjectionVersion, Repo: "o/r", Hostname: "github.com", Proposal: 7, Issue: 9,
		AcceptedReceipts: []AcceptedReceiptProjection{{Role: assignment.RoleVerification,
			Carrier: Target{Issue: 9, Type: "VERIFY", ID: "VERIFY-001"}, ReceiptID: receiptID, ReceiptDigest: digest, Generation: 1,
			Lifecycle:       []ReceiptLifecycle{{Target: Target{Issue: 9, Type: "VERIFY", ID: "VERIFY-001"}, Status: "done"}},
			CoverageTargets: []Target{{Issue: 9, Type: "SPEC", ID: "SPEC-999"}},
			CurrentTargets:  []Target{{Issue: 9, Type: "PROCESS", ID: "PROCESS-001"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Engine{Backend: f}).Run(context.Background(), plan, ""); err == nil ||
		!strings.Contains(err.Error(), "not explicitly authorized") || f.writes != 0 {
		t.Fatalf("err=%v writes=%d", err, f.writes)
	}
}

func TestReconcileResolvedRelationshipAuthorityPreservesCarrierAndRejectsStaleRetry(t *testing.T) {
	t.Skip("superseded by reconcile v2 owner-only relationship updates")
	assignmentDigest, receiptDigest := strings.Repeat("a", 64), strings.Repeat("b", 64)
	processBody := resolvedAuthorityProcessBody(t, assignmentDigest)
	carrier := typedBody(t, "VERIFY", "VERIFY-036", "done")
	carrier = strings.TrimRight(carrier, "\n") + "\n\n<!-- issue-spec:accepted-verification-receipt version=1 -->\n" +
		`{"receipt_id":"receipt-verification-036","receipt_digest":"` + receiptDigest +
		`","assignment_id":"assignment-verification-036","assignment_digest":"` + assignmentDigest +
		`","assignment_generation":1}` + "\n<!-- /issue-spec:accepted-verification-receipt -->\n"
	f := newFake()
	addComment(f, 9, 1, carrier)
	addComment(f, 9, 2, processBody)
	addComment(f, 7, 3, typedBody(t, "SPEC", "SPEC-005", "confirmed"))
	immutableCarrier := f.comments[9][0].Body
	carrierTarget := Target{Issue: 9, Type: "VERIFY", ID: "VERIFY-036"}
	peerTarget := Target{Issue: 7, Type: "SPEC", ID: "SPEC-005"}
	processTarget := Target{Issue: 9, Type: "PROCESS", ID: "PROCESS-036"}
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "relationship", Kind: "link",
		Target: carrierTarget, Desired: Desired{Peer: &peerTarget, CarrierAuthorizedBacklink: true},
		Precondition: Precondition{AcceptedReceipt: &model.AcceptedReceiptAuthority{Role: assignment.RoleVerification,
			ReceiptID: "receipt-verification-036", Digest: receiptDigest, Generation: 1,
			AssignmentID: "assignment-verification-036", AssignmentDigest: assignmentDigest},
			RelationshipAuthority: &RelationshipAuthority{CarrierURL: f.comments[9][0].HTMLURL,
				CarrierBodyDigest: model.RepresentationDigest(carrier), PeerURL: f.comments[7][0].HTMLURL,
				AssignmentProcess: &processTarget, AssignmentProcessURL: f.comments[9][1].HTMLURL,
				AssignmentProcessBodyDigest: model.RepresentationDigest(processBody), AssignmentID: "assignment-verification-036",
				AssignmentDigest: assignmentDigest, AssignmentGeneration: 1}}}}}
	cp := t.TempDir() + "/checkpoint.json"
	result, err := (Engine{Backend: f}).Run(t.Context(), plan, cp)
	if err != nil || !result.OK || result.Updated != 1 || f.writes != 1 || f.comments[9][0].Body != immutableCarrier {
		t.Fatalf("result=%+v err=%v writes=%d carrier_changed=%t", result, err, f.writes, f.comments[9][0].Body != immutableCarrier)
	}
	originalPeerURL := f.comments[7][0].HTMLURL
	f.comments[7][0].HTMLURL = "https://github.com/o/r/issues/7#issuecomment-999"
	if _, err := (Engine{Backend: f}).Run(t.Context(), plan, cp); err == nil || !strings.Contains(err.Error(), "provider URL") || f.writes != 1 {
		t.Fatalf("mismatched URL retry err=%v writes=%d", err, f.writes)
	}
	f.comments[7][0].HTMLURL = originalPeerURL
	f.comments[9][1].Body += "\n"
	if _, err := (Engine{Backend: f}).Run(t.Context(), plan, cp); err == nil || !strings.Contains(err.Error(), "authority is stale") ||
		f.writes != 1 || f.comments[9][0].Body != immutableCarrier {
		t.Fatalf("stale retry err=%v writes=%d carrier_changed=%t", err, f.writes, f.comments[9][0].Body != immutableCarrier)
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

func TestReconcileNonAtomicDigestGuardRejectsChangedFreshObservation(t *testing.T) {
	f := newFake()
	addComment(f, 1, 1, typedBody(t, "TASK", "TASK-001", "confirmed"))
	f.listHook = func(f *fakeBackend, issue, call int) {
		if issue == 1 && call == 3 {
			f.comments[issue][0].Body += "\n\nConcurrent human note."
			f.versions[1]++
		}
	}
	plan := Plan{Version: 1, Repo: "o/r", AllowNonAtomic: true, Operations: []Operation{{ID: "transition", Kind: "transition",
		Target: Target{Issue: 1, Type: "TASK", ID: "TASK-001"}, Desired: Desired{Status: "done"}}}}

	result, err := (Engine{Backend: plainBackend{IssueBackend: f}}).Run(context.Background(), plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Conflicted != 1 || f.writes != 0 || model.ParseTypedComment(f.comments[1][0].Body).Status != "confirmed" ||
		!strings.Contains(f.comments[1][0].Body, "Concurrent human note.") ||
		!strings.Contains(result.Operations[0].Message, "representation digest changed") {
		t.Fatalf("result=%+v writes=%d body=%q", result, f.writes, f.comments[1][0].Body)
	}
	operation := result.Operations[0]
	if operation.BeforeDigest != model.RepresentationDigest(typedBody(t, "TASK", "TASK-001", "confirmed")) ||
		operation.AfterDigest != model.RepresentationDigest(f.comments[1][0].Body) || operation.BeforeDigest == operation.AfterDigest {
		t.Fatalf("drift operation digests=%+v", operation)
	}
}

func TestReconcileCASDriftFailsClosedWithExactObservedDigests(t *testing.T) {
	f := newFake()
	before := typedBody(t, "TASK", "TASK-001", "confirmed")
	addComment(f, 1, 1, before)
	f.conditionalHook = func(f *fakeBackend, id int64) {
		f.conditionalHook = nil
		f.comments[1][0].Body += "\nConcurrent provider change."
		f.versions[id]++
	}
	plan := Plan{Version: 1, Repo: "o/r", Operations: []Operation{{ID: "transition", Kind: "transition",
		Target: Target{Issue: 1, Type: "TASK", ID: "TASK-001"}, Desired: Desired{Status: "done"}}}}

	result, err := (Engine{Backend: f}).Run(t.Context(), plan, "")
	if err != nil || result.Conflicted != 1 || f.writes != 0 || model.ParseTypedComment(f.comments[1][0].Body).Status != "confirmed" {
		t.Fatalf("result=%+v writes=%d err=%v", result, f.writes, err)
	}
	operation := result.Operations[0]
	if operation.BeforeDigest != model.RepresentationDigest(before) ||
		operation.AfterDigest != model.RepresentationDigest(f.comments[1][0].Body) || operation.BeforeDigest == operation.AfterDigest {
		t.Fatalf("CAS drift operation digests=%+v", operation)
	}
}

func TestReconcileNonAtomicLostUpdateResponseUsesExactRecovery(t *testing.T) {
	f := newFake()
	f.updateLost = true
	addComment(f, 1, 1, typedBody(t, "TASK", "TASK-001", "confirmed"))
	plan := Plan{Version: 1, Repo: "o/r", AllowNonAtomic: true, Operations: []Operation{{ID: "transition", Kind: "transition",
		Target: Target{Issue: 1, Type: "TASK", ID: "TASK-001"}, Desired: Desired{Status: "done"}}}}
	cp := t.TempDir() + "/cp.json"

	result, err := (Engine{Backend: plainBackend{IssueBackend: f}}).Run(context.Background(), plan, cp)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Updated != 1 || result.Atomic || f.writes != 1 ||
		model.ParseTypedComment(f.comments[1][0].Body).Status != "done" {
		t.Fatalf("result=%+v writes=%d", result, f.writes)
	}
	checkpoint, err := LoadCheckpoint(cp, result.PlanDigest)
	if err != nil || checkpoint.Completed["transition"] != "updated" {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
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

func typedBodyWithLinks(t *testing.T, kind, id, status string, links map[string][]string) string {
	t.Helper()
	body, err := model.EnsureTypedBody(kind, id, "## Work\n\ncontent", model.BodyOptions{
		Agent: "Worker", Status: status, Scope: "test", Links: links,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func containsLinkValue(values []string, want string) bool {
	for _, value := range values {
		if model.NormalizeURL(value) == model.NormalizeURL(want) {
			return true
		}
	}
	return false
}

func linkEndpointPreconditions(left, right Target, leftBefore, rightBefore, leftAfter, rightAfter string,
	conditional bool) []EndpointPrecondition {
	result := []EndpointPrecondition{
		{Target: left, BodyDigest: model.RepresentationDigest(leftBefore), AfterDigest: model.RepresentationDigest(leftAfter)},
		{Target: right, BodyDigest: model.RepresentationDigest(rightBefore), AfterDigest: model.RepresentationDigest(rightAfter)},
	}
	if conditional {
		for index := range result {
			result[index].RepresentationVersion = 1
			result[index].BodyDigest = ""
		}
	}
	return result
}

func assertEndpointResult(t *testing.T, result EndpointResult, target Target, before, after string) {
	t.Helper()
	if !sameProjectionTarget(result.Target, target) || result.BeforeDigest != model.RepresentationDigest(before) ||
		result.AfterDigest != model.RepresentationDigest(after) || result.CommentID == 0 || result.URL == "" {
		t.Fatalf("endpoint result=%+v target=%+v", result, target)
	}
}

func acceptedReceiptBody(t *testing.T, kind, id, status string, role assignment.Role,
	receiptID, digest string, generation uint64) string {
	t.Helper()
	body := typedBody(t, kind, id, status)
	name := map[assignment.Role]string{assignment.RoleImplementation: "implementation",
		assignment.RoleReview: "review", assignment.RoleVerification: "verification"}[role]
	payload := fmt.Sprintf(`{"receipt_id":%q,"receipt_digest":%q,"assignment_generation":%d}`,
		receiptID, digest, generation)
	return strings.TrimRight(body, "\n") + "\n\n<!-- issue-spec:accepted-" + name + "-receipt version=1 -->\n" +
		payload + "\n<!-- /issue-spec:accepted-" + name + "-receipt -->\n"
}

func resolvedAuthorityProcessBody(t *testing.T, digest string) string {
	t.Helper()
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	revision := strings.Repeat("c", 40)
	workspace := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion,
		WorkspaceID: "assignment-verification-036", Repository: "o/r", ProcessID: "PROCESS-036",
		ExecutionClass: processworkspace.ExecutionVerification, Mode: processworkspace.ModeSnapshot,
		BaseSHA: revision, DetachedRevision: revision, RuntimeNamespace: "assignment-verification-036",
		Assignment: &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
			AssignmentID: "assignment-verification-036", Digest: digest, Role: assignment.RoleVerification,
			SubjectRevision: revision, Generation: 1}, State: processworkspace.StatePrepared, CreatedAt: now, UpdatedAt: now}
	section, err := model.RenderProcessWorkspaceSection(workspace)
	if err != nil {
		t.Fatal(err)
	}
	body, err := model.EnsureTypedBody("PROCESS", "PROCESS-036",
		"## Process: verification\n\n### Parent TASK\n\n- TASK-005\n\n### Execution Class\n\n- verification\n\n"+section+"\n\n### Handoff\n\nN/A",
		model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
