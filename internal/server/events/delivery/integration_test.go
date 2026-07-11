package delivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/events/delivery"
	"github.com/higress-group/issue-spec/internal/server/events/networkpolicy"
	"github.com/higress-group/issue-spec/internal/server/events/subscriptions"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExpansionLeasesOrderingDualWorkersAndGracefulRun(t *testing.T) {
	env := newEnvironment(t, 3, 10*time.Minute)
	alreadyCanceled, cancelImmediately := context.WithCancel(context.Background())
	cancelImmediately()
	if err := env.service.Run(alreadyCanceled); err != nil {
		t.Fatalf("already-canceled run = %v", err)
	}
	first := env.enqueue(t, 1)
	second := env.enqueue(t, 2)
	env.expandAll(t)
	if got := rowCount(t, env.pool, "webhook_deliveries"); got != 2 {
		t.Fatalf("deliveries = %d", got)
	}
	if _, err := env.pool.Exec(t.Context(), `UPDATE event_outbox SET published_at = NULL WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	if expanded, err := env.service.ExpandOne(t.Context()); err != nil || !expanded || rowCount(t, env.pool, "webhook_deliveries") != 2 {
		t.Fatalf("idempotent expansion expanded=%v rows=%d err=%v", expanded, rowCount(t, env.pool, "webhook_deliveries"), err)
	}

	claimed, err := env.service.ClaimOne(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.EventID != first {
		t.Fatalf("first claimed event = %s, want %s", claimed.EventID, first)
	}
	if _, err := env.service.ClaimOne(t.Context()); !errors.Is(err, delivery.ErrNoWork) {
		t.Fatalf("later repository event bypassed active lease: %v", err)
	}
	env.clock.Advance(3 * time.Second)
	if err := env.service.ProcessOne(t.Context()); err != nil {
		t.Fatalf("crash lease recovery: %v", err)
	}

	results := make(chan error, 2)
	for range 2 {
		go func() { results <- env.service.ProcessOne(context.Background()) }()
	}
	var sent, empty int
	for range 2 {
		err := <-results
		if err == nil {
			sent++
		} else if errors.Is(err, delivery.ErrNoWork) {
			empty++
		} else {
			t.Fatal(err)
		}
	}
	if sent != 1 || empty != 1 || env.sender.CallCount() != 2 {
		t.Fatalf("dual worker sent=%d empty=%d calls=%d", sent, empty, env.sender.CallCount())
	}
	sequences := env.sender.Sequences(t)
	if len(sequences) != 2 || sequences[0] != 1 || sequences[1] != 2 {
		t.Fatalf("delivery order = %v", sequences)
	}
	if state := deliveryState(t, env.pool, second); state != "succeeded" {
		t.Fatalf("second event state = %s", state)
	}

	env.enqueue(t, 3)
	runContext, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() { runResult <- env.service.Run(runContext) }()
	deadline := time.Now().Add(5 * time.Second)
	for env.sender.CallCount() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-runResult; err != nil {
		t.Fatalf("graceful run: %v", err)
	}
	if env.sender.CallCount() != 3 {
		t.Fatalf("run loop calls = %d", env.sender.CallCount())
	}
}

func TestRunDoesNotLoseWorkerErrorWhenWorkersFinish(t *testing.T) {
	env := newEnvironment(t, 2, time.Minute)
	if _, err := env.pool.Exec(t.Context(), `DROP TABLE event_outbox CASCADE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := env.service.Run(ctx); err == nil {
		t.Fatal("worker database error was lost when workers completed")
	}
}

func TestRunBoundsConcurrentRequests(t *testing.T) {
	env := newEnvironment(t, 2, time.Minute)
	for index := range 3 {
		if _, err := env.subscriptions.Create(t.Context(), subscriptions.ActorFromPrincipal(env.owner, "create-more"),
			authz.Authenticated(env.owner), subscriptions.CreateInput{OrganizationID: env.scope.OrgID,
				RepositoryID: &env.scope.RepoID, URL: fmt.Sprintf("https://runner-%d.example.test", index),
				EventTypes: []string{"issue_comment.created"}, Retry: subscriptions.RetryPolicy{
					MaxAttempts: 2, InitialBackoff: time.Second, MaxBackoff: 10 * time.Second}}); err != nil {
			t.Fatal(err)
		}
	}
	env.sender.SetDelay(50 * time.Millisecond)
	env.enqueue(t, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.service.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for env.sender.CallCount() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if env.sender.CallCount() != 4 || env.sender.MaxActive() != 2 {
		t.Fatalf("calls=%d max active=%d", env.sender.CallCount(), env.sender.MaxActive())
	}
}

func TestRetryMatrixDLQManualRedeliveryAuthorizationAndRedaction(t *testing.T) {
	env := newEnvironment(t, 2, 10*time.Minute)
	subject := authz.Authenticated(env.owner)

	tests := []struct {
		name      string
		outcome   sendOutcome
		wantDelay time.Duration
	}{
		{"408", sendOutcome{status: http.StatusRequestTimeout}, time.Second},
		{"429 seconds", sendOutcome{status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {"7"}}}, 7 * time.Second},
		{"429 capped", sendOutcome{status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {"100"}}}, 10 * time.Second},
		{"429 overflow capped", sendOutcome{status: http.StatusTooManyRequests,
			header: http.Header{"Retry-After": {"9223372036854775807"}}}, 10 * time.Second},
		{"429 date", sendOutcome{status: http.StatusTooManyRequests, retryDate: 9 * time.Second}, 8 * time.Second},
		{"500", sendOutcome{status: http.StatusInternalServerError}, time.Second},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			eventID := env.enqueue(t, 10+index)
			env.expandAll(t)
			outcome := test.outcome
			if outcome.retryDate > 0 {
				outcome.header = http.Header{"Retry-After": {env.clock.Now().Add(outcome.retryDate).Format(http.TimeFormat)}}
			}
			env.sender.Set(outcome, sendOutcome{status: http.StatusOK})
			before := env.clock.Now()
			if err := env.service.ProcessOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			state, next := deliveryStateAndNext(t, env.pool, eventID)
			if state != "pending" || next.Sub(before) < test.wantDelay {
				t.Fatalf("retry state=%s delay=%s", state, next.Sub(before))
			}
			env.clock.Set(next)
			if err := env.service.ProcessOne(t.Context()); err != nil || deliveryState(t, env.pool, eventID) != "succeeded" {
				t.Fatalf("retry completion state=%s err=%v", deliveryState(t, env.pool, eventID), err)
			}
		})
	}

	for index, status := range []int{http.StatusBadRequest, http.StatusFound} {
		eventID := env.enqueue(t, 20+index)
		env.expandAll(t)
		env.sender.Set(sendOutcome{status: status})
		if err := env.service.ProcessOne(t.Context()); err != nil {
			t.Fatal(err)
		}
		if state := deliveryState(t, env.pool, eventID); state != "failed" {
			t.Fatalf("terminal status %d state=%s", status, state)
		}
	}

	deadEvent := env.enqueue(t, 30)
	env.expandAll(t)
	env.sender.Set(sendOutcome{status: 500}, sendOutcome{status: 500})
	if err := env.service.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, next := deliveryStateAndNext(t, env.pool, deadEvent)
	env.clock.Set(next)
	if err := env.service.ProcessOne(t.Context()); err != nil || deliveryState(t, env.pool, deadEvent) != "dead" {
		t.Fatalf("DLQ state=%s err=%v", deliveryState(t, env.pool, deadEvent), err)
	}
	deliveryID := deliveryIDForEvent(t, env.pool, deadEvent)
	redelivered, err := env.service.Redeliver(t.Context(), delivery.ActorFromPrincipal(env.owner, "redeliver"),
		subject, env.scope, deliveryID)
	if err != nil || redelivered.State != "pending" {
		t.Fatalf("manual redelivery=%+v err=%v", redelivered, err)
	}
	env.sender.Set(sendOutcome{status: http.StatusOK})
	if err := env.service.ProcessOne(t.Context()); err != nil || deliveryState(t, env.pool, deadEvent) != "succeeded" {
		t.Fatalf("manual redelivery completion=%s err=%v", deliveryState(t, env.pool, deadEvent), err)
	}

	leakEvent := env.enqueue(t, 31)
	env.expandAll(t)
	env.sender.Set(sendOutcome{leakSecret: true}, sendOutcome{status: http.StatusOK})
	if err := env.service.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	var recorded string
	if err := env.pool.QueryRow(t.Context(), `SELECT request_headers::text || ' ' ||
		COALESCE(response_headers::text, '') || ' ' || COALESCE(error, '')
		FROM webhook_delivery_attempts attempt JOIN webhook_deliveries delivery
		ON delivery.organization_id = attempt.organization_id AND delivery.repository_id = attempt.repository_id
		AND delivery.id = attempt.delivery_id WHERE delivery.event_id = $1`, leakEvent).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(recorded, env.secret) || strings.Contains(strings.ToLower(recorded), "authorization") {
		t.Fatalf("attempt leaked credential: %s", recorded)
	}
	_, next = deliveryStateAndNext(t, env.pool, leakEvent)
	env.clock.Set(next)
	if err := env.service.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}

	reader := env.addMember(t, "reader", "reader")
	if _, err := env.service.List(t.Context(), authz.Authenticated(reader), env.scope); !errors.Is(err, delivery.ErrForbidden) {
		t.Fatalf("reader inspect error = %v", err)
	}
	outsider := env.addUser(t, "outsider")
	if _, err := env.service.List(t.Context(), authz.Authenticated(outsider), env.scope); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("outsider inspect error = %v", err)
	}
	if _, err := env.service.Get(t.Context(), subject,
		models.RepoScope{OrgID: env.scope.OrgID, RepoID: uuid.New()}, deliveryID); !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("cross-tenant inspect error = %v", err)
	}
	detail, err := env.service.Get(t.Context(), subject, env.scope, deliveryID)
	if err != nil || detail.Attempts == nil {
		t.Fatalf("delivery detail=%+v err=%v", detail, err)
	}
}

