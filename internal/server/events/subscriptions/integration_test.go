package subscriptions_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/events/networkpolicy"
	"github.com/higress-group/issue-spec/internal/server/events/subscriptions"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestScopedSubscriptionAuthorizationSecretRotationAndRedaction(t *testing.T) {
	env := newEnvironment(t)
	ownerSubject := authz.Authenticated(env.owner)
	actor := subscriptions.ActorFromPrincipal(env.owner, "request-create")

	if _, err := env.service.Create(t.Context(), actor, ownerSubject, subscriptions.CreateInput{
		OrganizationID: env.scope.OrgID, URL: "http://runner.example.test/hook",
		EventTypes: []string{"issue_comment.created"},
	}); !errors.Is(err, subscriptions.ErrInvalidInput) {
		t.Fatalf("production HTTP URL error = %v", err)
	}
	organizationScoped, err := env.service.Create(t.Context(), actor, ownerSubject, subscriptions.CreateInput{
		OrganizationID: env.scope.OrgID, URL: "https://runner.example.test/org-hook",
		EventTypes: []string{" issue_comment.created "},
	})
	if err != nil || organizationScoped.Subscription.ScopeType != subscriptions.ScopeOrganization ||
		organizationScoped.Subscription.URL != "https://runner.example.test/org-hook" ||
		len(organizationScoped.Subscription.EventTypes) != 1 || organizationScoped.Subscription.EventTypes[0] != "issue_comment.created" {
		t.Fatalf("organization subscription = %+v, %v", organizationScoped, err)
	}
	var orgAudits int
	if err := env.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events
		WHERE organization_id = $1 AND repository_id IS NULL AND resource_id = $2`,
		env.scope.OrgID, organizationScoped.Subscription.ID).Scan(&orgAudits); err != nil || orgAudits != 1 {
		t.Fatalf("organization audit count=%d err=%v", orgAudits, err)
	}
	otherOrg, otherRepo := uuid.New(), uuid.New()
	if _, err := env.pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name) VALUES ($1, 'other', 'other')`, otherOrg); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name)
		VALUES ($1, $2, 'other', 'other')`, otherRepo, otherOrg); err != nil {
		t.Fatal(err)
	}
	if _, err := env.service.Create(t.Context(), actor, ownerSubject, subscriptions.CreateInput{
		OrganizationID: env.scope.OrgID, RepositoryID: &otherRepo,
		URL: "https://runner.example.test/cross-tenant", EventTypes: []string{"issue_comment.created"},
	}); !errors.Is(err, subscriptions.ErrNotFound) {
		t.Fatalf("cross-tenant repository error = %v", err)
	}
	created, err := env.service.Create(t.Context(), actor, ownerSubject, subscriptions.CreateInput{
		OrganizationID: env.scope.OrgID, RepositoryID: &env.scope.RepoID,
		URL: "https://runner.example.test/hook", EventTypes: []string{"issue_comment.edited", "issue_comment.created"},
		Retry: subscriptions.RetryPolicy{MaxAttempts: 4, InitialBackoff: 2 * time.Second, MaxBackoff: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Secret == "" || created.SecretVersion != 1 || created.Subscription.ScopeType != subscriptions.ScopeRepository {
		t.Fatalf("created = %+v", created)
	}
	var ciphertext []byte
	var keyID string
	if err := env.pool.QueryRow(t.Context(), `SELECT secret_ciphertext, encryption_key_id
		FROM webhook_secret_versions WHERE subscription_id = $1`, created.Subscription.ID).Scan(&ciphertext, &keyID); err != nil {
		t.Fatal(err)
	}
	if keyID != "primary" || bytes.Contains(ciphertext, []byte(created.Secret)) {
		t.Fatalf("secret storage key=%q ciphertext=%q", keyID, ciphertext)
	}
	var auditText string
	if err := env.pool.QueryRow(t.Context(), `SELECT metadata::text FROM audit_events
		WHERE resource_id = $1 ORDER BY created_at DESC LIMIT 1`, created.Subscription.ID).Scan(&auditText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditText, created.Secret) || strings.Contains(auditText, string(ciphertext)) {
		t.Fatalf("audit leaked secret material: %s", auditText)
	}

	reader := env.addMember(t, "reader", "reader")
	if _, err := env.service.Create(t.Context(), subscriptions.ActorFromPrincipal(reader, "reader-create"),
		authz.Authenticated(reader), subscriptions.CreateInput{OrganizationID: env.scope.OrgID,
			RepositoryID: &env.scope.RepoID, URL: "https://runner.example.test/reader",
			EventTypes: []string{"issue_comment.created"}}); !errors.Is(err, subscriptions.ErrForbidden) {
		t.Fatalf("reader create error = %v", err)
	}
	if listed, err := env.service.List(t.Context(), authz.Authenticated(reader), env.scope.OrgID, &env.scope.RepoID); !errors.Is(err, subscriptions.ErrForbidden) || listed != nil {
		t.Fatalf("reader list = %+v, %v, want integration-management denial", listed, err)
	}

	current := created.Subscription
	updates := make(chan error, 2)
	for index := range 2 {
		go func(index int) {
			_, err := env.service.Update(context.Background(), subscriptions.ActorFromPrincipal(env.owner, "update"),
				ownerSubject, env.scope.OrgID, current.ID, subscriptions.UpdateInput{
					ExpectedVersion: current.RepresentationVersion, URL: "https://runner.example.test/update-" + string(rune('a'+index)),
					Active: true, EventTypes: []string{"issue_comment.created"},
					Retry: subscriptions.RetryPolicy{MaxAttempts: 8, InitialBackoff: time.Second, MaxBackoff: time.Minute},
				})
			updates <- err
		}(index)
	}
	var successes, conflicts int
	for range 2 {
		err := <-updates
		if err == nil {
			successes++
		} else if errors.Is(err, subscriptions.ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("updates success=%d conflict=%d", successes, conflicts)
	}
	var beforeRotationVersion, beforeRotationCollection int64
	if err := env.pool.QueryRow(t.Context(), `SELECT representation_version FROM webhook_subscriptions
		WHERE id = $1`, created.Subscription.ID).Scan(&beforeRotationVersion); err != nil {
		t.Fatal(err)
	}
	if err := env.pool.QueryRow(t.Context(), `SELECT webhooks_collection_version FROM repos
		WHERE id = $1`, env.scope.RepoID).Scan(&beforeRotationCollection); err != nil {
		t.Fatal(err)
	}

	rotations := make(chan subscriptions.SecretResult, 2)
	rotationErrors := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			rotated, err := env.service.RotateSecret(context.Background(), subscriptions.ActorFromPrincipal(env.owner, "rotate"),
				ownerSubject, env.scope.OrgID, created.Subscription.ID)
			rotations <- rotated
			rotationErrors <- err
		}()
	}
	wait.Wait()
	close(rotations)
	close(rotationErrors)
	for err := range rotationErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	secrets := map[string]struct{}{created.Secret: {}}
	for result := range rotations {
		secrets[result.Secret] = struct{}{}
	}
	if len(secrets) != 3 {
		t.Fatalf("rotation plaintexts were not unique")
	}
	var afterRotationVersion, afterRotationCollection int64
	if err := env.pool.QueryRow(t.Context(), `SELECT representation_version FROM webhook_subscriptions
		WHERE id = $1`, created.Subscription.ID).Scan(&afterRotationVersion); err != nil {
		t.Fatal(err)
	}
	if err := env.pool.QueryRow(t.Context(), `SELECT webhooks_collection_version FROM repos
		WHERE id = $1`, env.scope.RepoID).Scan(&afterRotationCollection); err != nil {
		t.Fatal(err)
	}
	if afterRotationVersion != beforeRotationVersion+2 || afterRotationCollection != beforeRotationCollection+2 {
		t.Fatalf("rotation visibility version=%d->%d collection=%d->%d", beforeRotationVersion,
			afterRotationVersion, beforeRotationCollection, afterRotationCollection)
	}
	accepted, err := env.service.AcceptedSecrets(t.Context(), env.scope.OrgID, created.Subscription.ID, time.Now().UTC())
	if err != nil || len(accepted) != 2 || accepted[0].Version != 3 || accepted[1].Version != 2 {
		t.Fatalf("accepted overlap = %+v, %v", accepted, err)
	}
	if _, err := env.pool.Exec(t.Context(), `CREATE FUNCTION reject_test_webhook_secret_insert()
		RETURNS trigger LANGUAGE plpgsql AS $function$ BEGIN
		RAISE EXCEPTION 'injected secret insert failure'; END $function$;
		CREATE TRIGGER reject_test_webhook_secret_insert BEFORE INSERT ON webhook_secret_versions
		FOR EACH ROW EXECUTE FUNCTION reject_test_webhook_secret_insert()`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.service.RotateSecret(t.Context(), actor, ownerSubject,
		env.scope.OrgID, created.Subscription.ID); err == nil {
		t.Fatal("injected rotation failure committed")
	}
	var failedRotationVersion, failedRotationCollection int64
	if err := env.pool.QueryRow(t.Context(), `SELECT representation_version FROM webhook_subscriptions
		WHERE id = $1`, created.Subscription.ID).Scan(&failedRotationVersion); err != nil {
		t.Fatal(err)
	}
	if err := env.pool.QueryRow(t.Context(), `SELECT webhooks_collection_version FROM repos
		WHERE id = $1`, env.scope.RepoID).Scan(&failedRotationCollection); err != nil {
		t.Fatal(err)
	}
	if failedRotationVersion != afterRotationVersion || failedRotationCollection != afterRotationCollection {
		t.Fatalf("failed rotation changed version=%d/%d collection=%d/%d", failedRotationVersion,
			afterRotationVersion, failedRotationCollection, afterRotationCollection)
	}
	acceptedAfterFailure, err := env.service.AcceptedSecrets(t.Context(), env.scope.OrgID,
		created.Subscription.ID, time.Now())
	if err != nil || len(acceptedAfterFailure) != 2 || acceptedAfterFailure[0].Version != 3 || acceptedAfterFailure[1].Version != 2 {
		t.Fatalf("failed rotation changed accepted secrets = %+v, %v", acceptedAfterFailure, err)
	}
	afterOverlap, err := env.service.AcceptedSecrets(t.Context(), env.scope.OrgID, created.Subscription.ID, time.Now().Add(time.Hour))
	if err != nil || len(afterOverlap) != 1 || afterOverlap[0].Version != 3 {
		t.Fatalf("accepted after overlap = %+v, %v", afterOverlap, err)
	}
	if err := env.service.Revoke(t.Context(), actor, ownerSubject, env.scope.OrgID, created.Subscription.ID); err != nil {
		t.Fatal(err)
	}
	accepted, err = env.service.AcceptedSecrets(t.Context(), env.scope.OrgID, created.Subscription.ID, time.Now())
	if err != nil || len(accepted) != 0 {
		t.Fatalf("accepted after revoke = %+v, %v", accepted, err)
	}
	var active, revoked int
	if err := env.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE active),
		count(*) FILTER (WHERE revoked_at IS NOT NULL) FROM webhook_secret_versions
		WHERE subscription_id = $1`, created.Subscription.ID).Scan(&active, &revoked); err != nil {
		t.Fatal(err)
	}
	if active != 0 || revoked != 3 {
		t.Fatalf("secret rows active=%d revoked=%d", active, revoked)
	}
	revokedSubscription, err := env.service.Get(t.Context(), ownerSubject, env.scope.OrgID, created.Subscription.ID)
	if err != nil || revokedSubscription.Active || revokedSubscription.RevokedAt == nil {
		t.Fatalf("revoked subscription = %+v, %v", revokedSubscription, err)
	}
	revokedVersion, revokedAt := revokedSubscription.RepresentationVersion, *revokedSubscription.RevokedAt
	var revokedCollectionVersion int64
	if err := env.pool.QueryRow(t.Context(), `SELECT webhooks_collection_version FROM repos WHERE id = $1`,
		env.scope.RepoID).Scan(&revokedCollectionVersion); err != nil {
		t.Fatal(err)
	}
	if err := env.service.Revoke(t.Context(), actor, ownerSubject, env.scope.OrgID, created.Subscription.ID); err != nil {
		t.Fatalf("idempotent revoke: %v", err)
	}
	afterRepeat, err := env.service.Get(t.Context(), ownerSubject, env.scope.OrgID, created.Subscription.ID)
	if err != nil || afterRepeat.RepresentationVersion != revokedVersion || afterRepeat.RevokedAt == nil || !afterRepeat.RevokedAt.Equal(revokedAt) {
		t.Fatalf("repeated revoke mutated terminal state: before=%+v after=%+v err=%v", revokedSubscription, afterRepeat, err)
	}
	var repeatedCollectionVersion, revokeAudits int64
	if err := env.pool.QueryRow(t.Context(), `SELECT webhooks_collection_version FROM repos WHERE id = $1`,
		env.scope.RepoID).Scan(&repeatedCollectionVersion); err != nil {
		t.Fatal(err)
	}
	if err := env.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE resource_id = $1
		AND action = 'webhook.subscription.revoked'`, created.Subscription.ID).Scan(&revokeAudits); err != nil {
		t.Fatal(err)
	}
	if repeatedCollectionVersion != revokedCollectionVersion || revokeAudits != 1 {
		t.Fatalf("repeated revoke collection=%d/%d audits=%d", revokedCollectionVersion, repeatedCollectionVersion, revokeAudits)
	}
	if _, err := env.service.Update(t.Context(), actor, ownerSubject, env.scope.OrgID, created.Subscription.ID,
		subscriptions.UpdateInput{ExpectedVersion: revokedVersion, URL: "https://runner.example.test/resume",
			Active: true, EventTypes: []string{"issue_comment.created"}}); !errors.Is(err, subscriptions.ErrRevoked) {
		t.Fatalf("revoked update error = %v", err)
	}
	if _, err := env.service.RotateSecret(t.Context(), actor, ownerSubject, env.scope.OrgID,
		created.Subscription.ID); !errors.Is(err, subscriptions.ErrRevoked) {
		t.Fatalf("revoked rotate error = %v", err)
	}
	if _, err := env.pool.Exec(t.Context(), `UPDATE webhook_subscriptions SET active = true WHERE id = $1`,
		created.Subscription.ID); err == nil {
		t.Fatal("database trigger allowed revoked subscription to resume")
	}
}

func TestDestinationPreflightRejectsUnsafeCreateAndUpdateTargets(t *testing.T) {
	env := newEnvironment(t)
	var resolverCalls atomic.Int32
	preflight := networkpolicy.Preflight{
		Policy: networkpolicy.Policy{Production: true},
		Resolver: preflightResolver{calls: &resolverCalls, addresses: map[string][]net.IPAddr{
			"public.example":  {{IP: net.ParseIP("93.184.216.34")}},
			"private.example": {{IP: net.ParseIP("10.0.0.2")}},
			"mixed.example": {
				{IP: net.ParseIP("93.184.216.34")},
				{IP: net.ParseIP("10.0.0.2")},
			},
		}},
	}
	service, err := subscriptions.New(store.New(env.pool), env.authorizer, env.keys,
		subscriptions.Config{Production: true, SecretOverlap: 10 * time.Minute, DestinationPreflight: preflight})
	if err != nil {
		t.Fatal(err)
	}
	actor := subscriptions.ActorFromPrincipal(env.owner, "destination-preflight")
	subject := authz.Authenticated(env.owner)
	reader := env.addMember(t, "preflight-reader", "reader")
	readerSubject := authz.Authenticated(reader)
	if _, err := service.Create(t.Context(), subscriptions.ActorFromPrincipal(reader, "denied-create"), readerSubject,
		subscriptions.CreateInput{OrganizationID: env.scope.OrgID, RepositoryID: &env.scope.RepoID,
			URL: "https://public.example/hook", EventTypes: []string{"issue_comment.created"}}); !errors.Is(err, subscriptions.ErrForbidden) {
		t.Fatalf("unauthorized create error = %v", err)
	}
	if resolverCalls.Load() != 0 {
		t.Fatalf("unauthorized create performed %d DNS lookups", resolverCalls.Load())
	}
	for _, destination := range []string{
		" https://public.example/hook",
		"https://public.example/hook ",
		"https://runner:secret@public.example/hook",
		"https://public.example/hook?access_token=secret",
		"https://public.example/hook?",
		"https://public.example/hook#secret",
		"https://public.example\\@private.example/hook",
		"https://127.0.0.1/hook",
		"https://169.254.169.254/hook",
		"https://100.100.100.200/hook",
		"https://private.example/hook",
		"https://mixed.example/hook",
	} {
		if _, err := service.Create(t.Context(), actor, subject, subscriptions.CreateInput{
			OrganizationID: env.scope.OrgID, RepositoryID: &env.scope.RepoID,
			URL: destination, EventTypes: []string{"issue_comment.created"},
		}); !errors.Is(err, subscriptions.ErrInvalidInput) {
			t.Fatalf("unsafe create destination %q error = %v", destination, err)
		}
	}
	created, err := service.Create(t.Context(), actor, subject, subscriptions.CreateInput{
		OrganizationID: env.scope.OrgID, RepositoryID: &env.scope.RepoID,
		URL: "https://public.example/hook", EventTypes: []string{"issue_comment.created"},
	})
	if err != nil {
		t.Fatal(err)
	}
	callsBeforeDeniedUpdate := resolverCalls.Load()
	if _, err := service.Update(t.Context(), subscriptions.ActorFromPrincipal(reader, "denied-update"), readerSubject,
		env.scope.OrgID, created.Subscription.ID, subscriptions.UpdateInput{
			ExpectedVersion: created.Subscription.RepresentationVersion, URL: "https://public.example/updated",
			Active: true, EventTypes: []string{"issue_comment.created"},
		}); !errors.Is(err, subscriptions.ErrForbidden) {
		t.Fatalf("unauthorized update error = %v", err)
	}
	if resolverCalls.Load() != callsBeforeDeniedUpdate {
		t.Fatalf("unauthorized update performed DNS lookup: before=%d after=%d",
			callsBeforeDeniedUpdate, resolverCalls.Load())
	}
	for _, destination := range []string{
		"https://public.example/hook?access_token=secret",
		"https://public.example/hook?",
		"https://public.example/hook#secret",
		"https://runner:secret@public.example/hook",
		"https://127.0.0.1/hook",
		"https://private.example/hook",
		"https://mixed.example/hook",
	} {
		if _, err := service.Update(t.Context(), actor, subject, env.scope.OrgID, created.Subscription.ID,
			subscriptions.UpdateInput{ExpectedVersion: created.Subscription.RepresentationVersion,
				URL: destination, Active: true, EventTypes: []string{"issue_comment.created"},
			}); !errors.Is(err, subscriptions.ErrInvalidInput) {
			t.Fatalf("unsafe update destination %q error = %v", destination, err)
		}
	}
	unchanged, err := service.Get(t.Context(), subject, env.scope.OrgID, created.Subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.URL != created.Subscription.URL || unchanged.RepresentationVersion != created.Subscription.RepresentationVersion {
		t.Fatalf("rejected update mutated subscription: before=%+v after=%+v", created.Subscription, unchanged)
	}
}

func TestDestinationQueryIsBoundToExactDestinationOnUpdate(t *testing.T) {
	env := newEnvironment(t)
	actor := subscriptions.ActorFromPrincipal(env.owner, "destination-query-rebind")
	subject := authz.Authenticated(env.owner)
	policy := subscriptions.ContentPolicy{IssueActions: []string{"opened"}, IssueKinds: []string{"ordinary"}, ActorClasses: []string{"human", "automation"}}
	created, err := env.service.Create(t.Context(), actor, subject, subscriptions.CreateInput{
		OrganizationID: env.scope.OrgID, RepositoryID: &env.scope.RepoID,
		URL:            "https://robot.example.test/hook?access_token=old-secret&mode=sync",
		DeliveryFormat: subscriptions.DeliveryFormatGitHubV3, SigningMode: subscriptions.SigningModeNone,
		ContentPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Subscription.HasDestinationQuery || strings.Contains(created.Subscription.URL, "old-secret") {
		t.Fatalf("created destination was not redacted: %+v", created.Subscription)
	}

	preserved, err := env.service.Update(t.Context(), actor, subject, env.scope.OrgID, created.Subscription.ID,
		subscriptions.UpdateInput{ExpectedVersion: created.Subscription.RepresentationVersion,
			URL: created.Subscription.URL, DeliveryFormat: subscriptions.DeliveryFormatGitHubV3,
			SigningMode: subscriptions.SigningModeNone, ContentPolicy: policy, Active: true})
	if err != nil || !preserved.HasDestinationQuery {
		t.Fatalf("same-destination query preservation = %+v, %v", preserved, err)
	}

	cleared, err := env.service.Update(t.Context(), actor, subject, env.scope.OrgID, created.Subscription.ID,
		subscriptions.UpdateInput{ExpectedVersion: preserved.RepresentationVersion,
			URL: "https://robot.example.test/other-path", DeliveryFormat: subscriptions.DeliveryFormatGitHubV3,
			SigningMode: subscriptions.SigningModeNone, ContentPolicy: policy, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.HasDestinationQuery || cleared.DestinationQueryVersion != 0 || len(cleared.DestinationQuery) != 0 {
		t.Fatalf("path change retained encrypted destination query: %+v", cleared)
	}
	var storedQuery []byte
	var auditText string
	if err := env.pool.QueryRow(t.Context(), `SELECT COALESCE(destination_query_ciphertext, ''::bytea)
		FROM webhook_subscriptions WHERE id=$1`, created.Subscription.ID).Scan(&storedQuery); err != nil {
		t.Fatal(err)
	}
	if err := env.pool.QueryRow(t.Context(), `SELECT string_agg(metadata::text, ' ' ORDER BY created_at)
		FROM audit_events WHERE resource_id=$1`, created.Subscription.ID).Scan(&auditText); err != nil {
		t.Fatal(err)
	}
	if len(storedQuery) != 0 || strings.Contains(auditText, "old-secret") || strings.Contains(auditText, "access_token") {
		t.Fatalf("cleared destination leaked credential: ciphertext=%d audit=%q", len(storedQuery), auditText)
	}

	replaced, err := env.service.Update(t.Context(), actor, subject, env.scope.OrgID, created.Subscription.ID,
		subscriptions.UpdateInput{ExpectedVersion: cleared.RepresentationVersion,
			URL: "https://other.example.test/hook?access_token=new-secret", DeliveryFormat: subscriptions.DeliveryFormatGitHubV3,
			SigningMode: subscriptions.SigningModeNone, ContentPolicy: policy, Active: true})
	if err != nil || !replaced.HasDestinationQuery {
		t.Fatalf("explicit cross-host replacement = %+v, %v", replaced, err)
	}
	plaintext, err := env.service.DecryptDestinationQuery(t.Context(), replaced.ID,
		replaced.DestinationQueryKeyID, replaced.DestinationQueryVersion, replaced.DestinationQuery)
	if err != nil || string(plaintext) != "access_token=new-secret" {
		t.Fatalf("replacement query = %q, %v", plaintext, err)
	}
}

func TestLegacyUnsafeDestinationFailsClosedWithoutReflectingOrBlockingRevocation(t *testing.T) {
	env := newEnvironment(t)
	actor := subscriptions.ActorFromPrincipal(env.owner, "legacy-unsafe")
	subject := authz.Authenticated(env.owner)
	created, err := env.service.Create(t.Context(), actor, subject, subscriptions.CreateInput{
		OrganizationID: env.scope.OrgID, RepositoryID: &env.scope.RepoID,
		URL: "https://runner.example.test/hook", EventTypes: []string{"issue_comment.created"},
	})
	if err != nil {
		t.Fatal(err)
	}
	const legacyURL = "https://runner.example.test/hook?access_token=legacy-secret"
	if _, err := env.pool.Exec(t.Context(), `UPDATE webhook_subscriptions SET url = $2 WHERE id = $1`,
		created.Subscription.ID, legacyURL); err != nil {
		t.Fatal(err)
	}
	reader := env.addMember(t, "legacy-reader", "reader")
	listed, err := env.service.List(t.Context(), authz.Authenticated(reader), env.scope.OrgID, &env.scope.RepoID)
	if !errors.Is(err, subscriptions.ErrForbidden) || listed != nil || strings.Contains(err.Error(), "access_token") {
		t.Fatalf("legacy list result=%+v error=%v", listed, err)
	}
	if _, err := env.service.Get(t.Context(), subject, env.scope.OrgID, created.Subscription.ID); !errors.Is(err, subscriptions.ErrUnsafeDestination) || strings.Contains(err.Error(), "legacy-secret") {
		t.Fatalf("legacy get error=%v", err)
	}
	if _, err := env.service.RotateSecret(t.Context(), actor, subject, env.scope.OrgID, created.Subscription.ID); !errors.Is(err, subscriptions.ErrUnsafeDestination) {
		t.Fatalf("legacy rotate error=%v", err)
	}
	if err := env.service.Revoke(t.Context(), actor, subject, env.scope.OrgID, created.Subscription.ID); err != nil {
		t.Fatalf("revoke legacy unsafe subscription: %v", err)
	}
	var active bool
	var revokedAt *time.Time
	if err := env.pool.QueryRow(t.Context(), `SELECT active, revoked_at FROM webhook_subscriptions WHERE id = $1`,
		created.Subscription.ID).Scan(&active, &revokedAt); err != nil || active || revokedAt == nil {
		t.Fatalf("legacy terminal state active=%v revoked_at=%v error=%v", active, revokedAt, err)
	}
}

func TestDestinationPreflightReauthorizesInsideCreateTransaction(t *testing.T) {
	env := newEnvironment(t)
	resolver := &blockingPreflightResolver{entered: make(chan struct{}), release: make(chan struct{})}
	service, err := subscriptions.New(store.New(env.pool), env.authorizer, env.keys,
		subscriptions.Config{Production: true, SecretOverlap: 10 * time.Minute,
			DestinationPreflight: networkpolicy.Preflight{Policy: networkpolicy.Policy{Production: true}, Resolver: resolver}})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := service.Create(context.Background(), subscriptions.ActorFromPrincipal(env.owner, "revoked-during-preflight"),
			authz.Authenticated(env.owner), subscriptions.CreateInput{OrganizationID: env.scope.OrgID,
				RepositoryID: &env.scope.RepoID, URL: "https://public.example/hook",
				EventTypes: []string{"issue_comment.created"}})
		result <- err
	}()
	select {
	case <-resolver.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("create did not reach destination preflight")
	}
	if _, err := env.pool.Exec(t.Context(), `DELETE FROM org_memberships
		WHERE organization_id = $1 AND user_id = $2`, env.scope.OrgID, env.owner.User.ID); err != nil {
		t.Fatal(err)
	}
	close(resolver.release)
	if err := <-result; !errors.Is(err, subscriptions.ErrForbidden) && !errors.Is(err, subscriptions.ErrNotFound) {
		t.Fatalf("create after permission revocation error = %v", err)
	}
	var count int
	if err := env.pool.QueryRow(t.Context(), `SELECT count(*) FROM webhook_subscriptions
		WHERE organization_id = $1`, env.scope.OrgID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("subscription count after revoked create = %d, %v", count, err)
	}
}

type environment struct {
	pool       *pgxpool.Pool
	scope      models.RepoScope
	owner      serverauth.Principal
	authorizer *authz.Service
	keys       *subscriptions.Keyring
	service    *subscriptions.Service
}

func newEnvironment(t *testing.T) *environment {
	pool := migratedPool(t)
	orgID, repoID, userID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, 'owner', 'owner')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name, base_permission)
		VALUES ($1, 'acme', 'acme', 'read')`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos
		(id, organization_id, name, display_name, visibility, contribution_policy)
		VALUES ($1, $2, 'widgets', 'widgets', 'private', 'members')`, repoID, orgID); err != nil {
		t.Fatal(err)
	}
	insertMembership(t, pool, orgID, userID, "owner")
	sessionID := insertSession(t, pool, userID)
	owner := serverauth.Principal{User: serverauth.User{ID: userID, Login: "owner", Status: "active"},
		Kind: serverauth.CredentialSession, CredentialID: sessionID}
	authorizer, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := subscriptions.NewKeyring("primary", map[string][]byte{"primary": []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	service, err := subscriptions.New(store.New(pool), authorizer, keys,
		subscriptions.Config{Production: true, SecretOverlap: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return &environment{pool: pool, scope: models.RepoScope{OrgID: orgID, RepoID: repoID}, owner: owner,
		authorizer: authorizer, keys: keys, service: service}
}

type preflightResolver struct {
	addresses map[string][]net.IPAddr
	calls     *atomic.Int32
}

func (r preflightResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if r.calls != nil {
		r.calls.Add(1)
	}
	return r.addresses[host], nil
}

type blockingPreflightResolver struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingPreflightResolver) LookupIPAddr(ctx context.Context, _ string) ([]net.IPAddr, error) {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.release:
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
}

func (e *environment) addMember(t *testing.T, login, role string) serverauth.Principal {
	id := uuid.New()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $2)`, id, login); err != nil {
		t.Fatal(err)
	}
	insertMembership(t, e.pool, e.scope.OrgID, id, role)
	return serverauth.Principal{User: serverauth.User{ID: id, Login: login, Status: "active"},
		Kind: serverauth.CredentialSession, CredentialID: insertSession(t, e.pool, id)}
}

func insertMembership(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID, role string) {
	if _, err := pool.Exec(t.Context(), `INSERT INTO org_memberships
		(organization_id, user_id, role, state, activated_at) VALUES ($1, $2, $3, 'active', clock_timestamp())`,
		orgID, userID, role); err != nil {
		t.Fatal(err)
	}
}

func insertSession(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) uuid.UUID {
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO sessions
		(id, user_id, token_prefix, token_hash, csrf_hash, idle_expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp() + interval '1 hour', clock_timestamp() + interval '2 hours')`,
		id, userID, "session-"+id.String(), []byte(id.String()), []byte("csrf-"+id.String())); err != nil {
		t.Fatal(err)
	}
	return id
}

func migratedPool(t *testing.T) *pgxpool.Pool {
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL")
	}
	admin, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "subscription_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") })
	config, _ := pgxpool.ParseConfig(databaseURL)
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.RunMigrations(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
