package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	githubissues "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	"github.com/higress-group/issue-spec/internal/server/changes"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/models"
	projectnotifications "github.com/higress-group/issue-spec/internal/server/projection/reponotifications"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
)

type issueNotificationAdapter struct {
	ordinary  *projectnotifications.OrdinaryIssueProjector
	milestone *projectnotifications.MilestoneProjector
}

func newIssueNotificationAdapter() (*issueNotificationAdapter, error) {
	selector := projectnotifications.StoreSubscriberSelector{}
	ordinary, err := projectnotifications.NewOrdinaryIssueProjector(selector)
	if err != nil {
		return nil, err
	}
	milestone, err := projectnotifications.NewMilestoneProjector(selector)
	if err != nil {
		return nil, err
	}
	return &issueNotificationAdapter{ordinary: ordinary, milestone: milestone}, nil
}

func (a *issueNotificationAdapter) ProjectIssueCreated(ctx context.Context, repository store.RepoStore,
	queue *emaildelivery.Store, resource models.RepositoryResource, issue models.Issue, actorID uuid.UUID) error {
	event := projectnotifications.IssueCreated{Repository: resource, Issue: issue, ActorID: actorID}
	result, err := a.milestone.ProjectCreatedArtifact(ctx, repository, queue, event)
	if err != nil || result.Handled {
		return err
	}
	_, err = a.ordinary.ProjectIssueCreated(ctx, repository, queue, event)
	return err
}

func (a *issueNotificationAdapter) Capture(ctx context.Context, tx pgx.Tx, scope models.RepoScope,
	issueID uuid.UUID) (githubissues.ChangeLifecycle, error) {
	snapshot, err := changes.LifecycleForIssueTx(ctx, tx, scope, issueID)
	return githubissues.ChangeLifecycle{ChangeKey: snapshot.ChangeKey, Lifecycle: string(snapshot.Lifecycle)}, err
}

func (a *issueNotificationAdapter) ProjectCompleted(ctx context.Context, repository store.RepoStore,
	queue *emaildelivery.Store, resource models.RepositoryResource, issue models.Issue,
	comment *models.CommentSnapshot, actorID uuid.UUID, before, after githubissues.ChangeLifecycle) error {
	_, err := a.milestone.ProjectCompleted(ctx, repository, queue, projectnotifications.CompletedMutation{
		Repository: resource, Issue: issue, Comment: comment, ActorID: actorID,
		Before: projectnotifications.LifecycleState{ChangeKey: before.ChangeKey, Lifecycle: before.Lifecycle},
		After:  projectnotifications.LifecycleState{ChangeKey: after.ChangeKey, Lifecycle: after.Lifecycle},
	})
	return err
}

type expirer interface {
	Expire(context.Context, int) (int, error)
}

type profileExpiryWorker struct {
	service  expirer
	interval time.Duration
	stop     chan struct{}
	stopOnce sync.Once
}

func newProfileExpiryWorker(service expirer, interval time.Duration) (*profileExpiryWorker, error) {
	if service == nil || interval <= 0 {
		return nil, errors.New("profile expiry worker: service and interval are required")
	}
	return &profileExpiryWorker{service: service, interval: interval, stop: make(chan struct{})}, nil
}

func (w *profileExpiryWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-w.stop:
			return nil
		case <-ticker.C:
			if _, err := w.service.Expire(ctx, 100); err != nil {
				return err
			}
		}
	}
}

func (w *profileExpiryWorker) StopClaims() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stop) })
}