func TestSecretRotationOverlapExpiryAndRevocation(t *testing.T) {
	env := newEnvironment(t, 2, 2*time.Minute)
	subject := authz.Authenticated(env.owner)
	actor := subscriptions.ActorFromPrincipal(env.owner, "rotate")

	firstEvent := env.enqueue(t, 41)
	env.expandAll(t)
	rotated, err := env.subscriptions.RotateSecret(t.Context(), actor, subject,
		env.scope.OrgID, env.subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	env.sender.Set(sendOutcome{status: http.StatusOK})
	if err := env.service.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sent := env.sender.LastSecret(); sent != env.secret {
		t.Fatalf("overlap delivery secret = %q, want previous", sent)
	}
	if deliveryState(t, env.pool, firstEvent) != "succeeded" {
		t.Fatal("overlap delivery failed")
	}

	secondEvent := env.enqueue(t, 42)
	env.expandAll(t)
	newest, err := env.subscriptions.RotateSecret(t.Context(), actor, subject,
		env.scope.OrgID, env.subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	env.clock.Advance(3 * time.Minute)
	env.sender.Set(sendOutcome{status: http.StatusOK})
	if err := env.service.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sent := env.sender.LastSecret(); sent != newest.Secret || sent == rotated.Secret {
		t.Fatalf("expired previous secret delivery = %q", sent)
	}
	var storedVersion int64
	if err := env.pool.QueryRow(t.Context(), `SELECT secret.version FROM webhook_deliveries delivery
		JOIN webhook_secret_versions secret ON secret.id = delivery.secret_version_id
		WHERE delivery.event_id = $1`, secondEvent).Scan(&storedVersion); err != nil || storedVersion != newest.SecretVersion {
		t.Fatalf("rebound secret version=%d err=%v", storedVersion, err)
	}

	revokedEvent := env.enqueue(t, 43)
	env.expandAll(t)
	if err := env.subscriptions.Revoke(t.Context(), actor, subject, env.scope.OrgID, env.subscription.ID); err != nil {
		t.Fatal(err)
	}
	beforeCalls := env.sender.CallCount()
	if err := env.service.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state := deliveryState(t, env.pool, revokedEvent); state != "failed" || env.sender.CallCount() != beforeCalls {
		t.Fatalf("revoked delivery state=%s calls=%d/%d", state, beforeCalls, env.sender.CallCount())
	}
}

type testEnvironment struct {
	pool          *pgxpool.Pool
	scope         models.RepoScope
	owner         serverauth.Principal
	subscription  subscriptions.Subscription
	secret        string
	subscriptions *subscriptions.Service
	service       *delivery.Service
	sender        *fakeSender
	clock         *manualClock
}

func newEnvironment(t *testing.T, maxAttempts int, overlap time.Duration) *testEnvironment {
	pool := migratedPool(t)
	orgID, repoID, userID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, 'owner', 'owner')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name, base_permission) VALUES ($1, 'acme', 'acme', 'read')`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name,
		visibility, contribution_policy) VALUES ($1, $2, 'widgets', 'widgets', 'private', 'members')`, repoID, orgID); err != nil {
		t.Fatal(err)
	}
	insertMembership(t, pool, orgID, userID, "owner")
	owner := principal(t, pool, userID, "owner")
	authorizer, _ := authz.New(pool)
	keys, _ := subscriptions.NewKeyring("primary", map[string][]byte{"primary": []byte(strings.Repeat("k", 32))})
	subscriptionService, err := subscriptions.New(store.New(pool), authorizer, keys,
		subscriptions.Config{Production: true, SecretOverlap: overlap})
	if err != nil {
		t.Fatal(err)
	}
	scope := models.RepoScope{OrgID: orgID, RepoID: repoID}
	created, err := subscriptionService.Create(t.Context(), subscriptions.ActorFromPrincipal(owner, "create"),
		authz.Authenticated(owner), subscriptions.CreateInput{OrganizationID: orgID, RepositoryID: &repoID,
			URL: "https://runner.example.test/hook", EventTypes: []string{"issue_comment.created"},
			Retry: subscriptions.RetryPolicy{MaxAttempts: maxAttempts, InitialBackoff: time.Second, MaxBackoff: 10 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	clock := &manualClock{value: time.Now().UTC().Add(time.Second)}
	sender := &fakeSender{}
	service, err := delivery.New(pool, authorizer, subscriptionService, sender,
		delivery.Config{LeaseDuration: 2 * time.Second, MaxConcurrency: 2, PollInterval: 5 * time.Millisecond, Clock: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return &testEnvironment{pool: pool, scope: scope, owner: owner, subscription: created.Subscription,
		secret: created.Secret, subscriptions: subscriptionService, service: service, sender: sender, clock: clock}
}

func (e *testEnvironment) enqueue(t *testing.T, sequence int) uuid.UUID {
	payload, _ := json.Marshal(map[string]any{"schema_version": 1, "sequence": sequence})
	event, err := store.New(e.pool).ScopedRepo(e.scope).EnqueueEvent(t.Context(), models.NewOutboxEvent{
		ID: uuid.New(), SchemaVersion: 1, AggregateType: "comment", AggregateID: uuid.New(),
		EventType: "issue_comment.created", EventKey: fmt.Sprintf("event-%d-%s", sequence, uuid.New()),
		Payload: payload, AvailableAt: e.clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return event.ID
}

func (e *testEnvironment) expandAll(t *testing.T) {
	for {
		expanded, err := e.service.ExpandOne(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !expanded {
			return
		}
	}
}

func (e *testEnvironment) addMember(t *testing.T, login, role string) serverauth.Principal {
	id := uuid.New()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $2)`, id, login); err != nil {
		t.Fatal(err)
	}
	insertMembership(t, e.pool, e.scope.OrgID, id, role)
	return principal(t, e.pool, id, login)
}

func (e *testEnvironment) addUser(t *testing.T, login string) serverauth.Principal {
	id := uuid.New()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $2)`, id, login); err != nil {
		t.Fatal(err)
	}
	return principal(t, e.pool, id, login)
}

type sendOutcome struct {
	status     int
	header     http.Header
	err        error
	retryDate  time.Duration
	leakSecret bool
}

type fakeSender struct {
	mu        sync.Mutex
	outcomes  []sendOutcome
	calls     []networkpolicy.Request
	delay     time.Duration
	active    int
	maxActive int
}

func (s *fakeSender) Set(outcomes ...sendOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes = append([]sendOutcome(nil), outcomes...)
}

func (s *fakeSender) Send(_ context.Context, request networkpolicy.Request) (networkpolicy.Result, error) {
	s.mu.Lock()
	copyRequest := request
	copyRequest.Secret = append([]byte(nil), request.Secret...)
	copyRequest.Body = append([]byte(nil), request.Body...)
	s.calls = append(s.calls, copyRequest)
	outcome := sendOutcome{status: http.StatusOK}
	if len(s.outcomes) > 0 {
		outcome, s.outcomes = s.outcomes[0], s.outcomes[1:]
	}
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	delay := s.delay
	s.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	if outcome.leakSecret {
		return networkpolicy.Result{}, fmt.Errorf("transport echoed %s", request.Secret)
	}
	return networkpolicy.Result{StatusCode: outcome.status, Header: outcome.header}, outcome.err
}

func (s *fakeSender) CallCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.calls) }
func (s *fakeSender) SetDelay(delay time.Duration) {
	s.mu.Lock()
	s.delay = delay
	s.mu.Unlock()
}
func (s *fakeSender) MaxActive() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxActive
}
func (s *fakeSender) LastSecret() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.calls[len(s.calls)-1].Secret)
}
func (s *fakeSender) Sequences(t *testing.T) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]int, len(s.calls))
	for index, call := range s.calls {
		var payload struct {
			Sequence int `json:"sequence"`
		}
		if err := json.Unmarshal(call.Body, &payload); err != nil {
			t.Fatal(err)
		}
		result[index] = payload.Sequence
	}
	return result
}

