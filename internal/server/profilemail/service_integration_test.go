package profilemail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	serverstore "github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServiceVerificationLifecycleAndFoundationWorker(t *testing.T) {
	pool := profileMailPool(t)
	userID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name, email)
		VALUES ($1,$2,'Profile Person','provider-identity@example.test')`, userID, "profile-"+userID.String()); err != nil {
		t.Fatal(err)
	}
	secrets := testSecrets(t)
	clock := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	config := Config{VerificationTTL: 5 * time.Minute, ConfirmationURL: "https://issues.example.test/settings/email/confirm"}
	service, err := New(pool, secrets, config)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return clock }

	if _, err := service.Onboard(t.Context(), OnboardingInput{UserID: userID, PreferredName: "   ",
		Email: "invalid-name@example.test", ExpectedUserVersion: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty onboarding name = %v", err)
	}
	ordinary, err := service.Set(t.Context(), SetInput{UserID: userID, Email: "ordinary@example.test", ExpectedUserVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	var beforeOnboarding *time.Time
	var beforeNickname *string
	var beforeVersion int64
	if err := pool.QueryRow(t.Context(), `SELECT onboarding_completed_at, nickname, representation_version
		FROM users WHERE id = $1`, userID).Scan(&beforeOnboarding, &beforeNickname, &beforeVersion); err != nil {
		t.Fatal(err)
	}
	if beforeOnboarding != nil || beforeNickname != nil || beforeVersion != 2 {
		t.Fatalf("ordinary email bypassed onboarding: completed=%v nickname=%v version=%d", beforeOnboarding, beforeNickname, beforeVersion)
	}

	request, err := service.Onboard(t.Context(), OnboardingInput{UserID: userID, PreferredName: "  Preferred Person  ",
		Email: "notify@example.test", ExpectedUserVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	if request.PendingEmail != "notify@example.test" || request.RepresentationVersion != 1 {
		t.Fatalf("request = %+v", request)
	}
	if _, err := service.Set(t.Context(), SetInput{UserID: userID, Email: "stale@example.test", ExpectedUserVersion: 2}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale set = %v", err)
	}
	if _, err := service.Onboard(t.Context(), OnboardingInput{UserID: userID, PreferredName: "Overwrite Attempt",
		Email: "second-onboarding@example.test", ExpectedUserVersion: 3}); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed onboarding replay = %v", err)
	}

	var providerEmail string
	var notificationEmail *string
	var nickname *string
	var onboardingCompletedAt *time.Time
	var userVersion int64
	if err := pool.QueryRow(t.Context(), `SELECT email, notification_email, nickname, onboarding_completed_at, representation_version
		FROM users WHERE id = $1`, userID).Scan(&providerEmail, &notificationEmail, &nickname, &onboardingCompletedAt, &userVersion); err != nil {
		t.Fatal(err)
	}
	if providerEmail != "provider-identity@example.test" || notificationEmail != nil || nickname == nil || *nickname != "Preferred Person" || onboardingCompletedAt == nil || userVersion != 3 {
		t.Fatalf("unconfirmed profile provider=%q notification=%v nickname=%v onboarding=%v version=%d", providerEmail, notificationEmail, nickname, onboardingCompletedAt, userVersion)
	}
	var verificationCount int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM email_verification_requests WHERE user_id = $1`, userID).Scan(&verificationCount); err != nil {
		t.Fatal(err)
	}
	if verificationCount != 2 {
		t.Fatalf("transactional onboarding request count = %d, want ordinary plus onboarding only", verificationCount)
	}
	var ordinarySuperseded *time.Time
	if err := pool.QueryRow(t.Context(), `SELECT superseded_at FROM email_verification_requests WHERE id = $1`, ordinary.ID).Scan(&ordinarySuperseded); err != nil || ordinarySuperseded == nil {
		t.Fatalf("ordinary pending request was not atomically superseded: %v/%v", ordinarySuperseded, err)
	}

	var digest, ciphertext []byte
	var snapshot string
	if err := pool.QueryRow(t.Context(), `SELECT r.token_digest, r.token_ciphertext, d.render_snapshot::text
		FROM email_verification_requests r JOIN email_deliveries d ON d.verification_request_id = r.id
		WHERE r.id = $1`, request.ID).Scan(&digest, &ciphertext, &snapshot); err != nil {
		t.Fatal(err)
	}
	plaintext, err := secrets.Decrypt(tokenCipherPurpose(request.ID), ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	token := string(plaintext)
	if !bytes.Equal(digest, secrets.Digest(tokenPurpose, token)) || bytes.Contains(ciphertext, plaintext) {
		t.Fatal("verification token was not stored as digest plus opaque temporary ciphertext")
	}
	if strings.Contains(snapshot, token) || strings.Contains(snapshot, request.PendingEmail) || strings.Contains(snapshot, string(ciphertext)) {
		t.Fatalf("queue snapshot leaked verification data: %s", snapshot)
	}
	queue, err := emaildelivery.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := NewVerificationPreparer(pool, secrets, config)
	if err != nil {
		t.Fatal(err)
	}
	preparer.now = func() time.Time { return clock }
	sender := &recordingSender{errors: []error{emaildelivery.Retryable(emaildelivery.ReasonSMTPUnavailable)}}
	worker, err := emaildelivery.NewWorker(queue, preparer, sender, emaildelivery.WorkerConfig{Clock: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	var state string
	var reason *string
	if err := pool.QueryRow(t.Context(), `SELECT state, last_reason FROM email_deliveries
		WHERE verification_request_id = $1`, request.ID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || reason == nil || *reason != string(emaildelivery.ReasonSMTPUnavailable) {
		t.Fatalf("retry state = %q/%v", state, reason)
	}
	if err := pool.QueryRow(t.Context(), `SELECT token_ciphertext FROM email_verification_requests WHERE id = $1`, request.ID).
		Scan(&ciphertext); err != nil || len(ciphertext) == 0 {
		t.Fatalf("retry did not retain temporary token ciphertext: %d/%v", len(ciphertext), err)
	}

	clock = clock.Add(2 * time.Minute)
	if err := worker.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 2 || !strings.Contains(sender.messages[1].Body, token) || sender.messages[1].To != request.PendingEmail {
		t.Fatalf("sent messages = %+v", sender.messages)
	}
	if err := pool.QueryRow(t.Context(), `SELECT state FROM email_deliveries
		WHERE verification_request_id = $1`, request.ID).Scan(&state); err != nil || state != "succeeded" {
		t.Fatalf("success state = %q/%v", state, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT token_ciphertext FROM email_verification_requests WHERE id = $1`, request.ID).
		Scan(&ciphertext); err != nil || ciphertext != nil {
		t.Fatalf("successful delivery retained ciphertext: %x/%v", ciphertext, err)
	}

	confirmed, err := service.Confirm(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.NotificationEmail != request.PendingEmail || confirmed.RepresentationVersion != 4 {
		t.Fatalf("confirmed = %+v", confirmed)
	}
	if _, err := service.Confirm(t.Context(), token); !errors.Is(err, ErrConsumed) {
		t.Fatalf("confirmation replay = %v", err)
	}

	replacement, err := service.Set(t.Context(), SetInput{UserID: userID, Email: "replacement@example.test", ExpectedUserVersion: 4})
	if err != nil {
		t.Fatal(err)
	}
	var replacementCiphertext []byte
	if err := pool.QueryRow(t.Context(), `SELECT token_ciphertext FROM email_verification_requests WHERE id = $1`, replacement.ID).
		Scan(&replacementCiphertext); err != nil {
		t.Fatal(err)
	}
	replacementPlaintext, err := secrets.Decrypt(tokenCipherPurpose(replacement.ID), replacementCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Minute)
	resent, err := service.Resend(t.Context(), ResendInput{UserID: userID, ExpectedUserVersion: 5,
		ExpectedVerificationVersion: replacement.RepresentationVersion})
	if err != nil {
		t.Fatal(err)
	}
	if resent.ID == replacement.ID || resent.PendingEmail != replacement.PendingEmail {
		t.Fatalf("resent = %+v prior=%+v", resent, replacement)
	}
	if _, err := service.Confirm(t.Context(), string(replacementPlaintext)); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("superseded replacement token = %v", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT state FROM email_deliveries
		WHERE verification_request_id = $1`, replacement.ID).Scan(&state); err != nil || state != "suppressed" {
		t.Fatalf("superseded delivery = %q/%v", state, err)
	}

	if err := service.Remove(t.Context(), RemoveInput{UserID: userID, ExpectedUserVersion: 6}); err != nil {
		t.Fatal(err)
	}
	profile, err := service.Get(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.NotificationEmail != nil || profile.Pending != nil || profile.RepresentationVersion != 7 {
		t.Fatalf("removed profile = %+v", profile)
	}
	if err := pool.QueryRow(t.Context(), `SELECT email FROM users WHERE id = $1`, userID).Scan(&providerEmail); err != nil || providerEmail != "provider-identity@example.test" {
		t.Fatalf("provider email changed = %q/%v", providerEmail, err)
	}

	expiring, err := service.Set(t.Context(), SetInput{UserID: userID, Email: "expires@example.test", ExpectedUserVersion: 7})
	if err != nil {
		t.Fatal(err)
	}
	var expiringCiphertext []byte
	if err := pool.QueryRow(t.Context(), `SELECT token_ciphertext FROM email_verification_requests WHERE id = $1`, expiring.ID).
		Scan(&expiringCiphertext); err != nil {
		t.Fatal(err)
	}
	expiringToken, err := secrets.Decrypt(tokenCipherPurpose(expiring.ID), expiringCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(6 * time.Minute)
	if _, err := service.Confirm(t.Context(), string(expiringToken)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired confirmation = %v", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT token_ciphertext FROM email_verification_requests WHERE id = $1`, expiring.ID).
		Scan(&expiringCiphertext); err != nil || expiringCiphertext != nil {
		t.Fatalf("expired request retained ciphertext: %x/%v", expiringCiphertext, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT state FROM email_deliveries WHERE verification_request_id = $1`, expiring.ID).
		Scan(&state); err != nil || state != "suppressed" {
		t.Fatalf("expired delivery = %q/%v", state, err)
	}
}

func TestServiceRateLimitAndExpirySweep(t *testing.T) {
	pool := profileMailPool(t)
	secrets := testSecrets(t)
	clock := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	service, err := New(pool, secrets, Config{VerificationTTL: time.Minute, RateLimit: 1, RateWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return clock }
	userID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name, email)
		VALUES ($1,$2,'Limited Person','provider-only@example.test')`, userID, "limited-"+userID.String()); err != nil {
		t.Fatal(err)
	}
	request, err := service.Set(t.Context(), SetInput{UserID: userID, Email: "limited@example.test", ExpectedUserVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Set(t.Context(), SetInput{UserID: userID, Email: "second@example.test", ExpectedUserVersion: 2}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate limit = %v", err)
	}
	clock = clock.Add(2 * time.Minute)
	count, err := service.Expire(t.Context(), 10)
	if err != nil || count != 1 {
		t.Fatalf("expiry sweep = %d/%v", count, err)
	}
	var ciphertext []byte
	var superseded *time.Time
	var state string
	if err := pool.QueryRow(t.Context(), `SELECT r.token_ciphertext, r.superseded_at, d.state
		FROM email_verification_requests r JOIN email_deliveries d ON d.verification_request_id = r.id
		WHERE r.id = $1`, request.ID).Scan(&ciphertext, &superseded, &state); err != nil {
		t.Fatal(err)
	}
	if ciphertext != nil || superseded == nil || state != "suppressed" {
		t.Fatalf("expired request ciphertext=%x superseded=%v state=%q", ciphertext, superseded, state)
	}
}

func TestServiceEnforcesChangedAddressPolicyAcrossLifecycle(t *testing.T) {
	pool := profileMailPool(t)
	userID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name, email)
		VALUES ($1,$2,'Policy Person','provider-only@example.test')`, userID, "policy-"+userID.String()); err != nil {
		t.Fatal(err)
	}
	allowed, err := emaildelivery.NewAddressPolicy([]string{"corp.example"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(pool, testSecrets(t), Config{AddressPolicy: allowed, ResendInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Set(t.Context(), SetInput{UserID: userID, Email: "person@personal.example",
		ExpectedUserVersion: 1}); !errors.Is(err, ErrEmailDomainNotAllowed) {
		t.Fatalf("Set() error = %v", err)
	}
	if _, err := service.Onboard(t.Context(), OnboardingInput{UserID: userID, PreferredName: "Policy Person",
		Email: "person@personal.example", ExpectedUserVersion: 1}); !errors.Is(err, ErrEmailDomainNotAllowed) {
		t.Fatalf("Onboard() error = %v", err)
	}
	request, err := service.Set(t.Context(), SetInput{UserID: userID, Email: "person@team.corp.example",
		ExpectedUserVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := service.Get(t.Context(), userID)
	if err != nil || strings.Join(profile.AllowedEmailDomainSuffixes, ",") != "corp.example" {
		t.Fatalf("Get() profile = %+v err=%v", profile, err)
	}
	var ciphertext []byte
	if err := pool.QueryRow(t.Context(), `SELECT token_ciphertext FROM email_verification_requests WHERE id = $1`, request.ID).
		Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	token, err := service.secrets.Decrypt(tokenCipherPurpose(request.ID), ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	tightened, err := emaildelivery.NewAddressPolicy([]string{"other.example"})
	if err != nil {
		t.Fatal(err)
	}
	service.config.AddressPolicy = tightened
	if _, err := service.Resend(t.Context(), ResendInput{UserID: userID, ExpectedUserVersion: 2,
		ExpectedVerificationVersion: request.RepresentationVersion}); !errors.Is(err, ErrEmailDomainNotAllowed) {
		t.Fatalf("Resend() error = %v", err)
	}
	if _, err := service.Confirm(t.Context(), string(token)); !errors.Is(err, ErrEmailDomainNotAllowed) {
		t.Fatalf("Confirm() error = %v", err)
	}
	var consumedAt *time.Time
	var notificationEmail *string
	if err := pool.QueryRow(t.Context(), `SELECT r.consumed_at, u.notification_email
		FROM email_verification_requests r JOIN users u ON u.id = r.user_id WHERE r.id = $1`, request.ID).
		Scan(&consumedAt, &notificationEmail); err != nil {
		t.Fatal(err)
	}
	if consumedAt != nil || notificationEmail != nil {
		t.Fatalf("disallowed confirmation mutated state: consumed=%v email=%v", consumedAt, notificationEmail)
	}
}

func TestServiceEmptyAddressPolicyPreservesEnrollmentThroughVerifiedCompatibility(t *testing.T) {
	pool := profileMailPool(t)
	secrets := testSecrets(t)
	service, err := New(pool, secrets, Config{})
	if err != nil {
		t.Fatal(err)
	}

	for index, address := range []string{"person@例子.测试", "person@[192.0.2.1]"} {
		userID := uuid.New()
		if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name, email)
			VALUES ($1,$2,'Compatibility Person','provider-only@example.test')`, userID,
			fmt.Sprintf("compatibility-%d-%s", index, userID)); err != nil {
			t.Fatal(err)
		}
		request, err := service.Onboard(t.Context(), OnboardingInput{UserID: userID,
			PreferredName: "Compatibility Person", Email: address, ExpectedUserVersion: 1})
		if err != nil {
			t.Fatalf("Onboard(%q) error = %v", address, err)
		}
		pending, err := service.Get(t.Context(), userID)
		if err != nil || pending.OnboardingCompletedAt == nil || pending.Pending == nil ||
			pending.Pending.PendingEmail != address || pending.NotificationEmail != nil {
			t.Fatalf("pending profile for %q = %+v err=%v", address, pending, err)
		}
		var ciphertext []byte
		if err := pool.QueryRow(t.Context(), `SELECT token_ciphertext FROM email_verification_requests WHERE id = $1`, request.ID).
			Scan(&ciphertext); err != nil {
			t.Fatal(err)
		}
		token, err := secrets.Decrypt(tokenCipherPurpose(request.ID), ciphertext)
		if err != nil {
			t.Fatal(err)
		}
		confirmed, err := service.Confirm(t.Context(), string(token))
		if err != nil || confirmed.NotificationEmail != address {
			t.Fatalf("Confirm(%q) = %+v err=%v", address, confirmed, err)
		}
		verified, err := service.Get(t.Context(), userID)
		if err != nil || verified.Pending != nil || verified.NotificationEmail == nil ||
			*verified.NotificationEmail != address || verified.NotificationVerifiedAt == nil {
			t.Fatalf("verified profile for %q = %+v err=%v", address, verified, err)
		}
	}
}

func profileMailPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL")
	}
	admin, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "profile_mail_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") })
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
	if err := serverstore.RunMigrations(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
