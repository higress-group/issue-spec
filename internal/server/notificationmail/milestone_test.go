package notificationmail

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
	"github.com/higress-group/issue-spec/internal/server/store"
)

func TestMilestoneRendererKeepsStageAndCompletedTemplatesNarrow(t *testing.T) {
	base := MilestoneSnapshot{Version: SnapshotVersion, ActorLogin: "actor", ActorDisplayName: "Actor",
		RepositoryOwner: "acme", RepositoryName: "widgets", ChangeKey: "email-flow", IssueID: uuid.New(),
		OccurredAt: time.Now().UTC()}
	stage := base
	stage.Milestone, stage.IssueNumber, stage.IssueTitle, stage.Content = store.NotificationMilestoneProposal, 17, "Proposal", "body"
	if err := stage.Validate(); err != nil {
		t.Fatal(err)
	}
	_, stageBody := render(stage, "https://example.test/acme/widgets/issues/17")
	if !strings.Contains(stageBody, "Artifact content") || !strings.Contains(stageBody, "#17") {
		t.Fatalf("stage body = %q", stageBody)
	}
	completed := base
	completed.Milestone = store.NotificationMilestoneCompleted
	if err := completed.Validate(); err != nil {
		t.Fatal(err)
	}
	_, completedBody := render(completed, "https://example.test/acme/widgets/changes/email-flow")
	if strings.Contains(completedBody, "Artifact content") || !strings.Contains(completedBody, "completed") {
		t.Fatalf("completed body = %q", completedBody)
	}
}

func TestMilestonePreparerRechecksEligibilityAndSuppressesWithoutDetails(t *testing.T) {
	orgID, repoID, issueID, recipientID, milestoneID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	origin, err := publicurl.ParseOrigin("web", "https://issues.example.test")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := MilestoneSnapshot{Version: SnapshotVersion, ActorLogin: "actor", RepositoryOwner: "acme",
		RepositoryName: "widgets", ChangeKey: "email-flow", Milestone: store.NotificationMilestoneCompleted,
		IssueID: issueID, OccurredAt: time.Now().UTC()}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	delivery := emaildelivery.Delivery{ID: uuid.New(), Kind: emaildelivery.KindChangeMilestone,
		RecipientUserID: recipientID, OrganizationID: &orgID, RepositoryID: &repoID,
		MilestoneID: &milestoneID, Snapshot: payload}
	preparer, err := NewPreparer(milestoneEligibilityFunc(func(_ context.Context, scope models.RepoScope,
		userID uuid.UUID) (store.RepositoryNotificationRecipient, error) {
		if scope != (models.RepoScope{OrgID: orgID, RepoID: repoID}) || userID != recipientID {
			t.Fatalf("eligibility input = %+v/%s", scope, userID)
		}
		return store.RepositoryNotificationRecipient{}, store.ErrNotFound
	}), origin, emaildelivery.AddressPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = preparer.Prepare(t.Context(), delivery)
	var outcome *emaildelivery.OutcomeError
	if !errors.As(err, &outcome) || !outcome.Suppressed || outcome.Reason != emaildelivery.ReasonRecipientUnavailable {
		t.Fatalf("suppression = %#v err=%v", outcome, err)
	}
	for _, forbidden := range []string{"subscription", "address", "repository"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("suppression leaked %q: %v", forbidden, err)
		}
	}
}

func TestMilestonePreparerSuppressesAddressOutsideCurrentDomainPolicy(t *testing.T) {
	orgID, repoID, issueID, recipientID, milestoneID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	origin, _ := publicurl.ParseOrigin("web", "https://issues.example.test")
	policy, err := emaildelivery.NewAddressPolicy([]string{"corp.example"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := MilestoneSnapshot{Version: SnapshotVersion, ActorLogin: "actor", RepositoryOwner: "acme",
		RepositoryName: "widgets", ChangeKey: "email-flow", Milestone: store.NotificationMilestoneCompleted,
		IssueID: issueID, OccurredAt: time.Now().UTC()}
	payload, _ := json.Marshal(snapshot)
	delivery := emaildelivery.Delivery{ID: uuid.New(), Kind: emaildelivery.KindChangeMilestone,
		RecipientUserID: recipientID, OrganizationID: &orgID, RepositoryID: &repoID,
		MilestoneID: &milestoneID, Snapshot: payload}
	preparer, err := NewPreparer(milestoneEligibilityFunc(func(context.Context, models.RepoScope,
		uuid.UUID) (store.RepositoryNotificationRecipient, error) {
		return store.RepositoryNotificationRecipient{Address: "reader@personal.example"}, nil
	}), origin, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = preparer.Prepare(t.Context(), delivery)
	var outcome *emaildelivery.OutcomeError
	if !errors.As(err, &outcome) || !outcome.Suppressed || outcome.Reason != emaildelivery.ReasonRecipientUnavailable {
		t.Fatalf("Prepare() error = %#v / %v", outcome, err)
	}
}

type milestoneEligibilityFunc func(context.Context, models.RepoScope, uuid.UUID) (store.RepositoryNotificationRecipient, error)

func (f milestoneEligibilityFunc) Recipient(ctx context.Context, scope models.RepoScope,
	userID uuid.UUID) (store.RepositoryNotificationRecipient, error) {
	return f(ctx, scope, userID)
}