type manualClock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *manualClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.value }
func (c *manualClock) Advance(value time.Duration) {
	c.mu.Lock()
	c.value = c.value.Add(value)
	c.mu.Unlock()
}
func (c *manualClock) Set(value time.Time) { c.mu.Lock(); c.value = value; c.mu.Unlock() }

func deliveryState(t *testing.T, pool *pgxpool.Pool, eventID uuid.UUID) string {
	state, _ := deliveryStateAndNext(t, pool, eventID)
	return state
}

func deliveryStateAndNext(t *testing.T, pool *pgxpool.Pool, eventID uuid.UUID) (string, time.Time) {
	var state string
	var next time.Time
	if err := pool.QueryRow(t.Context(), `SELECT state, next_attempt_at FROM webhook_deliveries WHERE event_id = $1`, eventID).Scan(&state, &next); err != nil {
		t.Fatal(err)
	}
	return state, next
}

func deliveryIDForEvent(t *testing.T, pool *pgxpool.Pool, eventID uuid.UUID) uuid.UUID {
	var id uuid.UUID
	if err := pool.QueryRow(t.Context(), `SELECT id FROM webhook_deliveries WHERE event_id = $1`, eventID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func rowCount(t *testing.T, pool *pgxpool.Pool, table string) int {
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func insertMembership(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID, role string) {
	if _, err := pool.Exec(t.Context(), `INSERT INTO org_memberships
		(organization_id, user_id, role, state, activated_at) VALUES ($1, $2, $3, 'active', clock_timestamp())`, orgID, userID, role); err != nil {
		t.Fatal(err)
	}
}

func principal(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, login string) serverauth.Principal {
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO sessions
		(id, user_id, token_prefix, token_hash, csrf_hash, idle_expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp() + interval '1 hour', clock_timestamp() + interval '2 hours')`,
		id, userID, "session-"+id.String(), []byte(id.String()), []byte("csrf")); err != nil {
		t.Fatal(err)
	}
	return serverauth.Principal{User: serverauth.User{ID: userID, Login: login, Status: "active"},
		Kind: serverauth.CredentialSession, CredentialID: id}
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
	schema := "delivery_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") })
	config, _ := pgxpool.ParseConfig(databaseURL)
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 64
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
