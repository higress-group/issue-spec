package issues_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	issueapi "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/changes"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/projection/artifacts"
	projectnotifications "github.com/higress-group/issue-spec/internal/server/projection/reponotifications"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
)

func TestNotificationHooksShareMutationTransactionsAndKeepArtifactMailExclusive(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPrivate)
	recipient := environment.addMember(t, "recipient", "member")
	setNotificationRecipient(t, environment, recipient.User.ID)
	environment.service = notificationService(t, environment)

	_, ordinary, err := environment.service.CreateIssue(t.Context(), "acme", "widgets",
		authz.Authenticated(environment.owner), models.NewIssue{Title: "Ordinary", Body: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	assertDeliveryCounts(t, environment, ordinary.Issue.ID, 1, 0)

	artifactBody := "<!-- issue-spec:issue=proposal change=mail-flow version=1 -->\n# Proposal"
	_, artifact, err := environment.service.CreateIssue(t.Context(), "acme", "widgets",
		authz.Authenticated(environment.owner), models.NewIssue{Title: "Proposal", Body: artifactBody})
	if err != nil {
		t.Fatal(err)
	}
	assertDeliveryCounts(t, environment, artifact.Issue.ID, 0, 1)

	beforeIssues, beforeDeliveries, beforeEvents := notificationRowCounts(t, environment)
	environment.hook.fail.Store(true)
	if _, _, err := environment.service.CreateIssue(t.Context(), "acme", "widgets",
		authz.Authenticated(environment.owner), models.NewIssue{Title: "Rollback", Body: "@recipient"}); err == nil {
		t.Fatal("injected outbox failure did not roll back issue notification transaction")
	}
	afterIssues, afterDeliveries, afterEvents := notificationRowCounts(t, environment)
	if beforeIssues != afterIssues || beforeDeliveries != afterDeliveries || beforeEvents != afterEvents {
		t.Fatalf("rollback rows before=%d/%d/%d after=%d/%d/%d", beforeIssues, beforeDeliveries, beforeEvents,
			afterIssues, afterDeliveries, afterEvents)
	}
}

func TestMentionFirstSeenAndCompletedLifecycleNotifyOnce(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPrivate)
	recipient := environment.addMember(t, "recipient", "member")
	setNotificationRecipient(t, environment, recipient.User.ID)
	environment.service = notificationService(t, environment)

	_, issue, err := environment.service.CreateIssue(t.Context(), "acme", "widgets",
		authz.Authenticated(environment.owner), models.NewIssue{Title: "Mentions", Body: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	_, comment, err := environment.service.CreateComment(t.Context(), "acme", "widgets", issue.Issue.Number,
		authz.Authenticated(environment.owner), "hello @recipient")
	if err != nil {
		t.Fatal(err)
	}
	if got := countNotificationRows(t, environment, `SELECT count(*) FROM email_deliveries WHERE kind = 'mention'`); got != 1 {
		t.Fatalf("mention deliveries = %d", got)
	}
	if _, _, err := environment.service.UpdateComment(t.Context(), "acme", "widgets", commentCompatibilityID(t, environment, comment.Comment.ID),
		authz.Authenticated(environment.owner), "edited @recipient"); err != nil {
		t.Fatal(err)
	}
	if got := countNotificationRows(t, environment, `SELECT count(*) FROM email_deliveries WHERE kind = 'mention'`); got != 1 {
		t.Fatalf("repeat mention deliveries = %d", got)
	}

	proposal := createArtifact(t, environment, "proposal", "complete-flow")
	design := createArtifact(t, environment, "design", "complete-flow")
	implement := createArtifact(t, environment, "implement", "complete-flow")
	for _, candidate := range []models.IssueSnapshot{proposal, design, implement} {
		if _, _, err := environment.service.UpdateIssue(t.Context(), "acme", "widgets", candidate.Issue.Number,
			authz.Authenticated(environment.owner), func(current models.Issue) (models.IssueUpdate, error) {
				return models.IssueUpdate{Title: current.Title, Body: current.Body, State: models.IssueStateClosed}, nil
			}); err != nil {
			t.Fatal(err)
		}
	}
	verifyBody := "<!-- issue-spec:type=VERIFY id=VERIFY-001 version=1 -->\n" +
		"Agent: verifier\nType: VERIFY\nID: VERIFY-001\nStatus: done\nScope: final\nLinks:\n" +
		"- PR: https://code.example/acme/widgets/changes/42\n"
	_, verify, err := environment.service.CreateComment(t.Context(), "acme", "widgets", implement.Issue.Number,
		authz.Authenticated(environment.owner), verifyBody)
	if err != nil {
		t.Fatal(err)
	}
	if got := countNotificationRows(t, environment, `SELECT count(*) FROM change_notification_milestones
		WHERE change_key_key = 'complete-flow' AND milestone = 'completed'`); got != 1 {
		t.Fatalf("completed milestone facts = %d", got)
	}
	if got := countNotificationRows(t, environment, `SELECT count(*) FROM email_deliveries d
		JOIN change_notification_milestones m ON m.organization_id = d.organization_id
		AND m.repository_id = d.repository_id AND m.id = d.milestone_id
		WHERE d.kind = 'change_milestone' AND m.change_key_key = 'complete-flow'
		AND m.milestone = 'completed'`); got != 1 {
		t.Fatalf("completed milestone deliveries = %d", got)
	}
	if _, _, err := environment.service.UpdateComment(t.Context(), "acme", "widgets", commentCompatibilityID(t, environment, verify.Comment.ID),
		authz.Authenticated(environment.owner), verifyBody+"\nunchanged lifecycle"); err != nil {
		t.Fatal(err)
	}
	if got := countNotificationRows(t, environment, `SELECT count(*) FROM change_notification_milestones
		WHERE change_key_key = 'complete-flow' AND milestone = 'completed'`); got != 1 {
		t.Fatalf("completed milestone re-entry facts = %d", got)
	}
}

type integrationNotificationAdapter struct {
	ordinary  *projectnotifications.OrdinaryIssueProjector
	milestone *projectnotifications.MilestoneProjector
}

func notificationService(t *testing.T, environment *environment) *issueapi.Service {
	t.Helper()
	selector := projectnotifications.StoreSubscriberSelector{}
	ordinary, err := projectnotifications.NewOrdinaryIssueProjector(selector)
	if err != nil {
		t.Fatal(err)
	}
	milestone, err := projectnotifications.NewMilestoneProjector(selector)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &integrationNotificationAdapter{ordinary: ordinary, milestone: milestone}
	service, err := issueapi.NewService(store.New(environment.pool), environment.authorizer,
		artifacts.MarkerProjector{}, environment.hook, issueapi.NotificationIntegration{
			Enabled: true, OrdinaryIssue: adapter, Completed: adapter,
		})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func (a *integrationNotificationAdapter) ProjectIssueCreated(ctx context.Context, repository store.RepoStore,
	queue *emaildelivery.Store, resource models.RepositoryResource, issue models.Issue, actorID uuid.UUID) error {
	event := projectnotifications.IssueCreated{Repository: resource, Issue: issue, ActorID: actorID}
	result, err := a.milestone.ProjectCreatedArtifact(ctx, repository, queue, event)
	if err != nil || result.Handled {
		return err
	}
	_, err = a.ordinary.ProjectIssueCreated(ctx, repository, queue, event)
	return err
}

func (a *integrationNotificationAdapter) Capture(ctx context.Context, tx pgx.Tx, scope models.RepoScope,
	issueID uuid.UUID) (issueapi.ChangeLifecycle, error) {
	snapshot, err := changes.LifecycleForIssueTx(ctx, tx, scope, issueID)
	return issueapi.ChangeLifecycle{ChangeKey: snapshot.ChangeKey, Lifecycle: string(snapshot.Lifecycle)}, err
}

func (a *integrationNotificationAdapter) ProjectCompleted(ctx context.Context, repository store.RepoStore,
	queue *emaildelivery.Store, resource models.RepositoryResource, issue models.Issue,
	comment *models.CommentSnapshot, actorID uuid.UUID, before, after issueapi.ChangeLifecycle) error {
	_, err := a.milestone.ProjectCompleted(ctx, repository, queue, projectnotifications.CompletedMutation{
		Repository: resource, Issue: issue, Comment: comment, ActorID: actorID,
		Before: projectnotifications.LifecycleState{ChangeKey: before.ChangeKey, Lifecycle: before.Lifecycle},
		After:  projectnotifications.LifecycleState{ChangeKey: after.ChangeKey, Lifecycle: after.Lifecycle},
	})
	return err
}

func setNotificationRecipient(t *testing.T, environment *environment, userID uuid.UUID) {
	t.Helper()
	if _, err := environment.pool.Exec(t.Context(), `UPDATE users SET notification_email = $2,
		notification_email_verified_at = clock_timestamp() WHERE id = $1`, userID, "recipient@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.pool.Exec(t.Context(), `INSERT INTO repo_subscriptions
		(organization_id, repository_id, user_id, reason) VALUES ($1,$2,$3,'manual')`,
		environment.scope.OrgID, environment.scope.RepoID, userID); err != nil {
		t.Fatal(err)
	}
}

func assertDeliveryCounts(t *testing.T, environment *environment, issueID uuid.UUID, ordinary, milestone int) {
	t.Helper()
	if got := countNotificationRows(t, environment, `SELECT count(*) FROM email_deliveries
		WHERE issue_id = $1 AND kind = 'repo_issue_created'`, issueID); got != ordinary {
		t.Fatalf("ordinary deliveries for %s = %d, want %d", issueID, got, ordinary)
	}
	if got := countNotificationRows(t, environment, `SELECT count(*) FROM change_notification_milestones
		WHERE triggering_issue_id = $1`, issueID); got != milestone {
		t.Fatalf("milestone facts for %s = %d, want %d", issueID, got, milestone)
	}
	if got := countNotificationRows(t, environment, `SELECT count(*) FROM email_deliveries d
		JOIN change_notification_milestones m ON m.organization_id = d.organization_id
		AND m.repository_id = d.repository_id AND m.id = d.milestone_id
		WHERE m.triggering_issue_id = $1 AND d.kind = 'change_milestone'`, issueID); got != milestone {
		t.Fatalf("milestone deliveries for %s = %d, want %d", issueID, got, milestone)
	}
}

func notificationRowCounts(t *testing.T, environment *environment) (int, int, int) {
	t.Helper()
	return countNotificationRows(t, environment, `SELECT count(*) FROM issues`),
		countNotificationRows(t, environment, `SELECT count(*) FROM email_deliveries`),
		countNotificationRows(t, environment, `SELECT count(*) FROM event_outbox`)
}

func countNotificationRows(t *testing.T, environment *environment, query string, args ...any) int {
	t.Helper()
	var count int
	if err := environment.pool.QueryRow(t.Context(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func commentCompatibilityID(t *testing.T, environment *environment, commentID uuid.UUID) int64 {
	t.Helper()
	var compatibilityID int64
	if err := environment.pool.QueryRow(t.Context(), `SELECT compatibility_id FROM comments
		WHERE organization_id = $1 AND repository_id = $2 AND id = $3`, environment.scope.OrgID,
		environment.scope.RepoID, commentID).Scan(&compatibilityID); err != nil {
		t.Fatal(err)
	}
	return compatibilityID
}

func createArtifact(t *testing.T, environment *environment, kind, changeKey string) models.IssueSnapshot {
	t.Helper()
	body := "<!-- issue-spec:issue=" + kind + " change=" + changeKey + " version=1 -->\n# " + kind
	_, snapshot, err := environment.service.CreateIssue(t.Context(), "acme", "widgets",
		authz.Authenticated(environment.owner), models.NewIssue{Title: kind, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
