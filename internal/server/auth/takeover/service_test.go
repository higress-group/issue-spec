package takeover

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/recovery"
	"github.com/higress-group/issue-spec/internal/server/auth/session"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExchangeIsAtomicOneTimeAndCreatesSession(t *testing.T) {
	pool := takeoverPool(t)
	secrets, err := serverauth.NewSecrets([]byte(strings.Repeat("p", 32)), []byte(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, 'admin', 'Admin')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO site_role_assignments (id, user_id, role)
		VALUES ($1, $2, 'site_admin')`, uuid.New(), userID); err != nil {
		t.Fatal(err)
	}
	recoveryService := recovery.New(pool, secrets)
	sessionService, err := session.New(pool, secrets, session.Config{Secure: true, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(pool, recoveryService, sessionService)
	if err != nil {
		t.Fatal(err)
	}

	createdRecovery, err := recoveryService.Mint(t.Context(), userID, "test", "takeover", "req-mint", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	createdSession, err := service.Exchange(t.Context(), createdRecovery.Plaintext, "req-exchange", "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if principal, err := sessionService.Authenticate(t.Context(), createdSession.Token); err != nil || principal.User.ID != userID {
		t.Fatalf("authenticate exchanged session = %+v, %v", principal, err)
	}
	if _, err := service.Exchange(t.Context(), createdRecovery.Plaintext, "req-replay", "", ""); !errors.Is(err, serverauth.ErrInvalidCredential) {
		t.Fatalf("replay error = %v", err)
	}

	rollbackRecovery, err := recoveryService.Mint(t.Context(), userID, "test", "rollback", "req-rollback-mint", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	failing, err := New(pool, recoveryService, failingSessionCreator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.Exchange(t.Context(), rollbackRecovery.Plaintext, "req-rollback", "", ""); err == nil {
		t.Fatal("injected session failure unexpectedly succeeded")
	}
	if _, err := service.Exchange(t.Context(), rollbackRecovery.Plaintext, "req-after-rollback", "", ""); err != nil {
		t.Fatalf("credential was consumed despite transaction rollback: %v", err)
	}

	concurrentRecovery, err := recoveryService.Mint(t.Context(), userID, "test", "concurrent", "req-concurrent-mint", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.Exchange(t.Context(), concurrentRecovery.Plaintext, uuid.NewString(), "", "")
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	successes, rejected := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, serverauth.ErrInvalidCredential) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent exchange error: %v", err)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("concurrent exchange successes=%d rejected=%d", successes, rejected)
	}
}

type failingSessionCreator struct{}

func (failingSessionCreator) CreateInTx(context.Context, pgx.Tx, uuid.UUID, string, string) (session.Created, error) {
	return session.Created{}, errors.New("injected session failure")
}

func takeoverPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	adminPool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := "takeover_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 8
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
