package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	issueapi "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/events/outbox"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestReconcilerPersistsJobBeforeEyesAndDeduplicatesRedelivery(t *testing.T) {
	fixture := newReconcileFixture(t, "/new verify durable event")
	reconciler, queue, store := fixture.open(t, nil)
	defer store.Close()
	first, err := reconciler.ProcessOne(t.Context())
	if err != nil || !first.Completed || first.Outcome != state.DeliveryOutcomeJob {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	loaded, _ := store.Load(t.Context())
	if len(loaded.Jobs) != 1 || fixture.backend.addCalls != 1 || len(fixture.backend.reactions) != 1 {
		t.Fatalf("state=%+v addCalls=%d reactions=%+v", loaded, fixture.backend.addCalls, fixture.backend.reactions)
	}
	redelivery := fixture.delivery
	redelivery.DeliveryID = uuid.NewString()
	redelivery.Status = state.DeliveryPending
	if _, err := queue.Accept(t.Context(), redelivery); err != nil {
		t.Fatal(err)
	}
	second, err := reconciler.ProcessOne(t.Context())
	if err != nil || !second.Completed {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	loaded, _ = store.Load(t.Context())
	if len(loaded.Jobs) != 1 || fixture.backend.addCalls != 1 || len(fixture.backend.reactions) != 1 {
		t.Fatalf("redelivery duplicated effects: jobs=%d addCalls=%d reactions=%d", len(loaded.Jobs), fixture.backend.addCalls, len(fixture.backend.reactions))
	}
}

func TestReconcilerAckFailureRetriesWithoutDuplicatingJob(t *testing.T) {
	fixture := newReconcileFixture(t, "/new retry ack")
	fixture.backend.addErrors = []error{errors.New("injected ack failure"), nil}
	reconciler, _, store := fixture.open(t, nil)
	defer store.Close()
	first, err := reconciler.ProcessOne(t.Context())
	if err != nil || !first.Retried {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	loaded, _ := store.Load(t.Context())
	delivery := loaded.Deliveries[fixture.delivery.DeliveryID]
	if len(loaded.Jobs) != 1 || delivery.Status != state.DeliveryPending || !delivery.AckPending {
		t.Fatalf("retry state=%+v jobs=%d", delivery, len(loaded.Jobs))
	}
	second, err := reconciler.ProcessOne(t.Context())
	if err != nil || !second.Completed {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	loaded, _ = store.Load(t.Context())
	if len(loaded.Jobs) != 1 || len(fixture.backend.reactions) != 1 {
		t.Fatalf("ack retry duplicated state: %+v reactions=%+v", loaded.Jobs, fixture.backend.reactions)
	}
}

func TestReconcilerDecisionSaveFailureHasNoAckOrLeakedJob(t *testing.T) {
	fixture := newReconcileFixture(t, "/new retry save")
	faults := &faultStateStore{failDecision: true}
	reconciler, _, store := fixture.open(t, faults)
	defer store.Close()
	first, err := reconciler.ProcessOne(t.Context())
	if err != nil || !first.Retried {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	loaded, _ := store.Load(t.Context())
	if len(loaded.Jobs) != 0 || fixture.backend.addCalls != 0 ||
		loaded.Deliveries[fixture.delivery.DeliveryID].Outcome != "" {
		t.Fatalf("failed save leaked effects: jobs=%d adds=%d delivery=%+v", len(loaded.Jobs), fixture.backend.addCalls,
			loaded.Deliveries[fixture.delivery.DeliveryID])
	}
	second, err := reconciler.ProcessOne(t.Context())
	if err != nil || !second.Completed {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	loaded, _ = store.Load(t.Context())
	if len(loaded.Jobs) != 1 || len(fixture.backend.reactions) != 1 {
		t.Fatalf("retry did not converge: jobs=%d reactions=%d", len(loaded.Jobs), len(fixture.backend.reactions))
	}
}

func TestReconcilerRecoversCrashAfterDecisionBeforeAck(t *testing.T) {
	fixture := newReconcileFixture(t, "/new recover crash")
	reconciler, queue, store := fixture.open(t, nil)
	defer store.Close()
	claim, err := queue.Claim(t.Context(), "crashed-worker", time.Minute, fixture.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	decision, err := intake.DecideAuthoritativeComment(t.Context(), fixture.backend, fixture.runner,
		fixture.policy, state.NewState(), fixture.backend.comment.Comment, fixture.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.RecordDecision(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken,
		fixture.clock.Now(), durableDecision(decision, fixture.backend.comment.RepresentationVersion), decision.Apply); err != nil {
		t.Fatal(err)
	}
	fixture.clock.Advance(2 * time.Minute)
	result, err := reconciler.ProcessOne(t.Context())
	if err != nil || !result.Completed {
		t.Fatalf("recovered=%+v err=%v", result, err)
	}
	loaded, _ := store.Load(t.Context())
	if len(loaded.Jobs) != 1 || len(fixture.backend.reactions) != 1 {
		t.Fatalf("crash recovery duplicated effects: jobs=%d reactions=%d", len(loaded.Jobs), len(fixture.backend.reactions))
	}
}

func TestReconcilerRecoversAckBeforeMarkAndCompleteFailures(t *testing.T) {
	for _, mode := range []string{"ack-mark", "complete"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newReconcileFixture(t, "/new recover "+mode)
			faults := &faultStateStore{}
			if mode == "ack-mark" {
				faults.failAckMark = true
			} else {
				faults.failComplete = true
			}
			reconciler, _, store := fixture.open(t, faults)
			defer store.Close()
			first, err := reconciler.ProcessOne(t.Context())
			if err != nil || !first.Retried {
				t.Fatalf("first=%+v err=%v", first, err)
			}
			second, err := reconciler.ProcessOne(t.Context())
			if err != nil || !second.Completed {
				t.Fatalf("second=%+v err=%v", second, err)
			}
			loaded, _ := store.Load(t.Context())
			if len(loaded.Jobs) != 1 || fixture.backend.addCalls != 1 || len(fixture.backend.reactions) != 1 {
				t.Fatalf("mode=%s jobs=%d addCalls=%d reactions=%d", mode, len(loaded.Jobs), fixture.backend.addCalls, len(fixture.backend.reactions))
			}
		})
	}
}

func TestReconcilerRevisionClassification(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*reconcileFixture)
		check  func(ReconcileResult) bool
	}{
		{name: "same revision conflict", mutate: func(f *reconcileFixture) { f.backend.comment.Comment.Body = "/new changed without revision" },
			check: func(result ReconcileResult) bool { return result.Failed && result.Reason == "invalid_envelope" }},
		{name: "newer supersedes", mutate: func(f *reconcileFixture) {
			f.backend.comment.RepresentationVersion++
			f.backend.comment.Comment.UpdatedAt = f.backend.comment.Comment.UpdatedAt.Add(time.Second)
			f.backend.comment.Comment.Body = "/new newer"
		}, check: func(result ReconcileResult) bool {
			return result.Completed && result.Outcome == state.DeliveryOutcomeSuperseded
		}},
		{name: "older retries", mutate: func(f *reconcileFixture) { f.backend.comment.RepresentationVersion-- },
			check: func(result ReconcileResult) bool { return result.Retried && result.Reason == "authoritative_older" }},
		{name: "malformed protocol permanent", mutate: func(f *reconcileFixture) { f.backend.commentErr = errors.New("decode GitHub response: malformed JSON") },
			check: func(result ReconcileResult) bool { return result.Failed && result.Reason == "processing_failed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReconcileFixture(t, "/new revision")
			test.mutate(fixture)
			reconciler, _, store := fixture.open(t, nil)
			defer store.Close()
			result, err := reconciler.ProcessOne(t.Context())
			if err != nil || !test.check(result) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestReconcilerFailsClosedOnWrongDurableLink(t *testing.T) {
	fixture := newReconcileFixture(t, "/new wrong link")
	reconciler, queue, store := fixture.open(t, nil)
	defer store.Close()
	claim, _ := queue.Claim(t.Context(), "crashed-worker", time.Minute, fixture.clock.Now())
	decision, _ := intake.DecideAuthoritativeComment(t.Context(), fixture.backend, fixture.runner,
		fixture.policy, state.NewState(), fixture.backend.comment.Comment, fixture.clock.Now())
	wrong := decision.Job
	wrong.IssueNumber = 999
	_, err := queue.RecordDecision(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken,
		fixture.clock.Now(), durableDecision(decision, fixture.backend.comment.RepresentationVersion), func(current *state.RunnerState) error {
			return current.UpsertJob(wrong)
		})
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.Advance(2 * time.Minute)
	result, err := reconciler.ProcessOne(t.Context())
	if err != nil || !result.Failed || fixture.backend.addCalls != 0 {
		t.Fatalf("wrong-link result=%+v err=%v addCalls=%d", result, err, fixture.backend.addCalls)
	}
}

type reconcileFixture struct {
	delivery state.WebhookDelivery
	backend  *reconcileBackend
	scopes   RepositoryScopes
	runner   commentrunner.Config
	policy   commentrunner.AuthorizationPolicy
	clock    *mutableReconcileClock
}

func newReconcileFixture(t *testing.T, body string) *reconcileFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	orgID, repoID, issueID, commentID, userID, eventID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	scope := models.RepoScope{OrgID: orgID, RepoID: repoID}
	issue := models.Issue{ID: issueID, Scope: scope, Number: 12, AuthorID: &userID, Title: "runner",
		RepresentationVersion: 2, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-30 * time.Minute)}
	comment := models.CommentSnapshot{Comment: models.Comment{ID: commentID, Scope: scope, IssueID: issueID,
		AuthorID: &userID, Body: body, RepresentationVersion: 3, CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now},
		IssueNumber: 12, AuthorLogin: "alice"}
	digest := sha256.Sum256([]byte(body))
	envelope, _, err := outbox.BuildEnvelope(eventID, issueapi.MutationEvent{Type: "issue_comment.created", Scope: scope,
		Issue: issue, Comment: &comment, RawBody: body, BodyHash: digest, ActorUserID: userID,
		ActorCredentialKind: serverauth.CredentialSession, RepresentationVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(envelope)
	rawDigest := sha256.Sum256(raw)
	delivery := state.WebhookDelivery{DeliveryID: uuid.NewString(), EventID: envelope.EventID.String(),
		SubscriptionID: uuid.NewString(), BodySHA256: hex.EncodeToString(rawDigest[:]), RawEnvelope: raw,
		SchemaVersion: envelope.SchemaVersion, EventKey: envelope.EventKey, EventType: envelope.EventType,
		Action: envelope.Action, OrganizationID: orgID.String(), RepositoryID: repoID.String(), IssueID: issueID.String(),
		IssueNumber: issue.Number, CommentID: commentID.String(), CommentRevision: comment.Comment.RepresentationVersion,
		AuthorLogin: "alice", EnvelopeBodySHA256: envelope.BodyHash, ReceivedAt: now, Status: state.DeliveryPending}
	remoteComment := github.Comment{ID: envelope.Comment.NumericID, NodeID: codec.NodeID("IssueComment", commentID.String()),
		URL: "https://issues.test/repos/owner/repo/issues/comments/1", IssueURL: "https://issues.test/repos/owner/repo/issues/12",
		IssueNumber: 12, Body: body, User: &github.User{Login: "alice", NodeID: codec.NodeID("User", userID.String())},
		CreatedAt: comment.Comment.CreatedAt, UpdatedAt: comment.Comment.UpdatedAt}
	backend := &reconcileBackend{comment: github.RunnerCommentResult{Comment: remoteComment,
		RepresentationVersion: comment.Comment.RepresentationVersion}, runner: "runner", permission: "write",
		issue: github.IssueContextResult{Issue: github.Issue{Number: 12, NodeID: codec.NodeID("Issue", issueID.String()), State: "open"}}}
	return &reconcileFixture{delivery: delivery, backend: backend,
		scopes: RepositoryScopes{ByRepository: map[string]models.RepoScope{"owner/repo": scope}, ByScope: map[models.RepoScope]string{scope: "owner/repo"}},
		runner: commentrunner.Config{Hostname: "issues.test", Repositories: []string{"owner/repo"}, RunnerIdentity: "runner",
			CancellationEnabled: true, Agent: commentrunner.AgentConfig{Kind: commentrunner.AgentCodex}},
		policy: commentrunner.AuthorizationPolicy{RunnerLogin: "runner", AllowedUsers: []string{"alice"}},
		clock:  &mutableReconcileClock{now: now.Add(time.Second)}}
}

func (f *reconcileFixture) open(t *testing.T, faults *faultStateStore) (*Reconciler, *Queue, state.StateStore) {
	t.Helper()
	base, err := state.OpenFileStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var store state.StateStore = base
	if faults != nil {
		faults.StateStore = base
		store = faults
	}
	queue, _ := NewQueue(store, QueueConfig{MaxItemBytes: 1 << 20, MaxTotalBytes: 2 << 20})
	if _, err := queue.Accept(t.Context(), f.delivery); err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewReconciler(ReconcilerConfig{Queue: queue, Store: store, Backend: f.backend, Scopes: f.scopes,
		Runner: f.runner, AuthorizationPolicy: f.policy, WorkerID: "test-worker", LeaseDuration: time.Minute, Clock: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	return reconciler, queue, store
}

type mutableReconcileClock struct{ now time.Time }

func (c *mutableReconcileClock) Now() time.Time          { return c.now }
func (c *mutableReconcileClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

type reconcileBackend struct {
	comment    github.RunnerCommentResult
	commentErr error
	issue      github.IssueContextResult
	runner     string
	permission string
	reactions  []github.Reaction
	addErrors  []error
	addCalls   int
	statusID   int64
}

func (b *reconcileBackend) GetIssueComment(context.Context, string, int64) (github.RunnerCommentResult, error) {
	return b.comment, b.commentErr
}
func (b *reconcileBackend) GetUser(context.Context) (github.User, []string, error) {
	return github.User{Login: b.runner}, nil, nil
}
func (b *reconcileBackend) GetCollaboratorPermission(context.Context, string, string) (github.CollaboratorPermissionResult, error) {
	return github.CollaboratorPermissionResult{Permission: github.CollaboratorPermission{Permission: b.permission}, CanWrite: b.permission == "write"}, nil
}
func (b *reconcileBackend) GetIssueContext(context.Context, string, int, github.ConditionalRequest) (github.IssueContextResult, error) {
	return b.issue, nil
}
func (b *reconcileBackend) ListCommentReactionsPage(context.Context, string, int64, github.RunnerPageOptions) (github.CommentReactionsResult, error) {
	return github.CommentReactionsResult{Reactions: append([]github.Reaction(nil), b.reactions...)}, nil
}
func (b *reconcileBackend) AddCommentReaction(context.Context, string, int64, string) (github.RunnerReactionResult, error) {
	b.addCalls++
	if len(b.addErrors) > 0 {
		err := b.addErrors[0]
		b.addErrors = b.addErrors[1:]
		if err != nil {
			return github.RunnerReactionResult{}, err
		}
	}
	b.reactions = append(b.reactions, github.Reaction{ID: int64(len(b.reactions) + 1), User: &github.User{Login: b.runner}, Content: "eyes"})
	return github.RunnerReactionResult{}, nil
}
func (b *reconcileBackend) ListIssueCommentsPage(context.Context, string, int, github.CommentListOptions) (github.IssueCommentsResult, error) {
	return github.IssueCommentsResult{}, nil
}
func (b *reconcileBackend) CreateRunnerComment(_ context.Context, _ string, issue int, body string) (github.RunnerCommentResult, error) {
	b.statusID++
	return github.RunnerCommentResult{Comment: github.Comment{ID: b.statusID, IssueNumber: issue, Body: body}}, nil
}
func (b *reconcileBackend) UpdateRunnerComment(_ context.Context, _ string, id int64, body string) (github.RunnerCommentResult, error) {
	return github.RunnerCommentResult{Comment: github.Comment{ID: id, Body: body}}, nil
}

type faultStateStore struct {
	state.StateStore
	failDecision bool
	failAckMark  bool
	failComplete bool
}

func (s *faultStateStore) Update(ctx context.Context, mutate func(*state.RunnerState) error) error {
	before, err := s.StateStore.Load(ctx)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(before)
	if err != nil {
		return err
	}
	var after state.RunnerState
	if err := json.Unmarshal(encoded, &after); err != nil {
		return err
	}
	if err := mutate(&after); err != nil {
		return err
	}
	for id, prior := range before.Deliveries {
		next := after.Deliveries[id]
		if s.failDecision && prior.Outcome == "" && next.Outcome != "" && len(after.Jobs) > len(before.Jobs) {
			s.failDecision = false
			return errors.New("injected decision save failure")
		}
		if s.failAckMark && prior.AckPending && !next.AckPending && !next.AckCompletedAt.IsZero() {
			s.failAckMark = false
			return errors.New("injected ack mark save failure")
		}
		if s.failComplete && prior.Status == state.DeliveryProcessing && next.Status == state.DeliveryCompleted {
			s.failComplete = false
			return errors.New("injected completion save failure")
		}
	}
	return s.StateStore.Save(ctx, after)
}
