package store

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryNotificationSubscriptionAndEligibility(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgID := insertOrg(t, pool, "notification-org")
	repoID := insertRepo(t, pool, orgID, "notification-repo")
	actor := insertNotificationUser(t, pool, "notification-actor", true)
	member := insertNotificationUser(t, pool, "notification-member", true)
	noAddress := insertNotificationUser(t, pool, "notification-no-address", false)
	serviceUser := insertNotificationUser(t, pool, "notification-service", true)
	outsider := insertNotificationUser(t, pool, "notification-outsider", true)
	for _, userID := range []uuid.UUID{actor, member, noAddress, serviceUser} {
		insertNotificationMembership(t, pool, orgID, userID)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO service_accounts
		(user_id, organization_id, name, created_by_user_id) VALUES ($1,$2,'mailer',$3)`,
		serviceUser, orgID, actor); err != nil {
		t.Fatal(err)
	}

	database := New(pool)
	var firstCollection int64
	if err := database.WithinTx(t.Context(), func(tx *Tx) error {
		state, changed, err := tx.Repo(orgID, repoID).SetManualRepositorySubscription(t.Context(), actor, true)
		if err != nil || !changed || !state.Subscribed || state.Reason != "manual" || state.CollectionVersion != 2 {
			t.Fatalf("first subscribe = %+v changed=%v err=%v", state, changed, err)
		}
		firstCollection = state.CollectionVersion
		state, changed, err = tx.Repo(orgID, repoID).SetManualRepositorySubscription(t.Context(), actor, true)
		if err != nil || changed || state.CollectionVersion != firstCollection {
			t.Fatalf("repeated subscribe = %+v changed=%v err=%v", state, changed, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.WithinTx(t.Context(), func(tx *Tx) error {
		state, changed, err := tx.Repo(orgID, repoID).SetManualRepositorySubscription(t.Context(), actor, false)
		if err != nil || !changed || state.Subscribed || state.CollectionVersion != firstCollection+1 {
			t.Fatalf("unsubscribe = %+v changed=%v err=%v", state, changed, err)
		}
		state, changed, err = tx.Repo(orgID, repoID).SetManualRepositorySubscription(t.Context(), actor, false)
		if err != nil || changed || state.CollectionVersion != firstCollection+1 {
			t.Fatalf("repeated unsubscribe = %+v changed=%v err=%v", state, changed, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	repository := database.Repo(orgID, repoID)
	for _, item := range []struct {
		user uuid.UUID
		want bool
	}{{member, true}, {noAddress, false}, {serviceUser, false}} {
		got, err := repository.RepositoryNotificationActorEligible(t.Context(), item.user)
		if err != nil || got != item.want {
			t.Fatalf("actor eligibility %s = %v, want %v, err=%v", item.user, got, item.want, err)
		}
	}
	for _, userID := range []uuid.UUID{member, noAddress, serviceUser, outsider} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO repo_subscriptions
			(organization_id, repository_id, user_id, reason) VALUES ($1,$2,$3,'manual')`, orgID, repoID, userID); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err := repository.ManualRepositoryNotificationSubscribers(t.Context(), actor)
	if err != nil || len(candidates) != 1 || candidates[0].UserID != member {
		t.Fatalf("eligible subscribers = %+v err=%v", candidates, err)
	}
	recipient, err := RepositoryNotificationRecipientForDelivery(t.Context(), pool,
		repository.Scope(), member)
	if err != nil || recipient.UserID != member || recipient.Address == "" ||
		recipient.RepositoryOwner != "notification-org" || recipient.RepositoryName != "notification-repo" {
		t.Fatalf("delivery recipient = %+v err=%v", recipient, err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM repo_subscriptions
		WHERE organization_id=$1 AND repository_id=$2 AND user_id=$3`, orgID, repoID, member); err != nil {
		t.Fatal(err)
	}
	if _, err := RepositoryNotificationRecipientForDelivery(t.Context(), pool, repository.Scope(), member); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unsubscribed delivery eligibility = %v", err)
	}
}

func insertNotificationUser(t *testing.T, pool *pgxpool.Pool, login string, verified bool) uuid.UUID {
	t.Helper()
	userID := uuid.New()
	var address any
	var verifiedAt any
	if verified {
		address = login + "@example.test"
		verifiedAt = time.Now().UTC()
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO users
		(id, login, display_name, notification_email, notification_email_verified_at)
		VALUES ($1,$2,$2,$3,$4)`, userID, login, address, verifiedAt); err != nil {
		t.Fatal(err)
	}
	return userID
}

func insertNotificationMembership(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO org_memberships
		(organization_id, user_id, role, state, activated_at)
		VALUES ($1,$2,'reader','active',clock_timestamp())`, orgID, userID); err != nil {
		t.Fatal(err)
	}
}
